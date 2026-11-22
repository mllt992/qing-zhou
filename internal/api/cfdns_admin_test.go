package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"qingzhou/internal/cfdns"
	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
)

func TestServerCFDNSLifecycleAndClientHostPriority(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting("cf_api_token", "cf-tok-secret"); err != nil {
		t.Fatal(err)
	}

	records := map[string]cfdns.Record{
		"existing-manual": {ID: "existing-manual", Type: "A", Name: "conflict.mllt.vip", Content: "9.9.9.9", Proxied: false, Comment: "manual"},
	}
	cfSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cf-tok-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"message": "bad token"}}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  []cfdns.Zone{{ID: "zone1", Name: "mllt.vip", Status: "active"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone1/dns_records":
			typ := r.URL.Query().Get("type")
			name := r.URL.Query().Get("name")
			var out []cfdns.Record
			for _, rec := range records {
				if (typ == "" || rec.Type == typ) && (name == "" || rec.Name == name) {
					out = append(out, rec)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": out})
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone1/dns_records":
			var rec cfdns.Record
			_ = json.NewDecoder(r.Body).Decode(&rec)
			rec.ID = "rec-" + rec.Type + "-" + rec.Name
			records[rec.ID] = rec
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": rec})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/zones/zone1/dns_records/"):
			id := strings.TrimPrefix(r.URL.Path, "/zones/zone1/dns_records/")
			var rec cfdns.Record
			_ = json.NewDecoder(r.Body).Decode(&rec)
			rec.ID = id
			records[id] = rec
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": rec})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/zones/zone1/dns_records/"):
			id := strings.TrimPrefix(r.URL.Path, "/zones/zone1/dns_records/")
			delete(records, id)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]string{"id": id}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer cfSrv.Close()

	a.cfClientFn = func(token string) *cfdns.Client {
		return &cfdns.Client{Token: token, APIBase: cfSrv.URL, HTTP: cfSrv.Client()}
	}

	sid, err := st.CreateServer(store.Server{Name: "TAXI", Host: "217.216.188.164", HostV6: "2402:1920:fc:af::2", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Test token endpoint
	w := httptest.NewRecorder()
	a.handleTestCFToken(w, adminRequest(http.MethodPost, "/api/admin/settings/test-cf-token", `{}`))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "mllt.vip") {
		t.Fatalf("test-cf-token failed: %d %s", w.Code, w.Body.String())
	}

	// 2. Conflict returns 409 without overwriting
	body := `{"domain":"conflict.mllt.vip","sync_v4":true,"overwrite":false}`
	req := withURLParam(adminRequest(http.MethodPost, "/api/admin/servers/"+strconv.FormatInt(sid, 10)+"/dns", body), "id", strconv.FormatInt(sid, 10))
	w = httptest.NewRecorder()
	a.handleAdminServerCFDNS(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict, got %d %s", w.Code, w.Body.String())
	}

	// 3. Configure A + AAAA subdomains on Cloudflare
	body = `{"domain":"taxi.mllt.vip","domain_v6":"taxi-v6.mllt.vip","sync_v4":true,"sync_v6":true,"cleanup_old":true}`
	req = withURLParam(adminRequest(http.MethodPost, "/api/admin/servers/"+strconv.FormatInt(sid, 10)+"/dns", body), "id", strconv.FormatInt(sid, 10))
	w = httptest.NewRecorder()
	a.handleAdminServerCFDNS(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("configure dns failed: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"suggested_wildcard":"*.mllt.vip"`) {
		t.Fatalf("expected suggested_wildcard *.mllt.vip in %s", w.Body.String())
	}

	sv, _ := st.GetServer(sid)
	if sv.Host != "217.216.188.164" || sv.Domain != "taxi.mllt.vip" || sv.DomainV6 != "taxi-v6.mllt.vip" {
		t.Fatalf("server fields wrong: host=%q domain=%q domain_v6=%q", sv.Host, sv.Domain, sv.DomainV6)
	}
	if sv.ClientHost() != "taxi.mllt.vip" || sv.ClientHostV6() != "taxi-v6.mllt.vip" {
		t.Fatalf("ClientHost helpers wrong: %q / %q", sv.ClientHost(), sv.ClientHostV6())
	}

	// Verify httpsProxyHostWarning uses ClientHost (domain) and passes with wildcard cert!
	certPEM, keyPEM, _ := singbox.GenerateSelfSignedCert("*.mllt.vip", 365)
	cid, _ := st.SaveCert(&store.Cert{Name: "wc", Domain: "*.mllt.vip", Source: "acme", CertPEM: certPEM, KeyPEM: keyPEM})
	tid, _ := st.SaveSbTls(&store.SbTls{ServerID: sid, Name: "wc", Mode: "tls", CertID: cid, ServerJSON: `{"enabled":true,"server_name":"taxi.mllt.vip"}`, ClientJSON: `{}`})
	ib := &store.SbInbound{ServerID: sid, Type: "mixed", Tag: "px", Listen: "::", ListenPort: 7648, TlsID: tid, Options: "{}", Enabled: true}
	if warn := httpsProxyHostWarning(st.InboundTLSInfo(ib), []string{sv.ClientHost(), sv.ClientHostV6()}); warn != "" {
		t.Fatalf("wildcard cert covering domain/domain_v6 must not warn: %s", warn)
	}

	// 4. Delete managed DNS records + clear server domain
	dreq := withURLParam(adminRequest(http.MethodDelete, "/api/admin/servers/"+strconv.FormatInt(sid, 10)+"/dns?delete_cf=1", ""), "id", strconv.FormatInt(sid, 10))
	w = httptest.NewRecorder()
	a.handleAdminDeleteServerCFDNS(w, dreq)
	if w.Code != http.StatusOK {
		t.Fatalf("delete dns failed: %d %s", w.Code, w.Body.String())
	}
	sv, _ = st.GetServer(sid)
	if sv.Domain != "" || sv.DomainV6 != "" || sv.ClientHost() != "217.216.188.164" {
		t.Fatalf("domain not cleared: %+v", sv)
	}
	if _, stillManual := records["existing-manual"]; !stillManual {
		t.Fatal("delete_cf must never touch unmanaged manual records")
	}
	_ = bytes.NewBuffer(nil)
}
