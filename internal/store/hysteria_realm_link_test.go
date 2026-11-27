package store

import (
	"strings"
	"testing"
)

func TestSelfBuiltLinksHysteria2RealmCarrier(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "alice")
	pkg := mkPlan(t, st, "P", 10, 100, 30)
	buy(t, st, uid, pkg)

	opts := `{"up_mbps":100,"realm":{"server_url":"https://realm.example.com","token":"tok","realm_id":"home","stun_servers":["stun.example.com:3478"],"ip_version":4},"hop_interval":"30s"}`
	if _, err := st.SaveSbInbound(&SbInbound{
		Type: "hysteria2", Tag: "hy2-realm", Listen: "::", ListenPort: 443,
		Options: opts, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(Node{
		Type: "self_built", Name: "家里宽", Protocol: "hysteria2",
		InboundTag: "hy2-realm", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	bindPlanToInbound(t, st, pkg.ID, "hy2-realm")

	u, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	links := st.BuildSelfBuiltLinks(u, "203.0.113.10")
	if len(links) != 1 {
		t.Fatalf("links = %d", len(links))
	}
	link := links[0].Link
	for _, want := range []string{"qz-realm=1", "realm-id=home", "realm-server-url=https%3A%2F%2Frealm.example.com", "realm-token=tok", "realm-stun=stun.example.com%3A3478", "realm-ip-version=4", "hop-interval=30s"} {
		if !strings.Contains(link, want) {
			t.Errorf("link missing %s: %s", want, link)
		}
	}
}
