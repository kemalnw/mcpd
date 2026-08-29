package oauth

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	ScopeRead  = "mcp:read"
	ScopeWrite = "mcp:write"
)

var supportedScopes = []string{ScopeRead, ScopeWrite}

type Options struct {
	IssuerURL                string
	ResourceURL              string
	StateDir                 string
	AccessTokenTTL           time.Duration
	AuthorizationCodeTTL     time.Duration
	LoginSessionTTL          time.Duration
	ClientMetadataTimeout    time.Duration
	ClientMetadataHTTPClient *http.Client
	Logger                   *slog.Logger
}

type Server struct {
	opts         Options
	logger       *slog.Logger
	privateKey   ed25519.PrivateKey
	publicKey    ed25519.PublicKey
	kid          string
	passwordHash string
	httpClient   *http.Client

	mu      sync.Mutex
	pending map[string]*pendingAuthorization
	codes   map[[32]byte]authorizationCode
}

type pendingAuthorization struct {
	CreatedAt     time.Time
	ExpiresAt     time.Time
	Attempts      int
	ClientID      string
	ClientName    string
	RedirectURI   string
	State         string
	Scope         string
	Resource      string
	CodeChallenge string
}

type authorizationCode struct {
	ExpiresAt     time.Time
	ClientID      string
	RedirectURI   string
	Scope         string
	Resource      string
	CodeChallenge string
}

func NewServer(opts Options) (*Server, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	if opts.AccessTokenTTL <= 0 || opts.AuthorizationCodeTTL <= 0 || opts.LoginSessionTTL <= 0 || opts.ClientMetadataTimeout <= 0 {
		return nil, errors.New("OAuth lifetimes and client metadata timeout must be positive")
	}
	if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create OAuth state directory: %w", err)
	}
	if err := os.Chmod(opts.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod OAuth state directory: %w", err)
	}
	passwordHash, err := loadPasswordHash(opts.StateDir)
	if err != nil {
		return nil, err
	}
	privateKey, kid, err := loadOrCreateSigningKey(opts.StateDir)
	if err != nil {
		return nil, err
	}
	client := opts.ClientMetadataHTTPClient
	if client == nil {
		client = newSafeHTTPClient(opts.ClientMetadataTimeout)
	}
	return &Server{
		opts: opts, logger: opts.Logger, privateKey: privateKey, publicKey: privateKey.Public().(ed25519.PublicKey), kid: kid,
		passwordHash: passwordHash, httpClient: client, pending: make(map[string]*pendingAuthorization), codes: make(map[[32]byte]authorizationCode),
	}, nil
}

func (s *Server) ProtectedResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource": s.opts.ResourceURL, "resource_name": "mcpd", "authorization_servers": []string{s.opts.IssuerURL},
		"scopes_supported": supportedScopes, "bearer_methods_supported": []string{"header"},
	})
}

func (s *Server) AuthorizationServerMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         s.opts.IssuerURL,
		"authorization_endpoint":                         s.opts.IssuerURL + "/oauth/authorize",
		"token_endpoint":                                 s.opts.IssuerURL + "/oauth/token",
		"jwks_uri":                                       s.opts.IssuerURL + "/oauth/jwks.json",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code"},
		"code_challenge_methods_supported":               []string{"S256"},
		"scopes_supported":                               supportedScopes,
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
		"protected_resources":                            []string{s.opts.ResourceURL},
	})
}

func (s *Server) JWKS(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]any{{
		"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA", "kid": s.kid,
		"x": base64.RawURLEncoding.EncodeToString(s.publicKey),
	}}})
}

