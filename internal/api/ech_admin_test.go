package api

import (
	"bytes"
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
	"gopkg.in/yaml.v3"

	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
)

func withChiParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestAdminECHKeygenAndCertTlsLifecycle_NoPrivateKeyLeak(t *testing.T) {
	a, st := newResetSubAPI(t)

	// 1. Keygen validates public_name and returns only public config + opaque token.
	badReq := httptest.NewRequest(http.MethodPost, "/api/admin/sb/tls/ech-keygen", strings.NewReader(`{"public_name":"bad host!"}`))
	wBad := httptest.NewRecorder()
	a.handleAdminGenerateECHKeypair(wBad, badReq)
	if wBad.Code != http.StatusBadRequest {
		t.Fatalf("bad public_name status = %d, want 400", wBad.Code)
	}

	genReq := httptest.NewRequest(http.MethodPost, "/api/admin/sb/tls/ech-keygen", strings.NewReader(`{"public_name":"cloudflare-ech.com"}`))
	wGen := httptest.NewRecorder()
	a.handleAdminGenerateECHKeypair(wGen, genReq)
	if wGen.Code != http.StatusOK {
		t.Fatalf("keygen status = %d body = %s", wGen.Code, wGen.Body.String())
	}
	if strings.Contains(wGen.Body.String(), "BEGIN ECH KEYS") {
		t.Fatalf("keygen response leaked ECH private key: %s", wGen.Body.String())
	}
	var genResp struct {
		Data struct {
			PublicName   string `json:"public_name"`
			ConfigPEM    string `json:"config_pem"`
			ConfigBase64 string `json:"config_base64"`
			KeyToken     string `json:"key_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(wGen.Body.Bytes(), &genResp); err != nil {
		t.Fatal(err)
	}
	if genResp.Data.PublicName != "cloudflare-ech.com" || genResp.Data.ConfigBase64 == "" || genResp.Data.KeyToken == "" {
		t.Fatalf("incomplete keygen response: %+v", genResp.Data)
	}

	// 2. Create self-signed cert TLS profile with ECH enabled.
	certPEM, certKeyPEM, err := singbox.GenerateSelfSignedCert("real.example.com", 365)
	if err != nil {
		t.Fatal(err)
	}

	// Reject TLS 1.2 max_version with ECH.
	badMaxBody, _ := json.Marshal(map[string]any{
		"name": "tls-ech", "server_name": "real.example.com",
		"certificate": certPEM, "key": certKeyPEM, "insecure": true,
		"max_version": "1.2", "ech_enabled": true, "ech_key_token": genResp.Data.KeyToken,
	})
	wBadMax := httptest.NewRecorder()
	a.handleAdminSaveCertTls(wBadMax, httptest.NewRequest(http.MethodPost, "/api/admin/sb/tls/cert", bytes.NewReader(badMaxBody)))
	if wBadMax.Code != http.StatusBadRequest {
		t.Fatalf("max_version=1.2 with ECH status = %d, want 400", wBadMax.Code)
	}

	// Reject tampered key token.
	badTokBody, _ := json.Marshal(map[string]any{
		"name": "tls-ech", "server_name": "real.example.com",
		"certificate": certPEM, "key": certKeyPEM, "insecure": true,
		"ech_enabled": true, "ech_key_token": "qzech1:tampered",
	})
	wBadTok := httptest.NewRecorder()
	a.handleAdminSaveCertTls(wBadTok, httptest.NewRequest(http.MethodPost, "/api/admin/sb/tls/cert", bytes.NewReader(badTokBody)))
	if wBadTok.Code != http.StatusBadRequest {
		t.Fatalf("tampered token status = %d, want 400", wBadTok.Code)
	}

	// Valid save.
	saveBody, _ := json.Marshal(map[string]any{
		"name": "tls-ech", "server_name": "real.example.com",
		"certificate": certPEM, "key": certKeyPEM, "insecure": true,
		"ech_enabled": true, "ech_key_token": genResp.Data.KeyToken,
	})
	wSave := httptest.NewRecorder()
	a.handleAdminSaveCertTls(wSave, httptest.NewRequest(http.MethodPost, "/api/admin/sb/tls/cert", bytes.NewReader(saveBody)))
	if wSave.Code != http.StatusOK {
		t.Fatalf("save cert tls status = %d body = %s", wSave.Code, wSave.Body.String())
	}
	if strings.Contains(wSave.Body.String(), "BEGIN ECH KEYS") {
		t.Fatalf("save response leaked ECH private key: %s", wSave.Body.String())
	}
	var saveResp struct {
		Data struct {
			ID            int64  `json:"id"`
			ECHEnabled    bool   `json:"ech_enabled"`
			ECHHasKey     bool   `json:"ech_has_key"`
			ECHPublicName string `json:"ech_public_name"`
			ECHConfig     string `json:"ech_config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(wSave.Body.Bytes(), &saveResp); err != nil {
		t.Fatal(err)
	}
	tlsID := saveResp.Data.ID
	if tlsID == 0 || !saveResp.Data.ECHEnabled || !saveResp.Data.ECHHasKey ||
		saveResp.Data.ECHPublicName != "cloudflare-ech.com" || saveResp.Data.ECHConfig != genResp.Data.ConfigBase64 {
		t.Fatalf("unexpected save view: %+v", saveResp.Data)
	}

	// 3. List endpoint redacts ECH private key.
	wList := httptest.NewRecorder()
	a.handleAdminListSbTls(wList, httptest.NewRequest(http.MethodGet, "/api/admin/sb/tls", nil))
	if wList.Code != http.StatusOK {
		t.Fatalf("list status = %d", wList.Code)
	}
	if strings.Contains(wList.Body.String(), "BEGIN ECH KEYS") {
		t.Fatalf("list response leaked ECH private key")
	}

	// 4. Attach a Trojan inbound + node + free group + user, verify generated
	// server config has real ECH key while preview API redacts it, and all 3
	// subscription formats carry ECH without leaking the private key.
	gid, err := st.CreateGroup(store.NodeGroup{Name: "free"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("free_group_id", strconv.FormatInt(gid, 10)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("node_host_override", "203.0.113.55"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSbInbound(&store.SbInbound{
		Type: "trojan", Tag: "tr-ech", Listen: "::", ListenPort: 443, TlsID: tlsID, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(store.Node{
		Type: "self_built", Name: "ECH-Node", Protocol: "trojan", InboundTag: "tr-ech",
		Enabled: true, GroupIDs: []int64{gid},
	}); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(store.NewUser{
		Username: "ech-user", PasswordHash: "x", SubToken: "ECH_SUB_TOK",
		TrafficLimit: 1 << 30, ExpiryAt: time.Now().Add(24 * time.Hour).Unix(),
		Points: 1_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkgID, err := st.CreatePackage(store.Package{Type: "plan", Name: "ECH-Plan", PricePoints: 10, TrafficBytes: 100 << 30, DurationDays: 30, Stock: -1, Enabled: true})
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
	if err := st.SetPlanGroups(pkgID, []int64{gid}); err != nil {
		t.Fatal(err)
	}

	// Real server config built for sing-box MUST include ECH private key.
	realCfg, err := a.buildPreviewConfig(0)
	if err != nil {
		t.Fatalf("buildPreviewConfig: %v", err)
	}
	if !strings.Contains(string(realCfg), "BEGIN ECH KEYS") {
		t.Fatalf("generated sing-box server config missing ECH keys:\n%s", realCfg)
	}

	// Preview API MUST redact ECH private key.
	wPrev := httptest.NewRecorder()
	a.handleAdminSbPreview(wPrev, httptest.NewRequest(http.MethodGet, "/api/admin/sb/preview?server_id=0", nil))
	if wPrev.Code != http.StatusOK {
		t.Fatalf("preview status = %d", wPrev.Code)
	}
	if strings.Contains(wPrev.Body.String(), "BEGIN ECH KEYS") {
		t.Fatalf("preview API leaked ECH private key")
	}
	if !strings.Contains(wPrev.Body.String(), "[REDACTED]") {
		t.Fatalf("preview API missing [REDACTED] marker:\n%s", wPrev.Body.String())
	}

	// Subscriptions: base64 share link, sing-box JSON, Clash YAML.
	for _, format := range []string{"base64", "singbox", "clash"} {
		w := httptest.NewRecorder()
		a.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sub/ECH_SUB_TOK?format="+format, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("sub format=%s status=%d body=%s", format, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if strings.Contains(body, "BEGIN ECH KEYS") {
			t.Fatalf("sub format=%s leaked ECH private key", format)
		}
		switch format {
		case "base64":
			dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body))
			if err != nil {
				t.Fatalf("base64 decode: %v", err)
			}
			u, err := url.Parse(strings.TrimSpace(string(dec)))
			if err != nil {
				t.Fatalf("url.Parse: %v", err)
			}
			if got := u.Query().Get("ech"); got != genResp.Data.ConfigBase64 {
				t.Errorf("share link ech = %q, want %q", got, genResp.Data.ConfigBase64)
			}
		case "singbox":
			if !strings.Contains(body, genResp.Data.ConfigBase64) || !strings.Contains(body, "BEGIN ECH CONFIGS") {
				t.Errorf("sing-box sub missing ECH config:\n%s", body)
			}
		case "clash":
			var doc struct {
				Proxies []map[string]any `yaml:"proxies"`
			}
			if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatalf("clash yaml: %v", err)
			}
			if len(doc.Proxies) != 1 {
				t.Fatalf("clash proxies = %d", len(doc.Proxies))
			}
			opts, _ := doc.Proxies[0]["ech-opts"].(map[string]any)
			if opts["enable"] != true || opts["config"] != genResp.Data.ConfigBase64 {
				t.Errorf("clash ech-opts = %#v", opts)
			}
			if doc.Proxies[0]["fingerprint"] == nil {
				t.Errorf("clash self-signed fingerprint missing alongside ECH")
			}
		}
	}

	// 5. Editing the TLS profile without re-submitting ech_key_token preserves ECH!
	editBody, _ := json.Marshal(map[string]any{
		"name": "tls-ech-renamed", "server_name": "real.example.com",
		"insecure": true, "alpn": []string{"h2"}, "ech_enabled": true,
	})
	wEdit := httptest.NewRecorder()
	reqEdit := withChiParam(httptest.NewRequest(http.MethodPut, "/api/admin/sb/tls/cert/"+strconv.FormatInt(tlsID, 10), bytes.NewReader(editBody)), "id", strconv.FormatInt(tlsID, 10))
	a.handleAdminSaveCertTls(wEdit, reqEdit)
	if wEdit.Code != http.StatusOK {
		t.Fatalf("edit status = %d body = %s", wEdit.Code, wEdit.Body.String())
	}
	if strings.Contains(wEdit.Body.String(), "BEGIN ECH KEYS") {
		t.Fatalf("edit response leaked ECH private key")
	}
	afterEdit, _ := st.GetSbTls(tlsID)
	var afterClient map[string]any
	_ = json.Unmarshal([]byte(afterEdit.ClientJSON), &afterClient)
	afterClientECH, _ := afterClient["ech"].(map[string]any)
	if !strings.Contains(afterEdit.ServerJSON, "BEGIN ECH KEYS") || singbox.ECHConfigBase64(afterClientECH["config"]) != genResp.Data.ConfigBase64 {
		t.Fatalf("edit did not preserve ECH keypair: server=%v clientCfg=%q", strings.Contains(afterEdit.ServerJSON, "BEGIN ECH KEYS"), singbox.ECHConfigBase64(afterClientECH["config"]))
	}

	// 5b. Calling UpdateRealityTls against a cert-TLS profile must fail cleanly
	// without corrupting mode or leaking ECH private keys.
	realityMisuseBody := `{"name":"oops","server_name":"real.example.com"}`
	wMisuse := httptest.NewRecorder()
	reqMisuse := withChiParam(httptest.NewRequest(http.MethodPut, "/api/admin/sb/tls/reality/"+strconv.FormatInt(tlsID, 10), strings.NewReader(realityMisuseBody)), "id", strconv.FormatInt(tlsID, 10))
	a.handleAdminUpdateRealityTls(wMisuse, reqMisuse)
	if wMisuse.Code != http.StatusBadRequest || strings.Contains(wMisuse.Body.String(), "BEGIN ECH KEYS") {
		t.Fatalf("reality update on cert TLS: code=%d body=%s", wMisuse.Code, wMisuse.Body.String())
	}

	// 5c. Mismatched key_token vs ech_public_name is rejected; changing
	// ech_public_name without key_token regenerates the ECH keypair.
	mismatchBody, _ := json.Marshal(map[string]any{
		"name": "tls-ech", "server_name": "real.example.com", "insecure": true,
		"ech_enabled": true, "ech_key_token": genResp.Data.KeyToken, "ech_public_name": "other.example.com",
	})
	wMismatch := httptest.NewRecorder()
	a.handleAdminSaveCertTls(wMismatch, withChiParam(httptest.NewRequest(http.MethodPut, "/api/admin/sb/tls/cert/"+strconv.FormatInt(tlsID, 10), bytes.NewReader(mismatchBody)), "id", strconv.FormatInt(tlsID, 10)))
	if wMismatch.Code != http.StatusBadRequest {
		t.Fatalf("mismatched key_token/public_name status = %d, want 400", wMismatch.Code)
	}

	rotateBody, _ := json.Marshal(map[string]any{
		"name": "tls-ech-rotated", "server_name": "real.example.com", "insecure": true,
		"ech_enabled": true, "ech_public_name": "new-public.example.com",
	})
	wRotate := httptest.NewRecorder()
	a.handleAdminSaveCertTls(wRotate, withChiParam(httptest.NewRequest(http.MethodPut, "/api/admin/sb/tls/cert/"+strconv.FormatInt(tlsID, 10), bytes.NewReader(rotateBody)), "id", strconv.FormatInt(tlsID, 10)))
	if wRotate.Code != http.StatusOK || strings.Contains(wRotate.Body.String(), "BEGIN ECH KEYS") {
		t.Fatalf("rotate public_name status=%d body=%s", wRotate.Code, wRotate.Body.String())
	}
	var rotateResp struct {
		Data struct {
			ECHPublicName string `json:"ech_public_name"`
			ECHConfig     string `json:"ech_config"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wRotate.Body.Bytes(), &rotateResp)
	if rotateResp.Data.ECHPublicName != "new-public.example.com" || rotateResp.Data.ECHConfig == "" || rotateResp.Data.ECHConfig == genResp.Data.ConfigBase64 {
		t.Fatalf("expected regenerated ECH for new-public.example.com, got %+v", rotateResp.Data)
	}

	// 6. Disabling ECH removes it from server config and subscriptions cleanly.
	offBody, _ := json.Marshal(map[string]any{
		"name": "tls-ech-off", "server_name": "real.example.com",
		"insecure": true, "ech_enabled": false,
	})
	wOff := httptest.NewRecorder()
	reqOff := withChiParam(httptest.NewRequest(http.MethodPut, "/api/admin/sb/tls/cert/"+strconv.FormatInt(tlsID, 10), bytes.NewReader(offBody)), "id", strconv.FormatInt(tlsID, 10))
	a.handleAdminSaveCertTls(wOff, reqOff)
	if wOff.Code != http.StatusOK {
		t.Fatalf("disable ECH status = %d body = %s", wOff.Code, wOff.Body.String())
	}
	a.invalidateLinks(uid)
	wClashOff := httptest.NewRecorder()
	a.Router().ServeHTTP(wClashOff, httptest.NewRequest(http.MethodGet, "/sub/ECH_SUB_TOK?format=clash", nil))
	if strings.Contains(wClashOff.Body.String(), "ech-opts") {
		t.Errorf("disabled ECH still appeared in Clash subscription")
	}
}
