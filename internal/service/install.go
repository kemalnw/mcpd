package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/kemalnw/mcpd/internal/config"
	"github.com/pelletier/go-toml/v2"
)

type InstallOptions struct {
	Root         string
	SourceBinary string
	SourceConfig string
	ServiceUser  string
	ForceConfig  bool
	Enable       bool
	Start        bool
}

type InstallResult struct {
	Paths           Paths
	Account         Account
	ConfigPreserved bool
	NeedsPassword   bool
}

const (
	legacySocketUnit = "/etc/systemd/system/mcpd.socket"
	legacySocketName = "mcpd.socket"
)

func Install(opts InstallOptions) (InstallResult, error) {
	paths := PathsForRoot(opts.Root)
	if paths.Root == "/" && os.Geteuid() != 0 {
		return InstallResult{}, errors.New("system installation requires root; run `sudo mcpd install`")
	}
	account, err := resolveAccount(opts.ServiceUser)
	if err != nil {
		return InstallResult{}, err
	}
	binary := opts.SourceBinary
	if binary == "" {
		binary, err = os.Executable()
		if err != nil {
			return InstallResult{}, fmt.Errorf("resolve current executable: %w", err)
		}
	}
	if err := copyAtomic(binary, paths.Binary, 0o755); err != nil {
		return InstallResult{}, fmt.Errorf("install binary: %w", err)
	}
	cfg, preserved, err := installConfig(paths, opts)
	if err != nil {
		return InstallResult{}, err
	}
	if err := os.MkdirAll(paths.State, 0o700); err != nil {
		return InstallResult{}, fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(paths.State, 0o700); err != nil {
		return InstallResult{}, fmt.Errorf("chmod state directory: %w", err)
	}
	if err := chownTree(paths.State, account.UID, account.GID); err != nil {
		return InstallResult{}, fmt.Errorf("chown state directory to %s: %w", account.User, err)
	}
	if paths.Root == "/" {
		_ = runCommand(io.Discard, io.Discard, "systemctl", "disable", "--now", legacySocketName)
	}
	if err := os.Remove(rootedPath(paths.Root, legacySocketUnit)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return InstallResult{}, fmt.Errorf("remove legacy systemd socket: %w", err)
	}
	if err := writeAtomic(paths.ServiceUnit, []byte(RenderService(account)), 0o644); err != nil {
		return InstallResult{}, fmt.Errorf("install systemd service: %w", err)
	}
	if err := writeAtomic(paths.DurableServiceUnit, []byte(RenderDurableService(account)), 0o644); err != nil {
		return InstallResult{}, fmt.Errorf("install durable systemd service: %w", err)
	}
	needsPassword := false
	if cfg.Auth.Enabled {
		_, statErr := os.Stat(rootedPath(paths.Root, filepath.Join(cfg.Auth.StateDir, "owner.password")))
		needsPassword = errors.Is(statErr, os.ErrNotExist)
		if statErr != nil && !needsPassword {
			return InstallResult{}, fmt.Errorf("check owner password: %w", statErr)
		}
	}
	result := InstallResult{
		Paths: paths, Account: account, ConfigPreserved: preserved, NeedsPassword: needsPassword,
	}
	if paths.Root != "/" {
		return result, nil
	}
	if err := runCommand(io.Discard, os.Stderr, "systemctl", "daemon-reload"); err != nil {
		return InstallResult{}, err
	}
	if opts.Enable {
		if err := runCommand(os.Stdout, os.Stderr, "systemctl", "enable", DurableServiceName, ServiceName); err != nil {
			return InstallResult{}, err
		}
	}
	if opts.Start && !needsPassword {
		// Never restart the durable supervisor during an MCPD upgrade: doing so
		// would intentionally terminate its job cgroup. `start` is a no-op when
		// already active and starts it on first install.
		if err := runCommand(os.Stdout, os.Stderr, "systemctl", "start", DurableServiceName); err != nil {
			return InstallResult{}, err
		}
		if err := runCommand(os.Stdout, os.Stderr, "systemctl", "restart", ServiceName); err != nil {
			return InstallResult{}, err
		}
	}
	return result, nil
}
func installConfig(paths Paths, opts InstallOptions) (config.Config, bool, error) {
	if err := os.MkdirAll(filepath.Dir(paths.Config), 0o755); err != nil {
		return config.Config{}, false, fmt.Errorf("create config directory: %w", err)
	}
	if opts.SourceConfig != "" {
		cfg, err := LoadSystemConfig(opts.SourceConfig)
		if err != nil {
			return config.Config{}, false, err
		}
		data, err := toml.Marshal(cfg)
		if err != nil {
			return config.Config{}, false, fmt.Errorf("encode installed config: %w", err)
		}
		if err := writeAtomic(paths.Config, data, 0o644); err != nil {
			return config.Config{}, false, fmt.Errorf("install config: %w", err)
		}
		return cfg, false, nil
	}
	if !opts.ForceConfig {
		if _, err := os.Stat(paths.Config); err == nil {
			cfg, loadErr := LoadSystemConfig(paths.Config)
			return cfg, true, loadErr
		} else if !errors.Is(err, os.ErrNotExist) {
			return config.Config{}, false, err
		}
	}
	cfg := SystemDefaultConfig()
	data, err := toml.Marshal(cfg)
	if err != nil {
		return config.Config{}, false, fmt.Errorf("encode system config: %w", err)
	}
	if err := writeAtomic(paths.Config, data, 0o644); err != nil {
		return config.Config{}, false, fmt.Errorf("write system config: %w", err)
	}
	return cfg, false, nil
}

func SystemDefaultConfig() config.Config {
	cfg := config.Default()
	cfg.Server.Listen = "127.0.0.1:31354"
	cfg.Audit.Path = filepath.Join(StatePath, "audit.jsonl")
	cfg.Auth.StateDir = filepath.Join(StatePath, "auth")
	cfg.Workflow.StateDir = filepath.Join(StatePath, "runs")
	return cfg
}

func LoadSystemConfig(path string) (config.Config, error) {
	cfg := SystemDefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("read system config %q: %w", path, err)
	}
	decoded, err := config.Decode(data, cfg)
	if err != nil {
		return config.Config{}, fmt.Errorf("decode system config %q: %w", path, err)
	}
	return decoded, nil
}

func chownTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
}

func rootedPath(root, path string) string {
	if root == "" || root == "/" || !filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func resolveAccount(name string) (Account, error) {
	if name == "" {
		if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
			name = sudoUser
		}
	}
	var u *user.User
	var err error
	if name == "" {
		u, err = user.Current()
	} else {
		u, err = user.Lookup(name)
	}
	if err != nil {
		return Account{}, fmt.Errorf("resolve service user %q: %w", name, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return Account{}, fmt.Errorf("parse uid %q: %w", u.Uid, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return Account{}, fmt.Errorf("parse gid %q: %w", u.Gid, err)
	}
	groupName := u.Gid
	if group, lookupErr := user.LookupGroupId(u.Gid); lookupErr == nil {
		groupName = group.Name
	}
	return Account{User: u.Username, Group: groupName, Home: u.HomeDir, UID: uid, GID: gid}, nil
}

func copyAtomic(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return atomicFile(dst, mode, func(out *os.File) error {
		_, err := io.Copy(out, in)
		return err
	})
}
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicFile(path, mode, func(out *os.File) error {
		_, err := out.Write(data)
		return err
	})
}

func atomicFile(path string, mode os.FileMode, write func(*os.File) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcpd-install-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func runCommand(stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}
