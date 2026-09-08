package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/store"
)

// handleAdminListAPITokens GET /api/admin/tokens
func (a *API) handleAdminListAPITokens(w http.ResponseWriter, r *http.Request) {
	if !a.requireJWTAdmin(w, r) {
		return
	}
	list, err := a.st.ListAPITokens()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取失败")
		return
	}
	ok(w, list)
}

type createAPITokenReq struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresIn int64    `json:"expires_in"` // seconds from now; 0 = never
	ExpiresAt int64    `json:"expires_at"` // absolute unix; takes precedence if >0
}

// handleAdminCreateAPIToken POST /api/admin/tokens
func (a *API) handleAdminCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	if !a.requireJWTAdmin(w, r) {
		return
	}
	var req createAPITokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "无效请求")
		return
	}
	expiresAt := req.ExpiresAt
	if expiresAt == 0 && req.ExpiresIn > 0 {
		expiresAt = time.Now().Unix() + req.ExpiresIn
	}
	uid, _ := r.Context().Value(ctxUserID).(int64)
	plain, tok, err := a.st.CreateAPIToken(req.Name, req.Scopes, uid, expiresAt)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("audit: api_token created id=%d name=%q scopes=%v by=%d", tok.ID, tok.Name, tok.Scopes, uid)
	ok(w, J{
		"token":      plain, // shown once
		"id":         tok.ID,
		"name":       tok.Name,
		"prefix":     tok.Prefix,
		"scopes":     tok.Scopes,
		"expires_at": tok.ExpiresAt,
		"created_at": tok.CreatedAt,
	})
}

// handleAdminRevokeAPIToken DELETE /api/admin/tokens/{id}
func (a *API) handleAdminRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if !a.requireJWTAdmin(w, r) {
		return
	}
	id := int64(atoi(chi.URLParam(r, "id")))
	if id <= 0 {
		fail(w, http.StatusBadRequest, "无效 id")
		return
	}
	if err := a.st.RevokeAPIToken(id); err != nil {
		fail(w, http.StatusNotFound, "令牌不存在")
		return
	}
	uid, _ := r.Context().Value(ctxUserID).(int64)
	log.Printf("audit: api_token revoked id=%d by=%d", id, uid)
	ok(w, nil)
}

// requireJWTAdmin rejects API-token auth for token management endpoints.
func (a *API) requireJWTAdmin(w http.ResponseWriter, r *http.Request) bool {
	kind, _ := r.Context().Value(ctxAuthKind).(string)
	if kind == "api_token" {
		fail(w, http.StatusForbidden, "权限不足")
		return false
	}
	role, _ := r.Context().Value(ctxRole).(string)
	if role != "admin" {
		fail(w, http.StatusForbidden, "需要管理员权限")
		return false
	}
	return true
}

// scopeForAdminRequest maps an admin HTTP request to a required API token
// scope. Empty means the route is not available to API tokens (JWT only).
func scopeForAdminRequest(r *http.Request) string {
	path := r.URL.Path
	method := r.Method
	if strings.HasPrefix(path, "/api/admin/tokens") {
		return "" // JWT only
	}
	if strings.HasPrefix(path, "/api/admin/update") {
		return "" // never for tokens
	}
	if method == http.MethodGet && strings.HasPrefix(path, "/api/admin/stats") {
		return "stats:read"
	}
	if method == http.MethodGet && (path == "/api/admin/users" || strings.HasPrefix(path, "/api/admin/users/")) {
		return "users:read"
	}
	if method == http.MethodGet && (path == "/api/admin/nodes" || strings.HasPrefix(path, "/api/admin/nodes/")) {
		return "nodes:read"
	}
	if method == http.MethodGet && (path == "/api/admin/servers" || strings.HasPrefix(path, "/api/admin/servers/")) {
		return "servers:read"
	}
	if method == http.MethodGet && path == "/api/admin/backup" {
		return "backup:write"
	}
	return ""
}

// enforceAPITokenScope denies API-token callers that lack the mapped scope.
// JWT sessions pass through unchanged.
func (a *API) enforceAPITokenScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind, _ := r.Context().Value(ctxAuthKind).(string)
		if kind != "api_token" {
			next.ServeHTTP(w, r)
			return
		}
		need := scopeForAdminRequest(r)
		if need == "" {
			fail(w, http.StatusForbidden, "权限不足")
			return
		}
		scopes, _ := r.Context().Value(ctxScopes).([]string)
		if !store.APITokenHasScope(scopes, need) {
			fail(w, http.StatusForbidden, "权限不足")
			return
		}
		next.ServeHTTP(w, r)
	})
}
