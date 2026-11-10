package files

import (
	"errors"
	"github.com/linknestx/linknestx/server/internal/auth"
	"testing"
)

func TestFinalAdminDirectoryKeepsChatMembershipSeparate(t *testing.T) {
	f := newFixture(t)
	gs, e := f.g.List(f.adminCtx, f.admin.User.ID)
	if e != nil || len(gs) != 1 {
		t.Fatalf("directory %v %v", gs, e)
	}
	if gs[0].IsMember {
		t.Fatal("implicit membership")
	}
	if _, e = f.g.ListMembers(f.adminCtx, f.admin.User.ID, f.group); e != nil {
		t.Fatal(e)
	}
	if e = f.g.RequireMember(f.adminCtx, f.admin.User.ID, f.group); e == nil {
		t.Fatal("chat access")
	}
	if _, e = f.g.ListMembers(f.otherCtx, f.other.User.ID, f.group); e == nil {
		t.Fatal("outsider")
	}
}
func TestFinalPoolMetadataUnlistRelist(t *testing.T) {
	f := newFixture(t)
	u := f.published(t)
	if _, e := f.s.PromoteToPool(f.adminCtx, f.admin.User.ID, u.FileID); e != nil {
		t.Fatal(e)
	}
	name := "renamed.pdf"
	listed := false
	if e := f.s.EditPool(f.memberCtx, f.member.User.ID, u.FileID, PoolEdit{Name: &name}); !errors.Is(e, auth.ErrAdminRequired) {
		t.Fatal(e)
	}
	if e := f.s.EditPool(f.adminCtx, f.admin.User.ID, u.FileID, PoolEdit{Name: &name, Listed: &listed}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Manifest(f.memberCtx, f.member.User.ID, u.VersionID); !errors.Is(e, ErrForbidden) {
		t.Fatalf("unlisted %v", e)
	}
	list, e := f.s.List(f.memberCtx, f.member.User.ID, ListInput{GroupID: f.group})
	if e != nil || len(list) != 0 {
		t.Fatalf("origin leaks %v %v", list, e)
	}
	list, e = f.s.List(f.adminCtx, f.admin.User.ID, ListInput{Scope: "pool"})
	if e != nil || len(list) != 1 || list[0].Listed || list[0].Name != name {
		t.Fatalf("retained %v %v", list, e)
	}
	m, e := f.s.Manifest(f.adminCtx, f.admin.User.ID, u.VersionID)
	if e != nil || m.Name == name || m.ExpiresAt != nil {
		t.Fatalf("immutable %v %v", m, e)
	}
	listed = true
	if e = f.s.EditPool(f.adminCtx, f.admin.User.ID, u.FileID, PoolEdit{Listed: &listed}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Manifest(f.otherCtx, f.other.User.ID, u.VersionID); e != nil {
		t.Fatal(e)
	}
}
func TestFinalSharingPolicyPreservesHTTPAndPoolIndependence(t *testing.T) {
	f := newFixture(t)
	u := f.published(t)
	p := SharingPolicy{Enabled: false, MaxConcurrent: 2, BandwidthLimitBps: 1024}
	if e := f.s.SetSharingPolicy(f.memberCtx, f.member.User.ID, "group", f.group, p); !errors.Is(e, auth.ErrAdminRequired) {
		t.Fatal(e)
	}
	if e := f.s.SetSharingPolicy(f.adminCtx, f.admin.User.ID, "group", f.group, p); e != nil {
		t.Fatal(e)
	}
	m, e := f.s.Manifest(f.memberCtx, f.member.User.ID, u.VersionID)
	if e != nil || m.SharingPolicy.Enabled {
		t.Fatalf("HTTP %v %v", m, e)
	}
	if e = f.s.RequirePeerSharing(f.memberCtx, f.member.User.ID, u.VersionID); !errors.Is(e, ErrSharingDisabled) {
		t.Fatal(e)
	}
	if _, e = f.s.PromoteToPool(f.adminCtx, f.admin.User.ID, u.FileID); e != nil {
		t.Fatal(e)
	}
	if e = f.s.RequirePeerSharing(f.otherCtx, f.other.User.ID, u.VersionID); e != nil {
		t.Fatal(e)
	}
	if e = f.s.SetSharingPolicy(f.adminCtx, f.admin.User.ID, "pool", u.FileID, p); e != nil {
		t.Fatal(e)
	}
	if e = f.s.RequirePeerSharing(f.otherCtx, f.other.User.ID, u.VersionID); !errors.Is(e, ErrSharingDisabled) {
		t.Fatal(e)
	}
}
