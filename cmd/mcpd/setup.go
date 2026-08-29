package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kemalnw/mcpd/internal/config"
	oauthsrv "github.com/kemalnw/mcpd/internal/oauth"
	"github.com/kemalnw/mcpd/internal/service"
	setupcore "github.com/kemalnw/mcpd/internal/setup"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/term"
)

var (
	setupEUID       = os.Geteuid
	setupIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
)

type setupCLIOptions struct {
	Domain        string
	Listen        string
	MCPPath       string
	ServiceUser   string
	NoAuth        bool
	PasswordStdin bool
	HTTPSReady    bool
	Yes           bool
	Reconfigure   bool
}

func setupCommand(args []string) error {
	if runtime.GOOS != "linux" {
		return errors.New("setup is supported only on Linux")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return fmt.Errorf("setup is unsupported on architecture %s", runtime.GOARCH)
	}
	if setupEUID() != 0 {
		return errors.New("setup requires root; run `sudo mcpd setup`")
	}
	opts, err := parseSetupFlags(args)
	if err != nil {
		return err
	}
	interactive := setupIsTerminal()
	if !interactive && !opts.Yes {
		return errors.New("setup is non-interactive; pass --yes with explicit options or run `sudo mcpd setup` from a terminal")
	}
	if interactive {
		return runInteractiveSetup(opts)
	}
	return runNonInteractiveSetup(opts)
}

func parseSetupFlags(args []string) (setupCLIOptions, error) {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	var opts setupCLIOptions
	fs.StringVar(&opts.Domain, "domain", "", "public domain or canonical HTTPS origin")
	fs.StringVar(&opts.Listen, "listen", "", "backend HTTP listen address")
	fs.StringVar(&opts.MCPPath, "mcp-path", "", "MCP endpoint path")
	fs.StringVar(&opts.ServiceUser, "user", "", "Unix service user")
	fs.BoolVar(&opts.NoAuth, "no-auth", false, "disable OAuth authentication")
	fs.BoolVar(&opts.PasswordStdin, "password-stdin", false, "read owner password from stdin")
	fs.BoolVar(&opts.HTTPSReady, "https-ready", false, "record that external HTTPS is already configured")
	fs.BoolVar(&opts.Yes, "yes", false, "apply without confirmation")
	fs.BoolVar(&opts.Reconfigure, "reconfigure", false, "replace an existing config")
	if err := fs.Parse(args); err != nil {
		return setupCLIOptions{}, err
	}
	if fs.NArg() != 0 {
		return setupCLIOptions{}, errors.New("setup does not accept positional arguments")
	}
	return opts, nil
}

func runInteractiveSetup(opts setupCLIOptions) error {
	fmt.Println("mcpd setup")
	fmt.Println()
	existing := pathExists(service.ConfigPath)
	if existing && !opts.Reconfigure {
		choice, err := promptExistingChoice()
		if err != nil {
			return err
		}
		switch choice {
		case "keep":
			return repairExistingSetup(true, false)
		case "cancel":
			fmt.Println("Setup cancelled.")
			return nil
		case "reconfigure":
			opts.Reconfigure = true
		}
	}
	plan, password, err := collectInteractivePlan(opts)
	if err != nil {
		return err
	}
	defer zeroBytes(password)
	printSetupSummary(plan)
	ok, err := promptYesNo("Apply configuration?", true)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("Setup cancelled; no changes were applied.")
		return nil
	}
	return applySetup(plan, password, true, existing)
}

