package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/auth"
	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
)

const stepUpTestPassword = "current-password-fixture"

func newStepUpTestAPI(t *testing.T) (*API, string, string) {
	t.Helper()
	base, st := openAPITokenTestAPI(t)
	hash, err := auth.HashPassword(stepUpTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePassword(1, hash); err != nil {
		t.Fatal(err)
	}
	a := New(st, base.secret, nil)
	t.Cleanup(a.Close)
	token, jti := stepUpTestLogin(t, a, 1, "first-device")
	return a, token, jti
}
func stepUpTestLogin(t *testing.T, a *API, uid int64, device string) (string, string) {
	t.Helper()
	u, err := a.st.UserByID(uid)
	if err != nil || u == nil {
		t.Fatalf("test user: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.Header.Set("User-Agent", device)
	token, err := a.issueLogin(httptest.NewRecorder(), req, u)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.Parse(a.secret, token)
	if err != nil {
		t.Fatal(err)
	}
	return token, claims.ID
}
func stepUpHTTP(a *API, method, path, token, proof, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if proof != "" {
		req.Header.Set(stepUpHeader, proof)
	}
	rec := httptest.NewRecorder()
	a.Router().ServeHTTP(rec, req)
	return rec
}
func stepUpTestProof(t *testing.T, a *API, token, scope string) string {
	t.Helper()
	body, _ := json.Marshal(J{"password": stepUpTestPassword, "scope": scope, "method": "password"})
	rec := stepUpHTTP(a, "POST", "/api/user/reauth", token, "", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("reauth: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("reauth is cacheable")
	}
	var result struct {
		Data struct {
			Proof     string `json:"proof"`
			Scope     string `json:"scope"`
			ExpiresIn int    `json:"expires_in"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Proof == "" || result.Data.Scope != scope || result.Data.ExpiresIn != 300 {
		t.Fatal("invalid proof contract")
	}
	return result.Data.Proof
}
func requireStepUpChallenge(t *testing.T, rec *httptest.ResponseRecorder, scope string) {
	t.Helper()
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), `"error":"step_up_required"`) || !strings.Contains(rec.Body.String(), `"scope":"`+scope+`"`) {
		t.Fatalf("wanted %s challenge, got %d %s", scope, rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("challenge is cacheable")
	}
}

func TestStepUpProtectsRiskyRoutesBeforeHandlers(t *testing.T) {
	a, token, _ := newStepUpTestAPI(t)
	for _, tc := range []struct{ method, path, scope string }{
		{"GET", "/api/admin/backup", stepUpBackup},
		{"GET", "/api/admin/update/snapshots/missing/download", stepUpBackup},
		{"GET", "/api/admin/backups/1/download-url", stepUpBackup},
		{"POST", "/api/admin/backups", stepUpBackup},
		{"DELETE", "/api/admin/backups/1", stepUpBackup},
		{"POST", "/api/admin/certs/1/export", stepUpCertExport},
		{"POST", "/api/admin/sb/tls/1/export", stepUpCertExport},
		{"GET", "/api/admin/sb/preview", stepUpCertExport},
		{"GET", "/api/admin/sb/import-remote/preview", stepUpCertExport},
		{"PUT", "/api/admin/sb/tls/reality/1", stepUpSettings},
		{"PUT", "/api/admin/sb/tls/cert/1", stepUpSettings},
		{"POST", "/api/admin/sb/tls", stepUpSettings},
		{"POST", "/api/admin/update/apply", stepUpUpdate},
		{"POST", "/api/admin/update/rollback", stepUpUpdate},
		{"POST", "/api/admin/nodes/1/singbox/upgrade", stepUpUpdate},
		{"POST", "/api/admin/monitor/servers/1/probe/upgrade", stepUpUpdate},
		{"POST", "/api/admin/servers", stepUpSSH},
		{"PUT", "/api/admin/servers/1", stepUpSSH},
		{"DELETE", "/api/admin/servers/1", stepUpSSH},
		{"POST", "/api/admin/servers/1/clear-host-key", stepUpSSH},
		{"POST", "/api/admin/users/2/reset-node-creds", stepUpUsers},
		{"PUT", "/api/admin/users/2", stepUpUsers},
		{"PUT", "/api/admin/settings", stepUpSettings},
		{"PUT", "/api/admin/oauth2", stepUpSettings},
		{"POST", "/api/admin/tokens", stepUpSettings},
		{"DELETE", "/api/admin/tokens/1", stepUpSettings},
		{"PUT", "/api/admin/backups/config", stepUpSettings},
		{"POST", "/api/admin/backups/config/test", stepUpSettings},
		{"PUT", "/api/admin/backups/schedule", stepUpSettings},
		{"PUT", "/api/admin/upstreams/provider", stepUpSettings},
		{"DELETE", "/api/admin/upstreams/provider", stepUpSettings},
		{"POST", "/api/user/reset-sub", stepUpSubscription},
		{"POST", "/api/user/reset-node-creds", stepUpNodeCredentials},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			requireStepUpChallenge(t, stepUpHTTP(a, tc.method, tc.path, token, "", `{}`), tc.scope)
		})
	}
	for _, path := range []string{"/api/admin/settings", "/api/admin/servers", "/api/admin/certs", "/api/admin/update/status", "/api/admin/update/snapshots"} {
		if rec := stepUpHTTP(a, "GET", path, token, "", ""); rec.Code != 200 {
			t.Fatalf("read blocked %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestStepUpHTTPBackupCertUpdate(t *testing.T) {
	a, token, _ := newStepUpTestAPI(t)
	id, err := a.st.SaveCert(&store.Cert{Name: "fixture", Source: "paste", CertPEM: "public-cert-fixture", KeyPEM: "private-key-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, scope string
		status              int
		content             string
	}{
		{"GET", "/api/admin/backup", stepUpBackup, 200, "SQLite format 3"},
		{"POST", "/api/admin/certs/" + itoa(id) + "/export", stepUpCertExport, 200, "private-key-fixture"},
		// Disable updater so the HTTP integration test cannot replace a binary or
		// reach the network. 503 proves the correct scoped proof reached the handler.
		{"POST", "/api/admin/update/apply", stepUpUpdate, 503, "更新服务未启用"},
		{"POST", "/api/admin/update/rollback", stepUpUpdate, 503, "更新服务未启用"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			a.updater = nil
			proof := stepUpTestProof(t, a, token, tc.scope)
			rec := stepUpHTTP(a, tc.method, tc.path, token, proof, `{}`)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.content) {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	rec := stepUpHTTP(a, "GET", "/api/admin/certs/"+itoa(id)+"/export", token, "", "")
	if rec.Code == 200 || strings.Contains(rec.Body.String(), "private-key-fixture") {
		t.Fatal("legacy GET exports private key")
	}
}

func TestStepUpProofBindingAndPurpose(t *testing.T) {
	a, token, jti := newStepUpTestAPI(t)
	proof := stepUpTestProof(t, a, token, stepUpBackup)
	if _, err := auth.Parse(a.secret, proof); err == nil {
		t.Fatal("proof parsed as login token")
	}
	if rec := stepUpHTTP(a, "GET", "/api/admin/backup", proof, "", ""); rec.Code != 401 {
		t.Fatalf("proof as login: %d", rec.Code)
	}
	requireStepUpChallenge(t, stepUpHTTP(a, "GET", "/api/admin/backup", token, token, ""), stepUpBackup)
	requireStepUpChallenge(t, stepUpHTTP(a, "POST", "/api/admin/certs/1/export", token, proof, ""), stepUpCertExport)
	requireStepUpChallenge(t, stepUpHTTP(a, "GET", "/api/admin/backup?proof="+proof, token, "", ""), stepUpBackup)
	u, _ := a.st.UserByID(1)
	expired, err := a.issueStepUp(u, jti, stepUpBackup, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	requireStepUpChallenge(t, stepUpHTTP(a, "GET", "/api/admin/backup", token, expired, ""), stepUpBackup)
	otherToken, _ := stepUpTestLogin(t, a, 1, "second-device")
	requireStepUpChallenge(t, stepUpHTTP(a, "GET", "/api/admin/backup", otherToken, proof, ""), stepUpBackup)
	id, err := a.st.CreateUser(store.NewUser{Username: "second-admin", PasswordHash: u.PasswordHash, Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	otherUser, _ := stepUpTestLogin(t, a, id, "other-user")
	requireStepUpChallenge(t, stepUpHTTP(a, "GET", "/api/admin/backup", otherUser, proof, ""), stepUpBackup)
	// A valid signed login with another account's session must fail closed too.
	mismatched, _ := auth.Issue(a.secret, id, "admin", jti, tokenTTL)
	if rec := stepUpHTTP(a, "GET", "/api/admin/backup", mismatched, proof, ""); rec.Code != 401 {
		t.Fatalf("wrong session owner: %d", rec.Code)
	}
	// Ordinary users can confirm their own credential resets, never admin scopes.
	uid, err := a.st.CreateUser(store.NewUser{Username: "ordinary", PasswordHash: u.PasswordHash, Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	ordinary, _ := stepUpTestLogin(t, a, uid, "ordinary-device")
	stepUpTestProof(t, a, ordinary, stepUpSubscription)
	rec := stepUpHTTP(a, "POST", "/api/user/reauth", ordinary, "", `{"password":"`+stepUpTestPassword+`","scope":"admin:backup"}`)
	if rec.Code != 403 {
		t.Fatalf("user admin scope: %d", rec.Code)
	}
}

func TestStepUpInvalidatedByPasswordLogoutAndRevocation(t *testing.T) {
	for _, action := range []string{"password", "logout", "revoke", "password-admin-reset", "banned", "demoted"} {
		t.Run(action, func(t *testing.T) {
			a, token, jti := newStepUpTestAPI(t)
			proof := stepUpTestProof(t, a, token, stepUpBackup)
			want := 403
			switch action {
			case "password":
				rec := stepUpHTTP(a, "POST", "/api/user/password", token, "", `{"old_password":"`+stepUpTestPassword+`","new_password":"changed-password"}`)
				if rec.Code != 200 {
					t.Fatalf("change: %d %s", rec.Code, rec.Body.String())
				}
				if !a.st.SessionValid(jti) {
					t.Fatal("current login should survive password change")
				}
			case "logout":
				rec := stepUpHTTP(a, "POST", "/api/auth/logout", token, "", "")
				if rec.Code != 200 {
					t.Fatal(rec.Body.String())
				}
				want = 401
			case "revoke":
				sessions, err := a.st.ListSessions(1)
				if err != nil || len(sessions) != 1 {
					t.Fatal("session missing")
				}
				rec := stepUpHTTP(a, "POST", "/api/user/sessions/"+itoa(sessions[0].ID)+"/revoke", token, "", "")
				if rec.Code != 200 {
					t.Fatal(rec.Body.String())
				}
				want = 401
			case "password-admin-reset":
				hash, _ := auth.HashPassword("reset-password")
				if err := a.st.UpdatePassword(1, hash); err != nil {
					t.Fatal(err)
				}
			case "banned":
				if _, err := a.st.DB().Exec(`UPDATE users SET status='banned' WHERE id=1`); err != nil {
					t.Fatal(err)
				}
			case "demoted":
				if _, err := a.st.DB().Exec(`UPDATE users SET role='user' WHERE id=1`); err != nil {
					t.Fatal(err)
				}
			}
			rec := stepUpHTTP(a, "GET", "/api/admin/backup", token, proof, "")
			if rec.Code != want {
				t.Fatalf("old proof after %s: %d", action, rec.Code)
			}
		})
	}
}

func TestStepUpPasswordFailuresAndLimits(t *testing.T) {
	a, token, _ := newStepUpTestAPI(t)
	a.reauthUserRL = newRateLimiter(2, 10*time.Minute)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "/api/user/reauth", strings.NewReader(`{"password":"wrong","scope":"admin:backup"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.RemoteAddr = "192.0.2." + itoa(int64(i+1)) + ":1000"
		rec := httptest.NewRecorder()
		a.Router().ServeHTTP(rec, req)
		want := 403
		if i == 2 {
			want = 429
		}
		if rec.Code != want {
			t.Fatalf("per-user rate limit: %d", rec.Code)
		}
		if i < 2 && !strings.Contains(rec.Body.String(), "身份验证失败") {
			t.Fatal("nonuniform error")
		}
	}
	// A second user at the same address still hits the IP limit.
	a.reauthUserRL = newRateLimiter(100, 10*time.Minute)
	a.reauthIPRL = newRateLimiter(1, 10*time.Minute)
	u, _ := a.st.UserByID(1)
	uid, err := a.st.CreateUser(store.NewUser{Username: "other", PasswordHash: u.PasswordHash, Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	token2, _ := stepUpTestLogin(t, a, uid, "second")
	for i, tok := range []string{token, token2} {
		rec := stepUpHTTP(a, "POST", "/api/user/reauth", tok, "", `{"password":"wrong","scope":"admin:backup"}`)
		want := 403
		if i == 1 {
			want = 429
		}
		if rec.Code != want {
			t.Fatalf("per-IP limit: %d", rec.Code)
		}
	}
}

func TestStepUpMachineTokenBackupCompatibility(t *testing.T) {
	a, _, _ := newStepUpTestAPI(t)
	token, _, err := a.st.CreateAPIToken("backup-fixture", []string{"backup:write"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rec := stepUpHTTP(a, "GET", "/api/admin/backup", token, "", ""); rec.Code != 200 {
		t.Fatalf("legacy backup scope: %d", rec.Code)
	}
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/user/reauth"}, {"POST", "/api/admin/certs/1/export"}, {"POST", "/api/admin/update/apply"}, {"POST", "/api/admin/update/rollback"}, {"GET", "/api/admin/update/snapshots/x/download"}, {"PUT", "/api/admin/servers/1"},
	} {
		if rec := stepUpHTTP(a, tc.method, tc.path, token, "", `{}`); rec.Code != 403 {
			t.Fatalf("machine token %s: %d", tc.path, rec.Code)
		}
	}
}

func TestStepUpTLSListRedactsSecretsAndEditPreservesFullRow(t *testing.T) {
	a, token, _ := newStepUpTestAPI(t)
	cert, key, err := singbox.GenerateSelfSignedCert("fixture.invalid", 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(J{"enabled": true, "server_name": "fixture.invalid", "certificate": cert, "key": key, "unknown_option": J{"key": "public-extension-field", "count": json.Number("9007199254740993")}})
	tlsID, err := a.st.SaveSbTls(&store.SbTls{Name: "legacy", Mode: "tls", ServerJSON: string(raw), ClientJSON: `{"unknown_client":{"keep":true},"utls":{"unknown_nested":9007199254740993}}`})
	if err != nil {
		t.Fatal(err)
	}
	realityID, err := a.st.SaveSbTls(&store.SbTls{Name: "reality", Mode: "reality", ServerJSON: `{"enabled":true,"server_name":"fixture.invalid","reality":{"private_key":"reality-private-fixture","short_id":["1234"],"unknown_reality":"keep","handshake":{"server":"old.invalid","unknown_handshake":9007199254740993}},"unknown_option":"preserve"}`, ClientJSON: `{"unknown_client":"preserve"}`})
	if err != nil {
		t.Fatal(err)
	}
	rec := stepUpHTTP(a, "GET", "/api/admin/sb/tls", token, "", "")
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "PRIVATE KEY") || strings.Contains(rec.Body.String(), "reality-private-fixture") {
		t.Fatal("list leaked private key")
	}
	if !strings.Contains(rec.Body.String(), "9007199254740993") || !strings.Contains(rec.Body.String(), "public-extension-field") {
		t.Fatal("redaction altered unknown extension fields")
	}
	exportProof := stepUpTestProof(t, a, token, stepUpCertExport)
	for _, id := range []int64{tlsID, realityID} {
		rec = stepUpHTTP(a, "POST", "/api/admin/sb/tls/"+itoa(id)+"/export", token, exportProof, "")
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
		var envelope struct {
			Data store.SbTls `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		stored, _ := a.st.GetSbTls(id)
		if envelope.Data.ServerJSON != stored.ServerJSON || envelope.Data.ClientJSON != stored.ClientJSON {
			t.Fatal("export reshaped complete editor data")
		}
	}
	settingsProof := stepUpTestProof(t, a, token, stepUpSettings)
	// Structured edit intentionally omits the original private key. Both the key
	// and options unknown to the form must survive; no masked placeholder is saved.
	rec = stepUpHTTP(a, "PUT", "/api/admin/sb/tls/cert/"+itoa(tlsID), token, settingsProof, `{"name":"renamed","server_name":"fixture.invalid"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	stored, _ := a.st.GetSbTls(tlsID)
	var saved map[string]json.RawMessage
	_ = json.Unmarshal([]byte(stored.ServerJSON), &saved)
	var savedKey string
	_ = json.Unmarshal(saved["key"], &savedKey)
	if savedKey != key || !strings.Contains(stored.ServerJSON, "9007199254740993") || !strings.Contains(stored.ClientJSON, "unknown_client") || !strings.Contains(stored.ClientJSON, "9007199254740993") {
		t.Fatal("editing lost omitted key or unknown options")
	}
	rec = stepUpHTTP(a, "PUT", "/api/admin/sb/tls/reality/"+itoa(realityID), token, settingsProof, `{"name":"renamed-reality","server_name":"new.invalid"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	stored, _ = a.st.GetSbTls(realityID)
	if !strings.Contains(stored.ServerJSON, "reality-private-fixture") || !strings.Contains(stored.ServerJSON, "unknown_reality") || !strings.Contains(stored.ServerJSON, "unknown_option") || !strings.Contains(stored.ServerJSON, "9007199254740993") || !strings.Contains(stored.ClientJSON, "unknown_client") {
		t.Fatal("editing changed key or unknown Reality options")
	}
}
