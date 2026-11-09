package files

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/groups"
)

const (
	MaxFileSize       int64 = 20_000_000_000
	DefaultChunkSize  int64 = 4_194_304
	MaxChunkSize      int64 = 16_777_216
	MaxManifestChunks       = 20000
	timestampLayout         = "2006-01-02T15:04:05.000000000Z"
)

var (
	ErrInvalidInput = errors.New("invalid file input")
	ErrForbidden    = errors.New("file access denied")
	ErrUnavailable  = errors.New("file unavailable")
	ErrIncomplete   = errors.New("upload is incomplete")
	ErrInvalidGrant = errors.New("invalid chunk grant")
)

type Chunk struct {
	Index int    `json:"index"`
	Hash  string `json:"hash"`
	Size  int64  `json:"size"`
}
type StartUploadInput struct {
	GroupID   string  `json:"groupId"`
	Name      string  `json:"name"`
	Size      int64   `json:"size"`
	SHA256    string  `json:"sha256"`
	ChunkSize int64   `json:"chunkSize"`
	Chunks    []Chunk `json:"chunks"`
	FileID    string  `json:"fileId,omitempty"`
}
type UploadSession struct {
	UploadID       string `json:"uploadId"`
	FileID         string `json:"fileId"`
	VersionID      string `json:"versionId"`
	MissingIndexes []int  `json:"missingIndexes"`
	Completed      bool   `json:"completed"`
}
type File struct {
	ID               string     `json:"id"`
	GroupID          string     `json:"groupId"`
	Name             string     `json:"name"`
	CreatedBy        string     `json:"createdBy"`
	Visibility       string     `json:"visibility"`
	CurrentVersionID string     `json:"currentVersionId"`
	CreatedAt        time.Time  `json:"createdAt"`
	ExpiresAt        *time.Time `json:"expiresAt"`
	Status           string     `json:"status"`
	Size             int64      `json:"size"`
	SHA256           string     `json:"sha256"`
}
type FileVersion struct {
	FileID     string     `json:"fileId"`
	VersionID  string     `json:"versionId"`
	Name       string     `json:"name"`
	Size       int64      `json:"size"`
	SHA256     string     `json:"sha256"`
	ChunkSize  int64      `json:"chunkSize"`
	Chunks     []Chunk    `json:"chunks"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	Visibility string     `json:"visibility"`
	CreatedAt  time.Time  `json:"createdAt"`
	CreatedBy  string     `json:"createdBy"`
	Status     string     `json:"status"`
}
type ListInput struct {
	GroupID string
	Scope   string
}
type FileService struct {
	db         *sql.DB
	auth       *auth.AuthService
	store      *ChunkStore
	signingKey []byte
	now        func() time.Time
	storageMu  sync.RWMutex
	chat       *chat.ChatService
}

func NewFileService(conn *sql.DB, a *auth.AuthService, store *ChunkStore, signingKey string, messages ...*chat.ChatService) *FileService {
	c := chat.NewChatService(conn, a, groups.NewGroupService(conn, a))
	if len(messages) > 0 && messages[0] != nil {
		c = messages[0]
	}
	return &FileService{db: conn, auth: a, store: store, signingKey: []byte(signingKey), now: time.Now, chat: c}
}
func (s *FileService) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}
func stamp(t time.Time) string { return t.UTC().Format(timestampLayout) }
func validateInput(in *StartUploadInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.GroupID == "" || in.Name == "" || !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) > 255 || strings.ContainsAny(in.Name, "/\\") {
		return ErrInvalidInput
	}
	for _, r := range in.Name {
		if unicode.IsControl(r) {
			return ErrInvalidInput
		}
	}
	if in.Size < 0 || in.Size > MaxFileSize || checkRequest(context.Background(), in.SHA256) != nil {
		return ErrInvalidInput
	}
	if in.ChunkSize == 0 {
		in.ChunkSize = DefaultChunkSize
	}
	if in.ChunkSize < 1 || in.ChunkSize > MaxChunkSize || len(in.Chunks) > MaxManifestChunks {
		return ErrInvalidInput
	}
	expected := (in.Size + in.ChunkSize - 1) / in.ChunkSize
	if int64(len(in.Chunks)) != expected {
		return ErrInvalidInput
	}
	for i, c := range in.Chunks {
		size := in.ChunkSize
		if i == len(in.Chunks)-1 {
			size = in.Size - int64(i)*in.ChunkSize
		}
		if c.Index != i || c.Size != size || checkRequest(context.Background(), c.Hash) != nil {
			return ErrInvalidInput
		}
	}
	if in.Size == 0 && in.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		return ErrInvalidInput
	}
	return nil
}
func (s *FileService) StartUpload(ctx context.Context, actorID string, in StartUploadInput) (UploadSession, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return UploadSession{}, err
	}
	if err := validateInput(&in); err != nil {
		return UploadSession{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UploadSession{}, err
	}
	defer tx.Rollback()
	if err = activeActor(ctx, tx, actorID); err != nil {
		return UploadSession{}, err
	}
	if err = fileGroupAccess(ctx, tx, actorID, in.GroupID); err != nil {
		return UploadSession{}, err
	}
	at := s.now().UTC()
	fileID := in.FileID
	if fileID == "" {
		fileID = uuid.NewString()
		_, err = tx.ExecContext(ctx, `INSERT INTO files(id,group_id,name,created_by,created_at) VALUES(?,?,?,?,?)`, fileID, in.GroupID, in.Name, actorID, stamp(at))
		if err != nil {
			return UploadSession{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO file_memberships(file_id,group_id) VALUES(?,?)`, fileID, in.GroupID); err != nil {
			return UploadSession{}, err
		}
	} else {
		f, e := loadFile(ctx, tx, fileID)
		if e != nil {
			return UploadSession{}, e
		}
		if f.groupID != in.GroupID {
			return UploadSession{}, ErrForbidden
		}
		if e = manage(ctx, tx, actorID, f); e != nil {
			return UploadSession{}, e
		}
		if f.deleted {
			return UploadSession{}, ErrUnavailable
		}
	}
	u := UploadSession{UploadID: uuid.NewString(), FileID: fileID, VersionID: uuid.NewString(), MissingIndexes: []int{}}
	_, err = tx.ExecContext(ctx, `INSERT INTO file_versions(id,file_id,created_by,name,size,sha256,chunk_size,status,created_at) VALUES(?,?,?,?,?,?,?,'pending',?)`, u.VersionID, fileID, actorID, in.Name, in.Size, in.SHA256, in.ChunkSize, stamp(at))
	if err != nil {
		return UploadSession{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO upload_sessions(id,version_id,user_id,created_at,expires_at) VALUES(?,?,?,?,?)`, u.UploadID, u.VersionID, actorID, stamp(at), stamp(at.Add(7*24*time.Hour)))
	if err != nil {
		return UploadSession{}, err
	}
	for _, c := range in.Chunks {
		if _, err = tx.ExecContext(ctx, `INSERT INTO file_chunks(version_id,chunk_index,hash,size) VALUES(?,?,?,?)`, u.VersionID, c.Index, c.Hash, c.Size); err != nil {
			return UploadSession{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO chunk_objects(hash,created_at) VALUES(?,?)`, c.Hash, stamp(at)); err != nil {
			return UploadSession{}, err
		}
		u.MissingIndexes = append(u.MissingIndexes, c.Index)
	}
	if err = recordAudit(ctx, tx, actorID, "upload.started", fileID, u.VersionID, at); err != nil {
		return UploadSession{}, err
	}
	return u, tx.Commit()
}

