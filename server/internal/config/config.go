// Package config contains the server's runtime configuration and its defaults.
package config

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultBindAddress      = "127.0.0.1:8080"
	defaultStorageRoot      = "./data"
	defaultSQLitePath       = "./data/linknestx.sqlite3"
	defaultJWTSigningKey    = "change-me-access-key"
	defaultJWTRefreshKey    = "change-me-refresh-key"
	defaultFileRetention    = 30 * 24 * time.Hour
	defaultMessageRetention = 2 * 365 * 24 * time.Hour
	defaultAuditRetention   = 2 * 365 * 24 * time.Hour
)

// Config controls server listeners, persistent storage, token signing, and
// lifecycle cleanup. Values can be supplied through LINKNESTX_* environment
// variables; unset values use the safe development defaults below.
type Config struct {
	TrustedProxyCIDRs      []string
	ICEServers             []ICEServer
	TURNSharedSecret       string
	TURNCredentialTTL      time.Duration
	BindAddress            string
	StorageRoot            string
	SQLitePath             string
	JWTSigningKey          string
	JWTRefreshKey          string
	Version                string
	FileRetention          time.Duration
	MessageRetention       time.Duration
	AuditRetention         time.Duration
	CleanupInterval        time.Duration
	BootstrapAdminUsername string
	BootstrapAdminPassword string
}

// Default returns a development configuration. Production deployments should
// provide both JWT keys explicitly through the environment.
func Default() Config {
	return Config{
		ICEServers:        []ICEServer{},
		TURNCredentialTTL: 10 * time.Minute,
		BindAddress:       defaultBindAddress,
		StorageRoot:       defaultStorageRoot,
		SQLitePath:        defaultSQLitePath,
		JWTSigningKey:     defaultJWTSigningKey,
		JWTRefreshKey:     defaultJWTRefreshKey,
		Version:           "dev",
		FileRetention:     defaultFileRetention,
		MessageRetention:  defaultMessageRetention,
		AuditRetention:    defaultAuditRetention,
		CleanupInterval:   time.Hour,
	}
}

