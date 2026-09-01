package api

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"qingzhou/internal/subconv"
)

// wantsSubInfoPage reports whether this request is a person looking at the
// subscription URL in a browser rather than a proxy client fetching it.
//
// Pasting the link into the address bar is a routine mistake — it is the same
// string the user just copied — and until now it answered with a screenful of
// base64, which reads as "the link is broken". An HTML page saying how much
// quota is left and which client to paste it into is strictly more useful.
//
// The detection is deliberately narrow, because guessing wrong in the other
// direction is far worse: a proxy client that got HTML would show the user an
// empty node list. All three conditions must hold —
//
//   - no explicit ?format=, so anyone who asks for a format still gets it;
//   - the request accepts text/html, which proxy clients do not send;
//   - the User-Agent looks like a real browser engine.
//
// v2rayN, Clash, sing-box, Surge, Shadowrocket and curl all fail at least two
// of these.
func wantsSubInfoPage(r *http.Request, explicitFormat string) bool {
	if explicitFormat != "" {
		return false
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		return false
	}
	return isBrowserUA(r.Header.Get("User-Agent"))
}

func isBrowserUA(ua string) bool {
	if !strings.HasPrefix(ua, "Mozilla/") {
		return false
	}
	for _, engine := range []string{"Chrome/", "Safari/", "Firefox/", "Edg/", "Gecko/"} {
		if strings.Contains(ua, engine) {
			return true
		}
	}
	return false
}

// subscriptionClientForUA collapses User-Agent into a small operational label.
// Never persist the raw value: it is unbounded, often identifying, and adds no
// value to the support question this field answers.
func subscriptionClientForUA(ua string) string {
	s := strings.ToLower(strings.TrimSpace(ua))
	switch {
	case isBrowserUA(ua):
		return "browser"
	case strings.Contains(s, "mihomo"):
		return "mihomo"
	case strings.Contains(s, "clash"):
		return "clash"
	case strings.Contains(s, "stash"):
		return "stash"
	case strings.Contains(s, "sing-box"), strings.Contains(s, "singbox"),
		strings.HasPrefix(s, "sfa/"), strings.HasPrefix(s, "sfi/"),
		strings.HasPrefix(s, "sfm/"), strings.HasPrefix(s, "sft/"):
		return "sing-box"
	case strings.Contains(s, "surge"):
		return "surge"
	case strings.Contains(s, "shadowrocket"):
		return "shadowrocket"
	case strings.Contains(s, "v2rayn"):
		return "v2rayn"
	case strings.HasPrefix(s, "curl/"):
		return "curl"
	default:
		return "unknown"
	}
}

// subInfo is what the info page and ?format=info report. It is intentionally the
// same data the Subscription-Userinfo header already carries — nothing here is
// visible to a holder of the link that was not already.
type subInfo struct {
	SiteName  string
	SubURL    string
	Used      int64
	Total     int64 // 0 = no quota
	ExpiryAt  int64 // 0 = never
	NodeCount int
	Expired   bool
	OverQuota bool
}

func fmtBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for v := n / u; v >= u && exp < 4; v /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

func (s subInfo) UsedText() string { return fmtBytes(s.Used) }

func (s subInfo) TotalText() string {
	if s.Total <= 0 {
		return "0 B"
	}
	return fmtBytes(s.Total)
}

func (s subInfo) RemainText() string {
	if s.Total <= 0 {
		return "0 B"
	}
	if rem := s.Total - s.Used; rem > 0 {
		return fmtBytes(rem)
	}
	return "已用尽"
}

// UsedPercent is clamped to [0,100] so an over-quota account cannot render a bar
// wider than its track.
func (s subInfo) UsedPercent() int {
	if s.Total <= 0 {
		return 0
	}
	p := s.Used * 100 / s.Total
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	default:
		return int(p)
	}
}

func (s subInfo) ExpiryText() string {
	if s.ExpiryAt <= 0 {
		if s.Total <= 0 && s.NodeCount == 0 {
			return "无套餐"
		}
		return "永久"
	}
	return time.Unix(s.ExpiryAt, 0).Format("2006-01-02 15:04")
}

