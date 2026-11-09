package files

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"os"
	"time"

	"github.com/linknestx/linknestx/server/internal/chat"
)

// PutChunk never treats knowledge of a hash as proof of ownership. Every upload
// requires its own verified full stream and durable receipt.
func (s *FileService) PutChunk(ctx context.Context, actorID, uploadID string, index int, r io.Reader) error {
	u, err := s.GetUpload(ctx, actorID, uploadID)
	if err != nil {
		return err
	}
	if u.Completed {
		return ErrUnavailable
	}
	var c Chunk
	c.Index = index
	if err = s.db.QueryRowContext(ctx, `SELECT hash,size FROM file_chunks WHERE version_id=? AND chunk_index=?`, u.VersionID, index).Scan(&c.Hash, &c.Size); err == sql.ErrNoRows {
		return ErrInvalidInput
	} else if err != nil {
		return err
	}
	staged, err := stageUploadChunk(ctx, r, c)
	if err != nil {
		return err
	}
	defer func() { staged.Close(); os.Remove(staged.Name()) }()
	// Network input has finished. Only bounded local verified bytes enter the
	// CAS publication lock; the existing pending-upload lease protects references.
	s.storageMu.RLock()
	defer s.storageMu.RUnlock()
	if err = s.auth.ValidateActor(ctx, actorID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	u, err = s.upload(ctx, tx, actorID, uploadID)
	if err != nil {
		return err
	}
	if u.Completed {
		return nil
	}
	if err = s.store.Put(ctx, c.Hash, staged, c.Size); err != nil {
		return err
	}
	at := s.now()
	receipt, err := tx.ExecContext(ctx, `INSERT INTO upload_receipts(upload_id,chunk_index,received_at) VALUES(?,?,?) ON CONFLICT(upload_id,chunk_index) DO NOTHING`, uploadID, index, stamp(at))
	if err != nil {
		return err
	}
	accepted, _ := receipt.RowsAffected()
	if accepted > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO transfer_records(actor_id,version_id,task_id,node_id,action,bytes,created_at,updated_at) VALUES(?,?,?,'server','server.received',?,?,?) ON CONFLICT(actor_id,version_id,task_id,node_id,action) DO UPDATE SET bytes=bytes+excluded.bytes,updated_at=excluded.updated_at`, actorID, u.VersionID, uploadID, c.Size, stamp(at), stamp(at)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE upload_sessions SET expires_at=? WHERE id=?`, stamp(at.Add(7*24*time.Hour)), uploadID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *FileService) CompleteUpload(ctx context.Context, actorID, uploadID string) (FileVersion, error) {
	u, err := s.GetUpload(ctx, actorID, uploadID)
	if err != nil {
		return FileVersion{}, err
	}
	if u.Completed {
		return s.Manifest(ctx, actorID, u.VersionID)
	}
	if len(u.MissingIndexes) > 0 {
		return FileVersion{}, ErrIncomplete
	}
	var size int64
	var expected, name, groupID string
	if err = s.db.QueryRowContext(ctx, `SELECT v.size,v.sha256,v.name,f.group_id FROM file_versions v JOIN files f ON f.id=v.file_id WHERE v.id=?`, u.VersionID).Scan(&size, &expected, &name, &groupID); err != nil {
		return FileVersion{}, err
	}
	chunks, err := loadChunks(ctx, s.db, u.VersionID)
	if err != nil {
		return FileVersion{}, err
	}
	whole := sha256.New()
	var total int64
	for _, c := range chunks {
		s.storageMu.RLock()
		r, info, e := s.store.Open(ctx, c.Hash)
		s.storageMu.RUnlock()
		if e != nil {
			return FileVersion{}, e
		}
		if info.Size != c.Size {
			r.Close()
			return FileVersion{}, ErrChunkSizeMismatch
		}
		n, e := io.CopyBuffer(whole, io.LimitReader(r, c.Size+1), make([]byte, 32*1024))
		closeErr := r.Close()
		if e != nil {
			return FileVersion{}, e
		}
		if closeErr != nil {
			return FileVersion{}, closeErr
		}
		if n != c.Size {
			return FileVersion{}, ErrChunkSizeMismatch
		}
		total += n
	}
	if total != size {
		return FileVersion{}, ErrChunkSizeMismatch
	}
	if hex.EncodeToString(whole.Sum(nil)) != expected {
		return FileVersion{}, ErrChunkHashMismatch
	}
	_, err = s.chat.AppendAttachment(ctx, actorID, chat.AttachmentInput{GroupID: groupID, ClientMessageID: "upload:" + uploadID, FileID: u.FileID, FileVersionID: u.VersionID, Name: name}, func(tx *sql.Tx) error {
		current, e := s.upload(ctx, tx, actorID, uploadID)
		if e != nil {
			return e
		}
		if current.Completed {
			return nil
		}
		if len(current.MissingIndexes) > 0 {
			return ErrIncomplete
		}
		f, e := loadFile(ctx, tx, u.FileID)
		if e != nil {
			return e
		}
		at := s.now().UTC()
		var expiry any = stamp(at.Add(30 * 24 * time.Hour))
		if f.visibility == "pool" {
			expiry = nil
		}
		if _, e = tx.ExecContext(ctx, `UPDATE file_versions SET status='published',published_at=?,expires_at=? WHERE id=? AND status='pending'`, stamp(at), expiry, u.VersionID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE files SET current_version_id=?,name=? WHERE id=?`, u.VersionID, name, u.FileID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE upload_sessions SET completed_at=? WHERE id=?`, stamp(at), uploadID); e != nil {
			return e
		}
		return recordAudit(ctx, tx, actorID, "file.published", u.FileID, u.VersionID, at)
	})
	if err != nil {
		return FileVersion{}, err
	}
	return s.Manifest(ctx, actorID, u.VersionID)
}
