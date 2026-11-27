package subconv

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func itoaPort(p int) string { return strconv.Itoa(p) }

// Surge renders a Surge 5 managed config. Surge has no VLESS/Reality or TUIC
// support, so those nodes are skipped — for a VLESS-heavy subscription the
// output is intentionally sparse. subURL, if set, becomes the MANAGED-CONFIG
// auto-update header.
func Surge(proxies []*Proxy, subURL string) string {
	return surgeWithProfile(proxies, subURL, ProfileLegacy)
}

// SurgeWithProfile changes only the CN rule. Surge resolves proxied domains at
// the proxy server and DIRECT domains locally, so adding Clash-style split DNS
// would duplicate its resolver model and can race the user's system DNS.
func SurgeWithProfile(proxies []*Proxy, subURL string, profile RoutingProfile) string {
	return surgeWithProfile(proxies, subURL, profile)
}

func surgeWithProfile(proxies []*Proxy, subURL string, profile RoutingProfile) string {
	var kept []*Proxy
	incompatible, withoutEarlyData := 0, 0
	for _, p := range proxies {
		if surgeWSCompatibility(p) != "" {
			incompatible++
			continue
		}
		if surgeProxy(p) != "" {
			if p.transportNetwork() == "ws" {
				if w, err := p.websocket(); err == nil && w.maxEarlyData > 0 {
					withoutEarlyData++
				}
			}
			p.Name = surgeName(p.Name)
			kept = append(kept, p)
		}
	}
	dedupeSurgeNames(kept)

	var b strings.Builder
	if subURL != "" {
		b.WriteString("#!MANAGED-CONFIG " + subURL + " interval=43200 strict=false\n\n")
	}
	if incompatible > 0 {
		fmt.Fprintf(&b, "# Qingzhou: omitted %d nodes with required settings unsupported by this Surge exporter (plaintext Trojan, multi-value WS headers or delimiter-bearing WS fields); use sing-box format.\n", incompatible)
	}
	if withoutEarlyData > 0 {
		fmt.Fprintf(&b, "# Qingzhou: %d WS nodes use normal WebSocket handshakes; optional early data is not supported by this Surge exporter.\n", withoutEarlyData)
	}
	b.WriteString("[Proxy]\n")
	for _, p := range kept {
		b.WriteString(p.Name + " = " + surgeProxy(p) + "\n")
	}

	sg := buildStrategyGroups(kept)
	b.WriteString("\n[Proxy Group]\n")
	if len(sg.all) == 0 {
		b.WriteString(grpSelectClash + " = select, DIRECT\n")
	} else {
		sel := []string{grpFixedClash}
		if len(sg.all) > 1 {
			sel = append(sel, grpFallbackClash)
		}
		sel = append(sel, "DIRECT")
		b.WriteString(grpSelectClash + " = select, " + strings.Join(sel, ", ") + "\n")
		b.WriteString(grpFixedClash + " = select, " + strings.Join(sg.all, ", ") + "\n")
		if len(sg.all) > 1 {
			b.WriteString(grpFallbackClash + " = fallback, " + strings.Join(sg.all, ", ") +
				", url = http://www.gstatic.com/generate_204, interval = 300\n")
		}
		if len(sg.ai) > 0 {
			b.WriteString(grpAIClash + " = fallback, " + strings.Join(sg.aiFallbackOrder(), ", ") +
				", url = http://www.gstatic.com/generate_204, interval = 300\n")
		}
	}

	b.WriteString("\n[Rule]\n")
	if len(sg.ai) > 0 {
		b.WriteString("RULE-SET," + surgeAIRuleURL + "," + grpAIClash + ",update-interval=86400\n")
	}
	b.WriteString("DOMAIN-SET,https://raw.githubusercontent.com/Loyalsoldier/surge-rules/release/reject.txt,REJECT\n")
	// Legacy keeps Surge's historical CN bypass exactly as it was. The explicit
	// cn-direct profile chooses the same behavior; proxy-all omits the rule so
	// FINAL sends every public destination to the selected proxy policy.
	if profile == ProfileLegacy || profile == ProfileCNDirect {
		b.WriteString("GEOIP,CN,DIRECT\n")
	}
	b.WriteString("FINAL," + grpSelectClash + ",dns-failed\n")
	return b.String()
}

// surgeName strips characters Surge treats as delimiters in proxy declarations.
//
// Newlines matter as much as the delimiters: the Surge renderer builds its
// output by hand (the Clash/sing-box ones go through yaml/json.Marshal, which
// escapes for us), and a node name is url-decoded out of the #fragment — so a
// remark carrying %0A would end the proxy line early and let the rest of the
// name become its own directive, e.g. a FINAL rule rewriting the user's routing.
// A leading # or ; would comment the whole line out instead, silently dropping a
// node the proxy groups still reference. Spaced #/;/ // are inline comments,
// even inside a node name; Surge uses a separate safe dedupe suffix.
func surgeName(s string) string {
	s = strings.NewReplacer(",", " ", "=", " ", "\n", " ", "\r", " ").Replace(s)
	s = strings.NewReplacer(" #", " -", " ;", " -", " //", " -", "\t#", " -", "\t;", " -", "\t//", " -").Replace(s)
	s = strings.TrimLeft(strings.TrimSpace(s), "#;/")
	if s = strings.TrimSpace(s); s == "" {
		s = "node"
	}
	return s
}

// surgeUDPRelay renders the udp-relay flag: false for a node whose egress
// drops UDP (see Proxy.udpBlocked), so Surge refuses UDP locally instead of
// relaying it into a server-side black hole.
func surgeUDPRelay(p *Proxy) string {
	if p.udpBlocked() {
		return "udp-relay=false"
	}
	return "udp-relay=true"
}

