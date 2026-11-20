package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
)

func TestInboundCredentialStats(t *testing.T) {
	t.Parallel()
	creds, accounts := inboundCredentialStats(nil)
	if creds != 0 || accounts != 0 {
		t.Fatalf("empty = %d/%d, want 0/0", creds, accounts)
	}

	// The issue's case: one panel account, many derived logical-route credentials.
	users := []singbox.User{
		{Name: "qz_alice~qzr11", OwnerID: 7},
		{Name: "qz_alice~qzr12", OwnerID: 7},
		{Name: "qz_alice~qzr13", OwnerID: 7},
	}
	creds, accounts = inboundCredentialStats(users)
	if creds != 3 || accounts != 1 {
		t.Fatalf("routed single account = %d creds / %d accounts, want 3/1", creds, accounts)
	}

	users = append(users,
		singbox.User{Name: "qz_bob~qzr11", OwnerID: 8},
		singbox.User{Name: "qz_bob~qzr12", OwnerID: 8},
	)
	creds, accounts = inboundCredentialStats(users)
	if creds != 5 || accounts != 2 {
		t.Fatalf("two accounts = %d creds / %d accounts, want 5/2", creds, accounts)
	}

	// mixed inbounds emit account-level and plan-level credentials for the same owner.
	creds, accounts = inboundCredentialStats([]singbox.User{
		{Name: "alice", OwnerID: 7},
		{Name: "qz_plan_1", OwnerID: 7},
	})
	if creds != 2 || accounts != 1 {
		t.Fatalf("mixed account+plan = %d creds / %d accounts, want 2/1", creds, accounts)
	}

	// Identities missing OwnerID must still be counted, not silently dropped.
	creds, accounts = inboundCredentialStats([]singbox.User{
		{Name: "orphan-a"},
		{Name: "orphan-b"},
		{Name: "qz_alice", OwnerID: 7},
	})
	if creds != 3 || accounts != 3 {
		t.Fatalf("unattributed = %d creds / %d accounts, want 3/3", creds, accounts)
	}
}

// One real user on a logical-route inbound must report 1 account and N
// credentials, which is the wording the inbound card used to get wrong.
func TestHandleAdminListSbInboundsCountsCredentialsAndAccounts(t *testing.T) {
	a, st := newResetSubAPI(t)

	uid, err := st.CreateUser(store.NewUser{Username: "alice", PasswordHash: "x", Points: 1_000_000, SubToken: "ALICE"})
	if err != nil {
		t.Fatal(err)
	}
	pkgID, err := st.CreatePackage(store.Package{
		Type: "plan", Name: "多落地", PricePoints: 10,
		TrafficBytes: 100 << 30, DurationDays: 30, Stock: -1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := st.GetPackage(pkgID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Purchase(uid, pkg, "", func(*store.User, bool) error { return nil }); err != nil {
		t.Fatalf("purchase: %v", err)
	}

	if _, err := st.SaveSbInbound(&store.SbInbound{Type: "vless", Tag: "shared-entry", Listen: "::", ListenPort: 8443, Options: `{}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	landingA, err := st.SaveSbInbound(&store.SbInbound{Type: "vless", Tag: "landing-a", Listen: "::", ListenPort: 9443, Options: `{}`, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	landingB, err := st.SaveSbInbound(&store.SbInbound{Type: "vless", Tag: "landing-b", Listen: "::", ListenPort: 10443, Options: `{}`, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// A non-routed inbound should stay 1:1 for a single entitled account.
	if _, err := st.SaveSbInbound(&store.SbInbound{Type: "vless", Tag: "plain-entry", Listen: "::", ListenPort: 11443, Options: `{}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	gid, err := st.CreateGroup(store.NodeGroup{Name: "共享入口"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPlanGroups(pkg.ID, []int64{gid}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []store.Node{
		{Type: "self_built", Name: "入口 → 落地 A", InboundTag: "shared-entry", RouteUpstreamInboundID: landingA, Enabled: true, GroupIDs: []int64{gid}},
		{Type: "self_built", Name: "入口 → 落地 B", InboundTag: "shared-entry", RouteUpstreamInboundID: landingB, Enabled: true, GroupIDs: []int64{gid}},
		{Type: "self_built", Name: "普通入口", InboundTag: "plain-entry", Enabled: true, GroupIDs: []int64{gid}},
	} {
		if _, err := st.CreateNode(n); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/sb/inbounds", nil)
	w := httptest.NewRecorder()
	a.handleAdminListSbInbounds(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list inbounds = %d %s", w.Code, w.Body.String())
	}

	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	got := map[string]map[string]any{}
	for _, row := range env.Data {
		tag, _ := row["tag"].(string)
		got[tag] = row
	}

	assertCounts := func(tag string, wantCreds, wantAccounts float64) {
		t.Helper()
		row, ok := got[tag]
		if !ok {
			t.Fatalf("missing inbound %q in %v", tag, got)
		}
		if row["credential_count"] != wantCreds || row["account_count"] != wantAccounts || row["user_count"] != wantCreds {
			t.Fatalf("%s counts credential=%v account=%v user=%v, want %v/%v/%v",
				tag, row["credential_count"], row["account_count"], row["user_count"],
				wantCreds, wantAccounts, wantCreds)
		}
	}

	assertCounts("shared-entry", 2, 1)
	assertCounts("plain-entry", 1, 1)
	// Landings have no self-built node of their own in this fixture, so they
	// should carry no entitled credentials.
	assertCounts("landing-a", 0, 0)
	assertCounts("landing-b", 0, 0)
}