func (s subInfo) DaysLeft() int64 {
	if s.ExpiryAt <= 0 {
		return -1
	}
	d := (s.ExpiryAt - time.Now().Unix()) / 86400
	if d < 0 {
		return 0
	}
	return d
}

// Notice is the one line explaining why a subscription would come back empty.
// A banned account has no case here on purpose: handleSub answers it with 403
// long before this page is reached, so a branch for it would be dead code
// claiming to handle something it never sees.
func (s subInfo) Notice() string {
	switch {
	case s.Expired:
		return "套餐已到期，订阅当前不返回节点。续期后立即恢复。"
	case s.OverQuota:
		return "流量已用尽，订阅当前不返回节点。补充流量后立即恢复。"
	case s.NodeCount == 0:
		return "当前没有可用节点。若刚刚购买，请稍候片刻再刷新。"
	}
	return ""
}

// FormatURL builds the explicit-format variant of this subscription URL.
func (s subInfo) FormatURL(format string) string {
	sep := "?"
	if strings.Contains(s.SubURL, "?") {
		sep = "&"
	}
	return s.SubURL + sep + "format=" + url.QueryEscape(format)
}

// subInfoPage renders inline rather than pulling in the SPA: the person looking
// at it is not logged in — they hold a subscription token, which is not a
// session — so the panel's authenticated UI is not available to them.
var subInfoPage = template.Must(template.New("subinfo").Parse(`<!doctype html>
<html lang="zh-CN"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>{{.SiteName}} · 订阅</title>
<style>
:root{color-scheme:light dark;--bg:#f6f7f9;--card:#fff;--fg:#1f2328;--dim:#6b7280;--line:#e5e7eb;--accent:#3b82f6;--warn:#b45309;--warnbg:#fef3c7}
@media(prefers-color-scheme:dark){:root{--bg:#16181d;--card:#1f2228;--fg:#e6e8eb;--dim:#9aa1ab;--line:#31353d;--warn:#fbbf24;--warnbg:#3b2f12}}
*{box-sizing:border-box}
body{margin:0;padding:24px 16px;background:var(--bg);color:var(--fg);
 font:15px/1.7 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}
.wrap{max-width:560px;margin:0 auto}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:20px;margin-bottom:16px}
h1{font-size:19px;margin:0 0 4px}
.sub{color:var(--dim);font-size:13px;margin:0 0 20px}
.row{display:flex;justify-content:space-between;gap:12px;padding:7px 0;border-bottom:1px solid var(--line);font-size:14px}
.row:last-child{border-bottom:0}
.row span:first-child{color:var(--dim)}
.row b{font-weight:600}
.bar{height:7px;border-radius:4px;background:var(--line);overflow:hidden;margin:14px 0 4px}
.bar i{display:block;height:100%;background:var(--accent)}
.pct{font-size:12px;color:var(--dim);text-align:right}
.notice{background:var(--warnbg);color:var(--warn);border-radius:8px;padding:10px 12px;font-size:13px;margin-bottom:16px}
.urlbox{display:flex;gap:8px;margin-top:10px}
input{flex:1;min-width:0;font:12px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace;padding:9px 10px;
 border:1px solid var(--line);border-radius:8px;background:var(--bg);color:var(--fg)}
button{padding:9px 14px;border:0;border-radius:8px;background:var(--accent);color:#fff;font-size:13px;cursor:pointer;white-space:nowrap}
button:active{opacity:.8}
h2{font-size:14px;margin:0 0 8px}
.links{display:flex;flex-wrap:wrap;gap:8px;margin-top:4px}
.links a{font-size:13px;color:var(--accent);text-decoration:none;border:1px solid var(--line);border-radius:7px;padding:5px 11px}
.tip{color:var(--dim);font-size:12px;margin:10px 0 0}
</style></head><body><div class="wrap">

<div class="card">
  <h1>{{.SiteName}}</h1>
  <p class="sub">这是一条订阅链接。把它复制到代理客户端里导入，不要在浏览器里打开。</p>
  {{with .Notice}}<div class="notice">{{.}}</div>{{end}}
  <div class="row"><span>已用流量</span><b>{{.UsedText}}</b></div>
  <div class="row"><span>总流量</span><b>{{.TotalText}}</b></div>
  <div class="row"><span>剩余流量</span><b>{{.RemainText}}</b></div>
  <div class="row"><span>到期时间</span><b>{{.ExpiryText}}{{if ge .DaysLeft 0}}（剩 {{.DaysLeft}} 天）{{end}}</b></div>
  <div class="row"><span>可用节点</span><b>{{.NodeCount}}</b></div>
  {{if gt .Total 0}}<div class="bar"><i style="width:{{.UsedPercent}}%"></i></div>
  <div class="pct">{{.UsedPercent}}%</div>{{end}}
</div>

<div class="card">
  <h2>订阅地址</h2>
  <div class="urlbox">
    <input id="u" readonly value="{{.SubURL}}">
    <button onclick="copy()">复制</button>
  </div>
  <p class="tip">同一条链接会按客户端自动返回 Clash / sing-box / Surge 格式，其余客户端返回通用格式，无需手动选。</p>
</div>

<div class="card">
  <h2>指定格式（一般用不到）</h2>
  <div class="links">
    <a href="{{.FormatURL "clash"}}">Clash</a>
    <a href="{{.FormatURL "singbox"}}">sing-box</a>
    <a href="{{.FormatURL "surge"}}">Surge</a>
    <a href="{{.FormatURL "base64"}}">通用 base64</a>
  </div>
</div>

<script>
function copy(){
  var el=document.getElementById('u'); el.select(); el.setSelectionRange(0,99999);
  var done=function(){var b=document.querySelector('button');b.textContent='已复制';setTimeout(function(){b.textContent='复制'},1500)};
  if(navigator.clipboard&&navigator.clipboard.writeText){navigator.clipboard.writeText(el.value).then(done,function(){document.execCommand('copy');done()})}
  else{document.execCommand('copy');done()}
}
</script>
</div></body></html>`))

