// Package compliance checks a server's sing-box config and its live listening
// sockets against common hosting-provider rules (#99):
//
//  1. no unrestricted public HTTP/SOCKS-style proxy;
//  2. no unrestricted public web service on 80/443/8080/8443.
//
// Everything here is rule based and read-only: it reports findings with a
// severity, a plain-Chinese explanation and a suggested fix. It never changes a
// config or a server.
package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Severity levels, ordered.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
	Info   = "info"
)

var sevRank = map[string]int{High: 0, Medium: 1, Low: 2, Info: 3}

// WebPorts are the ports hosting providers treat as web-service ports.
var WebPorts = map[int]bool{80: true, 443: true, 8080: true, 8443: true}

// Finding is one compliance issue.
type Finding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix"`
	Port     int    `json:"port,omitempty"`
	Target   string `json:"target,omitempty"` // inbound tag / process / "面板"
}

// Listener is a live listening socket on a server.
type Listener struct {
	Proto   string // tcp | udp
	Addr    string // bind address, "" / 0.0.0.0 / :: = all
	Port    int
	Process string
	// HTTP is the status code a plain-HTTP request to 127.0.0.1:Port returned
	// (0 = not tried / no answer); HTTPS likewise over TLS.
	HTTP, HTTPS int
}

// IsPublicBind reports whether a bind address is reachable from outside the
// host: wildcard or a global unicast address.
func IsPublicBind(addr string) bool {
	a := strings.Trim(strings.TrimSpace(addr), "[]")
	if i := strings.IndexByte(a, '%'); i >= 0 {
		a = a[:i]
	}
	switch a {
	case "", "*", "0.0.0.0", "::", "::0":
		return true
	case "localhost":
		return false
	}
	ip := net.ParseIP(a)
	if ip == nil {
		return true // a hostname: assume reachable
	}
	if v4 := ip.To4(); v4 != nil && strings.HasPrefix(a, "::ffff:") {
		ip = v4
	}
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

type inboundTLS struct {
	Enabled    bool   `json:"enabled"`
	ServerName string `json:"server_name"`
	Reality    *struct {
		Enabled   bool `json:"enabled"`
		Handshake struct {
			Server     string `json:"server"`
			ServerPort int    `json:"server_port"`
		} `json:"handshake"`
		ShortID json.RawMessage `json:"short_id"`
	} `json:"reality"`
}

type inbound struct {
	Type       string                 `json:"type"`
	Tag        string                 `json:"tag"`
	Listen     string                 `json:"listen"`
	ListenPort int                    `json:"listen_port"`
	Users      []json.RawMessage      `json:"users"`
	TLS        *inboundTLS            `json:"tls"`
	Transport  map[string]interface{} `json:"transport"`
	Fallback   map[string]interface{} `json:"fallback"`
	FallbackFA map[string]interface{} `json:"fallback_for_alpn"`
	Masquerade json.RawMessage        `json:"masquerade"`
}

type config struct {
	Inbounds []inbound `json:"inbounds"`
	Route    struct {
		Rules []map[string]interface{} `json:"rules"`
	} `json:"route"`
	Experimental struct {
		ClashAPI *struct {
			ExternalController string `json:"external_controller"`
			Secret             string `json:"secret"`
		} `json:"clash_api"`
		V2RayAPI *struct {
			Listen string `json:"listen"`
		} `json:"v2ray_api"`
	} `json:"experimental"`
}

var proxyTypes = map[string]string{"http": "HTTP", "socks": "SOCKS", "mixed": "HTTP/SOCKS 混合"}

// restricted reports whether a route rule limits who may use this inbound by
// source address (an allow-list), which is what providers mean by 设限制.
// Non-filtering resolve rules and wildcard CIDRs (0.0.0.0/0, ::/0, ::ffff:0:0/96
// emitted by FollowSourceFamilyEgress) never count as a source restriction.
func restricted(rules []map[string]interface{}, tag string) bool {
	for _, r := range rules {
		if !mentions(r["inbound"], tag) {
			continue
		}
		if act, _ := r["action"].(string); act == "resolve" || act == "sniff" || act == "hijack-dns" {
			continue
		}
		if v, ok := r["source_ip_cidr"]; ok && hasNarrowSourceCIDR(v) {
			return true
		}
		if v, ok := r["source_ip_is_private"].(bool); ok && v {
			return true
		}
	}
	return false
}

func hasNarrowSourceCIDR(v interface{}) bool {
	isWildcard := func(s string) bool {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || s == "0.0.0.0/0" || s == "::/0" || s == "::ffff:0:0/96" || s == "::ffff:0.0.0.0/96" {
			return true
		}
		_, ipNet, err := net.ParseCIDR(s)
		if err != nil {
			return false
		}
		ones, _ := ipNet.Mask.Size()
		return ones == 0
	}
	switch t := v.(type) {
	case string:
		return !isWildcard(t)
	case []interface{}:
		if len(t) == 0 {
			return false
		}
		for _, x := range t {
			s, _ := x.(string)
			if isWildcard(s) {
				return false
			}
		}
		return true
	case []string:
		if len(t) == 0 {
			return false
		}
		for _, s := range t {
			if isWildcard(s) {
				return false
			}
		}
		return true
	}
	return false
}

func mentions(v interface{}, tag string) bool {
	switch t := v.(type) {
	case string:
		return t == tag
	case []interface{}:
		for _, x := range t {
			if s, _ := x.(string); s == tag {
				return true
			}
		}
	}
	return false
}

func tlsOn(ib inbound) bool { return ib.TLS != nil && ib.TLS.Enabled }

func plainHTTPTransport(ib inbound) string {
	t, _ := ib.Transport["type"].(string)
	switch t {
	case "ws", "http", "httpupgrade":
		return t
	}
	return ""
}

// AnalyzeConfig checks a generated sing-box config. It returns the findings
// and the public TCP ports the config itself listens on (so live sockets on
// those ports can be attributed to sing-box rather than reported twice).
func AnalyzeConfig(raw []byte) ([]Finding, map[int]string, error) {
	var c config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, nil, fmt.Errorf("配置不是合法 JSON")
	}
	var out []Finding
	owned := map[int]string{}
	for _, ib := range c.Inbounds {
		if ib.ListenPort <= 0 || !IsPublicBind(ib.Listen) {
			continue
		}
		owned[ib.ListenPort] = ib.Tag
		name := ib.Tag
		if name == "" {
			name = ib.Type
		}
		if label, isProxy := proxyTypes[ib.Type]; isProxy {
			lim := restricted(c.Route.Rules, ib.Tag)
			switch {
			case len(ib.Users) == 0 && !lim:
				out = append(out, Finding{Rule: "proxy_open", Severity: High, Port: ib.ListenPort, Target: name,
					Title:  fmt.Sprintf("公网开放了无密码的 %s 代理", label),
					Detail: fmt.Sprintf("入站「%s」在公网端口 %d 上提供 %s 代理，没有设置账号密码，也没有限制来源 IP，任何人都能直接使用。这正是服务商禁止的“不设限制公开暴露不安全代理协议”。", name, ib.ListenPort, label),
					Fix:    "关闭这个入站，或者改为只监听 127.0.0.1；确实需要对外时，至少设置账号密码，并在防火墙/安全组里只放行固定来源 IP。"})
			case !lim:
				sev, extra := Medium, ""
				if ib.Type != "socks" && !tlsOn(ib) {
					extra = "而且没有开启 TLS，账号密码会明文传输。"
				}
				out = append(out, Finding{Rule: "proxy_no_ip_limit", Severity: sev, Port: ib.ListenPort, Target: name,
					Title:  fmt.Sprintf("公网 %s 代理没有限制来源 IP", label),
					Detail: fmt.Sprintf("入站「%s」在公网端口 %d 上提供 %s 代理，虽然有账号密码，但没有限制来源 IP。HTTP/SOCKS 属于服务商认定的不安全代理协议，只靠密码通常仍被视为“不设限制”。%s", name, ib.ListenPort, label, extra),
					Fix:    "在防火墙/安全组里只放行需要使用的来源 IP；或者改用 VLESS、Trojan、Hysteria2 等加密协议对外提供服务，HTTP/SOCKS 只在本机或内网使用。"})
			case ib.Type != "socks" && !tlsOn(ib):
				out = append(out, Finding{Rule: "proxy_plaintext", Severity: Low, Port: ib.ListenPort, Target: name,
					Title:  fmt.Sprintf("%s 代理未加密", label),
					Detail: fmt.Sprintf("入站「%s」已经限制了来源 IP，但没有开启 TLS，账号密码和访问内容会明文传输。", name),
					Fix:    "为该入站开启 TLS，或者只在内网使用。"})
			}
			if WebPorts[ib.ListenPort] && !lim {
				out = append(out, Finding{Rule: "web_port_proxy", Severity: High, Port: ib.ListenPort, Target: name,
					Title:  fmt.Sprintf("%d 端口上对外提供 HTTP 类服务", ib.ListenPort),
					Detail: fmt.Sprintf("入站「%s」占用了 %d 端口，这是服务商重点检查的 Web 端口，未限制来源时会被认定为公开托管 Web/代理服务。", name, ib.ListenPort),
					Fix:    "把这个入站换到非 80/443/8080/8443 的端口，并限制来源 IP。"})
			}
			continue
		}
		// Reality / Hysteria2 / Fallback abuse checks apply on EVERY public port,
		// not only 80/443/8080/8443: scanners probe arbitrary high ports too.
		if ib.TLS != nil && ib.TLS.Enabled && ib.TLS.Reality != nil && ib.TLS.Reality.Enabled {
			target := strings.TrimSpace(ib.TLS.Reality.Handshake.Server)
			if target == "" {
				target = strings.TrimSpace(ib.TLS.ServerName)
			}
			if cdnKind, cdnLabel := classifySharedCDNHost(target); cdnKind != "" {
				sev := Medium
				if cdnKind == "cloudflare" {
					sev = High
				}
				out = append(out, Finding{
					Rule:     "reality_cdn_fallback",
					Severity: sev,
					Port:     ib.ListenPort,
					Target:   name,
					Title:    fmt.Sprintf("Reality 握手目标指向 %s 共享 CDN（%s）", cdnLabel, target),
					Detail:   fmt.Sprintf("入站「%s」（端口 %d）的 Reality 回落目标 %s 托管在 %s 共享边缘网络。未认证的外部请求伪造 SNI 后会被原样透传到该 CDN 边缘节点，可能被僵尸网络用作 C2 代理或触发服务商（如 DMIT）滥用警告。", name, ib.ListenPort, target, cdnLabel),
					Fix:      "将 Reality 握手目标更换为独立源站（非 Cloudflare / Akamai / Fastly 等多租户 CDN），或改用自有域名证书的 VLESS+TLS / Hysteria2（不向外网回落）。",
				})
			}
			if realityHasEmptyShortID(ib.TLS.Reality.ShortID) {
				out = append(out, Finding{
					Rule:     "reality_empty_short_id",
					Severity: Medium,
					Port:     ib.ListenPort,
					Target:   name,
					Title:    "Reality 未设置有效 Short ID（允许空 short_id）",
					Detail:   fmt.Sprintf("入站「%s」（端口 %d）的 Reality short_id 为空或包含空字符串，降低了握手校验强度。", name, ib.ListenPort),
					Fix:      "在 TLS/Reality 配置中生成至少一个非空十六进制 Short ID，并移除空项。",
				})
			}
		}
		if ib.Type == "hysteria2" {
			if targetURL := externalMasqueradeURL(ib.Masquerade); targetURL != "" {
				u, _ := url.Parse(targetURL)
				host := ""
				if u != nil {
					host = u.Hostname()
				}
				sev := Medium
				if cdnKind, _ := classifySharedCDNHost(host); cdnKind == "cloudflare" {
					sev = High
				}
				out = append(out, Finding{
					Rule:     "hysteria2_external_masquerade",
					Severity: sev,
					Port:     ib.ListenPort,
					Target:   name,
					Title:    "Hysteria2 伪装（masquerade）反代了外部网站",
					Detail:   fmt.Sprintf("入站「%s」（端口 %d）将未认证的 HTTP/3 流量反向代理到外部地址 %s，可能被外部滥用或触发服务商开放代理告警。", name, ib.ListenPort, targetURL),
					Fix:      "移除 masquerade 配置（默认由本机直接返回 404），或改为指向本机静态文件目录。",
				})
			}
		}
		if !WebPorts[ib.ListenPort] {
			if ext := externalFallbackTarget(ib.Fallback, ib.FallbackFA); ext != "" {
				sev := Medium
				if cdnKind, _ := classifySharedCDNHost(ext); cdnKind == "cloudflare" {
					sev = High
				}
				out = append(out, Finding{
					Rule:     "fallback_external",
					Severity: sev,
					Port:     ib.ListenPort,
					Target:   name,
					Title:    fmt.Sprintf("入站回落（fallback）指向外部主机 %s", ext),
					Detail:   fmt.Sprintf("入站「%s」（端口 %d）将未认证流量回落转发到外部服务器 %s，可能导致本机被当作未认证 HTTPS 跳板。", name, ib.ListenPort, ext),
					Fix:      "删除指向外网的 fallback，或改为回落到 127.0.0.1 上的本机静态页面。",
				})
			}
			continue
		}
		if fb := ib.Fallback; len(fb) > 0 || len(ib.FallbackFA) > 0 {
			out = append(out, Finding{Rule: "web_port_fallback", Severity: Medium, Port: ib.ListenPort, Target: name,
				Title:  fmt.Sprintf("%d 端口的回落会把访问转给一个网站", ib.ListenPort),
				Detail: fmt.Sprintf("入站「%s」设置了回落（fallback），浏览器或扫描器访问 %d 端口时会看到被转发的网站，相当于在公网 Web 端口上公开托管了一个站点。", name, ib.ListenPort),
				Fix:    "确认回落的网站本身合规且不对外提供可被滥用的内容；不需要伪装站点时删除回落设置，或把入站换到其他端口。"})
		} else if t := plainHTTPTransport(ib); t != "" && !tlsOn(ib) {
			out = append(out, Finding{Rule: "web_port_plain", Severity: Medium, Port: ib.ListenPort, Target: name,
				Title:  fmt.Sprintf("%d 端口以明文 HTTP 对外响应", ib.ListenPort),
				Detail: fmt.Sprintf("入站「%s」在 %d 端口使用 %s 传输且没有 TLS，任何人用浏览器访问都会得到 HTTP 响应，容易被认定为公开 Web 服务。", name, ib.ListenPort, t),
				Fix:    "为该入站开启 TLS，或换到非 Web 端口；如果前面有 CDN / 反向代理，改为只监听 127.0.0.1 并限制来源。"})
		} else if ib.Type == "shadowtls" || ib.Type == "naive" {
			out = append(out, Finding{Rule: "web_port_camouflage", Severity: Low, Port: ib.ListenPort, Target: name,
				Title:  fmt.Sprintf("%d 端口对外呈现为网站", ib.ListenPort),
				Detail: fmt.Sprintf("入站「%s」（%s）在 %d 端口对未认证的访问会表现得像一个普通网站。", name, ib.Type, ib.ListenPort),
				Fix:    "确认服务商是否接受这种用法；不接受时换到其他端口。"})
		}
	}
	if api := c.Experimental.V2RayAPI; api != nil && api.Listen != "" {
		if host, _, err := net.SplitHostPort(api.Listen); err == nil && IsPublicBind(host) {
			out = append(out, Finding{Rule: "stats_api_public", Severity: High, Target: "v2ray_api",
				Title:  "流量统计接口暴露在公网",
				Detail: fmt.Sprintf("sing-box 的统计接口监听在 %s，外部可以直接访问。", api.Listen),
				Fix:    "在「sing-box 设置」里把统计接口地址改为 127.0.0.1。"})
		}
	}
	if api := c.Experimental.ClashAPI; api != nil && api.ExternalController != "" {
		if host, p, err := net.SplitHostPort(api.ExternalController); err == nil && IsPublicBind(host) {
			port, _ := strconv.Atoi(p)
			sev := Medium
			if api.Secret == "" || WebPorts[port] {
				sev = High
			}
			out = append(out, Finding{Rule: "clash_api_public", Severity: sev, Port: port, Target: "clash_api",
				Title:  "sing-box 控制接口暴露在公网",
				Detail: fmt.Sprintf("clash_api 监听在 %s，这是一个 Web 管理接口。", api.ExternalController),
				Fix:    "在基础配置里把 external_controller 改为 127.0.0.1，并设置 secret。"})
		}
	}
	return out, owned, nil
}

