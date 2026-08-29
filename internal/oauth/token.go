package oauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type tokenClaims struct {
	Issuer   string `json:"iss"`
	Subject  string `json:"sub"`
	Audience string `json:"aud"`
	Expires  int64  `json:"exp"`
	IssuedAt int64  `json:"iat"`
	JWTID    string `json:"jti"`
	Scope    string `json:"scope"`
	ClientID string `json:"client_id"`
}

type tokenHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

type Identity struct {
	Subject  string
	ClientID string
	Scopes   map[string]struct{}
}

func loadOrCreateSigningKey(stateDir string) (ed25519.PrivateKey, string, error) {
	path := filepath.Join(stateDir, "oauth-ed25519.key")
	data, err := os.ReadFile(path)
	if err == nil {
		raw, decErr := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if decErr != nil || len(raw) != ed25519.PrivateKeySize {
			return nil, "", errors.New("invalid OAuth signing key")
		}
		key := ed25519.PrivateKey(raw)
		return key, keyID(key.Public().(ed25519.PublicKey)), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("read OAuth signing key: %w", err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("generate OAuth signing key: %w", err)
	}
	if err := atomicWrite(path, []byte(base64.RawStdEncoding.EncodeToString(private)+"\n"), 0o600); err != nil {
		return nil, "", fmt.Errorf("persist OAuth signing key: %w", err)
	}
	return private, keyID(private.Public().(ed25519.PublicKey)), nil
}

func keyID(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

func signToken(private ed25519.PrivateKey, kid string, claims tokenClaims) (string, error) {
	headerBytes, err := json.Marshal(tokenHeader{Alg: "EdDSA", Typ: "JWT", Kid: kid})
	if err != nil {
		return "", err
	}
	claimBytes, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signed := base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(claimBytes)
	sig := ed25519.Sign(private, []byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func verifyToken(raw string, public ed25519.PublicKey, kid, issuer, audience string, now time.Time) (Identity, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Identity{}, errors.New("malformed access token")
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Identity{}, errors.New("malformed access token header")
	}
	var header tokenHeader
	if err := json.Unmarshal(headerRaw, &header); err != nil || header.Alg != "EdDSA" || header.Typ != "JWT" || header.Kid != kid {
		return Identity{}, errors.New("invalid access token header")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(public, []byte(parts[0]+"."+parts[1]), sig) {
		return Identity{}, errors.New("invalid access token signature")
	}
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Identity{}, errors.New("malformed access token claims")
	}
	var claims tokenClaims
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		return Identity{}, errors.New("malformed access token claims")
	}
	nowUnix := now.Unix()
	if claims.Issuer != issuer || claims.Audience != audience || claims.Subject == "" || claims.ClientID == "" {
		return Identity{}, errors.New("access token issuer, audience, or subject mismatch")
	}
	if claims.Expires <= nowUnix || claims.IssuedAt > nowUnix+60 || claims.IssuedAt <= 0 {
		return Identity{}, errors.New("access token expired or not yet valid")
	}
	scopes := make(map[string]struct{})
	for _, scope := range strings.Fields(claims.Scope) {
		scopes[scope] = struct{}{}
	}
	return Identity{Subject: claims.Subject, ClientID: claims.ClientID, Scopes: scopes}, nil
}

func randomURLToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
