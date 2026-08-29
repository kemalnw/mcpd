package oauth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestInjectToolSecuritySchemes(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_file"},{"name":"write_file"}]}}`)
	})
	handler := InjectToolSecuritySchemes(next, func(name string) string {
		if strings.HasPrefix(name, "read") {
			return ScopeRead
		}
		return ScopeWrite
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Mcp-Method", "tools/list")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var envelope struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Result.Tools) != 2 {
		t.Fatalf("tools = %#v", envelope.Result.Tools)
	}
	readSchemes := envelope.Result.Tools[0]["securitySchemes"].([]any)
	readScheme := readSchemes[0].(map[string]any)
	if readScheme["type"] != "oauth2" || readScheme["scopes"].([]any)[0] != ScopeRead {
		t.Fatalf("read securitySchemes = %#v", readSchemes)
	}
	writeSchemes := envelope.Result.Tools[1]["securitySchemes"].([]any)
	if writeSchemes[0].(map[string]any)["scopes"].([]any)[0] != ScopeWrite {
		t.Fatalf("write securitySchemes = %#v", writeSchemes)
	}
}

func TestToolAuthChallengeAndForgedHeaderCannotBypassScope(t *testing.T) {
	oauthServer := testOAuthServer(t)
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "oauth-test", Version: "dev"}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}})
	type input struct {
		Value string `json:"value"`
	}
	type output struct {
		Value string `json:"value"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "read_thing"}, func(_ context.Context, _ *mcp.CallToolRequest, in input) (*mcp.CallToolResult, output, error) {
		return nil, output{Value: "read:" + in.Value}, nil
	})
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "write_thing"}, func(_ context.Context, _ *mcp.CallToolRequest, in input) (*mcp.CallToolResult, output, error) {
		return nil, output{Value: "write:" + in.Value}, nil
	})
	resolve := func(name string) string {
		if name == "read_thing" {
			return ScopeRead
		}
		return ScopeWrite
	}
	mcpServer.AddReceivingMiddleware(EnforceToolScopes(oauthServer, resolve))
	base := http.Handler(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, PropagateRequestCancellation: true,
	}))
	handler := ProtectMCP(InjectToolSecuritySchemes(base, resolve), oauthServer)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	// Discovery/listing remain usable, but invoking a protected tool produces the
	// ChatGPT-specific MCP challenge metadata instead of executing it.
	anonymous := connectTestClient(t, httpServer.URL, httpServer.Client())
	result, err := anonymous.CallTool(context.Background(), &mcp.CallToolParams{Name: "write_thing", Arguments: map[string]any{"value": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("unauthenticated write unexpectedly succeeded: %#v", result)
	}
	challenge, ok := result.Meta["mcp/www_authenticate"].([]any)
	if !ok {
		// The SDK may preserve []string when the result originates in-process.
		if stringsChallenge, ok2 := result.Meta["mcp/www_authenticate"].([]string); ok2 && len(stringsChallenge) == 1 && strings.Contains(stringsChallenge[0], "invalid_token") {
			challenge = []any{stringsChallenge[0]}
		} else {
			t.Fatalf("missing mcp/www_authenticate: %#v", result.Meta)
		}
	}
	if len(challenge) != 1 || !strings.Contains(challenge[0].(string), `scope="mcp:write"`) || !strings.Contains(challenge[0].(string), `error="invalid_token"`) {
		t.Fatalf("unexpected challenge: %#v", challenge)
	}
	_ = anonymous.Close()

	readToken := mintToken(t, oauthServer, ScopeRead)
	client := httpServer.Client()
	client.Transport = authRoundTripper{base: client.Transport, token: readToken}
	authed := connectTestClient(t, httpServer.URL, client)
	defer authed.Close()

	readResult, err := authed.CallTool(context.Background(), &mcp.CallToolParams{Name: "read_thing", Arguments: map[string]any{"value": "ok"}})
	if err != nil || readResult.IsError {
		t.Fatalf("read failed: result=%#v err=%v", readResult, err)
	}

	writeResult, err := authed.CallTool(context.Background(), &mcp.CallToolParams{Name: "write_thing", Arguments: map[string]any{"value": "must-not-run"}})
	if err != nil {
		t.Fatal(err)
	}
	if !writeResult.IsError {
		t.Fatalf("read-only token executed write tool: %#v", writeResult)
	}
	meta := writeResult.Meta["mcp/www_authenticate"]
	if !strings.Contains(toString(meta), "insufficient_scope") || !strings.Contains(toString(meta), ScopeWrite) {
		t.Fatalf("unexpected insufficient-scope metadata: %#v", writeResult.Meta)
	}

	// The official 2026-07-28 transport also rejects a forged Mcp-Name that
	// disagrees with the JSON-RPC body before the tool can execute.
	forgedClient := httpServer.Client()
	forgedClient.Transport = authRoundTripper{base: forgedClient.Transport, token: readToken, forgedName: "read_thing"}
	forged := connectTestClient(t, httpServer.URL, forgedClient)
	defer forged.Close()
	if _, err := forged.CallTool(context.Background(), &mcp.CallToolParams{Name: "write_thing", Arguments: map[string]any{"value": "must-not-run"}}); err == nil || !strings.Contains(err.Error(), "header mismatch") {
		t.Fatalf("forged Mcp-Name was not rejected: %v", err)
	}
}

func testOAuthServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	if err := SetPassword(dir, []byte("correct horse battery staple")); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Options{IssuerURL: "https://mcp.example", ResourceURL: "https://mcp.example/mcp", StateDir: dir,
		AccessTokenTTL: time.Hour, AuthorizationCodeTTL: time.Minute, LoginSessionTTL: time.Minute, ClientMetadataTimeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mintToken(t *testing.T, s *Server, scope string) string {
	t.Helper()
	now := time.Now()
	raw, err := signToken(s.privateKey, s.kid, tokenClaims{Issuer: s.opts.IssuerURL, Subject: "owner", Audience: s.opts.ResourceURL,
		Expires: now.Add(time.Hour).Unix(), IssuedAt: now.Unix(), JWTID: "test", Scope: scope, ClientID: "https://client.example/client.json"})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func connectTestClient(t *testing.T, endpoint string, client *http.Client) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "dev"}, nil)
	session, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

type authRoundTripper struct {
	base              http.RoundTripper
	token, forgedName string
}

func (r authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+r.token)
	if clone.Header.Get("Mcp-Method") == "tools/call" && r.forgedName != "" {
		clone.Header.Set("Mcp-Name", r.forgedName)
	}
	return r.base.RoundTrip(clone)
}

func toString(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestHTTPBearerChallengeUsesRFCQuotedSyntax(t *testing.T) {
	rec := httptest.NewRecorder()
	writeBearerChallenge(rec, "https://mcp.example/.well-known/oauth-protected-resource/mcp", ScopeWrite, http.StatusUnauthorized, "invalid_token", `bad "token"`)
	got := rec.Header().Get("WWW-Authenticate")
	want := `Bearer resource_metadata="https://mcp.example/.well-known/oauth-protected-resource/mcp", scope="mcp:write", error="invalid_token", error_description="bad \"token\""`
	if got != want {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
	}
}
