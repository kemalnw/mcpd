package oauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type refreshTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

type refreshTestEnv struct {
	stateDir    string
	redirectURI string
	clientID    string
	metadata    *httptest.Server
	now         *time.Time
	idleTTL     time.Duration
	logs        *bytes.Buffer
}

func newRefreshTestEnv(t *testing.T, idleTTL time.Duration) *refreshTestEnv {
	t.Helper()
	stateDir := t.TempDir()
	if err := SetPassword(stateDir, []byte("correct horse battery staple")); err != nil {
		t.Fatal(err)
	}
	redirectURI := "https://client.example/callback"
	var clientID string
	metadata := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/client.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":                             clientID,
			"client_name":                           "Refresh Test Client",
			"redirect_uris":                         []string{redirectURI},
			"grant_types":                           []string{"authorization_code", "refresh_token"},
			"response_types":                        []string{"code"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	}))
	t.Cleanup(metadata.Close)
	clientID = metadata.URL + "/client.json"
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	return &refreshTestEnv{stateDir: stateDir, redirectURI: redirectURI, clientID: clientID, metadata: metadata, now: &now, idleTTL: idleTTL, logs: &bytes.Buffer{}}
}

func (e *refreshTestEnv) newServer(t *testing.T) *Server {
	t.Helper()
	s, err := NewServer(Options{
		IssuerURL: "https://mcp.example", ResourceURL: "https://mcp.example/mcp", StateDir: e.stateDir,
		AccessTokenTTL: time.Hour, RefreshTokenIdleTTL: e.idleTTL, AuthorizationCodeTTL: 5 * time.Minute, LoginSessionTTL: 10 * time.Minute,
		ClientMetadataTimeout: time.Second, ClientMetadataHTTPClient: e.metadata.Client(),
		Logger: slog.New(slog.NewJSONHandler(e.logs, nil)), Now: func() time.Time { return *e.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *refreshTestEnv) authorize(t *testing.T, s *Server, scope string) refreshTokenResponse {
	t.Helper()
	verifier := strings.Repeat("a", 43)
	digest := sha256.Sum256([]byte(verifier))
	params := url.Values{
		"response_type": {"code"}, "client_id": {e.clientID}, "redirect_uri": {e.redirectURI},
		"scope": {scope}, "state": {"refresh-state"}, "resource": {"https://mcp.example/mcp"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"},
	}
	rec := httptest.NewRecorder()
	s.Authorize(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize GET = %d: %s", rec.Code, rec.Body.String())
	}
	match := regexp.MustCompile(`name="transaction" value="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	if len(match) != 2 {
		t.Fatalf("authorization page lacks transaction: %s", rec.Body.String())
	}
	form := url.Values{"transaction": {match[1]}, "password": {"correct horse battery staple"}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
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
	code := location.Query().Get("code")
	if code == "" {
		t.Fatal("authorization response lacks code")
	}
	tokenForm := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {e.clientID}, "redirect_uri": {e.redirectURI},
		"resource": {"https://mcp.example/mcp"}, "code": {code}, "code_verifier": {verifier},
	}
	return tokenRequest(t, s, tokenForm, http.StatusOK)
}

func (e *refreshTestEnv) refresh(t *testing.T, s *Server, token string, extra url.Values, wantStatus int) refreshTokenResponse {
	t.Helper()
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {e.clientID}, "refresh_token": {token}}
	for key, values := range extra {
		form[key] = values
	}
	return tokenRequest(t, s, form, wantStatus)
}

func tokenRequest(t *testing.T, s *Server, form url.Values, wantStatus int) refreshTokenResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Token(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("token endpoint = %d, want %d: %s", rec.Code, wantStatus, rec.Body.String())
	}
	if wantStatus != http.StatusOK {
		return refreshTokenResponse{}
	}
	var response refreshTokenResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestAuthorizationMetadataAdvertisesRefreshTokenSupport(t *testing.T) {
	env := newRefreshTestEnv(t, 30*24*time.Hour)
	s := env.newServer(t)
	rec := httptest.NewRecorder()
	s.AuthorizationServerMetadata(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var metadata map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	grants := metadata["grant_types_supported"].([]any)
	if !anyContains(grants, "authorization_code") || !anyContains(grants, "refresh_token") {
		t.Fatalf("grant_types_supported = %#v", grants)
	}
	scopes := metadata["scopes_supported"].([]any)
	if !anyContains(scopes, ScopeOfflineAccess) {
		t.Fatalf("authorization scopes do not advertise offline_access: %#v", scopes)
	}

	rec = httptest.NewRecorder()
	s.ProtectedResourceMetadata(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	metadata = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	resourceScopes := metadata["scopes_supported"].([]any)
	if anyContains(resourceScopes, ScopeOfflineAccess) {
		t.Fatalf("protected resource must not advertise offline_access as an MCP resource scope: %#v", resourceScopes)
	}
}

func TestRefreshTokenRotatesPersistsAndDetectsReuse(t *testing.T) {
	env := newRefreshTestEnv(t, 30*24*time.Hour)
	s := env.newServer(t)
	initial := env.authorize(t, s, ScopeRead+" "+ScopeOfflineAccess)
	if initial.RefreshToken == "" || initial.AccessToken == "" || initial.ExpiresIn != 3600 || !hasScope(initial.Scope, ScopeOfflineAccess) {
		t.Fatalf("unexpected initial token response: %+v", initial)
	}
	if _, err := s.VerifyBearer(initial.AccessToken, ScopeRead); err != nil {
		t.Fatalf("initial access token did not verify: %v", err)
	}

	statePath := filepath.Join(env.stateDir, refreshStateFile)
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stateBytes, []byte(initial.RefreshToken)) {
		t.Fatal("persistent refresh state contains the raw bearer refresh token")
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("refresh state permissions = %#o, want 0600", info.Mode().Perm())
	}

	*env.now = env.now.Add(61 * time.Minute)
	if _, err := s.VerifyBearer(initial.AccessToken, ScopeRead); err == nil {
		t.Fatal("expired access token was still accepted")
	}
	second := env.refresh(t, s, initial.RefreshToken, nil, http.StatusOK)
	if second.RefreshToken == "" || second.RefreshToken == initial.RefreshToken {
		t.Fatalf("refresh token did not rotate: initial=%q next=%q", initial.RefreshToken, second.RefreshToken)
	}
	if _, err := s.VerifyBearer(second.AccessToken, ScopeRead); err != nil {
		t.Fatalf("refreshed access token did not verify: %v", err)
	}

	// Simulate a normal daemon restart; refresh authorization must survive it.
	s = env.newServer(t)
	*env.now = env.now.Add(61 * time.Minute)
	third := env.refresh(t, s, second.RefreshToken, nil, http.StatusOK)
	if third.RefreshToken == "" || third.RefreshToken == second.RefreshToken {
		t.Fatal("refresh token did not rotate after server restart")
	}

	// Reusing RT1 revokes the whole family, including the current RT3.
	env.refresh(t, s, initial.RefreshToken, nil, http.StatusBadRequest)
	env.refresh(t, s, third.RefreshToken, nil, http.StatusBadRequest)

	stateBytes, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{initial.RefreshToken, second.RefreshToken, third.RefreshToken} {
		if bytes.Contains(stateBytes, []byte(raw)) {
			t.Fatalf("persistent state leaked raw refresh token %q", raw)
		}
	}
	logs := env.logs.String()
	for _, raw := range []string{initial.RefreshToken, second.RefreshToken, third.RefreshToken, initial.AccessToken, second.AccessToken, third.AccessToken} {
		if strings.Contains(logs, raw) {
			t.Fatalf("OAuth log leaked raw bearer token: %s", logs)
		}
	}
	if !strings.Contains(logs, "oauth refresh success") || !strings.Contains(logs, `"reason":"reused"`) {
		t.Fatalf("refresh logs lack success/reuse metadata: %s", logs)
	}
}

func TestRefreshTokenBindingAndScopeFailuresDoNotConsumeToken(t *testing.T) {
	env := newRefreshTestEnv(t, 30*24*time.Hour)
	s := env.newServer(t)
	initial := env.authorize(t, s, ScopeRead+" "+ScopeOfflineAccess)

	env.refresh(t, s, initial.RefreshToken, url.Values{"client_id": {"https://other.example/client.json"}}, http.StatusBadRequest)
	env.refresh(t, s, initial.RefreshToken, url.Values{"resource": {"https://mcp.example/other"}}, http.StatusBadRequest)
	env.refresh(t, s, initial.RefreshToken, url.Values{"scope": {ScopeWrite}}, http.StatusBadRequest)

	// All binding/scope failures above are non-consuming; the legitimate client can still refresh.
	response := env.refresh(t, s, initial.RefreshToken, url.Values{"resource": {"https://mcp.example/mcp"}}, http.StatusOK)
	if _, err := s.VerifyBearer(response.AccessToken, ScopeRead); err != nil {
		t.Fatalf("legitimate refresh failed after rejected attempts: %v", err)
	}
	if _, err := s.VerifyBearer(response.AccessToken, ScopeWrite); err == nil {
		t.Fatal("refresh expanded access-token scope")
	}
}

func TestRefreshTokenSlidingIdleExpiration(t *testing.T) {
	env := newRefreshTestEnv(t, 30*24*time.Hour)
	s := env.newServer(t)
	initial := env.authorize(t, s, ScopeRead+" "+ScopeOfflineAccess)

	*env.now = env.now.Add(29 * 24 * time.Hour)
	second := env.refresh(t, s, initial.RefreshToken, nil, http.StatusOK)

	// The successful refresh above slides the idle deadline another 30 days.
	*env.now = env.now.Add(29 * 24 * time.Hour)
	third := env.refresh(t, s, second.RefreshToken, nil, http.StatusOK)

	*env.now = env.now.Add(31 * 24 * time.Hour)
	env.refresh(t, s, third.RefreshToken, nil, http.StatusBadRequest)
	if !strings.Contains(env.logs.String(), `"reason":"expired"`) {
		t.Fatalf("idle-expired refresh rejection was not logged: %s", env.logs.String())
	}
}

func TestOfflineAccessMustBeRequestedToIssueRefreshToken(t *testing.T) {
	env := newRefreshTestEnv(t, 30*24*time.Hour)
	s := env.newServer(t)
	response := env.authorize(t, s, ScopeRead)
	if response.RefreshToken != "" {
		t.Fatalf("refresh token issued without offline_access: %+v", response)
	}
}

func anyContains(values []any, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
