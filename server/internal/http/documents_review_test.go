package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/audit"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/files"
	"github.com/linknestx/linknestx/server/internal/notes"
)

func createDocumentForTest(t *testing.T, f *socialFixture, actor auth.Session, visibility, status string) notes.Document {
	t.Helper()
	w := f.req("POST", "/api/v1/documents", fmt.Sprintf(`{"title":"Guide","body":"keep this text","visibility":%q,"status":%q}`, visibility, status), actor)
	expectCode(t, w, 201)
	var d notes.Document
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func uploadDocumentForTest(t *testing.T, f *socialFixture, documentID string, data []byte) files.UploadSession {
	t.Helper()
	ctx, err := f.a.AuthenticateContext(context.Background(), f.alice.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	u, err := f.fs.StartUpload(ctx, f.alice.User.ID, files.StartUploadInput{DocumentID: documentID, Name: "guide.bin", Size: int64(len(data)), SHA256: hash, ChunkSize: files.DefaultChunkSize, Chunks: []files.Chunk{{Index: 0, Hash: hash, Size: int64(len(data))}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.fs.PutChunk(ctx, f.alice.User.ID, u.UploadID, 0, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.fs.CompleteUpload(ctx, f.alice.User.ID, u.UploadID); err != nil {
		t.Fatal(err)
	}
	return u
}

type documentChunkWriter struct {
	*httptest.ResponseRecorder
	afterFirstWrite func()
	writes          int
	deadlines       []time.Time
}

func (w *documentChunkWriter) SetWriteDeadline(at time.Time) error {
	w.deadlines = append(w.deadlines, at)
	return nil
}
func (w *documentChunkWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(data)
	w.writes++
	if w.writes == 1 && w.afterFirstWrite != nil {
		w.afterFirstWrite()
	}
	return n, err
}

func TestDocumentChunkStopsAfterAccessRevocation(t *testing.T) {
	for _, change := range []string{"friendship", "private", "document-deleted", "file-deleted", "session-revoked"} {
		t.Run(change, func(t *testing.T) {
			f := newSocialFixture(t)
			f.direct(t)
			d := createDocumentForTest(t, f, f.alice, "friends", "published")
			data := bytes.Repeat([]byte("chunk"), 80*1024)
			u := uploadDocumentForTest(t, f, d.ID, data)
			hash := sha256.Sum256(data)
			path := "/api/v1/files/" + u.VersionID + "/chunks/" + hex.EncodeToString(hash[:])
			request := func(task string) *http.Request {
				r := httptest.NewRequest("GET", path, nil)
				r.Header.Set("Authorization", "Bearer "+f.bob.AccessToken)
				r.Header.Set("X-Transfer-ID", task)
				return r
			}
			w := &documentChunkWriter{ResponseRecorder: httptest.NewRecorder()}
			f.router.ServeHTTP(w, request("complete"))
			if !bytes.Equal(w.Body.Bytes(), data) {
				t.Fatal("authorized download changed bytes")
			}
			var delivered int64
			if err := f.a.Database().QueryRow(`SELECT COALESCE(sum(bytes),0) FROM transfer_records WHERE task_id='complete'`).Scan(&delivered); err != nil || delivered != int64(len(data)) {
				t.Fatalf("complete delivery accounting: %d %v", delivered, err)
			}
			w = &documentChunkWriter{ResponseRecorder: httptest.NewRecorder(), afterFirstWrite: func() {
				switch change {
				case "friendship":
					expectCode(t, f.req("DELETE", "/api/v1/contacts/"+f.alice.User.ID, "", f.bob), 204)
				case "private":
					expectCode(t, f.req("PATCH", "/api/v1/documents/"+d.ID, `{"revision":1,"title":"Private","body":"keep text","visibility":"private","status":"published"}`, f.alice), 200)
				case "document-deleted":
					expectCode(t, f.req("DELETE", "/api/v1/documents/"+d.ID+"?revision=1", "", f.alice), 204)
				case "file-deleted":
					expectCode(t, f.req("DELETE", "/api/v1/files/"+u.FileID, "", f.alice), 204)
				case "session-revoked":
					if err := f.a.RevokeSession(context.Background(), f.bob.ID); err != nil {
						t.Fatal(err)
					}
				}
			}}
			f.router.ServeHTTP(w, request("revoked"))
			if w.Body.Len() > 32*1024 || w.Body.Len() == 0 || w.writes != 1 {
				t.Fatalf("stream leaked bytes after the first authorized buffer: %d bytes across %d writes", w.Body.Len(), w.writes)
			}
			if err := f.a.Database().QueryRow(`SELECT COALESCE(sum(bytes),0) FROM transfer_records WHERE task_id='revoked'`).Scan(&delivered); err != nil || delivered != 0 {
				t.Fatalf("partial response recorded as completed: %d %v", delivered, err)
			}
			if len(w.deadlines) < 2 || w.deadlines[0].IsZero() || !w.deadlines[len(w.deadlines)-1].IsZero() {
				t.Fatal("stream must bound writes and restore the deadline")
			}
		})
	}
}

func TestDocumentWritesRequireExplicitFields(t *testing.T) {
	f := newSocialFixture(t)
	d := createDocumentForTest(t, f, f.alice, "private", "draft")
	for _, method := range []string{"POST", "PATCH"} {
		for _, field := range []string{"title", "body", "visibility", "status"} {
			for _, missing := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/missing=%v", method, field, missing), func(t *testing.T) {
					body := map[string]any{"title": "Guide", "body": "replacement", "visibility": "private", "status": "draft", "revision": 1}
					if missing {
						delete(body, field)
					} else {
						body[field] = nil
					}
					raw, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					path := "/api/v1/documents"
					if method == "PATCH" {
						path += "/" + d.ID
					}
					expectCode(t, f.req(method, path, string(raw), f.alice), 400)
				})
			}
		}
	}
	w := f.req("GET", "/api/v1/documents/"+d.ID, "", f.alice)
	expectCode(t, w, 200)
	var unchanged notes.Document
	if err := json.Unmarshal(w.Body.Bytes(), &unchanged); err != nil || unchanged.Body != d.Body || unchanged.Revision != d.Revision {
		t.Fatalf("invalid updates changed note: %+v %v", unchanged, err)
	}
	for _, method := range []string{"POST", "PATCH"} {
		path := "/api/v1/documents"
		if method == "PATCH" {
			path += "/" + d.ID
		}
		w = f.req(method, path, `{"title":"Empty body","body":"","visibility":"private","status":"draft","revision":1}`, f.alice)
		want := 201
		if method == "PATCH" {
			want = 200
		}
		expectCode(t, w, want)
	}
}

func TestDocumentAuditRespectsCurrentVisibility(t *testing.T) {
	f := newSocialFixture(t)
	private := createDocumentForTest(t, f, f.alice, "private", "published")
	draft := createDocumentForTest(t, f, f.alice, "team", "draft")
	friend := createDocumentForTest(t, f, f.alice, "friends", "published")
	team := createDocumentForTest(t, f, f.alice, "team", "published")
	owned := createDocumentForTest(t, f, f.admin, "private", "draft")
	attachment := uploadDocumentForTest(t, f, private.ID, []byte("secret file"))
	expectCode(t, f.req("GET", "/api/v1/web/files/"+attachment.VersionID+"/download", "", f.alice), 200)
	ctx, err := f.a.AuthenticateContext(context.Background(), f.alice.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err = audit.NewService(f.a.Database(), f.a).RecordClientTransfer(ctx, audit.TransferInput{TaskID: uuid.NewString(), VersionID: attachment.VersionID, NodeID: "local", Action: "download.completed", Bytes: int64(len("secret file"))}); err != nil {
		t.Fatal(err)
	}
	check := func(allowed []string, denied []string) {
		t.Helper()
		w := f.req("GET", "/api/v1/admin/audit?action=document.created&limit=1", "", f.admin)
		expectCode(t, w, 200)
		var page audit.Page
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Total != int64(len(allowed)) || len(page.Events) != 1 {
			t.Fatalf("filtered pagination total: %+v %v", page, err)
		}
		for _, path := range []string{"/api/v1/admin/audit?limit=1000", "/api/v1/admin/audit/export?format=json", "/api/v1/admin/audit/export?format=csv"} {
			w = f.req("GET", path, "", f.admin)
			expectCode(t, w, 200)
			for _, id := range denied {
				if strings.Contains(w.Body.String(), id) {
					t.Errorf("%s disclosed inaccessible document/file %s", path, id)
				}
			}
			for _, id := range allowed {
				if !strings.Contains(w.Body.String(), id) {
					t.Errorf("%s omitted readable document %s", path, id)
				}
			}
		}
	}
	denied := []string{private.ID, draft.ID, friend.ID, attachment.FileID, attachment.VersionID}
	check([]string{team.ID, owned.ID}, denied)
	lo, hi := f.alice.User.ID, f.admin.User.ID
	if lo > hi {
		lo, hi = hi, lo
	}
	if _, err := f.a.Database().Exec(`INSERT INTO friendships VALUES(?,?,?)`, lo, hi, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	check([]string{team.ID, owned.ID, friend.ID}, []string{private.ID, draft.ID, attachment.FileID, attachment.VersionID})
	if _, err := f.a.Database().Exec(`DELETE FROM friendships`); err != nil {
		t.Fatal(err)
	}
	check([]string{team.ID, owned.ID}, denied)
}
