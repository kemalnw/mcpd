package tlsmgr

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
)

func TestActivatedHTTP01ProviderServesChallenge(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	used := false
	provider := newActivatedHTTP01Provider(func() (net.Listener, error) {
		if used {
			t.Fatal("listener factory called more than once")
		}
		used = true
		return listener, nil
	})
	if err := provider.Present(context.Background(), "127.0.0.1", "token", "key-auth"); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/.well-known/acme-challenge/token", nil)
	req.Host = "127.0.0.1"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "key-auth" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	if err := provider.CleanUp(context.Background(), "127.0.0.1", "token", "key-auth"); err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cleanup status=%d", resp.StatusCode)
	}
	if err := provider.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