// AnalyzePanel checks how the panel itself (which also serves the user
// subscription links) listens.
func AnalyzePanel(listenAddr string) []Finding {
	host, p, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return nil
	}
	port, _ := strconv.Atoi(p)
	if !IsPublicBind(host) {
		return nil
	}
	if WebPorts[port] {
		return []Finding{{Rule: "panel_web_port", Severity: Medium, Port: port, Target: "面板",
			Title:  fmt.Sprintf("管理面板直接在公网 %d 端口提供网页", port),
			Detail: fmt.Sprintf("面板（包括登录页和用户订阅地址）监听在 %s，任何人都能打开，会被认定为在 Web 端口公开托管网站。", listenAddr),
			Fix:    "把面板改为只监听 127.0.0.1（环境变量 QZ_LISTEN），由反向代理对外，并在反向代理或安全组里限制可访问的来源 IP；或换到非 Web 端口。"}}
	}
	return []Finding{{Rule: "panel_public", Severity: Info, Port: port, Target: "面板",
		Title:  fmt.Sprintf("管理面板在公网 %d 端口可直接访问", port),
		Detail: "这个端口不在服务商点名的 Web 端口里，但面板和订阅地址对所有人可见。",
		Fix:    "如有条件，在安全组里限制来源 IP，或放到反向代理后面。"}}
}

