package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/linknestx/linknestx/server/internal/files"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func (f *socialFixture) publish(t *testing.T, id string, data []byte) files.FileVersion {
	t.Helper()
	ctx, e := f.a.AuthenticateContext(context.Background(), f.alice.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	chunks := []files.Chunk{}
	if len(data) > 0 {
		chunks = append(chunks, files.Chunk{Index: 0, Hash: hash, Size: int64(len(data))})
	}
	u, e := f.fs.StartUpload(ctx, f.alice.User.ID, files.StartUploadInput{GroupID: id, Name: "测试文件.txt", Size: int64(len(data)), SHA256: hash, Chunks: chunks})
	if e != nil {
		t.Fatal(e)
	}
	if len(data) > 0 {
		if e = f.fs.PutChunk(ctx, f.alice.User.ID, u.UploadID, 0, bytes.NewReader(data)); e != nil {
			t.Fatal(e)
		}
	}
	v, e := f.fs.CompleteUpload(ctx, f.alice.User.ID, u.UploadID)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func (f *socialFixture) web(t *testing.T) *http.Cookie {
	t.Helper()
	r := httptest.NewRequest("POST", "http://example.com/api/v1/web/login", strings.NewReader(`{"username":"bob","password":"password123"}`))
	r.Header.Set("Origin", "http://example.com")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	expectCode(t, w, 200)
	return w.Result().Cookies()[0]
}

type blockedDownload struct {
	*httptest.ResponseRecorder
	once             sync.Once
	entered, release chan struct{}
}

func (b *blockedDownload) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.ResponseRecorder.Write(p)
}
func TestWebDownloadStreamsBoundsConcurrentSlotAndRevocation(t *testing.T) {
	f := newSocialFixture(t)
	id := f.direct(t)
	data := bytes.Repeat([]byte("a"), 128*1024)
	v := f.publish(t, id, data)
	cookie := f.web(t)
	path := "http://example.com/api/v1/web/files/" + v.VersionID + "/download"
	req := func(method string) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(cookie)
		return r
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req("GET"))
	expectCode(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), data) || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("stream mismatch")
	}
	f.a.Database().Exec(`UPDATE file_versions SET size=1000000001 WHERE id=?`, v.VersionID)
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req("GET"))
	expectCode(t, w, 413)
	f.a.Database().Exec(`UPDATE file_versions SET size=1000000000 WHERE id=?`, v.VersionID)
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req("HEAD"))
	expectCode(t, w, 200)
	if w.Body.Len() != 0 {
		t.Fatal("HEAD transferred data")
	}
	f.a.Database().Exec(`UPDATE file_versions SET size=? WHERE id=?`, len(data), v.VersionID)
	b := &blockedDownload{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); f.router.ServeHTTP(b, req("GET")) }()
	<-b.entered
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req("GET"))
	expectCode(t, w, 409)
	// A permission revocation during the first write prevents subsequent buffers.
	f.a.Database().Exec(`UPDATE users SET disabled=1 WHERE id=?`, f.bob.User.ID)
	close(b.release)
	<-done
	if b.Body.Len() >= len(data) {
		t.Fatal("revoked download continued")
	}
	f.a.Database().Exec(`UPDATE users SET disabled=0 WHERE id=?`, f.bob.User.ID)
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req("GET"))
	expectCode(t, w, 200)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := req("GET").WithContext(ctx)
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req("GET"))
	expectCode(t, w, 200)
}

func TestWebDownloadZeroRangeAndMissingFirstChunk(t *testing.T) {
	f := newSocialFixture(t)
	id := f.direct(t)
	empty := f.publish(t, id, nil)
	cookie := f.web(t)
	get := func(version, rangeValue string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://example.com/api/v1/web/files/"+version+"/download", nil)
		r.AddCookie(cookie)
		if rangeValue != "" {
			r.Header.Set("Range", rangeValue)
		}
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, r)
		return w
	}
	w := get(empty.VersionID, "")
	expectCode(t, w, 200)
	if w.Body.Len() != 0 {
		t.Fatal("zero file has content")
	}
	expectCode(t, get(empty.VersionID, "bytes=0-1"), 416)
	v := f.publish(t, id, []byte("missing"))
	f.a.Database().Exec(`UPDATE file_chunks SET hash='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE version_id=?`, v.VersionID)
	expectCode(t, get(v.VersionID, ""), 404)
}