func collectInteractivePlan(opts setupCLIOptions) (setupcore.Plan, []byte, error) {
	if opts.Domain == "" {
		value, err := promptRequired("Public domain")
		if err != nil {
			return setupcore.Plan{}, nil, err
		}
		opts.Domain = value
	}
	defaultUser, err := defaultSetupUser()
	if err != nil {
		return setupcore.Plan{}, nil, err
	}
	if opts.ServiceUser == "" {
		value, err := promptDefault("Service user", defaultUser)
		if err != nil {
			return setupcore.Plan{}, nil, err
		}
		opts.ServiceUser = value
	}
	oauthEnabled := !opts.NoAuth
	if !opts.NoAuth {
		oauthEnabled, err = promptYesNo("Enable OAuth authentication?", true)
		if err != nil {
			return setupcore.Plan{}, nil, err
		}
	}
	if opts.Listen == "" {
		opts.Listen, err = promptDefault("Backend listener", "127.0.0.1:31354")
		if err != nil {
			return setupcore.Plan{}, nil, err
		}
	}
	if opts.MCPPath == "" {
		opts.MCPPath, err = promptDefault("MCP path", "/mcp")
		if err != nil {
			return setupcore.Plan{}, nil, err
		}
	}
	if !opts.HTTPSReady {
		fmt.Println()
		fmt.Println("HTTPS is terminated outside mcpd.")
		fmt.Println("  1. Existing reverse proxy / tunnel")
		fmt.Println("  2. Not configured yet")
		choice, err := promptDefault("Choice", "1")
		if err != nil {
			return setupcore.Plan{}, nil, err
		}
		switch choice {
		case "1":
			opts.HTTPSReady = true
		case "2":
			opts.HTTPSReady = false
		default:
			return setupcore.Plan{}, nil, errors.New("HTTPS choice must be 1 or 2")
		}
	}
	plan, err := setupcore.BuildFrom(service.SystemDefaultConfig(), setupcore.Input{
		PublicOrigin: opts.Domain, Listen: opts.Listen, MCPPath: opts.MCPPath,
		ServiceUser: opts.ServiceUser, OAuthEnabled: oauthEnabled, HTTPSConfigured: opts.HTTPSReady,
	})
	if err != nil {
		return setupcore.Plan{}, nil, err
	}
	var password []byte
	if oauthEnabled && !ownerPasswordExists(plan.Config.Auth.StateDir) {
		password, err = readValidatedOwnerPassword()
	}
	return plan, password, err
}
func runNonInteractiveSetup(opts setupCLIOptions) error {
	existing := pathExists(service.ConfigPath)
	if existing && !opts.Reconfigure {
		if opts.Domain != "" || opts.Listen != "" || opts.MCPPath != "" || opts.ServiceUser != "" || opts.NoAuth {
			return errors.New("existing config is preserved by default; pass --reconfigure to apply new setup values")
		}
		return repairExistingSetup(false, opts.PasswordStdin)
	}
	if strings.TrimSpace(opts.Domain) == "" {
		return errors.New("--domain is required for non-interactive setup")
	}
	if opts.ServiceUser == "" {
		value, err := defaultSetupUser()
		if err != nil {
			return err
		}
		opts.ServiceUser = value
	}
	plan, err := setupcore.BuildFrom(service.SystemDefaultConfig(), setupcore.Input{
		PublicOrigin: opts.Domain, Listen: opts.Listen, MCPPath: opts.MCPPath,
		ServiceUser: opts.ServiceUser, OAuthEnabled: !opts.NoAuth, HTTPSConfigured: opts.HTTPSReady,
	})
	if err != nil {
		return err
	}
	password, err := nonInteractivePassword(plan, opts.PasswordStdin)
	if err != nil {
		return err
	}
	defer zeroBytes(password)
	return applySetup(plan, password, true, existing)
}
func nonInteractivePassword(plan setupcore.Plan, fromStdin bool) ([]byte, error) {
	if !plan.Config.Auth.Enabled {
		return nil, nil
	}
	if ownerPasswordExists(plan.Config.Auth.StateDir) && !fromStdin {
		return nil, nil
	}
	if !fromStdin {
		return nil, errors.New("OAuth owner password is not configured; use --password-stdin")
	}
	password, err := readOwnerPassword(true)
	if err != nil {
		return nil, err
	}
	if err := oauthsrv.ValidateOwnerPassword(password); err != nil {
		zeroBytes(password)
		return nil, err
	}
	return password, nil
}

