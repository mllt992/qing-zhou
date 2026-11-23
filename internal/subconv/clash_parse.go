package subconv

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// looksLikeClash reports whether a subscription blob is a Clash/mihomo YAML
// config rather than a base64 link list. Every Clash node config carries a
// top-level "proxies:" sequence; a base64 blob or link list never does.
func looksLikeClash(blob string) bool {
	var doc clashDoc
	if err := yaml.Unmarshal([]byte(blob), &doc); err != nil {
		return false
	}
	return len(doc.Proxies) > 0
}

type clashDoc struct {
	Proxies []map[string]any `yaml:"proxies"`
}

// ParseClashYAML extracts proxies from a Clash/mihomo YAML config, rebuilding a
// canonical share link for each so the result flows through the same ParseLink
// path as base64 subscriptions. Unsupported proxy types are skipped.
func ParseClashYAML(blob string) []*Proxy {
	var doc clashDoc
	if err := yaml.Unmarshal([]byte(blob), &doc); err != nil {
		return nil
	}
	out := make([]*Proxy, 0, len(doc.Proxies))
	for _, m := range doc.Proxies {
		link := clashToLink(m)
		if link == "" {
			continue
		}
		if p, err := ParseLink(link); err == nil {
			if previous := legacyClashToLink(m); previous != "" && previous != link {
				p.SourceLegacyKeys = NodeKeys(previous)
			}
			out = append(out, p)
		}
	}
	return out
}

