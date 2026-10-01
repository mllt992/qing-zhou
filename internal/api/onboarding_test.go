package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"qingzhou/internal/sbver"
	"qingzhou/internal/store"
	"strings"
	"testing"
	"time"
)

func readOnboarding(t *testing.T, a *API) (bool, map[string]onboardingStep, string) {
	t.Helper()
	w := httptest.NewRecorder()
	a.handleAdminOnboarding(w, httptest.NewRequest(http.MethodGet, "/api/admin/onboarding", nil))
	if w.Code != 200 {
		t.Fatalf("onboarding %d %s", w.Code, w.Body.String())
	}
	var data struct {
		Data struct {
			Complete bool             `json:"complete"`
			Steps    []onboardingStep `json:"steps"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	steps := map[string]onboardingStep{}
	for _, step := range data.Data.Steps {
		steps[step.ID] = step
	}
	return data.Data.Complete, steps, w.Body.String()
}
func TestOnboardingFreshInstallAndConfiguredLocalNode(t *testing.T) {
	a, st := newUserEditAPI(t)
	complete, steps, _ := readOnboarding(t, a)
	if complete || len(steps) != 6 || steps["server"].Done || steps["plan"].Done {
		t.Fatalf("fresh state %+v", steps)
	}
	if err := st.SetSetting("public_base", "https://panel.example"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeSingbox(store.LocalNodeID, sbver.Info{Version: "1.14.2", HasV2RayAPI: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSbInbound(&store.SbInbound{ServerID: store.LocalNodeID, Type: "mixed", Tag: "local-in", Listen: "127.0.0.1", ListenPort: 1080, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	group, err := st.CreateGroup(store.NodeGroup{Name: "default"})
	if err != nil {
		t.Fatal(err)
	}
	node, err := st.CreateNode(store.Node{Type: "self_built", Name: "local", Protocol: "mixed", InboundTag: "local-in", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO node_group_members(node_id,group_id) VALUES(?,?)`, node, group); err != nil {
		t.Fatal(err)
	}
	pkg, err := st.CreatePackage(store.Package{Type: "plan", Name: "monthly", Enabled: true, TrafficBytes: 1 << 30, DurationDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	st.SetPlanGroups(pkg, []int64{group})
	complete, steps, _ = readOnboarding(t, a)
	if !complete || !steps["server"].Done || !steps["metering"].Done || !steps["plan"].Done || steps["optional"].Done {
		t.Fatalf("configured local state %+v", steps)
	}
	// An empty/disabled group must not falsely promise a usable subscription.
	st.DB().Exec(`UPDATE nodes SET enabled=0 WHERE id=?`, node)
	complete, steps, _ = readOnboarding(t, a)
	if complete || steps["plan"].Done {
		t.Fatal("disabled nodes counted as usable")
	}
}
func TestOnboardingMissingPluginStaleAndDisabledNodes(t *testing.T) {
	a, st := newUserEditAPI(t)
	id, err := st.CreateServer(store.Server{Name: "node", Host: "server.example", SSHPassword: "SHOULD-NOT-LEAK", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	st.SetNodeSingbox(id, sbver.Info{Version: "1.14.2", HasV2RayAPI: false, Raw: "PRIVATE-RAW"})
	_, steps, body := readOnboarding(t, a)
	if steps["metering"].Severity != "error" || steps["metering"].Done || !strings.Contains(steps["metering"].Detail, "with_v2ray_api") {
		t.Fatalf("missing plugin: %+v", steps["metering"])
	}
	if strings.Contains(body, "SHOULD-NOT-LEAK") || strings.Contains(body, "PRIVATE-RAW") {
		t.Fatal("checklist leaked raw server data")
	}
	st.SetNodeSingbox(id, sbver.Info{Version: "1.14.2", HasV2RayAPI: true})
	st.DB().Exec(`UPDATE node_singbox SET checked_at=? WHERE server_id=?`, time.Now().Add(-24*time.Hour).Unix(), id)
	_, steps, _ = readOnboarding(t, a)
	if steps["server"].Done || steps["metering"].Done || steps["metering"].Severity != "warning" {
		t.Fatal("stale observation counted as healthy")
	}
	st.SetNodeSingbox(id, sbver.Info{Version: "1.14.2", HasV2RayAPI: true})
	st.SetNodeSingboxError(id, "offline")
	_, steps, _ = readOnboarding(t, a)
	if steps["server"].Done || steps["metering"].Done {
		t.Fatal("failed detection reused old success")
	}
	st.DB().Exec(`UPDATE servers SET enabled=0 WHERE id=?`, id)
	_, steps, _ = readOnboarding(t, a)
	if steps["server"].Done {
		t.Fatal("disabled server counted")
	}
}
func TestOnboardingEndpointRequiresAdmin(t *testing.T) {
	a, _ := newUserEditAPI(t)
	w := httptest.NewRecorder()
	a.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/onboarding", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", w.Code)
	}
}