func repairExistingSetup(interactive, fromStdin bool) error {
	cfg, err := service.LoadSystemConfig(service.ConfigPath)
	if err != nil {
		return fmt.Errorf("existing config cannot be preserved as-is: %w; rerun setup and choose reconfigure", err)
	}
	serviceUser, err := installedOrDefaultUser()
	if err != nil {
		return err
	}
	plan := setupcore.Plan{Config: cfg, PublicOrigin: cfg.Auth.ExternalURL, ServiceUser: serviceUser}
	var password []byte
	if cfg.Auth.Enabled && !ownerPasswordExists(cfg.Auth.StateDir) {
		if interactive {
			password, err = readValidatedOwnerPassword()
		} else if fromStdin {
			password, err = readOwnerPassword(true)
			if err == nil {
				err = oauthsrv.ValidateOwnerPassword(password)
			}
		} else {
			return errors.New("OAuth owner password is missing; use --password-stdin or an interactive terminal")
		}
		if err != nil {
			zeroBytes(password)
			return err
		}
	}
	defer zeroBytes(password)
	if interactive {
		fmt.Println("Keeping existing configuration and repairing the service.")
		printSetupSummary(plan)
	}
	return applySetup(plan, password, false, true)
}

func applySetup(plan setupcore.Plan, password []byte, reconfigure, existing bool) error {
	executor := &systemSetupExecutor{reconfigure: reconfigure, existing: existing}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := setupcore.Apply(ctx, plan, setupcore.ApplyOptions{Password: password}, executor); err != nil {
		return fmt.Errorf("setup failed: %w (run `mcpd doctor` and `mcpd logs --lines 100` for diagnostics)", err)
	}
	fmt.Println()
	fmt.Println("✓ mcpd installed")
	fmt.Println("✓ configuration ready")
	if plan.Config.Auth.Enabled {
		fmt.Println("✓ OAuth owner configured")
	}
	fmt.Println("✓ mcpd.service running")
	fmt.Println("✓ local health check passed")
	if plan.PublicOrigin != "" {
		fmt.Printf("\nPublic MCP endpoint: %s\n", plan.PublicMCPURL())
	}
	fmt.Printf("Backend:             %s\n", plan.BackendURL())
	fmt.Printf("Config:              %s\n", service.ConfigPath)
	if plan.HTTPSConfigured {
		fmt.Println("External HTTPS:      configured by deployment owner (not verified by mcpd)")
	} else {
		fmt.Println("External HTTPS:      still needs configuration")
		fmt.Printf("Next: route your HTTPS domain to %s\n", plan.BackendURL())
	}
	return nil
}

func printSetupSummary(plan setupcore.Plan) {
	fmt.Println()
	fmt.Println("Configuration summary")
	if plan.PublicOrigin != "" {
		fmt.Printf("  public URL     %s\n", plan.PublicOrigin)
		fmt.Printf("  endpoint       %s\n", plan.PublicMCPURL())
	}
	fmt.Printf("  backend        %s\n", plan.BackendURL())
	fmt.Printf("  service user   %s\n", plan.ServiceUser)
	fmt.Printf("  OAuth          %t\n", plan.Config.Auth.Enabled)
	fmt.Printf("  external HTTPS %t\n", plan.HTTPSConfigured)
	fmt.Println()
	fmt.Println("mcpd itself will serve HTTP only; HTTPS remains outside mcpd.")
}

type systemSetupExecutor struct {
	reconfigure bool
	existing    bool
	plan        setupcore.Plan
}

func (e *systemSetupExecutor) Preflight(plan setupcore.Plan) error {
	e.plan = plan
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemctl is unavailable; a systemd-based Linux system is required")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errors.New("systemd system manager is unavailable")
	}
	if _, err := user.Lookup(plan.ServiceUser); err != nil {
		return fmt.Errorf("service user %q is unavailable: %w", plan.ServiceUser, err)
	}
	if err := plan.Config.Validate(); err != nil {
		return fmt.Errorf("configuration validation: %w", err)
	}
	if !e.existing || e.reconfigure {
		if err := checkSetupListener(plan.Config.Server.Listen, e.existing); err != nil {
			return err
		}
	}
	return nil
}

func checkSetupListener(address string, existing bool) error {
	if existing {
		if cfg, err := service.LoadSystemConfig(service.ConfigPath); err == nil && cfg.Server.Listen == address {
			return nil
		}
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("backend listener %s is unavailable: %w", address, err)
	}
	return listener.Close()
}

