package store

import (
	"net/url"
	"strings"
	"testing"

	"qingzhou/internal/singbox"
	"qingzhou/internal/subconv"
)

// linkSNIScene: a trojan node on a managed certificate whose stored server_name
// is whatever the caller passes (legacy data, a swapped certificate, …).
func linkSNIScene(t *testing.T, certName, storedSNI, clientJSON string) (*Store, *User, int64) {
	t.Helper()
	st := newRefundStore(t)
	uid := mkUser(t, st, "sni")
	pkg := mkPlan(t, st, "P", 10, 100, 30)
	buy(t, st, uid, pkg)
	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert(certName, 3650)
	if err != nil {
		t.Fatal(err)
	}
	cid, err := st.SaveCert(&Cert{Name: "c", Domain: certName, Source: "paste", CertPEM: certPEM, KeyPEM: keyPEM})
	if err != nil {
		t.Fatal(err)
	}
	tid, err := st.SaveSbTls(&SbTls{Name: "p", Mode: "tls", CertID: cid,
		ServerJSON: `{"enabled":true,"server_name":"` + storedSNI + `"}`, ClientJSON: clientJSON})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSbInbound(&SbInbound{Type: "trojan", Tag: "tj", Listen: "::", ListenPort: 8443, TlsID: tid, Options: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(Node{Type: "self_built", Name: "tj", Protocol: "trojan", InboundTag: "tj", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	bindPlanToInbound(t, st, pkg.ID, "tj")
	u, _ := st.UserByID(uid)
	return st, u, tid
}

func linkSNI(t *testing.T, st *Store, u *User) (string, SelfBuiltLink) {
	t.Helper()
	links := st.BuildSelfBuiltLinks(u, "217.216.188.164")
	if len(links) != 1 {
		t.Fatalf("want 1 link, got %d", len(links))
	}
	q, err := url.Parse(links[0].Link)
	if err != nil {
		t.Fatal(err)
	}
	return q.Query().Get("sni"), links[0]
}

func inboundSNI(t *testing.T, st *Store, tid int64) string {
	t.Helper()
	block, err := st.resolveTlsBlock(tid, "tj", map[int64]*SbTls{}, map[int64]*Cert{})
	if err != nil {
		t.Fatal(err)
	}
	return mapStr(block, "server_name")
}

// #121: stored server_name differs from the managed certificate. The node
// serves the certificate's name, so the subscription must advertise that same
// name — one resolution for both — and the node keeps its old identity.
func TestLinkSNIMatchesNodeConfigWhenStoredDiffers(t *testing.T) {
	st, u, tid := linkSNIScene(t, "ssl.mllt.vip", "old.example", `{}`)
	got, link := linkSNI(t, st, u)
	if want := inboundSNI(t, st, tid); got != want || want != "ssl.mllt.vip" {
		t.Fatalf("link sni=%q, inbound server_name=%q, want both ssl.mllt.vip", got, want)
	}
	// The SNI correction re-keys the link; the pre-fix key (old SNI) must still
	// be offered so per-user blocklists keep matching the node.
	oldKey := subconv.NodeKey(strings.Replace(link.Link, "sni=ssl.mllt.vip", "sni=old.example", 1))
	found := false
	for _, k := range link.LegacyKeys {
		found = found || k == oldKey
	}
	if !found {
		t.Fatalf("legacy keys %v do not include the pre-fix key %s", link.LegacyKeys, oldKey)
	}
}

// Wildcard certificate: a concrete covered SNI is kept on both sides.
func TestLinkSNIWildcardKeepsConcreteName(t *testing.T) {
	st, u, tid := linkSNIScene(t, "*.mllt.vip", "node1.mllt.vip", `{}`)
	got, _ := linkSNI(t, st, u)
	if want := inboundSNI(t, st, tid); got != want || want != "node1.mllt.vip" {
		t.Fatalf("link sni=%q, inbound server_name=%q, want node1.mllt.vip", got, want)
	}
}

// Normal data (stored == certificate domain) is unchanged, and an explicit
// client-side server_name override still wins on the link.
func TestLinkSNIUnchangedForConsistentDataAndClientOverride(t *testing.T) {
	st, u, _ := linkSNIScene(t, "ssl.mllt.vip", "ssl.mllt.vip", `{}`)
	if got, _ := linkSNI(t, st, u); got != "ssl.mllt.vip" {
		t.Fatalf("consistent data must not change: sni=%q", got)
	}
	st, u, _ = linkSNIScene(t, "ssl.mllt.vip", "ssl.mllt.vip", `{"server_name":"front.example"}`)
	if got, _ := linkSNI(t, st, u); got != "front.example" {
		t.Fatalf("client override must win, got %q", got)
	}
}