// AnalyzeListeners checks live sockets. owned maps ports that the generated
// sing-box config explains (already judged by AnalyzeConfig); panelPort is the
// panel's own port on the panel host (0 elsewhere).
func AnalyzeListeners(ls []Listener, owned map[int]string, panelPort int) []Finding {
	var out []Finding
	seen := map[int]bool{}
	for _, l := range ls {
		if l.Proto != "tcp" || !WebPorts[l.Port] || !IsPublicBind(l.Addr) || seen[l.Port] {
			continue
		}
		seen[l.Port] = true
		if _, ok := owned[l.Port]; ok || l.Port == panelPort {
			continue
		}
		proc := l.Process
		who := "一个程序"
		if proc != "" {
			who = "「" + proc + "」"
		}
		sev, how := Medium, "无法确认它返回什么内容"
		switch {
		case l.HTTPS > 0:
			sev, how = Medium, fmt.Sprintf("用 HTTPS 访问返回了 %d，是一个网站", l.HTTPS)
		case l.HTTP > 0:
			sev, how = High, fmt.Sprintf("用 HTTP 访问返回了 %d，是一个明文网站", l.HTTP)
		}
		out = append(out, Finding{Rule: "live_web_port", Severity: sev, Port: l.Port, Target: proc,
			Title:  fmt.Sprintf("服务器上有程序在公网 %d 端口提供服务", l.Port),
			Detail: fmt.Sprintf("实际检测到%s监听在 %s:%d（不是轻舟生成的 sing-box 入站），%s。", who, displayAddr(l.Addr), l.Port, how),
			Fix:    "如果不需要，停掉这个服务；需要的话让它只监听 127.0.0.1，或在防火墙/安全组里只放行需要的来源 IP。已经在安全组限制过来源的可以忽略这一条。"})
	}
	return out
}

