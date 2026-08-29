package service

import (
	"fmt"
	"strings"
)

type Account struct {
	User  string
	Group string
	Home  string
	UID   int
	GID   int
}

func RenderService(account Account) string {
	return fmt.Sprintf(`[Unit]
Description=mcpd self-hosted MCP daemon
Documentation=https://github.com/kemalnw/mcpd
After=network-online.target
Wants=network-online.target

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
`, account.User, account.Group, escapeUnitPath(account.Home), escapeUnitValue(account.Home), BinaryPath, ConfigPath)
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
