package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultUpdateRepository = "kemalnw/mcpd"
	maxUpdateArchiveBytes   = 64 << 20
	maxChecksumsBytes       = 1 << 20
	maxUpdateBinaryBytes    = 64 << 20
)

type UpdateOptions struct {
	CurrentVersion string
	CheckOnly      bool
	Force          bool
	Repository     string
	LatestVersion  string
	ReleaseBaseURL string
	BinaryPath     string
	ConfigPath     string
	HTTPClient     *http.Client
	HealthTimeout  time.Duration
}

func Update(stdout, stderr io.Writer, opts UpdateOptions) error {
	if opts.Repository == "" {
		opts.Repository = defaultUpdateRepository
	}
	if opts.BinaryPath == "" {
		opts.BinaryPath = BinaryPath
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = ConfigPath
	}
	if opts.HealthTimeout <= 0 {
		opts.HealthTimeout = 10 * time.Second
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	latest, err := resolveLatestVersion(client, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Current: %s\n", opts.CurrentVersion)
	fmt.Fprintf(stdout, "Latest:  %s\n", latest)

	if opts.CurrentVersion == latest && !opts.Force {
		fmt.Fprintln(stdout, "MCPD is already up to date.")
		return nil
	}
	if cmp, comparable := compareReleaseVersions(opts.CurrentVersion, latest); comparable && cmp > 0 && !opts.Force {
		return fmt.Errorf("installed version %s is newer than latest release %s; use --force to replace it", opts.CurrentVersion, latest)
	}
	if opts.CheckOnly {
		fmt.Fprintf(stdout, "Update available: %s -> %s\n", opts.CurrentVersion, latest)
		return nil
	}
	if err := requireRoot("update"); err != nil {
		return err
	}
	if _, err := os.Stat(opts.BinaryPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s is not installed; run `sudo mcpd install` first", opts.BinaryPath)
		}
		return fmt.Errorf("inspect installed binary: %w", err)
	}

	arch, err := updateArchitecture()
	if err != nil {
		return err
	}
	versionNoV := strings.TrimPrefix(latest, "v")
	archiveName := fmt.Sprintf("mcpd_%s_linux_%s.tar.gz", versionNoV, arch)
	baseURL := opts.ReleaseBaseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://github.com/%s/releases/download/%s", opts.Repository, latest)
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	archive, err := fetchUpdateAsset(client, baseURL+"/"+archiveName, maxUpdateArchiveBytes)
	if err != nil {
		return fmt.Errorf("download %s: %w", archiveName, err)
	}
	checksums, err := fetchUpdateAsset(client, baseURL+"/checksums.txt", maxChecksumsBytes)
	if err != nil {
		return fmt.Errorf("download checksums.txt: %w", err)
	}
	if err := verifyArchiveChecksum(archiveName, archive, checksums); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "SHA-256 verified.")

	binary, err := extractReleaseBinary(archive, versionNoV, arch)
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "mcpd-update-*")
	if err != nil {
		return fmt.Errorf("create update staging directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	stagedBinary := filepath.Join(tmpDir, "mcpd")
	if err := os.WriteFile(stagedBinary, binary, 0o755); err != nil {
		return fmt.Errorf("stage update binary: %w", err)
	}
	if err := verifyStagedVersion(stagedBinary, latest); err != nil {
		return err
	}

	backup, err := backupInstalledBinary(opts.BinaryPath)
	if err != nil {
		return err
	}
	defer os.Remove(backup)
	if err := copyAtomic(stagedBinary, opts.BinaryPath, 0o755); err != nil {
		return fmt.Errorf("install update binary: %w", err)
	}
	fmt.Fprintf(stdout, "Installed %s.\n", latest)

	if err := Restart(stdout, stderr); err != nil {
		return rollbackUpdate(stdout, stderr, opts, backup, fmt.Errorf("restart updated service: %w", err))
	}
	if err := waitForBackendHealth(opts.ConfigPath, opts.HealthTimeout); err != nil {
		return rollbackUpdate(stdout, stderr, opts, backup, fmt.Errorf("updated service failed health check: %w", err))
	}

	fmt.Fprintf(stdout, "Updated MCPD %s -> %s.\n", opts.CurrentVersion, latest)
	return nil
}

func resolveLatestVersion(client *http.Client, opts UpdateOptions) (string, error) {
	if opts.LatestVersion != "" {
		if !validReleaseVersion(opts.LatestVersion) {
			return "", fmt.Errorf("invalid latest release version %q", opts.LatestVersion)
		}
		return opts.LatestVersion, nil
	}
	latestURL := fmt.Sprintf("https://github.com/%s/releases/latest", opts.Repository)
	req, err := http.NewRequest(http.MethodHead, latestURL, nil)
	if err != nil {
		return "", fmt.Errorf("build latest release request: %w", err)
	}
	req.Header.Set("User-Agent", "mcpd-updater")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("resolve latest release: HTTP %s", resp.Status)
	}
	latest := path.Base(strings.TrimSuffix(resp.Request.URL.Path, "/"))
	if !validReleaseVersion(latest) {
		return "", fmt.Errorf("latest release redirect returned invalid version %q", latest)
	}
	return latest, nil
}

