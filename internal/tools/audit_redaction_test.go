package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditMetadataDoesNotContainSensitivePayloads(t *testing.T) {
	secret := "mcpd-super-secret-value"
	cases := []struct {
		name string
		in   any
	}{
		{"command", StartProcessInput{Command: "echo " + secret, CWD: "/srv/app", TimeoutMS: 1}},
		{"stdin", InteractWithProcessInput{PID: 12, Input: secret, TimeoutMS: 1}},
		{"file body", WriteFileInput{Path: "/tmp/x", Content: secret, Mode: "rewrite"}},
		{"edit bodies", EditBlockInput{FilePath: "/tmp/x", OldString: secret, NewString: strPtr("replacement")}},
		{"search", StartSearchInput{Path: "/srv/app", Pattern: secret, SearchType: "content"}},
		{"url secret", ReadFileInput{Path: "https://user:" + secret + "@example.test/x?token=" + secret, IsURL: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := auditMetadata(tc.in)
			encoded, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("secret leaked in durable audit metadata: %s", encoded)
			}
			if len(encoded) > 16<<10 {
				t.Fatalf("audit metadata unexpectedly large: %d bytes", len(encoded))
			}
		})
	}
}

func strPtr(v string) *string { return &v }
