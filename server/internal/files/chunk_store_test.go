package files_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/linknestx/linknestx/server/internal/files"
)

func TestChunkStoreStreamsVerifiedContent(t *testing.T) {
	for _, data := range [][]byte{nil, bytes.Repeat([]byte("streaming chunk\n"), 650000)} {
		t.Run(fmt.Sprintf("%d_bytes", len(data)), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			store := newStore(t, root)
			hash := contentHash(data)
			if err := store.Put(context.Background(), hash, &boundedReader{reader: bytes.NewReader(data)}, int64(len(data))); err != nil {
				t.Fatalf("Put: %v", err)
			}
			reader, info, err := store.Open(context.Background(), hash)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer reader.Close()
			got, err := io.ReadAll(reader)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("stored content differs: length %d, error %v", len(got), err)
			}
			if info.Hash != hash || info.Size != int64(len(data)) {
				t.Fatalf("unexpected metadata: %+v", info)
			}
			for _, path := range []string{root, filepath.Join(root, "chunks"), filepath.Join(root, "chunks", hash[:2]), filepath.Join(root, "chunks", hash[:2], hash[2:4])} {
				assertMode(t, path, 0700)
			}
			assertMode(t, chunkPath(root, hash), 0600)
		})
	}
}

func TestChunkStoreRejectsInvalidHashes(t *testing.T) {
	for _, hash := range []string{"", "a", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64), "../" + strings.Repeat("a", 61), "/" + strings.Repeat("a", 63), strings.Repeat("a", 63) + "\x00"} {
		t.Run(fmt.Sprintf("%q", hash), func(t *testing.T) {
			root := t.TempDir()
			store := newStore(t, root)
			if err := store.Put(context.Background(), hash, strings.NewReader("x"), 1); !errors.Is(err, files.ErrInvalidChunkHash) {
				t.Fatalf("Put invalid hash error: %v", err)
			}
			if reader, _, err := store.Open(context.Background(), hash); !errors.Is(err, files.ErrInvalidChunkHash) {
				if reader != nil {
					reader.Close()
				}
				t.Fatalf("Open invalid hash error: %v", err)
			}
			if err := store.Delete(context.Background(), hash); !errors.Is(err, files.ErrInvalidChunkHash) {
				t.Fatalf("Delete invalid hash error: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid hash changed filesystem: %v, %v", entries, err)
			}
		})
	}
}

func TestChunkStoreRejectsUnverifiedContent(t *testing.T) {
	data := []byte("expected data")
	readError := errors.New("source failed")
	for _, test := range []struct {
		name   string
		hash   string
		size   int64
		reader io.Reader
	}{
		{"short", contentHash(data), int64(len(data) + 1), bytes.NewReader(data)},
		{"long", contentHash(data), int64(len(data) - 1), bytes.NewReader(data)},
		{"negative", contentHash(data), -1, bytes.NewReader(data)},
		{"incorrect_hash", contentHash([]byte("different")), int64(len(data)), bytes.NewReader(data)},
		{"read_error", contentHash(data), int64(len(data)), io.MultiReader(strings.NewReader("exp"), errorReader{readError})},
		{"error_after_declared_size", contentHash(data), int64(len(data)), io.MultiReader(bytes.NewReader(data), errorReader{readError})},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := newStore(t, root)
			if err := store.Put(context.Background(), test.hash, test.reader, test.size); err == nil {
				t.Fatal("Put accepted unverified input")
			}
			assertNoFiles(t, root)
		})
	}
}

func TestChunkStoreDoesNotPublishDuringInput(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	data := bytes.Repeat([]byte("chunk"), 30000)
	hash := contentHash(data)
	reader := callbackReader{reader: bytes.NewReader(data), afterRead: func() {
		if _, err := os.Stat(chunkPath(root, hash)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("chunk visible before input verification: %v", err)
		}
	}}
	if err := store.Put(context.Background(), hash, reader, int64(len(data))); err != nil {
		t.Fatal(err)
	}
}

func TestChunkStoreHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	hash := contentHash([]byte("data"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Put(ctx, hash, strings.NewReader("data"), 4); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Put: %v", err)
	}
	if reader, _, err := store.Open(ctx, hash); !errors.Is(err, context.Canceled) {
		if reader != nil {
			reader.Close()
		}
		t.Fatalf("canceled Open: %v", err)
	}
	if err := store.Delete(ctx, hash); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Delete: %v", err)
	}
	assertNoFiles(t, root)
}

