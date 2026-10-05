package store

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"qingzhou/internal/sbver"
	"qingzhou/internal/singbox"
)

// This is an explicit capability matrix for the fixed test core, not an
// inference that arbitrary protocol/transport/TLS combinations are supported.
// All entries carry TCP and UDP. AnyTLS carries UDP through its built-in UoT;
// QUIC protocols have their own multiplexing and must not get a V2Ray mux field.
// Mixed is an entry-only SOCKS/HTTP driver, never a managed landing protocol.
// These cases do not claim coverage for Reality, ECH, or every possible TLS,
// transport and multiplex combination; configuration checks are not traffic.
type relayProtocolFixture struct {
	protocol string
	tlsMode  string // plain, tls, tls-vision, http-tls, ws-tls, quic-tls, grpc-tls, httpupgrade-tls
	method   string // only SS2022 AES-128/AES-256 support this multi-user fixture
	mux      bool   // explicit V2Ray multiplex, incompatible with VLESS Vision
	variant  string // explicitly tested protocol-specific options
}

type relayTrafficScenario struct {
	hops        []relayProtocolFixture
	checkOnly   bool
	wsStability bool // additional TLS reverse-proxy/long-flow recovery exercise
}

func (p relayProtocolFixture) hasTLS() bool { return p.tlsMode != "" && p.tlsMode != "plain" }
func (p relayProtocolFixture) description() string {
	name := p.protocol
	if p.method != "" {
		name = strings.TrimPrefix(p.method, "2022-blake3-")
	}
	name += "-" + p.tlsMode
	if p.mux {
		name += "-mux"
	}
	if p.variant != "" {
		name += "-" + p.variant
	}
	return name
}
func (s relayTrafficScenario) description() string {
	parts := make([]string, len(s.hops))
	for i, hop := range s.hops {
		parts[i] = hop.description()
	}
	return strings.Join(parts, "_to_")
}
func (s relayTrafficScenario) hasTLS() bool {
	for _, hop := range s.hops {
		if hop.hasTLS() {
			return true
		}
	}
	return false
}
func (s relayTrafficScenario) hasVision() bool {
	for _, hop := range s.hops {
		if hop.tlsMode == "tls-vision" {
			return true
		}
	}
	return false
}

// hasTrojan marks paths terminating a Trojan inbound (#87 handshake patch).
func (s relayTrafficScenario) hasTrojan() bool {
	for _, hop := range s.hops {
		if hop.protocol == "trojan" {
			return true
		}
	}
	return false
}

func (p relayProtocolFixture) transport() map[string]any {
	switch p.tlsMode {
	case "http-tls":
		return map[string]any{"type": "http", "host": []string{"localhost"}, "path": "/fixture", "method": "POST", "headers": map[string]any{"X-QZ-Fixture-Transport": []string{"http"}}}
	case "quic-tls":
		return map[string]any{"type": "quic"}
	case "ws-tls":
		return map[string]any{"type": "ws", "path": "/fixture", "headers": map[string]any{"Host": "localhost"}, "max_early_data": 2048, "early_data_header_name": "Sec-WebSocket-Protocol"}
	case "grpc-tls":
		return map[string]any{"type": "grpc", "service_name": "fixture-relay"}
	case "httpupgrade-tls":
		return map[string]any{"type": "httpupgrade", "path": "/fixture", "host": "localhost"}
	default:
		return nil
	}
}

// Transport-specific ALPN follows the actual wire protocol. HTTP and gRPC
// use HTTP/2, while WS/HTTPUpgrade require HTTP/1.1 upgrades. This fixture
// correction does not work around or fix HTTPUpgrade buffered-byte loss.
// Native QUIC protocols keep their own defaults; V2Ray QUIC explicitly uses h3.
func (p relayProtocolFixture) alpn() []string {
	if !p.hasTLS() {
		return nil
	}
	switch p.tlsMode {
	case "ws-tls", "httpupgrade-tls":
		return []string{"http/1.1"}
	case "http-tls", "grpc-tls":
		return []string{"h2"}
	case "quic-tls":
		return []string{"h3"}
	}
	switch p.protocol {
	case "vless", "vmess", "trojan":
		return []string{"h2", "http/1.1"}
	default:
		return nil
	}
}