func displayAddr(a string) string {
	if a == "" || a == "*" {
		return "0.0.0.0"
	}
	return a
}

// ParseListeners parses `ss -Hltunp` (preferred) or `netstat -ltunp` output,
// plus "WEB <port> <http> <https>" lines from the probe script.
func ParseListeners(out string) []Listener {
	var ls []Listener
	web := map[int][2]int{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if f[0] == "WEB" && len(f) >= 4 {
			p, _ := strconv.Atoi(f[1])
			h, _ := strconv.Atoi(f[2])
			s, _ := strconv.Atoi(f[3])
			web[p] = [2]int{h, s}
			continue
		}
		proto := strings.ToLower(f[0])
		var local, proc string
		switch {
		case (proto == "tcp" || proto == "udp") && len(f) >= 5 && !strings.Contains(f[3], ":"):
			// ss: Netid State Recv-Q Send-Q Local Peer [Process]
			if len(f) < 6 {
				continue
			}
			local = f[4]
			if len(f) >= 7 {
				proc = ssProcess(strings.Join(f[6:], " "))
			}
		case strings.HasPrefix(proto, "tcp") || strings.HasPrefix(proto, "udp"):
			// netstat: Proto Recv-Q Send-Q Local Foreign [State] [PID/Program]
			if len(f) < 4 {
				continue
			}
			local = f[3]
			last := f[len(f)-1]
			if i := strings.IndexByte(last, '/'); i > 0 {
				proc = last[i+1:]
			}
			proto = proto[:3]
		default:
			continue
		}
		i := strings.LastIndexByte(local, ':')
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(local[i+1:])
		if err != nil {
			continue
		}
		ls = append(ls, Listener{Proto: proto, Addr: local[:i], Port: port, Process: proc})
	}
	for i := range ls {
		if w, ok := web[ls[i].Port]; ok {
			ls[i].HTTP, ls[i].HTTPS = w[0], w[1]
		}
	}
	return ls
}

