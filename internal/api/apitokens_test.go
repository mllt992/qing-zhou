package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/store"
)

func openAPITokenTestAPI(t *testing.T) (*API, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return &API{st: st, secret: []byte("test-secret-32-bytes-pad-pad-pad!")}, st
}

func TestAPIToken_AuthAndScopes(t *testing.T) {
	a, st := openAPITokenTestAPI(t)
	plain, _, err := st.CreateAPIToken("ci", []string{"stats:read"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(a.authMiddleware)
	r.Use(a.requireAdmin)
	r.Use(a.enforceAPITokenScope)
	r.Get("/api/admin/stats/overview", func(w http.ResponseWriter, r *http.Request) {
		ok(w, J{"ok": true})
	})
	r.Get("/api/admin/users", func(w http.ResponseWriter, r *http.Request) {
		ok(w, J{"ok": true})
	})
	r.Get("/api/admin/tokens", a.handleAdminListAPITokens)
	r.Post("/api/admin/settings", func(w http.ResponseWriter, r *http.Request) {
		ok(w, nil)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/overview", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("stats with scope: got %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("users without scope: got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/admin/settings", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+plain)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("settings write: got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/admin/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("tokens list via api token: got %d", rec.Code)
	}

	list, _ := st.ListAPITokens()
	_ = st.RevokeAPIToken(list[0].ID)
	req = httptest.NewRequest(http.MethodGet, "/api/admin/stats/overview", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("revoked: got %d", rec.Code)
	}
}

func TestAPIToken_CreateHandlerJWT(t *testing.T) {
	a, _ := openAPITokenTestAPI(t)
	body, _ := json.Marshal(map[string]any{
		"name": "n", "scopes": []string{"nodes:read"}, "expires_in": 3600,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/tokens", bytes.NewReader(body))
	ctx := context.WithValue(req.Context(), ctxUserID, int64(7))
	ctx = context.WithValue(ctx, ctxRole, "admin")
	ctx = context.WithValue(ctx, ctxAuthKind, "jwt")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	a.handleAdminCreateAPIToken(rec, req)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Data.Token == "" {
		t.Fatalf("envelope: %v body=%s", err, rec.Body.String())
	}
}

func TestAPIToken_RejectedOnUserRoutes(t *testing.T) {
	a, st := openAPITokenTestAPI(t)
	plain, _, err := st.CreateAPIToken("ci", []string{"stats:read"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(a.authMiddleware)
	r.Use(a.rejectAPIToken)
	r.Get("/api/user/dashboard", func(w http.ResponseWriter, r *http.Request) {
		ok(w, J{"ok": true})
	})
	r.Post("/api/user/password", func(w http.ResponseWriter, r *http.Request) {
		ok(w, nil)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/user/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("dashboard with scoped api token: got %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/user/password", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+plain)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("password mutation with scoped api token: got %d %s", rec.Code, rec.Body.String())
	}
}
