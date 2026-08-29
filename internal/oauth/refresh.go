package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	refreshStateVersion = 1
	refreshStateFile    = "refresh-tokens.json"
	refreshTokenPrefix  = "mcpd_rt_"
)

type refreshState struct {
	Version  int                       `json:"version"`
	Families map[string]*refreshFamily `json:"families"`
}

type refreshFamily struct {
	ID               string     `json:"family_id"`
	ClientID         string     `json:"client_id"`
	Resource         string     `json:"resource"`
	Scope            string     `json:"scope"`
	CurrentTokenHash string     `json:"current_token_hash"`
	UsedTokenHashes  []string   `json:"used_token_hashes,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	LastRotatedAt    time.Time  `json:"last_rotated_at"`
	IdleExpiresAt    time.Time  `json:"idle_expires_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}

type refreshStore struct {
	mu    sync.Mutex
	path  string
	state refreshState
}

type refreshRotation struct {
	RefreshToken string
	FamilyID     string
	Scope        string
	Resource     string
	ClientID     string
}

type refreshReject struct {
	Reason string
	Err    error
}

func (e *refreshReject) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Reason
}

func openRefreshStore(stateDir string) (*refreshStore, error) {
	path := filepath.Join(stateDir, refreshStateFile)
	store := &refreshStore{path: path, state: refreshState{Version: refreshStateVersion, Families: make(map[string]*refreshFamily)}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read OAuth refresh state: %w", err)
	}
	if err := json.Unmarshal(data, &store.state); err != nil {
		return nil, fmt.Errorf("decode OAuth refresh state: %w", err)
	}
	if store.state.Version != refreshStateVersion {
		return nil, fmt.Errorf("unsupported OAuth refresh state version %d", store.state.Version)
	}
	if store.state.Families == nil {
		store.state.Families = make(map[string]*refreshFamily)
	}
	for id, family := range store.state.Families {
		if family == nil || family.ID != id || family.ClientID == "" || family.Resource == "" || family.CurrentTokenHash == "" || family.CreatedAt.IsZero() || family.IdleExpiresAt.IsZero() {
			return nil, fmt.Errorf("invalid OAuth refresh family %q", id)
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("chmod OAuth refresh state: %w", err)
	}
	return store, nil
}

func (s *refreshStore) Issue(clientID, resource, scope string, now time.Time, idleTTL time.Duration) (string, string, error) {
	token, err := newRefreshToken()
	if err != nil {
		return "", "", err
	}
	familyID, err := randomURLToken(16)
	if err != nil {
		return "", "", err
	}
	family := &refreshFamily{
		ID: familyID, ClientID: clientID, Resource: resource, Scope: scope,
		CurrentTokenHash: refreshTokenHash(token), CreatedAt: now.UTC(), LastRotatedAt: now.UTC(), IdleExpiresAt: now.Add(idleTTL).UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Families[familyID] = family
	if err := s.persistLocked(); err != nil {
		delete(s.state.Families, familyID)
		return "", "", err
	}
	return token, familyID, nil
}

func (s *refreshStore) Rotate(rawToken, clientID, resource, requestedScope string, now time.Time, idleTTL time.Duration) (refreshRotation, error) {
	if rawToken == "" {
		return refreshRotation{}, &refreshReject{Reason: "missing", Err: errors.New("refresh token is required")}
	}
	tokenHash := refreshTokenHash(rawToken)
	now = now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	family, used := s.findFamilyLocked(tokenHash)
	if family == nil {
		return refreshRotation{}, &refreshReject{Reason: "unknown", Err: errors.New("refresh token is invalid or expired")}
	}
	if used {
		if err := s.revokeFamilyLocked(family, now); err != nil {
			return refreshRotation{}, err
		}
		return refreshRotation{}, &refreshReject{Reason: "reused", Err: errors.New("refresh token reuse detected")}
	}
	if family.RevokedAt != nil {
		return refreshRotation{}, &refreshReject{Reason: "revoked", Err: errors.New("refresh token is invalid or expired")}
	}
	if !family.IdleExpiresAt.After(now) {
		if err := s.revokeFamilyLocked(family, now); err != nil {
			return refreshRotation{}, err
		}
		return refreshRotation{}, &refreshReject{Reason: "expired", Err: errors.New("refresh token is invalid or expired")}
	}
	if clientID == "" || family.ClientID != clientID {
		return refreshRotation{}, &refreshReject{Reason: "client_mismatch", Err: errors.New("refresh token client binding mismatch")}
	}
	if resource != "" && family.Resource != resource {
		return refreshRotation{}, &refreshReject{Reason: "resource_mismatch", Err: errors.New("refresh token resource binding mismatch")}
	}

	newScope := family.Scope
	if requestedScope != "" {
		if !scopeSubset(requestedScope, family.Scope) {
			return refreshRotation{}, &refreshReject{Reason: "scope_expansion", Err: errors.New("requested scope exceeds the original authorization")}
		}
		newScope = requestedScope
	}

	newToken, err := newRefreshToken()
	if err != nil {
		return refreshRotation{}, err
	}
	previous := cloneRefreshFamily(family)
	family.UsedTokenHashes = append(family.UsedTokenHashes, family.CurrentTokenHash)
	family.CurrentTokenHash = refreshTokenHash(newToken)
	family.Scope = newScope
	family.LastRotatedAt = now
	family.IdleExpiresAt = now.Add(idleTTL).UTC()
	if err := s.persistLocked(); err != nil {
		*family = previous
		return refreshRotation{}, err
	}
	return refreshRotation{RefreshToken: newToken, FamilyID: family.ID, Scope: family.Scope, Resource: family.Resource, ClientID: family.ClientID}, nil
}

func (s *refreshStore) findFamilyLocked(tokenHash string) (*refreshFamily, bool) {
	for _, family := range s.state.Families {
		if family.CurrentTokenHash == tokenHash {
			return family, false
		}
		for _, usedHash := range family.UsedTokenHashes {
			if usedHash == tokenHash {
				return family, true
			}
		}
	}
	return nil, false
}

func (s *refreshStore) revokeFamilyLocked(family *refreshFamily, now time.Time) error {
	if family.RevokedAt != nil {
		return nil
	}
	previous := cloneRefreshFamily(family)
	revokedAt := now.UTC()
	family.RevokedAt = &revokedAt
	if err := s.persistLocked(); err != nil {
		*family = previous
		return err
	}
	return nil
}

func (s *refreshStore) persistLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode OAuth refresh state: %w", err)
	}
	data = append(data, '\n')
	if err := atomicWrite(s.path, data, 0o600); err != nil {
		return fmt.Errorf("persist OAuth refresh state: %w", err)
	}
	return nil
}

func cloneRefreshFamily(family *refreshFamily) refreshFamily {
	clone := *family
	clone.UsedTokenHashes = append([]string(nil), family.UsedTokenHashes...)
	if family.RevokedAt != nil {
		revokedAt := *family.RevokedAt
		clone.RevokedAt = &revokedAt
	}
	return clone
}

func newRefreshToken() (string, error) {
	raw, err := randomURLToken(32)
	if err != nil {
		return "", err
	}
	return refreshTokenPrefix + raw, nil
}

func refreshTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func scopeSubset(requested, granted string) bool {
	grantedSet := make(map[string]struct{})
	for _, scope := range strings.Fields(granted) {
		grantedSet[scope] = struct{}{}
	}
	for _, scope := range strings.Fields(requested) {
		if _, ok := grantedSet[scope]; !ok {
			return false
		}
	}
	return true
}