// ssProcess pulls the program name out of users:(("nginx",pid=1,fd=6),...).
func ssProcess(s string) string {
	i := strings.Index(s, "((\"")
	if i < 0 {
		return ""
	}
	s = s[i+3:]
	if j := strings.IndexByte(s, '"'); j >= 0 {
		return s[:j]
	}
	return ""
}

// ListenScript is the read-only command run on a server to list its sockets
// and see whether its web ports answer plain HTTP/HTTPS from loopback.
const ListenScript = `(ss -Hltunp 2>/dev/null || ss -Hltun 2>/dev/null || netstat -ltunp 2>/dev/null || netstat -ltun 2>/dev/null) | head -n 500
if command -v curl >/dev/null 2>&1; then
for p in 80 443 8080 8443; do
  h=$(curl -s -o /dev/null -m 2 -w '%{http_code}' "http://127.0.0.1:$p/" 2>/dev/null); [ -n "$h" ] || h=0
  s=$(curl -sk -o /dev/null -m 2 -w '%{http_code}' "https://127.0.0.1:$p/" 2>/dev/null); [ -n "$s" ] || s=0
  echo "WEB $p $h $s"
done
fi`

// Sort orders findings by severity, then port.
func Sort(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if sevRank[fs[i].Severity] != sevRank[fs[j].Severity] {
			return sevRank[fs[i].Severity] < sevRank[fs[j].Severity]
		}
		return fs[i].Port < fs[j].Port
	})
}

