package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"qingzhou/internal/intervalcfg"
	"qingzhou/internal/store"
)

func TestHealthTimelineQuery(t *testing.T) {
	t.Setenv(intervalcfg.EnvProbeInterval, "60s")
	a, st := newUserEditAPI(t)
	now := time.Now().Unix()
	id, err := st.CreateServer(store.Server{Name: "边缘", Host: "10.0.0.8", ProbeEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	off, err := st.CreateServer(store.Server{Name: "未开探针", Host: "10.0.0.9", ProbeEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertMetrics(id, store.ServerMetrics{
		Ts: now - 120, NetRxTotal: 1000, NetTxTotal: 2000,
		NetTotalsValid: true, Uptime: 500, TCPConnections: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertMetrics(id, store.ServerMetrics{
		Ts: now - 60, NetRxTotal: 1000, NetTxTotal: 2000,
		NetTotalsValid: true, Uptime: 560, TCPConnections: 2,
	}); err != nil {
		t.Fatal(err)
	}
	// A disabled probe must not show up even if something wrote a row.
	if err := st.InsertMetrics(off, store.ServerMetrics{
		Ts: now - 60, NetRxTotal: 9, NetTxTotal: 9, NetTotalsValid: true, Uptime: 10,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/monitor/health-timeline?from="+strconv.FormatInt(now-600, 10)+"&to="+strconv.FormatInt(now, 10), nil)
	w := httptest.NewRecorder()
	a.handleHealthTimeline(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Data store.HealthTimeline `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	tl := body.Data
	if tl.LatencyAvailable || tl.LossAvailable || tl.HealthSignal != store.HealthSignalProbePresence {
		t.Fatalf("probe does not provide latency/loss, payload=%+v", tl)
	}
	var foundIdle, foundOff, foundLocal bool
	for _, m := range tl.Machines {
		if m.ServerID == off {
			foundOff = true
		}
		if m.Local {
			foundLocal = true
		}
		if m.ServerID == id {
			foundIdle = true
			if m.Empty || m.MaxBytes != 0 {
				t.Fatalf("idle machine = %+v", m)
			}
			knownZero := false
			for _, c := range m.Cells {
				if c.State == store.HealthOnline && c.Known && c.Bytes == 0 {
					knownZero = true
				}
				if c.State == store.HealthGap && (c.Known || c.Bytes != 0) {
					t.Fatalf("gap cell painted as traffic: %+v", c)
				}
			}
			if !knownZero {
				t.Fatalf("expected a known-zero online cell, got %+v", m.Cells)
			}
		}
	}
	if !foundIdle || !foundLocal || foundOff {
		t.Fatalf("machines idle=%v local=%v disabled=%v list=%+v", foundIdle, foundLocal, foundOff, tl.Machines)
	}

	wide := httptest.NewRequest(http.MethodGet, "/api/admin/monitor/health-timeline?from="+strconv.FormatInt((now-40*86400)*1000, 10)+"&to="+strconv.FormatInt(now*1000, 10), nil)
	wb := httptest.NewRecorder()
	a.handleHealthTimeline(wb, wide)
	if wb.Code != http.StatusBadRequest {
		t.Fatalf("wide range status %d: %s", wb.Code, wb.Body.String())
	}
}
