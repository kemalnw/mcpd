//go:build linux

package activation

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const firstListenFD = 3

type Set struct {
	files []*os.File
	names []string
}

func FromEnvironment() (*Set, error) {
	rawCount := os.Getenv("LISTEN_FDS")
	if rawCount == "" {
		return &Set{}, nil
	}
	count, err := strconv.Atoi(rawCount)
	if err != nil || count < 0 {
		return nil, fmt.Errorf("invalid LISTEN_FDS %q", rawCount)
	}
	rawPID := os.Getenv("LISTEN_PID")
	pid, err := strconv.Atoi(rawPID)
	if err != nil || pid != os.Getpid() {
		return nil, fmt.Errorf("LISTEN_PID %q does not match process %d", rawPID, os.Getpid())
	}
	names := strings.Split(os.Getenv("LISTEN_FDNAMES"), ":")
	if len(names) == 1 && names[0] == "" {
		names = nil
	}
	set := &Set{}
	for i := 0; i < count; i++ {
		fd := firstListenFD + i
		syscall.CloseOnExec(fd)
		name := fmt.Sprintf("systemd-listener-%d", i)
		if i < len(names) && names[i] != "" {
			name = names[i]
		}
		set.files = append(set.files, os.NewFile(uintptr(fd), name))
		set.names = append(set.names, name)
	}
	for _, key := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		_ = os.Unsetenv(key)
	}
	return set, nil
}

func (s *Set) Count() int {
	if s == nil {
		return 0
	}
	return len(s.files)
}

func (s *Set) ListenerFor(address string) (net.Listener, bool, error) {
	if s == nil || len(s.files) == 0 {
		return nil, false, nil
	}
	port, err := addressPort(address)
	if err != nil {
		return nil, false, err
	}
	var match net.Listener
	for _, file := range s.files {
		dupFD, err := syscall.Dup(int(file.Fd()))
		if err != nil {
			return nil, false, fmt.Errorf("duplicate activated socket: %w", err)
		}
		dupFile := os.NewFile(uintptr(dupFD), file.Name()+"-dup")
		listener, err := net.FileListener(dupFile)
		_ = dupFile.Close()
		if err != nil {
			return nil, false, fmt.Errorf("open activated socket %q: %w", file.Name(), err)
		}
		listenerPort, ok := tcpPort(listener.Addr())
		if !ok || listenerPort != port {
			_ = listener.Close()
			continue
		}
		if match != nil {
			_ = listener.Close()
			_ = match.Close()
			return nil, false, fmt.Errorf("multiple activated listeners match port %d", port)
		}
		match = listener
	}
	if match == nil {
		return nil, false, nil
	}
	return match, true, nil
}

func (s *Set) ListenerFactory(address string) func() (net.Listener, error) {
	return func() (net.Listener, error) {
		listener, ok, err := s.ListenerFor(address)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("no systemd-activated listener matches %s", address)
		}
		return listener, nil
	}
}

func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	var joined error
	for _, file := range s.files {
		if file != nil {
			joined = errors.Join(joined, file.Close())
		}
	}
	s.files = nil
	return joined
}

func addressPort(address string) (int, error) {
	_, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return 0, fmt.Errorf("parse listen address %q: %w", address, err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid listen port in %q", address)
	}
	return port, nil
}

func tcpPort(addr net.Addr) (int, bool) {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return 0, false
	}
	return tcp.Port, true
}
