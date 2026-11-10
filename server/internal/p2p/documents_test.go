package p2p

import (
	"bytes"
	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/audit"
	"github.com/linknestx/linknestx/server/internal/files"
	"github.com/linknestx/linknestx/server/internal/notes"
	"testing"
	"time"
)

func TestDocumentPeerSourcesAndAuditFollowCurrentVisibility(t *testing.T) {
	f := setup(t)
	n := notes.New(f.db, f.auth)
	owner, friend := f.sessions[0], f.sessions[1]
	ctx, friendCtx := f.contexts[0], f.contexts[1]
	d, e := n.Create(ctx, owner.User.ID, notes.Input{Title: "Peer guide", Body: "# files", Visibility: "team", Status: "published"})
	if e != nil {
		t.Fatal(e)
	}
	payload := []byte("verified peer bytes")
	u, e := f.files.StartUpload(ctx, owner.User.ID, files.StartUploadInput{DocumentID: d.ID, Name: "guide.bin", Size: int64(len(payload)), SHA256: f.hash, ChunkSize: files.DefaultChunkSize, Chunks: []files.Chunk{{Index: 0, Hash: f.hash, Size: int64(len(payload))}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.files.PutChunk(ctx, owner.User.ID, u.UploadID, 0, bytes.NewReader(payload)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.files.CompleteUpload(ctx, owner.User.ID, u.UploadID); e != nil {
		t.Fatal(e)
	}
	if e = f.service.Announce(friendCtx, friend.User.ID, AnnounceInput{VersionID: u.VersionID, Indexes: []int{0}}); e != nil {
		t.Fatal(e)
	}
	sources, e := f.service.ListSources(ctx, owner.User.ID, u.VersionID)
	if e != nil || len(sources.Sources) != 1 {
		t.Fatalf("sources %+v %v", sources, e)
	}
	report := audit.TransferInput{TaskID: uuid.NewString(), VersionID: u.VersionID, NodeID: "local", Action: "download.completed", Bytes: int64(len(payload))}
	a := audit.NewService(f.db, f.auth)
	if e = a.RecordClientTransfer(friendCtx, report); e != nil {
		t.Fatal(e)
	}
	lo, hi := owner.User.ID, friend.User.ID
	if lo > hi {
		lo, hi = hi, lo
	}
	if _, e = f.db.Exec(`INSERT INTO friendships VALUES(?,?,?)`, lo, hi, time.Now().UTC().Format(time.RFC3339Nano)); e != nil {
		t.Fatal(e)
	}
	d, e = n.Update(ctx, owner.User.ID, d.ID, notes.Input{Title: d.Title, Body: d.Body, Visibility: "friends", Status: "published", Revision: d.Revision})
	if e != nil {
		t.Fatal(e)
	}
	sources, e = f.service.ListSources(ctx, owner.User.ID, u.VersionID)
	if e != nil || len(sources.Sources) != 1 {
		t.Fatalf("friend source %+v %v", sources, e)
	}
	if _, e = f.db.Exec(`DELETE FROM friendships`); e != nil {
		t.Fatal(e)
	}
	sources, e = f.service.ListSources(ctx, owner.User.ID, u.VersionID)
	if e != nil || len(sources.Sources) != 0 {
		t.Fatalf("revoked source %+v %v", sources, e)
	}
	if e = a.RecordClientTransfer(friendCtx, report); e == nil {
		t.Fatal("revoked reader can claim new completion")
	}
	if _, e = f.service.ListSources(friendCtx, friend.User.ID, u.VersionID); e == nil {
		t.Fatal("revoked reader discovers sources")
	}
	if e = f.service.Announce(friendCtx, friend.User.ID, AnnounceInput{VersionID: u.VersionID, Indexes: []int{0}}); e == nil {
		t.Fatal("revoked reader can advertise")
	}
}