func fetchUpdateAsset(client *http.Client, rawURL string, maxBytes int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
		return nil, errors.New("release URL must use HTTPS")
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mcpd-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	limited := io.LimitReader(resp.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("asset exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func verifyArchiveChecksum(name string, archive, checksums []byte) error {
	var expected string
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[1], "*") == name {
			expected = fields[0]
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksum entry missing for %s", name)
	}
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("invalid SHA-256 checksum for %s", name)
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("invalid SHA-256 checksum for %s: %w", name, err)
	}
	actual := sha256.Sum256(archive)
	if !strings.EqualFold(expected, hex.EncodeToString(actual[:])) {
		return fmt.Errorf("SHA-256 mismatch for %s", name)
	}
	return nil
}

func extractReleaseBinary(archive []byte, versionNoV, arch string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	pkg := fmt.Sprintf("mcpd_%s_linux_%s", versionNoV, arch)
	expected := map[string]bool{
		pkg + "/":          true,
		pkg + "/LICENSE":   true,
		pkg + "/README.md": true,
		pkg + "/mcpd":      true,
	}
	var binary []byte
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		if !expected[hdr.Name] || seen[hdr.Name] {
			return nil, fmt.Errorf("unexpected release archive entry %q", hdr.Name)
		}
		seen[hdr.Name] = true
		if hdr.Name != pkg+"/mcpd" {
			continue
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			return nil, errors.New("release mcpd entry is not a regular file")
		}
		if hdr.Size <= 0 || hdr.Size > maxUpdateBinaryBytes {
			return nil, fmt.Errorf("release mcpd binary has invalid size %d", hdr.Size)
		}
		binary, err = io.ReadAll(io.LimitReader(tr, maxUpdateBinaryBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read release mcpd binary: %w", err)
		}
		if int64(len(binary)) != hdr.Size {
			return nil, errors.New("release mcpd binary was truncated")
		}
	}
	for name := range expected {
		if !seen[name] {
			return nil, fmt.Errorf("release archive entry missing: %s", name)
		}
	}
	if len(binary) == 0 {
		return nil, errors.New("release archive contains an empty mcpd binary")
	}
	return binary, nil
}

func verifyStagedVersion(binaryPath, expected string) error {
	cmd := exec.Command(binaryPath, "version")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("validate staged mcpd binary: %w", err)
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(output, &info); err != nil {
		return fmt.Errorf("decode staged mcpd version: %w", err)
	}
	if info.Version != expected {
		return fmt.Errorf("staged binary reports version %s, expected %s", info.Version, expected)
	}
	return nil
}

