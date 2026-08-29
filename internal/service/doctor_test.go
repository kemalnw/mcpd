package service

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBackendDoctorCheckReportsHealthyLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	listen := strings.TrimPrefix(server.URL, "http://")
	checks := map[string]Check{}
	addBackendDoctorCheck(listen, collectDoctorChecks(checks), server.Client())
	if got := checks["backend-health"]; got.Status != "ok" {
		t.Fatalf("backend-health = %#v", got)
	}
}

func TestPublicDoctorChecksVerifyTLSHealthAndOAuth(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/.well-known/oauth-authorization-server":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	checks := map[string]Check{}
	addPublicDoctorChecks(server.URL, collectDoctorChecks(checks), net.DefaultResolver, server.Client())
	for _, name := range []string{"public-dns", "public-https", "public-oauth"} {
		if got := checks[name]; got.Status != "ok" {
			t.Fatalf("%s = %#v", name, got)
		}
	}
}

func TestPublicDoctorChecksFailWhenHTTPSListenerIsUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	checks := map[string]Check{}
	client := &http.Client{Timeout: 200 * time.Millisecond}
	addPublicDoctorChecks("https://"+address, collectDoctorChecks(checks), net.DefaultResolver, client)
	if got := checks["public-dns"]; got.Status != "ok" {
		t.Fatalf("public-dns = %#v", got)
	}
	if got := checks["public-https"]; got.Status != "error" || !strings.Contains(got.Message, "healthz") {
		t.Fatalf("public-https = %#v", got)
	}
	if got := checks["public-oauth"]; got.Status != "error" {
		t.Fatalf("public-oauth = %#v", got)
	}
}

func collectDoctorChecks(dst map[string]Check) func(string, string, string) {
	return func(name, status, message string) {
		dst[name] = Check{Name: name, Status: status, Message: message}
	}
}
