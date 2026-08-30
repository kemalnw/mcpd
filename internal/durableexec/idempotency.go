package durableexec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxIdempotencyKeyBytes = 512

type startIdempotencyRecord struct {
	SchemaVersion int       `json:"schema_version"`
	Fingerprint   string    `json:"fingerprint"`
	JobID         string    `json:"job_id"`
	CreatedAt     time.Time `json:"created_at"`
}

func startKeyDigest(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", nil
	}
	if len(key) > maxIdempotencyKeyBytes {
		return "", fmt.Errorf("idempotency_key exceeds %d bytes", maxIdempotencyKeyBytes)
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:]), nil
}

func startRequestFingerprint(req StartRequest) (string, error) {
	material := struct{ Command, CWD, Shell string }{req.Command, req.CWD, req.Shell}
	data, err := json.Marshal(material)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func idempotencyPath(root, digest string) string {
	return filepath.Join(root, "idempotency", digest+".json")
}

func readStartIdempotency(path string) (startIdempotencyRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return startIdempotencyRecord{}, err
	}
	if len(data) > 16<<10 {
		return startIdempotencyRecord{}, errors.New("durable idempotency record too large")
	}
	var record startIdempotencyRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return startIdempotencyRecord{}, fmt.Errorf("decode durable idempotency record: %w", err)
	}
	if record.SchemaVersion != SchemaVersion || record.Fingerprint == "" || !validJobID(record.JobID) {
		return startIdempotencyRecord{}, errors.New("invalid durable idempotency record")
	}
	return record, nil
}

func writeStartIdempotency(path string, record startIdempotencyRecord) error {
	record.SchemaVersion = SchemaVersion
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".idempotency-*.tmp")
	if err != nil {
		return err
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
		return err
	}
	return syncDir(dir)
}
