package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"qingzhou/internal/auth"
	"qingzhou/internal/store"
)

// Models the authentication-center project's RS256, nonce, S256, basic client
// authentication and userinfo contract without requiring its live database.
type oauthFixture struct {
	a                *API
	st               *store.Store
	c                oauthConfig
	server           *httptest.Server
	nonce, challenge string
	subject, email   string
	verified         bool
	fault            string
	exchanges        int
}
type oauthRoundTripFunc func(*http.Request) (*http.Response, error)

func (f oauthRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	a, st := newResetSubAPI(t)
	st.SetSecretKey([]byte("test-at-rest-key"))
	t.Cleanup(a.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &oauthFixture{a: a, st: st, subject: "center-user-1", email: "alice@example.com", verified: true}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			ep := f.server.URL
			if f.fault == "foreign_endpoint" {
				ep = "https://attacker.example"
			}
			json.NewEncoder(w).Encode(J{"issuer": f.server.URL, "authorization_endpoint": ep + "/oauth/authorize", "token_endpoint": ep + "/oauth/token", "jwks_uri": f.server.URL + "/jwks", "userinfo_endpoint": f.server.URL + "/oauth/userinfo", "response_types_supported": []string{"code"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}})
		case "/jwks":
			json.NewEncoder(w).Encode(J{"keys": []J{{"kty": "RSA", "kid": "test", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/oauth/token":
			f.exchanges++
			r.ParseForm()
			id, secret, has := r.BasicAuth()
			if !has || id != f.c.ClientID || secret != f.c.ClientSecret || r.Form.Get("redirect_uri") != f.c.RedirectURL || r.Form.Get("grant_type") != "authorization_code" || oauthHash(r.Form.Get("code_verifier")) != f.challenge {
				http.Error(w, "bad exchange", 400)
				return
			}
			claims := jwt.MapClaims{"iss": f.server.URL, "aud": f.c.ClientID, "sub": f.subject, "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": f.nonce, "at_hash": accessHash("access-token")}
			switch f.fault {
			case "nonce":
				claims["nonce"] = "wrong"
			case "audience":
				claims["aud"] = "wrong"
			case "issuer":
				claims["iss"] = "https://wrong.example"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			case "at_hash":
				claims["at_hash"] = "wrong"
			}
			tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			tok.Header["kid"] = "test"
			signKey := key
			if f.fault == "signature" {
				signKey, _ = rsa.GenerateKey(rand.Reader, 2048)
			}
			signed, _ := tok.SignedString(signKey)
			json.NewEncoder(w).Encode(J{"access_token": "access-token", "token_type": "Bearer", "expires_in": 600, "id_token": signed})
		case "/oauth/userinfo":
			if r.Header.Get("Authorization") != "Bearer access-token" {
				http.Error(w, "unauthorized", 401)
				return
			}
			subject := f.subject
			if f.fault == "subject" {
				subject = "different-user"
			}
			json.NewEncoder(w).Encode(J{"sub": subject, "name": "Admin From Center", "role": "admin", "email": f.email, "email_verified": f.verified})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	a.sourceClient = f.server.Client()
	f.c = oauthConfig{Enabled: true, Name: "认证中心", Issuer: f.server.URL, ClientID: "qingzhou", ClientSecret: "client-secret", RedirectURL: "https://panel.example/api/auth/oauth2/callback", AutoRegister: true}
	f.save(t)
	st.SetSetting("register_mode", "open")
	st.SetSetting("email_verify_required", "1")
	return f
}
func accessHash(s string) string {
	h := oauthHash(s)
	b, _ := base64.RawURLEncoding.DecodeString(h)
	return base64.RawURLEncoding.EncodeToString(b[:16])
}
func (f *oauthFixture) save(t *testing.T) {
	t.Helper()
	b, _ := json.Marshal(f.c)
	if err := f.st.SetSetting("oauth2_config", string(b)); err != nil {
		t.Fatal(err)
	}
}
func (f *oauthFixture) start(t *testing.T) (string, *http.Cookie) {
	t.Helper()
	r := httptest.NewRequest("POST", "https://panel.example/api/auth/oauth2/start", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.a.handleOAuthStart(w, r)
	if w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			URL string `json:"authorization_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(body.Data.URL)
	q := u.Query()
	f.nonce = q.Get("nonce")
	f.challenge = q.Get("code_challenge")
	if q.Get("code_challenge_method") != "S256" || q.Get("scope") != "openid profile email" || q.Get("response_type") != "code" || f.nonce == "" {
		t.Fatalf("bad authorization request %s", u)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe state cookie")
	}
	return q.Get("state"), cookies[0]
}
func (f *oauthFixture) callback(state string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", f.c.RedirectURL+"?code=single-use-code&state="+url.QueryEscape(state), nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.a.handleOAuthCallback(w, r)
	return w
}
func TestOAuthLoginAndReplay(t *testing.T) {
	f := newOAuthFixture(t)
	state, cookie := f.start(t)
	w := f.callback(state, cookie)
	if w.Code != 303 || strings.Contains(w.Header().Get("Location"), "error=") {
		t.Fatalf("callback %d %s", w.Code, w.Header().Get("Location"))
	}
	var login *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			login = c
		}
	}
	if login == nil {
		t.Fatal("no login cookie")
	}
	claims, err := auth.Parse(f.a.secret, login.Value)
	if err != nil {
		t.Fatal(err)
	}
	u, err := f.st.UserByID(claims.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != "user" || !u.EmailVerified || u.Email.String != f.email || !u.ClientUUID.Valid {
		t.Fatalf("bad provisioned user %+v", u)
	}
	mapping, _ := f.st.OAuthIdentityForUser(u.ID, f.c.Issuer)
	if mapping == nil || !mapping.Provisioned {
		t.Fatal("missing durable binding")
	}
	if !strings.Contains(f.callback(state, cookie).Header().Get("Location"), "error=state") || f.exchanges != 1 {
		t.Fatal("replayed authorization code")
	}
	f.c.AutoRegister = false
	f.save(t)
	state, cookie = f.start(t)
	w = f.callback(state, cookie)
	if strings.Contains(w.Header().Get("Location"), "error=") {
		t.Fatal("existing mapping must login when auto-registration disabled")
	}
}

// Tabs share a cookie jar. Starting or completing one login must not replace
// the browser proof for another in-flight login (issue #51).
func TestOAuthOverlappingLogins(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint("reverse=", reverse), func(t *testing.T) {
			f := newOAuthFixture(t)
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			panel, _ := url.Parse(f.c.RedirectURL)
			type flow struct{ state, nonce, challenge string }
			var flows []flow
			for i := 0; i < 2; i++ {
				state, cookie := f.start(t)
				jar.SetCookies(panel, []*http.Cookie{cookie})
				flows = append(flows, flow{state, f.nonce, f.challenge})
			}
			if reverse {
				flows[0], flows[1] = flows[1], flows[0]
			}
			for _, flow := range flows {
				f.nonce, f.challenge = flow.nonce, flow.challenge
				w := f.callback(flow.state, jar.Cookies(panel)...)
				if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "https://panel.example/#/oauth2/callback" {
					t.Fatalf("overlapping login: %d %s", w.Code, w.Header().Get("Location"))
				}
				jar.SetCookies(panel, w.Result().Cookies())
			}
			if f.exchanges != 2 {
				t.Fatalf("exchanges = %d, want 2", f.exchanges)
			}
			for _, cookie := range jar.Cookies(panel) {
				if strings.HasPrefix(cookie.Name, oauthCookie) {
					t.Fatal("completed login left a browser proof cookie")
				}
			}
		})
	}
}

func TestOAuthLegacyStateCookie(t *testing.T) {
	f := newOAuthFixture(t)
	state, cookie := f.start(t)
	// Older releases used one fixed name; an in-flight flow must survive an
	// upgrade without accepting a different browser proof or a replay.
	cookie.Name = oauthCookie
	wrong := *cookie
	wrong.Value = strings.Repeat("x", 43)
	if loc := f.callback(state, &wrong).Header().Get("Location"); !strings.Contains(loc, "error=state") {
		t.Fatalf("accepted wrong legacy proof: %s", loc)
	}
	w := f.callback(state, cookie)
	if loc := w.Header().Get("Location"); loc != "https://panel.example/#/oauth2/callback" {
		t.Fatalf("legacy callback: %s", loc)
	}
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == oauthCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("legacy proof cookie was not cleared")
	}
	if loc := f.callback(state, cookie).Header().Get("Location"); !strings.Contains(loc, "error=state") || f.exchanges != 1 {
		t.Fatalf("accepted legacy replay: %s", loc)
	}
}
func TestOAuthRejectsUntrustedClaims(t *testing.T) {
	for _, fault := range []string{"nonce", "audience", "issuer", "expired", "signature", "at_hash", "subject"} {
		t.Run(fault, func(t *testing.T) {
			f := newOAuthFixture(t)
			state, cookie := f.start(t)
			f.fault = fault
			w := f.callback(state, cookie)
			if !strings.Contains(w.Header().Get("Location"), "error=provider") {
				t.Fatalf("accepted %s: %s", fault, w.Header().Get("Location"))
			}
			for _, c := range w.Result().Cookies() {
				if c.Name == cookieName {
					t.Fatal("issued login on invalid claims")
				}
			}
		})
	}
}
func TestOAuthStateAndPolicy(t *testing.T) {
	for _, mode := range []string{"missing_cookie", "wrong_cookie", "other_flow_cookie", "changed_config", "closed", "unverified", "email_collision", "banned"} {
		t.Run(mode, func(t *testing.T) {
			f := newOAuthFixture(t)
			if mode == "closed" {
				f.st.SetSetting("register_mode", "code")
			}
			if mode == "unverified" {
				f.verified = false
			}
			if mode == "email_collision" {
				_, err := f.st.CreateUser(store.NewUser{Username: "existing", Email: f.email, PasswordHash: "!unset", Role: "admin"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "banned" {
				state, cookie := f.start(t)
				f.callback(state, cookie)
				f.st.DB().Exec(`UPDATE users SET status='banned'`)
			}
			state, cookie := f.start(t)
			expected := "state"
			switch mode {
			case "missing_cookie":
				cookie = &http.Cookie{Name: "other", Value: "other"}
			case "wrong_cookie":
				cookie.Value = strings.Repeat("x", 43)
			case "other_flow_cookie":
				_, cookie = f.start(t)
			case "changed_config":
				f.c.Name = "changed"
				f.save(t)
			case "closed", "unverified":
				expected = "registration"
			case "email_collision":
				expected = "email_exists"
			case "banned":
				expected = "account"
			}
			w := f.callback(state, cookie)
			if !strings.Contains(w.Header().Get("Location"), "error="+expected) {
				t.Fatalf("expected %s got %s", expected, w.Header().Get("Location"))
			}
		})
	}
}
func TestOAuthConfigurationAndTransport(t *testing.T) {
	f := newOAuthFixture(t)
	w := httptest.NewRecorder()
	f.a.handleGetOAuth(w, httptest.NewRequest("GET", "/", nil))
	if strings.Contains(w.Body.String(), f.c.ClientSecret) || !strings.Contains(w.Body.String(), "***") {
		t.Fatal("secret exposed")
	}
	var raw string
	f.st.DB().QueryRow(`SELECT value FROM settings WHERE key='oauth2_config'`).Scan(&raw)
	if !strings.HasPrefix(raw, "enc:v1:") || strings.Contains(raw, f.c.ClientSecret) {
		t.Fatal("secret not encrypted")
	}
	for _, bad := range []string{"http://issuer.example", "https://issuer.example?foo=1", "https://user:pass@issuer.example"} {
		c := f.c
		c.Issuer = bad
		if c.validate() == nil {
			t.Fatal("accepted unsafe issuer")
		}
	}
	f.fault = "foreign_endpoint"
	if _, _, _, err := f.a.discoverOAuth(context.Background(), f.c); err == nil {
		t.Fatal("accepted foreign token endpoint")
	}
	f.fault = ""
	req := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
	w = httptest.NewRecorder()
	f.a.handleOAuthStart(w, req)
	if w.Code != 415 {
		t.Fatal("accepted form-compatible CSRF")
	}
	// Generic settings cannot overwrite a validated OAuth configuration.
	w = httptest.NewRecorder()
	f.a.handlePutSettings(w, httptest.NewRequest("PUT", "/", strings.NewReader(`{"oauth2_config":"attacker"}`)))
	after, _ := f.a.oauthConfig()
	if after != f.c {
		t.Fatal("generic settings overwrote OAuth")
	}
	// A stream may not hide an oversized body behind an otherwise valid prefix.
	tr := oauthTransport{base: oauthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (1<<20)+1)))}, nil
	}), origin: "https://issuer.example"}
	r := httptest.NewRequest("GET", "https://issuer.example/jwks", nil)
	if _, err := tr.RoundTrip(r); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestOAuthExplicitBinding(t *testing.T) {
	for _, mode := range []string{"success", "password", "session", "already_bound"} {
		t.Run(mode, func(t *testing.T) {
			f := newOAuthFixture(t)
			hash, _ := auth.HashPassword("local-password")
			uid, err := f.st.CreateUser(store.NewUser{Username: "local", PasswordHash: hash, Role: "admin"})
			if err != nil {
				t.Fatal(err)
			}
			f.st.CreateSession(uid, "local-session", "test", "test")
			token, _ := auth.Issue(f.a.secret, uid, "admin", "local-session", time.Hour)
			pw := "local-password"
			if mode == "password" {
				pw = "incorrect"
			}
			r := httptest.NewRequest("POST", "https://panel.example/api/user/oauth2/bind", strings.NewReader(`{"password":"`+pw+`"}`))
			r.Header.Set("Content-Type", "application/json")
			ctx := context.WithValue(r.Context(), ctxUserID, uid)
			ctx = context.WithValue(ctx, ctxJti, "local-session")
			r = r.WithContext(ctx)
			w := httptest.NewRecorder()
			f.a.handleOAuthBind(w, r)
			if mode == "password" {
				if w.Code != 403 {
					t.Fatal("accepted wrong local password")
				}
				return
			}
			if w.Code != 200 {
				t.Fatalf("start bind %d %s", w.Code, w.Body.String())
			}
			var body struct {
				Data struct {
					URL string `json:"authorization_url"`
				} `json:"data"`
			}
			json.Unmarshal(w.Body.Bytes(), &body)
			u, _ := url.Parse(body.Data.URL)
			q := u.Query()
			f.nonce = q.Get("nonce")
			f.challenge = q.Get("code_challenge")
			if mode == "session" {
				token, _ = auth.Issue(f.a.secret, uid, "admin", "different-session", time.Hour)
			}
			if mode == "already_bound" {
				other, _ := f.st.CreateUser(store.NewUser{Username: "other", PasswordHash: hash, Role: "user"})
				if err = f.st.BindOAuthIdentity(context.Background(), other, f.c.Issuer, f.subject); err != nil {
					t.Fatal(err)
				}
			}
			result := f.callback(q.Get("state"), w.Result().Cookies()[0], &http.Cookie{Name: cookieName, Value: token})
			loc := result.Header().Get("Location")
			expected := "bound=1"
			if mode == "session" {
				expected = "error=session"
			}
			if mode == "already_bound" {
				expected = "error=binding"
			}
			if !strings.Contains(loc, expected) {
				t.Fatalf("expected %s got %s", expected, loc)
			}
			binding, _ := f.st.OAuthIdentityForUser(uid, f.c.Issuer)
			if mode == "success" && (binding == nil || !binding.Provisioned) {
				t.Fatal("binding missing")
			}
			if mode != "success" && binding != nil {
				t.Fatal("binding changed on failure")
			}
		})
	}
}

func TestOAuthAdminLivePermissionsAndAtomicConfig(t *testing.T) {
	f := newOAuthFixture(t)
	uid, err := f.st.CreateUser(store.NewUser{Username: "operator", PasswordHash: "!unset", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	f.st.CreateSession(uid, "admin-session", "test", "test")
	token, _ := auth.Issue(f.a.secret, uid, "admin", "admin-session", time.Hour)
	router := f.a.Router()
	request := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://panel.example/api/admin/oauth2", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", ""); w.Code != 200 {
		t.Fatalf("admin denied %d", w.Code)
	}
	c := f.c
	c.ClientSecret = "***"
	c.Name = "保存测试"
	b, _ := json.Marshal(c)
	if w := request("PUT", string(b)); w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}
	saved, _ := f.a.oauthConfig()
	if saved.ClientSecret != f.c.ClientSecret || saved.Name != c.Name {
		t.Fatal("masked secret not preserved")
	}
	c.Issuer = "http://unsafe.example"
	b, _ = json.Marshal(c)
	if w := request("PUT", string(b)); w.Code != 400 {
		t.Fatal("invalid config accepted")
	}
	after, _ := f.a.oauthConfig()
	if after != saved {
		t.Fatal("invalid config changed stored settings")
	}
	f.st.DB().Exec(`UPDATE users SET role='user' WHERE id=?`, uid)
	if w := request("GET", ""); w.Code != 403 {
		t.Fatal("demoted admin retained access")
	}
	f.st.DB().Exec(`UPDATE users SET role='admin',status='banned' WHERE id=?`, uid)
	if w := request("PUT", string(b)); w.Code != 403 {
		t.Fatal("banned admin retained access")
	}
}
