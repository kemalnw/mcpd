package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompareReleaseVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v1.0.0", "v1.0.0", 0, true},
		{"v1.0.0", "v1.0.1", -1, true},
		{"v2.0.0", "v1.9.9", 1, true},
		{"v1.0.0-rc.1", "v1.0.0", -1, true},
		{"v1.0.0-rc.2", "v1.0.0-rc.10", -1, true},
		{"dev", "v1.0.0", 0, false},
	}
	for _, tt := range tests {
		got, ok := compareReleaseVersions(tt.a, tt.b)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("compareReleaseVersions(%q, %q) = (%d, %v), want (%d, %v)", tt.a, tt.b, got, ok, tt.want, tt.ok)
		}
	}
}

func TestUpdateNoopWhenAlreadyLatest(t *testing.T) {
	var out bytes.Buffer
	if err := Update(&out, &out, UpdateOptions{
		CurrentVersion: "v0.4.1",
		LatestVersion:  "v0.4.1",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already up to date") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
}

func TestUpdateInstallsVerifiedReleaseAndRestartsMainService(t *testing.T) {
	newVersion := "v0.4.2"
	newBinary := testVersionScript(newVersion)
	archiveName, archive := testReleaseArchive(t, newVersion, newBinary)
	checksum := sha256.Sum256(archive)
	release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + archiveName:
			_, _ = w.Write(archive)
		case "/checksums.txt":
			fmt.Fprintf(w, "%x  %s\n", checksum, archiveName)
		default:
			http.NotFound(w, r)
		}
	}))
	defer release.Close()

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()

	root := t.TempDir()
	binaryPath := filepath.Join(root, "mcpd")
	if err := os.WriteFile(binaryPath, testVersionScript("v0.4.1"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := writeUpdateTestConfig(t, root, health.URL)
	commandLog := installFakeSystemctl(t)
	setUpdateTestRoot(t)

	var stdout, stderr bytes.Buffer
	if err := Update(&stdout, &stderr, UpdateOptions{
		CurrentVersion: "v0.4.1",
		LatestVersion:  newVersion,
		ReleaseBaseURL: release.URL,
		BinaryPath:     binaryPath,
		ConfigPath:     configPath,
		HealthTimeout:  time.Second,
	}); err != nil {
		t.Fatalf("Update() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	gotBinary, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBinary) != string(newBinary) {
		t.Fatal("installed binary does not match release binary")
	}
	logData, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "systemctl start mcpd-durable.service") {
		t.Fatalf("durable supervisor was not ensured active:\n%s", logText)
	}
	if strings.Contains(logText, "restart mcpd-durable.service") {
		t.Fatalf("durable supervisor must not be restarted during update:\n%s", logText)
	}
	if strings.Count(logText, "systemctl restart mcpd.service") != 1 {
		t.Fatalf("main service restart count unexpected:\n%s", logText)
	}
	if !strings.Contains(stdout.String(), "Updated MCPD v0.4.1 -> v0.4.2") {
		t.Fatalf("unexpected stdout:\n%s", stdout.String())
	}
}

func TestUpdateRollsBackWhenNewServiceIsUnhealthy(t *testing.T) {
	newVersion := "v0.4.2"
	oldBinary := testVersionScript("v0.4.1")
	newBinary := testVersionScript(newVersion)
	archiveName, archive := testReleaseArchive(t, newVersion, newBinary)
	checksum := sha256.Sum256(archive)
	release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + archiveName:
			_, _ = w.Write(archive)
		case "/checksums.txt":
			fmt.Fprintf(w, "%x  %s\n", checksum, archiveName)
		default:
			http.NotFound(w, r)
		}
	}))
	defer release.Close()

	root := t.TempDir()
	binaryPath := filepath.Join(root, "mcpd")
	if err := os.WriteFile(binaryPath, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	commandLog := installFakeSystemctl(t)
	setUpdateTestRoot(t)

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logData, _ := os.ReadFile(commandLog)
		if strings.Count(string(logData), "systemctl restart mcpd.service") >= 2 {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "new version unhealthy", http.StatusServiceUnavailable)
	}))
	defer health.Close()
	configPath := writeUpdateTestConfig(t, root, health.URL)

	var stdout, stderr bytes.Buffer
	err := Update(&stdout, &stderr, UpdateOptions{
		CurrentVersion: "v0.4.1",
		LatestVersion:  newVersion,
		ReleaseBaseURL: release.URL,
		BinaryPath:     binaryPath,
		ConfigPath:     configPath,
		HealthTimeout:  250 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "rolled back successfully") {
		t.Fatalf("Update() error = %v, want successful rollback; stderr:\n%s", err, stderr.String())
	}
	gotBinary, readErr := os.ReadFile(binaryPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(gotBinary) != string(oldBinary) {
		t.Fatal("rollback did not restore previous binary")
	}
	logData, readErr := os.ReadFile(commandLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Count(string(logData), "systemctl restart mcpd.service") != 2 {
		t.Fatalf("expected update restart plus rollback restart:\n%s", string(logData))
	}
	if !strings.Contains(stderr.String(), "Rollback complete") {
		t.Fatalf("rollback message missing:\n%s", stderr.String())
	}
}

func TestUpdateRejectsChecksumMismatchBeforeReplacingBinary(t *testing.T) {
	newVersion := "v0.4.2"
	oldBinary := testVersionScript("v0.4.1")
	archiveName, archive := testReleaseArchive(t, newVersion, testVersionScript(newVersion))
	release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + archiveName:
			_, _ = w.Write(archive)
		case "/checksums.txt":
			fmt.Fprintf(w, "%064x  %s\n", 1, archiveName)
		default:
			http.NotFound(w, r)
		}
	}))
	defer release.Close()

	root := t.TempDir()
	binaryPath := filepath.Join(root, "mcpd")
	if err := os.WriteFile(binaryPath, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	setUpdateTestRoot(t)
	var out bytes.Buffer
	err := Update(&out, &out, UpdateOptions{
		CurrentVersion: "v0.4.1",
		LatestVersion:  newVersion,
		ReleaseBaseURL: release.URL,
		BinaryPath:     binaryPath,
	})
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("Update() error = %v, want checksum mismatch", err)
	}
	got, readErr := os.ReadFile(binaryPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(oldBinary) {
		t.Fatal("binary changed despite checksum failure")
	}
}