// surgeProxy returns the right-hand side of a Surge proxy line, or "" if Surge
// can't express this protocol.
func surgeProxy(p *Proxy) string {
	if surgeWSCompatibility(p) != "" {
		return ""
	}
	switch p.Protocol {
	case "ss":
		parts := []string{"ss", p.Server, itoaPort(p.Port),
			"encrypt-method=" + p.Method, "password=" + p.Password, surgeUDPRelay(p)}
		return strings.Join(parts, ", ")
	case "trojan":
		parts := []string{"trojan", p.Server, itoaPort(p.Port), "password=" + p.Password}
		if v := p.param("sni", "peer"); v != "" {
			parts = append(parts, "sni="+v)
		}
		if p.tlsInsecure() {
			parts = append(parts, "skip-cert-verify=true")
		}
		parts = append(parts, surgeWSParams(p)...)
		if alpn := p.tlsParam("alpn"); alpn != "" {
			parts = append(parts, "alpn="+strconv.Quote(alpn))
		}
		parts = append(parts, surgeUDPRelay(p))
		return strings.Join(parts, ", ")
	case "vmess":
		parts := []string{"vmess", p.Server, itoaPort(p.Port), "username=" + p.UUID}
		parts = append(parts, surgeWSParams(p)...)
		if str(p.VMess["tls"]) == "tls" {
			parts = append(parts, "tls=true")
			if alpn := p.tlsParam("alpn"); alpn != "" {
				parts = append(parts, "alpn="+strconv.Quote(alpn))
			}
			if sni := str(p.VMess["sni"]); sni != "" {
				parts = append(parts, "sni="+sni)
			}
			// Matches the trojan/hysteria2 branches; vmess was the only TLS
			// protocol here with no way to accept a self-signed certificate.
			if p.tlsInsecure() {
				parts = append(parts, "skip-cert-verify=true")
			}
		}
		return strings.Join(parts, ", ")
	case "hysteria2":
		// Surge has no realm field. Emitting the placeholder address would
		// publish a node that cannot connect.
		if p.viaRealm() {
			return ""
		}
		parts := []string{"hysteria2", p.Server, itoaPort(p.Port), "password=" + p.Password}
		if v := p.param("sni"); v != "" {
			parts = append(parts, "sni="+v)
		}
		if p.tlsInsecure() {
			parts = append(parts, "skip-cert-verify=true")
		}
		return strings.Join(parts, ", ")
	case "anytls":
		// Surge iOS 5.17.0+ / Mac 6.4.3+. Older builds reject the line.
		parts := []string{"anytls", p.Server, itoaPort(p.Port), "password=" + p.Password}
		if v := p.param("sni"); v != "" {
			parts = append(parts, "sni="+v)
		}
		if p.tlsInsecure() {
			parts = append(parts, "skip-cert-verify=true")
		}
		return strings.Join(parts, ", ")
	default:
		// vless / tuic / hysteria v1 have no Surge equivalent — Surge's proxy
		// policy list covers hysteria2 but not v1.
		return ""
	}
}

// Surge has WS for VMess/Trojan, but no documented V2Ray early-data knobs.
// ED is optional at the sing-box server: absent early bytes use a normal WS
// upgrade. Keep that connection mode; never silently replace WS with TCP.
func surgeWSCompatibility(p *Proxy) string {
	if p.Protocol == "trojan" && p.param("security") == "none" {
		return "plaintext Trojan"
	}
	if p.transportNetwork() != "ws" || (p.Protocol != "vmess" && p.Protocol != "trojan") {
		return ""
	}
	w, err := p.websocket()
	if err != nil {
		return "invalid WS"
	}
	if _, ok := w.singleHeaders(); !ok {
		return "multi-value WS headers"
	}
	for _, value := range []string{w.path, p.Server, p.UUID, p.Password, p.tlsParam("sni", "peer"), p.tlsParam("alpn")} {
		if strings.ContainsAny(value, "\r\n\x00\"") || surgeInlineComment(value) {
			return "unsafe Surge token"
		}
	}
	if strings.ContainsAny(p.Server+p.UUID+p.Password+p.tlsParam("sni", "peer"), ",|") {
		return "unrepresentable credential or TLS token"
	}
	if strings.ContainsAny(w.path, ",|") {
		return "unrepresentable WS path"
	}
	for key, values := range w.headers {
		if strings.ContainsAny(key+values[0], ",|\"\r\n\t") || surgeInlineComment(values[0]) {
			return "unrepresentable WS header"
		}
	}
	return ""
}
func surgeWSParams(p *Proxy) []string {
	if p.transportNetwork() != "ws" {
		return nil
	}
	w, err := p.websocket()
	if err != nil {
		return nil
	}
	parts := []string{"ws=true"}
	if w.path != "" {
		parts = append(parts, "ws-path="+w.path)
	}
	keys := make([]string, 0, len(w.headers))
	for key := range w.headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := []string{}
	for _, key := range keys {
		values = append(values, key+":"+w.headers[key][0])
	}
	if len(values) > 0 {
		parts = append(parts, "ws-headers="+strings.Join(values, "|"))
	}
	return parts
}

func surgeInlineComment(value string) bool {
	for _, marker := range []string{" #", " ;", " //", "\t#", "\t;", "\t//"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
func dedupeSurgeNames(proxies []*Proxy) {
	seen := reservedTags()
	for _, p := range proxies {
		base := surgeName(p.Name)
		name := base
		for n := 2; seen[name]; n++ {
			name = fmt.Sprintf("%s (%d)", base, n)
		}
		p.Name = name
		seen[name] = true
	}
}
