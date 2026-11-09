package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// stageUploadChunk consumes client input without holding any shared CAS/DB
// locks. Its private0600 temporary file is removed on every ordinary exit. The
// HTTP boundary supplies a finite read deadline; closable sources also unblock
// when their context is canceled. Generic embedded readers must obey context or
// provide Close to support interrupting an in-progress Read.
func stageUploadChunk(ctx context.Context, source io.Reader, c Chunk) (file *os.File, err error) {
	file, err = os.CreateTemp("", "linknestx-upload-*")
	if err != nil {
		return nil, err
	}
	staged := file
	complete := false
	defer func() {
		if !complete {
			staged.Close()
			os.Remove(staged.Name())
		}
	}()
	if closer, ok := source.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
		defer stop()
	}
	reader := contextReader{ctx: ctx, reader: source}
	hash := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(file, hash), io.LimitReader(reader, c.Size), make([]byte, 32*1024))
	if err != nil {
		return nil, err
	}
	if n != c.Size {
		return nil, ErrChunkSizeMismatch
	}
	var extra [1]byte
	if n, e := io.ReadFull(reader, extra[:]); n != 0 {
		return nil, ErrChunkSizeMismatch
	} else if e != io.EOF {
		return nil, e
	}
	if hex.EncodeToString(hash.Sum(nil)) != c.Hash {
		return nil, ErrChunkHashMismatch
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	complete = true
	return file, nil
}
