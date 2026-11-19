package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"qingzhou/internal/sbver"
	"qingzhou/internal/store"
)

func TestNodeVersionViewKeepsVisionCapabilitySeparate(t *testing.T) {
	for _, version := range []string{"", "1.14.2", "1.14.3", sbver.VisionFramingFixVersion} {
		fixed := sbver.HasVisionFramingFix(version)
		observed := map[int64]*store.NodeSingbox{0: {Version: version, HasVisionFramingFix: fixed, Error: "probe unavailable"}}
		view := viewFor(0, store.LocalNodeName, "", true, true, observed, nil)
		if view.HasVisionFramingFix != fixed || view.Error != "probe unavailable" || view.Version != version {
			t.Fatalf("capability/version/error presentation drift: %+v", view)
		}
		if version != "" && view.TooOld {
			t.Fatal("stock supported-version status must remain separate from P1 Vision capability")
		}
	}
}

func TestNodeVersionViewKeepsTransportFixSeparateFromVisionAndSemver(t *testing.T) {
	for _, version := range []string{"", "1.14.2", "1.99.0", sbver.VisionFramingFixVersion, sbver.TransportReadBufferFixVersion} {
		info := sbver.Parse("sing-box version " + version + "\nTags: with_v2ray_api")
		observed := map[int64]*store.NodeSingbox{0: {Version: version, HasV2RayAPI: true, HasVisionFramingFix: info.HasVisionFramingFix, HasTransportReadBufferFix: info.HasTransportReadBufferFix, Error: "running process not confirmed"}}
		view := viewFor(0, store.LocalNodeName, "", true, true, observed, nil)
		if view.HasTransportReadBufferFix != (version == sbver.TransportReadBufferFixVersion) || view.HasVisionFramingFix != sbver.HasVisionFramingFix(version) || view.Error != "running process not confirmed" {
			t.Fatalf("capability view conflated observations: %+v", view)
		}
	}
}

func TestNodeVersionsIncludeSeparatePublishedFixVersions(t *testing.T) {
	a, _ := newUserEditAPI(t)
	w := httptest.NewRecorder()
	a.handleAdminNodeVersions(w, httptest.NewRequest(http.MethodGet, "/api/admin/nodes/singbox", nil))
	if w.Code != 200 {
		t.Fatalf("node versions %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Vision    string `json:"vision_fixed_version"`
			Transport string `json:"transport_fixed_version"`
			Trojan    string `json:"trojan_fixed_version"`
			Current   string `json:"current_release_version"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Vision != sbver.VisionFramingFixVersion || response.Data.Transport != sbver.TransportReadBufferFixVersion || response.Data.Vision == response.Data.Transport {
		t.Fatalf("missing independent fix versions: %+v", response.Data)
	}
	if response.Data.Trojan != sbver.TrojanHandshakeFixVersion || response.Data.Current != sbver.CurrentReleaseVersion || response.Data.Current != response.Data.Trojan {
		t.Fatalf("current release version missing or drifted: %+v", response.Data)
	}
}