var cloudflareCIDRs = mustParseCIDRs([]string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
})

func mustParseCIDRs(list []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(list))
	for _, s := range list {
		_, ipNet, err := net.ParseCIDR(s)
		if err == nil {
			out = append(out, ipNet)
		}
	}
	return out
}

// IsCloudflareIP reports whether ip falls inside Cloudflare's published edge ranges.
func IsCloudflareIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range cloudflareCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// classifySharedCDNHost matches well-known multi-tenant CDN domains/IPs whose
// use as a Reality handshake or unauthenticated fallback turns a node into an
// open relay to other tenants on the same edge.
func classifySharedCDNHost(host string) (kind, label string) {
	h := strings.TrimSuffix(strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]")), ".")
	if h == "" {
		return "", ""
	}
	if ip := net.ParseIP(h); ip != nil {
		if IsCloudflareIP(ip) {
			return "cloudflare", "Cloudflare"
		}
		return "", ""
	}
	matchSuffix := func(domains ...string) bool {
		for _, d := range domains {
			if h == d || strings.HasSuffix(h, "."+d) {
				return true
			}
		}
		return false
	}
	if matchSuffix("cloudflare.com", "cloudflare.net", "cloudflareclient.com", "workers.dev", "pages.dev", "chatgpt.com", "openai.com", "discord.com") {
		return "cloudflare", "Cloudflare"
	}
	if matchSuffix("icloud.com", "apple.com", "akamai.com", "akamai.net", "akamaized.net", "edgekey.net", "edgesuite.net") {
		return "akamai", "Akamai"
	}
	if matchSuffix("fastly.net", "fastly.com", "tesla.com") {
		return "cdn", "多租户"
	}
	return "", ""
}

