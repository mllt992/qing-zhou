package subconv

import (
	"strings"
	"testing"
)

func TestClashDisableUDPOmitsKey(t *testing.T) {
	ps := ParseLinks([]string{
		"vless://11111111-1111-1111-1111-111111111111@1.2.3.4:443?security=tls&sni=a.example#n1",
	})
	out, err := ClashWithOptions(ps, "", ProfileLegacy, ClashOptions{DisableUDP: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "udp: true") || strings.Contains(out, "udp:true") {
		t.Fatalf("DisableUDP must not emit udp: true:\n%s", out)
	}
	outOn, err := ClashWithOptions(ps, "", ProfileLegacy, ClashOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outOn, "udp: true") && !strings.Contains(outOn, "udp:true") {
		t.Fatalf("default must still emit udp: true:\n%s", outOn)
	}
}
