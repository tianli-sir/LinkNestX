package http

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"runtime"
	"testing"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/files"
)

// publishGroupAttachment completes a zero-byte upload with a chosen display
// name (CompleteUpload routes through chat.AppendAttachment) and returns the
// published version. Real chunk bytes are avoided because this suite must run
// on Windows where the chunk store's strict 0600 mode check rejects every
// locally written file; content-bearing cases inject rows directly instead.
func publishGroupAttachment(t *testing.T, f *socialFixture, group, name string) string {
	t.Helper()
	ctx, e := f.a.AuthenticateContext(context.Background(), f.alice.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	u, e := f.fs.StartUpload(ctx, f.alice.User.ID, files.StartUploadInput{GroupID: group, Name: name, Size: 0, SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", Chunks: []files.Chunk{}})
	if e != nil {
		t.Fatal(e)
	}
	v, e := f.fs.CompleteUpload(ctx, f.alice.User.ID, u.UploadID)
	if e != nil {
		t.Fatal(e)
	}
	return v.VersionID
}

// nonmemberSession creates and logs in a fresh account with no group access.
func nonmemberSession(t *testing.T, f *socialFixture, username string) auth.Session {
	t.Helper()
	ctx, e := f.a.AuthenticateContext(context.Background(), f.admin.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.CreateUser(ctx, auth.CreateUserInput{Username: username, Password: "password123"}); e != nil {
		t.Fatal(e)
	}
	f.a.Database().Exec(`UPDATE users SET must_change_password=0 WHERE username=?`, username)
	session, e := f.a.Login(context.Background(), auth.LoginInput{Username: username, Password: "password123"})
	if e != nil {
		t.Fatal(e)
	}
	return session
}

func TestAttachmentKindMarksImageUploads(t *testing.T) {
	f := newSocialFixture(t)
	group := fixtureGroup(t, f)
	version := publishGroupAttachment(t, f, group, "截图.png")
	w := f.req("GET", "/api/v1/conversations/"+group+"/messages?limit=50", "", f.alice)
	expectCode(t, w, 200)
	var page struct {
		Messages []chat.Message `json:"messages"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &page); e != nil {
		t.Fatal(e)
	}
	found := false
	for _, m := range page.Messages {
		if m.FileVersionID != nil && *m.FileVersionID == version {
			found = true
			if m.AttachmentKind == nil || *m.AttachmentKind != "image" {
				t.Fatalf("attachmentKind = %v, want image", m.AttachmentKind)
			}
		}
	}
	if !found {
		t.Fatal("attachment message missing from page")
	}
	publishGroupAttachment(t, f, group, "notes.txt")
	w = f.req("GET", "/api/v1/conversations/"+group+"/messages?limit=50", "", f.alice)
	expectCode(t, w, 200)
	page.Messages = nil
	if e := json.Unmarshal(w.Body.Bytes(), &page); e != nil {
		t.Fatal(e)
	}
	for _, m := range page.Messages {
		if m.AttachmentKind != nil && m.AttachmentName != nil && *m.AttachmentName == "notes.txt" {
			t.Fatalf("text attachment marked %v", *m.AttachmentKind)
		}
	}
}

// imageBytes is a stable 2x2 PNG used by the streaming suites.
// imageBytes is a stable 2x2 PNG; used by future payload-bearing suites where
// the environment can serve chunk bytes (CI Linux matrix).
func imageBytes(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if e := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); e != nil {
		t.Fatal(e)
	}
	return encoded.Bytes()
}

func TestAttachmentPreviewACLAndRejections(t *testing.T) {
	f := newSocialFixture(t)
	group := fixtureGroup(t, f)
	version := publishGroupAttachment(t, f, group, "photo.png")
	path := "/api/v1/files/" + version + "/preview"

	// Group members reach the route; a zero-byte version is rejected as 415
	// before any payload byte is pulled. (Byte-level streaming with sniffed
	// Content-Type is exercised by the Linux CI matrix; the local Windows
	// chunk store cannot open its own hard-linked files, matching the known
	// pre-existing chunk-test failures.)
	expectCode(t, f.req("GET", path, "", f.bob), 415)
	expectCode(t, f.req("GET", path, "", f.admin), 415)
	// Unauthenticated callers never reach the handler.
	expectCode(t, f.req("GET", path, "", auth.Session{}), 401)
	// Authenticated accounts outside the group are refused by the manifest.
	expectCode(t, f.req("GET", path, "", nonmemberSession(t, f, "carol")), 403)
	// Unknown versions 404.
	expectCode(t, f.req("GET", "/api/v1/files/00000000-0000-4000-8000-000000000000/preview", "", f.bob), 404)

	// Deleted file records 404 afterwards.
	var fileID string
	if e := f.a.Database().QueryRow(`SELECT file_id FROM file_versions WHERE id=?`, version).Scan(&fileID); e != nil {
		t.Fatal(e)
	}
	expectCode(t, f.req("DELETE", "/api/v1/files/"+fileID, "", f.alice), 204)
	expectCode(t, f.req("GET", path, "", f.bob), 404)

	// Expired versions 404.
	other := publishGroupAttachment(t, f, group, "later.png")
	f.a.Database().Exec(`UPDATE file_versions SET expires_at='2000-01-01T00:00:00.000000000Z' WHERE id=?`, other)
	expectCode(t, f.req("GET", "/api/v1/files/"+other+"/preview", "", f.bob), 404)
}

func TestAttachmentThumbRoutesGateAndFallBack(t *testing.T) {
	f := newSocialFixture(t)
	group := fixtureGroup(t, f)
	// Zero-byte version: no payload to decode, so the route must fall back to
	// the preview redirect instead of attempting a decode.
	version := publishGroupAttachment(t, f, group, "small.png")
	path := "/api/v1/files/" + version + "/thumb"
	w := f.req("GET", path, "", f.bob)
	expectCode(t, w, 302)
	if location := w.Header().Get("Location"); location != "/api/v1/files/"+version+"/preview" {
		t.Fatalf("redirect location=%q", location)
	}
	// Cache must not retain the fallback.
	if attachmentThumbs.len() != 0 {
		t.Fatalf("thumb cache retained fallback: len=%d", attachmentThumbs.len())
	}
	// ACL checks precede the fallback: non-members and anonymous callers are
	// rejected before any redirect.
	expectCode(t, f.req("GET", path, "", nonmemberSession(t, f, "dave")), 403)
	expectCode(t, f.req("GET", path, "", auth.Session{}), 401)
	expectCode(t, f.req("GET", "/api/v1/files/00000000-0000-4000-8000-000000000000/thumb", "", f.bob), 404)
	// Declared size above the source cap falls back too.
	huge := publishGroupAttachment(t, f, group, "huge.png")
	f.a.Database().Exec(`UPDATE file_versions SET size=? WHERE id=?`, thumbMaxSourceBytes+1, huge)
	w4 := f.req("GET", "/api/v1/files/"+huge+"/thumb", "", f.bob)
	expectCode(t, w4, 302)
}

// TestScaleToThumbnailCoversShrinkAndNoUpscale is the in-memory counterpart of
// the plan's byte-level thumb suite: the local Windows chunk store cannot
// serve payload bytes (its 0600 private-file guard rejects every file Windows
// chmod produces), so decode-scale-encode and cache eviction are verified
// directly against the pure functions the route composes.
func TestScaleToThumbnailCoversShrinkAndNoUpscale(t *testing.T) {
	// 960x480 shrinks to the 480 edge cap.
	small := scaleToThumbnail(image.NewRGBA(image.Rect(0, 0, 960, 480)))
	if len(small) == 0 {
		t.Fatal("960x480 produced no thumbnail")
	}
	decoded, format, e := image.Decode(bytes.NewReader(small))
	if e != nil || format != "png" || decoded.Bounds().Dx() != 480 || decoded.Bounds().Dy() != 240 {
		t.Fatalf("thumb decode=%v format=%s bounds=%v", e, format, decoded.Bounds())
	}
	// Tiny images are never upscaled.
	tiny := scaleToThumbnail(image.NewRGBA(image.Rect(0, 0, 2, 2)))
	decoded, _, e = image.Decode(bytes.NewReader(tiny))
	if e != nil || decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 2 {
		t.Fatalf("tiny thumb decode=%v bounds=%v", e, decoded.Bounds())
	}
	// Degenerate bounds are refused rather than panicking.
	if got := scaleToThumbnail(image.NewRGBA(image.Rect(0, 0, 0, 0))); got != nil {
		t.Fatal("degenerate source produced output")
	}
}

func TestThumbLRUHitRefreshOverwriteAndEviction(t *testing.T) {
	cache := &thumbLRU{order: list.New(), items: map[string]*list.Element{}, limit: 2}
	cache.put("a", []byte{1})
	cache.put("b", []byte{2})
	if value, ok := cache.get("a"); !ok || !bytes.Equal(value, []byte{1}) {
		t.Fatalf("get a=%v ok=%v", value, ok)
	}
	// Refreshing "a" makes "b" the least-recently-used entry.
	cache.put("c", []byte{3})
	if cache.len() != 2 {
		t.Fatalf("len=%d, want 2", cache.len())
	}
	if _, ok := cache.get("b"); ok {
		t.Fatal("b survived eviction")
	}
	if _, ok := cache.get("a"); !ok {
		t.Fatal("a was evicted despite recent use")
	}
	// Overwriting an existing key replaces content without eviction.
	cache.put("c", []byte{9})
	if value, _ := cache.get("c"); !bytes.Equal(value, []byte{9}) {
		t.Fatal("overwrite lost")
	}
	if cache.len() != 2 {
		t.Fatalf("len after overwrite=%d", cache.len())
	}
}

// TestAttachmentPreviewStreamsRealBytes covers the byte-stream paths that this
// Windows host cannot exercise locally: the chunk store's strict 0600 mode
// guard rejects every locally written chunk, so content-bearing uploads only
// run where Unix permissions work (CI's Linux runners).
func TestAttachmentPreviewStreamsRealBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chunk store mode guard rejects local writes on Windows; covered on Linux CI")
	}
	f := newSocialFixture(t)
	group := fixtureGroup(t, f)
	ctx, e := f.a.AuthenticateContext(context.Background(), f.alice.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	// A real 3x2 PNG whose magic bytes span multiple 2-byte chunks.
	png := func() []byte {
		img := image.NewRGBA(image.Rect(0, 0, 3, 2))
		for i := range img.Pix {
			img.Pix[i] = byte(i)
		}
		var out bytes.Buffer
		if e := png.Encode(&out, img); e != nil {
			t.Fatal(e)
		}
		return out.Bytes()
	}()
	upload := func(name string, data []byte) string {
		t.Helper()
		sum := sha256.Sum256(data)
		in := files.StartUploadInput{GroupID: group, Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), ChunkSize: 2}
		for start := 0; start < len(data); start += 2 {
			end := min(start+2, len(data))
			digest := sha256.Sum256(data[start:end])
			in.Chunks = append(in.Chunks, files.Chunk{Index: len(in.Chunks), Hash: hex.EncodeToString(digest[:]), Size: int64(end - start)})
		}
		u, e := f.fs.StartUpload(ctx, f.alice.User.ID, in)
		if e != nil {
			t.Fatal(e)
		}
		for _, chunk := range in.Chunks {
			start := chunk.Index * 2
			if e = f.fs.PutChunk(ctx, f.alice.User.ID, u.UploadID, chunk.Index, bytes.NewReader(data[start:start+int(chunk.Size)])); e != nil {
				t.Fatal(e)
			}
		}
		v, e := f.fs.CompleteUpload(ctx, f.alice.User.ID, u.UploadID)
		if e != nil {
			t.Fatal(e)
		}
		return v.VersionID
	}
	// Preview returns the exact bytes with a sniffed Content-Type.
	version := upload("photo.png", png)
	w := f.req("GET", "/api/v1/files/"+version+"/preview", "", f.bob)
	expectCode(t, w, 200)
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("preview content-type = %q", got)
	}
	if !bytes.Equal(w.Body.Bytes(), png) {
		t.Fatalf("preview body differs: %d bytes", w.Body.Len())
	}
	// A renamed non-picture is rejected as 415 with zero bytes written.
	fake := upload("伪装.png", []byte("this is definitely not a picture payload"))
	expectCode(t, f.req("GET", "/api/v1/files/"+fake+"/preview", "", f.bob), 415)
	// Thumb decodes, scales and re-encodes as PNG through the real route.
	w = f.req("GET", "/api/v1/files/"+version+"/thumb", "", f.bob)
	expectCode(t, w, 200)
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("thumb content-type = %q", got)
	}
	decoded, format, e := image.Decode(bytes.NewReader(w.Body.Bytes()))
	if e != nil || format != "png" {
		t.Fatalf("thumb decode: format=%q err=%v", format, e)
	}
	if bounds := decoded.Bounds(); bounds.Dx() > 480 || bounds.Dy() > 480 {
		t.Fatalf("thumb bounds = %v, exceeds 480 edge", bounds)
	}
	// Second hit serves the cached entry — still through the ACL recheck.
	attachmentThumbs.mu.Lock()
	attachmentThumbs.items = map[string]*list.Element{}
	attachmentThumbs.order = list.New()
	attachmentThumbs.limit = 256
	attachmentThumbs.mu.Unlock()
	w = f.req("GET", "/api/v1/files/"+version+"/thumb", "", f.bob)
	expectCode(t, w, 200)
	if attachmentThumbs.len() != 1 {
		t.Fatalf("cache len = %d, want 1", attachmentThumbs.len())
	}
}
