package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kemalnw/mcpd/internal/app"
	"github.com/kemalnw/mcpd/internal/config"
	"github.com/kemalnw/mcpd/internal/version"
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

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to TOML configuration")
	listen := fs.String("listen", "", "override server listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
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
  mcpd version
  mcpd help

Running without a command is equivalent to "mcpd serve".
`)
}
