package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func req(ua, accept string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/sub/tok", nil)
	r.Header.Set("User-Agent", ua)
	if accept != "" {
		r.Header.Set("Accept", accept)
	}
	return r
}

const browserAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

func TestBrowsersGetTheInfoPage(t *testing.T) {
	uas := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
		"Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36 Edg/126.0",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
	}
	for _, ua := range uas {
		if !wantsSubInfoPage(req(ua, browserAccept), "") {
			t.Errorf("browser not detected: %s", ua)
		}
	}
}

// The expensive mistake is the other direction: a proxy client handed HTML shows
// the user an empty node list and no reason why.
func TestProxyClientsNeverGetTheInfoPage(t *testing.T) {
	clients := []struct{ ua, accept string }{
		{"clash-verge/v2.0.0", "*/*"},
		{"ClashforWindows/0.20.39", ""},
		{"mihomo/1.18.0", "*/*"},
		{"sing-box 1.13.4", "*/*"},
		{"SFI/1.11.0 (io.nekohasekai.sfa)", "*/*"},
		{"Surge/2800 CFNetwork/1494", "*/*"},
		{"v2rayN/7.0", ""},
		{"Shadowrocket/2.2.32", "*/*"},
		{"NekoBox/1.3.6", "*/*"},
		{"curl/8.7.1", "*/*"},
		{"Go-http-client/2.0", ""},
		{"Stash/2.7.0", "*/*"},
		// A client that sends a browser-ish UA but not an HTML Accept still
		// gets its config: both signals are required.
		{"Mozilla/5.0 (compatible; Clash/1.0)", "*/*"},
	}
	for _, c := range clients {
		if wantsSubInfoPage(req(c.ua, c.accept), "") {
			t.Errorf("proxy client misdetected as a browser: %q accept=%q", c.ua, c.accept)
		}
	}
}

// An explicit ?format= always wins, even from a browser — that is how the info
// page's own format links work.
func TestExplicitFormatSuppressesInfoPage(t *testing.T) {
	ua := "Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
	for _, f := range []string{"clash", "singbox", "base64", "surge"} {
		if wantsSubInfoPage(req(ua, browserAccept), f) {
			t.Errorf("format=%s from a browser was hijacked by the info page", f)
		}
	}
}

func TestContentDispositionCarriesBothForms(t *testing.T) {
	got := contentDisposition("轻舟", "clash")
	if !strings.Contains(got, `filename="subscription.yaml"`) {
		t.Errorf("missing ASCII fallback: %s", got)
	}
	if !strings.Contains(got, `filename*=UTF-8''`) {
		t.Errorf("missing RFC 5987 form: %s", got)
	}
	// The UTF-8 form must be percent-encoded, never raw bytes in a header.
	if strings.Contains(got, "轻舟") {
		t.Errorf("site name was not percent-encoded: %s", got)
	}
	for _, c := range got {
		if c > 127 {
			t.Fatalf("header contains a non-ASCII byte: %s", got)
		}
	}
	if got := contentDisposition("", "singbox"); got != `attachment; filename="subscription.json"` {
		t.Errorf("blank site name = %q", got)
	}
	if got := contentDisposition("", "surge"); !strings.Contains(got, ".conf") {
		t.Errorf("surge extension = %q", got)
	}
	if got := contentDisposition("", "base64"); !strings.Contains(got, ".txt") {
		t.Errorf("base64 extension = %q", got)
	}
	// Characters that are legal in a URL path but would truncate or corrupt a
	// header parameter must still be escaped.
	got = contentDisposition(`a,b;c="d" e:f`, "clash")
	for _, bad := range []string{",", ";c", `"`, " ", ":"} {
		if strings.Contains(got[strings.Index(got, "UTF-8''"):], bad) {
			t.Errorf("unescaped %q in ext-value: %s", bad, got)
		}
	}
}

func TestRFC5987Escape(t *testing.T) {
	for in, want := range map[string]string{
		"plain.yaml": "plain.yaml",
		"a b":        "a%20b",
		"a,b":        "a%2Cb",
		"a=b":        "a%3Db",
		"a:b":        "a%3Ab",
		"a\"b":       "a%22b",
		"轻舟":         "%E8%BD%BB%E8%88%9F",
	} {
		if got := rfc5987Escape(in); got != want {
			t.Errorf("rfc5987Escape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSubInfoNumbers(t *testing.T) {
	s := subInfo{Used: 30 << 30, Total: 100 << 30}
	if s.UsedPercent() != 30 {
		t.Errorf("percent = %d, want 30", s.UsedPercent())
	}
	if s.RemainText() != "70.00 GiB" {
		t.Errorf("remain = %q", s.RemainText())
	}
	// Over quota must clamp, or the bar overflows its track.
	over := subInfo{Used: 200 << 30, Total: 100 << 30}
	if over.UsedPercent() != 100 {
		t.Errorf("over-quota percent = %d, want 100", over.UsedPercent())
	}
	if over.RemainText() != "已用尽" {
		t.Errorf("over-quota remain = %q", over.RemainText())
	}
	empty := subInfo{Used: 5 << 30}
	if empty.TotalText() != "0 B" || empty.RemainText() != "0 B" || empty.UsedPercent() != 0 {
		t.Errorf("zero quota rendered as %q/%q/%d", empty.TotalText(), empty.RemainText(), empty.UsedPercent())
	}
	if (subInfo{}).ExpiryText() != "无套餐" {
		t.Error("empty subscription should read as 无套餐")
	}
	if (subInfo{Total: 1}).ExpiryText() != "永久" {
		t.Error("positive quota with zero expiry should read as 永久")
	}
}

// The token lives in the URL; the page must not leak it into anything cacheable
// or indexable, and must escape whatever it echoes back.
func TestSubInfoPageIsSafeToRender(t *testing.T) {
	w := httptest.NewRecorder()
	(&API{}).writeSubInfoHTML(w, subInfo{
		SiteName: `<script>alert(1)</script>`,
		SubURL:   `https://x.example/sub/tok"><script>alert(2)</script>`,
		Total:    100, Used: 10,
	})
	body := w.Body.String()
	if strings.Contains(body, "<script>alert(1)") || strings.Contains(body, "<script>alert(2)") {
		t.Error("template did not escape injected markup")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", w.Header().Get("Cache-Control"))
	}
	if !strings.Contains(body, "noindex") {
		t.Error("page is missing the noindex directive")
	}
	if w.Header().Get("X-Frame-Options") == "" {
		t.Error("page is framable")
	}
}

func TestSubInfoFormatURL(t *testing.T) {
	s := subInfo{SubURL: "https://x.example/sub/tok"}
	if got := s.FormatURL("clash"); got != "https://x.example/sub/tok?format=clash" {
		t.Errorf("got %q", got)
	}
	q := subInfo{SubURL: "https://x.example/sub/tok?a=1"}
	if got := q.FormatURL("clash"); got != "https://x.example/sub/tok?a=1&format=clash" {
		t.Errorf("existing query not honoured: %q", got)
	}
}
