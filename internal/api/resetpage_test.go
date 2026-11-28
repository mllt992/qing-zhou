package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/store"
)

// The regression this page exists for: /reset?token=... used to fall through to
// the SPA catch-all, and because the panel router is hash-based the token was
// dropped and the user landed on the public monitor page. Anything that answers
// here must actually be a password form.
func TestResetPage_RendersAPasswordForm(t *testing.T) {
	a, _ := newUserEditAPI(t)

	w := httptest.NewRecorder()
	a.handleResetPage(w, httptest.NewRequest("GET", "/reset?token=abc123", nil))

	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `type="password"`) {
		t.Error("the reset page has no password field")
	}
	if !strings.Contains(body, `/api/auth/reset`) {
		t.Error("the reset page never posts to the reset endpoint")
	}
	if !strings.Contains(body, `value="abc123"`) {
		t.Error("the token from the link is not carried into the form")
	}
}

// The token is a credential and the page embeds it.
func TestResetPage_IsNotCacheableOrIndexable(t *testing.T) {
	a, _ := newUserEditAPI(t)

	w := httptest.NewRecorder()
	a.handleResetPage(w, httptest.NewRequest("GET", "/reset?token=abc123", nil))

	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store on a page holding a reset token", cc)
	}
	if rt := w.Header().Get("X-Robots-Tag"); !strings.Contains(rt, "noindex") {
		t.Errorf("X-Robots-Tag = %q, want noindex", rt)
	}
}

// The token is raw query input reflected into an attribute. Unescaped, this is
// a reflected XSS on an unauthenticated page that an attacker delivers simply
// by mailing someone a "reset" link.
func TestResetPage_EscapesTheToken(t *testing.T) {
	a, _ := newUserEditAPI(t)

	w := httptest.NewRecorder()
	a.handleResetPage(w, httptest.NewRequest("GET", `/reset?token="><script>alert(1)</script>`, nil))

	body := w.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("the token is reflected unescaped — script injection through the reset link")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected the injected markup to survive as escaped text")
	}
}

// Rendering must not spend the token: mail scanners and link previewers fetch
// these unprompted, and a GET that redeemed would burn the link before the user
// ever saw it.
func TestResetPage_DoesNotConsumeTheToken(t *testing.T) {
	a, st := newUserEditAPI(t)
	uid, err := st.CreateUser(store.NewUser{Username: "u1", Email: "u1@example.com", PasswordHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetEmailVerified(uid); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.IssueEmailToken(uid, "u1@example.com", "tok-live", "reset", time.Hour); err != nil || !ok {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	a.handleResetPage(w, httptest.NewRequest("GET", "/reset?token=tok-live", nil))
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}

	// Still redeemable afterwards — that is the whole point.
	if okTok, err := st.ResetTokenValid("tok-live"); err != nil || !okTok {
		t.Error("merely rendering the page consumed the reset token")
	}
}

// A link with no token at all is a broken link, not a blank form to submit.
func TestResetPage_RejectsAMissingToken(t *testing.T) {
	a, _ := newUserEditAPI(t)

	w := httptest.NewRecorder()
	a.handleResetPage(w, httptest.NewRequest("GET", "/reset", nil))

	if w.Code != 400 {
		t.Errorf("status %d, want 400 for a link with no token", w.Code)
	}
	if strings.Contains(w.Body.String(), `type="password"`) {
		t.Error("rendered a password form for a link carrying no token")
	}
}

// The route has to be registered ABOVE the SPA catch-all. That ordering is the
// entire bug: r.Handle("/*", frontend.Handler()) answers 200 with index.html for
// anything unclaimed, so a missing /reset route did not 404 — it silently served
// the app shell and looked fine.
func TestRouter_ResetIsNotSwallowedBySPACatchAll(t *testing.T) {
	a, _ := newUserEditAPI(t)
	rt := a.Router()

	w := httptest.NewRecorder()
	rt.ServeHTTP(w, httptest.NewRequest("GET", "/reset?token=abc123", nil))

	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `type="password"`) {
		t.Fatal("/reset served the SPA shell instead of the reset form — the route is below the catch-all")
	}
}
