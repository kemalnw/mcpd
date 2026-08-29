package oauth

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

func (s *Server) tokenAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	code := r.PostForm.Get("code")
	key := authorizationCodeKey(code)
	now := s.now()
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

	accessToken, err := s.issueAccessToken(grant.ClientID, grant.Resource, grant.Scope, now)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "unable to issue access token")
		return
	}
	response := map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   int64(s.opts.AccessTokenTTL.Seconds()),
		"scope":        grant.Scope,
	}
	refreshIssued := false
	if hasScope(grant.Scope, ScopeOfflineAccess) {
		refreshToken, familyID, err := s.refresh.Issue(grant.ClientID, grant.Resource, grant.Scope, now, s.opts.RefreshTokenIdleTTL)
		if err != nil {
			s.logger.Error("oauth refresh authorization issue failed", "client_id", grant.ClientID, "error", err)
			oauthError(w, http.StatusInternalServerError, "server_error", "unable to persist refresh authorization")
			return
		}
		response["refresh_token"] = refreshToken
		refreshIssued = true
		s.logger.Info("oauth refresh authorization issued", "client_id", grant.ClientID, "family_id", familyID, "idle_seconds", int64(s.opts.RefreshTokenIdleTTL.Seconds()))
	}
	s.logger.Info("oauth authorization code exchanged",
		"client_id", grant.ClientID, "resource", grant.Resource, "scope", grant.Scope,
		"offline_access", hasScope(grant.Scope, ScopeOfflineAccess), "refresh_token_issued", refreshIssued,
		"access_token_seconds", int64(s.opts.AccessTokenTTL.Seconds()))
	writeTokenResponse(w, response)
}

func (s *Server) tokenRefresh(w http.ResponseWriter, r *http.Request) {
	requestedScope := ""
	if rawScope := strings.TrimSpace(r.PostForm.Get("scope")); rawScope != "" {
		normalized, err := normalizeScope(rawScope)
		if err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_scope", err.Error())
			return
		}
		requestedScope = normalized
	}

	clientID := r.PostForm.Get("client_id")
	rotation, err := s.refresh.Rotate(
		r.PostForm.Get("refresh_token"),
		clientID,
		r.PostForm.Get("resource"),
		requestedScope,
		s.now(),
		s.opts.RefreshTokenIdleTTL,
	)
	if err != nil {
		var rejected *refreshReject
		if errors.As(err, &rejected) {
			s.logger.Warn("oauth refresh rejected", "client_id", clientID, "reason", rejected.Reason)
			if rejected.Reason == "scope_expansion" {
				oauthError(w, http.StatusBadRequest, "invalid_scope", "requested scope exceeds the original authorization")
				return
			}
			oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid or expired")
			return
		}
		s.logger.Error("oauth refresh failed", "client_id", clientID, "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "unable to update refresh authorization")
		return
	}

	accessToken, err := s.issueAccessToken(rotation.ClientID, rotation.Resource, rotation.Scope, s.now())
	if err != nil {
		s.logger.Error("oauth refresh access token issue failed", "client_id", rotation.ClientID, "family_id", rotation.FamilyID, "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "unable to issue access token")
		return
	}
	s.logger.Info("oauth refresh success", "client_id", rotation.ClientID, "family_id", rotation.FamilyID)
	writeTokenResponse(w, map[string]any{
		"access_token":  accessToken,
		"token_type":    "Bearer",
		"expires_in":    int64(s.opts.AccessTokenTTL.Seconds()),
		"refresh_token": rotation.RefreshToken,
		"scope":         rotation.Scope,
	})
}

func (s *Server) issueAccessToken(clientID, resource, scope string, now time.Time) (string, error) {
	jti, err := randomURLToken(16)
	if err != nil {
		return "", err
	}
	claims := tokenClaims{
		Issuer: s.opts.IssuerURL, Subject: "owner", Audience: resource,
		Expires: now.Add(s.opts.AccessTokenTTL).Unix(), IssuedAt: now.Unix(), JWTID: jti,
		Scope: scope, ClientID: clientID,
	}
	return signToken(s.privateKey, s.kid, claims)
}

func writeTokenResponse(w http.ResponseWriter, response map[string]any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, response)
}

func hasScope(scopeList, target string) bool {
	for _, scope := range strings.Fields(scopeList) {
		if scope == target {
			return true
		}
	}
	return false
}