// CreateVersion always starts a pending upload; publication requires CompleteUpload.
func (s *FileService) CreateVersion(ctx context.Context, actorID, fileID string, in StartUploadInput) (UploadSession, error) {
	in.FileID = fileID
	return s.StartUpload(ctx, actorID, in)
}
func (s *FileService) GetUpload(ctx context.Context, actorID, uploadID string) (UploadSession, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return UploadSession{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UploadSession{}, err
	}
	defer tx.Rollback()
	return s.upload(ctx, tx, actorID, uploadID)
}
func (s *FileService) upload(ctx context.Context, tx *sql.Tx, actorID, uploadID string) (UploadSession, error) {
	if err := activeActor(ctx, tx, actorID); err != nil {
		return UploadSession{}, err
	}
	u := UploadSession{UploadID: uploadID, MissingIndexes: []int{}}
	var owner, expiry, status string
	var complete sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT u.version_id,v.file_id,u.user_id,u.expires_at,u.completed_at,v.status FROM upload_sessions u JOIN file_versions v ON v.id=u.version_id WHERE u.id=?`, uploadID).Scan(&u.VersionID, &u.FileID, &owner, &expiry, &complete, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrUnavailable
	}
	if err != nil {
		return u, err
	}
	if owner != actorID {
		return u, ErrForbidden
	}
	f, err := loadFile(ctx, tx, u.FileID)
	if err != nil {
		return u, err
	}
	if err = manage(ctx, tx, actorID, f); err != nil {
		return u, err
	}
	if f.deleted || status == "deleted" || status == "expired" {
		return u, ErrUnavailable
	}
	if complete.Valid {
		if _, err = s.authorizedVersion(ctx, tx, actorID, u.VersionID); err != nil {
			return u, err
		}
		u.Completed = true
		return u, nil
	}
	if expiry <= stamp(s.now()) {
		return u, ErrUnavailable
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.chunk_index FROM file_chunks c LEFT JOIN upload_receipts r ON r.upload_id=? AND r.chunk_index=c.chunk_index WHERE c.version_id=? AND r.chunk_index IS NULL ORDER BY c.chunk_index`, u.UploadID, u.VersionID)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		if err = rows.Scan(&index); err != nil {
			return u, err
		}
		u.MissingIndexes = append(u.MissingIndexes, index)
	}
	return u, rows.Err()
}
