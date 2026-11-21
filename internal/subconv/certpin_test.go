package subconv

import (
	"strings"
	"testing"
	"time"

	"qingzhou/internal/singbox"
)

func pinnedLink(t *testing.T, typ string, withAnchor bool) (link, pin string) {
	t.Helper()
	certPEM, _, err := singbox.GenerateSelfSignedCert("ssl.mllt.vip", 3650)
	if err != nil {
		t.Fatal(err)
	}
	pin = singbox.CertFingerprintSHA256(certPEM)
	p := singbox.LinkParams{Type: typ, Tag: "n", Host: "217.216.188.164", Port: 443,
		UUID: "11111111-1111-1111-1111-111111111111", Password: "pw", TLS: true,
		SNI: "ssl.mllt.vip", Insecure: true, PinSHA256: pin}
	if withAnchor {
		p.PinCert = singbox.SelfSignedTrustAnchor(certPEM, "ssl.mllt.vip", time.Now())
	}
	return singbox.BuildShareLink(p), pin
}

// #116 / #85: a self-signed node is pinned in Clash (mihomo `fingerprint` is
// the leaf SHA-256) instead of skipping verification, for every TLS protocol.
func TestClashPinsSelfSignedInsteadOfSkipping(t *testing.T) {
	for _, typ := range []string{"vless", "vmess", "trojan", "tuic", "hysteria2", "hysteria", "anytls"} {
		link, pin := pinnedLink(t, typ, true)
		ps := ParseLinks([]string{link})
		if len(ps) != 1 {
			t.Fatalf("%s: link did not parse: %s", typ, link)
		}
		m := clashProxy(ps[0], ClashOptions{})
		if m["fingerprint"] != pin {
			t.Errorf("%s: clash fingerprint = %v, want %s", typ, m["fingerprint"], pin)
		}
		if _, ok := m["skip-cert-verify"]; ok {
			t.Errorf("%s: skip-cert-verify kept next to a pin", typ)
		}
	}
}

// sing-box: the embedded certificate replaces insecure, in every version.
func TestSingboxEmbedsSelfSignedTrustAnchor(t *testing.T) {
	for _, typ := range []string{"vless", "vmess", "trojan", "tuic", "hysteria2", "anytls"} {
		link, _ := pinnedLink(t, typ, true)
		ob, err := SingboxOutboundFromLink(link)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		tls, _ := ob["tls"].(map[string]any)
		if tls == nil {
			t.Fatalf("%s: no tls block", typ)
		}
		if pem, _ := tls["certificate"].(string); !strings.Contains(pem, "BEGIN CERTIFICATE") {
			t.Errorf("%s: tls.certificate missing: %v", typ, tls)
		}
		if _, ok := tls["insecure"]; ok {
			t.Errorf("%s: insecure kept next to an embedded certificate", typ)
		}
	}
}

// Without a verifiable anchor (e.g. the SNI is not covered), sing-box keeps
// today's insecure behavior; Clash still pins (fingerprint has no name check).
func TestSingboxFallsBackWithoutAnchor(t *testing.T) {
	link, pin := pinnedLink(t, "trojan", false)
	ob, err := SingboxOutboundFromLink(link)
	if err != nil {
		t.Fatal(err)
	}
	tls, _ := ob["tls"].(map[string]any)
	if tls["insecure"] != true || tls["certificate"] != nil {
		t.Fatalf("fallback must be unchanged insecure: %v", tls)
	}
	if m := clashProxy(ParseLinks([]string{link})[0], ClashOptions{}); m["fingerprint"] != pin {
		t.Fatalf("clash must still pin: %v", m)
	}
}

// Third-party links reach the same code: a malformed pin must never become a
// mihomo `fingerprint` (it would fail the whole config), and a qz-cert that
// contradicts the pin is ignored.
func TestMalformedPinsIgnored(t *testing.T) {
	ps := ParseLinks([]string{"hysteria2://pw@1.2.3.4:443?sni=a.example&insecure=1&pinSHA256=chrome#x"})
	m := clashProxy(ps[0], ClashOptions{})
	if _, ok := m["fingerprint"]; ok || m["skip-cert-verify"] != true {
		t.Fatalf("malformed pin leaked: %v", m)
	}
	link, _ := pinnedLink(t, "trojan", true)
	other, _ := pinnedLink(t, "trojan", false) // different certificate → different pin
	otherPin := ParseLinks([]string{other})[0].certPin()
	tampered := strings.Replace(link, "qz-pin="+ParseLinks([]string{link})[0].certPin(), "qz-pin="+otherPin, 1)
	if pem := ParseLinks([]string{tampered})[0].pinnedCertPEM(); pem != "" {
		t.Fatal("qz-cert contradicting qz-pin must be ignored")
	}
}

// Pinning must not re-key nodes: users' blocklists are keyed by NodeKey.
func TestPinsDoNotChangeNodeKey(t *testing.T) {
	certPEM, _, _ := singbox.GenerateSelfSignedCert("ssl.mllt.vip", 3650)
	for _, typ := range []string{"vless", "trojan", "tuic", "anytls", "vmess"} {
		p := singbox.LinkParams{Type: typ, Tag: "n", Host: "1.2.3.4", Port: 443,
			UUID: "11111111-1111-1111-1111-111111111111", Password: "pw", TLS: true, SNI: "ssl.mllt.vip", Insecure: true}
		plain := singbox.BuildShareLink(p)
		p.PinSHA256 = singbox.CertFingerprintSHA256(certPEM)
		p.PinCert = singbox.SelfSignedTrustAnchor(certPEM, "ssl.mllt.vip", time.Now())
		pinned := singbox.BuildShareLink(p)
		if plain == pinned {
			t.Fatalf("%s: pin not emitted", typ)
		}
		if NodeKey(plain) != NodeKey(pinned) {
			t.Errorf("%s: pinning changed NodeKey", typ)
		}
	}
}
