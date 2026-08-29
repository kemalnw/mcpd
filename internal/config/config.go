package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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

func Default() Config {
	return Config{
		Server:  ServerConfig{Listen: defaultListen, MCPPath: defaultMCPPath, ShutdownSeconds: 10},
		Process: ProcessConfig{DefaultShell: defaultShell, DefaultWaitMS: defaultWaitTimeoutMS, OutputBufferBytes: defaultOutputBufferBytes, MaxLineBytes: defaultMaxLineBytes, CompletedSessions: defaultCompletedSessions},
		Files:   FilesConfig{DefaultReadLines: defaultFileReadLines, MaxLineBytes: defaultMaxLineBytes, NestedEntryLimit: defaultNestedEntries, HTTPTimeoutSeconds: defaultHTTPTimeoutSecs, MaxRemoteBytes: defaultMaxRemoteBytes},
		Search:  SearchConfig{DefaultMaxResults: 1000, RetentionSeconds: 300, InitialWaitMS: 40},
		Audit:   AuditConfig{Enabled: true, Path: defaultAuditPath()},
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

func defaultAuditPath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "audit.jsonl"
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "mcpd", "audit.jsonl")
}
