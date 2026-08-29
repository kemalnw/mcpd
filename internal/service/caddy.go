package service

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	CaddyServiceName       = "caddy.service"
	CaddyConfigPath        = "/etc/caddy/Caddyfile"
	CaddyManagedConfigPath = "/etc/caddy/mcpd.caddy"
	caddyMarkerStart       = "# BEGIN mcpd managed include"
	caddyMarkerEnd         = "# END mcpd managed include"
)

type CaddyOptions struct {
	Host    string
	Backend string
}

func ManagedCaddyConfigured() bool {
	_, err := os.Stat(CaddyManagedConfigPath)
	return err == nil
}

func ManagedCaddyImportPresent() bool {
	data, err := os.ReadFile(CaddyConfigPath)
	if err != nil {
		return false
	}
	want := "import " + CaddyManagedConfigPath
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func PreflightManagedCaddy(host string) error {
	if strings.TrimSpace(host) == "" {
		return errors.New("Caddy hostname must not be empty")
	}
	if ip := net.ParseIP(host); ip == nil {
		if addresses, err := net.LookupHost(host); err != nil || len(addresses) == 0 {
			if err == nil {
				err = errors.New("no addresses returned")
			}
			return fmt.Errorf("resolve public domain %s: %w", host, err)
		}
	}
	if commandSuccessful("systemctl", "is-active", "--quiet", CaddyServiceName) {
		return nil
	}
	for _, address := range []string{":80", ":443"} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return fmt.Errorf("Caddy requires public listener %s but it is unavailable: %w", address, err)
		}
		_ = listener.Close()
	}
	return nil
}

func ConfigureManagedCaddy(opts CaddyOptions) error {
	if strings.TrimSpace(opts.Host) == "" || strings.TrimSpace(opts.Backend) == "" {
		return errors.New("Caddy host and backend are required")
	}
	if err := ensureCaddyInstalled(); err != nil {
		return err
	}
	if err := configureManagedCaddyFiles(CaddyConfigPath, CaddyManagedConfigPath, opts, validateCaddyConfig); err != nil {
		return err
	}
	if err := runCombined("systemctl", "enable", CaddyServiceName); err != nil {
		return err
	}
	if commandSuccessful("systemctl", "is-active", "--quiet", CaddyServiceName) {
		if err := runCombined("systemctl", "reload", CaddyServiceName); err != nil {
			return err
		}
	} else if err := runCombined("systemctl", "start", CaddyServiceName); err != nil {
		return err
	}
	return nil
}

func RenderManagedCaddyConfig(opts CaddyOptions) string {
	return fmt.Sprintf("# Managed by mcpd. Manual edits may be replaced by `mcpd setup`.\n%s {\n\treverse_proxy %s\n}\n", opts.Host, opts.Backend)
}

func ensureCaddyInstalled() error {
	if _, err := exec.LookPath("caddy"); err == nil {
		return nil
	}
	type installer struct {
		name string
		args [][]string
	}
	installers := []installer{
		{name: "apt-get", args: [][]string{{"update"}, {"install", "-y", "caddy"}}},
		{name: "dnf", args: [][]string{{"install", "-y", "caddy"}}},
		{name: "yum", args: [][]string{{"install", "-y", "caddy"}}},
	}
	for _, candidate := range installers {
		if _, err := exec.LookPath(candidate.name); err != nil {
			continue
		}
		for _, args := range candidate.args {
			cmd := exec.Command(candidate.name, args...)
			cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("install Caddy with %s: %w: %s", candidate.name, err, strings.TrimSpace(string(output)))
			}
		}
		if _, err := exec.LookPath("caddy"); err != nil {
			return errors.New("Caddy package installation completed but the caddy executable is unavailable")
		}
		return nil
	}
	return errors.New("Caddy is not installed and no supported package manager was found (apt-get, dnf, yum)")
}

func validateCaddyConfig() error {
	caddy, err := exec.LookPath("caddy")
	if err != nil {
		return errors.New("caddy executable is unavailable after installation")
	}
	cmd := exec.Command(caddy, "validate", "--config", CaddyConfigPath, "--adapter", "caddyfile")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("validate Caddy config: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func configureManagedCaddyFiles(mainPath, fragmentPath string, opts CaddyOptions, validate func() error) error {
	mainBefore, mainExists, err := readOptionalFile(mainPath)
	if err != nil {
		return err
	}
	fragmentBefore, fragmentExists, err := readOptionalFile(fragmentPath)
	if err != nil {
		return err
	}
	rollback := func() {
		_ = restoreOptionalFile(mainPath, mainBefore, mainExists)
		_ = restoreOptionalFile(fragmentPath, fragmentBefore, fragmentExists)
	}
	if err := os.MkdirAll(filepath.Dir(mainPath), 0o755); err != nil {
		return fmt.Errorf("create Caddy config directory: %w", err)
	}
	mainAfter := ensureCaddyImport(mainBefore, fragmentPath)
	if err := atomicWriteFile(fragmentPath, []byte(RenderManagedCaddyConfig(opts)), 0o644); err != nil {
		return fmt.Errorf("write managed Caddy config: %w", err)
	}
	if err := atomicWriteFile(mainPath, mainAfter, 0o644); err != nil {
		rollback()
		return fmt.Errorf("write Caddy import: %w", err)
	}
	if err := validate(); err != nil {
		rollback()
		return err
	}
	return nil
}

func ensureCaddyImport(data []byte, fragmentPath string) []byte {
	importLine := "import " + fragmentPath
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == importLine {
			return data
		}
	}
	text := strings.TrimRight(string(data), "\n")
	if text != "" {
		text += "\n\n"
	}
	text += caddyMarkerStart + "\n" + importLine + "\n" + caddyMarkerEnd + "\n"
	return []byte(text)
}

func readOptionalFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return data, true, nil
}

func restoreOptionalFile(path string, data []byte, existed bool) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return atomicWriteFile(path, data, 0o644)
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".mcpd-caddy-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func runCombined(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func commandSuccessful(name string, args ...string) bool {
	return exec.Command(name, args...).Run() == nil
}