func testReleaseArchive(t *testing.T, version string, binary []byte) (string, []byte) {
	t.Helper()
	arch, err := updateArchitecture()
	if err != nil {
		t.Fatal(err)
	}
	versionNoV := strings.TrimPrefix(version, "v")
	pkg := fmt.Sprintf("mcpd_%s_linux_%s", versionNoV, arch)
	archiveName := pkg + ".tar.gz"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	entries := []struct {
		name     string
		mode     int64
		body     []byte
		typeflag byte
	}{
		{pkg + "/", 0o755, nil, tar.TypeDir},
		{pkg + "/LICENSE", 0o644, []byte("MIT\n"), tar.TypeReg},
		{pkg + "/README.md", 0o644, []byte("mcpd\n"), tar.TypeReg},
		{pkg + "/mcpd", 0o755, binary, tar.TypeReg},
	}
	for _, entry := range entries {
		hdr := &tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.body)), Typeflag: entry.typeflag}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if len(entry.body) > 0 {
			if _, err := tw.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return archiveName, buf.Bytes()
}

func testVersionScript(version string) []byte {
	return []byte("#!/bin/sh\nif [ \"${1:-}\" = version ]; then\n  printf '{\"version\":\"" + version + "\",\"commit\":\"test\",\"date\":\"test\"}\\n'\n  exit 0\nfi\nexit 0\n")
}

func writeUpdateTestConfig(t *testing.T, root, healthURL string) string {
	t.Helper()
	listen := strings.TrimPrefix(healthURL, "http://")
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte("[server]\nlisten = \""+listen+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func installFakeSystemctl(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "systemctl.log")
	script := "#!/bin/sh\nprintf 'systemctl %s\\n' \"$*\" >> \"$MCPD_UPDATE_TEST_LOG\"\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("MCPD_UPDATE_TEST_LOG", logPath)
	return logPath
}

func setUpdateTestRoot(t *testing.T) {
	t.Helper()
	old := effectiveUID
	effectiveUID = func() int { return 0 }
	t.Cleanup(func() { effectiveUID = old })
}
