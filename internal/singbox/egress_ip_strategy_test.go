package singbox

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func outboundByTag(t *testing.T, raw []byte, tag string) map[string]interface{} {
	t.Helper()
	var cfg map[string]interface{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	list, _ := cfg["outbounds"].([]interface{})
	for _, it := range list {
		m, _ := it.(map[string]interface{})
		if m["tag"] == tag {
			return m
		}
	}
	t.Fatalf("outbound %q not found in %s", tag, raw)
	return nil
}

func domainResolver(m map[string]interface{}) map[string]interface{} {
	res, _ := m["domain_resolver"].(map[string]interface{})
	return res
}

func stringSlice(v interface{}) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []interface{}:
		out := make([]string, 0, len(x))
		for _, it := range x {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSourceFamilyEgressOffLeavesDefaultUntouched(t *testing.T) {
	raw, err := GenerateConfigWithOptions(json.RawMessage(DefaultBaseConfig),
		[]Inbound{vlessInbound("in")}, Options{BlockPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	direct := outboundByTag(t, raw, "direct")
	if _, ok := direct["domain_resolver"]; ok {
		t.Fatalf("default behaviour must not set domain_resolver: %v", direct)
	}
	if _, ok := direct["domain_strategy"]; ok {
		t.Fatalf("must not reintroduce domain_strategy: %v", direct)
	}
	if strings.Contains(string(raw), `"direct-v4pref"`) || strings.Contains(string(raw), `"direct-v6pref"`) {
		t.Fatalf("pref outbounds must not appear without host_v6: %s", raw)
	}
	rules := routeRules(t, raw)
	if len(rules) < 2 || !isResolve(rules[0]) || rules[0]["strategy"] != nil {
		t.Fatalf("default resolve must have no strategy: %v", rules)
	}
	if !isPrivateReject(rules[1]) {
		t.Fatalf("private reject missing: %v", rules)
	}
	if strings.Contains(string(raw), `"domain_strategy"`) {
		t.Fatal("legacy domain_strategy leaked into generated config")
	}
}

func TestSourceFamilyEgressAddsPrefOutboundsAndSplitResolve(t *testing.T) {
	raw, err := GenerateConfigWithOptions(json.RawMessage(DefaultBaseConfig),
		[]Inbound{vlessInbound("in")}, Options{BlockPrivate: true, FollowSourceFamilyEgress: true})
	if err != nil {
		t.Fatal(err)
	}
	direct := outboundByTag(t, raw, "direct")
	if _, ok := direct["domain_resolver"]; ok {
		t.Fatalf("original direct must stay unstrategied: %v", direct)
	}
	v4 := outboundByTag(t, raw, directIPv4PrefTag)
	v6 := outboundByTag(t, raw, directIPv6PrefTag)
	if r := domainResolver(v4); r["strategy"] != "prefer_ipv4" || r["server"] != "local" {
		t.Fatalf("v4pref resolver: %v", r)
	}
	if r := domainResolver(v6); r["strategy"] != "prefer_ipv6" || r["server"] != "local" {
		t.Fatalf("v6pref resolver: %v", r)
	}
	if _, ok := v4["domain_strategy"]; ok || strings.Contains(string(raw), `"domain_strategy"`) {
		t.Fatalf("legacy domain_strategy present: %s", raw)
	}

	rules := routeRules(t, raw)
	if len(rules) < 5 {
		t.Fatalf("expected split resolve + reject + two family routes, got %v", rules)
	}
	if !isResolve(rules[0]) || rules[0]["strategy"] != "prefer_ipv4" {
		t.Fatalf("rule[0] want prefer_ipv4 resolve: %v", rules[0])
	}
	if !sameStrings(stringSlice(rules[0]["source_ip_cidr"]), sourceIPv4CIDRs) {
		t.Fatalf("rule[0] cidr: %v", rules[0]["source_ip_cidr"])
	}
	if !sameStrings(resolvedInbounds(rules[0]), []string{"in"}) {
		t.Fatalf("rule[0] inbound: %v", rules[0]["inbound"])
	}
	if rules[1]["type"] != "logical" || rules[1]["action"] != "resolve" || rules[1]["strategy"] != "prefer_ipv6" {
		t.Fatalf("rule[1] want prefer_ipv6 logical resolve: %v", rules[1])
	}
	if !isPrivateReject(rules[2]) {
		t.Fatalf("rule[2] want private reject: %v", rules[2])
	}
	if !sameStrings(resolvedInbounds(rules[3]), []string{"in"}) ||
		!sameStrings(stringSlice(rules[3]["source_ip_cidr"]), sourceIPv4CIDRs) ||
		rules[3]["outbound"] != directIPv4PrefTag {
		t.Fatalf("rule[3] want v4pref route: %v", rules[3])
	}
	if !sameStrings(resolvedInbounds(rules[4]), []string{"in"}) ||
		rules[4]["outbound"] != directIPv6PrefTag || rules[4]["source_ip_cidr"] != nil {
		t.Fatalf("rule[4] want v6pref route for remaining sources: %v", rules[4])
	}
}

func TestSourceFamilyEgressKeepsRelaySteeringAheadAndUntouched(t *testing.T) {
	relay := Relay{
		Outbound:    map[string]interface{}{"type": "socks", "tag": "to-landing", "server": "203.0.113.9", "server_port": 1080},
		InboundTags: []string{"relayed"},
	}
	raw, err := GenerateConfigWithOptions(json.RawMessage(DefaultBaseConfig),
		[]Inbound{vlessInbound("direct-in"), vlessInbound("relayed")},
		Options{BlockPrivate: true, Relays: []Relay{relay}, FollowSourceFamilyEgress: true})
	if err != nil {
		t.Fatal(err)
	}
	socks := outboundByTag(t, raw, "to-landing")
	if _, ok := socks["domain_resolver"]; ok {
		t.Fatalf("relay outbound must not get domain_resolver: %v", socks)
	}
	if _, ok := socks["domain_strategy"]; ok {
		t.Fatalf("relay outbound must not get domain_strategy: %v", socks)
	}

	rules := routeRules(t, raw)
	var resolveV4At, rejectAt, relayAt, familyAt = -1, -1, -1, -1
	for i, r := range rules {
		if isResolve(r) && r["strategy"] == "prefer_ipv4" && resolveV4At < 0 {
			got := resolvedInbounds(r)
			if len(got) != 1 || got[0] != "direct-in" {
				t.Fatalf("prefer_ipv4 resolve applies to %v, want only [direct-in]", got)
			}
			resolveV4At = i
		}
		if isPrivateReject(r) && rejectAt < 0 {
			rejectAt = i
		}
		if r["outbound"] == "to-landing" && relayAt < 0 {
			relayAt = i
		}
		if (r["outbound"] == directIPv4PrefTag || r["outbound"] == directIPv6PrefTag) && familyAt < 0 {
			familyAt = i
		}
	}
	if resolveV4At < 0 || rejectAt < 0 || relayAt < 0 || familyAt < 0 {
		t.Fatalf("missing rules resolve=%d reject=%d relay=%d family=%d: %v", resolveV4At, rejectAt, relayAt, familyAt, rules)
	}
	if !(resolveV4At < rejectAt && rejectAt < relayAt && relayAt < familyAt) {
		t.Fatalf("order resolve=%d reject=%d relay=%d family=%d, want resolve < reject < relay < family: %v",
			resolveV4At, rejectAt, relayAt, familyAt, rules)
	}
	// Family routes must only name the locally-direct inbound.
	for _, r := range rules {
		if r["outbound"] == directIPv4PrefTag || r["outbound"] == directIPv6PrefTag {
			if r["type"] == "logical" {
				continue
			}
			if got := resolvedInbounds(r); !sameStrings(got, []string{"direct-in"}) {
				t.Fatalf("family route inbound %v, want [direct-in]", got)
			}
		}
	}
}

func TestSourceFamilyEgressSharedInboundKeepsRelayUsersUnsteeredByFamily(t *testing.T) {
	relay := Relay{
		Outbound:    map[string]interface{}{"type": "socks", "tag": "to-landing", "server": "203.0.113.9", "server_port": 1080},
		InboundTags: []string{"shared"},
		AuthUsers:   []string{"relay-user"},
	}
	raw, err := GenerateConfigWithOptions(json.RawMessage(DefaultBaseConfig),
		[]Inbound{vlessInbound("shared")},
		Options{BlockPrivate: true, Relays: []Relay{relay}, FollowSourceFamilyEgress: true})
	if err != nil {
		t.Fatal(err)
	}
	rules := routeRules(t, raw)
	var relayAt, familyLogicalAt = -1, -1
	for i, r := range rules {
		if r["outbound"] == "to-landing" && relayAt < 0 {
			relayAt = i
		}
		if r["type"] == "logical" && (r["outbound"] == directIPv4PrefTag || r["outbound"] == directIPv6PrefTag) && familyLogicalAt < 0 {
			familyLogicalAt = i
		}
	}
	if relayAt < 0 || familyLogicalAt < 0 || relayAt > familyLogicalAt {
		t.Fatalf("shared inbound: relay at %d, family at %d: %v", relayAt, familyLogicalAt, rules)
	}
}

func TestSourceFamilyEgressUsesDefaultDomainResolverTag(t *testing.T) {
	base := `{
	  "log": {"level": "warn"},
	  "dns": {"servers": [{"type": "udp", "tag": "cloud", "server": "1.1.1.1"}, {"type": "local", "tag": "local"}]},
	  "outbounds": [{"type": "direct", "tag": "direct"}],
	  "route": {"rules": [], "final": "direct", "default_domain_resolver": "cloud"}
	}`
	raw, err := GenerateConfigWithOptions(json.RawMessage(base),
		[]Inbound{vlessInbound("in")}, Options{FollowSourceFamilyEgress: true})
	if err != nil {
		t.Fatal(err)
	}
	v4 := outboundByTag(t, raw, directIPv4PrefTag)
	res, _ := v4["domain_resolver"].(map[string]interface{})
	if res["server"] != "cloud" || res["strategy"] != "prefer_ipv4" {
		t.Fatalf("resolver: %v", res)
	}
}

func TestNormalizeEgressIPStrategy(t *testing.T) {
	if s, ok := NormalizeEgressIPStrategy("  prefer_ipv4 "); !ok || s != "prefer_ipv4" {
		t.Fatalf("got %q %v", s, ok)
	}
	if _, ok := NormalizeEgressIPStrategy("ipv4"); ok {
		t.Fatal("invalid strategy accepted")
	}
	if s, ok := NormalizeEgressIPStrategy(""); !ok || s != "" {
		t.Fatalf("empty must be valid, got %q %v", s, ok)
	}
}

func TestSourceFamilyEgressPassesSingboxCheck(t *testing.T) {
	bins := []struct{ name, env string }{
		{"current", "QZ_SINGBOX_TEST_BIN"},
		{"1.14.2", "QZ_SINGBOX_TEST_BIN_1_14_2"},
		{"1.14.4", "QZ_SINGBOX_TEST_BIN_1_14_4"},
	}
	cert, key := writeTestCert(t)
	ibs := []Inbound{
		{
			Type: "vless",
			Base: map[string]interface{}{
				"type": "vless", "tag": "v-in", "listen": "::", "listen_port": 8443,
				"tls": map[string]interface{}{
					"enabled":     true,
					"server_name": "www.cloudflare.com",
					"reality": map[string]interface{}{
						"enabled":     true,
						"handshake":   map[string]interface{}{"server": "www.cloudflare.com", "server_port": 443},
						"private_key": "0KYjtg5OHGEGL9XWTB15a7b0QYD-lbn7IHr-vsF-YWs",
						"short_id":    []string{"abcd"},
					},
				},
			},
			Users: []User{{Name: "u1", UUID: "00000000-0000-0000-0000-000000000001"}},
		},
		{
			Type: "hysteria2",
			Base: map[string]interface{}{
				"type": "hysteria2", "tag": "hy2-in", "listen": "::", "listen_port": 8444,
				"tls": map[string]interface{}{
					"enabled":          true,
					"server_name":      "example.com",
					"certificate_path": cert,
					"key_path":         key,
				},
			},
			Users: []User{{Name: "u1", Password: "testpass"}},
		},
		{
			Type: "tuic",
			Base: map[string]interface{}{
				"type": "tuic", "tag": "tuic-in", "listen": "::", "listen_port": 8445,
				"congestion_control": "bbr",
				"tls": map[string]interface{}{
					"enabled":          true,
					"server_name":      "example.com",
					"alpn":             []string{"h3"},
					"certificate_path": cert,
					"key_path":         key,
				},
			},
			Users: []User{{Name: "u1", UUID: "00000000-0000-0000-0000-000000000001", Password: "testpass"}},
		},
	}
	raw, err := GenerateConfigWithOptions(json.RawMessage(DefaultBaseConfig), ibs,
		Options{BlockPrivate: true, FollowSourceFamilyEgress: true, V2RayListen: "127.0.0.1:18081"})
	if err != nil {
		t.Fatal(err)
	}

	var ran int
	for _, b := range bins {
		path := os.Getenv(b.env)
		if path == "" {
			continue
		}
		ran++
		version, err := exec.Command(path, "version").CombinedOutput()
		if err != nil {
			t.Fatalf("%s version: %v: %s", b.name, err, version)
		}
		cfg := raw
		if !strings.Contains(string(version), "with_v2ray_api") {
			var m map[string]interface{}
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			delete(m, "experimental")
			cfg, _ = json.MarshalIndent(m, "", "  ")
		}
		file := filepath.Join(t.TempDir(), b.name+"-family.json")
		if err := os.WriteFile(file, cfg, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(path, "check", "-c", file).CombinedOutput()
		if err != nil {
			t.Fatalf("%s check failed:\n%s\n%s", b.name, out, cfg)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "deprecated") {
				t.Errorf("%s deprecation: %s", b.name, strings.TrimSpace(line))
			}
		}
	}
	if ran == 0 {
		t.Skip("set QZ_SINGBOX_TEST_BIN (and optionally QZ_SINGBOX_TEST_BIN_1_14_2 / _1_14_4) to run this")
	}
}

func writeTestCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
