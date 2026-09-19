package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/store"
	"qingzhou/internal/version"
)

func sampleLocal(t *testing.T, st *store.Store, cpu float64) {
	t.Helper()
	if err := st.InsertMetrics(store.LocalNodeID, store.ServerMetrics{
		CPUPercent: cpu, MemUsed: 1 << 30, MemTotal: 2 << 30,
		DiskUsed: 5 << 30, DiskTotal: 20 << 30, Hostname: "panel-host",
	}); err != nil {
		t.Fatal(err)
	}
}

func getJSON(t *testing.T, h http.HandlerFunc, target string, out any) {
	t.Helper()
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", target, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s: status %d, body %s", target, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("%s: decode %s: %v", target, w.Body.String(), err)
	}
}

// The panel's own machine has no servers row and never will — an SSH-managed
// row for it would make sbctl deploy sing-box to it twice, over SSH and
// in-process. So it is injected into the monitor list from its metrics, and the
// admin never has to add a server just to watch the machine already running the
// panel.
func TestLocalNodeAppearsInMonitorList(t *testing.T) {
	a, st := newNodeUpgradeAPI(t)

	var before struct {
		Data []struct {
			ID    int64 `json:"id"`
			Local bool  `json:"local"`
		} `json:"data"`
	}
	getJSON(t, a.handleMonitorServers, "/api/admin/monitor/servers", &before)
	if len(before.Data) != 0 {
		t.Fatalf("nothing has been sampled yet, so nothing should be listed: %+v", before.Data)
	}

	sampleLocal(t, st, 12.5)

	var after struct {
		Data []struct {
			ID             int64  `json:"id"`
			Name           string `json:"name"`
			Local          bool   `json:"local"`
			ProbeEnabled   bool   `json:"probe_enabled"`
			ProbeToken     string `json:"probe_token"`
			PublicVisible  bool   `json:"public_visible"`
			HomeVisibility string `json:"home_visibility"`
			Status         string `json:"status"`
			Metrics       *struct {
				CPUPercent float64 `json:"cpu_percent"`
			} `json:"metrics"`
		} `json:"data"`
	}
	getJSON(t, a.handleMonitorServers, "/api/admin/monitor/servers", &after)
	if len(after.Data) != 1 {
		t.Fatalf("want exactly the local machine, got %+v", after.Data)
	}
	row := after.Data[0]
	if row.ID != store.LocalNodeID || !row.Local || row.Name != store.LocalNodeName {
		t.Fatalf("row = %+v, want the local node", row)
	}
	if row.Metrics == nil || row.Metrics.CPUPercent != 12.5 {
		t.Fatalf("metrics = %+v, want the sample just written", row.Metrics)
	}
	// No token: the UI keys "show the probe install command" off this, and the
	// local machine needs no probe at all.
	if row.ProbeToken != "" {
		t.Fatalf("local node must not carry a probe token, got %q", row.ProbeToken)
	}
	if !row.ProbeEnabled || row.Status != "online" {
		t.Fatalf("local node should read as a live, monitored machine: %+v", row)
	}
	if row.HomeVisibility != "admin" || row.PublicVisible {
		t.Fatalf("default home visibility = %+v, want admin-only", row)
	}
}

