package store

import (
	"testing"

	"qingzhou/internal/subconv"
)

// A subscription link's remark must be the node's display name from the 节点
// page, not the raw sing-box inbound tag — the tag is an internal identifier and
// admins expect clients to show the name they configured.
func TestSelfBuiltLinks_RemarkUsesNodeName(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "alice")
	pkg := mkPlan(t, st, "P", 10, 100, 30)
	buy(t, st, uid, pkg)

	if _, err := st.SaveSbInbound(&SbInbound{
		Type: "vless", Tag: "vless-in", Listen: "::", ListenPort: 8443,
		Options: "{}", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(Node{
		Type: "self_built", Name: "香港 01", Protocol: "vless",
		InboundTag: "vless-in", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	bindPlanToInbound(t, st, pkg.ID, "vless-in")

	u, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	links := st.BuildSelfBuiltLinks(u, "example.com")
	if len(links) != 1 {
		t.Fatalf("want 1 link, got %d", len(links))
	}
	// The tag is still carried alongside — the group filter joins on it.
	if links[0].Tag != "vless-in" {
		t.Fatalf("link must carry its inbound tag, got %q", links[0].Tag)
	}
	// Exactly the node name, no dynamic quota/expiry suffix — clients persist
	// manual node selection by name, so the remark must be stable across refreshes.
	if remark := subconv.LinkRemark(links[0].Link); remark != "香港 01" {
		t.Fatalf("remark should be the node name %q, got %q", "香港 01", remark)
	}
}

// An inbound no node is bound to still needs a remark; it falls back to the tag.
func TestSelfBuiltLinks_RemarkFallsBackToTag(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "bob")
	pkg := mkPlan(t, st, "P", 10, 100, 30)
	buy(t, st, uid, pkg)

	if _, err := st.SaveSbInbound(&SbInbound{
		Type: "vless", Tag: "orphan-in", Listen: "::", ListenPort: 8443,
		Options: "{}", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	// Empty name so the remark still falls back to the tag; the node row is
	// only here to put the inbound in the plan's group.
	gid, err := st.CreateGroup(NodeGroup{Name: "g"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPlanGroups(pkg.ID, []int64{gid}); err != nil {
		t.Fatal(err)
	}
	nid, err := st.CreateNode(Node{Type: "self_built", Name: "", InboundTag: "orphan-in", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeGroups(nid, []int64{gid}); err != nil {
		t.Fatal(err)
	}

	u, _ := st.UserByID(uid)
	links := st.BuildSelfBuiltLinks(u, "example.com")
	if len(links) != 1 {
		t.Fatalf("want 1 link, got %d", len(links))
	}
	if remark := subconv.LinkRemark(links[0].Link); remark != "orphan-in" {
		t.Fatalf("unbound inbound should fall back to its tag, got %q", remark)
	}
}

func TestSelfBuiltLinks_RemarkPrefersNodeRemark(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "carol")
	pkg := mkPlan(t, st, "P", 10, 100, 30)
	buy(t, st, uid, pkg)

	if _, err := st.SaveSbInbound(&SbInbound{
		Type: "vless", Tag: "vless-in", Listen: "::", ListenPort: 8443,
		Options: "{}", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(Node{
		Type: "self_built", Name: "香港 01", Remark: "HK-VIP", Protocol: "vless",
		InboundTag: "vless-in", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	bindPlanToInbound(t, st, pkg.ID, "vless-in")

	u, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	links := st.BuildSelfBuiltLinks(u, "example.com")
	if len(links) != 1 {
		t.Fatalf("want 1 link, got %d", len(links))
	}
	if remark := subconv.LinkRemark(links[0].Link); remark != "HK-VIP" {
		t.Fatalf("remark should prefer node remark, got %q", remark)
	}
}
