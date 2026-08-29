package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	defaultListen            = "0.0.0.0:8787"
	defaultMCPPath           = "/mcp"
	defaultShell             = "/bin/bash"
	defaultWaitTimeoutMS     = 30_000
	defaultOutputBufferBytes = 50 << 20
	defaultMaxLineBytes      = 1 << 20
	defaultCompletedSessions = 100
	defaultFileReadLines     = 1000
	defaultNestedEntries     = 100
	defaultHTTPTimeoutSecs   = 15
	defaultMaxRemoteBytes    = 16 << 20
)

type Config struct {
	Server  ServerConfig  `toml:"server"`
	Process ProcessConfig `toml:"process"`
	Files   FilesConfig   `toml:"files"`
	Search  SearchConfig  `toml:"search"`
	Audit   AuditConfig   `toml:"audit"`
	Auth    AuthConfig    `toml:"auth"`
	TLS     TLSConfig     `toml:"tls"`
}

type ServerConfig struct {
	Listen          string `toml:"listen"`
	MCPPath         string `toml:"mcp_path"`
	ShutdownSeconds int    `toml:"shutdown_seconds"`
}

type ProcessConfig struct {
	DefaultShell      string `toml:"default_shell"`
	DefaultWaitMS     int    `toml:"default_wait_ms"`
	OutputBufferBytes int    `toml:"output_buffer_bytes"`
	MaxLineBytes      int    `toml:"max_line_bytes"`
	CompletedSessions int    `toml:"completed_sessions"`
}

type FilesConfig struct {
	DefaultReadLines   int   `toml:"default_read_lines"`
	MaxLineBytes       int   `toml:"max_line_bytes"`
	NestedEntryLimit   int   `toml:"nested_entry_limit"`
	HTTPTimeoutSeconds int   `toml:"http_timeout_seconds"`
	MaxRemoteBytes     int64 `toml:"max_remote_bytes"`
}

type SearchConfig struct {
	DefaultMaxResults int `toml:"default_max_results"`
	RetentionSeconds  int `toml:"retention_seconds"`
	InitialWaitMS     int `toml:"initial_wait_ms"`
}

type AuditConfig struct {
	Enabled bool   `toml:"enabled"`
	Path    string `toml:"path"`
}

type AuthConfig struct {
	Enabled                      bool   `toml:"enabled"`
	ExternalURL                  string `toml:"external_url"`
	StateDir                     string `toml:"state_dir"`
	AccessTokenSeconds           int    `toml:"access_token_seconds"`
	AuthorizationCodeSeconds     int    `toml:"authorization_code_seconds"`
	LoginSessionSeconds          int    `toml:"login_session_seconds"`
	ClientMetadataTimeoutSeconds int    `toml:"client_metadata_timeout_seconds"`
}

type TLSConfig struct {
	Mode              string `toml:"mode"`
	CertFile          string `toml:"cert_file"`
	KeyFile           string `toml:"key_file"`
	ACMEEmail         string `toml:"acme_email"`
	ACMEServer        string `toml:"acme_server"`
	ACMEProfile       string `toml:"acme_profile"`
	ACMEAcceptTOS     bool   `toml:"acme_accept_tos"`
	ChallengeListen   string `toml:"challenge_listen"`
	CertDir           string `toml:"cert_dir"`
	RenewCheckSeconds int    `toml:"renew_check_seconds"`
}

