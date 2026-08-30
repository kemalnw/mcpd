package durableexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const supervisorProtocolVersion = 1

type supervisorRequest struct {
	Version int          `json:"version"`
	Op      string       `json:"op"`
	Start   StartRequest `json:"start,omitempty"`
}

type supervisorResponse struct {
	Version          int    `json:"version"`
	Job              Job    `json:"job,omitempty"`
	IdempotentReplay bool   `json:"idempotent_replay,omitempty"`
	Error            string `json:"error,omitempty"`
}

type supervisorServer struct {
	root       string
	executable string
	startMu    sync.Mutex
}

func SupervisorSocket(root string) string { return filepath.Join(root, "supervisor.sock") }

// ServeSupervisor owns the systemd lifecycle/cgroup used by durable runners.
// The MCP HTTP daemon only submits start requests over this private Unix socket,
// so restarting mcpd.service cannot kill already-running durable jobs.
func ServeSupervisor(ctx context.Context, root, socketPath, executable string) error {
	if root == "" || socketPath == "" || executable == "" {
		return errors.New("supervisor root, socket, and executable are required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create durable root: %w", err)
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale supervisor socket: %w", err)
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen durable supervisor: %w", err)
	}
	defer func() { _ = ln.Close(); _ = os.Remove(socketPath) }()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("protect supervisor socket: %w", err)
	}
	server := &supervisorServer{root: root, executable: executable}
	go func() { <-ctx.Done(); _ = ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept durable supervisor connection: %w", err)
		}
		go server.handleConn(conn)
	}
}

func (s *supervisorServer) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	dec := json.NewDecoder(io.LimitReader(bufio.NewReader(conn), 1<<20))
	dec.DisallowUnknownFields()
	var req supervisorRequest
	if err := dec.Decode(&req); err != nil {
		writeSupervisorResponse(conn, supervisorResponse{Version: supervisorProtocolVersion, Error: "invalid request"})
		return
	}
	if req.Version != supervisorProtocolVersion {
		writeSupervisorResponse(conn, supervisorResponse{Version: supervisorProtocolVersion, Error: "unsupported supervisor protocol"})
		return
	}
	if req.Op != "start" {
		writeSupervisorResponse(conn, supervisorResponse{Version: supervisorProtocolVersion, Error: "unsupported operation"})
		return
	}
	job, replay, err := s.start(req.Start)
	if err != nil {
		writeSupervisorResponse(conn, supervisorResponse{Version: supervisorProtocolVersion, Error: err.Error()})
		return
	}
	writeSupervisorResponse(conn, supervisorResponse{Version: supervisorProtocolVersion, Job: job, IdempotentReplay: replay})
}

func (s *supervisorServer) start(req StartRequest) (Job, bool, error) {
	if strings.TrimSpace(req.Command) == "" {
		return Job{}, false, errors.New("command is required")
	}
	if req.Shell == "" {
		req.Shell = "/bin/bash"
	}
	digest, err := startKeyDigest(req.IdempotencyKey)
	if err != nil {
		return Job{}, false, err
	}
	if digest == "" {
		job, err := launchRunner(s.root, s.executable, req, "")
		return job, false, err
	}
	fingerprint, err := startRequestFingerprint(req)
	if err != nil {
		return Job{}, false, err
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	path := idempotencyPath(s.root, digest)
	if record, err := readStartIdempotency(path); err == nil {
		if record.Fingerprint != fingerprint {
			return Job{}, false, errors.New("idempotency_key was already used for a different durable execution request")
		}
		if job, err := readJob(filepath.Join(s.root, "jobs", record.JobID+".json")); err == nil {
			return job, true, nil
		}
		job, err := launchRunner(s.root, s.executable, req, record.JobID)
		return job, true, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return Job{}, false, err
	}
	jobID, err := newJobID()
	if err != nil {
		return Job{}, false, err
	}
	record := startIdempotencyRecord{SchemaVersion: SchemaVersion, Fingerprint: fingerprint, JobID: jobID, CreatedAt: time.Now().UTC()}
	if err := writeStartIdempotency(path, record); err != nil {
		return Job{}, false, err
	}
	job, err := launchRunner(s.root, s.executable, req, jobID)
	if err != nil {
		return Job{}, false, err
	}
	return job, false, nil
}

func writeSupervisorResponse(w io.Writer, resp supervisorResponse) {
	_ = json.NewEncoder(w).Encode(resp)
}

func launchRunner(root, executable string, req StartRequest, id string) (Job, error) {
	if req.Command == "" {
		return Job{}, errors.New("command is required")
	}
	if req.Shell == "" {
		req.Shell = "/bin/bash"
	}
	if req.CWD != "" {
		info, err := os.Stat(req.CWD)
		if err != nil || !info.IsDir() {
			return Job{}, fmt.Errorf("invalid cwd %q", req.CWD)
		}
	}
	if id == "" {
		var err error
		id, err = newJobID()
		if err != nil {
			return Job{}, err
		}
	} else if !validJobID(id) {
		return Job{}, errors.New("invalid durable job id")
	}
	jobsDir := filepath.Join(root, "jobs")
	logsDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(jobsDir, 0o700); err != nil {
		return Job{}, err
	}
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		return Job{}, err
	}
	statePath := filepath.Join(jobsDir, id+".json")
	logPath := filepath.Join(logsDir, id+".log")
	spec := runnerSpec{JobID: id, Command: req.Command, CWD: req.CWD, Shell: req.Shell, LogPath: logPath}
	data, err := json.Marshal(spec)
	if err != nil {
		return Job{}, err
	}
	cmd := exec.Command(executable, "__durable_runner", "--state", statePath)
	cmd.Stdin = bytesReader(data)
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return Job{}, err
	}
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = devnull.Close()
		return Job{}, fmt.Errorf("start durable runner: %w", err)
	}
	go func() { _ = cmd.Wait(); _ = devnull.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	job, err := waitForState(ctx, statePath, func(job Job) bool { return job.State == StateRunning || terminal(job.State) })
	if err != nil {
		return Job{}, fmt.Errorf("wait durable runner state: %w", err)
	}
	return job, nil
}

type byteReader struct {
	data []byte
	off  int
}

func bytesReader(data []byte) *byteReader { return &byteReader{data: data} }
func (r *byteReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
