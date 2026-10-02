package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"qingzhou/internal/store"
	"testing"
)

func TestAudienceAndMachineEndpointsRequireAdmin(t *testing.T) {
	a, st := newUserEditAPI(t)
	u, _ := st.CreateUser(store.NewUser{Username: "u", PasswordHash: "x"})
	admin, _ := st.CreateUser(store.NewUser{Username: "admin", PasswordHash: "x", Role: "admin"})
	for _, path := range []string{"/api/admin/stats/audience/counts", "/api/admin/stats/audience?scope=node&id=1", "/api/admin/stats/usage/machine?server=1"} {
		for _, c := range []struct {
			id     int64
			role   string
			status int
		}{{0, "", 401}, {u, "user", 403}, {admin, "admin", 200}} {
			w := pointCodeRequest(t, a, st, c.id, c.role, http.MethodGet, path, "")
			if w.Code != c.status {
				t.Fatalf("%s role %s = %d: %s", path, c.role, w.Code, w.Body.String())
			}
		}
	}
}
func TestAudienceValidationAndEmptyShape(t *testing.T) {
	a, _ := newUserEditAPI(t)
	for _, query := range []string{"?scope=node&id=0", "?scope=bogus&id=1", "?scope=node&id=1%20OR%201=1", "?scope=server&id=-1", "?scope=node&id=1&page=1000001"} {
		w := httptest.NewRecorder()
		a.handleAdminAudience(w, httptest.NewRequest("GET", "/"+query, nil))
		if w.Code != 400 {
			t.Fatalf("bad query accepted %s", query)
		}
	}
	w := httptest.NewRecorder()
	a.handleAdminAudience(w, httptest.NewRequest("GET", "/?scope=node&id=1&page=99999", nil))
	var r struct {
		Data struct {
			Users []store.AudienceUser `json:"users"`
			Total int                  `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Data.Users == nil || r.Data.Total != 0 {
		t.Fatalf("empty shape %s", w.Body.String())
	}
	for _, q := range []string{"?server=-1", "?server=abc", "?server=1&page=999999999"} {
		w = httptest.NewRecorder()
		a.handleAdminMachineUsage(w, httptest.NewRequest("GET", "/"+q, nil))
		if w.Code != 400 {
			t.Fatalf("bad machine query accepted %s", q)
		}
	}
}
func TestLocalMachineZeroIsQueryable(t *testing.T) {
	a, st := newUserEditAPI(t)
	if _, err := st.DB().Exec(`INSERT INTO server_user_traffic_samples(server_id,user_id,ts,up,down) VALUES(0,123,strftime('%s','now'),10,20)`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.handleAdminMachineUsage(w, httptest.NewRequest("GET", "/?server=0", nil))
	if w.Code != 200 {
		t.Fatalf("local report %d %s", w.Code, w.Body.String())
	}
	var r struct {
		Data struct {
			Report store.MachineUsageReport `json:"report"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Data.Report.Up != 10 || r.Data.Report.Down != 20 {
		t.Fatal("local usage lost")
	}
	w = httptest.NewRecorder()
	a.handleAdminAudience(w, httptest.NewRequest("GET", "/?scope=server&id=0", nil))
	if w.Code != 200 {
		t.Fatalf("local audience %d", w.Code)
	}
}
