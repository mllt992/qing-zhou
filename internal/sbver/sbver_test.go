package sbver

import "testing"

// Real output shapes. The panel already runs this command on every node, so the
// parser has to survive whatever the installed binary happens to print.
func TestParseRealOutputs(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		version string
		api     bool
	}{
		{
			name: "official build, no v2ray_api",
			out: "sing-box version 1.13.18\n\n" +
				"Environment: go1.24.5 linux/amd64\n" +
				"Tags: with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme\n" +
				"Revision: abc123\n",
			version: "1.13.18",
			api:     false,
		},
		{
			name: "panel build, with v2ray_api",
			out: "sing-box version 1.12.25\n\n" +
				"Environment: go1.24.5 linux/arm64\n" +
				"Tags: with_gvisor,with_quic,with_v2ray_api,with_utls\n",
			version: "1.12.25",
			api:     true,
		},
		{
			name:    "prerelease",
			out:     "sing-box version 1.14.0-beta.12\n\nTags: with_v2ray_api\n",
			version: "1.14.0-beta.12",
			api:     true,
		},
		{
			name:    "leading v",
			out:     "sing-box version v1.13.0\n",
			version: "1.13.0",
		},
		{
			name:    "single line, nothing else",
			out:     "sing-box version 1.11.15",
			version: "1.11.15",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Parse(c.out)
			if got.Version != c.version {
				t.Errorf("version = %q, want %q", got.Version, c.version)
			}
			if got.HasV2RayAPI != c.api {
				t.Errorf("HasV2RayAPI = %v, want %v", got.HasV2RayAPI, c.api)
			}
			if got.Raw == "" {
				t.Error("Raw is empty; a human needs something to look at")
			}
		})
	}
}

// A probe that resolved the wrong binary, or none, must read as "unknown" —
// never as a version, and never as "too old" (which would nag the operator to
// fix something that may not be broken).
func TestParseRefusesToGuess(t *testing.T) {
	for _, out := range []string{
		"", "   \n\n", "unknown", "sing-box version unknown",
		"bash: sing-box: command not found",
		"sing-box version", // truncated
	} {
		got := Parse(out)
		if got.Version != "" {
			t.Errorf("Parse(%q).Version = %q, want empty", out, got.Version)
		}
		if got.TooOld() {
			t.Errorf("Parse(%q) reported TooOld on an unknown version", out)
		}
	}
}

func TestTooOld(t *testing.T) {
	cases := map[string]bool{
		"1.11.15":        true,
		"1.10.0":         true,
		"1.9.3":          true,
		"1.12.0":         false, // exactly the floor
		"1.12.25":        false,
		"1.13.18":        false,
		"1.14.0-beta.12": false,
		"2.0.0":          false,
		"":               false, // unknown is not old
	}
	for v, want := range cases {
		if got := (Info{Version: v}).TooOld(); got != want {
			t.Errorf("Info{%q}.TooOld() = %v, want %v", v, got, want)
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.13.18", "1.13.18", 0},
		{"1.13", "1.13.0", 0},           // missing segments are zero
		{"v1.13.0", "1.13.0", 0},        // leading v ignored
		{"1.14.0-beta.12", "1.14.0", 0}, // pre-release suffix ignored
		{"1.12.25", "1.13.0", -1},
		{"1.13.0", "1.12.25", 1},
		{"1.9.0", "1.10.0", -1}, // numeric, not lexical
		{"1.2.3", "1.2.10", -1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestVisionFramingFixRequiresExactReviewedMarker(t *testing.T) {
	for _, version := range []string{
		"", "1.13.18", "1.14.2", "1.14.3", "2.0.0",
		"1.14.2+qz-vmess.9b95ab8", "1.14.2+qz-vmess.9b95ab8c9478-extra",
		"1.14.3+qz-vmess.9b95ab8c9478", "1.14.2+other",
	} {
		got := Parse("sing-box version " + version + "\nTags: with_v2ray_api\nRevision: " + VisionFramingFixVersion)
		if got.HasVisionFramingFix || HasVisionFramingFix(version) {
			t.Errorf("unreviewed %q claimed fixed Vision framing", version)
		}
	}
	for _, prefix := range []string{"", "v", "V"} {
		got := Parse("sing-box version " + prefix + VisionFramingFixVersion + "\nTags: with_v2ray_api\n")
		if got.Version != VisionFramingFixVersion || !got.HasVisionFramingFix || !got.HasV2RayAPI {
			t.Errorf("fixed marker not preserved: %+v", got)
		}
	}
	current := Parse("sing-box version " + CurrentReleaseVersion + "\nTags: with_v2ray_api")
	if !current.HasVisionFramingFix || current.Version != CurrentReleaseVersion {
		t.Fatalf("current release marker must prove Vision framing: %+v", current)
	}
	if Compare(VisionFramingFixVersion, "1.14.2") != 0 {
		t.Fatal("capability marker must not change the general minimum-version comparison")
	}
}