func relayFixtureTLSProfile(t *testing.T, raw string, p relayProtocolFixture) string {
	t.Helper()
	if len(p.alpn()) == 0 {
		return raw
	}
	var tls map[string]any
	if err := json.Unmarshal([]byte(raw), &tls); err != nil {
		t.Fatal(err)
	}
	tls["alpn"] = p.alpn()
	encoded, err := json.Marshal(tls)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func (p relayProtocolFixture) serverKey() string {
	return singbox.DeriveSSKey("synthetic-fixture-server-key", p.method)
}

func (p relayProtocolFixture) inboundOptions() map[string]any {
	opts := map[string]any{}
	if p.protocol == "vless" && p.hasTLS() && p.tlsMode != "tls-vision" {
		opts["flow"] = "none"
	}
	if transport := p.transport(); transport != nil {
		opts["transport"] = transport
	}
	if p.mux {
		opts["multiplex"] = map[string]any{"enabled": true, "padding": true}
	}
	switch p.protocol {
	case "hysteria":
		// v1 requires nonzero bandwidth. A string obfs exercises the v1 form,
		// which must not be confused with Hysteria2's object-valued obfs.
		opts["up_mbps"], opts["down_mbps"] = 100, 120
		opts["obfs"] = "synthetic-fixture-hy1-obfs"
	case "hysteria2":
		if p.variant == "forced-bandwidth-salamander" {
			opts["up_mbps"], opts["down_mbps"] = 100, 120
			opts["ignore_client_bandwidth"] = true
			opts["obfs"] = map[string]any{"type": "salamander", "password": "synthetic-fixture-hy2-obfs"}
		}
	case "tuic":
		opts["congestion_control"] = "cubic"
	case "shadowsocks":
		opts["method"], opts["password"] = p.method, p.serverKey()
		if p.variant == "tcp-only-listener" {
			opts["network"] = "tcp"
		}
	}
	return opts
}

// The original client uses the account's pre-existing credential, not the
// generated relay credential or the renderer being tested. Keep this independent
// of relayOutboundWithIdentity so renderer defects cannot change both peers.
func (p relayProtocolFixture) clientOutbound(original *User, port int, clientTLS map[string]any) map[string]any {
	out := map[string]any{"type": p.protocol, "tag": "original-customer", "server": "127.0.0.1", "server_port": port}
	switch p.protocol {
	case "vless":
		out["uuid"] = original.ClientUUID.String
		out["packet_encoding"] = "xudp"
		if p.tlsMode == "tls-vision" {
			out["flow"] = "xtls-rprx-vision"
		}
	case "vmess":
		out["uuid"], out["security"], out["alter_id"] = original.ClientUUID.String, "auto", 0
	case "tuic":
		out["uuid"], out["password"] = original.ClientUUID.String, original.ClientSecret.String
		out["congestion_control"], out["udp_relay_mode"] = "cubic", "native"
	case "hysteria":
		out["auth_str"] = original.ClientSecret.String
		out["up_mbps"], out["down_mbps"] = 120, 100
		out["obfs"] = "synthetic-fixture-hy1-obfs"
	case "hysteria2":
		out["password"] = original.ClientSecret.String
		if p.variant == "forced-bandwidth-salamander" {
			out["up_mbps"], out["down_mbps"] = 120, 100
			out["obfs"] = map[string]any{"type": "salamander", "password": "synthetic-fixture-hy2-obfs"}
		}
	case "shadowsocks":
		out["method"] = p.method
		out["password"] = p.serverKey() + ":" + singbox.DeriveSSKey(original.ClientSecret.String, p.method)
	default: // Trojan and AnyTLS authenticate using the stable secret.
		out["password"] = original.ClientSecret.String
	}
	if p.hasTLS() {
		tls := make(map[string]any, len(clientTLS)+1)
		for k, v := range clientTLS {
			tls[k] = v
		}
		if alpn := p.alpn(); len(alpn) > 0 {
			tls["alpn"] = alpn
		}
		out["tls"] = tls
	}
	if transport := p.transport(); transport != nil {
		out["transport"] = transport
	}
	if p.mux {
		out["multiplex"] = map[string]any{"enabled": true, "protocol": "smux", "padding": true}
	}
	return out
}

func relayFixtureCore(t *testing.T) (string, sbver.Info) {
	t.Helper()
	bin := os.Getenv("QZ_SINGBOX_TEST_BIN")
	if bin == "" {
		if os.Getenv("QZ_SINGBOX_REQUIRE_STATS") == "1" {
			t.Fatal("QZ_SINGBOX_REQUIRE_STATS=1 requires QZ_SINGBOX_TEST_BIN; integration must not silently skip")
		}
		t.Skip("set QZ_SINGBOX_TEST_BIN for loopback integration")
	}
	output, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("read actual fixture core version: %v %s", err, output)
	}
	info := sbver.Parse(string(output))
	if info.Version != sbver.TrojanHandshakeFixVersion || !info.HasV2RayAPI || !info.HasTransportReadBufferFix || !info.HasTrojanHandshakeFix || !info.HasVisionFramingFix {
		t.Fatalf("fixture requires the fixed %s core with with_v2ray_api, Vision, transport buffered-read and Trojan handshake capability; got %+v", sbver.TrojanHandshakeFixVersion, info)
	}
	t.Logf("actual integration core: %s", strings.TrimSpace(string(output)))
	return bin, info
}

