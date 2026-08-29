package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestAuthorizationCodePKCEFlow(t *testing.T) {
	stateDir := t.TempDir()
	if err := SetPassword(stateDir, []byte("correct horse battery staple")); err != nil {
		t.Fatal(err)
	}
	redirectURI := "https://client.example/callback"
	var clientID string
	metadataServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/client.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":                             clientID,
			"client_name":                           "Test MCP Client",
			"redirect_uris":                         []string{redirectURI},
			"grant_types":                           []string{"authorization_code"},
			"response_types":                        []string{"code"},
			"token_endpoint_auth_methods_supported": []string{"none", "private_key_jwt"},
		})
	}))
	defer metadataServer.Close()
	clientID = metadataServer.URL + "/client.json"

	s, err := NewServer(Options{
		IssuerURL: "https://mcp.example", ResourceURL: "https://mcp.example/mcp", StateDir: stateDir,
		AccessTokenTTL: time.Hour, AuthorizationCodeTTL: 5 * time.Minute, LoginSessionTTL: 10 * time.Minute,
		ClientMetadataTimeout: time.Second, ClientMetadataHTTPClient: metadataServer.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	verifier := strings.Repeat("a", 43)
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	params := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirectURI},
		"scope": {ScopeRead}, "state": {"state-123"}, "resource": {"https://mcp.example/mcp"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()
	s.Authorize(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize GET = %d: %s", rec.Code, rec.Body.String())
	}
	match := regexp.MustCompile(`name="transaction" value="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	if len(match) != 2 {
		t.Fatalf("authorization page lacks transaction: %s", rec.Body.String())
	}

	form := url.Values{"transaction": {match[1]}, "password": {"correct horse battery staple"}}
	req = httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.Authorize(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize POST = %d: %s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Query().Get("state") != "state-123" || location.Query().Get("iss") != "https://mcp.example" {
		t.Fatalf("unexpected callback parameters: %s", location.String())
	}
	code := location.Query().Get("code")
	if code == "" {
		t.Fatal("authorization response lacks code")
	}

	tokenForm := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "redirect_uri": {redirectURI},
		"resource": {"https://mcp.example/mcp"}, "code": {code}, "code_verifier": {verifier},
	}
	req = httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(tokenForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.Token(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("token = %d: %s", rec.Code, rec.Body.String())
	}
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&tokenResponse); err != nil {
		t.Fatal(err)
	}
	if tokenResponse.TokenType != "Bearer" || tokenResponse.Scope != ScopeRead || tokenResponse.AccessToken == "" {
		t.Fatalf("unexpected token response: %+v", tokenResponse)
	}
	identity, err := s.VerifyBearer(tokenResponse.AccessToken, ScopeRead)
	if err != nil || identity.ClientID != clientID {
		t.Fatalf("verify token: identity=%+v err=%v", identity, err)
	}
	if _, err := s.VerifyBearer(tokenResponse.AccessToken, ScopeWrite); err == nil {
		t.Fatal("read-only token unexpectedly has write scope")
	}

	// Authorization codes are one-time credentials.
	req = httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(tokenForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.Token(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_grant") {
		t.Fatalf("replayed authorization code = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMetadataUsesSpecificMCPResource(t *testing.T) {
	stateDir := t.TempDir()
	if err := SetPassword(stateDir, []byte("correct horse battery staple")); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Options{IssuerURL: "https://203.0.113.10", ResourceURL: "https://203.0.113.10/mcp", StateDir: stateDir,
		AccessTokenTTL: time.Hour, AuthorizationCodeTTL: time.Minute, LoginSessionTTL: time.Minute, ClientMetadataTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.ProtectedResourceMetadata(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var metadata map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["resource"] != "https://203.0.113.10/mcp" {
		t.Fatalf("resource = %#v", metadata["resource"])
	}
	servers := metadata["authorization_servers"].([]any)
	if len(servers) != 1 || servers[0] != "https://203.0.113.10" {
		t.Fatalf("authorization_servers = %#v", servers)
	}

	rec = httptest.NewRecorder()
	s.AuthorizationServerMetadata(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body, _ := io.ReadAll(rec.Body)
	var authMetadata map[string]any
	if err := json.Unmarshal(body, &authMetadata); err != nil {
		t.Fatal(err)
	}
	if authMetadata["issuer"] != "https://203.0.113.10" || authMetadata["client_id_metadata_document_supported"] != true {
		t.Fatalf("unexpected authorization metadata: %s", body)
	}
}

func TestPasswordHashIsSaltedAndVerifiable(t *testing.T) {
	dir := t.TempDir()
	password := []byte("correct horse battery staple")
	if err := SetPassword(dir, password); err != nil {
		t.Fatal(err)
	}
	first, err := loadPasswordHash(dir)
	if err != nil || !verifyPassword(first, password) || verifyPassword(first, []byte("wrong-password-value")) {
		t.Fatalf("password verification failed: %v", err)
	}
	if err := SetPassword(dir, password); err != nil {
		t.Fatal(err)
	}
	second, _ := loadPasswordHash(dir)
	if first == second {
		t.Fatal("password hashes reused the same salt")
	}
}

func TestResourceMetadataURLUsesMCPPath(t *testing.T) {
	stateDir := t.TempDir()
	if err := SetPassword(stateDir, []byte("correct horse battery staple")); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Options{IssuerURL: "https://203.0.113.10", ResourceURL: "https://203.0.113.10/mcp", StateDir: stateDir,
		AccessTokenTTL: time.Hour, AuthorizationCodeTTL: time.Minute, LoginSessionTTL: time.Minute, ClientMetadataTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.ResourceMetadataURL(), "https://203.0.113.10/.well-known/oauth-protected-resource/mcp"; got != want {
		t.Fatalf("ResourceMetadataURL() = %q, want %q", got, want)
	}
	if got, want := s.RootResourceMetadataURL(), "https://203.0.113.10/.well-known/oauth-protected-resource"; got != want {
		t.Fatalf("RootResourceMetadataURL() = %q, want %q", got, want)
	}
}

func TestAuthorizePageSetsBrowserSecurityHeaders(t *testing.T) {
	stateDir := t.TempDir()
	if err := SetPassword(stateDir, []byte("correct horse battery staple")); err != nil {
		t.Fatal(err)
	}
	redirectURI := "https://client.example/callback"
	var clientID string
	metadataServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id": clientID, "client_name": "Test", "redirect_uris": []string{redirectURI},
			"grant_types": []string{"authorization_code"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none",
		})
	}))
	defer metadataServer.Close()
	clientID = metadataServer.URL + "/client.json"
	s, err := NewServer(Options{IssuerURL: "https://mcp.example", ResourceURL: "https://mcp.example/mcp", StateDir: stateDir,
		AccessTokenTTL: time.Hour, AuthorizationCodeTTL: time.Minute, LoginSessionTTL: time.Minute, ClientMetadataTimeout: time.Second,
		ClientMetadataHTTPClient: metadataServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("a", 43)
	digest := sha256.Sum256([]byte(verifier))
	params := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirectURI}, "resource": {"https://mcp.example/mcp"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}}
	rec := httptest.NewRecorder()
	s.Authorize(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize = %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("missing browser security headers: %#v", rec.Header())
	}
}

func TestClientIDRejectsUnstableURLForms(t *testing.T) {
	for _, raw := range []string{
		"https://client.example/client.json?variant=1",
		"https://client.example/a/../client.json",
		"https://client.example/a/%2e%2e/client.json",
	} {
		if _, err := validateClientID(raw); err == nil {
			t.Errorf("validateClientID(%q) unexpectedly succeeded", raw)
		}
	}
}
