package assets

import (
	"os"
	"os/exec"
	"path/filepath"

	"qingzhou/internal/sbver"
	"regexp"
	"strings"
	"testing"
)

func TestSingboxFallbackVersionMatchesRelease(t *testing.T) {
	build, err := os.ReadFile("../../scripts/build-singbox.sh")
	if err != nil {
		t.Fatal(err)
	}
	pins, err := os.ReadFile("../../scripts/singbox-pins.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(build), `source "$script_root/singbox-pins.sh"`) {
		t.Fatal("core build must use the shared immutable pins")
	}
	pin := regexp.MustCompile(`(?m)^SB_TAG=(v[0-9]+\.[0-9]+\.[0-9]+)$`).FindSubmatch(pins)
	if len(pin) != 2 {
		t.Fatal("shared core build must pin a stable sing-box base version")
	}
	if !strings.Contains(InstallScript(), "QZ_SB_FALLBACK_VERSION="+string(pin[1])+"\n") {
		t.Fatal("official fallback must use the same stable base version as the panel release")
	}
	if !strings.Contains(string(pins), "CORE_VERSION="+sbver.TrojanHandshakeFixVersion+"\n") {
		t.Fatal("core build and capability parser must share the exact reviewed marker")
	}
	if strings.Contains(InstallScript(), "repos/SagerNet/sing-box/releases/latest") {
		t.Fatal("fallback must not silently switch to an unreviewed upstream release")
	}
}

func TestSingboxInstallerVisionCapabilityUsesExactMarker(t *testing.T) {
	script := InstallScript()
	pin := "QZ_SB_VISION_FIXED_VERSION=" + sbver.VisionFramingFixVersion + "\n" +
		"QZ_SB_TRANSPORT_FIXED_VERSION=" + sbver.TransportReadBufferFixVersion + "\n" +
		"QZ_SB_TROJAN_FIXED_VERSION=" + sbver.TrojanHandshakeFixVersion + "\n"
	if !strings.Contains(script, pin) {
		t.Fatal("installer capability marker must match the parser's reviewed version")
	}
	start := strings.Index(script, "report_vision_capability() {")
	if start < 0 {
		t.Fatal("missing capability reporter")
	}
	end := strings.Index(script[start:], "\n}\n")
	if end < 0 {
		t.Fatal("unterminated capability reporter")
	}
	function := script[start : start+end+3]
	// Execute only the pure reporter with a temporary fake binary. Never run
	// main, root checks, network downloads, tuning, install or systemd commands.
	for _, version := range []string{"", "1.14.2", "1.14.3", sbver.VisionFramingFixVersion, sbver.VisionFramingFixVersion + "-extra", sbver.TransportReadBufferFixVersion, sbver.TransportReadBufferFixVersion + "-extra", sbver.TrojanHandshakeFixVersion, sbver.TrojanHandshakeFixVersion + "-extra"} {
		t.Run(version, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "sing-box")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' 'sing-box version "+version+"' 'Tags: with_v2ray_api'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			program := pin + "ok() { echo OK:$*; }; warn() { echo WARN:$*; }; info() { echo INFO:$*; };\n" + function + "\nreport_vision_capability \"$1\"\n"
			out, err := exec.Command("bash", "-c", program, "fixture", bin).CombinedOutput()
			if err != nil {
				t.Fatalf("reporter: %v %s", err, out)
			}
			fixed := sbver.HasVisionFramingFix(version)
			if strings.Contains(string(out), "OK:内核版本标记包含已审定的 Vision") != fixed {
				t.Fatalf("version %q misreported capability: %s", version, out)
			}
			if strings.Contains(string(out), "OK:内核版本标记包含已审定的 WebSocket") != sbver.HasTransportReadBufferFix(version) {
				t.Fatalf("version %q misreported transport capability: %s", version, out)
			}
			if strings.Contains(string(out), "OK:内核版本标记包含 Trojan 分段握手修复") != sbver.HasTrojanHandshakeFix(version) {
				t.Fatalf("version %q misreported Trojan capability: %s", version, out)
			}
			if !fixed && (!strings.Contains(string(out), "P1 Vision") || !strings.Contains(string(out), "不会为此自动替换已有内核")) {
				t.Fatalf("stock/fallback warning omitted its P1 limit or manual-upgrade boundary: %s", out)
			}
		})
	}
	if !strings.Contains(script, `report_vision_capability "$cur"`) || !strings.Contains(script, `report_vision_capability "$BIN"`) {
		t.Fatal("existing and newly installed cores must both report the capability")
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("installer syntax: %v %s", err, out)
	}
}
