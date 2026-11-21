package singbox

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSelfSignedTrustAnchor(t *testing.T) {
	certPEM, _, err := GenerateSelfSignedCert("ssl.mllt.vip", 3650)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	anchor := SelfSignedTrustAnchor(certPEM, "ssl.mllt.vip", now)
	if anchor == "" {
		t.Fatal("self-signed cert valid for its SNI must yield a trust anchor")
	}
	der, err := base64.RawURLEncoding.DecodeString(anchor)
	if err != nil || len(der) == 0 {
		t.Fatalf("anchor is not base64url DER: %v", err)
	}
	if TrustAnchorPEM(anchor) == "" || !strings.Contains(TrustAnchorPEM(anchor), "BEGIN CERTIFICATE") {
		t.Fatal("anchor must round-trip to PEM")
	}
	// The client would verify against the IP it dials: not covered → no anchor,
	// the node keeps relying on insecure exactly as before.
	if SelfSignedTrustAnchor(certPEM, "217.216.188.164", now) != "" {
		t.Fatal("anchor emitted for a name the certificate does not cover")
	}
	if SelfSignedTrustAnchor(certPEM, "", now) != "" {
		t.Fatal("anchor emitted without a server name")
	}
	ipPEM, _, _ := GenerateSelfSignedCert("203.0.113.9", 3650)
	if SelfSignedTrustAnchor(ipPEM, "[203.0.113.9]", now) == "" {
		t.Fatal("IP-SAN self-signed cert must anchor for its IP")
	}
	// Expiring within the cache window: sing-box would start rejecting a cached
	// subscription that insecure used to accept.
	shortPEM, _, _ := GenerateSelfSignedCert("ssl.mllt.vip", 10)
	if SelfSignedTrustAnchor(shortPEM, "ssl.mllt.vip", now) != "" {
		t.Fatal("anchor emitted for a certificate expiring within 30 days")
	}
	if SelfSignedTrustAnchor("garbage", "x", now) != "" || TrustAnchorPEM("!!") != "" {
		t.Fatal("garbage must yield nothing")
	}
}

func TestNormalizeCertPin(t *testing.T) {
	sum := sha256.Sum256([]byte("x"))
	h := hex.EncodeToString(sum[:])
	if NormalizeCertPin(h) != strings.ToUpper(h) {
		t.Fatal("lowercase hex must normalize")
	}
	var colons []string
	for i := 0; i < len(h); i += 2 {
		colons = append(colons, h[i:i+2])
	}
	if NormalizeCertPin(strings.Join(colons, ":")) != strings.ToUpper(h) {
		t.Fatal("colon-separated hex must normalize")
	}
	for _, bad := range []string{"", "chrome", "abcd", h + "00", "zz" + h[2:]} {
		if NormalizeCertPin(bad) != "" {
			t.Errorf("NormalizeCertPin(%q) must be empty", bad)
		}
	}
}

// Every plain-TLS protocol carries the pin and trust anchor (#116), not just
// the hysteria family; insecure stays on the link for clients that cannot pin.
func TestPinReachesEveryTLSProtocol(t *testing.T) {
	certPEM, _, _ := GenerateSelfSignedCert("node.example.com", 3650)
	pin := CertFingerprintSHA256(certPEM)
	anchor := SelfSignedTrustAnchor(certPEM, "node.example.com", time.Now())
	base := LinkParams{Tag: "n", Host: "1.2.3.4", Port: 443, UUID: "11111111-1111-1111-1111-111111111111", Password: "pw",
		TLS: true, SNI: "node.example.com", Insecure: true, PinSHA256: pin, PinCert: anchor}
	for _, typ := range []string{"vless", "trojan", "tuic", "anytls"} {
		p := base
		p.Type = typ
		link := BuildShareLink(p)
		u, err := url.Parse(link)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		q := u.Query()
		if q.Get("qz-pin") != pin || q.Get("qz-cert") != anchor {
			t.Errorf("%s link lacks pin/anchor: %s", typ, link)
		}
		if q.Get("allowInsecure") != "1" && q.Get("insecure") != "1" {
			t.Errorf("%s link dropped insecure: %s", typ, link)
		}
	}
	p := base
	p.Type = "hysteria2"
	if l := BuildShareLink(p); !strings.Contains(l, "pinSHA256="+pin) || !strings.Contains(l, "qz-cert=") || strings.Contains(l, "qz-pin=") {
		t.Errorf("hysteria2 must keep the standard pinSHA256 and add qz-cert: %s", l)
	}
	p.Type = "vmess"
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(BuildShareLink(p), "vmess://"))
	if !strings.Contains(string(raw), `"qz-pin":"`+pin+`"`) || !strings.Contains(string(raw), `"qz-cert":"`) {
		t.Errorf("vmess payload lacks pin/anchor: %s", raw)
	}
	// Reality and plaintext never carry a pin.
	p = base
	p.Type, p.PublicKey = "vless", "pbk"
	if l := BuildShareLink(p); strings.Contains(l, "qz-pin") {
		t.Errorf("reality link carries a certificate pin: %s", l)
	}
	p = base
	p.Type, p.TLS, p.TLSDisabled = "trojan", false, true
	if l := BuildShareLink(p); strings.Contains(l, "qz-pin") {
		t.Errorf("plaintext link carries a certificate pin: %s", l)
	}
}
