package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// KnownAPITokenScopes is the whitelist of scopes a token may request.
var KnownAPITokenScopes = []string{
	"stats:read",
	"users:read",
	"nodes:read",
	"servers:read",
	"backup:write",
}

const apiTokenPlainPrefix = "qz_at_"

// APIToken is a row from api_tokens. TokenHash is never exported in JSON.
type APIToken struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes"`
	CreatedBy  int64    `json:"created_by"`
	ExpiresAt  int64    `json:"expires_at"`
	LastUsedAt int64    `json:"last_used_at"`
	RevokedAt  int64    `json:"revoked_at"`
	CreatedAt  int64    `json:"created_at"`
	TokenHash  string   `json:"-"`
}

func hashAPIToken(plain string) string {
	h := sha256.Sum256([]byte("qz-api-token:" + plain))
	return hex.EncodeToString(h[:])
}

func normalizeScopes(in []string) ([]string, error) {
	allowed := map[string]bool{}
	for _, s := range KnownAPITokenScopes {
		allowed[s] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !allowed[s] {
			return nil, fmt.Errorf("未知 scope: %s", s)
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, errors.New("至少选择一个 scope")
	}
	return out, nil
}

func scopesJSON(scopes []string) (string, error) {
	b, err := json.Marshal(scopes)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseScopesJSON(raw string) []string {
	var out []string
	if raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		return []string{}
	}
	return out
}

func mintAPITokenPlain() (plain, prefix string, err error) {
	var b [24]byte
	if _, err = rand.Read(b[:]); err != nil {
		return "", "", err
	}
	plain = apiTokenPlainPrefix + hex.EncodeToString(b[:])
	// Show a short stable prefix for list UI (never enough to recreate the token).
	prefix = plain[:len(apiTokenPlainPrefix)+8]
	return plain, prefix, nil
}

// CreateAPIToken mints a new token. The plaintext is returned once; only the
// hash is persisted. expiresAt 0 means never expire.
func (s *Store) CreateAPIToken(name string, scopes []string, createdBy, expiresAt int64) (plain string, tok *APIToken, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, errors.New("名称不能为空")
	}
	scopes, err = normalizeScopes(scopes)
	if err != nil {
		return "", nil, err
	}
	if expiresAt < 0 {
		return "", nil, errors.New("过期时间无效")
	}
	plain, prefix, err := mintAPITokenPlain()
	if err != nil {
		return "", nil, err
	}
	rawScopes, err := scopesJSON(scopes)
	if err != nil {
		return "", nil, err
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO api_tokens
		(name, token_prefix, token_hash, scopes, created_by, expires_at, last_used_at, revoked_at, created_at)
		VALUES (?,?,?,?,?,?,0,0,?)`,
		name, prefix, hashAPIToken(plain), rawScopes, createdBy, expiresAt, now)
	if err != nil {
		return "", nil, err
	}
	id, _ := res.LastInsertId()
	return plain, &APIToken{
		ID: id, Name: name, Prefix: prefix, Scopes: scopes,
		CreatedBy: createdBy, ExpiresAt: expiresAt, CreatedAt: now,
	}, nil
}

// ListAPITokens returns non-deleted tokens (including revoked) newest first.
func (s *Store) ListAPITokens() ([]*APIToken, error) {
	rows, err := s.db.Query(`SELECT id, name, token_prefix, scopes, created_by, expires_at, last_used_at, revoked_at, created_at
		FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*APIToken{}
	for rows.Next() {
		var t APIToken
		var rawScopes string
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &rawScopes, &t.CreatedBy, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Scopes = parseScopesJSON(rawScopes)
		out = append(out, &t)
	}
	return out, rows.Err()
}

// RevokeAPIToken marks a token revoked. Idempotent.
func (s *Store) RevokeAPIToken(id int64) error {
	now := time.Now().Unix()
	res, err := s.db.Exec(`UPDATE api_tokens SET revoked_at=? WHERE id=? AND revoked_at=0`, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Already revoked or missing — treat missing as not found.
		var exists int
		_ = s.db.QueryRow(`SELECT 1 FROM api_tokens WHERE id=?`, id).Scan(&exists)
		if exists == 0 {
			return sql.ErrNoRows
		}
	}
	return nil
}

// LookupAPIToken authenticates a plaintext bearer. Returns nil,nil when the
// token is unknown / revoked / expired — callers must not distinguish those
// cases in the HTTP response.
func (s *Store) LookupAPIToken(plain string) (*APIToken, error) {
	if !strings.HasPrefix(plain, apiTokenPlainPrefix) {
		return nil, nil
	}
	var t APIToken
	var rawScopes string
	err := s.db.QueryRow(`SELECT id, name, token_prefix, token_hash, scopes, created_by, expires_at, last_used_at, revoked_at, created_at
		FROM api_tokens WHERE token_hash=?`, hashAPIToken(plain)).
		Scan(&t.ID, &t.Name, &t.Prefix, &t.TokenHash, &rawScopes, &t.CreatedBy, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if t.RevokedAt > 0 {
		return nil, nil
	}
	if t.ExpiresAt > 0 && t.ExpiresAt <= now {
		return nil, nil
	}
	t.Scopes = parseScopesJSON(rawScopes)
	return &t, nil
}

// TouchAPIToken bumps last_used_at at most once per minute.
func (s *Store) TouchAPIToken(id int64) {
	now := time.Now().Unix()
	_, _ = s.db.Exec(`UPDATE api_tokens SET last_used_at=? WHERE id=? AND last_used_at < ?`, now, id, now-60)
}

// APITokenHasScope reports whether scopes includes want.
func APITokenHasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}
