package tools

import (
	"strings"
	"testing"
)

func TestCatalogFingerprintIsStableAndVersioned(t *testing.T) {
	one := CatalogFingerprint()
	two := CatalogFingerprint()
	if one != two {
		t.Fatalf("fingerprint changed between calls: %q != %q", one, two)
	}
	if !strings.HasPrefix(one, "sha256:") || len(one) != len("sha256:")+64 {
		t.Fatalf("unexpected fingerprint format: %q", one)
	}
	if CatalogVersion <= 0 {
		t.Fatalf("catalog version must be positive: %d", CatalogVersion)
	}
}
