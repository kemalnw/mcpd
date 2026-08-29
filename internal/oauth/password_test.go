package oauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetPasswordMinimumLength(t *testing.T) {
	t.Run("rejects seven characters", func(t *testing.T) {
		dir := t.TempDir()
		err := SetPassword(dir, []byte("1234567"))
		if err == nil {
			t.Fatal("SetPassword() accepted a 7-character password")
		}
		if got, want := err.Error(), "owner password must be at least 8 characters"; got != want {
			t.Fatalf("SetPassword() error = %q, want %q", got, want)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "owner.password")); !os.IsNotExist(statErr) {
			t.Fatalf("owner.password should not be created for an invalid password, stat err = %v", statErr)
		}
	})

	for _, tc := range []struct {
		name     string
		password string
	}{
		{name: "accepts exactly eight characters", password: "12345678"},
		{name: "accepts longer passwords", password: "correct horse battery staple"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			password := []byte(tc.password)
			if err := SetPassword(dir, password); err != nil {
				t.Fatalf("SetPassword() error = %v", err)
			}
			encoded, err := loadPasswordHash(dir)
			if err != nil {
				t.Fatalf("loadPasswordHash() error = %v", err)
			}
			if !verifyPassword(encoded, password) {
				t.Fatal("verifyPassword() rejected the stored password")
			}
		})
	}
}

func TestSetPasswordKeepsArgon2idParameters(t *testing.T) {
	dir := t.TempDir()
	if err := SetPassword(dir, []byte("12345678")); err != nil {
		t.Fatal(err)
	}
	encoded, err := loadPasswordHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "$argon2id$v=19$m=65536,t=3,p=1$"
	if !strings.HasPrefix(encoded, prefix) {
		t.Fatalf("password hash = %q, want prefix %q", encoded, prefix)
	}
}

func TestValidateOwnerPasswordUsesEightCharacterMinimum(t *testing.T) {
	if err := ValidateOwnerPassword([]byte("1234567")); err == nil || err.Error() != "owner password must be at least 8 characters" {
		t.Fatalf("7-character validation error = %v", err)
	}
	if err := ValidateOwnerPassword([]byte("12345678")); err != nil {
		t.Fatalf("8-character password rejected: %v", err)
	}
}
