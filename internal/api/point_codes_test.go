package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"qingzhou/internal/auth"
	"qingzhou/internal/store"
	"strings"
	"testing"
	"time"
)

func pointCodeRequest(t *testing.T, a *API, st *store.Store, uid int64, role, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	if uid > 0 {
		jti := "point-code-test-" + itoa(uid)
		st.CreateSession(uid, jti, "127.0.0.1", "test")
		token, err := auth.Issue(a.secret, uid, role, jti, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	a.Router().ServeHTTP(w, r)
	return w
}
func TestPointCodeHTTPAdminAndUserBoundaries(t *testing.T) {
	a, st := newUserEditAPI(t)
	uid, _ := st.CreateUser(store.NewUser{Username: "redeem-user", PasswordHash: "x"})
	admin, _ := st.CreateUser(store.NewUser{Username: "redeem-admin", PasswordHash: "x", Role: "admin"})
	for _, tc := range []struct {
		uid    int64
		role   string
		status int
	}{{0, "", 401}, {uid, "user", 403}} {
		w := pointCodeRequest(t, a, st, tc.uid, tc.role, http.MethodPost, "/api/admin/point-codes/generate", `{"points":20,"count":1,"days":30}`)
		if w.Code != tc.status {
			t.Fatalf("admin gate: %d %s", w.Code, w.Body.String())
		}
	}
	w := pointCodeRequest(t, a, st, admin, "admin", http.MethodPost, "/api/admin/point-codes/generate", `{"points":20,"count":1,"days":30}`)
	if w.Code != 200 {
		t.Fatalf("generate %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Codes []store.GeneratedPointCode `json:"codes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Data.Codes) != 1 {
		t.Fatalf("generate response %s", w.Body.String())
	}
	code := response.Data.Codes[0].Code
	list := pointCodeRequest(t, a, st, admin, "admin", http.MethodGet, "/api/admin/point-codes", "")
	if list.Code != 200 || strings.Contains(list.Body.String(), code) || strings.Contains(list.Body.String(), "code_hash") {
		t.Fatalf("list leaked code %s", list.Body.String())
	}
	redeemed := pointCodeRequest(t, a, st, uid, "user", http.MethodPost, "/api/user/points/redeem", `{"code":"`+code+`"}`)
	if redeemed.Code != 200 || !strings.Contains(redeemed.Body.String(), `"balance":20`) {
		t.Fatalf("redeem %d %s", redeemed.Code, redeemed.Body.String())
	}
	again := pointCodeRequest(t, a, st, uid, "user", http.MethodPost, "/api/user/points/redeem", `{"code":"`+code+`"}`)
	if again.Code != 400 || !strings.Contains(again.Body.String(), "已使用") {
		t.Fatalf("single-use %d %s", again.Code, again.Body.String())
	}
}
func TestPointCodeHTTPLimitsAttempts(t *testing.T) {
	a, st := newUserEditAPI(t)
	uid, _ := st.CreateUser(store.NewUser{Username: "throttled", PasswordHash: "x"})
	for i := 0; i < 5; i++ {
		w := pointCodeRequest(t, a, st, uid, "user", http.MethodPost, "/api/user/points/redeem", `{"code":"bad"}`)
		want := 400
		if i == 4 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("attempt%d: %d %s", i, w.Code, w.Body.String())
		}
	}
	a.redeemRL = newRateLimiter(1, time.Hour)
	one := pointCodeRequest(t, a, st, uid, "user", http.MethodPost, "/api/user/points/redeem", `{"code":"bad"}`)
	two := pointCodeRequest(t, a, st, uid, "user", http.MethodPost, "/api/user/points/redeem", `{"code":"bad"}`)
	if one.Code != 429 || two.Code != 429 || !strings.Contains(two.Body.String(), "过于频繁") {
		t.Fatal("request limiter not applied")
	}
}
