package sbver

import "testing"

// #87: the Trojan handshake capability is an exact, independent marker.
func TestTrojanHandshakeCapabilityUsesExactMarker(t *testing.T) {
	for _, version := range []string{"", "1.14.2", "1.14.3", "2.0.0", VisionFramingFixVersion, TransportReadBufferFixVersion,
		TrojanHandshakeFixVersion + "-extra", TransportReadBufferFixVersion + "-trojan.00000000",
		"1.14.3+qz-vmess.9b95ab8c9478-transport.07512b10-trojan.b85be9da"} {
		info := Parse("sing-box version " + version + "\nTags: with_v2ray_api\nRevision: " + TrojanHandshakeFixVersion)
		if info.HasTrojanHandshakeFix || HasTrojanHandshakeFix(version) {
			t.Fatalf("%q must not prove the Trojan handshake fix", version)
		}
	}
	info := Parse("sing-box version " + TrojanHandshakeFixVersion + "\nTags: with_v2ray_api")
	if info.Version != TrojanHandshakeFixVersion || !info.HasTrojanHandshakeFix || !info.HasTransportReadBufferFix || !info.HasVisionFramingFix || !info.HasV2RayAPI {
		t.Fatalf("current core marker must carry all reviewed fixes: %+v", info)
	}
	if Compare(TrojanHandshakeFixVersion, "1.14.2") != 0 {
		t.Fatal("build marker must not be ordered as a newer upstream release")
	}
}
