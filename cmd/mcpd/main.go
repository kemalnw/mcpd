package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kemalnw/mcpd/internal/app"
	"github.com/kemalnw/mcpd/internal/config"
	oauthsrv "github.com/kemalnw/mcpd/internal/oauth"
	"github.com/kemalnw/mcpd/internal/service"
	"github.com/kemalnw/mcpd/internal/version"
	"golang.org/x/term"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mcpd:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return serve(nil)
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "install":
		return installCommand(args[1:])
	case "setup":
		return setupCommand(args[1:])
	case "start":
		return startCommand(args[1:])
	case "stop":
		return stopCommand(args[1:])
	case "restart":
		return restartCommand(args[1:])
	case "status":
		return statusCommand(args[1:])
	case "logs":
		return logsCommand(args[1:])
	case "doctor":
		return doctorCommand(args[1:])
	case "auth":
		return authCommand(args[1:])
	case "version", "--version", "-v":
		data, _ := json.MarshalIndent(version.Current(), "", "  ")
		fmt.Println(string(data))
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func authCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("auth requires a subcommand: set-password")
	}
	switch args[0] {
	case "set-password":
		return setOwnerPassword(args[1:])
	default:
		return fmt.Errorf("unknown auth subcommand %q", args[0])
	}
}

func setOwnerPassword(args []string) error {
	fs := flag.NewFlagSet("auth set-password", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to TOML configuration")
	passwordStdin := fs.Bool("password-stdin", false, "read the new password from standard input")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadCommandConfig(*configPath)
	if err != nil {
		return err
	}
	password, err := readOwnerPassword(*passwordStdin)
	if err != nil {
		return err
	}
	defer zeroBytes(password)
	if err := oauthsrv.SetPassword(cfg.Auth.StateDir, password); err != nil {
		return err
	}
	if *configPath == service.ConfigPath && os.Geteuid() == 0 {
		if err := service.ChownToInstalledAccount(cfg.Auth.StateDir); err != nil {
			return err
		}
		if err := service.ChownToInstalledAccount(oauthsrv.PasswordPath(cfg.Auth.StateDir)); err != nil {
			return err
		}
	}
	fmt.Println("Owner password updated.")
	return nil
}

func readOwnerPassword(fromStdin bool) ([]byte, error) {
	if fromStdin {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
		if err != nil {
			return nil, fmt.Errorf("read password from stdin: %w", err)
		}
		if len(data) > 4096 {
			return nil, errors.New("password input exceeds 4096 bytes")
		}
		data = bytesTrimLineEnding(data)
		return data, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New("stdin is not a terminal; use --password-stdin for non-interactive setup")
	}
	fmt.Fprint(os.Stderr, "New owner password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("read owner password: %w", err)
	}
	fmt.Fprint(os.Stderr, "Confirm owner password: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		zeroBytes(first)
		return nil, fmt.Errorf("confirm owner password: %w", err)
	}
	defer zeroBytes(second)
	if string(first) != string(second) {
		zeroBytes(first)
		return nil, errors.New("password confirmation does not match")
	}
	return first, nil
}

func bytesTrimLineEnding(value []byte) []byte {
	if len(value) > 0 && value[len(value)-1] == '\n' {
		value = value[:len(value)-1]
		if len(value) > 0 && value[len(value)-1] == '\r' {
			value = value[:len(value)-1]
		}
	}
	return value
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func loadCommandConfig(path string) (config.Config, error) {
	if path == service.ConfigPath {
		return service.LoadSystemConfig(path)
	}
	return config.Load(path)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to TOML configuration")
	listen := fs.String("listen", "", "override server listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadCommandConfig(*configPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Server.Listen = *listen
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	application, err := app.New(cfg, logger)
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() { errCh <- application.Run() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Server.ShutdownSeconds)*time.Second)
		defer cancel()
		if err := application.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}
}

func printUsage() {
	fmt.Print(`mcpd — self-hosted MCP daemon for Linux VMs

Usage:
  mcpd serve [--config PATH] [--listen ADDR]
  mcpd setup [--domain HOST] [--yes] [--reconfigure]
  mcpd install [--user USER] [--config PATH] [--no-enable] [--no-start]
  mcpd start | stop | restart | status
  mcpd logs [--follow] [--lines N] [--since TIME]
  mcpd doctor [--config PATH] [--json]
  mcpd auth set-password [--config PATH] [--password-stdin]
  mcpd version
  mcpd help

Running without a command is equivalent to "mcpd serve".
`)
}