func realityHasEmptyShortID(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return true
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return strings.TrimSpace(single) == ""
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		if len(list) == 0 {
			return true
		}
		for _, item := range list {
			if strings.TrimSpace(item) == "" {
				return true
			}
		}
		return false
	}
	return false
}

func externalMasqueradeURL(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		str = strings.TrimSpace(str)
		if strings.HasPrefix(str, "http://") || strings.HasPrefix(str, "https://") {
			if u, err := url.Parse(str); err == nil && IsPublicBind(u.Hostname()) {
				return str
			}
		}
		return ""
	}
	var obj map[string]interface{}
	if json.Unmarshal(raw, &obj) == nil {
		if t, _ := obj["type"].(string); t == "proxy" {
			if target, _ := obj["url"].(string); target != "" {
				if u, err := url.Parse(strings.TrimSpace(target)); err == nil && IsPublicBind(u.Hostname()) {
					return strings.TrimSpace(target)
				}
			}
		}
	}
	return ""
}

func externalFallbackTarget(fb, fbAlpn map[string]interface{}) string {
	check := func(m map[string]interface{}) string {
		if m == nil {
			return ""
		}
		if srv, _ := m["server"].(string); strings.TrimSpace(srv) != "" && IsPublicBind(srv) {
			return strings.TrimSpace(srv)
		}
		return ""
	}
	if s := check(fb); s != "" {
		return s
	}
	for _, v := range fbAlpn {
		if sub, ok := v.(map[string]interface{}); ok {
			if s := check(sub); s != "" {
				return s
			}
		}
	}
	return ""
}

// AnalyzeRealityDNS performs a live DNS check on Reality handshake targets that
// were not already flagged as Cloudflare by name, catching custom domains
// proxied behind Cloudflare edge IPs (the exact DMIT C2-fallback abuse vector).
func AnalyzeRealityDNS(ctx context.Context, raw []byte, lookup func(context.Context, string) ([]net.IP, error)) []Finding {
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]net.IP, error) {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			ips := make([]net.IP, 0, len(addrs))
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
			return ips, nil
		}
	}
	var c config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil
	}
	var out []Finding
	for _, ib := range c.Inbounds {
		if ib.ListenPort <= 0 || !IsPublicBind(ib.Listen) {
			continue
		}
		if ib.TLS == nil || !ib.TLS.Enabled || ib.TLS.Reality == nil || !ib.TLS.Reality.Enabled {
			continue
		}
		target := strings.TrimSpace(ib.TLS.Reality.Handshake.Server)
		if target == "" {
			target = strings.TrimSpace(ib.TLS.ServerName)
		}
		if target == "" {
			continue
		}
		if kind, _ := classifySharedCDNHost(target); kind == "cloudflare" {
			continue // already reported as High by AnalyzeConfig
		}
		ips, err := lookup(ctx, target)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			if IsCloudflareIP(ip) {
				name := ib.Tag
				if name == "" {
					name = ib.Type
				}
				out = append(out, Finding{
					Rule:     "reality_cloudflare_ip",
					Severity: High,
					Port:     ib.ListenPort,
					Target:   name,
					Title:    fmt.Sprintf("Reality 握手目标 %s 解析到 Cloudflare IP（%s）", target, ip.String()),
					Detail:   fmt.Sprintf("入站「%s」（端口 %d）的 Reality 回落目标 %s 实际解析到了 Cloudflare 边缘 IP（%s）。未认证的 HTTPS 流量会被回退到 Cloudflare 基础设施，极易触发机房（如 DMIT）的 C2 代理滥用封禁。", name, ib.ListenPort, target, ip.String()),
					Fix:      "立即更换为非 Cloudflare 托管的独立源站作为 Reality 握手目标，或改用自有证书的 VLESS+TLS / Hysteria2。",
				})
				break
			}
		}
	}
	return out
}
