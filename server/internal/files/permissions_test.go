package files

import (
	"context"
	"errors"
	"github.com/linknestx/linknestx/server/internal/auth"
	"testing"
	"time"
)

func TestGrantBindsRecipientAndRechecksBothPeers(t *testing.T) {
	f := newFixture(t)
	u := f.published(t)
	hash := digest([]byte("a"))
	if _, e := f.db.Exec(`INSERT INTO file_chunks(version_id,chunk_index,hash,size) VALUES(?,0,?,1)`, u.VersionID, hash); e != nil {
		t.Fatal(e)
	}
	g, e := f.s.AuthorizeChunk(f.memberCtx, f.member.User.ID, u.VersionID, hash)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.ValidateChunkGrant(f.ownerCtx, g); e != nil {
		t.Fatal(e)
	}
	if e = f.s.ValidateChunkGrant(context.Background(), g); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatalf("spoof %v", e)
	}
	if e = f.s.ValidateChunkGrant(f.otherCtx, g); !errors.Is(e, ErrForbidden) {
		t.Fatalf("serving outsider %v", e)
	}
	bad := g
	bad.UserID = f.other.User.ID
	if e = f.s.ValidateChunkGrant(f.ownerCtx, bad); !errors.Is(e, ErrInvalidGrant) {
		t.Fatalf("tampered %v", e)
	}
	if e = f.g.RemoveMember(f.ownerCtx, f.owner.User.ID, f.group, f.member.User.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.s.ValidateChunkGrant(f.ownerCtx, g); !errors.Is(e, ErrForbidden) {
		t.Fatalf("removed recipient %v", e)
	}
	if e = f.g.AddMember(f.ownerCtx, f.owner.User.ID, f.group, f.member.User.ID, "member"); e != nil {
		t.Fatal(e)
	}
	f.s.SetClock(func() time.Time { return time.Now().Add(3 * time.Minute) })
	if e = f.s.ValidateChunkGrant(f.ownerCtx, g); !errors.Is(e, ErrInvalidGrant) {
		t.Fatalf("expired grant %v", e)
	}
	f.s.SetClock(time.Now)
	if e = f.a.RevokeSession(f.adminCtx, f.member.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.s.ValidateChunkGrant(f.ownerCtx, g); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatalf("revoked grant %v", e)
	}
}
