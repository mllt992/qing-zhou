package subconv

import (
	"strings"
	"testing"

	"qingzhou/internal/singbox"
)

// #108: sing-box rejects tls.utls on QUIC outbounds at dial time with
// "unsupported usage for uTLS". The panel's tuic/hysteria2 share links carry
// fp=chrome, so the sing-box rendering must drop it for QUIC protocols while
// keeping it for TCP protocols, and the share link itself must be unchanged.
func TestSingboxQUICOutboundsNeverCarryUTLS(t *testing.T) {
	links := map[string]string{
		"tuic": singbox.BuildShareLink(singbox.LinkParams{
			Type: "tuic", Tag: "t", Host: "1.2.3.4", Port: 443, UUID: "u", Password: "p",
			TLS: true, SNI: "node.example.com", ALPN: "h3", Fingerprint: "chrome",
		}),
		"hysteria2": singbox.BuildShareLink(singbox.LinkParams{
			Type: "hysteria2", Tag: "h", Host: "1.2.3.4", Port: 443, Password: "p",
			TLS: true, SNI: "node.example.com", ALPN: "h3", Fingerprint: "chrome",
		}),
		"hysteria": "hysteria://1.2.3.4:443?protocol=udp&auth=a&peer=node.example.com&alpn=h3&fp=chrome&upmbps=10&downmbps=50#hy",
	}
	for proto, link := range links {
		if link == "" {
			t.Fatalf("%s: empty share link", proto)
		}
		// The share link (v2ray/base64 output) keeps its fp= unchanged.
		if !strings.Contains(link, "fp=chrome") {
			t.Fatalf("%s: share link must keep fp=: %s", proto, link)
		}
		ob, err := SingboxOutboundFromLink(link)
		if err != nil || ob == nil {
			t.Fatalf("%s: SingboxOutboundFromLink(%q) = %v, %v", proto, link, ob, err)
		}
		if ob["type"] != proto {
			t.Fatalf("%s: outbound type = %v", proto, ob["type"])
		}
		tls, _ := ob["tls"].(map[string]any)
		if tls == nil || tls["enabled"] != true {
			t.Fatalf("%s: tls must stay enabled, got %#v", proto, ob["tls"])
		}
		if _, has := tls["utls"]; has {
			t.Fatalf("%s: QUIC outbound must not carry tls.utls, got %#v", proto, tls)
		}
		if tls["server_name"] != "node.example.com" {
			t.Fatalf("%s: server_name lost: %#v", proto, tls)
		}
	}
}

func TestSingboxTCPOutboundsKeepUTLS(t *testing.T) {
	for _, typ := range []string{"vless", "trojan", "anytls"} {
		link := singbox.BuildShareLink(singbox.LinkParams{
			Type: typ, Tag: "n", Host: "1.2.3.4", Port: 443, UUID: "u", Password: "p",
			TLS: true, SNI: "node.example.com", Fingerprint: "firefox",
		})
		if !strings.Contains(link, "fp=firefox") {
			t.Fatalf("%s: share link must carry fp=: %q", typ, link)
		}
		ob, err := SingboxOutboundFromLink(link)
		if err != nil || ob == nil {
			t.Fatalf("%s: SingboxOutboundFromLink(%q) = %v, %v", typ, link, ob, err)
		}
		tls, _ := ob["tls"].(map[string]any)
		u, _ := tls["utls"].(map[string]any)
		if u == nil || u["enabled"] != true || u["fingerprint"] != "firefox" {
			t.Fatalf("%s: TCP outbound must keep uTLS fingerprint, got %#v (link %s)", typ, tls, link)
		}
	}
}

func TestSbUTLSSupported(t *testing.T) {
	for _, p := range []string{"tuic", "hysteria", "hysteria2"} {
		if sbUTLSSupported(p) {
			t.Errorf("%s must not support uTLS", p)
		}
	}
	for _, p := range []string{"vless", "vmess", "trojan", "anytls"} {
		if !sbUTLSSupported(p) {
			t.Errorf("%s must support uTLS", p)
		}
	}
}

// Whole-document render through the public sing-box path, plus the Clash
// output for the same links, which must keep its client-fingerprint.
func TestSingboxRenderDropsUTLSOnlyForQUIC(t *testing.T) {
	mk := func(typ string) string {
		return singbox.BuildShareLink(singbox.LinkParams{
			Type: typ, Tag: typ, Host: "1.2.3.4", Port: 443, UUID: "u", Password: "p",
			TLS: true, SNI: "node.example.com", Fingerprint: "chrome",
		})
	}
	proxies := ParseLinks([]string{mk("tuic"), mk("hysteria2"), mk("vless")})
	cfg, err := Singbox(proxies, "")
	if err != nil {
		t.Fatalf("Singbox: %v", err)
	}
	for _, typ := range []string{"tuic", "hysteria2"} {
		if _, has := sbOutboundTLS(t, cfg, typ)["utls"]; has {
			t.Fatalf("%s outbound carries utls in rendered config", typ)
		}
	}
	if _, has := sbOutboundTLS(t, cfg, "vless")["utls"]; !has {
		t.Fatalf("vless outbound lost utls in rendered config")
	}
	clash, err := Clash(proxies, "")
	if err != nil {
		t.Fatalf("Clash: %v", err)
	}
	if n := strings.Count(clash, "client-fingerprint: chrome"); n < 1 {
		t.Fatalf("Clash output lost client-fingerprint:\n%s", clash)
	}
}
