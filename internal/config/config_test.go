package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != defaultListen || cfg.Process.DefaultShell != defaultShell {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadOverlaysDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[server]\nlisten = '127.0.0.1:9999'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:9999" {
		t.Fatalf("listen = %q", cfg.Server.Listen)
	}
	if cfg.Process.DefaultShell != defaultShell {
		t.Fatalf("defaults were not preserved: %+v", cfg.Process)
	}
	if cfg.Search.DefaultMaxResults != 1000 || cfg.Search.RetentionSeconds != 300 || cfg.Search.InitialWaitMS != 40 {
		t.Fatalf("search defaults were not preserved: %+v", cfg.Search)
	}
}

func TestInvalidFilesConfig(t *testing.T) {
	cfg := Default()
	cfg.Files.DefaultReadLines = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted zero files.default_read_lines")
	}
}

func TestInvalidSearchConfig(t *testing.T) {
	cfg := Default()
	cfg.Search.RetentionSeconds = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted zero search.retention_seconds")
	}
}

func TestAuthRequiresCanonicalHTTPSOrigin(t *testing.T) {
	cfg := Default()
	cfg.Auth.Enabled = true
	cfg.Auth.ExternalURL = "http://203.0.113.10"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted HTTP OAuth issuer")
	}
	cfg.Auth.ExternalURL = "https://203.0.113.10/"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted trailing slash in OAuth issuer")
	}
	cfg.Auth.ExternalURL = "https://203.0.113.10"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate rejected canonical HTTPS issuer: %v", err)
	}
}

func TestDefaultListenIsLocalHTTPOrigin(t *testing.T) {
	cfg := Default()
	if cfg.Server.Listen != "127.0.0.1:31354" {
		t.Fatalf("default listen = %q", cfg.Server.Listen)
	}
}

func TestDecodeRejectsRemovedTLSSection(t *testing.T) {
	_, err := Decode([]byte("[tls]\nmode = 'acme'\n"), Default())
	if err == nil || !strings.Contains(err.Error(), "[tls] was removed") {
		t.Fatalf("expected removed TLS migration error, got %v", err)
	}
}
