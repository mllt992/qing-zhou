package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/auth"
	"qingzhou/internal/store"
)

func newUserEditAPI(t *testing.T) (*API, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return New(st, []byte("secret"), nil), st
}

// putUser drives handleAdminUpdateUser with {id} bound, without standing up the
// router (which would need a real admin session).
func putUser(a *API, id int64, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PUT", "/api/admin/users/"+strconv.FormatInt(id, 10), strings.NewReader(body))
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", strconv.FormatInt(id, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	w := httptest.NewRecorder()
	a.handleAdminUpdateUser(w, req)
	return w
}

// The reason this endpoint accepts an address at all: the user's own rebind
// mails a link to the NEW address, which is no help to someone who mistyped it.
func TestAdminUpdateUser_SetsEmailWithoutVerification(t *testing.T) {
	a, st := newUserEditAPI(t)
	id, err := st.CreateUser(store.NewUser{Username: "u1", Email: "typo@exmaple.com", PasswordHash: "h"})
	if err != nil {
		t.Fatal(err)
	}

	if w := putUser(a, id, `{"status":"active","email":"Fixed@Example.com "}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	u, _ := st.UserByID(id)
	if u.Email.String != "fixed@example.com" {
		t.Errorf("email = %q, want it trimmed and lowercased", u.Email.String)
	}
	if !u.EmailVerified {
		t.Error("admin-set address landed unverified")
	}
}

// Uniqueness is what makes login-by-email and password reset unambiguous; the
// admin path has to enforce the same rule registration and self-rebind do.
func TestAdminUpdateUser_RejectsEmailHeldByAnotherAccount(t *testing.T) {
	a, st := newUserEditAPI(t)
	if _, err := st.CreateUser(store.NewUser{Username: "owner", Email: "taken@example.com", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}
	id, _ := st.CreateUser(store.NewUser{Username: "u2", Email: "mine@example.com", PasswordHash: "h"})

	w := putUser(a, id, `{"status":"active","email":"taken@example.com"}`)
	if w.Code != 409 {
		t.Fatalf("status %d, want 409: %s", w.Code, w.Body.String())
	}
	u, _ := st.UserByID(id)
	if u.Email.String != "mine@example.com" {
		t.Errorf("email changed to %q despite the conflict", u.Email.String)
	}
}

// Omitting the field must not unbind the address — most saves from this modal
// only touch the ban switch or the quota.
func TestAdminUpdateUser_OmittedEmailIsLeftAlone(t *testing.T) {
	a, st := newUserEditAPI(t)
	id, _ := st.CreateUser(store.NewUser{Username: "u1", Email: "keep@example.com", PasswordHash: "h"})

	if w := putUser(a, id, `{"status":"active"}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	u, _ := st.UserByID(id)
	if u.Email.String != "keep@example.com" {
		t.Errorf("email = %q, want it untouched", u.Email.String)
	}
}

func TestAdminUpdateUser_RejectsEnabledZeroTrafficGrant(t *testing.T) {
	a, st := newUserEditAPI(t)
	id, err := st.CreateUser(store.NewUser{Username: "zero-grant", PasswordHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	w := putUser(a, id, `{"manual_enabled":true,"manual_traffic":0,"manual_expiry":0}`)
	if w.Code != 400 {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
	buckets, err := st.ListBuckets(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range buckets {
		if b.Kind == "plan" && b.PackageID == 0 {
			t.Fatalf("zero-byte admin grant was created: %+v", b)
		}
	}
}

func TestAdminUpdateUser_RejectsMalformedEmail(t *testing.T) {
	a, st := newUserEditAPI(t)
	id, _ := st.CreateUser(store.NewUser{Username: "u1", Email: "ok@example.com", PasswordHash: "h"})

	if w := putUser(a, id, `{"status":"active","email":"not-an-address"}`); w.Code != 400 {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
	u, _ := st.UserByID(id)
	if u.Email.String != "ok@example.com" {
		t.Errorf("email = %q, want it untouched", u.Email.String)
	}
}

// Nothing in the panel grants admin. A writable role field would turn a stolen
// admin token (or an XSS on an admin page) into a second admin account that
// outlives locking the first one down — see the note on the request struct.
func TestAdminUpdateUser_CannotGrantAdmin(t *testing.T) {
	a, st := newUserEditAPI(t)
	id, _ := st.CreateUser(store.NewUser{Username: "u1", PasswordHash: "h"})

	if w := putUser(a, id, `{"status":"active","role":"admin"}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	u, _ := st.UserByID(id)
	if u.Role != "user" {
		t.Fatalf("role = %q — the panel promoted a user to admin", u.Role)
	}
}

// The token is a bearer credential for the admin's GitHub account. It may be
// written through the settings API, but it must never come back out of it.
func TestSettings_GitHubTokenIsNeverReturnedInPlaintext(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting("update_github_token", "ghp_realtoken"); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	a.handleGetSettings(w, httptest.NewRequest("GET", "/api/admin/settings", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "ghp_realtoken") {
		t.Fatal("the settings response leaks the GitHub token in plaintext")
	}

	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// Masked, not omitted: the admin still has to be able to tell it is set.
	if resp.Data["update_github_token"] != "***" {
		t.Errorf("update_github_token = %v, want the *** sentinel", resp.Data["update_github_token"])
	}
}

// The token is opt-in, so emptying the box has to actually remove it. Without
// this, one pasted-wrong token fails every update check with 401 and there is no
// way back to the working anonymous path from inside the panel.
func TestSettings_EmptyingTheGitHubTokenClearsIt(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting("update_github_token", "ghp_wrongtoken"); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	a.handlePutSettings(w, httptest.NewRequest("PUT", "/api/admin/settings",
		strings.NewReader(`{"update_github_token":""}`)))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got, _ := st.GetSetting("update_github_token"); got != "" {
		t.Errorf("token = %q — clearing the field did nothing, so a bad token is unrecoverable from the panel", got)
	}
}

// Clearing stays scoped to the opt-in secret: a blank SMTP password still means
// "left blank, keep what is stored", which is what protects a required
// credential from a half-filled form.
func TestSettings_BlankSMTPPasswordStillKeepsTheStoredOne(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting("smtp_pass", "hunter2"); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	a.handlePutSettings(w, httptest.NewRequest("PUT", "/api/admin/settings",
		strings.NewReader(`{"smtp_pass":""}`)))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got, _ := st.GetSetting("smtp_pass"); got != "hunter2" {
		t.Errorf("smtp_pass = %q — a blank field wiped a required credential", got)
	}
}

// The mask is a sentinel, not a value: saving the form back must not overwrite
// the real token with the literal "***".
func TestSettings_SavingTheMaskKeepsTheGitHubToken(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting("update_github_token", "ghp_realtoken"); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	a.handlePutSettings(w, httptest.NewRequest("PUT", "/api/admin/settings",
		strings.NewReader(`{"update_github_token":"***"}`)))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got, _ := st.GetSetting("update_github_token"); got != "ghp_realtoken" {
		t.Errorf("token = %q — round-tripping the masked form clobbered it", got)
	}
}

func TestSettings_ChangingTelegramTokenClearsCachedUsername(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting("telegram_bot_token", "OLD:token"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("telegram_bot_username", "old_bot"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.handlePutSettings(w, httptest.NewRequest("PUT", "/api/admin/settings",
		strings.NewReader(`{"telegram_bot_token":"NEW:token","telegram_bot_username":"old_bot"}`)))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got, _ := st.GetSetting("telegram_bot_token"); got != "NEW:token" {
		t.Fatalf("token = %q", got)
	}
	if got, _ := st.GetSetting("telegram_bot_username"); got != "" {
		t.Fatalf("stale username survived token change: %q", got)
	}
}

func TestSettings_MaskedTelegramTokenKeepsCachedUsername(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting("telegram_bot_token", "REAL:token"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("telegram_bot_username", "real_bot"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.handlePutSettings(w, httptest.NewRequest("PUT", "/api/admin/settings",
		strings.NewReader(`{"telegram_bot_token":"***","telegram_bot_username":"real_bot"}`)))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got, _ := st.GetSetting("telegram_bot_username"); got != "real_bot" {
		t.Fatalf("unchanged token cleared username: %q", got)
	}
}

// A rejection found halfway through must not leave earlier writes committed.
// The concrete case: a password reset submitted together with a mistyped
// address changed the password and killed every session that user had, then
// answered 400 — the admin reads "failed" and never learns otherwise.
func TestAdminUpdateUser_RejectsBeforeAnyWrite(t *testing.T) {
	a, st := newUserEditAPI(t)
	hash, err := auth.HashPassword("original-pw")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateUser(store.NewUser{Username: "u1", Email: "ok@example.com", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}

	w := putUser(a, id, `{"status":"active","password":"newsecret1","email":"not-an-address","remark":"note"}`)
	if w.Code != 400 {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}

	u, _ := st.UserByID(id)
	if !auth.CheckPassword(u.PasswordHash, "original-pw") {
		t.Error("the password was changed even though the request was rejected")
	}
	if u.Remark != "" {
		t.Errorf("remark = %q — written despite the rejection", u.Remark)
	}
	if u.Email.String != "ok@example.com" {
		t.Errorf("email = %q — changed despite the rejection", u.Email.String)
	}
}

// Same shape, one step later: an invalid status must not let the email through.
func TestAdminUpdateUser_BadStatusRejectsBeforeTheEmailWrite(t *testing.T) {
	a, st := newUserEditAPI(t)
	id, _ := st.CreateUser(store.NewUser{Username: "u1", Email: "ok@example.com", PasswordHash: "h"})

	w := putUser(a, id, `{"status":"nonsense","email":"new@example.com"}`)
	if w.Code != 400 {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
	u, _ := st.UserByID(id)
	if u.Email.String != "ok@example.com" {
		t.Errorf("email = %q — an invalid status still let the address change through", u.Email.String)
	}
}
