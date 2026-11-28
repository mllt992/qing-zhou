package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/auth"
	"qingzhou/internal/store"
)

// ---- 忘记密码 ----

// With no SMTP the link can only reach the server log, so "邮件已发送" was a
// flat lie and the user waited for mail that never existed. Say so instead.
func TestForgot_SaysSoWhenMailIsNotConfigured(t *testing.T) {
	a, st := newUserEditAPI(t)
	if _, err := st.CreateUser(store.NewUser{Username: "u1", Email: "u1@example.com", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	a.handleForgot(w, httptest.NewRequest("POST", "/api/auth/forgot",
		strings.NewReader(`{"email":"u1@example.com"}`)))

	if w.Code != 503 {
		t.Fatalf("status %d, want 503 when the panel cannot send mail: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "已发送") {
		t.Error("still claims a mail was sent with no mailer configured")
	}
	// And no token was minted — there is nothing that could deliver it.
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM email_tokens WHERE purpose='reset'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("minted %d undeliverable reset token(s)", n)
	}
}

// Per-address throttle. The route's per-IP limiter does not protect the person
// being mailed: the victim is the address, and an attacker rotates IPs freely.
func TestForgot_ThrottlesPerAddress(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting("smtp_host", "smtp.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(store.NewUser{Username: "u1", Email: "u1@example.com", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}

	codes := make([]int, 0, 5)
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		a.handleForgot(w, httptest.NewRequest("POST", "/api/auth/forgot",
			strings.NewReader(`{"email":"u1@example.com"}`)))
		codes = append(codes, w.Code)
	}
	// resendRL is 3 per 10 minutes.
	for i := 0; i < 3; i++ {
		if codes[i] != 200 {
			t.Fatalf("attempt %d = %d, want the first three to go through", i+1, codes[i])
		}
	}
	if codes[3] != 429 || codes[4] != 429 {
		t.Fatalf("attempts 4/5 = %d/%d, want 429 — the address is not throttled", codes[3], codes[4])
	}
}

// The throttle must not become the enumeration oracle that the uniform success
// message exists to prevent: an unregistered address has to behave identically.
func TestForgot_ThrottleDoesNotRevealWhoIsRegistered(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting("smtp_host", "smtp.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(store.NewUser{Username: "u1", Email: "known@example.com", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}

	probe := func(email string) []string {
		var out []string
		for i := 0; i < 5; i++ {
			w := httptest.NewRecorder()
			a.handleForgot(w, httptest.NewRequest("POST", "/api/auth/forgot",
				strings.NewReader(`{"email":"`+email+`"}`)))
			var resp struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
				Data struct {
					Message string `json:"message"`
				} `json:"data"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			out = append(out, strconv.Itoa(w.Code)+"|"+resp.Msg+resp.Data.Message)
		}
		return out
	}

	known := probe("known@example.com")
	unknown := probe("nobody@example.com")
	for i := range known {
		if known[i] != unknown[i] {
			t.Fatalf("attempt %d differs: registered=%q unregistered=%q — this tells an attacker which addresses exist",
				i+1, known[i], unknown[i])
		}
	}
}

// ---- 修改密码 ----

// The old-password check is the re-authentication gate in front of a takeover:
// a stolen session already passes auth, so its holder only needs this secret,
// and succeeding logs the real owner out. It must not be free to guess.
func TestChangePassword_ThrottlesOldPasswordGuesses(t *testing.T) {
	a, st := newUserEditAPI(t)
	hash, err := auth.HashPassword("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(store.NewUser{Username: "u1", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}

	var got429 bool
	for i := 0; i < 8; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/user/password",
			strings.NewReader(`{"old_password":"guess","new_password":"whatever123"}`))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, uid))
		a.handleChangePassword(w, req)
		if w.Code == 429 {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("eight wrong old-password guesses in a row all got a straight answer — the takeover gate is unthrottled")
	}

	// And the password really was not changed by any of that.
	u, _ := st.UserByID(uid)
	if !auth.CheckPassword(u.PasswordHash, "correct-horse") {
		t.Error("password changed despite every attempt using the wrong old password")
	}
}

// The limit is per user, so one throttled account cannot lock out another.
func TestChangePassword_ThrottleIsPerUser(t *testing.T) {
	a, st := newUserEditAPI(t)
	hash, _ := auth.HashPassword("pw-one-two")
	victim, _ := st.CreateUser(store.NewUser{Username: "victim", PasswordHash: hash})
	other, _ := st.CreateUser(store.NewUser{Username: "other", PasswordHash: hash})

	for i := 0; i < 8; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/user/password",
			strings.NewReader(`{"old_password":"guess","new_password":"whatever123"}`))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, victim))
		a.handleChangePassword(w, req)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/user/password",
		strings.NewReader(`{"old_password":"pw-one-two","new_password":"brand-new-1"}`))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, other))
	a.handleChangePassword(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d — hammering one account blocked a different user: %s", w.Code, w.Body.String())
	}
}

// ---- /api/config ----

// The login dialog decides whether 找回密码 is a real offer from this flag.
func TestConfig_ReportsWhetherMailWorks(t *testing.T) {
	a, st := newUserEditAPI(t)

	read := func() bool {
		w := httptest.NewRecorder()
		a.handleConfig(w, httptest.NewRequest("GET", "/api/config", nil))
		var resp struct {
			Data struct {
				EmailEnabled bool `json:"email_enabled"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Data.EmailEnabled
	}

	if read() {
		t.Error("email_enabled is true with no SMTP host set")
	}
	if err := st.SetSetting("smtp_host", "smtp.example.com"); err != nil {
		t.Fatal(err)
	}
	if !read() {
		t.Error("email_enabled is still false after SMTP was configured")
	}
}

// Delivery must not run on the request goroutine. Against an SMTP host that
// blackholes packets, Mailer.Send sits for its full 20s timeout — and 忘记密码
// is a public, unauthenticated endpoint. The bound below is deliberately loose:
// it is not measuring speed, it is distinguishing "returned" from "waited for
// the SMTP timeout".
func TestForgot_DoesNotBlockOnAnUnreachableSMTPHost(t *testing.T) {
	a, st := newUserEditAPI(t)
	// TEST-NET-1: routable nowhere, so the dial hangs rather than being refused.
	if err := st.SetSetting("smtp_host", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(store.NewUser{Username: "u1", Email: "u1@example.com", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	w := httptest.NewRecorder()
	a.handleForgot(w, httptest.NewRequest("POST", "/api/auth/forgot",
		strings.NewReader(`{"email":"u1@example.com"}`)))
	elapsed := time.Since(start)

	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if elapsed > 5*time.Second {
		t.Fatalf("handler took %s — it is waiting on the SMTP send instead of handing it off", elapsed)
	}
}

// The address reaches a rate-limiter key and a log line, both from an
// unauthenticated request. CR/LF in it would forge log entries.
func TestForgot_RejectsMalformedAddresses(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting("smtp_host", "smtp.example.com"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`not-an-address`,
		`a@b.com\n2026/01/01 12:00:00 forged log line`,
		`"a@b.com` + "\r\n" + `injected"`,
	} {
		w := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"email": bad})
		a.handleForgot(w, httptest.NewRequest("POST", "/api/auth/forgot", strings.NewReader(string(body))))
		if w.Code != 400 {
			t.Errorf("email %q got status %d, want 400", bad, w.Code)
		}
	}
}

func TestEmailRebindRequiresPasswordAndMailbox(t *testing.T) {
	a, st := newUserEditAPI(t)
	t.Setenv("QZ_SMTP_HOST", "127.0.0.1")
	t.Setenv("QZ_SMTP_PORT", "1")
	hash, err := auth.HashPassword("existing-password")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(store.NewUser{Username: "u", Email: "original@example.com", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetUserClient(uid, 0, "qz_u", "existing-uuid", "existing-secret"); err != nil {
		t.Fatal(err)
	}
	if err = st.SetEmailVerified(uid); err != nil {
		t.Fatal(err)
	}
	if err = st.CreateSession(uid, "session", "local", "test"); err != nil {
		t.Fatal(err)
	}
	bearer, err := auth.Issue(a.secret, uid, "user", "session", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	post := func(data string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/user/email", strings.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		a.Router().ServeHTTP(w, req)
		return w
	}
	if w := post(`{"email":"new@example.com","password":"wrong"}`); w.Code != 403 {
		t.Fatal(w.Code)
	}
	var pending int
	if err = st.DB().QueryRow("SELECT COUNT(*) FROM email_tokens").Scan(&pending); err != nil || pending != 0 {
		t.Fatal(pending, err)
	}
	if w := post(`{"email":"new@example.com","password":"existing-password"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	u, err := st.UserByID(uid)
	if err != nil || u.Email.String != "original@example.com" || !u.EmailVerified {
		t.Fatal("address changed before confirmation")
	}
	var token string
	if err = st.DB().QueryRow("SELECT token FROM email_tokens WHERE user_id=? AND address=? AND purpose='rebind'", uid, "new@example.com").Scan(&token); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.Router().ServeHTTP(w, httptest.NewRequest("GET", "/confirm-email?token="+token, nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	u, err = st.UserByID(uid)
	if err != nil || u.Email.String != "original@example.com" {
		t.Fatal("link preview changed mailbox")
	}
	req := httptest.NewRequest("POST", "/confirm-email", strings.NewReader("token="+token))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	a.Router().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	u, err = st.UserByID(uid)
	if err != nil || u.Email.String != "new@example.com" || !u.EmailVerified {
		t.Fatal("new address not confirmed")
	}
}