func TestChunkStoreCancelsDuringInput(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	data := bytes.Repeat([]byte("data"), 100000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := callbackReader{reader: bytes.NewReader(data), afterRead: cancel}
	if err := store.Put(ctx, contentHash(data), reader, int64(len(data))); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put did not honor cancellation during input: %v", err)
	}
	assertNoFiles(t, root)
}

func TestChunkStoreCancelsOpenReader(t *testing.T) {
	store := newStore(t, t.TempDir())
	hash := contentHash([]byte("data"))
	if err := store.Put(context.Background(), hash, strings.NewReader("data"), 4); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, _, err := store.Open(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	cancel()
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("open reader did not honor cancellation: %v", err)
	}
}

func TestChunkStoreDeletionIsIdempotent(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	hash := contentHash([]byte("data"))
	if err := store.Delete(context.Background(), hash); err != nil {
		t.Fatalf("delete absent chunk: %v", err)
	}
	if err := store.Put(context.Background(), hash, strings.NewReader("data"), 4); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.Delete(context.Background(), hash); err != nil {
			t.Fatalf("delete #%d: %v", i+1, err)
		}
	}
	if reader, _, err := store.Open(context.Background(), hash); !errors.Is(err, os.ErrNotExist) {
		if reader != nil {
			reader.Close()
		}
		t.Fatalf("deleted chunk remained visible: %v", err)
	}
	assertNoFiles(t, root)
}

func TestChunkStoreDuplicateWritesStillVerifyInput(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	data := []byte("original")
	hash := contentHash(data)
	for i := 0; i < 2; i++ {
		if err := store.Put(context.Background(), hash, bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Put(context.Background(), hash, strings.NewReader("modified"), 8); !errors.Is(err, files.ErrChunkHashMismatch) {
		t.Fatalf("duplicate hash bypassed input verification: %v", err)
	}
	if err := store.Put(context.Background(), hash, bytes.NewReader(data), 7); !errors.Is(err, files.ErrChunkSizeMismatch) {
		t.Fatalf("duplicate hash bypassed size verification: %v", err)
	}
	got, err := os.ReadFile(chunkPath(root, hash))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("duplicate writes changed chunk: %q, %v", got, err)
	}
	assertOnlyChunk(t, root, hash)
}

func TestChunkStoreConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	stores := []*files.ChunkStore{newStore(t, root), newStore(t, root)}
	data := bytes.Repeat([]byte("parallel"), 70000)
	hash := contentHash(data)
	start := make(chan struct{})
	errors := make(chan error, 16)
	var workers sync.WaitGroup
	for i := 0; i < cap(errors); i++ {
		workers.Add(1)
		go func(store *files.ChunkStore) {
			defer workers.Done()
			<-start
			errors <- store.Put(context.Background(), hash, bytes.NewReader(data), int64(len(data)))
		}(stores[i%len(stores)])
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Errorf("concurrent Put: %v", err)
		}
	}
	got, err := os.ReadFile(chunkPath(root, hash))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("concurrent writes produced incorrect content: %d bytes, %v", len(got), err)
	}
	assertOnlyChunk(t, root, hash)
}

func TestChunkStoreRejectsCorruptExistingChunk(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	data := []byte("original")
	hash := contentHash(data)
	if err := store.Put(context.Background(), hash, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chunkPath(root, hash), []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), hash, bytes.NewReader(data), int64(len(data))); !errors.Is(err, files.ErrChunkHashMismatch) {
		t.Fatalf("corrupt existing chunk was accepted as idempotent: %v", err)
	}
	assertOnlyChunk(t, root, hash)
}

func TestChunkStoreRejectsSymlinkRoot(t *testing.T) {
	outside := t.TempDir()
	root := filepath.Join(t.TempDir(), "store")
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	store, err := files.NewChunkStore(root + string(os.PathSeparator))
	if store != nil {
		store.Close()
	}
	if err == nil {
		t.Fatal("store followed symlink root")
	}
}

func TestChunkStoreDoesNotFollowSymlinksOutOfRoot(t *testing.T) {
	data := []byte("protected")
	hash := contentHash(data)
	for _, depth := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprintf("depth_%d", depth), func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			store := newStore(t, root)
			parts := []string{"chunks", hash[:2], hash[2:4], hash}
			link := filepath.Join(append([]string{root}, parts[:depth+1]...)...)
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			target := outside
			if depth == 3 {
				target = filepath.Join(outside, "protected")
				if err := os.WriteFile(target, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if err := store.Put(context.Background(), hash, bytes.NewReader(data), int64(len(data))); err == nil {
				t.Error("Put accepted path escaping through symlink")
			}
			if reader, _, err := store.Open(context.Background(), hash); err == nil {
				reader.Close()
				t.Error("Open followed symlink")
			}
			_ = store.Delete(context.Background(), hash)
			if depth == 3 {
				got, err := os.ReadFile(target)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("outside file changed: %q, %v", got, err)
				}
			} else {
				assertNoFiles(t, outside)
			}
		})
	}
}

