// Package p2p owns authenticated source discovery and WebRTC signaling. Bytes
// flow through WebRTC/TURN or the existing HTTP chunk endpoint, never this hub.
package p2p

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/config"
	"github.com/linknestx/linknestx/server/internal/files"
)

const MaxBodyBytes = 65536
const SourceLease = 60 * time.Second
const timestampLayout = "2006-01-02T15:04:05.000000000Z"

var ErrInvalidInput = errors.New("invalid peer input")
var ErrLimit = errors.New("peer capacity exceeded")
var ErrConnectionExists = errors.New("peer connection ID already recorded")

type AnnounceInput struct {
	VersionID string `json:"versionId"`
	Indexes   []int  `json:"indexes"`
}
type PeerSource struct {
	DeviceID string    `json:"deviceId"`
	UserID   string    `json:"userId"`
	Indexes  []int     `json:"indexes"`
	LastSeen time.Time `json:"lastSeen"`
}
type SourceResult struct {
	Sources    []PeerSource       `json:"sources"`
	ICEServers []config.ICEServer `json:"iceServers"`
}
type ICEConfig struct {
	ICEServers []config.ICEServer `json:"iceServers"`
}

// PeerService is shared by the HTTP boundary and hub, using the existing trusted
// auth context. No caller-supplied device/user fields establish identity.
type PeerService interface {
	Allocate(context.Context, string, AllocationInput) (Allocation, error)
	Announce(context.Context, string, AnnounceInput) error
	Heartbeat(context.Context, string, string) error
	Withdraw(context.Context, string, string) error
	ListSources(context.Context, string, string) (SourceResult, error)
	CreateChunkGrant(context.Context, string, string, string) (files.ChunkGrant, error)
	ValidateChunkGrant(context.Context, files.ChunkGrant) error
	Config(context.Context, string) (ICEConfig, error)
	ValidateActor(context.Context, string) error
	AuthorizeVersion(context.Context, string, string) error
	IsSource(context.Context, string, string, string) (bool, error)
	RecordConnection(context.Context, ConnectionAudit) error
	Cleanup(context.Context) error
}
type Service struct {
	allocations allocationBook
	db          *sql.DB
	auth        *auth.AuthService
	files       *files.FileService
	cfg         config.Config
	now         func() time.Time
}

func NewService(db *sql.DB, a *auth.AuthService, f *files.FileService, cfg config.Config) *Service {
	return &Service{db: db, auth: a, files: f, cfg: cfg, now: time.Now}
}
func stamp(t time.Time) string { return t.UTC().Format(timestampLayout) }
func validID(id string) bool {
	u, e := uuid.Parse(id)
	return e == nil && u != uuid.Nil && u.String() == id
}
func (s *Service) ValidateActor(ctx context.Context, actor string) error {
	return s.auth.ValidateActor(ctx, actor)
}
func (s *Service) AuthorizeVersion(ctx context.Context, actor, version string) error {
	if !validID(version) {
		return ErrInvalidInput
	}
	return s.files.RequirePeerSharing(ctx, actor, version)
}
func (s *Service) CreateChunkGrant(ctx context.Context, actor, version, hash string) (files.ChunkGrant, error) {
	if err := s.files.RequirePeerSharing(ctx, actor, version); err != nil {
		return files.ChunkGrant{}, err
	}
	return s.files.AuthorizeChunk(ctx, actor, version, hash)
}
func (s *Service) ValidateChunkGrant(ctx context.Context, g files.ChunkGrant) error {
	return s.files.ValidateChunkGrant(ctx, g)
}
func (s *Service) Config(ctx context.Context, actor string) (ICEConfig, error) {
	if err := s.ValidateActor(ctx, actor); err != nil {
		return ICEConfig{}, err
	}
	return ICEConfig{s.iceServers(auth.DeviceIDFromContext(ctx))}, nil
}
func (s *Service) iceServers(device string) []config.ICEServer {
	result := make([]config.ICEServer, 0, len(s.cfg.ICEServers))
	for _, entry := range s.cfg.ICEServers {
		entry.URLs = append([]string{}, entry.URLs...)
		for _, url := range entry.URLs {
			if (strings.HasPrefix(url, "turn:") || strings.HasPrefix(url, "turns:")) && s.cfg.TURNSharedSecret != "" {
				entry.Username = fmt.Sprintf("%d:%s", s.now().Add(s.cfg.TURNCredentialTTL).Unix(), device)
				mac := hmac.New(sha1.New, []byte(s.cfg.TURNSharedSecret))
				mac.Write([]byte(entry.Username))
				entry.Credential = base64.StdEncoding.EncodeToString(mac.Sum(nil))
				break
			}
		}
		result = append(result, entry)
	}
	return result
}

// Run performs lease expiry independent of the slower file retention schedule.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Cleanup(ctx)
		}
	}
}