func (e *systemSetupExecutor) Install(plan setupcore.Plan) error {
	opts := service.InstallOptions{Root: "/", ServiceUser: plan.ServiceUser, Enable: true, Start: false}
	if e.reconfigure {
		path, cleanup, err := writeSetupConfig(plan.Config)
		if err != nil {
			return err
		}
		defer cleanup()
		opts.SourceConfig = path
		opts.ForceConfig = true
	}
	_, err := service.Install(opts)
	return err
}

func writeSetupConfig(cfg config.Config) (string, func(), error) {
	data, err := toml.Marshal(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("encode configuration: %w", err)
	}
	file, err := os.CreateTemp("", "mcpd-setup-*.toml")
	if err != nil {
		return "", nil, fmt.Errorf("create temporary config: %w", err)
	}
	name := file.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		cleanup()
		return "", nil, err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		cleanup()
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return name, cleanup, nil
}

func (e *systemSetupExecutor) ConfigurePassword(password []byte) error {
	if err := oauthsrv.SetPassword(e.plan.Config.Auth.StateDir, password); err != nil {
		return err
	}
	if err := service.ChownToInstalledAccount(e.plan.Config.Auth.StateDir); err != nil {
		return err
	}
	return service.ChownToInstalledAccount(oauthsrv.PasswordPath(e.plan.Config.Auth.StateDir))
}

func (e *systemSetupExecutor) Restart() error { return service.Restart(io.Discard, os.Stderr) }

func (e *systemSetupExecutor) Doctor() error {
	report := service.Doctor(service.ConfigPath)
	if report.Healthy {
		return nil
	}
	var messages []string
	for _, check := range report.Checks {
		if check.Status == "error" {
			messages = append(messages, check.Name+": "+check.Message)
		}
	}
	return errors.New(strings.Join(messages, "; "))
}

func (e *systemSetupExecutor) HealthCheck(ctx context.Context, rawURL string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	var last error
	for i := 0; i < 15; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("status %s", resp.Status)
		} else {
			last = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return last
}

func defaultSetupUser() (string, error) {
	if name := strings.TrimSpace(os.Getenv("SUDO_USER")); name != "" && name != "root" {
		return name, nil
	}
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve current user: %w", err)
	}
	return u.Username, nil
}

func installedOrDefaultUser() (string, error) {
	if account, err := service.InstalledAccount(); err == nil {
		return account.User, nil
	}
	return defaultSetupUser()
}

func ownerPasswordExists(stateDir string) bool {
	_, err := os.Stat(oauthsrv.PasswordPath(stateDir))
	return err == nil
}

func readValidatedOwnerPassword() ([]byte, error) {
	for {
		password, err := readOwnerPassword(false)
		if err != nil {
			return nil, err
		}
		if err := oauthsrv.ValidateOwnerPassword(password); err != nil {
			zeroBytes(password)
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		return password, nil
	}
}

func promptLine(label string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", label)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func promptRequired(label string) (string, error) {
	for {
		value, err := promptLine(label)
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(os.Stderr, "A value is required.")
	}
}

func promptDefault(label, defaultValue string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s [%s]: ", label, defaultValue)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		value = defaultValue
	}
	return value, nil
}

func promptYesNo(label string, defaultYes bool) (bool, error) {
	suffix := "[Y/n]"
	if !defaultYes {
		suffix = "[y/N]"
	}
	fmt.Fprintf(os.Stderr, "%s %s: ", label, suffix)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	value := strings.ToLower(strings.TrimSpace(line))
	if value == "" {
		return defaultYes, nil
	}
	switch value {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("expected yes or no, got %q", value)
	}
}

func promptExistingChoice() (string, error) {
	fmt.Println("Existing installation detected.")
	fmt.Println("  1. Keep current configuration and repair/reinstall service")
	fmt.Println("  2. Reconfigure interactively")
	fmt.Println("  3. Cancel")
	value, err := promptDefault("Choice", "1")
	if err != nil {
		return "", err
	}
	switch value {
	case "1":
		return "keep", nil
	case "2":
		return "reconfigure", nil
	case "3":
		return "cancel", nil
	default:
		return "", errors.New("choice must be 1, 2, or 3")
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(filepath.Clean(path))
	return err == nil
}
