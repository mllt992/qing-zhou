package assets

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestSingboxFallbackVersionMatchesRelease(t *testing.T) {
	workflow, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`SB_VERSION: (v[0-9]+\.[0-9]+\.[0-9]+)`).FindSubmatch(workflow)
	if len(pin) != 2 {
		t.Fatal("release must pin a stable sing-box version")
	}
	if !strings.Contains(InstallScript(), "QZ_SB_FALLBACK_VERSION="+string(pin[1])+"\n") {
		t.Fatal("official fallback must use the same stable version as the panel release")
	}
	if strings.Contains(InstallScript(), "repos/SagerNet/sing-box/releases/latest") {
		t.Fatal("fallback must not silently switch to an unreviewed upstream release")
	}
}
