package store

import (
	"strings"
	"testing"

	"qingzhou/internal/subconv"
)

func bindNodeToPlan(t *testing.T, st *Store, pkgID, nodeID int64) {
	t.Helper()
	gid, err := st.CreateGroup(NodeGroup{Name: "g-disable"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPlanGroups(pkgID, []int64{gid}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeGroups(nodeID, []int64{gid}); err != nil {
		t.Fatal(err)
	}
}

func setupDisableFixture(t *testing.T, hostV6 string) (*Store, *User, int64, int64, int64) {
	t.Helper()
	st := newRefundStore(t)
	uid := mkUser(t, st, "disable-user")
	pkg := mkPlan(t, st, "P", 10, 100, 30)
	buy(t, st, uid, pkg)
	sid, err := st.CreateServer(Server{Name: "s1", Host: "203.0.113.5", HostV6: hostV6, Port: 22, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSbInbound(&SbInbound{ServerID: sid, Type: "vless", Tag: "v-in", Listen: "::", ListenPort: 8443, Options: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	nid, err := st.CreateNode(Node{Type: "self_built", Name: "香港 01", Protocol: "vless", InboundTag: "v-in", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	bindNodeToPlan(t, st, pkg.ID, nid)
	u, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	return st, u, sid, nid, pkg.ID
}

func linkRemarks(links []SelfBuiltLink) []string {
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, subconv.LinkRemark(l.Link))
	}
	return out
}

func TestDisableNode_HidesFromSubscriptionAndRestores(t *testing.T) {
	st, u, _, nid, _ := setupDisableFixture(t, "2001:db8::1")
	links := st.BuildSelfBuiltLinks(u, "example.com")
	if len(links) != 2 {
		t.Fatalf("want v4+v6, got %d %v", len(links), linkRemarks(links))
	}

	n, err := st.GetNode(nid)
	if err != nil || n == nil {
		t.Fatal(err)
	}
	n.Enabled = false
	if err := st.UpdateNode(*n); err != nil {
		t.Fatal(err)
	}
	if got := st.BuildSelfBuiltLinks(u, "example.com"); len(got) != 0 {
		t.Fatalf("disabled node still in subscription: %v", linkRemarks(got))
	}
	gids, err := st.NodeGroupIDs(nid)
	if err != nil {
		t.Fatal(err)
	}
	grouped, err := st.NodesInGroupsTagged(gids)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grouped {
		if g.ID == nid {
			t.Fatal("disabled node still in NodesInGroupsTagged")
		}
	}

	n.Enabled = true
	if err := st.UpdateNode(*n); err != nil {
		t.Fatal(err)
	}
	restored := st.BuildSelfBuiltLinks(u, "example.com")
	if len(restored) != 2 {
		t.Fatalf("re-enable did not restore both families: %v", linkRemarks(restored))
	}
	if r := subconv.LinkRemark(restored[0].Link); r != "香港 01" {
		t.Fatalf("v4 remark after restore: %q", r)
	}
	if r := subconv.LinkRemark(restored[1].Link); r != "香港 01-v6" {
		t.Fatalf("v6 remark after restore: %q", r)
	}
}

func TestDisableInbound_HidesFromSubscriptionAndRestores(t *testing.T) {
	st, u, _, _, _ := setupDisableFixture(t, "2001:db8::1")
	ib, err := st.GetSbInboundByTag("v-in")
	if err != nil || ib == nil {
		t.Fatal(err)
	}
	ib.Enabled = false
	if _, err := st.SaveSbInbound(ib); err != nil {
		t.Fatal(err)
	}
	if got := st.BuildSelfBuiltLinks(u, "example.com"); len(got) != 0 {
		t.Fatalf("disabled inbound still in subscription: %v", linkRemarks(got))
	}
	ib.Enabled = true
	if _, err := st.SaveSbInbound(ib); err != nil {
		t.Fatal(err)
	}
	if got := st.BuildSelfBuiltLinks(u, "example.com"); len(got) != 2 {
		t.Fatalf("re-enabled inbound: %v", linkRemarks(got))
	}
}

func TestDisableServer_HidesNodesKeepsRow(t *testing.T) {
	st, u, sid, nid, _ := setupDisableFixture(t, "2001:db8::1")
	sv, err := st.GetServer(sid)
	if err != nil || sv == nil {
		t.Fatal(err)
	}
	sv.Enabled = false
	if err := st.UpdateServer(*sv); err != nil {
		t.Fatal(err)
	}
	if got := st.BuildSelfBuiltLinks(u, "example.com"); len(got) != 0 {
		t.Fatalf("disabled server still published nodes: %v", linkRemarks(got))
	}
	n, err := st.GetNode(nid)
	if err != nil || n == nil || !n.Enabled || n.InboundTag != "v-in" {
		t.Fatalf("disabling the server must keep the node row intact: %+v %v", n, err)
	}
	sv.Enabled = true
	if err := st.UpdateServer(*sv); err != nil {
		t.Fatal(err)
	}
	if got := st.BuildSelfBuiltLinks(u, "example.com"); len(got) != 2 {
		t.Fatalf("re-enabled server: %v", linkRemarks(got))
	}
}

func TestOutputFamilies_V4OffV6On(t *testing.T) {
	st, u, _, nid, _ := setupDisableFixture(t, "2001:db8::1")
	n, _ := st.GetNode(nid)
	n.SetOutputFamilies(false, true)
	if err := st.UpdateNode(*n); err != nil {
		t.Fatal(err)
	}
	links := st.BuildSelfBuiltLinks(u, "example.com")
	if len(links) != 1 {
		t.Fatalf("want only -v6, got %v", linkRemarks(links))
	}
	if r := subconv.LinkRemark(links[0].Link); r != "香港 01-v6" {
		t.Fatalf("remark: %q", r)
	}
	if !strings.Contains(links[0].Link, "@[2001:db8::1]:8443") {
		t.Fatalf("v6 host: %s", links[0].Link)
	}
}

func TestOutputFamilies_V6OffKeepsV4(t *testing.T) {
	st, u, _, nid, _ := setupDisableFixture(t, "2001:db8::1")
	n, _ := st.GetNode(nid)
	n.SetOutputFamilies(true, false)
	if err := st.UpdateNode(*n); err != nil {
		t.Fatal(err)
	}
	links := st.BuildSelfBuiltLinks(u, "example.com")
	if len(links) != 1 {
		t.Fatalf("want only v4, got %v", linkRemarks(links))
	}
	if r := subconv.LinkRemark(links[0].Link); r != "香港 01" {
		t.Fatalf("remark: %q", r)
	}
	if strings.Contains(links[0].Link, "2001:db8::1") {
		t.Fatalf("v6 leaked into v4-only output: %s", links[0].Link)
	}
}

func TestOutputFamilies_BothOffEqualsDisabled(t *testing.T) {
	st, u, _, nid, _ := setupDisableFixture(t, "2001:db8::1")
	n, _ := st.GetNode(nid)
	n.SetOutputFamilies(false, false)
	n.Enabled = true
	if err := st.UpdateNode(*n); err != nil {
		t.Fatal(err)
	}
	if got := st.BuildSelfBuiltLinks(u, "example.com"); len(got) != 0 {
		t.Fatalf("both families off still published: %v", linkRemarks(got))
	}
	// CreateNode historical zero value still publishes both.
	other, err := st.CreateNode(Node{Type: "self_built", Name: "历史默认", InboundTag: "v-in", Enabled: true, GroupIDs: n.GroupIDs})
	if err != nil {
		t.Fatal(err)
	}
	created, _ := st.GetNode(other)
	if !created.OutputV4 || !created.OutputV6 {
		t.Fatalf("CreateNode zero value should default both on: %+v", created)
	}
}

func TestOutputFamilies_V6WithoutHostIsNoop(t *testing.T) {
	st, u, _, nid, _ := setupDisableFixture(t, "")
	n, _ := st.GetNode(nid)
	n.SetOutputFamilies(false, true)
	if err := st.UpdateNode(*n); err != nil {
		t.Fatal(err)
	}
	if got := st.BuildSelfBuiltLinks(u, "example.com"); len(got) != 0 {
		t.Fatalf("v6-only without host_v6 leaked: %v", linkRemarks(got))
	}
}

func TestDisableExternalNode_HiddenFromGroups(t *testing.T) {
	st := newRefundStore(t)
	gid, err := st.CreateGroup(NodeGroup{Name: "g"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateNode(Node{Type: "external", Name: "外链", Protocol: "trojan", ShareLink: "trojan://pw@1.2.3.4:443#ext", Enabled: true, GroupIDs: []int64{gid}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.NodesInGroupsTagged([]int64{gid})
	if err != nil || len(got) != 1 {
		t.Fatalf("enabled external: %d %v", len(got), err)
	}
	n, _ := st.GetNode(id)
	n.Enabled = false
	if err := st.UpdateNode(*n); err != nil {
		t.Fatal(err)
	}
	got, err = st.NodesInGroupsTagged([]int64{gid})
	if err != nil || len(got) != 0 {
		t.Fatalf("disabled external still listed: %d %v", len(got), err)
	}
}

func TestDisableLandingServer_HidesLogicalRouteOnly(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "route-user")
	pkg := mkPlan(t, st, "多落地", 10, 100, 30)
	buy(t, st, uid, pkg)

	entryID, err := st.CreateServer(Server{Name: "entry", Host: "203.0.113.10", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	landAID, err := st.CreateServer(Server{Name: "land-a", Host: "203.0.113.11", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	landBID, err := st.CreateServer(Server{Name: "land-b", Host: "203.0.113.12", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSbInbound(&SbInbound{ServerID: entryID, Type: "vless", Tag: "shared-entry", ListenPort: 8443, Options: `{}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	landingA, err := st.SaveSbInbound(&SbInbound{ServerID: landAID, Type: "vless", Tag: "landing-a", ListenPort: 9443, Options: `{}`, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	landingB, err := st.SaveSbInbound(&SbInbound{ServerID: landBID, Type: "vless", Tag: "landing-b", ListenPort: 10443, Options: `{}`, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	gid, err := st.CreateGroup(NodeGroup{Name: "共享入口"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPlanGroups(pkg.ID, []int64{gid}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []Node{
		{Type: "self_built", Name: "入口 → 落地 A", InboundTag: "shared-entry", RouteUpstreamInboundID: landingA, Enabled: true, GroupIDs: []int64{gid}},
		{Type: "self_built", Name: "入口 → 落地 B", InboundTag: "shared-entry", RouteUpstreamInboundID: landingB, Enabled: true, GroupIDs: []int64{gid}},
	} {
		if _, err := st.CreateNode(n); err != nil {
			t.Fatal(err)
		}
	}
	u, _ := st.UserByID(uid)
	if got := len(st.BuildSelfBuiltLinks(u, "entry.example.com")); got != 2 {
		t.Fatalf("setup links = %d, want 2", got)
	}

	sv, _ := st.GetServer(landBID)
	sv.Enabled = false
	if err := st.UpdateServer(*sv); err != nil {
		t.Fatal(err)
	}
	links := st.BuildSelfBuiltLinks(u, "entry.example.com")
	if len(links) != 1 {
		t.Fatalf("disabled landing server left %d links, want 1 (fail closed, not leak): %v", len(links), linkRemarks(links))
	}
	if r := subconv.LinkRemark(links[0].Link); r != "入口 → 落地 A" {
		t.Fatalf("remaining route: %q", r)
	}

	sv.Enabled = true
	if err := st.UpdateServer(*sv); err != nil {
		t.Fatal(err)
	}
	if got := len(st.BuildSelfBuiltLinks(u, "entry.example.com")); got != 2 {
		t.Fatalf("re-enabled landing server: want 2, got %d", got)
	}
}

func TestDisable_UserProxiesHonorFamilyAndServer(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "proxy-user")
	pkg := mkPlan(t, st, "P", 10, 100, 30)
	buy(t, st, uid, pkg)
	sid, err := st.CreateServer(Server{Name: "mix", Host: "203.0.113.20", HostV6: "2001:db8::20", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSbInbound(&SbInbound{ServerID: sid, Type: "mixed", Tag: "mixed-proxy", Listen: "::", ListenPort: 7890, Options: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	nid, err := st.CreateNode(Node{Type: "self_built", Name: "混合代理", InboundTag: "mixed-proxy", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	bindNodeToPlan(t, st, pkg.ID, nid)
	u, _ := st.UserByID(uid)

	proxies := st.BuildUserProxies(u, "example.com")
	if len(proxies) != 2 {
		t.Fatalf("want v4+v6 proxies, got %d", len(proxies))
	}

	n, _ := st.GetNode(nid)
	n.SetOutputFamilies(false, true)
	if err := st.UpdateNode(*n); err != nil {
		t.Fatal(err)
	}
	proxies = st.BuildUserProxies(u, "example.com")
	if len(proxies) != 1 || proxies[0].Tag != "mixed-proxy-v6" || proxies[0].Host != "2001:db8::20" {
		t.Fatalf("v4 off v6 on proxies: %+v", proxies)
	}

	n.SetOutputFamilies(false, false)
	if err := st.UpdateNode(*n); err != nil {
		t.Fatal(err)
	}
	if got := st.BuildUserProxies(u, "example.com"); len(got) != 0 {
		t.Fatalf("both off still published proxies: %+v", got)
	}

	n.SetOutputFamilies(true, true)
	if err := st.UpdateNode(*n); err != nil {
		t.Fatal(err)
	}
	sv, _ := st.GetServer(sid)
	sv.Enabled = false
	if err := st.UpdateServer(*sv); err != nil {
		t.Fatal(err)
	}
	if got := st.BuildUserProxies(u, "example.com"); len(got) != 0 {
		t.Fatalf("disabled server still published proxies: %+v", got)
	}
}

func TestNodeOutputMigrationDefaultsOn(t *testing.T) {
	st, _, _, nid, _ := setupDisableFixture(t, "")
	n, err := st.GetNode(nid)
	if err != nil || n == nil {
		t.Fatal(err)
	}
	if !n.OutputV4 || !n.OutputV6 {
		t.Fatalf("fresh node should publish both families: %+v", n)
	}
}