// Session changes occur after the first real payload buffer; no wall-clock
// sleeps are needed to exercise downloads spanning the native access deadline.
type sessionBoundaryDownload struct {
	*httptest.ResponseRecorder
	afterFirstWrite func()
}

func (w *sessionBoundaryDownload) Write(p []byte) (int, error) {
	n, e := w.ResponseRecorder.Write(p)
	if w.afterFirstWrite != nil {
		w.afterFirstWrite()
		w.afterFirstWrite = nil
	}
	return n, e
}

func TestWebDownloadUsesBrowserSessionLifetimeAcrossBuffersAndChunks(t *testing.T) {
	for _, scenario := range []string{"valid_after_sixteen_minutes", "session_expired", "session_revoked", "account_disabled", "membership_removed", "file_expired"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSocialFixture(t)
			id := f.direct(t)
			data := bytes.Repeat([]byte("a"), 128*1024)
			ctx, e := f.a.AuthenticateContext(context.Background(), f.alice.AccessToken)
			if e != nil {
				t.Fatal(e)
			}
			whole := sha256.Sum256(data)
			part := sha256.Sum256(data[:64*1024])
			hash := hex.EncodeToString(part[:])
			u, e := f.fs.StartUpload(ctx, f.alice.User.ID, files.StartUploadInput{GroupID: id, Name: "long-download.bin", Size: int64(len(data)), SHA256: hex.EncodeToString(whole[:]), ChunkSize: 64 * 1024, Chunks: []files.Chunk{{Index: 0, Hash: hash, Size: 64 * 1024}, {Index: 1, Hash: hash, Size: 64 * 1024}}})
			if e != nil {
				t.Fatal(e)
			}
			for index := 0; index < 2; index++ {
				if e = f.fs.PutChunk(ctx, f.alice.User.ID, u.UploadID, index, bytes.NewReader(data[index*64*1024:(index+1)*64*1024])); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = f.fs.CompleteUpload(ctx, f.alice.User.ID, u.UploadID); e != nil {
				t.Fatal(e)
			}
			now := time.Now()
			f.a.SetClock(func() time.Time { return now })
			cookie := f.web(t)
			_, session, e := f.a.AuthenticateWeb(context.Background(), cookie.Value)
			if e != nil {
				t.Fatal(e)
			}
			r := httptest.NewRequest("GET", "http://example.com/api/v1/web/files/"+u.VersionID+"/download", nil)
			r.AddCookie(cookie)
			w := &sessionBoundaryDownload{ResponseRecorder: httptest.NewRecorder(), afterFirstWrite: func() {
				now = now.Add(16 * time.Minute)
				switch scenario {
				case "session_expired":
					now = session.ExpiresAt.Add(time.Second)
				case "session_revoked":
					e = f.a.RevokeSession(context.Background(), session.ID)
				case "account_disabled":
					_, e = f.a.Database().Exec(`UPDATE users SET disabled=1 WHERE id=?`, f.bob.User.ID)
				case "membership_removed":
					_, e = f.a.Database().Exec(`DELETE FROM group_members WHERE group_id=? AND user_id=?`, id, f.bob.User.ID)
				case "file_expired":
					_, e = f.a.Database().Exec(`UPDATE file_versions SET expires_at='2000-01-01T00:00:00Z' WHERE id=?`, u.VersionID)
				}
				if e != nil {
					t.Fatal(e)
				}
			}}
			f.router.ServeHTTP(w, r)
			if scenario == "valid_after_sixteen_minutes" {
				if !bytes.Equal(w.Body.Bytes(), data) {
					t.Fatalf("valid session stream truncated: %d/%d bytes", w.Body.Len(), len(data))
				}
				sessionRequest := httptest.NewRequest("GET", "http://example.com/api/v1/web/session", nil)
				sessionRequest.AddCookie(cookie)
				out := httptest.NewRecorder()
				f.router.ServeHTTP(out, sessionRequest)
				expectCode(t, out, 200)
			} else if w.Body.Len() != 32*1024 {
				t.Fatalf("%s did not stop after first buffer: %d bytes", scenario, w.Body.Len())
			}
		})
	}
}
