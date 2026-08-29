package setup

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBuildDerivesCanonicalRemotePlan(t *testing.T) {
	plan, err := Build(Input{PublicOrigin: "mcp.example.com", ServiceUser: "alice", OAuthEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Config.Auth.ExternalURL != "https://mcp.example.com" {
		t.Fatalf("external URL = %q", plan.Config.Auth.ExternalURL)
	}
	if plan.Config.Server.Listen != "127.0.0.1:31354" || plan.Config.Server.MCPPath != "/mcp" {
		t.Fatalf("unexpected backend defaults: %#v", plan.Config.Server)
	}
	if plan.PublicMCPURL() != "https://mcp.example.com/mcp" {
		t.Fatalf("public MCP URL = %q", plan.PublicMCPURL())
	}
}
func TestBuildRejectsInvalidPublicOrigin(t *testing.T) {
	for _, raw := range []string{"", "http://mcp.example.com", "https://mcp.example.com/path"} {
		if _, err := Build(Input{PublicOrigin: raw, ServiceUser: "alice", OAuthEnabled: true}); err == nil {
			t.Fatalf("Build accepted public origin %q", raw)
		}
	}
}

func TestHealthURLUsesLoopbackForWildcardListener(t *testing.T) {
	plan, err := Build(Input{
		PublicOrigin: "mcp.example.com", ServiceUser: "alice", OAuthEnabled: true,
		Listen: "0.0.0.0:31354",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := plan.HealthURL()
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:31354/healthz" {
		t.Fatalf("health URL = %q", got)
	}
}

type fakeExecutor struct {
	calls  []string
	failAt string
}

func (f *fakeExecutor) step(name string) error {
	f.calls = append(f.calls, name)
	if f.failAt == name {
		return errors.New("boom")
	}
	return nil
}
func (f *fakeExecutor) Preflight(Plan) error                          { return f.step("preflight") }
func (f *fakeExecutor) Install(Plan) error                            { return f.step("install") }
func (f *fakeExecutor) ConfigurePassword([]byte) error                { return f.step("password") }
func (f *fakeExecutor) Restart() error                                { return f.step("restart") }
func (f *fakeExecutor) Doctor() error                                 { return f.step("doctor") }
func (f *fakeExecutor) HealthCheck(context.Context, string) error     { return f.step("health") }
func (f *fakeExecutor) ConfigureFrontend(Plan) error                  { return f.step("frontend") }
func (f *fakeExecutor) PublicHealthCheck(context.Context, Plan) error { return f.step("public-health") }

func TestApplyRunsCompleteSetupSequence(t *testing.T) {
	plan, err := Build(Input{PublicOrigin: "mcp.example.com", ServiceUser: "alice", OAuthEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeExecutor{}
	if err := Apply(context.Background(), plan, ApplyOptions{Password: []byte("12345678")}, fake); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.calls, ","); got != "preflight,install,password,restart,health,frontend,public-health,doctor" {
		t.Fatalf("calls = %q", got)
	}
}
func TestApplyStopsAtFailedStage(t *testing.T) {
	plan, err := Build(Input{PublicOrigin: "mcp.example.com", ServiceUser: "alice", OAuthEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"preflight", "install", "password", "restart", "health", "frontend", "public-health", "doctor"} {
		t.Run(stage, func(t *testing.T) {
			fake := &fakeExecutor{failAt: stage}
			err := Apply(context.Background(), plan, ApplyOptions{Password: []byte("12345678")}, fake)
			if err == nil || !strings.Contains(err.Error(), "boom") {
				t.Fatalf("Apply error = %v", err)
			}
			if fake.calls[len(fake.calls)-1] != stage {
				t.Fatalf("last call = %q, want %q", fake.calls[len(fake.calls)-1], stage)
			}
		})
	}
}

func TestApplyDoesNotReplacePasswordWhenNoneProvided(t *testing.T) {
	plan, _ := Build(Input{PublicOrigin: "mcp.example.com", ServiceUser: "alice", OAuthEnabled: true})
	fake := &fakeExecutor{}
	if err := Apply(context.Background(), plan, ApplyOptions{}, fake); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(fake.calls, ","), "password") {
		t.Fatalf("password unexpectedly changed: %v", fake.calls)
	}
}

func TestBuildDefaultsToManagedCaddy(t *testing.T) {
	plan, err := Build(Input{PublicOrigin: "mcp.example.com", ServiceUser: "alice", OAuthEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ManagedCaddy() || plan.HTTPSMode != HTTPSModeCaddy {
		t.Fatalf("HTTPS mode = %q", plan.HTTPSMode)
	}
	host, err := plan.PublicHost()
	if err != nil || host != "mcp.example.com" {
		t.Fatalf("public host = %q, err=%v", host, err)
	}
	backend, err := plan.CaddyBackend()
	if err != nil || backend != "127.0.0.1:31354" {
		t.Fatalf("Caddy backend = %q, err=%v", backend, err)
	}
}

func TestBuildSupportsExternalHTTPSFrontend(t *testing.T) {
	plan, err := Build(Input{PublicOrigin: "mcp.example.com", ServiceUser: "alice", OAuthEnabled: true, HTTPSMode: HTTPSModeExternal})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ManagedCaddy() || plan.HTTPSMode != HTTPSModeExternal {
		t.Fatalf("HTTPS mode = %q", plan.HTTPSMode)
	}
}
