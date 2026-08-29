package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderManagedCaddyConfigRoutesWholeOrigin(t *testing.T) {
	got := RenderManagedCaddyConfig(CaddyOptions{Host: "mcp.example.com", Backend: "127.0.0.1:31354"})
	for _, want := range []string{"mcp.example.com {", "reverse_proxy 127.0.0.1:31354"} {
		if !strings.Contains(got, want) {
			t.Fatalf("config missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "handle /mcp") || strings.Contains(got, "route /mcp") {
		t.Fatalf("config unexpectedly restricts proxy to MCP path:\n%s", got)
	}
}

func TestConfigureManagedCaddyFilesPreservesExistingConfigAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "Caddyfile")
	fragmentPath := filepath.Join(dir, "mcpd.caddy")
	original := "other.example.com {\n\trespond \"existing\"\n}\n"
	if err := os.WriteFile(mainPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := CaddyOptions{Host: "mcp.example.com", Backend: "127.0.0.1:31354"}
	validateCalls := 0
	validate := func() error { validateCalls++; return nil }
	if err := configureManagedCaddyFiles(mainPath, fragmentPath, opts, validate); err != nil {
		t.Fatal(err)
	}
	if err := configureManagedCaddyFiles(mainPath, fragmentPath, opts, validate); err != nil {
		t.Fatal(err)
	}
	mainData, _ := os.ReadFile(mainPath)
	text := string(mainData)
	if !strings.Contains(text, original[:len(original)-1]) {
		t.Fatalf("existing config was not preserved:\n%s", text)
	}
	if got := strings.Count(text, "import "+fragmentPath); got != 1 {
		t.Fatalf("managed import count = %d, want 1:\n%s", got, text)
	}
	if validateCalls != 2 {
		t.Fatalf("validate calls = %d", validateCalls)
	}
}

func TestConfigureManagedCaddyFilesReconfigurationUpdatesOnlyFragment(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "Caddyfile")
	fragmentPath := filepath.Join(dir, "mcpd.caddy")
	if err := os.WriteFile(mainPath, []byte("other.example.com { respond \"ok\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := configureManagedCaddyFiles(mainPath, fragmentPath, CaddyOptions{Host: "old.example.com", Backend: "127.0.0.1:31354"}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	mainBefore, _ := os.ReadFile(mainPath)
	if err := configureManagedCaddyFiles(mainPath, fragmentPath, CaddyOptions{Host: "new.example.com", Backend: "127.0.0.1:40000"}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	mainAfter, _ := os.ReadFile(mainPath)
	if string(mainAfter) != string(mainBefore) {
		t.Fatalf("main Caddyfile changed during reconfiguration:\nbefore:\n%s\nafter:\n%s", mainBefore, mainAfter)
	}
	fragment, _ := os.ReadFile(fragmentPath)
	if !strings.Contains(string(fragment), "new.example.com") || !strings.Contains(string(fragment), "127.0.0.1:40000") {
		t.Fatalf("fragment was not updated:\n%s", fragment)
	}
}

func TestConfigureManagedCaddyFilesRollsBackOnValidationFailure(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "Caddyfile")
	fragmentPath := filepath.Join(dir, "mcpd.caddy")
	mainBefore := []byte("other.example.com { respond \"ok\" }\n")
	fragmentBefore := []byte("old.example.com { reverse_proxy 127.0.0.1:9999 }\n")
	if err := os.WriteFile(mainPath, mainBefore, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fragmentPath, fragmentBefore, 0o644); err != nil {
		t.Fatal(err)
	}
	err := configureManagedCaddyFiles(mainPath, fragmentPath, CaddyOptions{Host: "new.example.com", Backend: "127.0.0.1:31354"}, func() error {
		return errors.New("invalid config")
	})
	if err == nil || !strings.Contains(err.Error(), "invalid config") {
		t.Fatalf("error = %v", err)
	}
	mainAfter, _ := os.ReadFile(mainPath)
	fragmentAfter, _ := os.ReadFile(fragmentPath)
	if string(mainAfter) != string(mainBefore) || string(fragmentAfter) != string(fragmentBefore) {
		t.Fatalf("validation failure did not roll back files")
	}
}
