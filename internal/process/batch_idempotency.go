package process

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const maxBatchIdempotencyKeyBytes = 512

var ErrBatchIdempotencyConflict = errors.New("batch idempotency key conflicts with a different request")

type batchIdempotencyRecord struct {
	Fingerprint string
	BatchID     string
}

func validateBatchIdempotencyKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) > maxBatchIdempotencyKeyBytes || strings.ContainsRune(key, '\x00') {
		return errors.New("idempotency_key must be <= 512 bytes and contain no NUL")
	}
	return nil
}

func batchIdempotencyKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func batchRequestFingerprint(jobs []BatchJobRequest, effectiveMaxParallel int) (string, error) {
	payload := struct {
		Jobs        []BatchJobRequest `json:"jobs"`
		MaxParallel int               `json:"max_parallel"`
	}{Jobs: jobs, MaxParallel: effectiveMaxParallel}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode batch idempotency fingerprint: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// readBatchReplaySnapshot returns a fresh caller-owned observation cursor. A
// retry after a lost start response returns the same logical batch but never
// consumes another agent's progress because batch cursors are stateless/caller-owned.
func (m *Manager) readBatchReplaySnapshot(b *processBatch, length int) BatchResult {
	result := m.readBatchSnapshot(b, false, length, batchCursor{Version: 1, BatchID: b.id, Jobs: make(map[string]batchJobCursor)})
	result.IdempotentReplay = true
	return result
}
