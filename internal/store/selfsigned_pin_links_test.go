package store

import (
	"net/url"
	"strings"
	"testing"

	"qingzhou/internal/singbox"
	"qingzhou/internal/subconv"
)

// #116 end to end: a trojan inbound using a pasted self-signed certificate
// shared across nodes (insecure off, as the old source-based rule forced) gets
// a link that pins the certificate and embeds it as a trust anchor, without
// changing the node's NodeKey.
func TestSelfBuiltLinkPinsSelfSignedCert(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "pin")
	pkg := mkPlan(t, st, "P", 10, 100, 30)
	buy(t, st, uid, pkg)
	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert("ssl.mllt.vip", 3650)
	if err != nil {
		t.Fatal(err)
	}
	cid, err := st.SaveCert(&Cert{Name: "shared", Domain: "ssl.mllt.vip", Source: "paste", CertPEM: certPEM, KeyPEM: keyPEM})
	if err != nil {
		t.Fatal(err)
	}
	tid, err := st.SaveSbTls(&SbTls{Name: "shared", Mode: "tls", CertID: cid,
		ServerJSON: `{"enabled":true,"server_name":"ssl.mllt.vip"}`, ClientJSON: `{"insecure":false}`})
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
	links := st.BuildSelfBuiltLinks(u, "217.216.188.164")
	if len(links) != 1 {
		t.Fatalf("want 1 link, got %d", len(links))
	}
	link := links[0].Link
	q, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if q.Query().Get("qz-pin") != singbox.CertFingerprintSHA256(certPEM) {
		t.Fatalf("link lacks the certificate pin: %s", link)
	}
	if q.Query().Get("qz-cert") == "" {
		t.Fatalf("link lacks the trust anchor: %s", link)
	}
	ob, err := subconv.SingboxOutboundFromLink(link)
	if err != nil {
		t.Fatal(err)
	}
	tls, _ := ob["tls"].(map[string]any)
	if pem, _ := tls["certificate"].(string); !strings.Contains(pem, "BEGIN CERTIFICATE") || tls["server_name"] != "ssl.mllt.vip" {
		t.Fatalf("sing-box outbound does not trust the node certificate: %v", tls)
	}
	// Blocklist stability: the same link without the pin extensions keys the same.
	stripped := strings.NewReplacer("&qz-pin="+q.Query().Get("qz-pin"), "", "&qz-cert="+url.QueryEscape(q.Query().Get("qz-cert")), "").Replace(link)
	if stripped == link || subconv.NodeKey(stripped) != subconv.NodeKey(link) {
		t.Fatal("pin extensions must not change the NodeKey")
	}
}
