package service

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestSystemDefaultConfigIsLocalAndUsesSystemState(t *testing.T) {
	cfg := SystemDefaultConfig()
	if cfg.Server.Listen != "127.0.0.1:31354" {
		t.Fatalf("listen=%q", cfg.Server.Listen)
	}
	if cfg.Audit.Path != "/var/lib/mcpd/audit.jsonl" || cfg.Auth.StateDir != "/var/lib/mcpd/auth" {
		t.Fatalf("unexpected system state paths: %#v", cfg)
	}
}

func TestRenderServiceIsSingleUnprivilegedUnit(t *testing.T) {
	unit := RenderService(Account{User: "alice", Group: "alice", Home: "/home/alice", UID: 1000, GID: 1000})
	for _, want := range []string{"User=alice", "StateDirectory=mcpd", "ExecStart=/usr/local/bin/mcpd serve --config /etc/mcpd/config.toml"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
	for _, forbidden := range []string{"mcpd.socket", "ListenStream", "Capability", "AmbientCapabilities"} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("unit contains removed/privileged feature %q:\n%s", forbidden, unit)
		}
	}
}

func TestStagedInstallPreservesExistingConfig(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "mcpd")
	if err := os.WriteFile(binary, []byte("test-binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Install(InstallOptions{Root: root, SourceBinary: binary})
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(result.Paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	cfg := SystemDefaultConfig()
	cfg.Server.Listen = "127.0.0.1:9999"
	changed, _ := toml.Marshal(cfg)
	if err := os.WriteFile(result.Paths.Config, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Install(InstallOptions{Root: root, SourceBinary: binary})
	if err != nil {
		t.Fatal(err)
	}
	if !second.ConfigPreserved {
		t.Fatal("existing config was not reported as preserved")
	}
	after, _ := os.ReadFile(result.Paths.Config)
	if string(after) == string(original) || string(after) != string(changed) {
		t.Fatal("reinstall overwrote existing config")
	}
}

func TestStagedInstallRemovesLegacySocketAndServiceVerifies(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze not available")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "mcpd")
	if err := os.WriteFile(binary, []byte("test-binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := rootedPath(root, legacySocketUnit)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Install(InstallOptions{Root: root, SourceBinary: binary, ServiceUser: current.Username})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy socket unit still exists: %v", err)
	}
	serviceData, err := os.ReadFile(result.Paths.ServiceUnit)
	if err != nil {
		t.Fatal(err)
	}
	serviceData = []byte(strings.Replace(string(serviceData), BinaryPath, "/bin/true", 1))
	verifyService := filepath.Join(t.TempDir(), "mcpd.service")
	if err := os.WriteFile(verifyService, serviceData, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("systemd-analyze", "verify", verifyService)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("systemd-analyze verify failed: %v\n%s", err, output)
	}
}

func TestStagedInstallDefersStartUntilOAuthPasswordExists(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "mcpd")
	if err := os.WriteFile(binary, []byte("test-binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := SystemDefaultConfig()
	cfg.Auth.Enabled = true
	cfg.Auth.ExternalURL = "https://mcp.example.com"
	cfgBytes, _ := toml.Marshal(cfg)
	sourceConfig := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(sourceConfig, cfgBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Install(InstallOptions{Root: root, SourceBinary: binary, SourceConfig: sourceConfig})
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsPassword {
		t.Fatal("OAuth install should require an owner password")
	}
	password := rootedPath(root, filepath.Join(cfg.Auth.StateDir, "owner.password"))
	if err := os.MkdirAll(filepath.Dir(password), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(password, []byte("verifier"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = Install(InstallOptions{Root: root, SourceBinary: binary, SourceConfig: sourceConfig})
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsPassword {
		t.Fatal("existing owner password was not detected")
	}
}

func TestSourceConfigUsesSafeSystemDefaults(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "mcpd")
	if err := os.WriteFile(binary, []byte("test-binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "partial.toml")
	if err := os.WriteFile(source, []byte("[server]\nshutdown_seconds = 12\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Install(InstallOptions{Root: root, SourceBinary: binary, SourceConfig: source})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadSystemConfig(result.Paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:31354" || cfg.Server.ShutdownSeconds != 12 {
		t.Fatalf("unexpected server defaults: %#v", cfg.Server)
	}
	if cfg.Auth.StateDir != "/var/lib/mcpd/auth" {
		t.Fatalf("unexpected auth state default: %q", cfg.Auth.StateDir)
	}
}
