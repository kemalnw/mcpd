package version

import "runtime/debug"

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

func Current() Info {
	info := Info{Version: Version, Commit: Commit, Date: Date}
	if Version != "dev" {
		return info
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "(devel)" && bi.Main.Version != "" {
		info.Version = bi.Main.Version
	}
	return info
}
