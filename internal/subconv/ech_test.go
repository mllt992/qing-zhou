package subconv

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/singbox"

	"gopkg.in/yaml.v3"
)

func testECHBase64(t *testing.T) string {
	t.Helper()
	cfgPEM, _, err := singbox.GenerateECHKeypair("public.example.com")
	if err != nil {
		t.Fatalf("GenerateECHKeypair: %v", err)
	}
	b64 := singbox.ECHConfigBase64(cfgPEM)
	if b64 == "" {
		t.Fatal("empty ECH base64")
	}
	return b64
}

func TestECH_RenderedInSingboxAndClashAndCoexistsWithCertPin(t *testing.T) {
	echB64 := testECHBase64(t)
	certPEM, _, err := singbox.GenerateSelfSignedCert("node.example.com", 365)
	if err != nil {
		t.Fatal(err)
	}
	pin := singbox.CertFingerprintSHA256(certPEM)
	anchor := singbox.SelfSignedTrustAnchor(certPEM, "node.example.com", time.Now())

	protocols := []string{"vless", "vmess", "trojan", "tuic", "hysteria2", "anytls", "hysteria"}
	links := make([]string, 0, len(protocols))
	for _, typ := range protocols {
		links = append(links, singbox.BuildShareLink(singbox.LinkParams{
			Type: typ, Tag: "ech-" + typ, Host: "203.0.113.10", Port: 443,
			UUID: "11111111-1111-1111-1111-111111111111", Password: "secret",
			TLS: true, SNI: "node.example.com", Insecure: true,
			PinSHA256: pin, PinCert: anchor, ECHConfig: echB64,
			UpMbps: 100, DownMbps: 100,
		}))
	}

	proxies := ParseLinks(links)
	if len(proxies) != len(protocols) {
		t.Fatalf("parsed %d proxies, want %d", len(proxies), len(protocols))
	}

	// 1. sing-box JSON: tls.ech + tls.certificate (self-signed anchor) coexist!
	sbOut, err := Singbox(proxies, "")
	if err != nil {
		t.Fatalf("Singbox: %v", err)
	}
	var sbDoc struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(sbOut), &sbDoc); err != nil {
		t.Fatalf("unmarshal singbox: %v", err)
	}
	sbByTag := map[string]map[string]any{}
	for _, ob := range sbDoc.Outbounds {
		if tag, ok := ob["tag"].(string); ok {
			sbByTag[tag] = ob
		}
	}
	for _, typ := range protocols {
		ob := sbByTag["ech-"+typ]
		if ob == nil {
			t.Fatalf("sing-box missing outbound ech-%s", typ)
		}
		tlsMap, _ := ob["tls"].(map[string]any)
		echMap, _ := tlsMap["ech"].(map[string]any)
		if echMap["enabled"] != true {
			t.Errorf("sing-box %s missing tls.ech.enabled=true: %#v", typ, tlsMap)
		}
		cfgLines, _ := echMap["config"].([]any)
		if len(cfgLines) != 3 || cfgLines[0] != "-----BEGIN ECH CONFIGS-----" || cfgLines[1] != echB64 || cfgLines[2] != "-----END ECH CONFIGS-----" {
			t.Errorf("sing-box %s unexpected tls.ech.config: %#v", typ, cfgLines)
		}
		// Self-signed cert pin must not be overwritten by ECH (and vice versa).
		if cert, _ := tlsMap["certificate"].(string); !strings.Contains(cert, "BEGIN CERTIFICATE") {
			t.Errorf("sing-box %s lost pinned tls.certificate alongside ECH", typ)
		}
	}

	// 2. Clash YAML: ech-opts (bare base64, NO PEM armour) + fingerprint coexist!
	clashOut, err := Clash(proxies, "")
	if err != nil {
		t.Fatalf("Clash: %v", err)
	}
	var clashDoc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(clashOut), &clashDoc); err != nil {
		t.Fatalf("unmarshal clash: %v", err)
	}
	clashByName := map[string]map[string]any{}
	for _, pr := range clashDoc.Proxies {
		if name, ok := pr["name"].(string); ok {
			clashByName[name] = pr
		}
	}
	for _, typ := range protocols {
		pr := clashByName["ech-"+typ]
		if pr == nil {
			t.Fatalf("clash missing proxy ech-%s", typ)
		}
		opts, _ := pr["ech-opts"].(map[string]any)
		if opts["enable"] != true || opts["config"] != echB64 {
			t.Errorf("clash %s unexpected ech-opts: %#v", typ, opts)
		}
		if strings.Contains(str(opts["config"]), "BEGIN ECH") {
			t.Errorf("clash %s ech-opts.config must not carry PEM armour", typ)
		}
		if pr["fingerprint"] != pin {
			t.Errorf("clash %s lost fingerprint pin alongside ECH: %#v", typ, pr["fingerprint"])
		}
	}

	// 3. Round-trip import from Clash YAML back into proxies -> preserves ECH!
	reimported := ParseClashYAML(clashOut)
	if len(reimported) != len(protocols) {
		t.Fatalf("reimported %d proxies from Clash YAML, want %d", len(reimported), len(protocols))
	}
	for _, p := range reimported {
		cfg, _ := singbox.ParseECHLinkParam(p.tlsParam("ech"))
		if cfg != echB64 {
			t.Errorf("clash round-trip lost ECH on %s (%s): got %q", p.Protocol, p.Name, p.tlsParam("ech"))
		}
	}
}

