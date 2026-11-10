package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linknestx/linknestx/server/internal/files"
	"github.com/linknestx/linknestx/server/internal/notes"
)

func TestDocumentVisibilityRevisionAndCurationHTTP(t *testing.T) {
	f := newSocialFixture(t)
	create := func(visibility, status string) notes.Document {
		t.Helper()
		w := f.req("POST", "/api/v1/documents", fmt.Sprintf(`{"title":"教程","body":"# hello","visibility":%q,"status":%q}`, visibility, status), f.alice)
		expectCode(t, w, 201)
		var d notes.Document
		if e := json.Unmarshal(w.Body.Bytes(), &d); e != nil {
			t.Fatal(e)
		}
		return d
	}
	draft := create("team", "draft")
	path := "/api/v1/documents/" + draft.ID
	expectCode(t, f.req("GET", path, "", f.admin), 404)
	expectCode(t, f.req("GET", path, "", f.bob), 404)
	private := create("private", "published")
	expectCode(t, f.req("GET", "/api/v1/documents/"+private.ID, "", f.admin), 404)
	friend := create("friends", "published")
	expectCode(t, f.req("GET", "/api/v1/documents/"+friend.ID, "", f.bob), 404)
	f.direct(t)
	expectCode(t, f.req("GET", "/api/v1/documents/"+friend.ID, "", f.bob), 200)
	expectCode(t, f.req("DELETE", "/api/v1/contacts/"+f.alice.User.ID, "", f.bob), 204)
	expectCode(t, f.req("GET", "/api/v1/documents/"+friend.ID, "", f.bob), 404)
	w := f.req("PATCH", path, `{"revision":1,"title":"新版教程","body":"body","visibility":"team","status":"published"}`, f.alice)
	expectCode(t, w, 200)
	var published notes.Document
	json.Unmarshal(w.Body.Bytes(), &published)
	expectCode(t, f.req("PATCH", path, `{"revision":1,"title":"stale","body":"x","visibility":"team","status":"published"}`, f.alice), 409)
	expectCode(t, f.req("POST", path+"/curation", `{"revision":2,"curated":true}`, f.bob), 403)
	w = f.req("POST", path+"/curation", `{"revision":2,"curated":true}`, f.admin)
	expectCode(t, w, 200)
	var curated notes.Document
	json.Unmarshal(w.Body.Bytes(), &curated)
	if !curated.Curated || !curated.CanEdit || curated.Revision != 3 {
		t.Fatalf("curated %+v", curated)
	}
	expectCode(t, f.req("DELETE", path+"?revision=3", "", f.alice), 403)
	expectCode(t, f.req("PATCH", path, `{"revision":3,"title":"changed","body":"x","visibility":"team","status":"published"}`, f.alice), 403)
	expectCode(t, f.req("PATCH", path, `{"revision":3,"title":"changed","body":"x","visibility":"private","status":"draft"}`, f.admin), 403)
	w = f.req("GET", "/api/v1/documents?scope=public", "", f.bob)
	expectCode(t, w, 200)
	var page notes.Page
	json.Unmarshal(w.Body.Bytes(), &page)
	if len(page.Documents) != 1 || page.Documents[0].ID != draft.ID {
		t.Fatalf("public %+v", page)
	}
	expectCode(t, f.req("POST", path+"/curation", `{"revision":3,"curated":false}`, f.admin), 200)
	expectCode(t, f.req("DELETE", path+"?revision=4", "", f.alice), 204)
	expectCode(t, f.req("GET", path, "", f.admin), 404)
}
func TestDocumentPaginationScopesAndInput(t *testing.T) {
	f := newSocialFixture(t)
	for i := 0; i < 3; i++ {
		expectCode(t, f.req("POST", "/api/v1/documents", fmt.Sprintf(`{"title":"A%d","body":"needle","visibility":"team","status":"published"}`, i), f.alice), 201)
	}
	w := f.req("GET", "/api/v1/documents?q=needle&limit=2", "", f.bob)
	expectCode(t, w, 200)
	var page notes.Page
	json.Unmarshal(w.Body.Bytes(), &page)
	if !page.HasMore || len(page.Documents) != 2 || page.NextCursor == "" {
		t.Fatalf("page %+v", page)
	}
	w = f.req("GET", "/api/v1/documents?q=needle&limit=2&cursor="+page.NextCursor, "", f.bob)
	expectCode(t, w, 200)
	var next notes.Page
	json.Unmarshal(w.Body.Bytes(), &next)
	if next.HasMore || len(next.Documents) != 1 || next.Documents[0].ID == page.Documents[0].ID {
		t.Fatalf("next %+v", next)
	}
	expectCode(t, f.req("GET", "/api/v1/documents?scope=mine&cursor="+page.NextCursor, "", f.bob), 400)
	for _, path := range []string{"?scope=bad", "?limit=0", "?limit=51", "?cursor=bad"} {
		expectCode(t, f.req("GET", "/api/v1/documents"+path, "", f.bob), 400)
	}
	expectCode(t, f.req("POST", "/api/v1/documents", `{"title":"","body":"x","visibility":"team","status":"published"}`, f.alice), 400)
}

