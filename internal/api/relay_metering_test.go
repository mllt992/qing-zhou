package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"qingzhou/internal/store"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRelayMeteringNeedsExplicitChangeConfirmation(t *testing.T) {
	a, st := newUserEditAPI(t)
	w := httptest.NewRecorder()
	a.handlePutRelayMetering(w, httptest.NewRequest(http.MethodPut, "/api/admin/relay-metering", strings.NewReader(`{"enabled":true}`)))
	if w.Code != 400 || st.RelayMeteringEnabled() {
		t.Fatalf("unconfirmed mutation %d", w.Code)
	}
}
func TestGenericSettingsCannotBypassMeteringBoundary(t *testing.T) {
	a, st := newUserEditAPI(t)
	w := httptest.NewRecorder()
	a.handlePutSettings(w, httptest.NewRequest(http.MethodPut, "/api/admin/settings", strings.NewReader(`{"relay_link_metering":"true","traffic_cumulative_metering":"true"}`)))
	if st.RelayMeteringEnabled() {
		t.Fatal("generic settings enabled credential deployment")
	}
	v, _ := st.GetSetting(cumulativeMeteringSetting)
	if v != "" {
		t.Fatal("generic settings bypassed mode boundary")
	}
}
func TestServerTrafficAnalysisKeepsPhysicalAndServiceSeparate(t *testing.T) {
	a, st := newUserEditAPI(t)
	id, err := st.CreateServer(store.Server{Name: "ledger", Host: "192.0.2.1", Enabled: true, TrafficLimitBytes: 100000, TrafficResetDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err = st.InsertMetrics(id, store.ServerMetrics{Ts: now - 60, NetTotalsValid: true, NetRxTotal: 100, NetTxTotal: 200, Uptime: 100}); err != nil {
		t.Fatal(err)
	}
	if err = st.InsertMetrics(id, store.ServerMetrics{Ts: now, NetTotalsValid: true, NetRxTotal: 1100, NetTxTotal: 1200, Uptime: 160}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.RecordTrafficPoll(store.NewTrafficPoll(id, map[string]store.UsageDelta{"unknown-fixture": {Up: 100, Down: 200}})); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.handleServerTrafficAnalysis(w, withURLParam(httptest.NewRequest(http.MethodGet, "/", nil), "id", strconv.FormatInt(id, 10)))
	if w.Code != 200 {
		t.Fatalf("response %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Usage       store.ServerTrafficUsage       `json:"usage"`
			Service     store.ServiceTraffic           `json:"service"`
			Attribution store.ServerTrafficAttribution `json:"attribution"`
			Projection  trafficCapacityProjection      `json:"projection"`
		} `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	r := response.Data
	if r.Usage.Total != 2000 || r.Service.Total != 300 || r.Attribution.Total != 0 || r.Projection.Available {
		t.Fatalf("mixed ledgers %+v", r)
	}
}
