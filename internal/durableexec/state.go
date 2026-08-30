package durableexec

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func readJob(path string) (Job, error) {
	f, err := os.Open(path)
	if err != nil {
		return Job{}, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 256<<10))
	dec.DisallowUnknownFields()
	var job Job
	if err := dec.Decode(&job); err != nil {
		return Job{}, fmt.Errorf("decode durable job: %w", err)
	}
	if job.SchemaVersion != SchemaVersion || job.ID == "" || job.RunnerPID <= 0 || job.BootID == "" || job.RunnerStartTicks == 0 || job.LogPath == "" {
		return Job{}, errors.New("invalid durable job state")
	}
	return job, nil
}

func writeJob(path string, job Job) error {
	job.SchemaVersion = SchemaVersion
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("encode durable job: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create durable job directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".job-*.tmp")
	if err != nil {
		return fmt.Errorf("create durable job temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace durable job state: %w", err)
	}
	return syncDir(dir)
}

func bootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("read boot id: %w", err)
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", errors.New("empty boot id")
	}
	return id, nil
}

func processStartTicks(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// comm is parenthesized and may contain spaces. Field 22 (starttime) is the
	// 20th token after the closing parenthesis because pid+comm occupy fields 1-2.
	text := string(data)
	close := strings.LastIndex(text, ")")
	if close < 0 || close+2 >= len(text) {
		return 0, errors.New("invalid /proc stat")
	}
	fields := strings.Fields(text[close+2:])
	if len(fields) < 20 {
		return 0, errors.New("short /proc stat")
	}
	value, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse process starttime: %w", err)
	}
	return value, nil
}

func processIdentityMatches(pid int, ticks uint64) bool {
	if pid <= 0 || ticks == 0 {
		return false
	}
	got, err := processStartTicks(pid)
	return err == nil && got == ticks
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
