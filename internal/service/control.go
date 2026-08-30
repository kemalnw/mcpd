package service

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
)

var effectiveUID = os.Geteuid

func Start(stdout, stderr io.Writer) error {
	if err := requireRoot("start"); err != nil {
		return err
	}
	if err := runCommand(stdout, stderr, "systemctl", "start", DurableServiceName); err != nil {
		return err
	}
	return runCommand(stdout, stderr, "systemctl", "start", ServiceName)
}

func Stop(stdout, stderr io.Writer) error {
	if err := requireRoot("stop"); err != nil {
		return err
	}
	return runCommand(stdout, stderr, "systemctl", "stop", ServiceName)
}
func Restart(stdout, stderr io.Writer) error {
	if err := requireRoot("restart"); err != nil {
		return err
	}
	if err := runCommand(stdout, stderr, "systemctl", "start", DurableServiceName); err != nil {
		return err
	}
	return runCommand(stdout, stderr, "systemctl", "restart", ServiceName)
}

func Status(stdout, stderr io.Writer) error {
	return runCommand(stdout, stderr, "systemctl", "status", "--no-pager", ServiceName, DurableServiceName)
}

type LogOptions struct {
	Follow bool
	Lines  int
	Since  string
}

func Logs(stdout, stderr io.Writer, opts LogOptions) error {
	if opts.Lines <= 0 {
		opts.Lines = 100
	}
	args := []string{"-u", ServiceName, "-u", DurableServiceName, "-n", strconv.Itoa(opts.Lines)}
	if opts.Since != "" {
		args = append(args, "--since", opts.Since)
	}
	if opts.Follow {
		args = append(args, "--follow")
	} else {
		args = append(args, "--no-pager")
	}
	return runCommand(stdout, stderr, "journalctl", args...)
}

func requireRoot(action string) error {
	if effectiveUID() == 0 {
		return nil
	}
	return errors.New(action + " requires root; run `sudo mcpd " + action + "`")
}

func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