// Load reads runtime configuration from LINKNESTX_* variables. Duration
// values accept the syntax documented by time.ParseDuration (for example
// "720h" or "30d" is not accepted; use "720h").
func Load() (Config, error) {
	cfg := Default()
	if raw := strings.TrimSpace(os.Getenv("LINKNESTX_TRUSTED_PROXY_CIDRS")); raw != "" {
		if len(raw) > 4096 {
			return Config{}, fmt.Errorf("invalid trusted proxy CIDRs")
		}
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(value)
			if _, err := netip.ParsePrefix(value); err != nil {
				return Config{}, fmt.Errorf("invalid trusted proxy CIDR")
			}
			cfg.TrustedProxyCIDRs = append(cfg.TrustedProxyCIDRs, value)
		}
		if len(cfg.TrustedProxyCIDRs) > 32 {
			return Config{}, fmt.Errorf("too many trusted proxy CIDRs")
		}
	}
	cfg.TURNSharedSecret = os.Getenv("LINKNESTX_TURN_SHARED_SECRET")
	if value := strings.TrimSpace(os.Getenv("LINKNESTX_ICE_SERVERS")); value != "" {
		if len(value) > 32768 || json.Unmarshal([]byte(value), &cfg.ICEServers) != nil {
			return Config{}, fmt.Errorf("invalid LINKNESTX_ICE_SERVERS JSON")
		}
	}
	if value := strings.TrimSpace(os.Getenv("LINKNESTX_TURN_CREDENTIAL_TTL")); value != "" {
		ttl, err := time.ParseDuration(value)
		if err != nil || ttl < time.Minute || ttl > time.Hour {
			return Config{}, fmt.Errorf("LINKNESTX_TURN_CREDENTIAL_TTL must be between 1m and 1h")
		}
		cfg.TURNCredentialTTL = ttl
	}
	if err := validateICE(cfg); err != nil {
		return Config{}, err
	}
	setString(&cfg.BindAddress, "LINKNESTX_BIND_ADDRESS")
	setString(&cfg.StorageRoot, "LINKNESTX_STORAGE_ROOT")
	setString(&cfg.SQLitePath, "LINKNESTX_SQLITE_PATH")
	setString(&cfg.JWTSigningKey, "LINKNESTX_JWT_SIGNING_KEY")
	setString(&cfg.JWTRefreshKey, "LINKNESTX_JWT_REFRESH_KEY")
	setString(&cfg.Version, "LINKNESTX_VERSION")
	setString(&cfg.BootstrapAdminUsername, "LINKNESTX_BOOTSTRAP_ADMIN_USERNAME")
	// Preserve password whitespace; credentials never appear in logs.
	cfg.BootstrapAdminPassword = os.Getenv("LINKNESTX_BOOTSTRAP_ADMIN_PASSWORD")
	if (cfg.BootstrapAdminUsername == "") != (cfg.BootstrapAdminPassword == "") {
		return Config{}, fmt.Errorf("bootstrap administrator username and password must be provided together")
	}
	if cfg.BootstrapAdminPassword != "" && (utf8.RuneCountInString(cfg.BootstrapAdminPassword) < 8 || len(cfg.BootstrapAdminPassword) > 1024 || !utf8.ValidString(cfg.BootstrapAdminPassword)) {
		return Config{}, fmt.Errorf("bootstrap administrator password must be at least 8 characters")
	}

	for _, item := range []struct {
		name string
		to   *time.Duration
	}{
		{name: "LINKNESTX_CLEANUP_INTERVAL", to: &cfg.CleanupInterval},
		{name: "LINKNESTX_FILE_RETENTION", to: &cfg.FileRetention},
		{name: "LINKNESTX_MESSAGE_RETENTION", to: &cfg.MessageRetention},
		{name: "LINKNESTX_AUDIT_RETENTION", to: &cfg.AuditRetention},
	} {
		value := strings.TrimSpace(os.Getenv(item.name))
		if value == "" {
			continue
		}
		duration, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", item.name, err)
		}
		if duration <= 0 {
			return Config{}, fmt.Errorf("%s must be greater than zero", item.name)
		}
		*item.to = duration
	}

	if err := validateExposure(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ICEServer uses the WebRTC URL list format. Shared TURN secrets are kept in
// Config only; callers receive either static or derived temporary credentials.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

func validateICE(cfg Config) error {
	if len(cfg.ICEServers) > 16 {
		return fmt.Errorf("too many ICE servers")
	}
	for _, server := range cfg.ICEServers {
		if len(server.URLs) == 0 || len(server.URLs) > 8 || len(server.Username) > 1024 || len(server.Credential) > 4096 {
			return fmt.Errorf("invalid ICE server")
		}
		turn := false
		for _, raw := range server.URLs {
			u, err := url.Parse(raw)
			if err != nil || len(raw) > 2048 || strings.ContainsAny(raw, " \t\r\n") || u.Opaque == "" || u.Fragment != "" {
				return fmt.Errorf("invalid ICE server URL")
			}
			switch u.Scheme {
			case "stun", "stuns":
			case "turn", "turns":
				turn = true
			default:
				return fmt.Errorf("invalid ICE server URL scheme")
			}
		}
		if (server.Username == "") != (server.Credential == "") || (turn && cfg.TURNSharedSecret == "" && server.Username == "") {
			return fmt.Errorf("TURN requires paired static credentials or a shared secret")
		}
	}
	return nil
}

func setString(target *string, name string) {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		*target = value
	}
}

// Port returns the configured TCP port when BindAddress is in host:port form.
// It is a convenience for diagnostics and does not participate in startup.
func (c Config) Port() (int, error) {
	parts := strings.Split(c.BindAddress, ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("bind address %q has no port", c.BindAddress)
	}
	port, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid bind port in %q", c.BindAddress)
	}
	return port, nil
}
