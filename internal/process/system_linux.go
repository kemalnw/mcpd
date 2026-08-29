//go:build linux

package process

import (
	"bufio"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func ListSystemProcesses() ([]SystemProcess, error) {
	cmd := exec.Command("ps", "-eo", "pid=,pcpu=,pmem=,args=")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	processes := make([]SystemProcess, 0, 128)
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		cpu, err2 := strconv.ParseFloat(fields[1], 64)
		memory, err3 := strconv.ParseFloat(fields[2], 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		processes = append(processes, SystemProcess{PID: pid, CPU: cpu, Memory: memory, Command: strings.Join(fields[3:], " ")})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse process list: %w", err)
	}
	return processes, nil
}

func KillSystemProcess(pid int) error {
	if pid <= 0 {
		return errors.New("pid must be positive")
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	return nil
}