func TestMonitorListMarksLegacyProbeForUpgrade(t *testing.T) {
	a, st := newNodeUpgradeAPI(t)
	id, err := st.CreateServer(store.Server{Name: "old-probe", Host: "192.0.2.11", ProbeEnabled: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertMetrics(id, store.ServerMetrics{CPUPercent: 10}); err != nil {
		t.Fatal(err)
	}

	type row struct {
		ID            int64  `json:"id"`
		ProbeVersion  string `json:"probe_version"`
		ProbeTarget   string `json:"probe_target_version"`
		ProbeOutdated bool   `json:"probe_outdated"`
	}
	list := func() row {
		var body struct {
			Data []row `json:"data"`
		}
		getJSON(t, a.handleMonitorServers, "/api/admin/monitor/servers", &body)
		for _, got := range body.Data {
			if got.ID == id {
				return got
			}
		}
		t.Fatalf("server %d missing from monitor list", id)
		return row{}
	}

	legacy := list()
	if !legacy.ProbeOutdated || legacy.ProbeVersion != "" || legacy.ProbeTarget != version.Current() {
		t.Fatalf("legacy probe state = %+v, want blank version marked outdated against %q", legacy, version.Current())
	}

	if err := st.InsertMetrics(id, store.ServerMetrics{CPUPercent: 11, ProbeVersion: version.Current()}); err != nil {
		t.Fatal(err)
	}
	current := list()
	if current.ProbeOutdated || current.ProbeVersion != version.Current() {
		t.Fatalf("current probe state = %+v, want current", current)
	}
}

// The status page is unauthenticated. The panel host is the one machine whose
// name says exactly what it is, so it stays off that page until an admin says
// otherwise — while every other machine keeps the behaviour it had before the
// flag existed, or an upgrade would silently empty someone's status page.
func TestPublicPageVisibilityDefaults(t *testing.T) {
	a, st := newNodeUpgradeAPI(t)
	sampleLocal(t, st, 20)
	id, err := st.CreateServer(store.Server{Name: "landing-1", Host: "192.0.2.10", ProbeEnabled: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertMetrics(id, store.ServerMetrics{CPUPercent: 30}); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchProbeSeen(id); err != nil {
		t.Fatal(err)
	}

	names := func() []string {
		var body struct {
			Data struct {
				Servers []struct {
					Name string `json:"name"`
				} `json:"servers"`
			} `json:"data"`
		}
		getJSON(t, a.handleMonitorPublic, "/api/monitor/public", &body)
		var out []string
		for _, s := range body.Data.Servers {
			out = append(out, s.Name)
		}
		return out
	}

	got := names()
	if len(got) != 1 || got[0] != "landing-1" {
		t.Fatalf("public page = %v; want the landing node only (local hidden by default)", got)
	}

	// Admin opts the panel host in.
	setVisible(t, a, store.LocalNodeID, true)
	if got := names(); len(got) != 2 {
		t.Fatalf("public page = %v; want the local machine listed once opted in", got)
	}

	// And opts the landing node out.
	setVisible(t, a, id, false)
	got = names()
	if len(got) != 1 || got[0] != store.LocalNodeName {
		t.Fatalf("public page = %v; want only the local machine after hiding the landing node", got)
	}
}

func TestLocalHomeVisibilityThreeState(t *testing.T) {
	a, st := newNodeUpgradeAPI(t)
	sampleLocal(t, st, 8)

	readAdmin := func() (public bool, mode string) {
		var body struct {
			Data []struct {
				Local          bool   `json:"local"`
				PublicVisible  bool   `json:"public_visible"`
				HomeVisibility string `json:"home_visibility"`
			} `json:"data"`
		}
		getJSON(t, a.handleMonitorServers, "/api/admin/monitor/servers", &body)
		for _, row := range body.Data {
			if row.Local {
				return row.PublicVisible, row.HomeVisibility
			}
		}
		t.Fatal("local node missing from admin monitor list")
		return false, ""
	}
	publicNames := func() []string {
		var body struct {
			Data struct {
				Servers []struct {
					Name string `json:"name"`
				} `json:"servers"`
			} `json:"data"`
		}
		getJSON(t, a.handleMonitorPublic, "/api/monitor/public", &body)
		var out []string
		for _, s := range body.Data.Servers {
			out = append(out, s.Name)
		}
		return out
	}
	setMode := func(mode string) {
		t.Helper()
		req := httptest.NewRequest("PUT", "/x", strings.NewReader(`{"home_visibility":"`+mode+`"}`))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", strconv.FormatInt(store.LocalNodeID, 10))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		a.handleUpdateServerMonitor(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("set home_visibility=%s: status %d, body %s", mode, w.Code, w.Body.String())
		}
	}

	public, mode := readAdmin()
	if public || mode != "admin" {
		t.Fatalf("default = public:%v mode:%s, want admin-only", public, mode)
	}
	if got := publicNames(); len(got) != 0 {
		t.Fatalf("public page default = %v; want empty", got)
	}

	setMode("public")
	public, mode = readAdmin()
	if !public || mode != "public" {
		t.Fatalf("after public = public:%v mode:%s", public, mode)
	}
	if got := publicNames(); len(got) != 1 || got[0] != store.LocalNodeName {
		t.Fatalf("public page after public = %v", got)
	}

	setMode("hidden")
	public, mode = readAdmin()
	if public || mode != "hidden" {
		t.Fatalf("after hidden = public:%v mode:%s", public, mode)
	}
	if got := publicNames(); len(got) != 0 {
		t.Fatalf("public page after hidden = %v; want empty", got)
	}

	req := httptest.NewRequest("PUT", "/x", strings.NewReader(`{"home_visibility":"everyone"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(store.LocalNodeID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	a.handleUpdateServerMonitor(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode: status %d, body %s", w.Code, w.Body.String())
	}
}

func TestPublicTrafficAndPriceDefaultOnAndCanBeHidden(t *testing.T) {
	a, st := newNodeUpgradeAPI(t)
	id, err := st.CreateServer(store.Server{Name: "asset-1", Host: "192.0.2.20", ProbeEnabled: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	sv, err := st.GetServer(id)
	if err != nil {
		t.Fatal(err)
	}
	if !sv.PublicShowTraffic || !sv.PublicShowPrice {
		t.Fatalf("new server public detail defaults = traffic:%v price:%v, want both on", sv.PublicShowTraffic, sv.PublicShowPrice)
	}
	sv.Price = 19.5
	if err := st.UpdateServer(*sv); err != nil {
		t.Fatal(err)
	}
	for i, total := range []int64{100, 180} {
		if err := st.InsertMetrics(id, store.ServerMetrics{
			Ts: int64(100 + i), NetTotalsValid: true, NetRxTotal: total, NetTxTotal: total,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.TouchProbeSeen(id); err != nil {
		t.Fatal(err)
	}

	type publicRow struct {
		Name    string   `json:"name"`
		Price   *float64 `json:"price"`
		Traffic *struct {
			UsedBytes int64 `json:"used_bytes"`
		} `json:"traffic"`
	}
	read := func() publicRow {
		var body struct {
			Data struct {
				Servers []publicRow `json:"servers"`
			} `json:"data"`
		}
		getJSON(t, a.handleMonitorPublic, "/api/monitor/public", &body)
		if len(body.Data.Servers) != 1 {
			t.Fatalf("public rows = %+v", body.Data.Servers)
		}
		return body.Data.Servers[0]
	}
	shown := read()
	if shown.Price == nil || *shown.Price != 19.5 || shown.Traffic == nil {
		t.Fatalf("default public asset details = %+v, want price and traffic", shown)
	}

	req := httptest.NewRequest("PUT", "/x", strings.NewReader(`{"public_show_traffic":false,"public_show_price":false}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(id, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	a.handleUpdateServerMonitor(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("hide public details: status %d, body %s", w.Code, w.Body.String())
	}
	hidden := read()
	if hidden.Price != nil || hidden.Traffic != nil {
		t.Fatalf("hidden public asset details leaked: %+v", hidden)
	}
}

func setVisible(t *testing.T, a *API, id int64, v bool) {
	t.Helper()
	body := `{"public_visible":` + map[bool]string{true: "true", false: "false"}[v] + `}`
	req := httptest.NewRequest("PUT", "/x", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(id, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	a.handleUpdateServerMonitor(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set public_visible=%v on %d: status %d, body %s", v, id, w.Code, w.Body.String())
	}
}

func calibrateTrafficRequest(a *API, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PUT", "/x", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	a.handleCalibrateServerTraffic(w, req)
	return w
}

func TestTrafficCalibrationRequiresExplicitValue(t *testing.T) {
	a, _ := newNodeUpgradeAPI(t)
	for _, tc := range []struct {
		id, body string
	}{
		{"0", `{}`},
		{"not-a-number", `{"used_bytes":1}`},
		{"0", `{"used_bytes":-1}`},
	} {
		if w := calibrateTrafficRequest(a, tc.id, tc.body); w.Code != http.StatusBadRequest {
			t.Fatalf("id=%q body=%s: status %d, body %s", tc.id, tc.body, w.Code, w.Body.String())
		}
	}
}

func TestLocalTrafficCalibrationRoundTrip(t *testing.T) {
	a, st := newNodeUpgradeAPI(t)
	if err := st.SetLocalAsset(store.LocalNodeAsset{TrafficResetDay: 16}); err != nil {
		t.Fatal(err)
	}
	if w := calibrateTrafficRequest(a, "0", `{"used_bytes":12345}`); w.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	now := time.Now()
	start := store.TrafficCycleStart(now, 16, 0).Unix()
	usage, err := st.TrafficUsageForCycles(map[int64]int64{store.LocalNodeID: start})
	if err != nil {
		t.Fatal(err)
	}
	if got := usage[store.LocalNodeID]; got.Total != 12345 || !got.Calibrated {
		t.Fatalf("local calibrated usage = %+v", got)
	}
}

// The panel's machine is rented like any other, and its expiry is the one that
// takes the whole service down rather than a single node — so the asset fields
// have to be editable for it too, even though it has no servers row to hold
// them. Only 启用探针 genuinely does not apply.
func TestLocalNodeAssetFieldsRoundTrip(t *testing.T) {
	a, st := newNodeUpgradeAPI(t)
	sampleLocal(t, st, 3)

	expiry := time.Now().AddDate(0, 0, 2).Unix() // inside the 3-day warning window
	body := `{"provider":"索隆云","location":"美国-洛杉矶","spec":"1H1G","price":39,` +
		`"expiry_date":` + strconv.FormatInt(expiry, 10) + `,"notes":"面板机","probe_enabled":false}`
	req := httptest.NewRequest("PUT", "/x", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	a.handleUpdateServerMonitor(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}

	var list struct {
		Data []struct {
			Provider     string  `json:"provider"`
			Location     string  `json:"location"`
			Spec         string  `json:"spec"`
			Price        float64 `json:"price"`
			ExpiryDate   int64   `json:"expiry_date"`
			DaysLeft     *int    `json:"days_left"`
			Notes        string  `json:"notes"`
			ProbeEnabled bool    `json:"probe_enabled"`
		} `json:"data"`
	}
	getJSON(t, a.handleMonitorServers, "/api/admin/monitor/servers", &list)
	if len(list.Data) != 1 {
		t.Fatalf("want the local machine, got %+v", list.Data)
	}
	row := list.Data[0]
	if row.Provider != "索隆云" || row.Location != "美国-洛杉矶" || row.Spec != "1H1G" || row.Price != 39 || row.Notes != "面板机" {
		t.Fatalf("asset fields did not round-trip: %+v", row)
	}
	if row.ExpiryDate != expiry || row.DaysLeft == nil {
		t.Fatalf("expiry did not round-trip: %+v", row)
	}
	// probe_enabled is meaningless here and must not be able to switch the panel's
	// own collection off — the card would go blank with no way to bring it back.
	if !row.ProbeEnabled {
		t.Fatal("local collection must stay on regardless of what is posted")
	}

	// The expiry has to reach the dashboard's 即将到期 counter, or an admin who
	// filled it in still gets no warning.
	var dash struct {
		Data struct {
			ExpiringSoon int `json:"expiring_soon"`
		} `json:"data"`
	}
	getJSON(t, a.handleMonitorDashboard, "/api/admin/monitor/dashboard", &dash)
	if dash.Data.ExpiringSoon != 1 {
		t.Fatalf("expiring_soon = %d, want 1", dash.Data.ExpiringSoon)
	}
}

// The heatmap is built once for two audiences. Getting the filter backwards
// there would publish, in outline and by name, exactly the machines the flag
// exists to keep off the public page.
func TestHeatmapRespectsPublicVisibility(t *testing.T) {
	a, st := newNodeUpgradeAPI(t)
	sampleLocal(t, st, 5) // local: hidden from the public page by default

	rows := func(h http.HandlerFunc, path string) int {
		var body struct {
			Data struct {
				Servers []struct {
					Name string `json:"name"`
				} `json:"servers"`
			} `json:"data"`
		}
		getJSON(t, h, path, &body)
		return len(body.Data.Servers)
	}

	if n := rows(a.handleMonitorHeatmap, "/api/admin/monitor/heatmap"); n != 1 {
		t.Fatalf("admin heatmap rows = %d, want 1 (the local machine)", n)
	}
	if n := rows(a.handleMonitorPublicHeatmap, "/api/monitor/heatmap"); n != 0 {
		t.Fatalf("public heatmap rows = %d, want 0 while the local machine is hidden", n)
	}

	setVisible(t, a, store.LocalNodeID, true)
	if n := rows(a.handleMonitorPublicHeatmap, "/api/monitor/heatmap"); n != 1 {
		t.Fatalf("public heatmap rows = %d, want 1 once opted in", n)
	}
}
