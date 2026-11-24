package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qingzhou/internal/store"

	"gopkg.in/yaml.v3"
)

func TestParseSubCustomHeaders(t *testing.T) {
	raw := "\n# comment\n  // another comment\n" +
		"profile-web-page-url: https://panel.example.com:8443/docs\n" +
		"announce: 欢迎使用轻舟\n" +
		"Profile-Update-Interval: 24\n"
	got, err := parseSubCustomHeaders(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d headers, want 3: %+v", len(got), got)
	}
	if got[0].Name != "profile-web-page-url" || got[0].Value != "https://panel.example.com:8443/docs" {
		t.Fatalf("header[0] = %+v", got[0])
	}
	if got[1].Name != "announce" || got[1].Value != "欢迎使用轻舟" {
		t.Fatalf("header[1] = %+v", got[1])
	}
	if got[2].Name != "Profile-Update-Interval" || got[2].Value != "24" {
		t.Fatalf("header[2] = %+v", got[2])
	}
}

func TestParseSubCustomHeadersRejectsInvalidAndForbidden(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"missing_colon", "announce without colon"},
		{"empty_name", ": value"},
		{"space_in_name", "Bad Name: value"},
		{"empty_value", "announce:   "},
		{"control_char_in_value", "announce: ok\x07bell"},
		{"duplicate_case_insensitive", "announce: a\nAnnounce: b"},
		{"forbidden_host", "Host: evil.example"},
		{"forbidden_content_length", "Content-Length: 10"},
		{"forbidden_transfer_encoding", "Transfer-Encoding: chunked"},
		{"forbidden_content_encoding", "Content-Encoding: gzip"},
		{"forbidden_set_cookie", "Set-Cookie: a=b"},
		{"forbidden_subscription_userinfo", "subscription-userinfo: upload=0"},
		{"forbidden_content_type", "Content-Type: text/html"},
		{"forbidden_cache_control", "Cache-Control: public, max-age=3600"},
		{"forbidden_content_disposition", "Content-Disposition: inline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseSubCustomHeaders(tc.raw); err == nil {
				t.Errorf("expected error for %q, got nil", tc.raw)
			}
		})
	}
}

