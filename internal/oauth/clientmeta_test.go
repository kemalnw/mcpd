package oauth

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestPublicIPRejectsSpecialUseNetworks(t *testing.T) {
	rejected := []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.0.1",
		"169.254.1.1", "100.64.0.1", "192.0.2.1", "198.18.0.1",
		"198.51.100.1", "203.0.113.1", "240.0.0.1", "::1", "fc00::1",
		"fe80::1", "100::1", "2001:2::1", "2001:db8::1",
	}
	for _, raw := range rejected {
		if publicIP(net.ParseIP(raw)) {
			t.Errorf("publicIP(%q) = true, want false", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(net.ParseIP(raw)) {
			t.Errorf("publicIP(%q) = false, want true", raw)
		}
	}
}

func TestSafeClientRejectsLoopbackMetadataTarget(t *testing.T) {
	client := newSafeHTTPClient(500 * time.Millisecond)
	_, err := fetchClientMetadata(context.Background(), client, "https://127.0.0.1/client.json")
	if err == nil || !strings.Contains(err.Error(), "did not resolve to a public IP") {
		t.Fatalf("loopback metadata target was not rejected: %v", err)
	}
}
