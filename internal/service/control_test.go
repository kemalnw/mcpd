package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLifecycleCommandsUseSystemdAndJournal(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "commands.log")
	script := "#!/bin/sh\nprintf '%s %s\\n' \"$(basename \"$0\")\" \"$*\" >> \"$MCPD_TEST_COMMAND_LOG\"\n"
	for _, name := range []string{"systemctl", "journalctl"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("MCPD_TEST_COMMAND_LOG", logPath)

	oldEUID := effectiveUID
	oldSocket := socketUnitPresent
	effectiveUID = func() int { return 0 }
	socketUnitPresent = func() bool { return true }
	t.Cleanup(func() {
		effectiveUID = oldEUID
		socketUnitPresent = oldSocket
	})

	var output bytes.Buffer
	if err := Start(&output, &output); err != nil {
		t.Fatal(err)
	}
	if err := Restart(&output, &output); err != nil {
		t.Fatal(err)
	}
	if err := Status(&output, &output); err != nil {
		t.Fatal(err)
	}
	if err := Logs(&output, &output, LogOptions{Lines: 7, Since: "1 hour ago"}); err != nil {
		t.Fatal(err)
	}
	if err := Stop(&output, &output); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"systemctl start mcpd.socket",
		"systemctl start mcpd.service",
		"systemctl restart mcpd.service",
		"systemctl status --no-pager mcpd.service mcpd.socket",
		"journalctl -u mcpd.service -n 7 --since 1 hour ago --no-pager",
		"systemctl stop mcpd.service",
		"systemctl stop mcpd.socket",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("command log missing %q:\n%s", want, got)
		}
	}
}
