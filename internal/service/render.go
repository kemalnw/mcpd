package service

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/kemalnw/mcpd/internal/config"
)

type Account struct {
	User  string
	Group string
	Home  string
	UID   int
	GID   int
}

func RenderService(account Account, socketActivation bool) string {
	deps := "After=network-online.target\nWants=network-online.target"
	if socketActivation {
		deps += "\nRequires=mcpd.socket\nAfter=mcpd.socket"
	}
	return fmt.Sprintf(`[Unit]
Description=mcpd self-hosted MCP daemon
Documentation=https://github.com/kemalnw/mcpd
%s

[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=%s
Environment="HOME=%s"
ExecStart=%s serve --config %s
Restart=on-failure
RestartSec=2s
TimeoutStopSec=20s
KillSignal=SIGTERM
StateDirectory=mcpd
StateDirectoryMode=0700
UMask=0077
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`, deps, account.User, account.Group, escapeUnitPath(account.Home), escapeUnitValue(account.Home), BinaryPath, ConfigPath)
}
func RenderSocket(addresses []string) string {
	var listens strings.Builder
	for _, address := range addresses {
		fmt.Fprintf(&listens, "ListenStream=%s\n", address)
	}
	return fmt.Sprintf(`[Unit]
Description=mcpd privileged listeners
Documentation=https://github.com/kemalnw/mcpd

[Socket]
%sNoDelay=true
Service=mcpd.service

[Install]
WantedBy=sockets.target
`, listens.String())
}

func PrivilegedListeners(cfg config.Config, account Account) ([]string, error) {
	if account.UID == 0 {
		return nil, nil
	}
	candidates := []string{cfg.Server.Listen}
	if cfg.TLS.Mode == "acme" {
		candidates = append(candidates, cfg.TLS.ChallengeListen)
	}
	seen := map[string]bool{}
	var out []string
	for _, address := range candidates {
		port, err := listenPort(address)
		if err != nil {
			return nil, err
		}
		if port >= 1024 || seen[address] {
			continue
		}
		seen[address] = true
		out = append(out, address)
	}
	return out, nil
}

func listenPort(address string) (int, error) {
	_, raw, err := net.SplitHostPort(address)
	if err != nil {
		return 0, fmt.Errorf("parse listen address %q: %w", address, err)
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid listen port in %q", address)
	}
	return port, nil
}
func escapeUnitPath(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, " ", "\\x20")
	value = strings.ReplaceAll(value, "\t", "\\x09")
	return strings.ReplaceAll(value, "%", "%%")
}

func escapeUnitValue(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return strings.ReplaceAll(value, "%", "%%")
}
