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
