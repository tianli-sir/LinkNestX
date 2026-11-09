package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/linknestx/linknestx/server/internal/audit"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/files"
)

type Jobs struct {
	Files    *files.FileService
	Chat     *chat.ChatService
	Audit    *audit.Service
	Interval time.Duration
}
type Report struct {
	Files                         files.DeletionReport
	MessagesExpired, AuditDeleted int64
}

func (j Jobs) RunOnce(ctx context.Context, now time.Time) (Report, error) {
	var r Report
	var err error
	if r.Files, err = j.Files.DeleteExpired(ctx, now); err != nil {
		return r, err
	}
	if r.MessagesExpired, err = j.Chat.ExpireBeforeTwoYears(ctx, now); err != nil {
		return r, err
	}
	r.AuditDeleted, err = j.Audit.DeleteBeforeTwoYears(ctx, now)
	return r, err
}

// Run performs an initial sweep and waits between later sweeps. The owner waits
// for Run to return before closing database/storage resources during shutdown.
func (j Jobs) Run(ctx context.Context) {
	interval := j.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-timer.C:
			r, err := j.RunOnce(ctx, now)
			if err != nil && ctx.Err() == nil {
				slog.Error("retention sweep failed", "error", err)
			} else if err == nil {
				slog.Info("retention sweep completed", "versionsExpired", r.Files.VersionsExpired, "uploadsExpired", r.Files.UploadsExpired, "chunksDeleted", r.Files.ChunksDeleted, "messagesExpired", r.MessagesExpired, "auditDeleted", r.AuditDeleted)
			}
			timer.Reset(interval)
		}
	}
}