func checkRelayFixtureProtocols(t *testing.T, raw []byte, inbound relayProtocolFixture, outbound *relayProtocolFixture) {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	check := func(endpoint map[string]any, want relayProtocolFixture, isInbound bool) {
		t.Helper()
		if endpoint["type"] != want.protocol {
			t.Fatalf("fixture protocol=%v, want %s", endpoint["type"], want.protocol)
		}
		tls, _ := endpoint["tls"].(map[string]any)
		if want.hasTLS() {
			if tls["enabled"] != true || tls["insecure"] == true {
				t.Fatalf("%s fixture TLS disabled or certificate verification weakened", want.description())
			}
			// Do not let SSL_CERT_FILE make a renderer that discarded the
			// listener's configured trust anchor look correct.
			if tls["certificate"] == nil || tls["server_name"] != "localhost" {
				t.Fatalf("%s fixture TLS certificate/SNI was not preserved", want.description())
			}
		} else if tls != nil {
			t.Fatalf("%s fixture unexpectedly acquired TLS", want.description())
		}
		if want.tlsMode == "quic-tls" {
			utls, _ := tls["utls"].(map[string]any)
			if utls["enabled"] == true {
				t.Fatal("V2Ray QUIC requires standard TLS; uTLS cannot supply the core's QUIC STDConfig")
			}
		}
		transport, _ := endpoint["transport"].(map[string]any)
		if expected := want.transport(); expected != nil {
			for k, v := range expected {
				gotJSON, _ := json.Marshal(transport[k])
				wantJSON, _ := json.Marshal(v)
				if string(gotJSON) != string(wantJSON) {
					t.Fatalf("%s transport field %s=%v, want %v", want.description(), k, transport[k], v)
				}
			}
		} else if len(transport) != 0 {
			t.Fatalf("%s acquired unsupported transport: %v", want.description(), transport)
		}
		mux, _ := endpoint["multiplex"].(map[string]any)
		if (mux["enabled"] == true) != want.mux {
			t.Fatalf("%s multiplex=%v, want enabled=%t", want.description(), mux, want.mux)
		}
		if want.mux && mux["padding"] != true {
			t.Fatalf("%s multiplex padding was not preserved", want.description())
		}
		if alpn := want.alpn(); len(alpn) > 0 {
			gotJSON, _ := json.Marshal(tls["alpn"])
			wantJSON, _ := json.Marshal(alpn)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("%s ALPN=%s, want %s", want.description(), gotJSON, wantJSON)
			}
		}
		if want.protocol == "vless" {
			checkFlow := func(user map[string]any) {
				t.Helper()
				flow, _ := user["flow"].(string)
				expected := ""
				if want.tlsMode == "tls-vision" {
					expected = "xtls-rprx-vision"
				}
				if flow != expected {
					t.Fatalf("%s flow=%q, want %q", want.description(), flow, expected)
				}
			}
			if isInbound {
				users, _ := endpoint["users"].([]any)
				for _, user := range users {
					checkFlow(user.(map[string]any))
				}
			} else {
				checkFlow(endpoint)
			}
		}
		if want.protocol == "shadowsocks" {
			if endpoint["method"] != want.method {
				t.Fatalf("SS2022 method=%v, want %s", endpoint["method"], want.method)
			}
			if want.variant == "tcp-only-listener" {
				if isInbound && endpoint["network"] != "tcp" {
					t.Fatal("SS2022 fixture must bind a TCP-only physical listener")
				}
				if !isInbound && endpoint["network"] != nil {
					t.Fatal("SS2022 TCP mux must retain UDP payload support")
				}
			}
		}
		if want.protocol == "hysteria2" && want.variant == "forced-bandwidth-salamander" {
			up, down := float64(100), float64(120)
			if !isInbound {
				up, down = down, up
			}
			obfs, _ := endpoint["obfs"].(map[string]any)
			if endpoint["up_mbps"] != up || endpoint["down_mbps"] != down || obfs["type"] != "salamander" || obfs["password"] != "synthetic-fixture-hy2-obfs" {
				t.Fatal("Hysteria2 forced-bandwidth peer directions or obfuscation were not preserved")
			}
			if isInbound && endpoint["ignore_client_bandwidth"] != true {
				t.Fatal("Hysteria2 fixture lost its forced-bandwidth server requirement")
			}
		}
		if want.protocol == "hysteria" {
			up, down := float64(100), float64(120)
			if !isInbound {
				up, down = down, up // peer directions are relative to each endpoint
			}
			if endpoint["up_mbps"] != up || endpoint["down_mbps"] != down || endpoint["obfs"] != "synthetic-fixture-hy1-obfs" {
				t.Fatal("Hysteria v1 bandwidth directions or string obfuscation were not preserved")
			}
		}
	}
	inbounds, _ := config["inbounds"].([]any)
	if len(inbounds) != 1 {
		t.Fatalf("fixture requires exactly one physical shared inbound, got %d", len(inbounds))
	}
	check(inbounds[0].(map[string]any), inbound, true)
	outbounds, _ := config["outbounds"].([]any)
	checked := 0
	for _, value := range outbounds {
		endpoint := value.(map[string]any)
		if endpoint["type"] == "direct" {
			continue
		}
		if outbound == nil {
			t.Fatalf("last hop unexpectedly has proxy outbound type=%v", endpoint["type"])
		}
		check(endpoint, *outbound, false)
		checked++
	}
	if outbound != nil && checked == 0 {
		t.Fatal("fixture has no managed/client protocol outbound")
	}
}

