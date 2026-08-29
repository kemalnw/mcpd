package service

import "path/filepath"

const (
	BinaryPath  = "/usr/local/bin/mcpd"
	ConfigPath  = "/etc/mcpd/config.toml"
	StatePath   = "/var/lib/mcpd"
	ServiceUnit = "/etc/systemd/system/mcpd.service"
	ServiceName = "mcpd.service"
)

type Paths struct {
	Root        string
	Binary      string
	Config      string
	State       string
	ServiceUnit string
}

func PathsForRoot(root string) Paths {
	if root == "" {
		root = "/"
	}
	join := func(path string) string {
		if root == "/" {
			return path
		}
		return filepath.Join(root, path)
	}
	return Paths{
		Root: root, Binary: join(BinaryPath), Config: join(ConfigPath), State: join(StatePath),
		ServiceUnit: join(ServiceUnit),
	}
}