func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"gzip", true},
		{"deflate, gzip, br", true},
		{"GZIP;q=0.5, br;q=1.0", true},
		{"gzip;q=0", false},
		{"gzip;q=0.000, deflate", false},
		{"gzip;q=Inf", false},
		{"gzip;q=1.5", false},
		{"gzip;q=NaN", false},
		{"br, deflate", false},
		{"x-gzip", false},
	}
	for _, tc := range cases {
		if got := acceptsGzip(tc.header); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestAdminSettingsValidatesSubCustomHeaders(t *testing.T) {
	a, st := newResetSubAPI(t)

	put := func(raw string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{settingSubCustomHeaders: raw})
		return putSettings(a, string(body))
	}

	if w := put("Host: evil.example"); w.Code != http.StatusBadRequest {
		t.Fatalf("forbidden header status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	if w := put("Subscription-Userinfo: upload=0"); w.Code != http.StatusBadRequest {
		t.Fatalf("managed header status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	valid := "# 公告与主页\nprofile-web-page-url: https://example.com/panel\nannounce: 维护通知"
	if w := put(valid); w.Code != http.StatusOK {
		t.Fatalf("valid header status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	got, _ := st.GetSetting(settingSubCustomHeaders)
	if got != valid {
		t.Fatalf("stored setting = %q, want %q", got, valid)
	}
}

func TestHandleSubCustomHeadersAndGzip(t *testing.T) {
	a, st := newResetSubAPI(t)
	uid, err := st.CreateUser(store.NewUser{Username: "sub-hdr", PasswordHash: "x", SubToken: "HDR_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	// Include a forbidden header directly in DB to verify runtime defense-in-depth.
	raw := strings.Join([]string{
		"# comment",
		"profile-web-page-url: https://panel.example.com:8443/home",
		"announce: hello-qingzhou",
		"Profile-Update-Interval: 24",
		"Cache-Control: public, max-age=9999",
		"Subscription-Userinfo: tampered",
	}, "\n")
	if err := st.SetSetting(settingSubCustomHeaders, raw); err != nil {
		t.Fatal(err)
	}

	// 1. Plain request (no Accept-Encoding: gzip)
	rPlain := httptest.NewRequest(http.MethodGet, "/sub/HDR_TOKEN?format=clash", nil)
	rPlain.Header.Set("User-Agent", "mihomo/1.19.0")
	wPlain := httptest.NewRecorder()
	a.Router().ServeHTTP(wPlain, rPlain)
	if wPlain.Code != http.StatusOK {
		t.Fatalf("plain status = %d, body = %s", wPlain.Code, wPlain.Body.String())
	}
	if got := wPlain.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("plain Content-Encoding = %q, want empty", got)
	}
	if got := wPlain.Header().Get("Profile-Web-Page-Url"); got != "https://panel.example.com:8443/home" {
		t.Errorf("Profile-Web-Page-Url = %q", got)
	}
	if got := wPlain.Header().Get("Announce"); got != "hello-qingzhou" {
		t.Errorf("Announce = %q", got)
	}
	if got := wPlain.Header().Get("Profile-Update-Interval"); got != "24" {
		t.Errorf("Profile-Update-Interval = %q, want 24", got)
	}
	if got := wPlain.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := wPlain.Header().Get("Subscription-Userinfo"); !strings.HasPrefix(got, "upload=") {
		t.Errorf("Subscription-Userinfo was overwritten: %q", got)
	}

	// 2. Gzip request (Accept-Encoding: gzip) for Clash YAML
	rGzip := httptest.NewRequest(http.MethodGet, "/sub/HDR_TOKEN?format=clash", nil)
	rGzip.Header.Set("User-Agent", "mihomo/1.19.0")
	rGzip.Header.Set("Accept-Encoding", "deflate, gzip, br")
	wGzip := httptest.NewRecorder()
	a.Router().ServeHTTP(wGzip, rGzip)
	if wGzip.Code != http.StatusOK {
		t.Fatalf("gzip status = %d", wGzip.Code)
	}
	if got := wGzip.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("gzip Content-Encoding = %q, want gzip", got)
	}
	if !strings.Contains(wGzip.Header().Get("Vary"), "Accept-Encoding") {
		t.Errorf("Vary header missing Accept-Encoding: %q", wGzip.Header().Get("Vary"))
	}
	if got := wGzip.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("gzip Cache-Control = %q, want no-store", got)
	}
	if got := wGzip.Header().Get("Profile-Web-Page-Url"); got != "https://panel.example.com:8443/home" {
		t.Errorf("gzip Profile-Web-Page-Url = %q", got)
	}
	gr, err := gzip.NewReader(bytes.NewReader(wGzip.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	decoded, err := io.ReadAll(gr)
	_ = gr.Close()
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	if string(decoded) != wPlain.Body.String() {
		t.Fatalf("decompressed body differs from plain body")
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(decoded, &parsed); err != nil {
		t.Fatalf("decompressed Clash YAML invalid: %v", err)
	}

	// 3. Gzip request for sing-box JSON
	rSB := httptest.NewRequest(http.MethodGet, "/sub/HDR_TOKEN?format=singbox", nil)
	rSB.Header.Set("User-Agent", "sing-box 1.14.4")
	rSB.Header.Set("Accept-Encoding", "gzip")
	wSB := httptest.NewRecorder()
	a.Router().ServeHTTP(wSB, rSB)
	if wSB.Code != http.StatusOK || wSB.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("singbox gzip status=%d encoding=%q", wSB.Code, wSB.Header().Get("Content-Encoding"))
	}
	grSB, err := gzip.NewReader(bytes.NewReader(wSB.Body.Bytes()))
	if err != nil {
		t.Fatalf("singbox gzip.NewReader: %v", err)
	}
	decodedSB, err := io.ReadAll(grSB)
	_ = grSB.Close()
	if err != nil {
		t.Fatalf("singbox gzip read: %v", err)
	}
	var sbJSON map[string]any
	if err := json.Unmarshal(decodedSB, &sbJSON); err != nil {
		t.Fatalf("decompressed sing-box JSON invalid: %v", err)
	}

	// 4. gzip;q=0 must stay plaintext
	rQ0 := httptest.NewRequest(http.MethodGet, "/sub/HDR_TOKEN?format=base64", nil)
	rQ0.Header.Set("Accept-Encoding", "gzip;q=0")
	wQ0 := httptest.NewRecorder()
	a.Router().ServeHTTP(wQ0, rQ0)
	if wQ0.Code != http.StatusOK || wQ0.Header().Get("Content-Encoding") != "" {
		t.Fatalf("q=0 status=%d encoding=%q", wQ0.Code, wQ0.Header().Get("Content-Encoding"))
	}

	// 5. Browser info HTML must NOT receive custom subscription headers
	rInfo := httptest.NewRequest(http.MethodGet, "/sub/HDR_TOKEN", nil)
	rInfo.Header.Set("Accept", browserAccept)
	rInfo.Header.Set("User-Agent", "Mozilla/5.0 Chrome/126.0 Safari/537.36")
	wInfo := httptest.NewRecorder()
	a.Router().ServeHTTP(wInfo, rInfo)
	if wInfo.Code != http.StatusOK || wInfo.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("info status=%d ct=%q", wInfo.Code, wInfo.Header().Get("Content-Type"))
	}
	if got := wInfo.Header().Get("Announce"); got != "" {
		t.Errorf("browser info page unexpectedly received custom header Announce=%q", got)
	}

	// Telemetry was recorded on successful subscription fetch.
	u, _ := st.UserByID(uid)
	if u.SubLastFetchedAt == 0 {
		t.Errorf("expected subscription fetch telemetry to be recorded")
	}
}
