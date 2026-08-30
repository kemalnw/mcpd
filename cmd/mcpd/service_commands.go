package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/kemalnw/mcpd/internal/service"
	"github.com/kemalnw/mcpd/internal/version"
)

func installCommand(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	serviceUser := fs.String("user", "", "Unix user for the mcpd service (default: SUDO_USER/current user)")
	configPath := fs.String("config", "", "existing config to install")
	root := fs.String("root", "/", "alternate installation root for staging/testing")
	forceConfig := fs.Bool("force-config", false, "replace an existing installed config")
	noEnable := fs.Bool("no-enable", false, "do not enable the systemd unit")
	noStart := fs.Bool("no-start", false, "do not start/restart mcpd after install")
	if err := fs.Parse(args); err != nil {
		return err
	}
	result, err := service.Install(service.InstallOptions{
		Root: *root, SourceConfig: *configPath, ServiceUser: *serviceUser,
		ForceConfig: *forceConfig, Enable: !*noEnable, Start: !*noStart,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Installed mcpd to %s\n", result.Paths.Binary)
	fmt.Printf("Config: %s\n", result.Paths.Config)
	fmt.Printf("Service user: %s\n", result.Account.User)
	if result.ConfigPreserved {
		fmt.Println("Existing config preserved.")
	}
	if result.NeedsPassword {
		fmt.Println("OAuth is enabled, but the owner password is not configured; service start was deferred.")
		fmt.Printf("Run: sudo mcpd auth set-password --config %s\n", service.ConfigPath)
		fmt.Println("Then: sudo mcpd start")
	}
	return nil
}

func startCommand(args []string) error {
	if len(args) != 0 {
		return errors.New("start does not accept arguments")
	}
	return service.Start(os.Stdout, os.Stderr)
}

func stopCommand(args []string) error {
	if len(args) != 0 {
		return errors.New("stop does not accept arguments")
	}
	return service.Stop(os.Stdout, os.Stderr)
}

func restartCommand(args []string) error {
	if len(args) != 0 {
		return errors.New("restart does not accept arguments")
	}
	return service.Restart(os.Stdout, os.Stderr)
}

func updateCommand(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	checkOnly := fs.Bool("check", false, "check whether a newer release is available without installing it")
	force := fs.Bool("force", false, "reinstall the latest release even when it is not newer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("update does not accept positional arguments")
	}
	return service.Update(os.Stdout, os.Stderr, service.UpdateOptions{
		CurrentVersion: version.Current().Version,
		CheckOnly:      *checkOnly,
		Force:          *force,
	})
}

func statusCommand(args []string) error {
	if len(args) != 0 {
		return errors.New("status does not accept arguments")
	}
	return service.Status(os.Stdout, os.Stderr)
}

func logsCommand(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("follow", false, "follow new log entries")
	lines := fs.Int("lines", 100, "number of recent log lines")
	since := fs.String("since", "", "journalctl-compatible start time")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return service.Logs(os.Stdout, os.Stderr, service.LogOptions{Follow: *follow, Lines: *lines, Since: *since})
}

func doctorCommand(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	configPath := fs.String("config", "", "config path to validate (default: /etc/mcpd/config.toml)")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	report := service.Doctor(*configPath)
	if *jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		for _, check := range report.Checks {
			fmt.Printf("%-8s %-18s %s\n", check.Status, check.Name, check.Message)
		}
	}
	if !report.Healthy {
		return errors.New("doctor found configuration or installation errors")
	}
	return nil
}
