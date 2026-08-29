package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthSetPasswordFromStdin(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "auth")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configBody := []byte("[auth]\nstate_dir = \"" + stateDir + "\"\n")
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatal(err)
	}

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = read
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = read.Close()
	})
	if _, err := write.WriteString("a-long-test-owner-credential\n"); err != nil {
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}

	if err := setOwnerPassword([]string{"--config", configPath, "--password-stdin"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(stateDir, "owner.password"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("owner.password mode = %#o, want 0600", got)
	}
}
func TestInstallCommandStaged(t *testing.T) {
	root := t.TempDir()
	if err := installCommand([]string{"--root", root, "--no-enable", "--no-start"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(root, "usr/local/bin/mcpd"),
		filepath.Join(root, "etc/mcpd/config.toml"),
		filepath.Join(root, "etc/systemd/system/mcpd.service"),
		filepath.Join(root, "var/lib/mcpd"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("staged install missing %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system/mcpd.socket")); !os.IsNotExist(err) {
		t.Fatalf("safe default install should not create mcpd.socket, err=%v", err)
	}
}

func TestSetupRejectsNonInteractivePrompting(t *testing.T) {
	oldEUID, oldTTY := setupEUID, setupIsTerminal
	setupEUID = func() int { return 0 }
	setupIsTerminal = func() bool { return false }
	t.Cleanup(func() { setupEUID, setupIsTerminal = oldEUID, oldTTY })
	err := setupCommand(nil)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("setupCommand error = %v", err)
	}
}

func TestSetupFlagsKeepAutomationExplicit(t *testing.T) {
	opts, err := parseSetupFlags([]string{"--domain", "mcp.example.com", "--yes", "--password-stdin", "--reconfigure"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Domain != "mcp.example.com" || !opts.Yes || !opts.PasswordStdin || !opts.Reconfigure {
		t.Fatalf("unexpected setup options: %#v", opts)
	}
}
