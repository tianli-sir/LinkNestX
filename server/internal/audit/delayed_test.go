package audit_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/audit"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/config"
	"github.com/linknestx/linknestx/server/internal/db"
	"github.com/linknestx/linknestx/server/internal/files"
	"github.com/linknestx/linknestx/server/internal/groups"
	"github.com/linknestx/linknestx/server/internal/p2p"
)

// A completed authorized transfer may be reported after its other participant
// leaves. This exercises real account, group, file, and peer services without
// allowing that historical attribution to authorize another chunk read.
func TestDelayedPeerReportSurvivesOtherParticipantLeaving(t *testing.T) {
	for _, action := range []string{"peer.received", "peer.sent"} {
		for _, departure := range []string{"logout", "removed", "disabled", "device_revoked", "device_deleted"} {
			t.Run(action+"/"+departure, func(t *testing.T) {
				conn, err := db.Open(filepath.Join(t.TempDir(), "delayed.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if err := db.ApplyMigrations(context.Background(), conn); err != nil {
					t.Fatal(err)
				}
				a := auth.NewAuthService(conn, "sign", "refresh")
				if _, err := a.BootstrapAdmin(context.Background(), auth.CreateUserInput{Username: "admin", Password: "initial-password"}); err != nil {
					t.Fatal(err)
				}
				login := func(name string) (auth.Session, context.Context) {
					t.Helper()
					session, err := a.Login(context.Background(), auth.LoginInput{Username: name, Password: "initial-password"})
					if err != nil {
						t.Fatal(err)
					}
					ctx, err := a.AuthenticateContext(context.Background(), session.AccessToken)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := a.ChangePassword(ctx, auth.ChangePasswordInput{CurrentPassword: "initial-password", NewPassword: "changed-password"}); err != nil {
						t.Fatal(err)
					}
					return session, ctx
				}
				admin, adminCtx := login("admin")
				for _, name := range []string{"reporter", "peer"} {
					if _, err := a.CreateUser(adminCtx, auth.CreateUserInput{Username: name, Password: "initial-password"}); err != nil {
						t.Fatal(err)
					}
				}
				reporter, reporterCtx := login("reporter")
				peer, peerCtx := login("peer")
				gs := groups.NewGroupService(conn, a)
				group, err := gs.Create(adminCtx, admin.User.ID, groups.CreateInput{Name: "history"})
				if err != nil {
					t.Fatal(err)
				}
				for _, user := range []auth.Session{reporter, peer} {
					if err := gs.AddMember(adminCtx, admin.User.ID, group.ID, user.User.ID, "member"); err != nil {
						t.Fatal(err)
					}
				}
				store, err := files.NewChunkStore(filepath.Join(t.TempDir(), "chunks"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				fs := files.NewFileService(conn, a, store, "grant")
				ps := p2p.NewService(conn, a, fs, config.Default())
				payload := []byte("authorized historical transfer")
				sum := sha256.Sum256(payload)
				hash := hex.EncodeToString(sum[:])
				upload, err := fs.StartUpload(adminCtx, admin.User.ID, files.StartUploadInput{GroupID: group.ID, Name: "history.bin", Size: int64(len(payload)), SHA256: hash, Chunks: []files.Chunk{{Index: 0, Hash: hash, Size: int64(len(payload))}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := fs.PutChunk(adminCtx, admin.User.ID, upload.UploadID, 0, bytes.NewReader(payload)); err != nil {
					t.Fatal(err)
				}
				if _, err := fs.CompleteUpload(adminCtx, admin.User.ID, upload.UploadID); err != nil {
					t.Fatal(err)
				}
				sender, senderCtx, receiver, receiverCtx := peer, peerCtx, reporter, reporterCtx
				if action == "peer.sent" {
					sender, senderCtx, receiver, receiverCtx = reporter, reporterCtx, peer, peerCtx
				}
				if err := ps.Announce(senderCtx, sender.User.ID, p2p.AnnounceInput{VersionID: upload.VersionID, Indexes: []int{0}}); err != nil {
					t.Fatal(err)
				}
				grant, err := ps.CreateChunkGrant(receiverCtx, receiver.User.ID, upload.VersionID, hash)
				if err != nil {
					t.Fatal(err)
				}
				if err := ps.ValidateChunkGrant(senderCtx, grant); err != nil {
					t.Fatal(err)
				}
				reader, _, err := fs.OpenChunk(receiverCtx, receiver.User.ID, upload.VersionID, hash)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(reader)
				reader.Close()
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("verified bytes=%q err=%v", got, err)
				}
				connection := p2p.ConnectionAudit{ConnectionID: uuid.NewString(), VersionID: upload.VersionID, RequesterDeviceID: receiver.DeviceID, SourceDeviceID: sender.DeviceID, RequesterUserID: receiver.User.ID}
				if err := ps.RecordConnection(receiverCtx, connection); err != nil {
					t.Fatal(err)
				}
				connection.Ended, connection.Outcome = true, "closed"
				if err := ps.RecordConnection(receiverCtx, connection); err != nil {
					t.Fatal(err)
				}

				switch departure {
				case "logout":
					err = a.RevokeSession(peerCtx, peer.ID)
				case "removed":
					err = gs.RemoveMember(adminCtx, admin.User.ID, group.ID, peer.User.ID)
				case "disabled":
					err = a.SetUserDisabled(adminCtx, peer.User.ID, true)
				case "device_revoked":
					err = a.AdminRevokeDevice(adminCtx, peer.DeviceID)
				case "device_deleted":
					_, err = conn.Exec(`DELETE FROM devices WHERE id=?`, peer.DeviceID)
				}
				if err != nil {
					t.Fatal(err)
				}
				service := audit.NewService(conn, a)
				pending := audit.TransferInput{TaskID: uuid.NewString(), VersionID: upload.VersionID, NodeID: peer.DeviceID, Action: action, Bytes: int64(len(payload))}
				for _, count := range []int64{int64(len(payload)), 1, int64(len(payload))} {
					pending.Bytes = count
					if err := service.RecordClientTransfer(reporterCtx, pending); err != nil {
						t.Fatalf("delayed history rejected after peer %s: %v", departure, err)
					}
				}
				later := audit.TransferInput{TaskID: uuid.NewString(), VersionID: upload.VersionID, NodeID: "server", Action: "transfer.completed", Bytes: int64(len(payload))}
				if err := service.RecordClientTransfer(reporterCtx, later); err != nil {
					t.Fatalf("unrelated later report: %v", err)
				}
				page, err := service.Page(adminCtx, audit.Filter{Action: action})
				if err != nil || page.Total != 1 {
					t.Fatalf("peer aggregates=%+v err=%v", page, err)
				}
				event := page.Events[0]
				if event.ActorID != reporter.User.ID || event.DeviceID != reporter.DeviceID || event.NodeID != peer.DeviceID || event.TaskID != pending.TaskID || event.Bytes != int64(len(payload)) || event.Source != "client-reported" {
					t.Fatalf("history attribution=%+v", event)
				}
				page, err = service.Page(adminCtx, audit.Filter{Action: "transfer.completed"})
				if err != nil || page.Total != 1 {
					t.Fatalf("later report=%+v err=%v", page, err)
				}
				if err := ps.ValidateChunkGrant(senderCtx, grant); err == nil {
					t.Fatal("historical report authorized another peer read")
				}
				if reader, _, err := fs.OpenChunk(peerCtx, peer.User.ID, upload.VersionID, hash); err == nil {
					reader.Close()
					t.Fatal("departed peer retained chunk access")
				}
				invalid := pending
				invalid.NodeID = uuid.NewString()
				if err := service.RecordClientTransfer(reporterCtx, invalid); !errors.Is(err, audit.ErrTransferForbidden) {
					t.Fatalf("unknown peer: %v", err)
				}
				invalid = pending
				invalid.Bytes = int64(len(payload)) + 1
				if err := service.RecordClientTransfer(reporterCtx, invalid); !errors.Is(err, audit.ErrInvalidInput) {
					t.Fatalf("oversized count: %v", err)
				}
				if err := gs.RemoveMember(adminCtx, admin.User.ID, group.ID, reporter.User.ID); err != nil {
					t.Fatal(err)
				}
				if err := service.RecordClientTransfer(reporterCtx, pending); !errors.Is(err, audit.ErrTransferForbidden) {
					t.Fatalf("removed reporter: %v", err)
				}
				if err := a.RevokeSession(reporterCtx, reporter.ID); err != nil {
					t.Fatal(err)
				}
				if err := service.RecordClientTransfer(reporterCtx, pending); !errors.Is(err, auth.ErrSessionRevoked) {
					t.Fatalf("revoked reporter: %v", err)
				}
			})
		}
	}
}
