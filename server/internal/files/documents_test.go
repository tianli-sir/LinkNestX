package files

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/linknestx/linknestx/server/internal/notes"
)

func TestDocumentAttachmentACLRetentionAndCuration(t *testing.T) {
	f := newFixture(t)
	n := notes.New(f.db, f.a)
	d, e := n.Create(f.ownerCtx, f.owner.User.ID, notes.Input{Title: "Guide", Body: "# Hello", Visibility: "friends", Status: "draft"})
	if e != nil {
		t.Fatal(e)
	}
	in := f.input([]byte("document payload"))
	in.GroupID = ""
	in.DocumentID = d.ID
	u, e := f.s.StartUpload(f.ownerCtx, f.owner.User.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.PutChunk(f.ownerCtx, f.owner.User.ID, u.UploadID, 0, bytes.NewBufferString("document payload")); e != nil {
		t.Fatal(e)
	}
	m, e := f.s.CompleteUpload(f.ownerCtx, f.owner.User.ID, u.UploadID)
	if e != nil || m.ExpiresAt != nil {
		t.Fatalf("publish %+v %v", m, e)
	}
	var count int
	f.db.QueryRow(`SELECT count(*) FROM messages WHERE file_id=?`, u.FileID).Scan(&count)
	if count != 0 {
		t.Fatal("document upload generated chat")
	}
	if _, e = f.s.Manifest(f.adminCtx, f.admin.User.ID, u.VersionID); !errors.Is(e, ErrForbidden) {
		t.Fatalf("admin read draft: %v", e)
	}
	lo, hi := f.owner.User.ID, f.member.User.ID
	if lo > hi {
		lo, hi = hi, lo
	}
	f.db.Exec(`INSERT INTO friendships VALUES(?,?,?)`, lo, hi, stamp(time.Now()))
	d, e = n.Update(f.ownerCtx, f.owner.User.ID, d.ID, notes.Input{Title: "Guide", Body: "# Hello", Visibility: "friends", Status: "published", Revision: d.Revision})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Manifest(f.memberCtx, f.member.User.ID, u.VersionID); e != nil {
		t.Fatal(e)
	}
	grant, e := f.s.AuthorizeChunk(f.memberCtx, f.member.User.ID, u.VersionID, in.SHA256)
	if e != nil {
		t.Fatal(e)
	}
	f.db.Exec(`DELETE FROM friendships`)
	if _, e = f.s.Manifest(f.memberCtx, f.member.User.ID, u.VersionID); !errors.Is(e, ErrForbidden) {
		t.Fatalf("lost friend %v", e)
	}
	if e = f.s.ValidateChunkGrant(f.ownerCtx, grant); e == nil {
		t.Fatal("stale grant accepted")
	}
	d, e = n.Update(f.ownerCtx, f.owner.User.ID, d.ID, notes.Input{Title: "Guide", Body: "# Hello", Visibility: "team", Status: "published", Revision: d.Revision})
	if e != nil {
		t.Fatal(e)
	}
	d, e = n.Curate(f.adminCtx, f.admin.User.ID, d.ID, d.Revision, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Delete(f.ownerCtx, f.owner.User.ID, u.FileID); !errors.Is(e, ErrForbidden) {
		t.Fatalf("author delete curated %v", e)
	}
	in.FileID = u.FileID
	if _, e = f.s.StartUpload(f.ownerCtx, f.owner.User.ID, in); !errors.Is(e, ErrForbidden) {
		t.Fatalf("replace curated %v", e)
	}
	if _, e = f.s.PromoteToPool(f.adminCtx, f.admin.User.ID, u.FileID); !errors.Is(e, ErrForbidden) {
		t.Fatalf("separate promote %v", e)
	}
	f.s.SetClock(func() time.Time { return time.Now().AddDate(5, 0, 0) })
	if _, e = f.s.Manifest(f.otherCtx, f.other.User.ID, u.VersionID); e != nil {
		t.Fatalf("permanent %v", e)
	}
	if e = n.Delete(f.ownerCtx, f.owner.User.ID, d.ID, d.Revision); !errors.Is(e, notes.ErrForbidden) {
		t.Fatalf("author deletes curated doc %v", e)
	}
	if e = n.Delete(f.adminCtx, f.admin.User.ID, d.ID, d.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Manifest(f.otherCtx, f.other.User.ID, u.VersionID); e == nil {
		t.Fatal("deleted document bytes exposed")
	}
}

func TestDocumentSummaryAndImmutableAttachmentVersions(t *testing.T) {
	f := newFixture(t)
	n := notes.New(f.db, f.a)
	body := strings.Repeat("文", 2001) + "\n[attachment](linknestx-file:version)"
	d, err := n.Create(f.ownerCtx, f.owner.User.ID, notes.Input{Title: "Versioned guide", Body: body, Visibility: "team", Status: "published"})
	if err != nil {
		t.Fatal(err)
	}
	upload := func(content, fileID string) UploadSession {
		t.Helper()
		in := f.input([]byte(content))
		in.GroupID, in.DocumentID, in.FileID = "", d.ID, fileID
		u, err := f.s.StartUpload(f.ownerCtx, f.owner.User.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.s.PutChunk(f.ownerCtx, f.owner.User.ID, u.UploadID, 0, strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}
		if _, err = f.s.CompleteUpload(f.ownerCtx, f.owner.User.ID, u.UploadID); err != nil {
			t.Fatal(err)
		}
		return u
	}
	old := upload("original tutorial", "")
	latest := upload("updated tutorial", old.FileID)
	page, err := n.List(f.memberCtx, f.member.User.ID, notes.ListInput{})
	if err != nil || len(page.Documents) != 1 {
		t.Fatalf("list: %+v %v", page, err)
	}
	summary := page.Documents[0]
	if summary.Body != strings.Repeat("文", 2000) || !summary.BodyTruncated || len(summary.Attachments) != 0 {
		t.Fatalf("list must contain a Unicode-safe summary without attachments: body length=%d truncated=%v attachments=%d", len(summary.Body), summary.BodyTruncated, len(summary.Attachments))
	}
	full, err := n.Get(f.memberCtx, f.member.User.ID, d.ID)
	if err != nil || full.Body != body || full.BodyTruncated || len(full.Attachments) != 2 {
		t.Fatalf("full document: body matches=%v truncated=%v attachments=%d error=%v", full.Body == body, full.BodyTruncated, len(full.Attachments), err)
	}
	seen := map[string]bool{}
	for _, attachment := range full.Attachments {
		if attachment.ID != old.FileID || attachment.CurrentVersionID != latest.VersionID || attachment.ExpiresAt != nil {
			t.Fatalf("attachment metadata: %+v", attachment)
		}
		seen[attachment.VersionID] = true
		manifest, err := f.s.Manifest(f.memberCtx, f.member.User.ID, attachment.VersionID)
		if err != nil || manifest.VersionID != attachment.VersionID || manifest.ExpiresAt != nil {
			t.Fatalf("immutable download version unavailable: %+v %v", manifest, err)
		}
	}
	if !seen[old.VersionID] || !seen[latest.VersionID] {
		t.Fatalf("old and new markdown targets must remain available: %+v", seen)
	}
}
