package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"qingzhou/internal/store"
)

func TestAdminComplianceCheck(t *testing.T) {
	a, st := newUserEditAPI(t)
	sid, err := st.CreateServer(store.Server{Name: "香港1", Host: "203.0.113.7", Port: 22, SSHUser: "root", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var ran []string
	a.complianceRun = func(ctx context.Context, serverID int64, cmd string) (string, error) {
		if serverID == sid {
			return "", errors.New("dial tcp: i/o timeout")
		}
		ran = append(ran, cmd)
		return `tcp LISTEN 0 511 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=1,fd=6))
WEB 80 200 0`, nil
	}
	w := httptest.NewRecorder()
	a.handleAdminComplianceCheck(w, httptest.NewRequest("POST", "/api/admin/compliance/check", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Servers []complianceServer `json:"servers"`
			Counts  map[string]int     `json:"counts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data.Servers) != 2 {
		t.Fatalf("servers=%+v", resp.Data.Servers)
	}
	local, remote := resp.Data.Servers[0], resp.Data.Servers[1]
	if !local.LiveChecked || len(ran) != 1 {
		t.Fatalf("local not live-checked: %+v", local)
	}
	found := false
	for _, f := range local.Findings {
		if f.Rule == "live_web_port" && f.Port == 80 && f.Severity == "high" {
			found = true
		}
	}
	if !found {
		t.Fatalf("nginx on :80 not reported: %+v", local.Findings)
	}
	if remote.LiveChecked || remote.LiveNote == "" || remote.Findings == nil {
		t.Fatalf("remote failure not explained: %+v", remote)
	}
	if resp.Data.Counts["high"] < 1 {
		t.Fatalf("counts=%v", resp.Data.Counts)
	}
}
