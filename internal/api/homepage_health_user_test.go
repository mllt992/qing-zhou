package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/auth"
	"qingzhou/internal/store"
)

// The homepage health strip for non-admins goes through the real router so the
// auth wiring (guest 401, user gated on the setting, admin path unchanged) is
// locked in, not just the handler.
func TestUserHealthTimelineGatedBySetting(t *testing.T) {
	a, st := newUserEditAPI(t)
	hash, err := auth.HashPassword("secret1")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []store.NewUser{
		{Username: "alice", PasswordHash: hash, SubToken: "tok-alice", ExpiryAt: time.Now().Unix() + 86400},
		{Username: "boss", PasswordHash: hash, SubToken: "tok-boss", Role: "admin", ExpiryAt: time.Now().Unix() + 86400},
	} {
		if _, err := st.CreateUser(u); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateServer(store.Server{Name: "公开机", Host: "203.0.113.7", ProbeEnabled: true, PublicVisible: true}); err != nil {
		t.Fatal(err)
	}
	hid, err := st.CreateServer(store.Server{Name: "隐藏机", Host: "203.0.113.8", ProbeEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := st.GetServer(hid)
	if err != nil {
		t.Fatal(err)
	}
	hidden.PublicVisible = false
	if err := st.UpdateServer(*hidden); err != nil {
		t.Fatal(err)
	}
	token := func(user string) string {
		w := loginAs(a, user, "secret1")
		var resp struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Data.Token == "" {
			t.Fatalf("login %s: %d %s", user, w.Code, w.Body.String())
		}
		return resp.Data.Token
	}
	userTok, adminTok := token("alice"), token("boss")
	now := time.Now().Unix()
	q := "?from=" + itoa(now-3600) + "&to=" + itoa(now)
	get := func(path, tok string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path+q, nil)
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		a.Router().ServeHTTP(w, r)
		return w
	}
	const userPath = "/api/user/health-timeline"
	const adminPath = "/api/admin/monitor/health-timeline"

	// Setting off (default): nobody gets the user copy; admin page unaffected.
	if w := get(userPath, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("guest off = %d", w.Code)
	}
	for _, tok := range []string{userTok, adminTok} {
		if w := get(userPath, tok); w.Code != http.StatusNotFound {
			t.Fatalf("user path with setting off = %d %s", w.Code, w.Body.String())
		}
	}
	if w := get(adminPath, adminTok); w.Code != http.StatusOK {
		t.Fatalf("admin monitor page must not depend on the setting: %d", w.Code)
	}

	if err := st.SetSettingBool("homepage_machine_health", true); err != nil {
		t.Fatal(err)
	}
	if w := get(userPath, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("guest on = %d, guests stay excluded", w.Code)
	}
	if w := get(adminPath, userTok); w.Code != http.StatusForbidden {
		t.Fatalf("normal user on admin path = %d", w.Code)
	}
	w := get(userPath, userTok)
	if w.Code != http.StatusOK {
		t.Fatalf("user on = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "公开机") {
		t.Fatalf("public machine missing: %s", body)
	}
	for _, leak := range []string{"隐藏机", "203.0.113", "host", "ssh", "本机"} {
		if strings.Contains(body, leak) {
			t.Fatalf("user payload leaked %q: %s", leak, body)
		}
	}
	// Admin path still lists hidden machines and the panel itself.
	if adm := get(adminPath, adminTok).Body.String(); !strings.Contains(adm, "隐藏机") {
		t.Fatalf("admin strip lost hidden machine: %s", adm)
	}
}
