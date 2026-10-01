package singbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// serverFixture is the shape a real install generates: one inbound that exits
// from this machine and one steered into a relay.
func serverFixture(t *testing.T) []byte {
	t.Helper()
	// Users are not optional here: a mixed inbound with an empty users list is
	// dropped outright (it would otherwise be an open proxy), and a fixture
	// without them would generate a config that binds nothing.
	//
	// Two of them, because that is what a real mixed inbound now carries per
	// subscriber: the account-level credential the panel hands out plus the
	// per-bucket one kept alive for logins already saved elsewhere.
	user := []User{
		{Name: "tester", Password: "testpass"},
		{Name: "px_0123456789abcdef", Password: "0123456789abcdef0123456789abcdef"},
	}
	ibs := []Inbound{
		{Type: "mixed", Users: user, Base: map[string]interface{}{
			"type": "mixed", "tag": "direct-in", "listen": "127.0.0.1", "listen_port": 18894}},
		{Type: "mixed", Users: user, Base: map[string]interface{}{
			"type": "mixed", "tag": "relayed-in", "listen": "127.0.0.1", "listen_port": 18895}},
	}
	relay := Relay{
		Outbound: map[string]interface{}{
			"type": "socks", "tag": "to-landing", "server": "127.0.0.1", "server_port": 11080},
		InboundTags: []string{"relayed-in"},
	}
	raw, err := GenerateConfigWithOptions(json.RawMessage(DefaultBaseConfig), ibs,
		Options{BlockPrivate: true, Relays: []Relay{relay}})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return raw
}

// TestGeneratedServerConfigPassesSingboxCheck runs the real binary over the
// config a node would actually receive.
//
// Worth having because the guard rules are the kind of thing that reads fine
// and behaves differently: `action: "resolve"` needs a resolver the config
// actually defines, and a dangling reference is a startup failure, not a
// warning — on a node that would mean the panel silently stops being able to
// push any config at all, since an invalid config is never swapped in.
//
// Skipped unless QZ_SINGBOX_TEST_BIN points at a sing-box binary:
//
//	QZ_SINGBOX_TEST_BIN=/path/to/sing-box go test ./internal/singbox/ -run Singbox
func TestGeneratedServerConfigPassesSingboxCheck(t *testing.T) {
	bin := os.Getenv("QZ_SINGBOX_TEST_BIN")
	if bin == "" {
		t.Skip("set QZ_SINGBOX_TEST_BIN to a sing-box binary to run this")
	}
	raw := serverFixture(t)
	// Official binaries can still exercise the proxy configuration. Release
	// validation requires metering and must check the FULL generated config.
	version, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("sing-box version: %v: %s", err, version)
	}
	hasStats := strings.Contains(string(version), "with_v2ray_api")
	if os.Getenv("QZ_SINGBOX_REQUIRE_STATS") == "1" && !hasStats {
		t.Fatal("release sing-box binary lacks with_v2ray_api")
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("generated config is not valid JSON: %v", err)
	}
	if !hasStats {
		delete(cfg, "experimental")
	}
	trimmed, _ := json.MarshalIndent(cfg, "", "  ")

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, trimmed, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "check", "-c", path).CombinedOutput()
	if err != nil {
		t.Fatalf("sing-box rejected the config a node would receive:\n%s", out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "deprecated") {
			t.Logf("deprecation warning (not yet fatal): %s", strings.TrimSpace(line))
		}
	}
}

// TestDumpGeneratedServerConfig writes the fixture out so it can be driven with
// live traffic by hand. Not an assertion — a tap.
//
//	QZ_SINGBOX_DUMP=/tmp/gen.json go test ./internal/singbox/ -run TestDumpGenerated
func TestDumpGeneratedServerConfig(t *testing.T) {
	dst := os.Getenv("QZ_SINGBOX_DUMP")
	if dst == "" {
		t.Skip("set QZ_SINGBOX_DUMP to a path to write the generated config")
	}
	if err := os.WriteFile(dst, serverFixture(t), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", dst)
}