// clashToLink converts a single Clash proxy map into a canonical share URI.
// Returns "" for unsupported / malformed entries.
func clashToLink(m map[string]any) string {
	server := str(m["server"])
	port := str(m["port"])
	if server == "" || port == "" {
		return ""
	}
	// Same bracketing rules as singbox.joinHostPort — see the reasoning there.
	// Kept as a local copy rather than shared: it is five lines, and reaching
	// across into the singbox package for it would invert the dependency.
	//
	// The already-bracketed case is the one that matters most on this path:
	// Clash YAML in the wild carries both `server: 2001:db8::1` and
	// `server: "[2001:db8::1]"`, and a naive JoinHostPort turns the second into
	// [[…]] and drops the node.
	addr := joinHostPort(server, port)
	frag := ""
	if name := str(m["name"]); name != "" {
		frag = "#" + url.QueryEscape(name)
	}

	switch str(m["type"]) {
	case "vless":
		q := url.Values{}
		net := strOr(str(m["network"]), "tcp")
		q.Set("type", net)
		sec := ""
		if cbool(m["tls"]) {
			sec = "tls"
		}
		if ro, ok := m["reality-opts"].(map[string]any); ok {
			sec = "reality"
			if v := str(ro["public-key"]); v != "" {
				q.Set("pbk", v)
			}
			if v := str(ro["short-id"]); v != "" {
				q.Set("sid", v)
			}
		}
		if sec != "" {
			q.Set("security", sec)
		}
		if v := str(m["servername"]); v != "" {
			q.Set("sni", v)
		}
		if v := str(m["flow"]); v != "" {
			q.Set("flow", v)
		}
		if v := str(m["client-fingerprint"]); v != "" {
			q.Set("fp", v)
		}
		if net == "ws" && !clashWSToQuery(m, q) {
			return ""
		}
		if v := alpnStr(m["alpn"]); v != "" {
			q.Set("alpn", v)
		}
		if cbool(m["skip-cert-verify"]) {
			q.Set("allowInsecure", "1")
		}
		if q.Get("security") == "tls" {
			if ech := clashECHParam(m); ech != "" {
				q.Set("ech", ech)
			}
		}
		return "vless://" + str(m["uuid"]) + "@" + addr + qstr(q) + frag

	case "vmess":
		j := map[string]any{
			"v":    "2",
			"ps":   str(m["name"]),
			"add":  server,
			"port": port,
			"id":   str(m["uuid"]),
			"aid":  strOr(str(m["alterId"]), "0"),
			"net":  strOr(str(m["network"]), "tcp"),
			"type": "none",
		}
		if cbool(m["tls"]) {
			j["tls"] = "tls"
			if v := str(m["servername"]); v != "" {
				j["sni"] = v
			}
			// The mirror of the render-side gap: vmess stopped at servername here
			// while vless and trojan above already carried skip-cert-verify across.
			// Importing a Clash subscription with a self-signed vmess node dropped
			// the exemption at parse time, so no amount of fixing the renderers
			// could bring it back on re-export.
			if cbool(m["skip-cert-verify"]) {
				j["insecure"] = "1"
				j["allowInsecure"] = "1"
			}
			if v := str(m["client-fingerprint"]); v != "" {
				j["fp"] = v
			}
			if v := alpnStr(m["alpn"]); v != "" {
				j["alpn"] = v
			}
			if ech := clashECHParam(m); ech != "" {
				j["ech"] = ech
			}
		}
		if strOr(str(m["network"]), "tcp") == "ws" {
			q := url.Values{}
			if !clashWSToQuery(m, q) {
				return ""
			}
			for key, values := range q {
				if key == "qz-ws-headers" {
					var headers any
					if json.Unmarshal([]byte(values[0]), &headers) != nil {
						return ""
					}
					j[key] = headers
				} else {
					j[key] = values[0]
				}
			}
		}
		b, _ := json.Marshal(j)
		return "vmess://" + base64.StdEncoding.EncodeToString(b)

	case "ss":
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(str(m["cipher"]) + ":" + str(m["password"])))
		return "ss://" + userinfo + "@" + addr + frag

	case "trojan":
		q := url.Values{}
		q.Set("security", "tls")
		if network := str(m["network"]); network != "" {
			q.Set("type", network)
			if network == "ws" && !clashWSToQuery(m, q) {
				return ""
			}
		}
		if v := alpnStr(m["alpn"]); v != "" {
			q.Set("alpn", v)
		}
		if v := str(m["client-fingerprint"]); v != "" {
			q.Set("fp", v)
		}
		if v := str(m["sni"]); v != "" {
			q.Set("sni", v)
		}
		if cbool(m["skip-cert-verify"]) {
			q.Set("allowInsecure", "1")
		}
		if ech := clashECHParam(m); ech != "" {
			q.Set("ech", ech)
		}
		return "trojan://" + url.User(str(m["password"])).String() + "@" + addr + qstr(q) + frag

	// hysteria (v1) is deliberately NOT folded in here: it is a different wire
	// protocol with different fields (auth-str, up/down), so rendering it as a
	// hysteria2:// link produced "hysteria2://@host:443" — v1 has no `password`
	// key, so the credential came out empty. That node then shipped in every
	// user's subscription and could never connect. Dropping it is the honest
	// outcome; emitting a broken node is worse than emitting none.
	case "hysteria2":
		q := url.Values{}
		if v := str(m["sni"]); v != "" {
			q.Set("sni", v)
		}
		if cbool(m["skip-cert-verify"]) {
			q.Set("insecure", "1")
		}
		if ech := clashECHParam(m); ech != "" {
			q.Set("ech", ech)
		}
		return "hysteria2://" + str(m["password"]) + "@" + addr + qstr(q) + frag

	case "anytls":
		// Only sni and insecure are in the official anytls URI scheme; alpn/fp
		// are 轻舟 extensions that conforming parsers ignore, kept so our own
		// renderers round-trip without loss.
		q := url.Values{}
		if v := str(m["sni"]); v != "" {
			q.Set("sni", v)
		}
		if cbool(m["skip-cert-verify"]) {
			q.Set("insecure", "1")
		}
		if v := str(m["client-fingerprint"]); v != "" {
			q.Set("fp", v)
		}
		if v := alpnStr(m["alpn"]); v != "" {
			q.Set("alpn", v)
		}
		if ech := clashECHParam(m); ech != "" {
			q.Set("ech", ech)
		}
		return "anytls://" + url.QueryEscape(str(m["password"])) + "@" + addr + qstr(q) + frag

	case "hysteria":
		// hysteria v1, NOT hysteria2 — a different wire protocol with auth-str
		// and mandatory bandwidth instead of a password. It used to fall through
		// to the default and be dropped, because the alternative at the time was
		// rendering it as hysteria2:// and shipping a node with an empty
		// credential that could never connect.
		q := url.Values{}
		// No userinfo in this scheme: the credential is the `auth` param.
		if v := strOr(str(m["auth-str"]), str(m["auth_str"])); v != "" {
			q.Set("auth", v)
		}
		if v := str(m["sni"]); v != "" {
			q.Set("peer", v) // v1 spells SNI "peer"
		}
		if v := hysteriaMbps(m["up"], m["up-speed"]); v != "" {
			q.Set("upmbps", v)
		}
		if v := hysteriaMbps(m["down"], m["down-speed"]); v != "" {
			q.Set("downmbps", v)
		}
		if v := str(m["protocol"]); v != "" {
			q.Set("protocol", v)
		}
		// Inverted naming, same trap as the render side: mihomo's obfs is the
		// password (URI obfsParam) and obfs-protocol is the mode (URI obfs).
		if v := str(m["obfs"]); v != "" {
			q.Set("obfsParam", v)
		}
		if v := str(m["obfs-protocol"]); v != "" {
			q.Set("obfs", v)
		}
		if v := alpnStr(m["alpn"]); v != "" {
			q.Set("alpn", v)
		}
		if cbool(m["skip-cert-verify"]) {
			q.Set("insecure", "1")
		}
		if ech := clashECHParam(m); ech != "" {
			q.Set("ech", ech)
		}
		return "hysteria://" + addr + qstr(q) + frag

	case "tuic":
		q := url.Values{}
		if v := str(m["sni"]); v != "" {
			q.Set("sni", v)
		}
		if v := alpnStr(m["alpn"]); v != "" {
			q.Set("alpn", v)
		}
		if v := str(m["congestion-controller"]); v != "" {
			q.Set("congestion_control", v)
		}
		if cbool(m["skip-cert-verify"]) {
			q.Set("allow_insecure", "1")
		}
		if ech := clashECHParam(m); ech != "" {
			q.Set("ech", ech)
		}
		return "tuic://" + str(m["uuid"]) + ":" + str(m["password"]) + "@" + addr + qstr(q) + frag
	}
	return ""
}

