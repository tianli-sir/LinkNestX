package files

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/groups"
	"github.com/linknestx/linknestx/server/internal/notes"
)

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type fileState struct {
	id, groupID, documentID, createdBy, visibility, current string
	deleted                                                 bool
	listed                                                  bool
}

func loadFile(ctx context.Context, q queryer, id string) (fileState, error) {
	var f fileState
	var deleted, current sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id,COALESCE(group_id,''),COALESCE(document_id,''),created_by,visibility,current_version_id,deleted_at,listed FROM files WHERE id=?`, id).Scan(&f.id, &f.groupID, &f.documentID, &f.createdBy, &f.visibility, &current, &deleted, &f.listed)
	f.deleted = deleted.Valid
	f.current = current.String
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrUnavailable
	}
	return f, err
}
func activeSession(ctx context.Context, q queryer, actorID, sessionID, deviceID string) error {
	var count int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.id=? AND s.user_id=? AND s.device_id=? AND s.revoked_at IS NULL AND u.disabled=0 AND u.must_change_password=0 AND julianday(s.expires_at)>julianday('now')`, sessionID, actorID, deviceID).Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		return auth.ErrUnauthorized
	}
	return nil
}
func activeActor(ctx context.Context, q queryer, actorID string) error {
	return activeSession(ctx, q, actorID, auth.SessionIDFromContext(ctx), auth.DeviceIDFromContext(ctx))
}
func member(ctx context.Context, q queryer, actorID, groupID string) error {
	var count int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM group_members WHERE group_id=? AND user_id=?`, groupID, actorID).Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrForbidden
	}
	return nil
}
func admin(ctx context.Context, q queryer, actorID string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM user_roles u JOIN roles r ON r.id=u.role_id WHERE u.user_id=? AND r.name='admin'`, actorID).Scan(&n)
	return n > 0, err
}
func access(ctx context.Context, q queryer, actorID string, f fileState) error {
	if f.documentID != "" {
		return documentAccess(ctx, q, actorID, f.documentID, false)
	}
	if direct, err := groups.RequireDirectMember(ctx, q, actorID, f.groupID); direct || err != nil {
		return err
	}
	if f.visibility == "pool" && f.listed {
		return nil
	}
	a, err := admin(ctx, q, actorID)
	if err != nil {
		return err
	}
	if a {
		return nil
	}
	if f.visibility == "pool" {
		return ErrForbidden
	}
	return member(ctx, q, actorID, f.groupID)
}
func manage(ctx context.Context, q queryer, actorID string, f fileState) error {
	if f.documentID != "" {
		return documentAccess(ctx, q, actorID, f.documentID, true)
	}
	if direct, err := groups.RequireDirectMember(ctx, q, actorID, f.groupID); direct || err != nil {
		if err != nil {
			return err
		}
		if f.createdBy != actorID {
			return ErrForbidden
		}
		return nil
	}
	a, err := admin(ctx, q, actorID)
	if err != nil {
		return err
	}
	if a {
		return nil
	}
	if f.visibility == "pool" {
		return ErrForbidden
	}
	if err = member(ctx, q, actorID, f.groupID); err != nil {
		return err
	}
	if f.createdBy == actorID {
		return nil
	}
	var owners int
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM group_members WHERE group_id=? AND user_id=? AND role='owner'`, f.groupID, actorID).Scan(&owners)
	if err != nil {
		return err
	}
	if owners == 0 {
		return ErrForbidden
	}
	return nil
}
func (s *FileService) authorizedVersion(ctx context.Context, q queryer, actorID, versionID string) (FileVersion, error) {
	var m FileVersion
	var created string
	var expires sql.NullString
	err := q.QueryRowContext(ctx, `SELECT file_id,id,name,size,sha256,chunk_size,expires_at,created_at,created_by,status FROM file_versions WHERE id=?`, versionID).Scan(&m.FileID, &m.VersionID, &m.Name, &m.Size, &m.SHA256, &m.ChunkSize, &expires, &created, &m.CreatedBy, &m.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrUnavailable
	}
	if err != nil {
		return m, err
	}
	f, err := loadFile(ctx, q, m.FileID)
	if err != nil {
		return m, err
	}
	if err = access(ctx, q, actorID, f); err != nil {
		return m, err
	}
	if f.deleted || m.Status != "published" || (expires.Valid && expires.String <= stamp(s.now())) {
		return m, ErrUnavailable
	}
	m.SharingPolicy, err = loadSharingPolicy(ctx, q, f)
	if err != nil {
		return m, err
	}
	m.Visibility = f.visibility
	m.DocumentID = f.documentID
	m.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return m, err
	}
	if expires.Valid {
		at, e := time.Parse(time.RFC3339Nano, expires.String)
		if e != nil {
			return m, e
		}
		m.ExpiresAt = &at
	}
	return m, nil
}

type ChunkGrant struct {
	UserID    string    `json:"userId"`
	DeviceID  string    `json:"deviceId"`
	SessionID string    `json:"sessionId"`
	VersionID string    `json:"versionId"`
	ChunkHash string    `json:"chunkHash"`
	Size      int64     `json:"size"`
	ExpiresAt time.Time `json:"expiresAt"`
	Signature string    `json:"signature"`
}

func (s *FileService) grantSignature(g ChunkGrant) string {
	g.Signature = ""
	b, _ := json.Marshal(g)
	mac := hmac.New(sha256.New, s.signingKey)
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}
func (s *FileService) AuthorizeChunk(ctx context.Context, actorID, versionID, chunkHash string) (ChunkGrant, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return ChunkGrant{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChunkGrant{}, err
	}
	defer tx.Rollback()
	if err = activeActor(ctx, tx, actorID); err != nil {
		return ChunkGrant{}, err
	}
	m, err := s.authorizedVersion(ctx, tx, actorID, versionID)
	if err != nil {
		return ChunkGrant{}, err
	}
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT size FROM file_chunks WHERE version_id=? AND hash=? LIMIT 1`, versionID, chunkHash).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrUnavailable
	}
	if err != nil {
		return ChunkGrant{}, err
	}
	expiry := s.now().UTC().Add(2 * time.Minute)
	accessExpiry, err := s.auth.AccessExpiryFromContext(ctx)
	if err != nil {
		return ChunkGrant{}, err
	}
	if expiry.After(accessExpiry) {
		expiry = accessExpiry
	}
	if m.ExpiresAt != nil && expiry.After(*m.ExpiresAt) {
		expiry = *m.ExpiresAt
	}
	g := ChunkGrant{UserID: actorID, DeviceID: auth.DeviceIDFromContext(ctx), SessionID: auth.SessionIDFromContext(ctx), VersionID: versionID, ChunkHash: chunkHash, Size: size, ExpiresAt: expiry}
	g.Signature = s.grantSignature(g)
	return g, nil
}