func relayProtocolTrafficMatrix() []relayTrafficScenario {
	base := []relayProtocolFixture{
		{protocol: "vless", tlsMode: "plain"},
		{protocol: "vmess", tlsMode: "plain"},
		{protocol: "trojan", tlsMode: "tls"},
		{protocol: "tuic", tlsMode: "tls"},
		{protocol: "hysteria", tlsMode: "tls"},
		{protocol: "hysteria2", tlsMode: "tls"},
		{protocol: "anytls", tlsMode: "tls"},
		{protocol: "shadowsocks", tlsMode: "plain", method: "2022-blake3-aes-128-gcm"},
		{protocol: "shadowsocks", tlsMode: "plain", method: "2022-blake3-aes-256-gcm"},
	}
	var scenarios []relayTrafficScenario
	for _, protocol := range base {
		scenarios = append(scenarios,
			relayTrafficScenario{hops: []relayProtocolFixture{protocol, protocol}},
			relayTrafficScenario{hops: []relayProtocolFixture{{protocol: "mixed", tlsMode: "plain"}, protocol}},
		)
	}
	for _, protocol := range []string{"vless", "vmess", "trojan"} {
		for _, mode := range []string{"tls", "http-tls", "ws-tls", "quic-tls", "grpc-tls", "httpupgrade-tls"} {
			if protocol == "trojan" && mode == "tls" {
				continue // already present in the mandatory base matrix
			}
			p := relayProtocolFixture{protocol: protocol, tlsMode: mode}
			scenarios = append(scenarios, relayTrafficScenario{hops: []relayProtocolFixture{p, p}})
		}
		for _, mode := range []string{"tls", "ws-tls"} {
			p := relayProtocolFixture{protocol: protocol, tlsMode: mode, mux: true}
			scenarios = append(scenarios, relayTrafficScenario{hops: []relayProtocolFixture{p, p}})
		}
	}
	for _, baseSS := range base[7:] {
		p := baseSS
		p.mux = true
		scenarios = append(scenarios, relayTrafficScenario{hops: []relayProtocolFixture{p, p}})
		// This network option limits the listening socket, not the payload
		// carried by mux. UDP must still cross the TCP-only SS listener.
		p.variant = "tcp-only-listener"
		scenarios = append(scenarios, relayTrafficScenario{hops: []relayProtocolFixture{p, p}})
	}
	forcedHY2 := base[5]
	forcedHY2.variant = "forced-bandwidth-salamander"
	scenarios = append(scenarios, relayTrafficScenario{hops: []relayProtocolFixture{forcedHY2, forcedHY2}})
	// Each three-hop path adds C directly at hop 1. C must not appear at hop 0
	// or be charged at hop 2, while A and B stay independent on all machines.
	scenarios = append(scenarios,
		relayTrafficScenario{hops: []relayProtocolFixture{base[1], base[2], base[5]}},
		relayTrafficScenario{hops: []relayProtocolFixture{base[7], base[3], base[8]}},
		relayTrafficScenario{hops: []relayProtocolFixture{base[4], base[6], base[0]}},
	)
	return scenarios
}