func TestDocumentBrowserTextEditingAndAttachmentBoundary(t *testing.T) {
	f := newSocialFixture(t)
	req := func(method, path, body, csrf string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://example.com")
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, r)
		return w
	}
	w := req("POST", "/api/v1/web/login", `{"username":"alice","password":"password123"}`, "", nil)
	expectCode(t, w, 200)
	cookie := w.Result().Cookies()[0]
	var session struct {
		CSRF string `json:"csrfToken"`
	}
	json.Unmarshal(w.Body.Bytes(), &session)
	body := `{"title":"Browser note","body":"# hello","visibility":"team","status":"published"}`
	expectCode(t, req("POST", "/api/v1/documents", body, "", cookie), 403)
	w = req("POST", "/api/v1/documents", body, session.CSRF, cookie)
	expectCode(t, w, 201)
	var d notes.Document
	json.Unmarshal(w.Body.Bytes(), &d)
	expectCode(t, req("GET", "/api/v1/documents/"+d.ID, "", "", cookie), 200)
	expectCode(t, req("PATCH", "/api/v1/documents/"+d.ID, `{"revision":1,"title":"Updated","body":"text","visibility":"private","status":"draft"}`, session.CSRF, cookie), 200)
	expectCode(t, req("POST", "/api/v1/documents/"+d.ID+"/curation", `{"revision":2,"curated":true}`, session.CSRF, cookie), 403)
	expectCode(t, req("POST", "/api/v1/files/uploads", `{"documentId":"`+d.ID+`"}`, session.CSRF, cookie), 403)
	expectCode(t, req("DELETE", "/api/v1/documents/"+d.ID+"?revision=2", "", session.CSRF, cookie), 204)

	w = f.req("POST", "/api/v1/documents", body, f.alice)
	expectCode(t, w, 201)
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/documents/" + d.ID
	expectCode(t, f.req("POST", path+"/curation", `{"revision":1,"curated":true}`, f.admin), 200)
	w = req("POST", "/api/v1/web/login", `{"username":"admin","password":"correct horse battery staple"}`, "", nil)
	expectCode(t, w, 200)
	cookie = w.Result().Cookies()[0]
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	w = req("GET", path, "", "", cookie)
	expectCode(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.CanEdit || d.CanDelete || d.CanCurate {
		t.Fatal("web administrator must not receive curated-library write capabilities")
	}
	expectCode(t, req("PATCH", path, `{"revision":2,"title":"changed","body":"text","visibility":"team","status":"published"}`, session.CSRF, cookie), 403)
	expectCode(t, req("DELETE", path+"?revision=2", "", session.CSRF, cookie), 403)
}

func TestDocumentImagePreviewAndDownloadACL(t *testing.T) {
	f := newSocialFixture(t)
	ctx, e := f.a.AuthenticateContext(context.Background(), f.alice.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	n := notes.New(f.a.Database(), f.a)
	d, e := n.Create(ctx, f.alice.User.ID, notes.Input{Title: "Images", Body: "", Visibility: "team", Status: "published"})
	if e != nil {
		t.Fatal(e)
	}
	upload := func(name string, data []byte) files.UploadSession {
		t.Helper()
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		// Tiny chunks force MIME sniffing and streaming to span chunk boundaries.
		in := files.StartUploadInput{DocumentID: d.ID, Name: name, Size: int64(len(data)), SHA256: hash, ChunkSize: 3}
		for start := 0; start < len(data); start += 3 {
			end := min(start+3, len(data))
			digest := sha256.Sum256(data[start:end])
			in.Chunks = append(in.Chunks, files.Chunk{Index: len(in.Chunks), Hash: hex.EncodeToString(digest[:]), Size: int64(end - start)})
		}
		u, e := f.fs.StartUpload(ctx, f.alice.User.ID, in)
		if e != nil {
			t.Fatal(e)
		}
		for _, chunk := range in.Chunks {
			start := chunk.Index * 3
			if e = f.fs.PutChunk(ctx, f.alice.User.ID, u.UploadID, chunk.Index, bytes.NewReader(data[start:start+int(chunk.Size)])); e != nil {
				t.Fatal(e)
			}
		}
		if _, e = f.fs.CompleteUpload(ctx, f.alice.User.ID, u.UploadID); e != nil {
			t.Fatal(e)
		}
		return u
	}
	var encoded bytes.Buffer
	png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	data := encoded.Bytes()
	u := upload("image.png", data)
	path := "/api/v1/documents/" + d.ID + "/attachments/" + u.VersionID + "/preview"
	w := f.req("GET", path, "", f.bob)
	expectCode(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), data) || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("image response")
	}
	expectCode(t, f.req("GET", "/api/v1/documents/wrong/attachments/"+u.VersionID+"/preview", "", f.bob), 404)
	bad := upload("fake.png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	expectCode(t, f.req("GET", "/api/v1/documents/"+d.ID+"/attachments/"+bad.VersionID+"/preview", "", f.bob), 415)
	w = f.req("GET", "/api/v1/web/files/"+u.VersionID+"/download", "", f.bob)
	expectCode(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("download bytes")
	}
	d, e = n.Update(ctx, f.alice.User.ID, d.ID, notes.Input{Title: d.Title, Body: d.Body, Visibility: "private", Status: "published", Revision: d.Revision})
	if e != nil {
		t.Fatal(e)
	}
	expectCode(t, f.req("GET", path, "", f.bob), 403)
	expectCode(t, f.req("GET", "/api/v1/web/files/"+u.VersionID+"/download", "", f.bob), 403)
	expectCode(t, f.req("GET", "/api/v1/files?documentId="+d.ID, "", f.admin), 403)
}
