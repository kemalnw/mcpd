package service

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	oauthsrv "github.com/kemalnw/mcpd/internal/oauth"
)

type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type DoctorReport struct {
	Healthy bool    `json:"healthy"`
	Checks  []Check `json:"checks"`
}

func Doctor(configPath string) DoctorReport {
	report := DoctorReport{Healthy: true}
	add := func(name, status, message string) {
		report.Checks = append(report.Checks, Check{Name: name, Status: status, Message: message})
		if status == "error" {
			report.Healthy = false
		}
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		add("systemd", "error", "systemd system manager is not available")
	} else if !commandAvailable("systemctl") || !commandAvailable("journalctl") {
		add("systemd", "error", "systemctl/journalctl are not available")
	} else {
		add("systemd", "ok", "systemd and journald commands are available")
	}
	if info, err := os.Stat(BinaryPath); err != nil {
		add("binary", "error", fmt.Sprintf("%s is not installed: %v", BinaryPath, err))
	} else if info.Mode().Perm()&0o111 == 0 {
		add("binary", "error", BinaryPath+" is not executable")
	} else {
		add("binary", "ok", BinaryPath+" is executable")
	}
	if configPath == "" {
		configPath = ConfigPath
	}
	cfg, err := LoadSystemConfig(configPath)
	if err != nil {
		add("config", "error", err.Error())
		return report
	}
	if _, err := os.Stat(configPath); err != nil {
		add("config", "error", fmt.Sprintf("%s is missing", configPath))
	} else {
		add("config", "ok", configPath+" is valid")
	}
	unitData, err := os.ReadFile(ServiceUnit)
	if err != nil {
		add("service-unit", "error", fmt.Sprintf("%s is missing: %v", ServiceUnit, err))
		return report
	}
	add("service-unit", "ok", ServiceUnit+" is installed")
	serviceUser := unitValue(string(unitData), "User")
	account, accountErr := accountByName(serviceUser)
	if accountErr != nil {
		add("service-user", "error", accountErr.Error())
	} else {
		add("service-user", "ok", fmt.Sprintf("service runs as %s (uid %d)", account.User, account.UID))
	}
	if commandAvailable("systemd-analyze") {
		cmd := exec.Command("systemd-analyze", "verify", ServiceUnit)
		if output, verifyErr := cmd.CombinedOutput(); verifyErr != nil {
			add("unit-verify", "error", strings.TrimSpace(string(output)))
		} else {
			add("unit-verify", "ok", "systemd-analyze verify passed")
		}
	}
	if info, err := os.Stat(StatePath); err != nil {
		add("state", "error", fmt.Sprintf("%s is missing: %v", StatePath, err))
	} else if info.Mode().Perm()&0o077 != 0 {
		add("state", "warning", fmt.Sprintf("%s permissions are %04o; 0700 is recommended", StatePath, info.Mode().Perm()))
	} else {
		add("state", "ok", StatePath+" permissions are restrictive")
	}
	if cfg.Auth.Enabled {
		if _, err := os.Stat(oauthsrv.PasswordPath(cfg.Auth.StateDir)); err != nil {
			add("owner-password", "error", "OAuth is enabled but owner password is not configured")
		} else {
			add("owner-password", "ok", "OAuth owner password verifier exists")
		}
	}
	if activeErr := exec.Command("systemctl", "is-active", "--quiet", ServiceName).Run(); activeErr != nil {
		add("service-active", "warning", "mcpd.service is not currently active")
	} else {
		add("service-active", "ok", "mcpd.service is active")
	}
	if ManagedCaddyConfigured() {
		addCaddyDoctorChecks(add)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	addBackendDoctorCheck(cfg.Server.Listen, add, client)
	if cfg.Auth.Enabled && cfg.Auth.ExternalURL != "" {
		addPublicDoctorChecks(cfg.Auth.ExternalURL, add, net.DefaultResolver, client)
	}
	return report
}

func unitValue(unit, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), prefix))
		}
	}
	return ""
}

func accountByName(name string) (Account, error) {
	if name == "" {
		return Account{}, fmt.Errorf("systemd unit does not declare User=")
	}
	u, err := user.Lookup(name)
	if err != nil {
		return Account{}, fmt.Errorf("resolve service user %q: %w", name, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return Account{}, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return Account{}, err
	}
	groupName := u.Gid
	if group, lookupErr := user.LookupGroupId(u.Gid); lookupErr == nil {
		groupName = group.Name
	}
	return Account{
		User: u.Username, Group: groupName, Home: filepath.Clean(u.HomeDir), UID: uid, GID: gid,
	}, nil
}

func addCaddyDoctorChecks(add func(string, string, string)) {
	if _, err := exec.LookPath("caddy"); err != nil {
		add("caddy-binary", "error", "managed Caddy config exists but caddy executable is unavailable")
		return
	}
	if !ManagedCaddyImportPresent() {
		add("caddy-config", "error", CaddyManagedConfigPath+" exists but is not imported by "+CaddyConfigPath)
		return
	}
	if err := exec.Command("systemctl", "is-active", "--quiet", CaddyServiceName).Run(); err != nil {
		add("caddy-service", "error", "managed Caddy is not active")
	} else {
		add("caddy-service", "ok", "caddy.service is active")
	}
	if err := validateCaddyConfig(); err != nil {
		add("caddy-config", "error", err.Error())
	} else {
		add("caddy-config", "ok", CaddyManagedConfigPath+" is loaded by a valid Caddy configuration")
	}
}

func addBackendDoctorCheck(listen string, add func(string, string, string), client *http.Client) {
	rawURL, err := localHealthURL(listen)
	if err != nil {
		add("backend-health", "error", err.Error())
		return
	}
	if err := checkHTTP200(context.Background(), client, rawURL); err != nil {
		add("backend-health", "error", fmt.Sprintf("%s: %v", rawURL, err))
		return
	}
	add("backend-health", "ok", rawURL+" is healthy")
}

func addPublicDoctorChecks(origin string, add func(string, string, string), resolver *net.Resolver, client *http.Client) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		add("public-dns", "error", "invalid public origin "+origin)
		return
	}
	host := u.Hostname()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addresses := []string{host}
	if net.ParseIP(host) == nil {
		addresses, err = resolver.LookupHost(ctx, host)
		if err != nil || len(addresses) == 0 {
			add("public-dns", "error", fmt.Sprintf("resolve %s: %v", host, err))
			add("public-https", "error", "skipped because public DNS resolution failed")
			add("public-oauth", "error", "skipped because public DNS resolution failed")
			return
		}
	}
	add("public-dns", "ok", fmt.Sprintf("%s -> %s", host, strings.Join(addresses, ", ")))

	base := strings.TrimSuffix(origin, "/")
	healthURL := base + "/healthz"
	if err := checkHTTP200(context.Background(), client, healthURL); err != nil {
		add("public-https", "error", fmt.Sprintf("%s: %v", healthURL, err))
	} else {
		add("public-https", "ok", healthURL+" is reachable with valid TLS")
	}
	oauthURL := base + "/.well-known/oauth-authorization-server"
	if err := checkHTTP200(context.Background(), client, oauthURL); err != nil {
		add("public-oauth", "error", fmt.Sprintf("%s: %v", oauthURL, err))
	} else {
		add("public-oauth", "ok", oauthURL+" is reachable")
	}
}

func localHealthURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("parse backend listener %q: %w", listen, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}

func checkHTTP200(ctx context.Context, client *http.Client, rawURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %s", resp.Status)
	}
	return nil
}