// ValidateChunkGrant is called by an authenticated serving peer. Both the
// recipient's signed session and the serving caller must retain current access.
func (s *FileService) ValidateChunkGrant(ctx context.Context, g ChunkGrant) error {
	caller, ok := auth.UserFromContext(ctx)
	if !ok {
		return auth.ErrUnauthorized
	}
	if err := s.auth.ValidateActor(ctx, caller.ID); err != nil {
		return err
	}
	if !hmac.Equal([]byte(g.Signature), []byte(s.grantSignature(g))) || !g.ExpiresAt.After(s.now()) || g.ExpiresAt.After(s.now().Add(2*time.Minute)) {
		return ErrInvalidGrant
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = activeActor(ctx, tx, caller.ID); err != nil {
		return err
	}
	if err = activeSession(ctx, tx, g.UserID, g.SessionID, g.DeviceID); err != nil {
		return err
	}
	if _, err = s.authorizedVersion(ctx, tx, g.UserID, g.VersionID); err != nil {
		return err
	}
	m, err := s.authorizedVersion(ctx, tx, caller.ID, g.VersionID)
	if err == nil && !m.SharingPolicy.Enabled {
		return ErrSharingDisabled
	}
	if err != nil {
		return err
	}
	var size int64
	if err = tx.QueryRowContext(ctx, `SELECT size FROM file_chunks WHERE version_id=? AND hash=? LIMIT 1`, g.VersionID, g.ChunkHash).Scan(&size); err != nil || size != g.Size {
		return ErrInvalidGrant
	}
	return nil
}

// Administrators manage/read group files independently of group chat membership.
func fileGroupAccess(ctx context.Context, q queryer, actorID, groupID string) error {
	if direct, err := groups.RequireDirectMember(ctx, q, actorID, groupID); direct || err != nil {
		return err
	}
	a, err := admin(ctx, q, actorID)
	if err != nil {
		return err
	}
	if a {
		return nil
	}
	return member(ctx, q, actorID, groupID)
}

func documentAccess(ctx context.Context, q queryer, actor, id string, write bool) error {
	err := notes.Authorize(ctx, q, actor, id, write)
	if errors.Is(err, notes.ErrNotFound) || errors.Is(err, notes.ErrForbidden) {
		return ErrForbidden
	}
	return err
}
