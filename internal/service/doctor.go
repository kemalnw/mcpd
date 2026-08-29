package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

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