func (s *Server) Authorize(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.authorizeGET(w, r)
	case http.MethodPost:
		s.authorizePOST(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) authorizeGET(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	if q.Get("response_type") != "code" || clientID == "" || redirectURI == "" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "PKCE S256 is required", http.StatusBadRequest)
		return
	}
	resource := q.Get("resource")
	if resource != s.opts.ResourceURL {
		http.Error(w, "resource must exactly match this MCP resource", http.StatusBadRequest)
		return
	}
	scope, err := normalizeScope(q.Get("scope"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.opts.ClientMetadataTimeout)
	defer cancel()
	metadata, err := fetchClientMetadata(ctx, s.httpClient, clientID)
	if err != nil || validateRedirectURI(metadata, redirectURI) != nil {
		http.Error(w, "invalid OAuth client metadata or redirect URI", http.StatusBadRequest)
		return
	}
	txn, err := randomURLToken(32)
	if err != nil {
		http.Error(w, "unable to create authorization transaction", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	pending := &pendingAuthorization{CreatedAt: now, ExpiresAt: now.Add(s.opts.LoginSessionTTL), ClientID: clientID,
		ClientName: metadata.ClientName, RedirectURI: redirectURI, State: q.Get("state"), Scope: scope, Resource: resource,
		CodeChallenge: q.Get("code_challenge")}
	s.mu.Lock()
	s.cleanupLocked(now)
	s.pending[txn] = pending
	s.mu.Unlock()
	s.renderLogin(w, txn, pending, "")
}

func (s *Server) authorizePOST(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid authorization form", http.StatusBadRequest)
		return
	}
	txn := r.PostForm.Get("transaction")
	now := time.Now()

	// Claim the transaction before performing the intentionally expensive
	// Argon2 verification. This keeps the mutex uncontended and makes concurrent
	// submissions for one authorization transaction single-use.
	s.mu.Lock()
	s.cleanupLocked(now)
	pending := s.pending[txn]
	if pending != nil {
		delete(s.pending, txn)
	}
	s.mu.Unlock()
	if pending == nil {
		http.Error(w, "authorization transaction expired", http.StatusBadRequest)
		return
	}

	if !verifyPassword(s.passwordHash, []byte(r.PostForm.Get("password"))) {
		pending.Attempts++
		if pending.Attempts >= 5 || !pending.ExpiresAt.After(time.Now()) {
			http.Error(w, "authorization transaction expired", http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		s.pending[txn] = pending
		s.mu.Unlock()
		s.renderLogin(w, txn, pending, "Invalid owner password")
		return
	}

	code, err := randomURLToken(32)
	if err != nil {
		http.Error(w, "unable to issue authorization code", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.codes[sha256.Sum256([]byte(code))] = authorizationCode{ExpiresAt: now.Add(s.opts.AuthorizationCodeTTL), ClientID: pending.ClientID,
		RedirectURI: pending.RedirectURI, Scope: pending.Scope, Resource: pending.Resource, CodeChallenge: pending.CodeChallenge}
	s.mu.Unlock()

	location, err := authorizationRedirect(pending.RedirectURI, map[string]string{"code": code, "state": pending.State, "iss": s.opts.IssuerURL})
	if err != nil {
		http.Error(w, "invalid redirect URI", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, location, http.StatusFound)
}

func (s *Server) Token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Authorization") != "" {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "this authorization server accepts public clients with token endpoint auth method none")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code is supported")
		return
	}
	code := r.PostForm.Get("code")
	key := sha256.Sum256([]byte(code))
	now := time.Now()
	s.mu.Lock()
	s.cleanupLocked(now)
	grant, ok := s.codes[key]
	if ok {
		delete(s.codes, key)
	}
	s.mu.Unlock()
	if !ok || code == "" {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid or expired")
		return
	}
	if grant.ClientID != r.PostForm.Get("client_id") || grant.RedirectURI != r.PostForm.Get("redirect_uri") || grant.Resource != r.PostForm.Get("resource") {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "authorization code binding mismatch")
		return
	}
	verifier := r.PostForm.Get("code_verifier")
	if !validPKCEVerifier(verifier) || !verifyPKCE(verifier, grant.CodeChallenge) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	jti, err := randomURLToken(16)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "unable to issue access token")
		return
	}
	claims := tokenClaims{Issuer: s.opts.IssuerURL, Subject: "owner", Audience: grant.Resource, Expires: now.Add(s.opts.AccessTokenTTL).Unix(),
		IssuedAt: now.Unix(), JWTID: jti, Scope: grant.Scope, ClientID: grant.ClientID}
	token, err := signToken(s.privateKey, s.kid, claims)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "unable to issue access token")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": int64(s.opts.AccessTokenTTL.Seconds()), "scope": grant.Scope})
}

