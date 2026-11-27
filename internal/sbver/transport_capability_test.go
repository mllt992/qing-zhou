package sbver

import "testing"

func TestTransportReadBufferFixRequiresExactReviewedBuild(t *testing.T) {
	for _, version := range []string{"", "1.14.2", "1.14.3", "1.14.4", "1.14.5", "2.0.0", VisionFramingFixVersion, VisionFramingFixVersion + "-transport.07512b", TransportReadBufferFixVersion + "-extra", "1.14.3+qz-vmess.9b95ab8c9478-transport.07512b10"} {
		info := Parse("sing-box version " + version + "\nTags: with_v2ray_api\nRevision: " + TransportReadBufferFixVersion)
		if info.HasTransportReadBufferFix || HasTransportReadBufferFix(version) {
			t.Errorf("unreviewed marker proved transport fix: %q", version)
		}
	}
	for _, prefix := range []string{"", "v", "V"} {
		info := Parse("sing-box version " + prefix + TransportReadBufferFixVersion + "\nTags: with_v2ray_api")
		if info.Version != TransportReadBufferFixVersion || !info.HasTransportReadBufferFix || !info.HasVisionFramingFix || !info.HasV2RayAPI {
			t.Fatalf("combined marker lost capabilities: %+v", info)
		}
	}
	old := Parse("sing-box version " + VisionFramingFixVersion + "\nTags: with_v2ray_api")
	if !old.HasVisionFramingFix || old.HasTransportReadBufferFix {
		t.Fatal("Vision-only build was weakened or promoted")
	}
	current := Parse("sing-box version " + CurrentReleaseVersion + "\nTags: with_v2ray_api")
	if current.Version != CurrentReleaseVersion || !current.HasTransportReadBufferFix || !current.HasVisionFramingFix || !current.HasTrojanHandshakeFix {
		t.Fatalf("current 1.14.5 core must prove Vision, transport and Trojan fixes: %+v", current)
	}
	previous := Parse("sing-box version " + PreviousReleaseVersion + "\nTags: with_v2ray_api")
	if !previous.HasTransportReadBufferFix || !previous.HasVisionFramingFix || !previous.HasTrojanHandshakeFix {
		t.Fatalf("previous 1.14.4 core must still prove Vision, transport and Trojan fixes: %+v", previous)
	}
	if Compare(TransportReadBufferFixVersion, "1.14.2") != 0 {
		t.Fatal("capability changed minimum-version ordering")
	}
}
