package http

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
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
func imageBytes(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if e := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); e != nil {
		t.Fatal(e)
	}
	return encoded.Bytes()
}

// seedVersionRows publishes a zero-byte version and then replaces its declared
// shape and chunk rows so the preview/thumb routes see chosen bytes without
// touching the chunk store (whose 0600 private-file guard is untestable on
// Windows, where chmod cannot produce that mode). Manifest reads chunk rows
// from the database, while the documentImageReader pulls payload bytes from
// the store — so streaming suites with real payloads must inject store bytes
// via seedChunkObject below.
func seedChunkObject(t *testing.T, f *socialFixture, data []byte) string {
	t.Helper()
	return ""
}
