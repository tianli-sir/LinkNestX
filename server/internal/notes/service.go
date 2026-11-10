// Package notes implements durable employee documents and shared-library curation.
package notes

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/auth"
)

var (
	ErrInvalid   = errors.New("invalid document input")
	ErrForbidden = errors.New("document access denied")
	ErrNotFound  = errors.New("document unavailable")
	ErrConflict  = errors.New("document changed; reload before saving")
)

type Queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type State struct {
	Author, Visibility, Status string
	Curated                    bool
	Revision                   int64
	Deleted                    bool
}

func IsAdmin(ctx context.Context, q Queryer, actor string) (bool, error) {
	var n int
	e := q.QueryRowContext(ctx, `SELECT count(*) FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=? AND r.name='admin'`, actor).Scan(&n)
	return n > 0, e
}
func Load(ctx context.Context, q Queryer, id string) (State, error) {
	var s State
	var deleted sql.NullString
	e := q.QueryRowContext(ctx, `SELECT author_id,visibility,status,curated,revision,deleted_at FROM documents WHERE id=?`, id).Scan(&s.Author, &s.Visibility, &s.Status, &s.Curated, &s.Revision, &deleted)
	s.Deleted = deleted.Valid
	if e == sql.ErrNoRows || s.Deleted {
		return s, ErrNotFound
	}
	return s, e
}
func Authorize(ctx context.Context, q Queryer, actor, id string, write bool) error {
	s, e := Load(ctx, q, id)
	if e != nil {
		return e
	}
	if write {
		if s.Curated {
			if auth.ClientTypeFromContext(ctx) == "web" {
				return ErrForbidden
			}
			a, e := IsAdmin(ctx, q, actor)
			if e != nil {
				return e
			}
			if a {
				return nil
			}
			return ErrForbidden
		}
		if s.Author == actor {
			return nil
		}
		return ErrForbidden
	}
	if s.Author == actor {
		return nil
	}
	if s.Status != "published" || s.Visibility == "private" {
		return ErrNotFound
	}
	if s.Visibility == "team" {
		return nil
	}
	lo, hi := actor, s.Author
	if lo > hi {
		lo, hi = hi, lo
	}
	var n int
	e = q.QueryRowContext(ctx, `SELECT count(*) FROM friendships WHERE pair_low=? AND pair_high=?`, lo, hi).Scan(&n)
	if e != nil {
		return e
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type Author struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	DisplayName   string `json:"displayName"`
	AccountNumber string `json:"accountNumber"`
	AvatarURL     string `json:"avatarUrl,omitempty"`
}
type Attachment struct {
	ID               string  `json:"id"`
	GroupID          string  `json:"groupId"`
	DocumentID       string  `json:"documentId"`
	Name             string  `json:"name"`
	CreatedBy        string  `json:"createdBy"`
	Visibility       string  `json:"visibility"`
	CurrentVersionID string  `json:"currentVersionId"`
	VersionID        string  `json:"versionId"`
	CreatedAt        string  `json:"createdAt"`
	ExpiresAt        *string `json:"expiresAt"`
	Status           string  `json:"status"`
	Size             int64   `json:"size"`
	SHA256           string  `json:"sha256"`
	Listed           bool    `json:"listed"`
}
type Document struct {
	BodyTruncated bool         `json:"bodyTruncated"`
	ID            string       `json:"id"`
	Title         string       `json:"title"`
	Body          string       `json:"body"`
	Visibility    string       `json:"visibility"`
	Status        string       `json:"status"`
	Curated       bool         `json:"curated"`
	Revision      int64        `json:"revision"`
	Author        Author       `json:"author"`
	CreatedAt     string       `json:"createdAt"`
	UpdatedAt     string       `json:"updatedAt"`
	PublishedAt   *string      `json:"publishedAt,omitempty"`
	CanEdit       bool         `json:"canEdit"`
	CanDelete     bool         `json:"canDelete"`
	CanCurate     bool         `json:"canCurate"`
	Attachments   []Attachment `json:"attachments"`
}
type Input struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	Visibility string `json:"visibility"`
	Status     string `json:"status"`
	Revision   int64  `json:"revision"`
}
type Page struct {
	Documents  []Document `json:"documents"`
	NextCursor string     `json:"nextCursor"`
	HasMore    bool       `json:"hasMore"`
}
type ListInput struct {
	Scope, Q, Cursor string
	Limit            int
}
type Service struct {
	db      *sql.DB
	auth    *auth.AuthService
	changed func()
}

