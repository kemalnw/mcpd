package process

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	maxProcessIdempotencyKeyBytes = 512
	maxProcessIdempotencyRecords  = 10_000
)

var ErrProcessIdempotencyConflict = errors.New("process idempotency key conflicts with a different request")

type startIdempotencyRecord struct {
	Fingerprint string
	PID         int
	Result      StartResult
	Err         error
	Done        chan struct{}
}

func validateProcessIdempotencyKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) > maxProcessIdempotencyKeyBytes || strings.ContainsRune(key, '\x00') {
		return errors.New("idempotency_key must be <= 512 bytes and contain no NUL")
	}
	return nil
}

func processIdempotencyKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func startRequestFingerprint(req StartRequest) (string, error) {
	payload := struct {
		Command         string  `json:"command"`
		CWD             string  `json:"cwd"`
		Shell           string  `json:"shell"`
		PTY             PTYMode `json:"pty"`
		SeparateStreams bool    `json:"separate_streams"`
	}{req.Command, req.CWD, req.Shell, req.PTY, req.SeparateStreams}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode process idempotency fingerprint: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func cloneStartResult(in StartResult) StartResult {
	out := in
	out.Output = append([]string(nil), in.Output...)
	out.Streams = append([]StreamLine(nil), in.Streams...)
	out.ExitCode = cloneInt(in.ExitCode)
	return out
}
