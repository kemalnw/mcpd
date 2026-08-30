//go:build linux

package process

import (
	"bufio"
	"errors"
	"fmt"
	"os"
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
		startTicks, _ := systemProcessStartTicks(pid)
		processes = append(processes, SystemProcess{PID: pid, StartTicks: startTicks, CPU: cpu, Memory: memory, Command: strings.Join(fields[3:], " ")})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse process list: %w", err)
	}
	return processes, nil
}

func KillSystemProcess(pid int, expectedStartTicks uint64) error {
	if pid <= 0 {
		return errors.New("pid must be positive")
	}
	if expectedStartTicks != 0 {
		actual, err := systemProcessStartTicks(pid)
		if err != nil {
			return fmt.Errorf("verify process %d identity: %w", pid, err)
		}
		if actual != expectedStartTicks {
			return fmt.Errorf("process %d start_ticks changed: got %d expected %d", pid, actual, expectedStartTicks)
		}
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	return nil
}

func systemProcessStartTicks(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	text := string(data)
	close := strings.LastIndex(text, ")")
	if close < 0 || close+2 >= len(text) {
		return 0, errors.New("invalid /proc stat")
	}
	fields := strings.Fields(text[close+2:])
	if len(fields) < 20 {
		return 0, errors.New("short /proc stat")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}
