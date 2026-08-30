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
	maxInteractionOperationKeyBytes = 512
	maxInteractionReplayRecords     = 256
)

var ErrInteractionOperationConflict = errors.New("interaction operation key conflicts with different input")

type interactionReplayRecord struct {
	Fingerprint string
	Result      InteractResult
}

func validateInteractionOperationKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) > maxInteractionOperationKeyBytes || strings.ContainsRune(key, '\x00') {
		return errors.New("operation_key must be <= 512 bytes and contain no NUL")
	}
	return nil
}

func interactionKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func interactionFingerprint(req InteractRequest) (string, error) {
	payload := struct {
		Input    string `json:"input"`
		RawInput bool   `json:"raw_input"`
	}{req.Input, req.RawInput}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode interaction fingerprint: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func cloneInteractResult(in InteractResult) InteractResult {
	out := in
	out.Lines = append([]string(nil), in.Lines...)
	out.ExitCode = cloneInt(in.ExitCode)
	return out
}
