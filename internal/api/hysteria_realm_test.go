package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qingzhou/internal/sbver"
)

func TestSaveHysteriaRealmRequiresSingbox114(t *testing.T) {
	a, st := newHostKeyAPI(t)
	body := `{"type":"hysteria2","tag":"hy2","listen":"::","listen_port":8443,"server_id":1,"enabled":true,"options":"{\"realm\":{\"server_url\":\"https://realm.example.com\",\"realm_id\":\"home\",\"token\":\"tok\"}}"}`

	rec := httptest.NewRecorder()
	a.handleAdminSaveSbInbound(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "版本未知") {
		t.Fatalf("unknown version: %d %s", rec.Code, rec.Body.String())
	}

	if err := st.SetNodeSingbox(1, sbver.Parse("sing-box version 1.13.18\nTags: with_quic,with_v2ray_api")); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	a.handleAdminSaveSbInbound(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "低于 1.14") {
		t.Fatalf("old core: %d %s", rec.Code, rec.Body.String())
	}

	if err := st.SetNodeSingbox(1, sbver.Parse("sing-box version "+sbver.CurrentReleaseVersion+"\nTags: with_quic,with_v2ray_api")); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	a.handleAdminSaveSbInbound(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("1.14 core: %d %s", rec.Code, rec.Body.String())
	}

	plain := `{"type":"hysteria2","tag":"hy2-plain","listen":"::","listen_port":8444,"server_id":1,"enabled":true,"options":"{\"up_mbps\":100}"}`
	if err := st.SetNodeSingbox(1, sbver.Parse("sing-box version 1.13.18\nTags: with_quic")); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	a.handleAdminSaveSbInbound(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(plain)))
	if rec.Code != http.StatusOK {
		t.Fatalf("plain hysteria2 on 1.13: %d %s", rec.Code, rec.Body.String())
	}
}