func TestChunkStoreRemainsPinnedAfterRootReplacement(t *testing.T) {
	parent, outside := t.TempDir(), t.TempDir()
	root, moved := filepath.Join(parent, "store"), filepath.Join(parent, "moved")
	store := newStore(t, root)
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	hash := contentHash([]byte("data"))
	if err := store.Put(context.Background(), hash, strings.NewReader("data"), 4); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(chunkPath(moved, hash))
	if err != nil || string(got) != "data" {
		t.Fatalf("write left pinned root: %q, %v", got, err)
	}
	if err := store.Delete(context.Background(), hash); err != nil {
		t.Fatal(err)
	}
	assertNoFiles(t, outside)
	assertNoFiles(t, moved)
}

func TestChunkStoreRejectsDirectoryInPlaceOfChunk(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	hash := contentHash([]byte("data"))
	if err := os.MkdirAll(chunkPath(root, hash), 0700); err != nil {
		t.Fatal(err)
	}
	if reader, _, err := store.Open(context.Background(), hash); err == nil {
		reader.Close()
		t.Error("Open accepted directory as chunk")
	}
	if err := store.Put(context.Background(), hash, strings.NewReader("data"), 4); err == nil {
		t.Error("Put accepted directory as existing chunk")
	}
	if err := store.Delete(context.Background(), hash); err == nil {
		t.Error("Delete accepted directory as chunk")
	}
}

func TestChunkStoreCloseReleasesStoreButNotOpenReaders(t *testing.T) {
	store := newStore(t, t.TempDir())
	hash := contentHash([]byte("data"))
	if err := store.Put(context.Background(), hash, strings.NewReader("data"), 4); err != nil {
		t.Fatal(err)
	}
	reader, _, err := store.Open(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "data" {
		t.Fatalf("Close invalidated open reader: %q, %v", got, err)
	}
	if err := store.Put(context.Background(), hash, strings.NewReader("data"), 4); err == nil {
		t.Error("Put on closed store succeeded")
	}
	if reader, _, err := store.Open(context.Background(), hash); err == nil {
		reader.Close()
		t.Error("Open on closed store succeeded")
	}
	if err := store.Delete(context.Background(), hash); err == nil {
		t.Error("Delete on closed store succeeded")
	}
}

func TestChunkStoreRejectsNonPrivateExistingChunk(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	hash := contentHash([]byte("data"))
	if err := store.Put(context.Background(), hash, strings.NewReader("data"), 4); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(chunkPath(root, hash), 0644); err != nil {
		t.Fatal(err)
	}
	if reader, _, err := store.Open(context.Background(), hash); !errors.Is(err, files.ErrUnsafeChunkPath) {
		if reader != nil {
			reader.Close()
		}
		t.Fatalf("Open accepted non-private chunk: %v", err)
	}
	if err := store.Put(context.Background(), hash, strings.NewReader("data"), 4); !errors.Is(err, files.ErrUnsafeChunkPath) {
		t.Fatalf("Put accepted non-private existing chunk: %v", err)
	}
}

func newStore(t *testing.T, root string) *files.ChunkStore {
	t.Helper()
	store, err := files.NewChunkStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func chunkPath(root, hash string) string {
	return filepath.Join(root, "chunks", hash[:2], hash[2:4], hash)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, info.Mode().Perm(), want)
	}
}

func assertNoFiles(t *testing.T, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Errorf("unexpected artifact: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertOnlyChunk(t *testing.T, root, hash string) {
	t.Helper()
	count := 0
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			count++
			if path != chunkPath(root, hash) {
				t.Errorf("unexpected artifact: %s", path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one stored chunk, found %d", count)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type callbackReader struct {
	reader    io.Reader
	afterRead func()
}

func (r callbackReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.afterRead()
	return n, err
}

// Catch accidental whole-chunk buffering while still exercising a real stream.
type boundedReader struct{ reader io.Reader }

func (r *boundedReader) Read(p []byte) (int, error) {
	if len(p) > 64*1024 {
		return 0, fmt.Errorf("read buffer is unbounded: %d bytes", len(p))
	}
	return r.reader.Read(p)
}
