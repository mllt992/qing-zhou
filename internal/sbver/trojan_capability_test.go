package sbver

import "testing"

// #87: the Trojan handshake capability is an exact, independent marker.
func TestTrojanHandshakeCapabilityUsesExactMarker(t *testing.T) {
	for _, version := range []string{"", "1.14.2", "1.14.3", "1.14.4", "1.14.5", "2.0.0", VisionFramingFixVersion, TransportReadBufferFixVersion,
		TrojanHandshakeFixVersion + "-extra", PreviousReleaseVersion + "-extra", TransportReadBufferFixVersion + "-trojan.00000000",
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
	legacy := Parse("sing-box version " + LegacyTrojanHandshakeFixVersion + "\nTags: with_v2ray_api")
	if legacy.Version != LegacyTrojanHandshakeFixVersion || !legacy.HasTrojanHandshakeFix || !legacy.HasTransportReadBufferFix || !legacy.HasVisionFramingFix {
		t.Fatalf("legacy 1.14.2 trojan marker must still prove all reviewed fixes: %+v", legacy)
	}
	previous := Parse("sing-box version " + PreviousReleaseVersion + "\nTags: with_v2ray_api")
	if previous.Version != PreviousReleaseVersion || !previous.HasTrojanHandshakeFix || !previous.HasTransportReadBufferFix || !previous.HasVisionFramingFix {
		t.Fatalf("previous 1.14.4 release marker must still prove all reviewed fixes: %+v", previous)
	}
	if Compare(TrojanHandshakeFixVersion, "1.14.5") != 0 {
		t.Fatal("current build marker must compare as 1.14.5, ignoring the +qz suffix")
	}
	if Compare(PreviousReleaseVersion, "1.14.4") != 0 {
		t.Fatal("previous build marker must still compare as 1.14.4")
	}
	if Compare(LegacyTrojanHandshakeFixVersion, "1.14.2") != 0 {
		t.Fatal("legacy build marker must still compare as 1.14.2")
	}
}