func Default() Config {
	stateDir := DefaultStateDir()
	return Config{
		Server:  ServerConfig{Listen: defaultListen, MCPPath: defaultMCPPath, ShutdownSeconds: 10},
		Process: ProcessConfig{DefaultShell: defaultShell, DefaultWaitMS: defaultWaitTimeoutMS, OutputBufferBytes: defaultOutputBufferBytes, MaxLineBytes: defaultMaxLineBytes, CompletedSessions: defaultCompletedSessions},
		Files:   FilesConfig{DefaultReadLines: defaultFileReadLines, MaxLineBytes: defaultMaxLineBytes, NestedEntryLimit: defaultNestedEntries, HTTPTimeoutSeconds: defaultHTTPTimeoutSecs, MaxRemoteBytes: defaultMaxRemoteBytes},
		Search:  SearchConfig{DefaultMaxResults: 1000, RetentionSeconds: 300, InitialWaitMS: 40},
		Audit:   AuditConfig{Enabled: true, Path: defaultAuditPath()},
		Auth: AuthConfig{StateDir: filepath.Join(stateDir, "auth"), AccessTokenSeconds: 3600, AuthorizationCodeSeconds: 300,
			LoginSessionSeconds: 600, ClientMetadataTimeoutSeconds: 10},
		TLS: TLSConfig{Mode: "off", ACMEServer: "https://acme-v02.api.letsencrypt.org/directory", ACMEProfile: "shortlived",
			ChallengeListen: ":80", CertDir: filepath.Join(stateDir, "tls"), RenewCheckSeconds: 3600},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Server.Listen == "" {
		return errors.New("server.listen must not be empty")
	}
	if c.Server.MCPPath == "" || c.Server.MCPPath[0] != '/' {
		return errors.New("server.mcp_path must start with '/'")
	}
	if c.Server.ShutdownSeconds <= 0 {
		return errors.New("server.shutdown_seconds must be positive")
	}
	if c.Process.DefaultShell == "" {
		return errors.New("process.default_shell must not be empty")
	}
	if c.Process.DefaultWaitMS < 0 || c.Process.OutputBufferBytes <= 0 || c.Process.MaxLineBytes <= 0 || c.Process.CompletedSessions < 0 {
		return errors.New("process limits contain invalid values")
	}
	if c.Files.DefaultReadLines <= 0 || c.Files.MaxLineBytes <= 0 || c.Files.NestedEntryLimit <= 0 || c.Files.HTTPTimeoutSeconds <= 0 || c.Files.MaxRemoteBytes <= 0 {
		return errors.New("files limits contain invalid values")
	}
	if c.Search.DefaultMaxResults <= 0 || c.Search.RetentionSeconds <= 0 || c.Search.InitialWaitMS <= 0 {
		return errors.New("search limits contain invalid values")
	}
	if c.Auth.Enabled {
		if err := validateExternalURL(c.Auth.ExternalURL); err != nil {
			return err
		}
		if c.Auth.StateDir == "" || c.Auth.AccessTokenSeconds <= 0 || c.Auth.AuthorizationCodeSeconds <= 0 || c.Auth.LoginSessionSeconds <= 0 || c.Auth.ClientMetadataTimeoutSeconds <= 0 {
			return errors.New("auth state path and lifetimes must be positive/non-empty")
		}
	}
	switch c.TLS.Mode {
	case "off":
	case "files":
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			return errors.New("tls.cert_file and tls.key_file are required when tls.mode=files")
		}
	case "acme":
		if err := validateExternalURL(c.Auth.ExternalURL); err != nil {
			return fmt.Errorf("tls.mode=acme requires auth.external_url: %w", err)
		}
		if c.TLS.ACMEServer == "" || c.TLS.ChallengeListen == "" || c.TLS.CertDir == "" || c.TLS.RenewCheckSeconds <= 0 {
			return errors.New("tls ACME configuration is incomplete")
		}
		if !c.TLS.ACMEAcceptTOS {
			return errors.New("tls.acme_accept_tos must be true when tls.mode=acme")
		}
	default:
		return fmt.Errorf("tls.mode must be one of off, files, acme; got %q", c.TLS.Mode)
	}
	return nil
}

func validateExternalURL(raw string) error {
	if raw == "" {
		return errors.New("auth.external_url is required when authentication is enabled")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("auth.external_url must be an absolute HTTPS origin without credentials, query, or fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("auth.external_url must be an origin without a path")
	}
	if strings.HasSuffix(raw, "/") {
		return errors.New("auth.external_url must not have a trailing slash")
	}
	return nil
}

func DefaultPath() string {
	if path := os.Getenv("MCPD_CONFIG"); path != "" {
		return path
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(base, "mcpd", "config.toml")
}

func DefaultStateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ".mcpd-state"
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "mcpd")
}

func defaultAuditPath() string { return filepath.Join(DefaultStateDir(), "audit.jsonl") }
