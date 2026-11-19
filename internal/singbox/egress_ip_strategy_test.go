package singbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

func TestDirectOutboundIPStrategyLeavesDefaultUntouched(t *testing.T) {
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
	if strings.Contains(string(raw), "domain_strategy") {
		t.Fatal("legacy domain_strategy leaked into generated config")
	}
}

func TestDirectOutboundIPStrategySetsDomainResolverOnly(t *testing.T) {
	for _, strategy := range []string{"prefer_ipv4", "prefer_ipv6", "ipv4_only", "ipv6_only"} {
		raw, err := GenerateConfigWithOptions(json.RawMessage(DefaultBaseConfig),
			[]Inbound{vlessInbound("in")}, Options{BlockPrivate: true, EgressIPStrategy: strategy})
		if err != nil {
			t.Fatalf("%s: %v", strategy, err)
		}
		direct := outboundByTag(t, raw, "direct")
		res, _ := direct["domain_resolver"].(map[string]interface{})
		if res["strategy"] != strategy {
			t.Fatalf("%s: domain_resolver.strategy = %v", strategy, res)
		}
		if res["server"] != "local" {
			t.Fatalf("%s: domain_resolver.server = %v, want local", strategy, res["server"])
		}
		if _, ok := direct["domain_strategy"]; ok {
			t.Fatalf("%s reintroduced domain_strategy: %v", strategy, direct)
		}
		if strings.Contains(string(raw), `"domain_strategy"`) {
			t.Fatalf("%s leaked domain_strategy", strategy)
		}
		// A socks/relay outbound must not inherit the direct preference.
		if strings.Contains(string(raw), `"type": "socks"`) {
			t.Fatal("unexpected socks outbound in this fixture")
		}
	}
}

func TestDirectOutboundIPStrategyIgnoresNonDirectOutbounds(t *testing.T) {
	relay := Relay{
		Outbound:    map[string]interface{}{"type": "socks", "tag": "to-landing", "server": "203.0.113.9", "server_port": 1080},
		InboundTags: []string{"relayed"},
	}
	raw, err := GenerateConfigWithOptions(json.RawMessage(DefaultBaseConfig),
		[]Inbound{vlessInbound("direct-in"), vlessInbound("relayed")},
		Options{BlockPrivate: true, Relays: []Relay{relay}, EgressIPStrategy: "ipv4_only"})
	if err != nil {
		t.Fatal(err)
	}
	direct := outboundByTag(t, raw, "direct")
	res, _ := direct["domain_resolver"].(map[string]interface{})
	if res["strategy"] != "ipv4_only" {
		t.Fatalf("direct: %v", direct)
	}
	socks := outboundByTag(t, raw, "to-landing")
	if _, ok := socks["domain_resolver"]; ok {
		t.Fatalf("relay outbound must not get domain_resolver: %v", socks)
	}
	if _, ok := socks["domain_strategy"]; ok {
		t.Fatalf("relay outbound must not get domain_strategy: %v", socks)
	}
}

func TestDirectOutboundIPStrategyUsesDefaultDomainResolverTag(t *testing.T) {
	base := `{
	  "log": {"level": "warn"},
	  "dns": {"servers": [{"type": "udp", "tag": "cloud", "server": "1.1.1.1"}, {"type": "local", "tag": "local"}]},
	  "outbounds": [{"type": "direct", "tag": "direct"}],
	  "route": {"rules": [], "final": "direct", "default_domain_resolver": "cloud"}
	}`
	raw, err := GenerateConfigWithOptions(json.RawMessage(base),
		[]Inbound{vlessInbound("in")}, Options{EgressIPStrategy: "prefer_ipv6"})
	if err != nil {
		t.Fatal(err)
	}
	direct := outboundByTag(t, raw, "direct")
	res, _ := direct["domain_resolver"].(map[string]interface{})
	if res["server"] != "cloud" || res["strategy"] != "prefer_ipv6" {
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

func TestDirectOutboundIPStrategyPassesSingboxCheck(t *testing.T) {
	bins := []struct{ name, env string }{
		{"current", "QZ_SINGBOX_TEST_BIN"},
		{"1.14.2", "QZ_SINGBOX_TEST_BIN_1_14_2"},
		{"1.14.4", "QZ_SINGBOX_TEST_BIN_1_14_4"},
	}
	var ran int
	for _, b := range bins {
		path := os.Getenv(b.env)
		if path == "" {
			continue
		}
		ran++
		for _, strategy := range []string{"", "prefer_ipv4", "prefer_ipv6", "ipv4_only", "ipv6_only"} {
			raw, err := GenerateConfigWithOptions(json.RawMessage(DefaultBaseConfig),
				[]Inbound{vlessInbound("in")}, Options{BlockPrivate: true, V2RayListen: "127.0.0.1:18081", EgressIPStrategy: strategy})
			if err != nil {
				t.Fatalf("%s %s generate: %v", b.name, strategy, err)
			}
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
			file := filepath.Join(t.TempDir(), b.name+"-"+strategy+".json")
			if err := os.WriteFile(file, cfg, 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(path, "check", "-c", file).CombinedOutput()
			if err != nil {
				t.Fatalf("%s check strategy=%q failed:\n%s\n%s", b.name, strategy, out, cfg)
			}
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "deprecated") {
					t.Errorf("%s strategy=%q deprecation: %s", b.name, strategy, strings.TrimSpace(line))
				}
			}
		}
	}
	if ran == 0 {
		t.Skip("set QZ_SINGBOX_TEST_BIN (and optionally QZ_SINGBOX_TEST_BIN_1_14_2 / _1_14_4) to run this")
	}
}