// Names deliberately retain TestMeteringRelayRealSingbox so CI and release
// filters execute the full matrix. Configuration-only cases never substitute
// for traffic cases, which start distinct cores and execute both TCP and UDP.
func TestMeteringRelayRealSingboxProtocolMatrix(t *testing.T) {
	bin, coreInfo := relayFixtureCore(t)
	for _, scenario := range relayProtocolTrafficMatrix() {
		for _, phase := range []string{"config-check", "traffic"} {
			scenario.checkOnly = phase == "config-check"
			t.Run(fmt.Sprintf("%s/%s", scenario.description(), phase), func(t *testing.T) {
				runRelaySharedUserPath(t, bin, coreInfo, scenario)
			})
		}
	}
}

// Keep coverage from silently shrinking when the integration test is refactored.
// This is a matrix-definition test only; it does not claim any network evidence.
func TestRelayProtocolTrafficMatrixCoverage(t *testing.T) {
	wantProtocols := []string{"vless", "vmess", "trojan", "tuic", "hysteria", "hysteria2", "anytls", "shadowsocks/2022-blake3-aes-128-gcm", "shadowsocks/2022-blake3-aes-256-gcm"}
	key := func(p relayProtocolFixture) string {
		if p.method != "" {
			return p.protocol + "/" + p.method
		}
		return p.protocol
	}
	self, mixed, names := map[string]bool{}, map[string]bool{}, map[string]bool{}
	transports := map[string]map[string]bool{}
	var crossProtocolThreeHop int
	for _, scenario := range relayProtocolTrafficMatrix() {
		name := scenario.description()
		if names[name] {
			t.Fatalf("duplicate matrix path %s", name)
		}
		names[name] = true
		if len(scenario.hops) < 2 || len(scenario.hops) > 3 {
			t.Fatalf("fixture requires two or three machines: %s", name)
		}
		for i, hop := range scenario.hops {
			if hop.protocol == "mixed" && i != 0 {
				t.Fatalf("mixed is entry-only: %s", name)
			}
			if hop.mux {
				switch hop.protocol {
				case "vless", "vmess", "trojan", "shadowsocks":
				default:
					t.Fatalf("native protocol cannot accept V2Ray multiplex: %s", name)
				}
				if hop.tlsMode == "tls-vision" {
					t.Fatalf("Vision and multiplex cannot be combined: %s", name)
				}
			}
			if hop.transport() != nil && hop.protocol != "vless" && hop.protocol != "vmess" && hop.protocol != "trojan" {
				t.Fatalf("unsupported V2Ray transport: %s", name)
			}
		}
		if len(scenario.hops) == 2 {
			a, b := key(scenario.hops[0]), key(scenario.hops[1])
			if a == b {
				self[b] = true
				if tr := scenario.hops[0].transport(); tr != nil {
					if transports[b] == nil {
						transports[b] = map[string]bool{}
					}
					transports[b][tr["type"].(string)] = true
				}
			}
			if a == "mixed" {
				mixed[b] = true
			}
		} else if key(scenario.hops[0]) != key(scenario.hops[1]) && key(scenario.hops[1]) != key(scenario.hops[2]) {
			crossProtocolThreeHop++
		}
	}
	for _, protocol := range wantProtocols {
		if !self[protocol] || !mixed[protocol] {
			t.Errorf("protocol %s needs both P-to-P and mixed-to-P shared-user paths", protocol)
		}
	}
	for _, protocol := range []string{"vless", "vmess", "trojan"} {
		for _, transport := range []string{"http", "ws", "quic", "grpc", "httpupgrade"} {
			if !transports[protocol][transport] {
				t.Errorf("protocol %s needs an explicit %s transport path", protocol, transport)
			}
		}
	}
	if crossProtocolThreeHop < 3 {
		t.Fatalf("need the three independent mixed-protocol middle-entry paths, got %d", crossProtocolThreeHop)
	}
	t.Logf("defined %d distinct TCP+UDP paths; traffic is verified only by TestMeteringRelayRealSingboxProtocolMatrix", len(names))
}

func TestRelayProtocolTransportALPN(t *testing.T) {
	for _, protocol := range []string{"vless", "vmess", "trojan"} {
		for mode, want := range map[string]string{
			"plain": "", "tls": "h2,http/1.1", "tls-vision": "h2,http/1.1",
			"http-tls": "h2", "grpc-tls": "h2", "quic-tls": "h3",
			"ws-tls": "http/1.1", "httpupgrade-tls": "http/1.1",
		} {
			if mode == "tls-vision" && protocol != "vless" {
				continue
			}
			p := relayProtocolFixture{protocol: protocol, tlsMode: mode}
			if got := strings.Join(p.alpn(), ","); got != want {
				t.Errorf("%s/%s ALPN=%q, want %q", protocol, mode, got, want)
			}
		}
	}
}