func alpnStr(v any) string {
	switch x := v.(type) {
	case []string:
		return strings.Join(x, ",")
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			if s := str(e); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	case string:
		return x
	}
	return ""
}

// joinHostPort mirrors singbox.joinHostPort: bracket an IPv6 literal for a URI
// authority, and tolerate a host that already carries brackets.
func joinHostPort(host, port string) string {
	if len(host) > 1 && host[0] == '[' && host[len(host)-1] == ']' {
		host = host[1 : len(host)-1]
	}
	return net.JoinHostPort(host, port)
}

// hysteriaMbps extracts a plain Mbps number from mihomo's bandwidth fields.
//
// `up`/`down` are strings that may or may not carry a unit ("100", "100 Mbps",
// "1 Gbps"); `up-speed`/`down-speed` are plain integer Mbps. The v1 URI scheme
// only has room for a bare Mbps integer, so a non-Mbps unit cannot be preserved
// — returning "" then lets the renderer fall back to its default rather than
// emitting a number that means something else.
func hysteriaMbps(val, speed any) string {
	if s := strings.TrimSpace(str(val)); s != "" {
		fields := strings.Fields(s)
		if n := atoi(fields[0]); n > 0 {
			if len(fields) == 1 || strings.EqualFold(fields[1], "Mbps") {
				return strconv.Itoa(n)
			}
		}
	}
	if n := atoi(str(speed)); n > 0 {
		return strconv.Itoa(n)
	}
	return ""
}

func cbool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1"
	}
	return false
}

func strOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func qstr(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}
