package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qingzhou/internal/store"
)

func adminReq(method, body string) *http.Request {
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	ctx := context.WithValue(req.Context(), ctxRole, "admin")
	return req.WithContext(ctx)
}

func setupData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return body.Data
}

func TestSetupPreflight(t *testing.T) {
	a, st := newUserEditAPI(t)
	old := acmeInstalledFn
	acmeInstalledFn = func(context.Context) bool { return true }
	t.Cleanup(func() { acmeInstalledFn = old })
	_ = st.SetSetting("cf_api_token", "tok")
	_ = st.SetSetting("acme_email", "a@b.co")

	w := httptest.NewRecorder()
	a.handleAdminSetupPreflight(w, adminReq("GET", ""))
	d := setupData(t, w)
	if d["cf_configured"] != true || d["acme_installed"] != true || d["acme_email"] != "a@b.co" {
		t.Fatalf("%v", d)
	}
	if strings.Contains(w.Body.String(), "tok\"") {
		t.Fatalf("token leaked: %s", w.Body.String())
	}

	// not an admin 鈫?refused
	w = httptest.NewRecorder()
	a.handleAdminSetupPreflight(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("code %d", w.Code)
	}
}

func TestSetupAcmeInstall(t *testing.T) {
	a, st := newUserEditAPI(t)
	oldI, oldC := acmeInstallFn, acmeInstalledFn
	var gotEmail string
	acmeInstallFn = func(_ context.Context, email string) error { gotEmail = email; return nil }
	acmeInstalledFn = func(context.Context) bool { return true }
	t.Cleanup(func() { acmeInstallFn, acmeInstalledFn = oldI, oldC })

	w := httptest.NewRecorder()
	a.handleAdminSetupAcmeInstall(w, adminReq("POST", `{"email":"me@example.com"}`))
	if w.Code != http.StatusOK || gotEmail != "me@example.com" {
		t.Fatalf("code=%d email=%q %s", w.Code, gotEmail, w.Body.String())
	}
	if v, _ := st.GetSetting("acme_email"); v != "me@example.com" {
		t.Fatalf("email not remembered: %q", v)
	}

	// shell metacharacters never reach the install command
	w = httptest.NewRecorder()
	a.handleAdminSetupAcmeInstall(w, adminReq("POST", `{"email":"a@b.co;rm -rf /"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unsafe email must be rejected, got %d", w.Code)
	}

	acmeInstallFn = func(context.Context, string) error { return errors.New("need curl or wget") }
	w = httptest.NewRecorder()
	a.handleAdminSetupAcmeInstall(w, adminReq("POST", `{}`))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "curl") {
		t.Fatalf("install failure must surface: %d %s", w.Code, w.Body.String())
	}
}

func TestSetupDNSCheck(t *testing.T) {
	a, st := newUserEditAPI(t)
	sid, err := st.CreateServer(store.Server{Name: "n", Host: "217.216.188.164", HostV6: "2402:1920:fc:af::2"})
	if err != nil {
		t.Fatal(err)
	}
	old := lookupHostFn
	t.Cleanup(func() { lookupHostFn = old })
	run := func(addrs []string, lookupErr error) map[string]any {
		lookupHostFn = func(context.Context, string) ([]string, error) { return addrs, lookupErr }
		w := httptest.NewRecorder()
		a.handleAdminSetupDNSCheck(w, adminReq("POST", `{"domain":"https://VPS-Taxi.mllt.vip/","server_id":`+itoa(sid)+`}`))
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		return setupData(t, w)
	}

	if d := run([]string{"217.216.188.164"}, nil); d["status"] != "ok" || d["domain"] != "vps-taxi.mllt.vip" || d["v6_ok"] != false {
		t.Fatalf("A only: ipv6 missing is advisory, v4 ok: %v", d)
	}
	if d := run([]string{"217.216.188.164", "2402:1920:fc:af::2"}, nil); d["status"] != "ok" || d["v6_ok"] != true {
		t.Fatalf("%v", d)
	}
	if d := run([]string{"1.2.3.4"}, nil); d["status"] != "mismatch" {
		t.Fatalf("%v", d)
	}
	if d := run(nil, errors.New("no such host")); d["status"] != "unresolved" {
		t.Fatalf("%v", d)
	}

	w := httptest.NewRecorder()
	a.handleAdminSetupDNSCheck(w, adminReq("POST", `{"domain":"1.2.3.4"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("an IP is not a domain: %d", w.Code)
	}
}
