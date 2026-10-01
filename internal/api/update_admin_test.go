package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/auth"
	"qingzhou/internal/store"
	"qingzhou/internal/updater"
)

func TestUpgradeSnapshotRoutesRequireAdministrator(t *testing.T) {
	base, st := openAPITokenTestAPI(t)
	a := New(st, base.secret, nil)
	admin, err := st.UserByID(1)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(store.NewUser{Username: "snapshot-viewer", PasswordHash: "test", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{"anonymous": ""}
	for name, user := range map[string]*store.User{"admin": admin, "user": ordinary} {
		tok, err := a.issueLogin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), user)
		if err != nil {
			t.Fatal(err)
		}
		tokens[name] = tok
	}
	limited, _, err := st.CreateAPIToken("limited", []string{"stats:read"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	tokens["limited-token"] = limited

	id := "snapshot-20261001T120000.000000000Z-0123456789abcdef"
	dir := filepath.Join(filepath.Dir(st.Path()), "upgrade-snapshots", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "database.db")
	if err := st.BackupTo(db); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	info := updater.SnapshotInfo{ID: id, SourceVersion: "v1", TargetVersion: "v2", SourceRevision: "test-revision", CreatedAt: time.Now().Unix(), Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
	meta, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	claims, err := auth.Parse(a.secret, tokens["admin"])
	if err != nil {
		t.Fatal(err)
	}
	proof, err := a.issueStepUp(admin, claims.ID, stepUpBackup, stepUpTTL)
	if err != nil {
		t.Fatal(err)
	}
	router := a.Router()
	for name, token := range tokens {
		for _, path := range []string{"/api/admin/update/snapshots", "/api/admin/update/snapshots/" + id + "/download"} {
			t.Run(name+path, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				if name == "admin" {
					req.Header.Set(stepUpHeader, proof)
				}
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				want := http.StatusForbidden
				if name == "anonymous" {
					want = http.StatusUnauthorized
				}
				if name == "admin" {
					want = http.StatusOK
				}
				if rec.Code != want {
					t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body.String())
				}
				if want != http.StatusOK {
					return
				}
				if rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("missing no-store")
				}
				if strings.HasSuffix(path, "/download") {
					if rec.Body.String() != string(body) {
						t.Fatal("download is not the consistent database snapshot")
					}
					if !strings.Contains(rec.Header().Get("Content-Disposition"), id+".db") {
						t.Fatal("missing attachment filename")
					}
				} else if !strings.Contains(rec.Body.String(), "test-revision") {
					t.Fatal("missing snapshot metadata")
				}
			})
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/update/snapshots/not-a-snapshot/download", nil)
	req.Header.Set("Authorization", "Bearer "+tokens["admin"])
	req.Header.Set(stepUpHeader, proof)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unsafe ID status %d", rec.Code)
	}
}

func TestUpgradeSnapshotEndpointsReportDisabledService(t *testing.T) {
	a := &API{}
	for _, handler := range []http.HandlerFunc{a.handleUpdateSnapshots, a.handleUpdateSnapshotDownload} {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("disabled status: %d", rec.Code)
		}
	}
}
