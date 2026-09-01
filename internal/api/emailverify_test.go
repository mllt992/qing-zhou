package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/auth"
	"qingzhou/internal/store"
)

const leakLink = "ss://LEAKED-UPSTREAM-CRED@upstream.example:8388#free"

// unverifiedFixture is an unverified user who would otherwise receive an
// external share_link via the free group — the leak #22 closed.
func unverifiedFixture(t *testing.T) (*API, *store.Store, int64) {
	t.Helper()
	a, st := newUserEditAPI(t)
	if err := st.SetSettingBool("email_verify_required", true); err != nil {
		t.Fatal(err)
	}
	gid, err := st.CreateGroup(store.NodeGroup{Name: "免费"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("free_group_id", strconv.FormatInt(gid, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(store.Node{
		Type: "external", Name: "上游", Protocol: "ss",
		ShareLink: leakLink, Enabled: true, GroupIDs: []int64{gid},
	}); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("secret1")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(store.NewUser{
		Username: "unverified", Email: "u@example.com", PasswordHash: hash,
		SubToken: "tok-unverified", TrafficLimit: 10 << 30,
		ExpiryAt: time.Now().Unix() + 86400,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, st, uid
}

func loginAs(a *API, user, pass string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	body := `{"username":"` + user + `","password":"` + pass + `"}`
	a.handleLogin(w, httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body)))
	return w
}

func getSub(a *API, token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.Router().ServeHTTP(w, httptest.NewRequest("GET", "/sub/"+token+"?format=info", nil))
	return w
}

func TestUnverified_MixedProxyCredentialEndpointsAreBlocked(t *testing.T) {
	a, _, uid := unverifiedFixture(t)
	for _, tc := range []struct {
		name, method, path, body string
		handler                  http.HandlerFunc
	}{
		{"list", "GET", "/api/user/proxies", "", a.handleUserProxies},
		{"account", "GET", "/api/user/proxy-account", "", a.handleUserProxyAccount},
		{"update bucket", "PUT", "/api/user/proxies/1", `{}`, a.handleUpdateUserProxy},
		{"update account", "PUT", "/api/user/proxy-account", `{}`, a.handleUpdateUserProxyAccount},
	} {
		w := httptest.NewRecorder()
		r := asUser(uid, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		tc.handler(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403: %s", tc.name, w.Code, w.Body.String())
		}
	}
}

// The resend-verify button only works after login. Mail scanners also consume
// the one-shot verify link, so the recovery path is "log in → 个人中心 → 重发".
// Blocking login made that a dead end.
func TestUnverified_CanStillLogin(t *testing.T) {
	a, _, _ := unverifiedFixture(t)
	w := loginAs(a, "unverified", "secret1")
	if w.Code != http.StatusOK {
		t.Fatalf("login = %d %s — unverified users must be able to sign in to resend the verify mail",
			w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Token string `json:"token"`
			User  struct {
				EmailVerified bool `json:"email_verified"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Token == "" {
		t.Fatal("login succeeded but issued no token")
	}
	if resp.Data.User.EmailVerified {
		t.Fatal("fixture is supposed to be unverified")
	}
}

// The actual hole: an unverified account's /sub/{token} used to return the
// raw upstream share_link. Empty-but-valid is how expired/over-quota already
// look, so clients do not treat it as an error.
func TestUnverified_SubWithholdsExternalLinks(t *testing.T) {
	a, st, uid := unverifiedFixture(t)

	w := getSub(a, "tok-unverified")
	if w.Code != http.StatusOK {
		t.Fatalf("sub = %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "LEAKED-UPSTREAM-CRED") || strings.Contains(body, "upstream.example") {
		t.Fatal("unverified subscription leaked the external share_link")
	}
	if !strings.Contains(body, "可用节点</span><b>0</b>") {
		t.Fatalf("info page should report 0 nodes for an unverified account:\n%s", body)
	}

	if err := st.SetEmailVerified(uid); err != nil {
		t.Fatal(err)
	}
	w = getSub(a, "tok-unverified")
	if w.Code != http.StatusOK {
		t.Fatalf("sub after verify = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "可用节点</span><b>1</b>") {
		t.Fatalf("verified account should see the free-group node:\n%s", w.Body.String())
	}
}

// Valid ss:// so /nodes and /sub?format=base64 can actually parse it. The
// distinctive host is the leak marker — the info page only shows a count.
const paidLink = "ss://YWVzLTI1Ni1nY206cGFpZA@paid.example:8388#paid"

// grantPaidPlan binds a distinct paid-group node to a plan and grants it,
// matching the two ways an unverified account gets a real entitlement:
// admin assign, or a points purchase.
func grantPaidPlan(t *testing.T, st *store.Store, uid int64, viaPurchase bool) {
	t.Helper()
	gid, err := st.CreateGroup(store.NodeGroup{Name: "付费"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(store.Node{
		Type: "external", Name: "付费节点", Protocol: "ss",
		ShareLink: paidLink, Enabled: true, GroupIDs: []int64{gid},
	}); err != nil {
		t.Fatal(err)
	}
	pkgID, err := st.CreatePackage(store.Package{
		Type: "plan", Name: "月付", PricePoints: 100,
		TrafficBytes: 100 << 30, DurationDays: 30, Stock: -1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPlanGroups(pkgID, []int64{gid}); err != nil {
		t.Fatal(err)
	}
	pkg, err := st.GetPackage(pkgID)
	if err != nil {
		t.Fatal(err)
	}
	if viaPurchase {
		if _, err := st.AdjustPoints(uid, 100, "admin_recharge", 0, "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Purchase(uid, pkg, "", func(*store.User, bool) error { return nil }); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := st.AssignPackage(uid, pkg, 0, func(*store.User, bool) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func assertSubHasPaidNode(t *testing.T, a *API) {
	t.Helper()
	w := getSub(a, "tok-unverified")
	if w.Code != http.StatusOK {
		t.Fatalf("sub = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "可用节点</span><b>0</b>") {
		t.Fatalf("paid plan should release nodes without email verify:\n%s", w.Body.String())
	}
	// The info page only shows a count. Fetch a renderable format so we can
	// see the paid-group host itself, not just "some nodes came back".
	raw := httptest.NewRecorder()
	a.Router().ServeHTTP(raw, httptest.NewRequest("GET", "/sub/tok-unverified?format=clash", nil))
	if raw.Code != http.StatusOK {
		t.Fatalf("sub clash = %d", raw.Code)
	}
	if !strings.Contains(raw.Body.String(), "paid.example") {
		t.Fatalf("subscription should include the paid-plan node:\n%s", raw.Body.String())
	}
}

// An admin-assigned plan is a real entitlement: the user must receive that
// plan's nodes even if they never clicked the verify mail.
func TestUnverified_AdminAssignedPlanLiftsGate(t *testing.T) {
	a, st, uid := unverifiedFixture(t)
	grantPaidPlan(t, st, uid, false)
	assertSubHasPaidNode(t, a)
}

// Spending points on a plan is the same admission decision as an admin grant.
func TestUnverified_PointsPurchaseLiftsGate(t *testing.T) {
	a, st, uid := unverifiedFixture(t)
	grantPaidPlan(t, st, uid, true)
	assertSubHasPaidNode(t, a)
}

// A traffic top-up is not a plan. It must not turn an unverified signup into
// a free-group credential scrape.
func TestUnverified_TrafficPackageDoesNotLiftGate(t *testing.T) {
	a, st, uid := unverifiedFixture(t)
	pkgID, err := st.CreatePackage(store.Package{
		Type: "traffic", Name: "流量包", PricePoints: 50,
		TrafficBytes: 10 << 30, Stock: -1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := st.GetPackage(pkgID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AssignPackage(uid, pkg, 0, func(*store.User, bool) error { return nil }); err != nil {
		t.Fatal(err)
	}

	w := getSub(a, "tok-unverified")
	if w.Code != http.StatusOK {
		t.Fatalf("sub = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "可用节点</span><b>0</b>") {
		t.Fatalf("a traffic top-up must not bypass email verification:\n%s", w.Body.String())
	}
}

// Runtime client state is not an exemption: letting a newly provisioned identity
// lift the gate recreates the same bypass as checking for a newly purchased plan.
// Historical provisioned accounts are marked by migration before requests run.
func TestUnverified_NewProvisioningDoesNotLiftGate(t *testing.T) {
	a, st, uid := unverifiedFixture(t)
	if err := st.SetUserClient(uid, 0, "qz_unverified", "uuid", "secret"); err != nil {
		t.Fatal(err)
	}

	w := getSub(a, "tok-unverified")
	if w.Code != http.StatusOK {
		t.Fatalf("sub = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "可用节点</span><b>0</b>") {
		t.Fatalf("new provisioning state must not bypass email verification:\n%s", w.Body.String())
	}
}

// /nodes and /nodes/ping stay closed for a pending-verify signup with no
// paid plan, so they cannot be used to learn upstream hosts while /sub is
// empty. A live paid plan unlocks them the same way it unlocks /sub.
func TestUnverified_NodeInventoryAndPingRoutesAreGated(t *testing.T) {
	a, _, _ := unverifiedFixture(t)
	login := loginAs(a, "unverified", "secret1")
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	var authResp struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &authResp); err != nil || authResp.Data.Token == "" {
		t.Fatalf("decode login token: %v %s", err, login.Body.String())
	}

	// Exercise the registered routes, not the handlers directly. This locks both
	// /nodes and /nodes/ping behind the gate even if router wiring changes later.
	for _, path := range []string{"/api/user/nodes", "/api/user/nodes/ping"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer "+authResp.Data.Token)
			a.Router().ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s = %d %s, want 403", path, w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "upstream.example") {
				t.Fatalf("%s leaked node coordinates: %s", path, w.Body.String())
			}
		})
	}
}

func TestUnverified_PaidPlanUnlocksNodeRoutes(t *testing.T) {
	a, st, uid := unverifiedFixture(t)
	grantPaidPlan(t, st, uid, false)
	login := loginAs(a, "unverified", "secret1")
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	var authResp struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &authResp); err != nil || authResp.Data.Token == "" {
		t.Fatalf("decode login token: %v %s", err, login.Body.String())
	}

	for _, path := range []string{"/api/user/nodes", "/api/user/nodes/ping"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer "+authResp.Data.Token)
			a.Router().ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("%s = %d %s, want 200 after paid plan", path, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "paid.example") {
				t.Fatalf("%s missing paid-plan node: %s", path, w.Body.String())
			}
		})
	}
}

func TestUnverified_WelcomeGrantDoesNotLiftTheGate(t *testing.T) {
	a, st, uid := unverifiedFixture(t)
	if err := st.EnsureWelcomeBucket(uid, "unverified", 10<<30, time.Now().Unix()+86400); err != nil {
		t.Fatal(err)
	}

	w := getSub(a, "tok-unverified")
	if w.Code != http.StatusOK {
		t.Fatalf("sub = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "可用节点</span><b>0</b>") {
		t.Fatalf("welcome grant alone must not release external credentials:\n%s", w.Body.String())
	}
}

// Invite-code signup is allowed to skip email verify. Those accounts often
// have only the free group / signup grant — no paid plan, and (under the old
// gate) no client either — so the v0.2.53/54 heuristics still emptied them.
func TestUnverified_RegCodeUserKeepsNodes(t *testing.T) {
	a, st, uid := unverifiedFixture(t)
	codes, err := st.GenerateRegCodes(1, 1, "test", nil)
	if err != nil || len(codes) != 1 {
		t.Fatalf("GenerateRegCodes: %v %v", codes, err)
	}
	cid, ok := st.ConsumeRegCode(codes[0])
	if !ok {
		t.Fatal("ConsumeRegCode")
	}
	if err := st.RecordRegCodeUse(cid, uid, "unverified", ""); err != nil {
		t.Fatal(err)
	}

	w := getSub(a, "tok-unverified")
	if w.Code != http.StatusOK {
		t.Fatalf("sub = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "可用节点</span><b>1</b>") {
		t.Fatalf("invite-code account must keep its nodes without verifying email:\n%s", w.Body.String())
	}
}

// Admin-created accounts are pre-verified. Even if that flag were missing,
// they are provisioned immediately — covered by the client-id path. This
// locks the documented contract: the verify switch does not apply to them.
func TestUnverified_AdminCreatedIsPreVerified(t *testing.T) {
	a, st, uid := unverifiedFixture(t)
	if err := st.SetEmailVerified(uid); err != nil {
		t.Fatal(err)
	}
	w := getSub(a, "tok-unverified")
	if w.Code != http.StatusOK {
		t.Fatalf("sub = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "可用节点</span><b>1</b>") {
		t.Fatalf("admin-created (pre-verified) account must receive nodes:\n%s", w.Body.String())
	}
}

// Invite-code registration must not demand an email just because the open-
// signup verify switch is on.
func TestRegister_CodeDoesNotRequireEmailWhenVerifyOn(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting("register_mode", "code"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSettingBool("email_verify_required", true); err != nil {
		t.Fatal(err)
	}
	codes, err := st.GenerateRegCodes(1, 1, "", nil)
	if err != nil || len(codes) != 1 {
		t.Fatalf("GenerateRegCodes: %v %v", codes, err)
	}

	w := httptest.NewRecorder()
	body := `{"username":"codeuser","password":"secret1","code":"` + codes[0] + `"}`
	a.handleRegister(w, httptest.NewRequest("POST", "/api/auth/register", strings.NewReader(body)))
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "需要邮箱") {
		t.Fatalf("invite-code signup demanded an email: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "need_verify") {
		t.Fatalf("invite-code signup deferred to email verify: %s", w.Body.String())
	}
}

// A duration setting without traffic must not be staged in users.* while email
// verification defers provisioning. Zero traffic means no welcome entitlement.
func TestRegister_ZeroTrafficDoesNotCreateLegacyEntitlement(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting("register_mode", "open"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSettingBool("email_verify_required", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("default_traffic", "0"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("default_expiry_days", "30"); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	body := `{"username":"zeroquota","email":"zero@example.com","password":"secret1"}`
	a.handleRegister(w, httptest.NewRequest("POST", "/api/auth/register", strings.NewReader(body)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "need_verify") {
		t.Fatalf("register = %d: %s", w.Code, w.Body.String())
	}
	u, err := st.UserByUsername("zeroquota")
	if err != nil || u == nil {
		t.Fatalf("UserByUsername: %v %#v", err, u)
	}
	if u.TrafficLimit != 0 || u.ExpiryAt != 0 {
		t.Fatalf("legacy aggregate = traffic %d / expiry %d, want 0 / 0", u.TrafficLimit, u.ExpiryAt)
	}
	buckets, err := st.ListBuckets(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 0 {
		t.Fatalf("unverified zero-quota signup created buckets: %+v", buckets)
	}
}

// Turning the setting off must not keep withholding nodes from unverified
// accounts — otherwise flipping the toggle would silently lock everyone out.
func TestUnverified_SubPassesWhenVerifyNotRequired(t *testing.T) {
	a, st, _ := unverifiedFixture(t)
	if err := st.SetSettingBool("email_verify_required", false); err != nil {
		t.Fatal(err)
	}
	w := getSub(a, "tok-unverified")
	if w.Code != http.StatusOK {
		t.Fatalf("sub = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "可用节点</span><b>1</b>") {
		t.Fatalf("verify_required=off should serve the free-group node:\n%s", w.Body.String())
	}
}
