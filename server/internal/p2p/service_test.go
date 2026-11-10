package p2p

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"path/filepath"
	"testing"
	"time"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/config"
	"github.com/linknestx/linknestx/server/internal/db"
	"github.com/linknestx/linknestx/server/internal/files"
	"github.com/linknestx/linknestx/server/internal/groups"
)

type fixture struct {
	db       *sql.DB
	auth     *auth.AuthService
	files    *files.FileService
	groups   *groups.GroupService
	service  *Service
	sessions []auth.Session
	contexts []context.Context
	group    groups.Group
	version  files.UploadSession
	hash     string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "peer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err = db.ApplyMigrations(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	a := auth.NewAuthService(conn, "sign", "refresh")
	if _, err = a.BootstrapAdmin(context.Background(), auth.CreateUserInput{Username: "admin", Password: "password123"}); err != nil {
		t.Fatal(err)
	}
	// This fixture models accounts whose initial password was already changed.
	if _, err := a.Database().Exec(`UPDATE users SET must_change_password=0`); err != nil {
		t.Fatal(err)
	}
	admin, err := a.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	adminCtx, err := a.AuthenticateContext(context.Background(), admin.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: conn, auth: a}
	for _, name := range []string{"owner", "recipient", "outsider"} {
		if _, err = a.CreateUser(adminCtx, auth.CreateUserInput{Username: name, Password: "password123"}); err != nil {
			t.Fatal(err)
		}
		// This fixture models accounts whose initial password was already changed.
		if _, err := a.Database().Exec(`UPDATE users SET must_change_password=0`); err != nil {
			t.Fatal(err)
		}
		session, e := a.Login(context.Background(), auth.LoginInput{Username: name, Password: "password123"})
		if e != nil {
			t.Fatal(e)
		}
		ctx, e := a.AuthenticateContext(context.Background(), session.AccessToken)
		if e != nil {
			t.Fatal(e)
		}
		f.sessions = append(f.sessions, session)
		f.contexts = append(f.contexts, ctx)
	}
	f.groups = groups.NewGroupService(conn, a)
	f.group, err = f.groups.Create(adminCtx, admin.User.ID, groups.CreateInput{Name: "peers", OwnerID: f.sessions[0].User.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.groups.AddMember(f.contexts[0], f.sessions[0].User.ID, f.group.ID, f.sessions[1].User.ID, "member"); err != nil {
		t.Fatal(err)
	}
	store, err := files.NewChunkStore(filepath.Join(t.TempDir(), "chunks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f.files = files.NewFileService(conn, a, store, "grants")
	data := []byte("verified peer bytes")
	sum := sha256.Sum256(data)
	f.hash = hex.EncodeToString(sum[:])
	f.version, err = f.files.StartUpload(f.contexts[0], f.sessions[0].User.ID, files.StartUploadInput{GroupID: f.group.ID, Name: "peer.bin", Size: int64(len(data)), SHA256: f.hash, Chunks: []files.Chunk{{Index: 0, Hash: f.hash, Size: int64(len(data))}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.files.PutChunk(f.contexts[0], f.sessions[0].User.ID, f.version.UploadID, 0, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.files.CompleteUpload(f.contexts[0], f.sessions[0].User.ID, f.version.UploadID); err != nil {
		t.Fatal(err)
	}
	f.service = NewService(conn, a, f.files, config.Default())
	return f
}
func (f *fixture) announce(t *testing.T, i int) {
	t.Helper()
	if err := f.service.Announce(f.contexts[i], f.sessions[i].User.ID, AnnounceInput{VersionID: f.version.VersionID, Indexes: []int{0}}); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) sources(t *testing.T, i int) []PeerSource {
	t.Helper()
	r, e := f.service.ListSources(f.contexts[i], f.sessions[i].User.ID, f.version.VersionID)
	if e != nil {
		t.Fatal(e)
	}
	return r.Sources
}

func TestSourcesFollowCurrentPermissionsLeaseAndRefresh(t *testing.T) {
	f := setup(t)
	f.announce(t, 0)
	if got := f.sources(t, 1); len(got) != 1 || got[0].DeviceID != f.sessions[0].DeviceID || len(got[0].Indexes) != 1 {
		t.Fatalf("sources %+v", got)
	}
	if got := f.sources(t, 0); len(got) != 0 {
		t.Fatal("own device disclosed")
	}
	if _, err := f.service.ListSources(f.contexts[2], f.sessions[2].User.ID, f.version.VersionID); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("outsider %v", err)
	}
	for _, indexes := range [][]int{{-1}, {1}, {0, 0}, make([]int, 5001)} {
		if e := f.service.Announce(f.contexts[0], f.sessions[0].User.ID, AnnounceInput{f.version.VersionID, indexes}); !errors.Is(e, ErrInvalidInput) {
			t.Fatalf("indexes accepted %v", e)
		}
	}
	fresh, err := f.auth.Refresh(context.Background(), f.sessions[0].RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.sources(t, 1); len(got) != 0 {
		t.Fatal("revoked source discovered")
	}
	freshCtx, err := f.auth.AuthenticateContext(context.Background(), fresh.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	f.sessions[0], f.contexts[0] = fresh, freshCtx
	f.announce(t, 0)
	if len(f.sources(t, 1)) != 1 {
		t.Fatal("refreshed source missing")
	}
	now := time.Now().Add(61 * time.Second)
	f.service.now = func() time.Time { return now }
	if len(f.sources(t, 1)) != 0 {
		t.Fatal("expired heartbeat discovered")
	}
	if e := f.service.Heartbeat(f.contexts[0], f.sessions[0].User.ID, f.version.VersionID); e != nil {
		t.Fatal(e)
	}
	if len(f.sources(t, 1)) != 1 {
		t.Fatal("heartbeat did not restore source")
	}
	if e := f.service.Withdraw(f.contexts[0], f.sessions[0].User.ID, ""); e != nil {
		t.Fatal(e)
	}
	if len(f.sources(t, 1)) != 0 {
		t.Fatal("withdrawn source discovered")
	}
}

func TestRemovedSourceDeletedVersionAndDisabledAccountAreUnavailable(t *testing.T) {
	f := setup(t)
	f.announce(t, 1)
	if e := f.groups.RemoveMember(f.contexts[0], f.sessions[0].User.ID, f.group.ID, f.sessions[1].User.ID); e != nil {
		t.Fatal(e)
	}
	if len(f.sources(t, 0)) != 0 {
		t.Fatal("removed member discovered")
	}
	if e := f.groups.AddMember(f.contexts[0], f.sessions[0].User.ID, f.group.ID, f.sessions[1].User.ID, "member"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Exec(`UPDATE users SET disabled=1 WHERE id=?`, f.sessions[1].User.ID); e != nil {
		t.Fatal(e)
	}
	if len(f.sources(t, 0)) != 0 {
		t.Fatal("disabled source discovered")
	}
	if e := f.files.Delete(f.contexts[0], f.sessions[0].User.ID, f.version.FileID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.service.ListSources(f.contexts[0], f.sessions[0].User.ID, f.version.VersionID); !errors.Is(e, files.ErrUnavailable) {
		t.Fatalf("deleted version %v", e)
	}
}

func TestGrantsUseRealFileACLAndRevokeImmediately(t *testing.T) {
	f := setup(t)
	g, e := f.service.CreateChunkGrant(f.contexts[1], f.sessions[1].User.ID, f.version.VersionID, f.hash)
	if e != nil {
		t.Fatal(e)
	}
	if g.DeviceID != f.sessions[1].DeviceID || g.SessionID != f.sessions[1].ID || g.UserID != f.sessions[1].User.ID || g.Size != 19 || g.ExpiresAt.After(time.Now().Add(2*time.Minute)) {
		t.Fatalf("grant %+v", g)
	}
	if e = f.service.ValidateChunkGrant(f.contexts[0], g); e != nil {
		t.Fatal(e)
	}
	if e = f.service.ValidateChunkGrant(f.contexts[2], g); !errors.Is(e, files.ErrForbidden) {
		t.Fatalf("outsider serving %v", e)
	}
	tampered := g
	tampered.DeviceID = f.sessions[2].DeviceID
	if e = f.service.ValidateChunkGrant(f.contexts[0], tampered); !errors.Is(e, files.ErrInvalidGrant) {
		t.Fatalf("tampered %v", e)
	}
	if e = f.auth.RevokeSession(context.Background(), f.sessions[1].ID); e != nil {
		t.Fatal(e)
	}
	if e = f.service.ValidateChunkGrant(f.contexts[0], g); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatalf("revoked grant %v", e)
	}
}

func TestPoolSameAccountOtherDeviceAndExpiredFile(t *testing.T) {
	f := setup(t)
	f.announce(t, 0)
	// This fixture models accounts whose initial password was already changed.
	if _, err := f.auth.Database().Exec(`UPDATE users SET must_change_password=0`); err != nil {
		t.Fatal(err)
	}
	second, e := f.auth.Login(context.Background(), auth.LoginInput{Username: "owner", Password: "password123"})
	if e != nil {
		t.Fatal(e)
	}
	secondCtx, e := f.auth.AuthenticateContext(context.Background(), second.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	result, e := f.service.ListSources(secondCtx, second.User.ID, f.version.VersionID)
	if e != nil || len(result.Sources) != 1 {
		t.Fatalf("same account device %+v %v", result, e)
	}
	// This fixture models accounts whose initial password was already changed.
	if _, err := f.auth.Database().Exec(`UPDATE users SET must_change_password=0`); err != nil {
		t.Fatal(err)
	}
	admin, e := f.auth.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "password123"})
	if e != nil {
		t.Fatal(e)
	}
	ctx, e := f.auth.AuthenticateContext(context.Background(), admin.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.files.PromoteToPool(ctx, admin.User.ID, f.version.FileID); e != nil {
		t.Fatal(e)
	}
	if len(f.sources(t, 2)) != 1 {
		t.Fatal("authenticated pool source missing")
	}
	if _, e = f.service.ListSources(context.Background(), f.sessions[2].User.ID, f.version.VersionID); e == nil {
		t.Fatal("anonymous pool source accepted")
	}
	if _, e = f.db.Exec(`UPDATE file_versions SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).UTC().Format(timestampLayout), f.version.VersionID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.ListSources(f.contexts[0], f.sessions[0].User.ID, f.version.VersionID); !errors.Is(e, files.ErrUnavailable) {
		t.Fatalf("expired file %v", e)
	}
	if e = f.service.Cleanup(context.Background()); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*) FROM peer_sources`).Scan(&count); e != nil || count != 0 {
		t.Fatalf("expired source retention %d %v", count, e)
	}
}

func TestTURNRESTCredentialsAndStaticConfiguration(t *testing.T) {
	f := setup(t)
	at := time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC)
	f.service.now = func() time.Time { return at }
	f.service.cfg.ICEServers = []config.ICEServer{{URLs: []string{"stun:private:3478"}}, {URLs: []string{"turn:private:3478?transport=udp"}, Username: "static", Credential: "static-secret"}}
	result, e := f.service.Config(f.contexts[0], f.sessions[0].User.ID)
	if e != nil {
		t.Fatal(e)
	}
	if result.ICEServers[1].Username != "static" || result.ICEServers[1].Credential != "static-secret" {
		t.Fatalf("static %+v", result)
	}
	f.service.cfg.TURNSharedSecret = "shared-secret"
	result, e = f.service.Config(f.contexts[0], f.sessions[0].User.ID)
	if e != nil {
		t.Fatal(e)
	}
	expected := fmt.Sprintf("%d:%s", at.Add(10*time.Minute).Unix(), f.sessions[0].DeviceID)
	mac := hmac.New(sha1.New, []byte("shared-secret"))
	mac.Write([]byte(expected))
	if result.ICEServers[1].Username != expected || result.ICEServers[1].Credential != base64.StdEncoding.EncodeToString(mac.Sum(nil)) || result.ICEServers[0].Username != "" {
		t.Fatalf("REST credentials %+v", result)
	}
	if _, e = f.service.Config(context.Background(), f.sessions[0].User.ID); e == nil {
		t.Fatal("anonymous credential leak")
	}
}

func TestGrantExpiryAndRemovedRecipient(t *testing.T) {
	f := setup(t)
	g, e := f.service.CreateChunkGrant(f.contexts[1], f.sessions[1].User.ID, f.version.VersionID, f.hash)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.groups.RemoveMember(f.contexts[0], f.sessions[0].User.ID, f.group.ID, f.sessions[1].User.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.service.ValidateChunkGrant(f.contexts[0], g); !errors.Is(e, files.ErrForbidden) {
		t.Fatalf("removed recipient %v", e)
	}
	if e = f.groups.AddMember(f.contexts[0], f.sessions[0].User.ID, f.group.ID, f.sessions[1].User.ID, "member"); e != nil {
		t.Fatal(e)
	}
	f.files.SetClock(func() time.Time { return g.ExpiresAt.Add(time.Second) })
	if e = f.service.ValidateChunkGrant(f.contexts[0], g); !errors.Is(e, files.ErrInvalidGrant) {
		t.Fatalf("expired grant %v", e)
	}
}

func TestConnectionAuditRejectsHistoricalReuseAndTerminalMutation(t *testing.T) {
	f := setup(t)
	first := ConnectionAudit{ConnectionID: uuid.NewString(), VersionID: f.version.VersionID, RequesterDeviceID: f.sessions[1].DeviceID, SourceDeviceID: f.sessions[0].DeviceID, RequesterUserID: f.sessions[1].User.ID}
	if e := f.service.RecordConnection(context.Background(), first); e != nil {
		t.Fatal(e)
	}
	first.Ended = true
	first.Outcome = "closed"
	if e := f.service.RecordConnection(context.Background(), first); e != nil {
		t.Fatal(e)
	}
	snapshot := func() []string {
		t.Helper()
		values := make([]string, 8)
		e := f.db.QueryRow(`SELECT connection_id,version_id,requester_device_id,source_device_id,requester_user_id,started_at,ended_at,outcome FROM peer_transfers WHERE connection_id=?`, first.ConnectionID).Scan(&values[0], &values[1], &values[2], &values[3], &values[4], &values[5], &values[6], &values[7])
		if e != nil {
			t.Fatal(e)
		}
		return values
	}
	before := snapshot()
	restarted := NewService(f.db, f.auth, f.files, config.Default())
	reused := first
	reused.Ended = false
	reused.RequesterDeviceID = f.sessions[0].DeviceID
	reused.SourceDeviceID = f.sessions[1].DeviceID
	reused.RequesterUserID = f.sessions[0].User.ID
	if e := restarted.RecordConnection(context.Background(), reused); e == nil {
		t.Fatal("historical connection ID accepted after service restart")
	}
	restarted.now = func() time.Time { return time.Now().Add(time.Hour) }
	reused.Ended = true
	reused.Outcome = "disconnected"
	_ = restarted.RecordConnection(context.Background(), reused)
	first.Outcome = "expired"
	_ = restarted.RecordConnection(context.Background(), first)
	after := snapshot()
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("terminal audit field %d changed %q -> %q", i, before[i], after[i])
		}
	}
	unfinished := first
	unfinished.ConnectionID = uuid.NewString()
	unfinished.Ended = false
	if e := restarted.RecordConnection(context.Background(), unfinished); e != nil {
		t.Fatal(e)
	}
	forged := unfinished
	forged.Ended = true
	forged.Outcome = "closed"
	forged.RequesterDeviceID = f.sessions[2].DeviceID
	_ = restarted.RecordConnection(context.Background(), forged)
	var end sql.NullString
	if e := f.db.QueryRow(`SELECT ended_at FROM peer_transfers WHERE connection_id=?`, unfinished.ConnectionID).Scan(&end); e != nil || end.Valid {
		t.Fatalf("unbound terminal update %v %v", end, e)
	}
}

func TestFinalSharingAndUnlistedInvalidateExistingSourcesAndGrants(t *testing.T) {
	f := setup(t)
	f.announce(t, 0)
	f.announce(t, 1)
	grant, e := f.service.CreateChunkGrant(f.contexts[1], f.sessions[1].User.ID, f.version.VersionID, f.hash)
	if e != nil {
		t.Fatal(e)
	}
	// Policies are mutated through the authenticated admin service; admin was not added to the group.
	admin, e := f.auth.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "password123"})
	if e != nil {
		t.Fatal(e)
	}
	ctx, e := f.auth.AuthenticateContext(context.Background(), admin.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.files.SetSharingPolicy(ctx, admin.User.ID, "group", f.group.ID, files.SharingPolicy{Enabled: false}); e != nil {
		t.Fatal(e)
	}
	if e = f.service.ValidateChunkGrant(f.contexts[0], grant); !errors.Is(e, files.ErrSharingDisabled) {
		t.Fatalf("stale grant %v", e)
	}
	if e = f.service.Announce(f.contexts[0], f.sessions[0].User.ID, AnnounceInput{VersionID: f.version.VersionID, Indexes: []int{0}}); !errors.Is(e, files.ErrSharingDisabled) {
		t.Fatal(e)
	}
	if yes, e := f.service.IsSource(f.contexts[0], f.sessions[0].DeviceID, f.sessions[0].ID, f.version.VersionID); e != nil || yes {
		t.Fatalf("source still eligible %v %v", yes, e)
	}
	reader, _, e := f.files.OpenChunk(f.contexts[1], f.sessions[1].User.ID, f.version.VersionID, f.hash)
	if e != nil {
		t.Fatalf("HTTP fallback %v", e)
	}
	reader.Close()
	if _, e = f.files.PromoteToPool(ctx, admin.User.ID, f.version.FileID); e != nil {
		t.Fatal(e)
	}
	if e = f.service.ValidateChunkGrant(f.contexts[0], grant); e != nil {
		t.Fatalf("pool independent %v", e)
	}
	listed := false
	if e = f.files.EditPool(ctx, admin.User.ID, f.version.FileID, files.PoolEdit{Listed: &listed}); e != nil {
		t.Fatal(e)
	}
	if e = f.service.ValidateChunkGrant(f.contexts[0], grant); !errors.Is(e, files.ErrForbidden) {
		t.Fatalf("unlisted stale grant %v", e)
	}
	if yes, e := f.service.IsSource(ctx, f.sessions[0].DeviceID, f.sessions[0].ID, f.version.VersionID); e != nil || yes {
		t.Fatalf("unlisted source %v %v", yes, e)
	}
	if _, _, e = f.files.OpenChunk(f.contexts[0], f.sessions[0].User.ID, f.version.VersionID, f.hash); !errors.Is(e, files.ErrForbidden) {
		t.Fatal(e)
	}
	listed = true
	if e = f.files.EditPool(ctx, admin.User.ID, f.version.FileID, files.PoolEdit{Listed: &listed}); e != nil {
		t.Fatal(e)
	}
	reader, _, e = f.files.OpenChunk(f.contexts[1], f.sessions[1].User.ID, f.version.VersionID, f.hash)
	if e != nil {
		t.Fatal(e)
	}
	reader.Close()
}
