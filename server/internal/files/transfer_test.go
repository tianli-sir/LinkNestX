package files

import (
	"bytes"
	"context"
	"errors"
	"github.com/linknestx/linknestx/server/internal/chat"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func (f *fixture) publish(t *testing.T, data []byte) UploadSession {
	t.Helper()
	u, e := f.s.StartUpload(f.memberCtx, f.member.User.ID, f.input(data))
	if e != nil {
		t.Fatal(e)
	}
	if len(data) > 0 {
		if e = f.s.PutChunk(f.memberCtx, f.member.User.ID, u.UploadID, 0, bytes.NewReader(data)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = f.s.CompleteUpload(f.memberCtx, f.member.User.ID, u.UploadID); e != nil {
		t.Fatal(e)
	}
	return u
}
func TestUploadReceiptsVerificationResumeAndPublication(t *testing.T) {
	f := newFixture(t)
	data := bytes.Repeat([]byte("bytes"), 30000)
	u, e := f.s.StartUpload(f.memberCtx, f.member.User.ID, f.input(data))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.CompleteUpload(f.memberCtx, f.member.User.ID, u.UploadID); !errors.Is(e, ErrIncomplete) {
		t.Fatalf("incomplete %v", e)
	}
	for _, bad := range [][]byte{data[:len(data)-1], append(append([]byte{}, data...), 0), bytes.Repeat([]byte("x"), len(data))} {
		if e = f.s.PutChunk(f.memberCtx, f.member.User.ID, u.UploadID, 0, bytes.NewReader(bad)); e == nil {
			t.Fatal("invalid chunk accepted")
		}
	}
	for i := 0; i < 2; i++ {
		if e = f.s.PutChunk(f.memberCtx, f.member.User.ID, u.UploadID, 0, bytes.NewReader(data)); e != nil {
			t.Fatal(e)
		}
	}
	resume, e := f.s.GetUpload(f.memberCtx, f.member.User.ID, u.UploadID)
	if e != nil || len(resume.MissingIndexes) != 0 || resume.Completed {
		t.Fatalf("resume %+v %v", resume, e)
	}
	for i := 0; i < 2; i++ {
		m, e := f.s.CompleteUpload(f.memberCtx, f.member.User.ID, u.UploadID)
		if e != nil || m.VersionID != u.VersionID || m.ExpiresAt == nil {
			t.Fatalf("complete %+v %v", m, e)
		}
	}
	resume, e = f.s.GetUpload(f.memberCtx, f.member.User.ID, u.UploadID)
	if e != nil || !resume.Completed {
		t.Fatalf("completed %+v %v", resume, e)
	}
	r, info, e := f.s.OpenChunk(f.memberCtx, f.member.User.ID, u.VersionID, digest(data))
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(r)
	r.Close()
	if e != nil || !bytes.Equal(got, data) || info.Size != int64(len(data)) {
		t.Fatalf("download %v %v", info, e)
	}
	var messages int
	if e = f.db.QueryRow(`SELECT count(*) FROM messages WHERE file_version_id=?`, u.VersionID).Scan(&messages); e != nil || messages != 1 {
		t.Fatalf("messages %d %v", messages, e)
	}
	in := f.input(data)
	u2, e := f.s.StartUpload(f.ownerCtx, f.owner.User.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.CompleteUpload(f.ownerCtx, f.owner.User.ID, u2.UploadID); !errors.Is(e, ErrIncomplete) {
		t.Fatalf("hash stealing %v", e)
	}
	in.SHA256 = digest([]byte("wrong"))
	u3, e := f.s.StartUpload(f.memberCtx, f.member.User.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.PutChunk(f.memberCtx, f.member.User.ID, u3.UploadID, 0, bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.CompleteUpload(f.memberCtx, f.member.User.ID, u3.UploadID); !errors.Is(e, ErrChunkHashMismatch) {
		t.Fatalf("full hash %v", e)
	}
	f.publish(t, nil)
}
func TestSharedChunksCleanupAndExpiry(t *testing.T) {
	f := newFixture(t)
	data := []byte("shared")
	u := f.publish(t, data)
	v := f.publish(t, data)
	if e := f.s.Delete(f.memberCtx, f.member.User.ID, u.FileID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.DeleteExpired(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	r, _, e := f.store.Open(context.Background(), digest(data))
	if e != nil {
		t.Fatalf("shared removed %v", e)
	}
	r.Close()
	if _, e = f.s.PromoteToPool(f.adminCtx, f.admin.User.ID, v.FileID); e != nil {
		t.Fatal(e)
	}
	future := time.Now().Add(31 * 24 * time.Hour)
	f.s.SetClock(func() time.Time { return future })
	if _, e = f.s.DeleteExpired(context.Background(), future); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Manifest(f.otherCtx, f.other.User.ID, v.VersionID); e != nil {
		t.Fatalf("promoted expired %v", e)
	}
	if e = f.s.Delete(f.adminCtx, f.admin.User.ID, v.FileID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.DeleteExpired(context.Background(), future); e != nil {
		t.Fatal(e)
	}
	if r, _, e = f.store.Open(context.Background(), digest(data)); !errors.Is(e, os.ErrNotExist) {
		if r != nil {
			r.Close()
		}
		t.Fatalf("orphan retained %v", e)
	}
}
func TestPendingLeaseProtectsDedupAndRevocation(t *testing.T) {
	f := newFixture(t)
	data := []byte("lease")
	u := f.publish(t, data)
	pending, e := f.s.StartUpload(f.memberCtx, f.member.User.ID, f.input(data))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.PutChunk(f.memberCtx, f.member.User.ID, pending.UploadID, 0, bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
	if e = f.s.Delete(f.memberCtx, f.member.User.ID, u.FileID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.DeleteExpired(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.CompleteUpload(f.memberCtx, f.member.User.ID, pending.UploadID); e != nil {
		t.Fatal(e)
	}
	if e = f.g.RemoveMember(f.ownerCtx, f.owner.User.ID, f.group, f.member.User.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.s.OpenChunk(f.memberCtx, f.member.User.ID, pending.VersionID, digest(data)); !errors.Is(e, ErrForbidden) {
		t.Fatalf("removed chunk read %v", e)
	}
}
func TestConcurrentPromotionCleanupAndUpload(t *testing.T) {
	f := newFixture(t)
	data := []byte("concurrent")
	u := f.publish(t, data)
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Add(3)
	go func() { defer wg.Done(); _, e := f.s.PromoteToPool(f.adminCtx, f.admin.User.ID, u.FileID); errs <- e }()
	go func() { defer wg.Done(); _, e := f.s.DeleteExpired(context.Background(), time.Now()); errs <- e }()
	go func() {
		defer wg.Done()
		v, e := f.s.StartUpload(f.memberCtx, f.member.User.ID, f.input(data))
		if e == nil {
			e = f.s.PutChunk(f.memberCtx, f.member.User.ID, v.UploadID, 0, strings.NewReader(string(data)))
		}
		errs <- e
	}()
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	r, _, e := f.s.OpenChunk(f.otherCtx, f.other.User.ID, u.VersionID, digest(data))
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
}

func TestPublicationClockBoundaryAndTwoYearTombstone(t *testing.T) {
	f := newFixture(t)
	published := time.Now().UTC().Add(time.Hour)
	f.s.SetClock(func() time.Time { return published })
	u := f.publish(t, []byte("expires"))
	m, e := f.s.Manifest(f.memberCtx, f.member.User.ID, u.VersionID)
	if e != nil || m.ExpiresAt == nil || !m.ExpiresAt.Equal(published.Add(30*24*time.Hour)) {
		t.Fatalf("publication expiry %+v %v", m, e)
	}
	boundary := *m.ExpiresAt
	f.s.SetClock(func() time.Time { return boundary.Add(-time.Nanosecond) })
	if _, e = f.s.Manifest(f.memberCtx, f.member.User.ID, u.VersionID); e != nil {
		t.Fatal(e)
	}
	f.s.SetClock(func() time.Time { return boundary })
	if _, e = f.s.Manifest(f.memberCtx, f.member.User.ID, u.VersionID); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("exact boundary %v", e)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, e := f.s.PromoteToPool(f.adminCtx, f.admin.User.ID, u.FileID)
		if !errors.Is(e, ErrUnavailable) {
			t.Errorf("expired promotion %v", e)
		}
	}()
	go func() {
		defer wg.Done()
		_, e := f.s.DeleteExpired(context.Background(), boundary)
		if e != nil {
			t.Error(e)
		}
	}()
	wg.Wait()
	var status string
	if e = f.db.QueryRow(`SELECT status FROM file_versions WHERE id=?`, u.VersionID).Scan(&status); e != nil || status != "expired" {
		t.Fatalf("tombstone %s %v", status, e)
	}
	preserved := boundary.AddDate(2, 0, 0)
	if _, e = f.s.DeleteExpired(context.Background(), preserved); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = f.db.QueryRow(`SELECT count(*) FROM file_versions WHERE id=?`, u.VersionID).Scan(&n); e != nil || n != 1 {
		t.Fatalf("boundary metadata %d %v", n, e)
	}
	if _, e = f.s.DeleteExpired(context.Background(), preserved.Add(time.Nanosecond)); e != nil {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT count(*) FROM file_versions WHERE id=?`, u.VersionID).Scan(&n); e != nil || n != 0 {
		t.Fatalf("expired metadata %d %v", n, e)
	}
}
func TestMultiChunkManifestAndMissingBytes(t *testing.T) {
	f := newFixture(t)
	data := []byte("aabbc")
	in := f.input(data)
	in.ChunkSize = 2
	in.Chunks = []Chunk{{0, digest([]byte("aa")), 2}, {1, digest([]byte("bb")), 2}, {2, digest([]byte("c")), 1}}
	u, e := f.s.StartUpload(f.memberCtx, f.member.User.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	for i := 2; i >= 0; i-- {
		start := i * 2
		end := start + 2
		if end > len(data) {
			end = len(data)
		}
		if e = f.s.PutChunk(f.memberCtx, f.member.User.ID, u.UploadID, i, bytes.NewReader(data[start:end])); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = f.s.CompleteUpload(f.memberCtx, f.member.User.ID, u.UploadID); e != nil {
		t.Fatal(e)
	}
	if e = f.store.Delete(context.Background(), in.Chunks[0].Hash); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.s.OpenChunk(f.memberCtx, f.member.User.ID, u.VersionID, in.Chunks[0].Hash); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("missing chunk %v", e)
	}
}

func TestChatConflictRollsBackFilePublication(t *testing.T) {
	f := newFixture(t)
	data := []byte("atomic")
	u, e := f.s.StartUpload(f.memberCtx, f.member.User.ID, f.input(data))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.PutChunk(f.memberCtx, f.member.User.ID, u.UploadID, 0, bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.chat.Append(f.memberCtx, f.member.User.ID, chat.AppendInput{GroupID: f.group, ClientMessageID: "upload:" + u.UploadID, Body: "conflicting text"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.CompleteUpload(f.memberCtx, f.member.User.ID, u.UploadID); !errors.Is(e, chat.ErrConflict) {
		t.Fatalf("conflict %v", e)
	}
	if _, e = f.s.Manifest(f.memberCtx, f.member.User.ID, u.VersionID); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("failed publication visible %v", e)
	}
	var published int
	if e = f.db.QueryRow(`SELECT count(*) FROM audit_events WHERE version_id=? AND action='file.published'`, u.VersionID).Scan(&published); e != nil || published != 0 {
		t.Fatalf("failed audit committed %d %v", published, e)
	}
}

func TestAdminOutsideGroupReplacesPoolVersion(t *testing.T) {
	f := newFixture(t)
	u := f.publish(t, []byte("original"))
	if _, e := f.s.PromoteToPool(f.adminCtx, f.admin.User.ID, u.FileID); e != nil {
		t.Fatal(e)
	}
	if list, e := f.s.List(f.adminCtx, f.admin.User.ID, ListInput{GroupID: f.group}); e != nil || len(list) != 1 {
		t.Fatalf("admin list: %v %v", list, e)
	}
	data := []byte("admin replacement")
	in := f.input(data)
	in.FileID = u.FileID
	next, e := f.s.StartUpload(f.adminCtx, f.admin.User.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.PutChunk(f.adminCtx, f.admin.User.ID, next.UploadID, 0, bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.CompleteUpload(f.adminCtx, f.admin.User.ID, next.UploadID); e != nil {
		t.Fatal(e)
	}
	r, _, e := f.s.OpenChunk(f.otherCtx, f.other.User.ID, next.VersionID, digest(data))
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	got, e := io.ReadAll(r)
	if e != nil || !bytes.Equal(got, data) {
		t.Fatalf("replacement %q %v", got, e)
	}
	if _, e = f.s.chat.List(f.adminCtx, f.admin.User.ID, f.group, "", 50); e == nil {
		t.Fatal("administrator gained ordinary chat history")
	}
	if _, e = f.s.chat.Append(f.adminCtx, f.admin.User.ID, chat.AppendInput{GroupID: f.group, ClientMessageID: "not-member", Body: "text"}); e == nil {
		t.Fatal("administrator gained ordinary text chat")
	}
	page, e := f.s.chat.List(f.ownerCtx, f.owner.User.ID, f.group, "", 50)
	attachments := []chat.Message{}
	for _, m := range page.Messages {
		if m.FileVersionID != nil {
			attachments = append(attachments, m)
		}
	}
	if e != nil || len(attachments) != 2 || attachments[1].SenderID != f.admin.User.ID {
		t.Fatalf("association %+v %v", page, e)
	}
}

type blockingChunkReader struct {
	*io.PipeReader
	started chan struct{}
	once    sync.Once
}

func (r *blockingChunkReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.PipeReader.Read(p)
}
func TestStalledUploadDoesNotBlockCleanupOrOtherTransfers(t *testing.T) {
	f := newFixture(t)
	data := []byte("available")
	published := f.publish(t, data)
	pending, e := f.s.StartUpload(f.memberCtx, f.member.User.ID, f.input([]byte("blocked")))
	if e != nil {
		t.Fatal(e)
	}
	pipe, writer := io.Pipe()
	reader := &blockingChunkReader{PipeReader: pipe, started: make(chan struct{})}
	ctx, cancel := context.WithCancel(f.memberCtx)
	defer cancel()
	defer writer.Close()
	defer pipe.Close()
	blocked := make(chan error, 1)
	go func() { blocked <- f.s.PutChunk(ctx, f.member.User.ID, pending.UploadID, 0, reader) }()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("PUT never started")
	}
	cleanup := make(chan error, 1)
	go func() { _, e := f.s.DeleteExpired(context.Background(), time.Now()); cleanup <- e }()
	select {
	case e := <-cleanup:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stalled PUT blocked cleanup")
	}
	progress := make(chan error, 1)
	go func() {
		r, _, e := f.s.OpenChunk(f.memberCtx, f.member.User.ID, published.VersionID, digest(data))
		if e == nil {
			r.Close()
		}
		if e != nil {
			progress <- e
			return
		}
		u, e := f.s.StartUpload(f.memberCtx, f.member.User.ID, f.input(data))
		if e == nil {
			e = f.s.PutChunk(f.memberCtx, f.member.User.ID, u.UploadID, 0, bytes.NewReader(data))
		}
		progress <- e
	}()
	select {
	case e := <-progress:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("other transfers stalled")
	}
	cancel()
	select {
	case e := <-blocked:
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("canceled PUT %v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled PUT did not stop")
	}
}
func TestChatKeepsExpiredCauseAfterCleanup(t *testing.T) {
	f := newFixture(t)
	u := f.publish(t, []byte("expired"))
	boundary := time.Now().UTC().Add(-time.Hour)
	if _, e := f.db.Exec(`UPDATE file_versions SET expires_at=? WHERE id=?`, stamp(boundary), u.VersionID); e != nil {
		t.Fatal(e)
	}
	check := func(want string) {
		t.Helper()
		page, e := f.s.chat.List(f.ownerCtx, f.owner.User.ID, f.group, "", 50)
		attachments := []chat.Message{}
		for _, m := range page.Messages {
			if m.FileVersionID != nil && *m.FileVersionID == u.VersionID {
				attachments = append(attachments, m)
			}
		}
		if e != nil || len(attachments) != 1 || attachments[0].AttachmentStatus == nil || *attachments[0].AttachmentStatus != want {
			t.Fatalf("attachment %+v %v", page, e)
		}
	}
	check("expired")
	if _, e := f.s.DeleteExpired(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	check("expired")
}
