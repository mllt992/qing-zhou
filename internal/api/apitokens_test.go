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
	if _, err := st.CreateUser(store.NewUser{
		Username: "api-token-owner", PasswordHash: "test", Role: "admin",
	}); err != nil {
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

func TestAPIToken_ServersReadDoesNotExposeProbeCredential(t *testing.T) {
	a, st := openAPITokenTestAPI(t)
	st.SetSecretKey([]byte("api-token-test-encryption-key"))
	if _, err := st.CreateServer(store.Server{
		Name: "edge", Host: "192.0.2.10", Enabled: true,
		ProbeEnabled: true, ProbeToken: "probe-write-credential",
	}); err != nil {
		t.Fatal(err)
	}
	plain, _, err := st.CreateAPIToken("read-only", []string{"servers:read"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(a.authMiddleware)
	r.Use(a.requireAdmin)
	r.Use(a.enforceAPITokenScope)
	r.Get("/api/admin/servers", a.handleAdminListServers)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/servers", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list servers: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data []store.Server `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 1 {
		t.Fatalf("servers = %d, want 1", len(env.Data))
	}
	if env.Data[0].ProbeToken != "" {
		t.Fatalf("servers:read exposed probe write credential %q", env.Data[0].ProbeToken)
	}

	// A browser admin still needs the credential to copy the one-click probe
	// install command. The redaction applies only to scoped machine tokens.
	req = httptest.NewRequest(http.MethodGet, "/api/admin/servers", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxAuthKind, "jwt"))
	rec = httptest.NewRecorder()
	a.handleAdminListServers(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 1 || env.Data[0].ProbeToken != "probe-write-credential" {
		t.Fatalf("browser admin lost probe install credential: %+v", env.Data)
	}
}

func TestAPIToken_BannedOwnerIsRejectedAndTokenStaysRevoked(t *testing.T) {
	a, st := openAPITokenTestAPI(t)
	plain, tok, err := st.CreateAPIToken("ops", []string{"servers:read"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AdminUpdateUser(1, "banned", false, nil); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(a.authMiddleware)
	r.Get("/api/admin/servers", func(w http.ResponseWriter, r *http.Request) { ok(w, nil) })
	call := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/servers", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := call(plain); got != http.StatusUnauthorized {
		t.Fatalf("banned owner token: got %d, want 401", got)
	}
	// Defense in depth: even an unrevoked row (for example, created by direct
	// database maintenance) cannot authenticate while its owner is suspended.
	latePlain, _, err := st.CreateAPIToken("late", []string{"servers:read"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := call(latePlain); got != http.StatusUnauthorized {
		t.Fatalf("unrevoked token with banned owner: got %d, want 401", got)
	}
	if err := st.AdminUpdateUser(1, "active", false, nil); err != nil {
		t.Fatal(err)
	}
	if got := call(plain); got != http.StatusUnauthorized {
		t.Fatalf("unban resurrected token: got %d, want 401", got)
	}
	list, err := st.ListAPITokens()
	if err != nil {
		t.Fatal(err)
	}
	var original *store.APIToken
	for _, item := range list {
		if item.ID == tok.ID {
			original = item
			break
		}
	}
	if original == nil || original.RevokedAt == 0 {
		t.Fatalf("token was not persistently revoked: list=%+v err=%v", list, err)
	}
}

func TestAPIToken_MissingOwnerIsRejected(t *testing.T) {
	a, st := openAPITokenTestAPI(t)
	plain, _, err := st.CreateAPIToken("orphan", []string{"servers:read"}, 999, 0)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(a.authMiddleware)
	r.Get("/api/admin/servers", func(w http.ResponseWriter, r *http.Request) { ok(w, nil) })
	req := httptest.NewRequest(http.MethodGet, "/api/admin/servers", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("orphan token: got %d, want 401", rec.Code)
	}
}