func backupInstalledBinary(binaryPath string) (string, error) {
	src, err := os.Open(binaryPath)
	if err != nil {
		return "", fmt.Errorf("open installed binary for backup: %w", err)
	}
	defer src.Close()
	backup, err := os.CreateTemp(filepath.Dir(binaryPath), ".mcpd-rollback-*")
	if err != nil {
		return "", fmt.Errorf("create rollback binary: %w", err)
	}
	name := backup.Name()
	ok := false
	defer func() {
		_ = backup.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := backup.Chmod(0o755); err != nil {
		return "", fmt.Errorf("chmod rollback binary: %w", err)
	}
	if _, err := io.Copy(backup, src); err != nil {
		return "", fmt.Errorf("backup installed binary: %w", err)
	}
	if err := backup.Sync(); err != nil {
		return "", fmt.Errorf("sync rollback binary: %w", err)
	}
	if err := backup.Close(); err != nil {
		return "", fmt.Errorf("close rollback binary: %w", err)
	}
	ok = true
	return name, nil
}

func rollbackUpdate(stdout, stderr io.Writer, opts UpdateOptions, backup string, updateErr error) error {
	fmt.Fprintln(stderr, "Update failed; rolling back previous MCPD binary.")
	if err := copyAtomic(backup, opts.BinaryPath, 0o755); err != nil {
		return fmt.Errorf("%v; rollback binary restore failed: %w", updateErr, err)
	}
	if err := Restart(stdout, stderr); err != nil {
		return fmt.Errorf("%v; binary restored but rollback restart failed: %w", updateErr, err)
	}
	if err := waitForBackendHealth(opts.ConfigPath, opts.HealthTimeout); err != nil {
		return fmt.Errorf("%v; binary restored but rollback health check failed: %w", updateErr, err)
	}
	fmt.Fprintln(stderr, "Rollback complete; previous MCPD version is healthy.")
	return fmt.Errorf("%w; rolled back successfully", updateErr)
}

func waitForBackendHealth(configPath string, timeout time.Duration) error {
	cfg, err := LoadSystemConfig(configPath)
	if err != nil {
		return err
	}
	healthURL, err := localHealthURL(cfg.Server.Listen)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client := &http.Client{Timeout: time.Second}
	var lastErr error
	for {
		lastErr = checkHTTP200(ctx, client, healthURL)
		if lastErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", healthURL, lastErr)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func updateArchitecture() (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH, nil
	default:
		return "", fmt.Errorf("unsupported update architecture %s", runtime.GOARCH)
	}
}

type releaseVersion struct {
	major, minor, patch int
	pre                 []string
}

func compareReleaseVersions(a, b string) (int, bool) {
	va, ok := parseReleaseVersion(a)
	if !ok {
		return 0, false
	}
	vb, ok := parseReleaseVersion(b)
	if !ok {
		return 0, false
	}
	for _, pair := range [][2]int{{va.major, vb.major}, {va.minor, vb.minor}, {va.patch, vb.patch}} {
		if pair[0] < pair[1] {
			return -1, true
		}
		if pair[0] > pair[1] {
			return 1, true
		}
	}
	if len(va.pre) == 0 && len(vb.pre) == 0 {
		return 0, true
	}
	if len(va.pre) == 0 {
		return 1, true
	}
	if len(vb.pre) == 0 {
		return -1, true
	}
	limit := len(va.pre)
	if len(vb.pre) < limit {
		limit = len(vb.pre)
	}
	for i := 0; i < limit; i++ {
		ai, aErr := strconv.Atoi(va.pre[i])
		bi, bErr := strconv.Atoi(vb.pre[i])
		switch {
		case aErr == nil && bErr == nil:
			if ai < bi {
				return -1, true
			}
			if ai > bi {
				return 1, true
			}
		case aErr == nil:
			return -1, true
		case bErr == nil:
			return 1, true
		default:
			if va.pre[i] < vb.pre[i] {
				return -1, true
			}
			if va.pre[i] > vb.pre[i] {
				return 1, true
			}
		}
	}
	if len(va.pre) < len(vb.pre) {
		return -1, true
	}
	if len(va.pre) > len(vb.pre) {
		return 1, true
	}
	return 0, true
}

func parseReleaseVersion(value string) (releaseVersion, bool) {
	if !validReleaseVersion(value) {
		return releaseVersion{}, false
	}
	base := strings.TrimPrefix(value, "v")
	pre := []string(nil)
	if index := strings.IndexByte(base, '-'); index >= 0 {
		pre = strings.Split(base[index+1:], ".")
		base = base[:index]
	}
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return releaseVersion{}, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	patch, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return releaseVersion{}, false
	}
	return releaseVersion{major: major, minor: minor, patch: patch, pre: pre}, true
}

func validReleaseVersion(value string) bool {
	if !strings.HasPrefix(value, "v") {
		return false
	}
	body := strings.TrimPrefix(value, "v")
	if body == "" || strings.ContainsAny(body, "/\\") {
		return false
	}
	base := body
	pre := ""
	if index := strings.IndexByte(body, '-'); index >= 0 {
		base, pre = body[:index], body[index+1:]
		if pre == "" {
			return false
		}
	}
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	for _, r := range pre {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return true
}
