package main

import (
	"os"
	"path/filepath"
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
