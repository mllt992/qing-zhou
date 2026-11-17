package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/store"
)

func setupDisableSubAPI(t *testing.T) (*API, *store.Store, int64, int64, int64) {
	t.Helper()
	a, st := newUserEditAPI(t)
	uid, err := st.CreateUser(store.NewUser{
		Username: "sub-user", PasswordHash: "x", SubToken: "DISABLE_TOKEN",
		TrafficLimit: 10 << 30, ExpiryAt: time.Now().Add(24 * time.Hour).Unix(),
		Points: 1_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkgID, err := st.CreatePackage(store.Package{Type: "plan", Name: "P", PricePoints: 10, TrafficBytes: 100 << 30, DurationDays: 30, Stock: -1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := st.GetPackage(pkgID)
	if err != nil || pkg == nil {
		t.Fatal(err)
	}
	if _, err := st.Purchase(uid, pkg, "", nil); err != nil {
		t.Fatal(err)
	}
	sid, err := st.CreateServer(store.Server{Name: "s1", Host: "203.0.113.5", HostV6: "2001:db8::1", Port: 22, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSbInbound(&store.SbInbound{ServerID: sid, Type: "vless", Tag: "v-in", Listen: "::", ListenPort: 8443, Options: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	gid, err := st.CreateGroup(store.NodeGroup{Name: "g"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPlanGroups(pkgID, []int64{gid}); err != nil {
		t.Fatal(err)
	}
	nid, err := st.CreateNode(store.Node{Type: "self_built", Name: "香港 01", Protocol: "vless", InboundTag: "v-in", Enabled: true, GroupIDs: []int64{gid}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(store.Node{
		Type: "external", Name: "外链", Protocol: "trojan",
		ShareLink: "trojan://pw@198.51.100.9:443#外链",
		Enabled:   true, GroupIDs: []int64{gid},
	}); err != nil {
		t.Fatal(err)
	}
	return a, st, uid, nid, sid
}

func getSubFormat(t *testing.T, a *API, format string) string {
	t.Helper()
	w := httptest.NewRecorder()
	a.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sub/DISABLE_TOKEN?format="+format, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /sub?format=%s = %d: %s", format, w.Code, w.Body.String())
	}
	body := w.Body.String()
	if format == "base64" {
		raw := strings.TrimSpace(body)
		if dec, err := base64.StdEncoding.DecodeString(raw); err == nil {
			body = string(dec)
		}
	}
	if unesc, err := url.QueryUnescape(body); err == nil {
		body = unesc
	}
	return body
}

func hasOnlyV6Hongkong(body string) bool {
	return strings.Contains(body, "香港 01-v6") && strings.Count(body, "香港 01") == strings.Count(body, "香港 01-v6")
}

func assertSubContains(t *testing.T, body, format string, wantV4, wantV6, wantExt bool) {
	t.Helper()
	hasV6 := strings.Contains(body, "香港 01-v6")
	hasPlainV4 := strings.Contains(body, "香港 01") && !hasOnlyV6Hongkong(body)
	hasExt := strings.Contains(body, "外链")
	if hasPlainV4 != wantV4 || hasV6 != wantV6 || hasExt != wantExt {
		t.Fatalf("format %s: v4=%v (want %v) v6=%v (want %v) ext=%v (want %v)\n%s",
			format, hasPlainV4, wantV4, hasV6, wantV6, hasExt, wantExt, body)
	}
}

func withNodeID(id int64, req *http.Request) *http.Request {
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", strconv.FormatInt(id, 10))
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
}

func putNode(t *testing.T, a *API, id int64, body string) {
	t.Helper()
	req := withNodeID(id, httptest.NewRequest(http.MethodPut, "/api/admin/nodes/"+strconv.FormatInt(id, 10), strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.handleAdminUpdateNode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT node %d %s = %d: %s", id, body, w.Code, w.Body.String())
	}
}

func putServer(t *testing.T, a *API, id int64, body string) {
	t.Helper()
	req := withNodeID(id, httptest.NewRequest(http.MethodPut, "/api/admin/servers/"+strconv.FormatInt(id, 10), strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.handleAdminUpdateServer(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT server %d %s = %d: %s", id, body, w.Code, w.Body.String())
	}
}

func TestSubscriptionFormatsHonorNodeDisableAndFamilies(t *testing.T) {
	a, _, _, nid, sid := setupDisableSubAPI(t)
	formats := []string{"base64", "clash", "singbox"} // Surge has no VLESS equivalent

	// Populate the 30s cache with both families present.
	for _, format := range formats {
		assertSubContains(t, getSubFormat(t, a, format), format, true, true, true)
	}

	// Disable the self-built node: must disappear from every format immediately
	// (cache flushed by sbRebuildLog). The external node remains.
	putNode(t, a, nid, `{"enabled":false}`)
	for _, format := range formats {
		assertSubContains(t, getSubFormat(t, a, format), format, false, false, true)
	}

	putNode(t, a, nid, `{"enabled":true}`)
	for _, format := range formats {
		assertSubContains(t, getSubFormat(t, a, format), format, true, true, true)
	}

	putNode(t, a, nid, `{"output_v4":false,"output_v6":true}`)
	for _, format := range formats {
		assertSubContains(t, getSubFormat(t, a, format), format, false, true, true)
	}

	putNode(t, a, nid, `{"output_v4":false,"output_v6":false}`)
	for _, format := range formats {
		assertSubContains(t, getSubFormat(t, a, format), format, false, false, true)
	}

	putNode(t, a, nid, `{"output_v4":true,"output_v6":true}`)
	putServer(t, a, sid, `{"enabled":false}`)
	for _, format := range formats {
		assertSubContains(t, getSubFormat(t, a, format), format, false, false, true)
	}
	putServer(t, a, sid, `{"enabled":true}`)
	for _, format := range formats {
		assertSubContains(t, getSubFormat(t, a, format), format, true, true, true)
	}
}

func TestCreateNodeDefaultsMissingOutputFlagsOn(t *testing.T) {
	a, st, _, _, _ := setupDisableSubAPI(t)
	gid, err := st.CreateGroup(store.NodeGroup{Name: "g2"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"type":"self_built","name":"默认双栈","inbound_tag":"v-in","enabled":true,"group_ids":[` + strconv.FormatInt(gid, 10) + `]}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/nodes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.handleAdminCreateNode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data store.Node `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Data.OutputV4 || !resp.Data.OutputV6 {
		t.Fatalf("missing keys should default both on: %+v", resp.Data)
	}
}

func TestCreateNodeHonorsExplicitBothOff(t *testing.T) {
	a, st, _, _, _ := setupDisableSubAPI(t)
	gid, err := st.CreateGroup(store.NodeGroup{Name: "g3"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"type":"self_built","name":"双关","inbound_tag":"v-in","enabled":true,"output_v4":false,"output_v6":false,"group_ids":[` + strconv.FormatInt(gid, 10) + `]}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/nodes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.handleAdminCreateNode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data store.Node `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.OutputV4 || resp.Data.OutputV6 || !resp.Data.Enabled {
		t.Fatalf("explicit both-off should persist: %+v", resp.Data)
	}
}
