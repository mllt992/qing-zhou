package store

import (
	"strings"
	"testing"

	"qingzhou/internal/subconv"
)

func setupV6(t *testing.T, hostV6 string) ([]SelfBuiltLink, *Store, int64) {
	t.Helper()
	st := newRefundStore(t)
	uid := mkUser(t, st, "v6user")
	pkg := mkPlan(t, st, "P", 10, 100, 30)
	buy(t, st, uid, pkg)
	sid, err := st.CreateServer(Server{Name: "s1", Host: "203.0.113.5", HostV6: hostV6, Port: 22, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSbInbound(&SbInbound{ServerID: sid, Type: "vless", Tag: "v-in", Listen: "::", ListenPort: 8443, Options: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(Node{Type: "self_built", Name: "香港 01", Protocol: "vless", InboundTag: "v-in", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	bindPlanToInbound(t, st, pkg.ID, "v-in")
	u, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	return st.BuildSelfBuiltLinks(u, "example.com"), st, uid
}

func TestServerHostV6_EmptyUnchanged(t *testing.T) {
	links, _, _ := setupV6(t, "")
	if len(links) != 1 {
		t.Fatalf("want 1 link, got %d", len(links))
	}
	if r := subconv.LinkRemark(links[0].Link); r != "香港 01" {
		t.Fatalf("remark changed: %q", r)
	}
	if !strings.Contains(links[0].Link, "@203.0.113.5:8443") {
		t.Fatalf("v4 link: %s", links[0].Link)
	}
}

func TestServerHostV6_AddsBracketedV6Node(t *testing.T) {
	links, st, _ := setupV6(t, "2001:db8::1")
	if len(links) != 2 {
		t.Fatalf("want 2 links, got %d", len(links))
	}
	if r := subconv.LinkRemark(links[0].Link); r != "香港 01" {
		t.Fatalf("v4 remark: %q", r)
	}
	if r := subconv.LinkRemark(links[1].Link); r != "香港 01-v6" {
		t.Fatalf("v6 remark: %q", r)
	}
	if !strings.Contains(links[1].Link, "@[2001:db8::1]:8443") {
		t.Fatalf("v6 link not bracketed: %s", links[1].Link)
	}
	if links[1].Tag != "v-in" {
		t.Fatalf("v6 link tag: %q", links[1].Tag)
	}
	sv, _ := st.GetServer(1)
	if sv == nil || sv.HostV6 != "2001:db8::1" || sv.Host != "203.0.113.5" {
		t.Fatalf("persisted server: %+v", sv)
	}
}
