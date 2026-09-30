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

func TestServerTrafficStatusQuery(t *testing.T) {
	t.Setenv(intervalcfg.EnvProbeInterval, "60s")
	a, st := newUserEditAPI(t)
	now := time.Now().Unix()
	if err := st.InsertMetrics(7, store.ServerMetrics{
		Ts: now - 120, NetRxTotal: 1000, NetTxTotal: 4000,
		NetTotalsValid: true, Uptime: 1000, TCPConnections: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertMetrics(7, store.ServerMetrics{
		Ts: now - 60, NetRxTotal: 1800, NetTxTotal: 4500,
		NetTotalsValid: true, Uptime: 1060, TCPConnections: 11,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/monitor/servers/7/traffic-status?from="+strconv.FormatInt(now-180, 10)+"&to="+strconv.FormatInt(now, 10), nil)
	req = withURLParam(req, "id", "7")
	w := httptest.NewRecorder()
	a.handleServerTrafficStatus(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Data store.TrafficStatus `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.RxBytes != 800 || body.Data.TxBytes != 500 || body.Data.PeakConnections != 11 {
		t.Fatalf("status = %+v", body.Data)
	}
	if body.Data.SampleCount != 2 || len(body.Data.Points) == 0 {
		t.Fatalf("expected stored samples, got %+v", body.Data)
	}

	// Milliseconds from the date picker, and a window wider than 30 days.
	wideFrom := (now - 40*86400) * 1000
	wideTo := now * 1000
	bad := httptest.NewRequest(http.MethodGet, "/api/admin/monitor/servers/7/traffic-status?from="+strconv.FormatInt(wideFrom, 10)+"&to="+strconv.FormatInt(wideTo, 10), nil)
	bad = withURLParam(bad, "id", "7")
	wb := httptest.NewRecorder()
	a.handleServerTrafficStatus(wb, bad)
	if wb.Code != http.StatusBadRequest {
		t.Fatalf("wide range status %d: %s", wb.Code, wb.Body.String())
	}
}