func (s *Server) VerifyBearer(raw string, requiredScope string) (Identity, error) {
	identity, err := verifyToken(raw, s.publicKey, s.kid, s.opts.IssuerURL, s.opts.ResourceURL, time.Now())
	if err != nil {
		return Identity{}, err
	}
	if requiredScope != "" {
		if _, ok := identity.Scopes[requiredScope]; !ok {
			return Identity{}, fmt.Errorf("required scope %q is missing", requiredScope)
		}
	}
	return identity, nil
}

func (s *Server) ResourceMetadataURL() string {
	u, err := url.Parse(s.opts.ResourceURL)
	if err != nil || u.Path == "" || u.Path == "/" {
		return s.opts.IssuerURL + "/.well-known/oauth-protected-resource"
	}
	return s.opts.IssuerURL + "/.well-known/oauth-protected-resource" + u.EscapedPath()
}

func (s *Server) RootResourceMetadataURL() string {
	return s.opts.IssuerURL + "/.well-known/oauth-protected-resource"
}

func (s *Server) cleanupLocked(now time.Time) {
	for id, pending := range s.pending {
		if !pending.ExpiresAt.After(now) {
			delete(s.pending, id)
		}
	}
	for code, grant := range s.codes {
		if !grant.ExpiresAt.After(now) {
			delete(s.codes, code)
		}
	}
}

func normalizeScope(raw string) (string, error) {
	requested := strings.Fields(raw)
	if len(requested) == 0 {
		requested = append([]string(nil), supportedScopes...)
	}
	seen := make(map[string]bool)
	out := make([]string, 0, len(requested))
	for _, scope := range requested {
		if !contains(supportedScopes, scope) {
			return "", fmt.Errorf("unsupported scope %q", scope)
		}
		if !seen[scope] {
			seen[scope] = true
			out = append(out, scope)
		}
	}
	return strings.Join(out, " "), nil
}

func validPKCEVerifier(verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, ch := range verifier {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("-._~", ch) {
			continue
		}
		return false
	}
	return true
}

func verifyPKCE(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

func authorizationRedirect(raw string, values map[string]string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for key, value := range values {
		if value != "" {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *Server) renderLogin(w http.ResponseWriter, txn string, pending *pendingAuthorization, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	name := pending.ClientName
	if name == "" {
		name = pending.ClientID
	}
	_ = loginTemplate.Execute(w, map[string]string{"Transaction": txn, "Client": name, "Scope": pending.Scope, "Resource": pending.Resource, "Message": message})
}

var loginTemplate = template.Must(template.New("login").Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorize mcpd</title><style>body{font:16px system-ui;max-width:38rem;margin:5rem auto;padding:0 1rem}label,input,button{display:block;width:100%;box-sizing:border-box}input,button{padding:.75rem;margin:.5rem 0 1rem}code{overflow-wrap:anywhere}.err{color:#b42318}</style></head><body><h1>Authorize mcpd</h1><p>Client: <strong>{{.Client}}</strong></p><p>Resource: <code>{{.Resource}}</code></p><p>Scopes: <code>{{.Scope}}</code></p>{{if .Message}}<p class="err">{{.Message}}</p>{{end}}<form method="post" action="/oauth/authorize"><input type="hidden" name="transaction" value="{{.Transaction}}"><label>Owner password<input type="password" name="password" autocomplete="current-password" required autofocus></label><button type="submit">Authorize</button></form></body></html>`))

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func PasswordPath(stateDir string) string { return filepath.Join(stateDir, "owner.password") }