func TestECH_DomainDNSQueryServerNameForm(t *testing.T) {
	link := "trojan://pw@203.0.113.10:443?security=tls&sni=node.example.com&ech=cloudflare-ech.com+https://dns.google/dns-query#qsn"
	proxies := ParseLinks([]string{link})
	if len(proxies) != 1 {
		t.Fatalf("parsed %d proxies", len(proxies))
	}

	sbOut, err := Singbox(proxies, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sbOut, `"query_server_name": "cloudflare-ech.com"`) {
		t.Errorf("sing-box missing query_server_name:\n%s", sbOut)
	}

	clashOut, err := Clash(proxies, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(clashOut, "query-server-name: cloudflare-ech.com") {
		t.Errorf("clash missing query-server-name:\n%s", clashOut)
	}
}

func TestECH_DisabledRegressionAndNodeKeyStability(t *testing.T) {
	echB64 := testECHBase64(t)
	for _, typ := range []string{"vless", "vmess", "trojan", "tuic", "hysteria2", "anytls"} {
		base := singbox.LinkParams{
			Type: typ, Tag: "n", Host: "203.0.113.10", Port: 443,
			UUID: "11111111-1111-1111-1111-111111111111", Password: "secret",
			TLS: true, SNI: "node.example.com",
		}
		plainLink := singbox.BuildShareLink(base)
		withECH := base
		withECH.ECHConfig = echB64
		echLink := singbox.BuildShareLink(withECH)

		if NodeKey(plainLink) != NodeKey(echLink) {
			t.Errorf("%s: enabling ECH changed NodeKey (%s vs %s)", typ, NodeKey(plainLink), NodeKey(echLink))
		}
		// Non-ECH link must not emit ech / ech-opts in any format.
		if strings.Contains(plainLink, "ech") {
			t.Errorf("%s: non-ECH link unexpectedly mentions ech: %s", typ, plainLink)
		}
		ps := ParseLinks([]string{plainLink})
		sbOut, err := Singbox(ps, "")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sbOut, `"ech"`) {
			t.Errorf("%s: non-ECH sing-box output unexpectedly contains ech", typ)
		}
		clashOut, err := Clash(ps, "")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(clashOut, "ech-opts") {
			t.Errorf("%s: non-ECH clash output unexpectedly contains ech-opts", typ)
		}
	}
}