func (a *API) writeSubInfoHTML(w http.ResponseWriter, info subInfo) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Same reason the subscription body is no-store: the page reflects live
	// quota, and a token that has since been revoked must not be served from a
	// CDN as if it were still valid.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Nothing here is meant to be framed or indexed.
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if err := subInfoPage.Execute(w, info); err != nil {
		// Headers are already committed; a partial page is all we can do.
		return err
	}
	return nil
}

// subExt is the file extension matching a rendered subscription format.
func subExt(format string) string {
	switch format {
	case subconv.FormatClash:
		return "yaml"
	case subconv.FormatSingbox:
		return "json"
	case subconv.FormatSurge:
		return "conf"
	default:
		return "txt"
	}
}

// contentDisposition names the downloaded profile.
//
// Clash-family clients adopt this as the profile's display name. With no header
// they fall back to the URL's last path segment — which here is the
// subscription token, so the user ends up with their own secret shown as the
// profile name in a list they may well screenshot for support.
//
// Both forms are emitted: a plain ASCII `filename` every client can parse, and
// the RFC 5987 `filename*` carrying the real (often Chinese) site name for those
// that understand it. Sending only the UTF-8 form would give the rest mojibake.
func contentDisposition(siteName, format string) string {
	ext := subExt(format)
	ascii := "subscription." + ext
	name := strings.TrimSpace(siteName)
	if name == "" {
		return `attachment; filename="` + ascii + `"`
	}
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + rfc5987Escape(name+"."+ext)
}

// rfc5987Escape percent-encodes for an ext-value, per RFC 5987/8187.
//
// url.PathEscape is not a substitute: it leaves through characters that are
// legal in a URL path but not in a header parameter — ':', '=', '&', '@', ','
// among them. A site name containing one would produce a Content-Disposition
// that a strict parser reads as truncated or malformed, so the client would
// name the profile after the URL's last segment (the subscription token) after
// all — the exact outcome this header exists to prevent.
func rfc5987Escape(s string) string {
	const attrChar = "!#$&+-.^_`|~"
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte(attrChar, c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}
