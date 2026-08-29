package service

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
)

var effectiveUID = os.Geteuid
var socketUnitPresent = func() bool { return fileExists(SocketUnit) }

func Start(stdout, stderr io.Writer) error {
	if err := requireRoot("start"); err != nil {
		return err
	}
	if socketUnitPresent() {
		if err := runCommand(stdout, stderr, "systemctl", "start", SocketName); err != nil {
			return err
		}
	}
	return runCommand(stdout, stderr, "systemctl", "start", ServiceName)
}

func Stop(stdout, stderr io.Writer) error {
	if err := requireRoot("stop"); err != nil {
		return err
	}
	serviceErr := runCommand(stdout, stderr, "systemctl", "stop", ServiceName)
	if socketUnitPresent() {
		socketErr := runCommand(stdout, stderr, "systemctl", "stop", SocketName)
		return errors.Join(serviceErr, socketErr)
	}
	return serviceErr
}
func Restart(stdout, stderr io.Writer) error {
	if err := requireRoot("restart"); err != nil {
		return err
	}
	if socketUnitPresent() {
		if err := runCommand(stdout, stderr, "systemctl", "start", SocketName); err != nil {
			return err
		}
	}
	return runCommand(stdout, stderr, "systemctl", "restart", ServiceName)
}

func Status(stdout, stderr io.Writer) error {
	args := []string{"status", "--no-pager", ServiceName}
	if socketUnitPresent() {
		args = append(args, SocketName)
	}
	return runCommand(stdout, stderr, "systemctl", args...)
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
	args := []string{"-u", ServiceName, "-n", strconv.Itoa(opts.Lines)}
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
