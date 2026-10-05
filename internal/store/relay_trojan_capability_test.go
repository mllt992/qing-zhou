package store

import (
	"strings"
	"testing"

	"qingzhou/internal/sbver"
)

// #87: Trojan paths need the exact maintenance-patch marker; older Vision or
// transport markers, spoofed flags or suffixed versions never satisfy it.
func TestRelayCoreRequirementsTrojanUsesExactMarker(t *testing.T) {
	need := RelayCoreRequirements{TrojanHandshake: true}
	for _, version := range []string{sbver.VisionFramingFixVersion, sbver.TransportReadBufferFixVersion, sbver.TrojanHandshakeFixVersion + "-extra"} {
		if need.SupportedBy(sbver.Parse("sing-box version " + version + "\nTags: with_v2ray_api")) {
			t.Fatalf("%s satisfied the Trojan requirement", version)
		}
	}
	spoofed := sbver.Info{Version: sbver.TransportReadBufferFixVersion, HasV2RayAPI: true, HasVisionFramingFix: true, HasTransportReadBufferFix: true, HasTrojanHandshakeFix: true}
	if need.SupportedBy(spoofed) {
		t.Fatal("capability must derive from the version marker, not a spoofed flag")
	}
	fixed := sbver.Parse("sing-box version " + sbver.TrojanHandshakeFixVersion + "\nTags: with_v2ray_api")
	all := RelayCoreRequirements{VisionFraming: true, TransportReadBuffer: true, TrojanHandshake: true}
	if !need.SupportedBy(fixed) || !all.SupportedBy(fixed) {
		t.Fatal("current core must satisfy Vision, transport and Trojan requirements")
	}
	if noStats := sbver.Parse("sing-box version " + sbver.TrojanHandshakeFixVersion); need.SupportedBy(noStats) {
		t.Fatal("v2ray_api remains mandatory")
	}
	if need.FixedVersion() != sbver.TrojanHandshakeFixVersion || !strings.Contains(need.Label(), "Trojan") || !strings.Contains(all.Label(), "Vision 与 WebSocket/HTTPUpgrade 与 Trojan") {
		t.Fatalf("label/version: %q %q %q", need.Label(), all.Label(), need.FixedVersion())
	}
	for want, r := range map[string]RelayCoreRequirements{"Vision": {VisionFraming: true}, "WebSocket/HTTPUpgrade": {TransportReadBuffer: true}, "Vision 与 WebSocket/HTTPUpgrade": {VisionFraming: true, TransportReadBuffer: true}} {
		if r.Label() != want {
			t.Fatalf("existing label changed: %q want %q", r.Label(), want)
		}
	}
}

func TestRelayCoreConfigTrojanRequirementIsServerSideOnly(t *testing.T) {
	st := newRefundStore(t)
	rules := `"route":{"rules":[{"inbound":["entry"],"outbound":"relay-link-1-u1-g1"}]}`
	cases := map[string]bool{
		// Entry runs only a Trojan client outbound: no server-side patch needed.
		`{"inbounds":[{"type":"vless","tag":"entry"}],"outbounds":[{"type":"trojan","tag":"relay-link-1-u1-g1"}],` + rules + `}`: false,
		// Entry terminates Trojan itself.
		`{"inbounds":[{"type":"trojan","tag":"entry"}],"outbounds":[{"type":"vless","tag":"relay-link-1-u1-g1"}],` + rules + `}`: true,
		`{"inbounds":[{"type":"trojan","tag":"entry","transport":{"type":"httpupgrade"}}],"outbounds":[{"type":"trojan","tag":"relay-link-1-u1-g1"}],` + rules + `}`: true,
	}
	for raw, want := range cases {
		required, err := st.RelayCoreRequirementsForConfig(1, []byte(raw))
		if err != nil || required.TrojanHandshake != want {
			t.Fatalf("raw %s: got %+v err=%v want trojan=%v", raw, required, err, want)
		}
	}
}

func TestRelayTopologyTrojanLandingRequiresOnlyLanding(t *testing.T) {
	st, a, b, _, _, _ := meteringRelayFixture(t) // vless entry -> trojan landing
	required, err := st.PreviewRelayUserCoreRequirements()
	if err != nil {
		t.Fatal(err)
	}
	if !required[b].TrojanHandshake || required[a].TrojanHandshake {
		t.Fatalf("Trojan requirement must mark only the Trojan server: %+v", required)
	}
	// Planner record with an older marker blocks P1 activation fail-closed.
	if err = st.SetNodeSingbox(b, sbver.Parse("sing-box version "+sbver.TransportReadBufferFixVersion+"\nTags: with_v2ray_api")); err != nil {
		t.Fatal(err)
	}
	if err = st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	if err = st.PreflightTrafficMetering(true, false, true); err == nil || !strings.Contains(err.Error(), "Trojan 分段握手修复") || !strings.Contains(err.Error(), sbver.TrojanHandshakeFixVersion) {
		t.Fatalf("older marker must block Trojan P1 with an exact instruction: %v", err)
	}
}
