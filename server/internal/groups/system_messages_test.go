package groups

import "testing"

func TestGroupChangesPersistSystemMessagesAndIgnoreNoops(t *testing.T) {
	s, a, conn, admin, ctx := groupFixture(t)
	member, _ := groupUser(t, a, ctx, "member")
	g, err := s.Create(ctx, admin.User.ID, CreateInput{Name: "系统消息"})
	if err != nil {
		t.Fatal(err)
	}
	count := func(want int) {
		t.Helper()
		var got int
		if err := conn.QueryRow("SELECT count(*) FROM messages WHERE group_id=?", g.ID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("persisted group notices=%d want=%d", got, want)
		}
	}
	count(1)
	for _, role := range []string{"member", "member", "owner", "owner"} {
		if err := s.AddMember(ctx, admin.User.ID, g.ID, member.User.ID, role); err != nil {
			t.Fatal(err)
		}
	}
	count(3)
	if err := s.RemoveMember(ctx, admin.User.ID, g.ID, member.User.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(ctx, admin.User.ID, g.ID, member.User.ID); err != nil {
		t.Fatal(err)
	}
	count(4)
	if err := s.RemoveMember(ctx, admin.User.ID, g.ID, admin.User.ID); err != ErrLastOwner {
		t.Fatalf("last-owner guard %v", err)
	}
	count(4)
	rows, err := conn.Query("SELECT kind,system_type,body FROM messages WHERE group_id=? ORDER BY sequence", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []string{"group.created", "group.member_added", "group.owner_changed", "group.member_removed"}
	i := 0
	for rows.Next() {
		var kind, event, body string
		if err := rows.Scan(&kind, &event, &body); err != nil {
			t.Fatal(err)
		}
		if kind != "system" || event != want[i] || body == "" {
			t.Fatalf("system notice %q %q %q", kind, event, body)
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestFailedSystemMessageWriteRollsBackMembershipChange(t *testing.T) {
	s, a, conn, admin, ctx := groupFixture(t)
	member, _ := groupUser(t, a, ctx, "rollback-member")
	g, err := s.Create(ctx, admin.User.ID, CreateInput{Name: "atomic notices"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec("CREATE TRIGGER reject_notice BEFORE INSERT ON messages WHEN NEW.kind='system' BEGIN SELECT RAISE(ABORT,'notice write failed'); END"); err != nil {
		t.Fatal(err)
	}
	if err = s.AddMember(ctx, admin.User.ID, g.ID, member.User.ID, "member"); err == nil {
		t.Fatal("failed notice accepted")
	}
	var n int
	if err = conn.QueryRow("SELECT count(*) FROM group_members WHERE group_id=? AND user_id=?", g.ID, member.User.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("membership leaked from rolled-back notice: %d %v", n, err)
	}
	if err = conn.QueryRow("SELECT count(*) FROM messages WHERE group_id=?", g.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("history changed after failure: %d %v", n, err)
	}
}
