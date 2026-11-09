package files

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var (
	ErrInvalidChunkHash  = errors.New("chunk hash must be lowercase hexadecimal SHA-256")
	ErrInvalidChunkSize  = errors.New("chunk size must be nonnegative")
	ErrChunkSizeMismatch = errors.New("chunk size does not match declaration")
	ErrChunkHashMismatch = errors.New("chunk content does not match hash")
	ErrUnsafeChunkPath   = errors.New("chunk path must be a regular file in a private directory")
)

type ChunkInfo struct {
	Hash string
	Size int64
}

// ChunkStore owns a directory handle and is safe for concurrent use. Call Close
// when the store is no longer needed. Content is published with an atomic hard
// link so concurrent puts cannot replace a previously published chunk.
type ChunkStore struct{ root *os.Root }

func NewChunkStore(root string) (*ChunkStore, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(filepath.Dir(root))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := filepath.Base(root)
	if err := parent.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, ErrUnsafeChunkPath
	}
	dir, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := dir.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		dir.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrUnsafeChunkPath
	}
	if err := dir.Chmod(".", 0700); err != nil {
		dir.Close()
		return nil, err
	}
	return &ChunkStore{root: dir}, nil
}

// Close releases the store's directory handle. Open chunk readers remain usable.
func (s *ChunkStore) Close() error { return s.root.Close() }

func (s *ChunkStore) Delete(ctx context.Context, hash string) error {
	if err := checkRequest(ctx, hash); err != nil {
		return err
	}
	dir, err := s.chunkDir(hash, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := dir.Lstat(hash)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrUnsafeChunkPath
	}
	if err := dir.Remove(hash); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Put verifies the full incoming stream before publishing a chunk. It accepts any
// nonnegative declared size; callers impose their own chunk-size policy. Context
// cancellation is checked between reads; readers must bound their own blocking.
func (s *ChunkStore) Put(ctx context.Context, hash string, r io.Reader, size int64) error {
	if err := checkRequest(ctx, hash); err != nil {
		return err
	}
	if size < 0 {
		return ErrInvalidChunkSize
	}
	dir, err := s.chunkDir(hash, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	name := ".chunk-" + rand.Text()
	tmp, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer dir.Remove(name)
	defer tmp.Close()
	source := contextReader{ctx: ctx, reader: r}
	digest := sha256.New()
	written, err := io.CopyBuffer(io.MultiWriter(tmp, digest), io.LimitReader(source, size), make([]byte, 32*1024))
	if err != nil {
		return err
	}
	if written != size {
		return fmt.Errorf("%w: expected %d bytes, received %d", ErrChunkSizeMismatch, size, written)
	}
	var extra [1]byte
	if n, err := io.ReadFull(source, extra[:]); n != 0 {
		return ErrChunkSizeMismatch
	} else if err != io.EOF {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != hash {
		return ErrChunkHashMismatch
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := dir.Link(name, hash); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		return verifyExistingChunk(ctx, dir, hash, size)
	}
	return nil
}

func (s *ChunkStore) Open(ctx context.Context, hash string) (io.ReadCloser, ChunkInfo, error) {
	if err := checkRequest(ctx, hash); err != nil {
		return nil, ChunkInfo{}, err
	}
	dir, err := s.chunkDir(hash, false)
	if err != nil {
		return nil, ChunkInfo{}, err
	}
	defer dir.Close()
	file, info, err := openRegularChunk(dir, hash)
	if err != nil {
		return nil, ChunkInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		file.Close()
		return nil, ChunkInfo{}, err
	}
	return contextReadCloser{Reader: contextReader{ctx: ctx, reader: file}, Closer: file}, ChunkInfo{Hash: hash, Size: info.Size()}, nil
}

func openRegularChunk(dir *os.Root, hash string) (*os.File, os.FileInfo, error) {
	before, err := dir.Lstat(hash)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0600 {
		return nil, nil, ErrUnsafeChunkPath
	}
	file, err := dir.Open(hash)
	if err != nil {
		return nil, nil, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || after.Mode().Perm() != 0600 || !os.SameFile(before, after) {
		file.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, ErrUnsafeChunkPath
	}
	return file, after, nil
}

func verifyExistingChunk(ctx context.Context, dir *os.Root, hash string, size int64) error {
	file, info, err := openRegularChunk(dir, hash)
	if err != nil {
		return err
	}
	defer file.Close()
	if info.Size() != size {
		return ErrChunkSizeMismatch
	}
	digest := sha256.New()
	n, err := io.CopyBuffer(digest, contextReader{ctx: ctx, reader: file}, make([]byte, 32*1024))
	if err != nil {
		return err
	}
	if n != size {
		return ErrChunkSizeMismatch
	}
	if hex.EncodeToString(digest.Sum(nil)) != hash {
		return ErrChunkHashMismatch
	}
	return nil
}

func checkRequest(ctx context.Context, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(hash) != sha256.Size*2 {
		return ErrInvalidChunkHash
	}
	for _, c := range hash {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ErrInvalidChunkHash
		}
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, err
}

type contextReadCloser struct {
	io.Reader
	io.Closer
}

func (s *ChunkStore) chunkDir(hash string, create bool) (*os.Root, error) {
	dir := s.root
	for _, name := range []string{"chunks", hash[:2], hash[2:4]} {
		if create {
			if err := dir.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				if dir != s.root {
					dir.Close()
				}
				return nil, err
			}
		}
		info, err := dir.Lstat(name)
		if err != nil || !info.IsDir() {
			if dir != s.root {
				dir.Close()
			}
			if err != nil {
				return nil, err
			}
			return nil, ErrUnsafeChunkPath
		}
		next, err := dir.OpenRoot(name)
		if dir != s.root {
			dir.Close()
		}
		if err != nil {
			return nil, err
		}
		dir = next
		after, err := dir.Stat(".")
		if err != nil || !os.SameFile(info, after) {
			dir.Close()
			if err != nil {
				return nil, err
			}
			return nil, ErrUnsafeChunkPath
		}
		if create {
			if err := dir.Chmod(".", 0700); err != nil {
				dir.Close()
				return nil, err
			}
		}
	}
	return dir, nil
}
