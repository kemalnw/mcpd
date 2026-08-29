package oauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	MinOwnerPasswordLength = 8
	argonMemory            = 64 * 1024
	argonTime              = 3
	argonThreads           = 1
	argonKeyLen            = 32
)

func ValidateOwnerPassword(password []byte) error {
	if len(password) < MinOwnerPasswordLength {
		return fmt.Errorf("owner password must be at least %d characters", MinOwnerPasswordLength)
	}
	return nil
}

func SetPassword(stateDir string, password []byte) error {
	if err := ValidateOwnerPassword(password); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create auth state directory: %w", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return fmt.Errorf("chmod auth state directory: %w", err)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey(password, salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	encoded := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s\n", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))
	return atomicWrite(filepath.Join(stateDir, "owner.password"), []byte(encoded), 0o600)
}

func loadPasswordHash(stateDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "owner.password"))
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("owner password is not configured; run `mcpd auth set-password`")
	}
	if err != nil {
		return "", fmt.Errorf("read owner password hash: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func verifyPassword(encoded string, password []byte) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory uint64
	var iterations uint64
	var threads uint64
	for _, field := range strings.Split(parts[3], ",") {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) != 2 {
			return false
		}
		v, err := strconv.ParseUint(kv[1], 10, 32)
		if err != nil {
			return false
		}
		switch kv[0] {
		case "m":
			memory = v
		case "t":
			iterations = v
		case "p":
			threads = v
		}
	}
	if memory == 0 || iterations == 0 || threads == 0 || memory > 256*1024 || iterations > 10 || threads > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 {
		return false
	}
	got := argon2.IDKey(password, salt, uint32(iterations), uint32(memory), uint8(threads), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcpd-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
