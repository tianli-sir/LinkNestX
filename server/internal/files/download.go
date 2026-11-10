package files

import (
	"context"
	"errors"
	"github.com/linknestx/linknestx/server/internal/audit"
	"io"
	"os"
)

// OpenChunk opens the immutable CAS handle while cleanup is excluded. Permission
// and lifecycle are checked on every request, including resumed transfers.
func (s *FileService) OpenChunk(ctx context.Context, actorID, versionID, hash string) (io.ReadCloser, ChunkInfo, error) {
	s.storageMu.RLock()
	defer s.storageMu.RUnlock()
	g, err := s.AuthorizeChunk(ctx, actorID, versionID, hash)
	if err != nil {
		return nil, ChunkInfo{}, err
	}
	r, info, err := s.store.Open(ctx, hash)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ChunkInfo{}, ErrUnavailable
	}
	if err != nil {
		return nil, ChunkInfo{}, err
	}
	if info.Size != g.Size {
		r.Close()
		return nil, ChunkInfo{}, ErrChunkSizeMismatch
	}
	return r, info, nil
}

func (s *FileService) RecordServerDelivery(ctx context.Context, actorID, versionID, taskID string, n int64) error {
	return audit.NewService(s.db, s.auth).RecordServerDelivery(ctx, actorID, versionID, taskID, n)
}

// CheckDownload rechecks current session and ACL without holding a transaction
// across a network write. Web streams call it between bounded payload buffers.
func (s *FileService) CheckDownload(ctx context.Context, actor, version string) error {
	if err := s.auth.ValidateActor(ctx, actor); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = activeActor(ctx, tx, actor); err != nil {
		return err
	}
	_, err = s.authorizedVersion(ctx, tx, actor, version)
	return err
}