func New(db *sql.DB, a *auth.AuthService) *Service { return &Service{db: db, auth: a} }
func (s *Service) SetChangeObserver(f func())      { s.changed = f }
func (s *Service) notify() {
	if s.changed != nil {
		s.changed()
	}
}
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func validate(in *Input) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || utf8.RuneCountInString(in.Title) > 200 || !utf8.ValidString(in.Title) || !utf8.ValidString(in.Body) || len(in.Body) > 1024*1024 || strings.ContainsRune(in.Title, 0) || strings.ContainsRune(in.Body, 0) {
		return ErrInvalid
	}
	if in.Visibility != "private" && in.Visibility != "friends" && in.Visibility != "team" {
		return ErrInvalid
	}
	if in.Status != "draft" && in.Status != "published" {
		return ErrInvalid
	}
	return nil
}
func (s *Service) Get(ctx context.Context, actor, id string) (Document, error) {
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return Document{}, e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Document{}, e
	}
	defer tx.Rollback()
	return get(ctx, tx, actor, id, true)
}
func get(ctx context.Context, q Queryer, actor, id string, full bool) (Document, error) {
	var d Document
	if e := active(ctx, q, actor); e != nil {
		return d, e
	}
	if e := Authorize(ctx, q, actor, id, false); e != nil {
		return d, e
	}
	var pub sql.NullString
	var avatar bool
	e := q.QueryRowContext(ctx, `SELECT d.id,d.title,CASE WHEN ? THEN d.body ELSE substr(d.body,1,2000) END,(NOT ? AND length(d.body)>2000),d.visibility,d.status,d.curated,d.revision,d.author_id,u.username,p.display_name,p.account_number,p.avatar IS NOT NULL,d.created_at,d.updated_at,d.published_at FROM documents d JOIN users u ON u.id=d.author_id JOIN profiles p ON p.user_id=u.id WHERE d.id=?`, full, full, id).Scan(&d.ID, &d.Title, &d.Body, &d.BodyTruncated, &d.Visibility, &d.Status, &d.Curated, &d.Revision, &d.Author.ID, &d.Author.Username, &d.Author.DisplayName, &d.Author.AccountNumber, &avatar, &d.CreatedAt, &d.UpdatedAt, &pub)
	if e != nil {
		return d, e
	}
	if avatar {
		d.Author.AvatarURL = "/api/v1/profiles/" + d.Author.ID + "/avatar"
	}
	if pub.Valid {
		d.PublishedAt = &pub.String
	}
	a, e := IsAdmin(ctx, q, actor)
	if e != nil {
		return d, e
	}
	d.CanEdit = (!d.Curated && d.Author.ID == actor) || (d.Curated && a && auth.ClientTypeFromContext(ctx) != "web")
	d.CanDelete = d.CanEdit
	d.CanCurate = a && auth.ClientTypeFromContext(ctx) != "web" && d.Status == "published" && d.Visibility == "team"
	d.Attachments = []Attachment{}
	if !full {
		return d, nil
	}
	rows, e := q.QueryContext(ctx, `SELECT f.id,v.name,f.created_by,f.current_version_id,v.id,v.created_at,v.size,v.sha256 FROM files f JOIN file_versions v ON v.file_id=f.id WHERE f.document_id=? AND f.deleted_at IS NULL AND v.status='published' ORDER BY v.created_at DESC,v.id DESC`, id)
	if e != nil {
		return d, e
	}
	defer rows.Close()
	for rows.Next() {
		v := Attachment{DocumentID: id, Visibility: "document", Status: "published", Listed: true}
		if e = rows.Scan(&v.ID, &v.Name, &v.CreatedBy, &v.CurrentVersionID, &v.VersionID, &v.CreatedAt, &v.Size, &v.SHA256); e != nil {
			return d, e
		}
		d.Attachments = append(d.Attachments, v)
	}
	return d, rows.Err()
}
func (s *Service) Create(ctx context.Context, actor string, in Input) (Document, error) {
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return Document{}, e
	}
	if e := validate(&in); e != nil {
		return Document{}, e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Document{}, e
	}
	defer tx.Rollback()
	if e = active(ctx, tx, actor); e != nil {
		return Document{}, e
	}
	id := uuid.NewString()
	now := stamp(time.Now())
	var published any
	if in.Status == "published" {
		published = now
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO documents(id,author_id,title,body,visibility,status,created_at,updated_at,published_at)VALUES(?,?,?,?,?,?,?,?,?)`, id, actor, in.Title, in.Body, in.Visibility, in.Status, now, now, published)
	if e != nil {
		return Document{}, e
	}
	if e = auth.RecordAudit(ctx, tx, actor, "document.created", id, time.Now()); e != nil {
		return Document{}, e
	}
	d, e := get(ctx, tx, actor, id, true)
	if e != nil {
		return d, e
	}
	if e = tx.Commit(); e != nil {
		return d, e
	}
	s.notify()
	return d, nil
}
func (s *Service) Update(ctx context.Context, actor, id string, in Input) (Document, error) {
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return Document{}, e
	}
	if e := validate(&in); e != nil || in.Revision < 1 {
		return Document{}, ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Document{}, e
	}
	defer tx.Rollback()
	if e = active(ctx, tx, actor); e != nil {
		return Document{}, e
	}
	if e = Authorize(ctx, tx, actor, id, true); e != nil {
		return Document{}, e
	}
	state, e := Load(ctx, tx, id)
	if e != nil {
		return Document{}, e
	}
	if state.Revision != in.Revision {
		return Document{}, ErrConflict
	}
	if state.Curated && (in.Visibility != "team" || in.Status != "published") {
		return Document{}, ErrForbidden
	}
	now := stamp(time.Now())
	_, e = tx.ExecContext(ctx, `UPDATE documents SET title=?,body=?,visibility=?,status=?,revision=revision+1,updated_at=?,published_at=CASE WHEN ?='published' THEN COALESCE(published_at,?) ELSE published_at END WHERE id=?`, in.Title, in.Body, in.Visibility, in.Status, now, in.Status, now, id)
	if e != nil {
		return Document{}, e
	}
	if e = auth.RecordAudit(ctx, tx, actor, "document.updated", id, time.Now()); e != nil {
		return Document{}, e
	}
	d, e := get(ctx, tx, actor, id, true)
	if e != nil {
		return d, e
	}
	if e = tx.Commit(); e != nil {
		return d, e
	}
	s.notify()
	return d, nil
}
func (s *Service) Curate(ctx context.Context, actor, id string, revision int64, curated bool) (Document, error) {
	if auth.ClientTypeFromContext(ctx) == "web" {
		return Document{}, ErrForbidden
	}
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return Document{}, e
	}
	if revision < 1 {
		return Document{}, ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Document{}, e
	}
	defer tx.Rollback()
	if e = active(ctx, tx, actor); e != nil {
		return Document{}, e
	}
	a, e := IsAdmin(ctx, tx, actor)
	if e != nil {
		return Document{}, e
	}
	if !a {
		return Document{}, ErrForbidden
	}
	if e = Authorize(ctx, tx, actor, id, false); e != nil {
		return Document{}, e
	}
	state, e := Load(ctx, tx, id)
	if e != nil {
		return Document{}, e
	}
	if state.Revision != revision {
		return Document{}, ErrConflict
	}
	if state.Status != "published" || state.Visibility != "team" {
		return Document{}, ErrForbidden
	}
	_, e = tx.ExecContext(ctx, `UPDATE documents SET curated=?,revision=revision+1,updated_at=? WHERE id=?`, curated, stamp(time.Now()), id)
	if e != nil {
		return Document{}, e
	}
	if e = auth.RecordAudit(ctx, tx, actor, "document.curation_changed", id, time.Now()); e != nil {
		return Document{}, e
	}
	d, e := get(ctx, tx, actor, id, true)
	if e != nil {
		return d, e
	}
	if e = tx.Commit(); e != nil {
		return d, e
	}
	s.notify()
	return d, nil
}
func (s *Service) Delete(ctx context.Context, actor, id string, revision int64) error {
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return e
	}
	if revision < 1 {
		return ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = active(ctx, tx, actor); e != nil {
		return e
	}
	if e = Authorize(ctx, tx, actor, id, true); e != nil {
		return e
	}
	state, e := Load(ctx, tx, id)
	if e != nil {
		return e
	}
	if state.Revision != revision {
		return ErrConflict
	}
	now := stamp(time.Now())
	for _, q := range []string{`UPDATE documents SET deleted_at=?,revision=revision+1 WHERE id=?`, `UPDATE files SET deleted_at=? WHERE document_id=? AND deleted_at IS NULL`, `UPDATE file_versions SET status='deleted',deleted_at=? WHERE file_id IN(SELECT id FROM files WHERE document_id=?) AND status IN('pending','published')`} {
		if _, e = tx.ExecContext(ctx, q, now, id); e != nil {
			return e
		}
	}
	if e = auth.RecordAudit(ctx, tx, actor, "document.deleted", id, time.Now()); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.notify()
	return nil
}
func (s *Service) List(ctx context.Context, actor string, in ListInput) (Page, error) {
	out := Page{Documents: []Document{}}
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return out, e
	}
	if in.Scope == "" {
		in.Scope = "feed"
	}
	if in.Scope != "feed" && in.Scope != "mine" && in.Scope != "drafts" && in.Scope != "public" {
		return out, ErrInvalid
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if in.Limit < 1 || in.Limit > 50 || len(in.Q) > 800 || !utf8.ValidString(in.Q) || utf8.RuneCountInString(in.Q) > 200 {
		return out, ErrInvalid
	}
	type cursor struct{ At, ID, Scope, Q string }
	c := cursor{}
	if in.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(in.Cursor)
		if e != nil || len(b) > 2048 || json.Unmarshal(b, &c) != nil || c.Scope != in.Scope || c.Q != in.Q || c.ID == "" || c.At == "" {
			return out, ErrInvalid
		}
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if e = active(ctx, tx, actor); e != nil {
		return out, e
	}
	where := `d.deleted_at IS NULL AND (d.author_id=? OR (d.status='published' AND (d.visibility='team' OR (d.visibility='friends' AND EXISTS(SELECT 1 FROM friendships f WHERE f.pair_low=min(d.author_id,?) AND f.pair_high=max(d.author_id,?))))))`
	args := []any{actor, actor, actor}
	switch in.Scope {
	case "feed":
		where += ` AND d.status='published'`
	case "public":
		where += ` AND d.curated=1`
	case "mine":
		where += ` AND d.author_id=?`
		args = append(args, actor)
	case "drafts":
		where += ` AND d.author_id=? AND d.status='draft'`
		args = append(args, actor)
	}
	if in.Q != "" {
		where += ` AND (instr(lower(d.title),lower(?))>0 OR instr(lower(d.body),lower(?))>0)`
		args = append(args, in.Q, in.Q)
	}
	if c.ID != "" {
		where += ` AND (d.updated_at<? OR(d.updated_at=? AND d.id<?))`
		args = append(args, c.At, c.At, c.ID)
	}
	args = append(args, in.Limit+1)
	rows, e := tx.QueryContext(ctx, `SELECT d.id FROM documents d WHERE `+where+` ORDER BY d.updated_at DESC,d.id DESC LIMIT ?`, args...)
	if e != nil {
		return out, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(ids) > in.Limit {
		out.HasMore = true
		ids = ids[:in.Limit]
	}
	for _, id := range ids {
		d, e := get(ctx, tx, actor, id, false)
		if e != nil {
			return out, e
		}
		out.Documents = append(out.Documents, d)
	}
	if out.HasMore {
		last := out.Documents[len(out.Documents)-1]
		b, _ := json.Marshal(cursor{last.UpdatedAt, last.ID, in.Scope, in.Q})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, nil
}

func active(ctx context.Context, q Queryer, actor string) error {
	var n int
	e := q.QueryRowContext(ctx, `SELECT count(*) FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.id=? AND s.id=? AND s.device_id=? AND u.disabled=0 AND u.must_change_password=0 AND s.revoked_at IS NULL AND julianday(s.expires_at)>julianday('now')`, actor, auth.SessionIDFromContext(ctx), auth.DeviceIDFromContext(ctx)).Scan(&n)
	if e != nil {
		return e
	}
	if n != 1 {
		return auth.ErrUnauthorized
	}
	return nil
}
