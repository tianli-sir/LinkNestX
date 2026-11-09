package files

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/db"
	"github.com/linknestx/linknestx/server/internal/groups"
)

type fixture struct {
	s                                       *FileService
	a                                       *auth.AuthService
	g                                       *groups.GroupService
	db                                      *sql.DB
	admin, owner, member, other             auth.Session
	adminCtx, ownerCtx, memberCtx, otherCtx context.Context
	group                                   string
	store                                   *ChunkStore
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{}
	var err error
	f.db, err = db.Open(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Close() })
	if err = db.ApplyMigrations(context.Background(), f.db); err != nil {
		t.Fatal(err)
	}
	f.a = auth.NewAuthService(f.db, "signing", "refresh")
	_, err = f.a.BootstrapAdmin(context.Background(), auth.CreateUserInput{Username: "admin", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	f.admin, f.adminCtx = f.user(t, "admin", false)
	f.owner, f.ownerCtx = f.user(t, "owner", true)
	f.member, f.memberCtx = f.user(t, "member", true)
	f.other, f.otherCtx = f.user(t, "other", true)
	f.g = groups.NewGroupService(f.db, f.a)
	g, err := f.g.Create(f.adminCtx, f.admin.User.ID, groups.CreateInput{Name: "files", OwnerID: f.owner.User.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.group = g.ID
	if err = f.g.AddMember(f.ownerCtx, f.owner.User.ID, g.ID, f.member.User.ID, "member"); err != nil {
		t.Fatal(err)
	}
	f.store, err = NewChunkStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.store.Close() })
	f.s = NewFileService(f.db, f.a, f.store, "grant-key")
	return f
}
func (f *fixture) user(t *testing.T, name string, create bool) (auth.Session, context.Context) {
	t.Helper()
	if create {
		if _, e := f.a.CreateUser(f.adminCtx, auth.CreateUserInput{Username: name, Password: "password123"}); e != nil {
			t.Fatal(e)
		}
	}
	s, e := f.a.Login(context.Background(), auth.LoginInput{Username: name, Password: "password123"})
	if e != nil {
		t.Fatal(e)
	}
	c, e := f.a.AuthenticateContext(context.Background(), s.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	return s, c
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (f *fixture) input(data []byte) StartUploadInput {
	in := StartUploadInput{GroupID: f.group, Name: "report.bin", Size: int64(len(data)), SHA256: digest(data), ChunkSize: DefaultChunkSize, Chunks: []Chunk{}}
	if len(data) > 0 {
		in.Chunks = []Chunk{{Index: 0, Hash: digest(data), Size: int64(len(data))}}
	}
	return in
}

// Metadata-stage fixture models an already verified publication, with no bytes.
func (f *fixture) published(t *testing.T) UploadSession {
	t.Helper()
	u, e := f.s.StartUpload(f.memberCtx, f.member.User.ID, f.input(nil))
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	_, e = f.db.Exec(`UPDATE file_versions SET status='published',published_at=?,expires_at=? WHERE id=?`, stamp(now), stamp(now.Add(30*24*time.Hour)), u.VersionID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.db.Exec(`UPDATE files SET current_version_id=? WHERE id=?`, u.VersionID, u.FileID)
	if e != nil {
		t.Fatal(e)
	}
	return u
}
func TestMetadataPermissionsAndPromotion(t *testing.T) {
	f := newFixture(t)
	u := f.published(t)
	if _, e := f.s.List(context.Background(), f.member.User.ID, ListInput{GroupID: f.group}); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatalf("spoof %v", e)
	}
	if _, e := f.s.Manifest(f.otherCtx, f.other.User.ID, u.VersionID); !errors.Is(e, ErrForbidden) {
		t.Fatalf("outsider %v", e)
	}
	if _, e := f.s.PromoteToPool(f.ownerCtx, f.owner.User.ID, u.FileID); !errors.Is(e, auth.ErrAdminRequired) {
		t.Fatalf("promote %v", e)
	}
	got, e := f.s.List(f.memberCtx, f.member.User.ID, ListInput{GroupID: f.group})
	if e != nil || len(got) != 1 || got[0].CreatedBy != f.member.User.ID {
		t.Fatalf("list %+v %v", got, e)
	}
	if _, e = f.s.PromoteToPool(f.adminCtx, f.admin.User.ID, u.FileID); e != nil {
		t.Fatal(e)
	}
	m, e := f.s.Manifest(f.otherCtx, f.other.User.ID, u.VersionID)
	if e != nil || m.ExpiresAt != nil || m.Visibility != "pool" {
		t.Fatalf("pool %+v %v", m, e)
	}
	for _, a := range []struct {
		ctx context.Context
		id  string
	}{{f.memberCtx, f.member.User.ID}, {f.ownerCtx, f.owner.User.ID}} {
		if e = f.s.Delete(a.ctx, a.id, u.FileID); !errors.Is(e, ErrForbidden) {
			t.Fatalf("pool deletion %v", e)
		}
	}
	if e = f.s.Delete(f.adminCtx, f.admin.User.ID, u.FileID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Manifest(f.otherCtx, f.other.User.ID, u.VersionID); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("deleted %v", e)
	}
}
func TestPendingAndUploadValidation(t *testing.T) {
	f := newFixture(t)
	in := f.input(nil)
	u, e := f.s.StartUpload(f.memberCtx, f.member.User.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	items, e := f.s.List(f.memberCtx, f.member.User.ID, ListInput{GroupID: f.group})
	if e != nil || len(items) != 0 {
		t.Fatalf("pending visible %v %v", items, e)
	}
	if _, e = f.s.Manifest(f.memberCtx, f.member.User.ID, u.VersionID); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("pending %v", e)
	}
	in.Size = MaxFileSize
	in.ChunkSize = MaxChunkSize
	in.Chunks = nil
	for n, left := 0, in.Size; left > 0; n++ {
		size := in.ChunkSize
		if left < size {
			size = left
		}
		in.Chunks = append(in.Chunks, Chunk{n, digest(nil), size})
		left -= size
	}
	if _, e = f.s.StartUpload(f.memberCtx, f.member.User.ID, in); e != nil {
		t.Fatalf("20GB %v", e)
	}
	in.Size++
	if _, e = f.s.StartUpload(f.memberCtx, f.member.User.ID, in); !errors.Is(e, ErrInvalidInput) {
		t.Fatalf("over limit %v", e)
	}
	if e = f.g.RemoveMember(f.ownerCtx, f.owner.User.ID, f.group, f.member.User.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.GetUpload(f.memberCtx, f.member.User.ID, u.UploadID); !errors.Is(e, ErrForbidden) {
		t.Fatalf("removed %v", e)
	}
}
func TestOrdinaryDeleteACLAndExpiry(t *testing.T) {
	f := newFixture(t)
	u := f.published(t)
	if e := f.s.Delete(f.otherCtx, f.other.User.ID, u.FileID); !errors.Is(e, ErrForbidden) {
		t.Fatalf("outsider delete %v", e)
	}
	if e := f.s.Delete(f.ownerCtx, f.owner.User.ID, u.FileID); e != nil {
		t.Fatal(e)
	}
	u = f.published(t)
	if e := f.s.Delete(f.memberCtx, f.member.User.ID, u.FileID); e != nil {
		t.Fatal(e)
	}
	u = f.published(t)
	f.s.SetClock(func() time.Time { return time.Now().Add(31 * 24 * time.Hour) })
	if _, e := f.s.Manifest(f.memberCtx, f.member.User.ID, u.VersionID); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("expiry %v", e)
	}
}
