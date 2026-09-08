package subconv

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// Singbox renders a sing-box JSON config: a "proxy" selector over the parsed
// outbounds, a direct outbound, default tun+mixed inbounds, and the anti-leak
// dns/route template merged in. Best-effort for the official sing-box client.
func Singbox(proxies []*Proxy, template string) (string, error) {
	return singboxWithProfile(proxies, template, ProfileLegacy)
}

// SingboxWithProfile renders an explicitly selected routing profile while the
// old Singbox entry point remains the untouched legacy path.
func SingboxWithProfile(proxies []*Proxy, template string, profile RoutingProfile) (string, error) {
	return singboxWithProfile(proxies, template, profile)
}

func singboxWithProfile(proxies []*Proxy, template string, profile RoutingProfile) (string, error) {
	if strings.TrimSpace(template) == "" {
		template = DefaultSingboxTemplate
	}
	doc := map[string]any{}
	_ = json.Unmarshal([]byte(template), &doc)
	// The DNS block may be an admin-stored template written against the legacy
	// address-string format, which sing-box 1.14 removes outright. Rewrite it
	// before anything else touches dns.rules.
	modernizeSingboxDNS(doc)

	// Convert first, then dedupe so policy/selector tags are unique and real.
	customTags := singboxTemplateSelectorTags(doc["outbounds"])
	type conv struct {
		o map[string]any
		p *Proxy
	}
	var cs []conv
	for _, p := range proxies {
		if o := singboxOutbound(p); o != nil {
			cs = append(cs, conv{o: o, p: p})
		}
	}
	kept := make([]*Proxy, len(cs))
	for i, c := range cs {
		kept[i] = c.p
	}
	dedupeNamesWithReserved(kept, customTags)
	outs := make([]map[string]any, len(cs))
	for i, c := range cs {
		c.o["tag"] = c.p.Name // sync after dedupe
		outs[i] = c.o
	}
	sg := buildStrategyGroups(kept)

	// sing-box currently has no ordered fallback outbound. Keep the generated
	// fallback selector manual and ordered instead of silently replacing it with
	// latency-based urltest, which would switch nodes without user intent.
	sel := []string{}
	if len(sg.all) > 0 {
		sel = append(sel, tagFixed)
	}
	if len(sg.all) > 1 {
		sel = append(sel, tagFallback)
	}
	sel = append(sel, "direct")

	generatedTags := map[string]bool{
		tagProxy: true, tagFixed: true, tagFallback: true,
		"direct": true, allPlaceholder: true,
	}
	if len(sg.ai) > 0 {
		generatedTags[tagAI] = true
	}
	for _, n := range sg.all {
		generatedTags[n] = true
	}
	customSelectors := mergeSingboxSelectors(doc["outbounds"], sg.all, generatedTags)

	all := []map[string]any{{"type": "selector", "tag": tagProxy, "outbounds": sel}}
	// Keep optional platform selectors near the top and give them the same
	// ordering semantics as Clash: primary, custom, policies, nodes.
	all = append(all, customSelectors...)
	if len(sg.all) > 0 {
		all = append(all, map[string]any{"type": "selector", "tag": tagFixed, "outbounds": sg.all})
	}
	if len(sg.all) > 1 {
		all = append(all, map[string]any{"type": "selector", "tag": tagFallback, "outbounds": sg.all})
	}
	if len(sg.ai) > 0 {
		ai := map[string]any{"type": "selector", "tag": tagAI, "outbounds": sg.ai}
		if len(sg.ai) > 1 {
			ai = map[string]any{
				"type": "urltest", "tag": tagAI, "outbounds": sg.ai,
				"url": "https://www.gstatic.com/generate_204", "interval": "3m", "tolerance": 50,
			}
		}
		all = append(all, ai)
	}
	all = append(all, outs...)
	all = append(all, map[string]any{"type": "direct", "tag": "direct"})
	doc["outbounds"] = all
	if profile != ProfileLegacy {
		applySingboxRoutingProfile(doc, profile)
	}
	if len(sg.ai) > 0 {
		injectSingboxAIRoute(doc)
	}
	// Templates are normalized before generated outbounds exist. Perform this
	// compatibility cleanup only now, against the final direct outbound, so old
	// admin-stored templates without an outbounds array are fixed too.
	if dns, ok := doc["dns"].(map[string]any); ok {
		stripEmptyDirectDetour(doc, mapSlice(dns["servers"]))
	}

	if _, ok := doc["inbounds"]; !ok {
		doc["inbounds"] = []map[string]any{
			// The IPv6 prefix is not decorative: the DNS template answers AAAA with
			// a fake address out of fakeip.inet6_range (fc00::/18). Without an
			// inet6 address on the TUN, auto_route installs no IPv6 route, so that
			// fake address is unroutable — every AAAA-resolved connection dies with
			// ENETUNREACH, and an IPv6-only domain (no A record) is simply
			// unreachable. With it, IPv6 traffic enters the tunnel and the *node*
			// resolves the domain, so a v4-only node still serves it over v4.
			//
			// Capturing v6 also closes the leak: with no IPv6 route, anything that
			// reaches an IPv6 literal without asking DNS — BitTorrent, WebRTC/STUN,
			// a hardcoded address — bypasses the tunnel out the physical NIC.
			{"type": "tun", "tag": "tun-in",
				"address":    []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
				"auto_route": true, "strict_route": true, "stack": "gvisor",
				// Defensive, not load-bearing: auto_route installs ::/0, which
				// nominally covers link-local and multicast, but the kernel's own
				// more-specific on-link routes (fe80::/64 dev <if>) win in the main
				// table either way — so neighbor discovery and mDNS survive with or
				// without this. Listed explicitly because it costs nothing and makes
				// the intent legible. (The Clash template omits the equivalent for
				// the same reason it is safe to omit here.)
				//
				// fc00::/7 must NOT be listed, however. That one is load-bearing:
				// fakeip hands out fc00::/18 from inside it, so excluding the parent
				// prefix would route every fake IPv6 straight back out of the tunnel,
				// reintroducing the exact breakage the inet6 address above fixes.
				"route_exclude_address": []string{"fe80::/10", "ff00::/8"},
			},
			{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080},
		}
	}

	// Inject proxy server IPs into tun route_exclude_address to prevent routing loop.
	injectSingboxTunExclude(doc, proxies)
	// Domain-based node servers: keep them out of fake-ip and off the proxy
	// detour, mirroring the Clash injectNodeDomains fix (otherwise "TUN on → no
	// network" for any node whose server is a domain).
	injectSingboxDomains(doc, proxies)

	b, err := json.MarshalIndent(doc, "", "  ")
	return string(b), err
}

const (
	singboxCNGeositeURL = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs"
	singboxCNGeoIPURL   = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs"
)

// applySingboxRoutingProfile owns only the two qingzhou CN route-set tags. The
// built-in DNS already has a direct `local` resolver and a proxy-detoured
// `remote` resolver. With fake-IP, keeping the query on `fake` preserves the
// original domain for routing; once geosite-cn selects DIRECT, sing-box resolves
// the real destination through route.default_domain_resolver (`local`). Returning
// a real CN address from the initial DNS query would throw that domain mapping
// away and make routing depend unnecessarily on GeoIP accuracy.
func applySingboxRoutingProfile(doc map[string]any, profile RoutingProfile) {
	route, _ := doc["route"].(map[string]any)
	if route == nil {
		route = map[string]any{}
		doc["route"] = route
	}

	rules := mapSlice(route["rules"])
	kept := make([]map[string]any, 0, len(rules)+2)
	for _, rule := range rules {
		if isManagedSingboxCNRule(rule) {
			continue
		}
		kept = append(kept, rule)
	}
	if profile == ProfileCNDirect {
		kept = append(kept,
			map[string]any{"rule_set": "geosite-cn", "outbound": "direct"},
			map[string]any{"rule_set": "geoip-cn", "outbound": "direct"},
		)
		ensureSingboxLocalResolver(doc, route)
		ensureSingboxCNRuleSets(route)
	}
	route["rules"] = kept
}

func isManagedSingboxCNRule(rule map[string]any) bool {
	if len(rule) != 2 {
		return false
	}
	if _, ok := rule["outbound"]; !ok {
		return false
	}
	tags := stringSlice(rule["rule_set"])
	if tag, ok := rule["rule_set"].(string); ok {
		tags = []string{tag}
	}
	if len(tags) == 0 {
		return false
	}
	for _, tag := range tags {
		if tag != "geosite-cn" && tag != "geoip-cn" {
			return false
		}
	}
	return true
}

func ensureSingboxLocalResolver(doc, route map[string]any) {
	dns, _ := doc["dns"].(map[string]any)
	if dns == nil {
		dns = map[string]any{}
		doc["dns"] = dns
	}
	servers := mapSlice(dns["servers"])
	for _, server := range servers {
		if server["tag"] == "local" {
			route["default_domain_resolver"] = "local"
			return
		}
	}
	servers = append(servers, map[string]any{
		"tag": "local", "type": "https", "server": "223.5.5.5",
	})
	dns["servers"] = servers
	route["default_domain_resolver"] = "local"
}

func ensureSingboxCNRuleSets(route map[string]any) {
	ruleSets := mapSlice(route["rule_set"])
	kept := make([]map[string]any, 0, len(ruleSets)+2)
	for _, ruleSet := range ruleSets {
		tag, _ := ruleSet["tag"].(string)
		if tag != "geosite-cn" && tag != "geoip-cn" {
			kept = append(kept, ruleSet)
		}
	}
	kept = append(kept,
		map[string]any{
			"type": "remote", "tag": "geosite-cn", "format": "binary",
			"download_detour": tagProxy, "url": singboxCNGeositeURL,
		},
		map[string]any{
			"type": "remote", "tag": "geoip-cn", "format": "binary",
			"download_detour": tagProxy, "url": singboxCNGeoIPURL,
		},
	)
	route["rule_set"] = kept
}

// singboxTemplateSelectorTags returns stable custom tags that generated nodes
// and policy groups must not claim. Built-in tags and the placeholder are not
// reservable by templates; those selectors are ignored later.
func singboxTemplateSelectorTags(tpl any) map[string]bool {
	tags := map[string]bool{}
	reserved := map[string]bool{
		tagProxy: true, tagFixed: true, tagFallback: true,
		"direct": true, allPlaceholder: true,
	}
	for _, outbound := range mapSlice(tpl) {
		typ, _ := outbound["type"].(string)
		tag, _ := outbound["tag"].(string)
		if typ == "selector" && tag != "" && !reserved[tag] {
			tags[tag] = true
		}
	}
	return tags
}

// mergeSingboxSelectors preserves extra selector outbounds from an admin
// template. The renderer owns all other outbounds because node definitions are
// generated per subscriber; retaining arbitrary template outbounds would make
// collisions and dangling dependencies impossible to validate safely.
//
// "all" expands in place to every generated node tag. References are restricted
// to generated outbounds so a typo or a custom-selector cycle cannot make
// sing-box reject or loop the complete subscription. An empty selector falls
// back to proxy, which also gives optional platform groups the intended
// inherit-main behavior. A dangling `default` is removed so sing-box uses the
// first valid outbound as documented.
func mergeSingboxSelectors(tpl any, nodeTags []string, generatedTags map[string]bool) []map[string]any {
	candidates := mapSlice(tpl)
	accepted := make([]map[string]any, 0, len(candidates))
	taken := make(map[string]bool, len(generatedTags)+len(candidates))
	for tag := range generatedTags {
		taken[tag] = true
	}
	for _, outbound := range candidates {
		typ, _ := outbound["type"].(string)
		tag, _ := outbound["tag"].(string)
		if typ != "selector" || tag == "" || taken[tag] {
			continue
		}
		taken[tag] = true
		accepted = append(accepted, outbound)
	}

	for _, selector := range accepted {
		self, _ := selector["tag"].(string)
		raw := stringSlice(selector["outbounds"])
		refs := make([]string, 0, len(raw)+len(nodeTags))
		seen := map[string]bool{}
		appendRef := func(ref string) {
			if ref != "" && ref != self && generatedTags[ref] && !seen[ref] {
				seen[ref] = true
				refs = append(refs, ref)
			}
		}
		for _, ref := range raw {
			if ref == "auto" {
				ref = tagFallback // migrate stored selectors from the old built-in tag
			}
			if ref == allPlaceholder {
				for _, tag := range nodeTags {
					appendRef(tag)
				}
				continue
			}
			appendRef(ref)
		}
		if len(refs) == 0 {
			refs = []string{tagProxy}
			seen[tagProxy] = true
		}
		selector["outbounds"] = refs
		if def, ok := selector["default"].(string); !ok || !seen[def] {
			delete(selector, "default")
		}
	}
	return accepted
}

func stringSlice(v any) []string {
	switch values := v.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if s, ok := value.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// mapSlice normalizes a JSON array value to []map[string]any. A template loaded
// via json.Unmarshal yields []any (of map[string]any); the Go-built default
// yields []map[string]any. Element maps are shared, so mutating them in place
// affects the underlying document either way.
func mapSlice(v any) []map[string]any {
	switch s := v.(type) {
	case []map[string]any:
		return s
	case []any:
		out := make([]map[string]any, 0, len(s))
		for _, e := range s {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// prependRule inserts rule at the front of a dns/route rules array, preserving
// the array's concrete element type (which differs between a Go-built default
// and a json.Unmarshal'd custom template).
func prependRule(v any, rule map[string]any) any {
	switch s := v.(type) {
	case []map[string]any:
		return append([]map[string]any{rule}, s...)
	case []any:
		return append([]any{any(rule)}, s...)
	case nil:
		return []map[string]any{rule}
	default:
		return v
	}
}

// injectSingboxAIRoute keeps sniff/DNS actions first, then inserts the guarded
// domain route before any geographic direct rule from an admin template.
func injectSingboxAIRoute(doc map[string]any) {
	route, ok := doc["route"].(map[string]any)
	if !ok {
		route = map[string]any{}
		doc["route"] = route
	}
	rules := mapSlice(route["rules"])
	insertAt := 0
	for insertAt < len(rules) {
		action, _ := rules[insertAt]["action"].(string)
		if action != "sniff" && action != "hijack-dns" {
			break
		}
		insertAt++
	}
	aiRule := map[string]any{"rule_set": "qingzhou-ai", "outbound": tagAI}
	merged := make([]map[string]any, 0, len(rules)+1)
	merged = append(merged, rules[:insertAt]...)
	merged = append(merged, aiRule)
	merged = append(merged, rules[insertAt:]...)
	route["rules"] = merged

	ruleSets := mapSlice(route["rule_set"])
	kept := make([]map[string]any, 0, len(ruleSets)+1)
	for _, ruleSet := range ruleSets {
		if tag, _ := ruleSet["tag"].(string); tag != "qingzhou-ai" {
			kept = append(kept, ruleSet)
		}
	}
	kept = append(kept, map[string]any{
		"type": "remote", "tag": "qingzhou-ai", "format": "binary",
		"download_detour": tagProxy, "url": singboxAIRuleURL,
	})
	route["rule_set"] = kept
}

// injectSingboxDomains makes the client resolve each domain-based node server via
// the direct "local" DNS (never fake-ip or the proxy-detoured "remote" server)
// and route the connection to that server directly. Without this, resolving a
// foreign node domain either yields a 198.18.x fake-ip dialed into the node's own
// TUN, or deadlocks bootstrapping DNS through the very proxy being established.
func injectSingboxDomains(doc map[string]any, proxies []*Proxy) {
	seen := map[string]bool{}
	for _, p := range proxies {
		if p.Server != "" && net.ParseIP(p.Server) == nil {
			seen[p.Server] = true
		}
	}
	if len(seen) == 0 {
		return
	}
	domains := sortedKeys(seen)
	if dns, ok := doc["dns"].(map[string]any); ok {
		dns["rules"] = prependRule(dns["rules"], map[string]any{"domain": domains, "server": "local"})
	}
	if route, ok := doc["route"].(map[string]any); ok {
		route["rules"] = prependRule(route["rules"], map[string]any{"domain": domains, "outbound": "direct"})
	}
}

// SingboxOutboundFromLink parses a share link and renders it as a sing-box
// outbound (dial-side) object. Used to build relay upstream outbounds that dial
// a landing inbound. Returns an error if the link is unparseable or its protocol
// has no outbound renderer.
func SingboxOutboundFromLink(link string) (map[string]any, error) {
	p, err := ParseLink(link)
	if err != nil {
		return nil, err
	}
	o := singboxOutbound(p)
	if o == nil {
		return nil, fmt.Errorf("subconv: protocol %q has no sing-box outbound renderer", p.Protocol)
	}
	return o, nil
}

func singboxOutbound(p *Proxy) map[string]any {
	o := map[string]any{"tag": p.Name, "server": p.Server, "server_port": p.Port}
	switch p.Protocol {
	case "vless":
		o["type"] = "vless"
		o["uuid"] = p.UUID
		if v := p.param("flow"); v != "" {
			o["flow"] = v
		}
		if t := sbTransport(p); t != nil {
			o["transport"] = t
		}
		// UDP over VLESS requires an agreed packet encoding. Leaving it unset
		// makes each client fall back to its own default, which breaks UDP
		// (and so QUIC downloads) while leaving TCP intact.
		//
		// Whitelisted, not passed through: these params come from links an admin
		// pasted or a remote subscription served, and sing-box rejects an
		// unknown packet_encoding by failing to load the *whole* config — one
		// bad node would take down every subscriber's profile.
		switch p.param("packetEncoding", "packet_encoding") {
		case "packetaddr":
			o["packet_encoding"] = "packetaddr"
		default:
			o["packet_encoding"] = "xudp"
		}
		o["tls"] = sbTLS(p, p.param("security"))
	case "vmess":
		o["type"] = "vmess"
		o["uuid"] = p.UUID
		o["alter_id"] = p.AlterID
		o["security"] = "auto"
		if str(p.VMess["net"]) == "ws" {
			o["transport"] = sbWS(str(p.VMess["path"]), str(p.VMess["host"]), 0, "")
		}
		// Was a hand-rolled block that set only server_name, so alpn / utls
		// fingerprint / insecure were dropped for vmess alone. sbTLS reads all of
		// them through p.param, which falls through to the vmess JSON map, so
		// vmess now gets the same TLS treatment as every other protocol.
		if str(p.VMess["tls"]) == "tls" {
			o["tls"] = sbTLS(p, "tls")
		}
	case "ss":
		o["type"] = "shadowsocks"
		o["method"] = p.Method
		o["password"] = p.Password
	case "trojan":
		o["type"] = "trojan"
		o["password"] = p.Password
		// trojan carries the same transports as vless; it had none at all here,
		// so a ws/grpc trojan node was dialled as plain TCP and never connected.
		if t := sbTransport(p); t != nil {
			o["transport"] = t
		}
		o["tls"] = sbTLS(p, "tls")
	case "hysteria2":
		o["type"] = "hysteria2"
		o["password"] = p.Password
		o["tls"] = sbTLS(p, "tls")
		if v := p.param("obfs"); v != "" {
			obfs := map[string]any{"type": v}
			if pw := p.param("obfs-password"); pw != "" {
				obfs["password"] = pw
			}
			if n := atoi(p.param("obfs-min-packet")); n > 0 {
				obfs["min_packet_size"] = n
			}
			if n := atoi(p.param("obfs-max-packet")); n > 0 {
				obfs["max_packet_size"] = n
			}
			o["obfs"] = obfs
		}
		if v := p.param("bbr-profile"); v != "" {
			o["bbr_profile"] = v
		}
		// Client Chrome QUIC parrot is on by default in sing-box 1.14; only
		// emit the opt-out when the share link asks for it.
		if p.param("disable-chrome-parrot") == "1" {
			o["disable_chrome_parrot"] = true
		}
		if v := p.param("hop-interval"); v != "" {
			o["hop_interval"] = v
		}
		if v := p.param("hop-interval-max"); v != "" {
			o["hop_interval_max"] = v
		}
	case "anytls":
		// sing-box >= 1.12.0. tls is required by the outbound constructor.
		o["type"] = "anytls"
		o["password"] = p.Password
		o["tls"] = sbTLS(p, "tls")
	case "hysteria":
		o["type"] = "hysteria"
		if v := p.param("auth"); v != "" {
			o["auth_str"] = v // plaintext form; `auth` is the base64 variant
		}
		if v := atoi(p.param("upmbps")); v > 0 {
			o["up_mbps"] = v
		}
		if v := atoi(p.param("downmbps")); v > 0 {
			o["down_mbps"] = v
		}
		// XPlus obfuscation password — the URI's obfsParam, NOT its `obfs`
		// (which is the mode). sing-box has no field for the mode.
		if v := p.param("obfsParam"); v != "" {
			o["obfs"] = v
		}
		// Mandatory: NewOutbound rejects the node outright with ErrTLSRequired
		// when tls is absent or disabled, and that failure happens at startup,
		// taking the whole config with it.
		o["tls"] = sbTLS(p, "tls")
		// recv_window_conn / recv_window / disable_mtu_discovery are deliberately
		// not emitted: deprecated in 1.14 in favour of the shared QUIC options,
		// and the minimal field set is what both old and new cores accept.
	case "tuic":
		o["type"] = "tuic"
		o["uuid"] = p.UUID
		o["password"] = p.Password
		if v := p.param("congestion_control", "congestion-controller"); v != "" {
			o["congestion_control"] = v
		}
		if p.param("zero_rtt", "reduce_rtt") == "1" {
			o["zero_rtt_handshake"] = true
		}
		// Carried across so both ends agree on how UDP is relayed; a mismatch
		// breaks UDP only, leaving TCP healthy and the cause invisible. Absent
		// from the link (an imported external node), sing-box's own default
		// applies — we do not invent a value the source did not state.
		if v := p.param("udp_relay_mode", "udp-relay-mode"); v != "" {
			o["udp_relay_mode"] = v
		}
		o["tls"] = sbTLS(p, "tls")
	default:
		return nil
	}
	// TCP/multiplex dial tuning (vless/vmess/trojan). multiplex is incompatible
	// with vless xtls-rprx-vision, so drop it when a flow is set.
	switch p.Protocol {
	case "vless", "vmess", "trojan":
		t := p.tuning()
		_, hasFlow := o["flow"]
		sbApplyTuning(o, t, hasFlow)
	}
	// A node that drops UDP at the server (egress udp_mode=block) is rendered
	// TCP-only, so sing-box refuses UDP connections locally — instantly — and
	// applications take their TCP fallback instead of timing out against the
	// server-side black hole.
	//
	// Whitelisted per protocol, same discipline as packet_encoding above:
	// sing-box rejects an unknown field by refusing the whole config, so one
	// wrong emission would take down every subscriber's profile. `network` is a
	// documented option on the V2Ray family and the QUIC-transport protocols
	// (their `network` restricts what they RELAY, not how they dial), but NOT
	// on anytls — an anytls node keeps its unrestricted outbound and relies on
	// the node-side reject alone (its clash/surge outputs do carry the flag).
	if p.udpBlocked() {
		switch p.Protocol {
		case "vless", "vmess", "trojan", "ss", "hysteria", "hysteria2", "tuic":
			o["network"] = "tcp"
		}
	}
	return o
}

// sbApplyTuning sets sing-box's tcp_fast_open/tcp_multi_path/multiplex outbound
// options from t. mux is suppressed when suppressMux (a vless vision flow).
func sbApplyTuning(o map[string]any, t tuning, suppressMux bool) {
	if t.tfo {
		o["tcp_fast_open"] = true
	}
	if t.mptcp {
		o["tcp_multi_path"] = true
	}
	if t.mux && !suppressMux {
		mx := map[string]any{"enabled": true}
		if t.brutalUp > 0 && t.brutalDown > 0 {
			mx["brutal"] = map[string]any{"enabled": true, "up_mbps": t.brutalUp, "down_mbps": t.brutalDown}
		}
		o["multiplex"] = mx
	}
}

func sbTLS(p *Proxy, sec string) map[string]any {
	if sec != "tls" && sec != "reality" {
		return map[string]any{"enabled": false}
	}
	// tlsParam, not param: these keys must also resolve for vmess, whose link
	// carries them in its JSON payload rather than a query string.
	tls := map[string]any{"enabled": true}
	if sni := p.tlsParam("sni"); sni != "" {
		tls["server_name"] = sni
	}
	if alpn := p.tlsParam("alpn"); alpn != "" {
		tls["alpn"] = strings.Split(alpn, ",")
	}
	if fp := p.tlsParam("fp"); fp != "" {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	if sec == "reality" {
		re := map[string]any{"enabled": true}
		if v := p.param("pbk"); v != "" {
			re["public_key"] = v
		}
		if v := p.param("sid"); v != "" {
			re["short_id"] = v
		}
		tls["reality"] = re
	}
	if p.tlsInsecure() {
		tls["insecure"] = true
	}
	return tls
}

// sbTransport renders the transport block for a URL-style proxy, or nil for
// plain TCP.
//
// Only "ws" used to be handled, so a grpc / httpupgrade / h2 node was emitted
// with no transport at all — the client then dialled plain TCP at a server
// speaking gRPC and the node simply never connected, with nothing in the config
// to suggest why. An unrecognised value also yields nil rather than being passed
// through: sing-box rejects an unknown transport type by refusing the whole
// config, which would take down every node in the subscription, not just this one.
func sbTransport(p *Proxy) map[string]any {
	switch p.param("type") {
	case "ws":
		return sbWS(p.param("path"), p.param("host"),
			atoi(p.param("max_early_data")), p.param("early_data_header_name"))
	case "grpc":
		t := map[string]any{"type": "grpc"}
		if v := p.param("serviceName", "servicename"); v != "" {
			t["service_name"] = v
		}
		return t
	case "httpupgrade":
		t := map[string]any{"type": "httpupgrade"}
		if v := p.param("path"); v != "" {
			t["path"] = v
		}
		if v := p.param("host"); v != "" {
			t["host"] = v
		}
		return t
	case "h2", "http":
		t := map[string]any{"type": "http"}
		if v := p.param("path"); v != "" {
			t["path"] = v
		}
		if v := p.param("host"); v != "" {
			t["host"] = []string{v}
		}
		return t
	}
	return nil
}

func sbWS(path, host string, maxEarlyData int, edHeader string) map[string]any {
	t := map[string]any{"type": "ws"}
	if path != "" {
		t["path"] = path
	}
	if host != "" {
		t["headers"] = map[string]any{"Host": host}
	}
	// ws 0-RTT early data (must mirror the inbound).
	if maxEarlyData > 0 {
		t["max_early_data"] = maxEarlyData
		if edHeader != "" {
			t["early_data_header_name"] = edHeader
		}
	}
	return t
}

// injectSingboxTunExclude adds proxy server IPs to the TUN inbound's
// route_exclude_address to prevent routing loops on the client.
//
// The key is route_exclude_address, NOT exclude_address: sing-box has no field
// by the latter name, and it rejects unknown config fields fatally — so every
// sing-box-format subscription failed to load outright with
// "inbounds[0].exclude_address: json: unknown field". Verified against
// sing-box 1.13's option/tun.go, which declares route_exclude_address /
// route_exclude_address_set (plus the inet4_/inet6_ variants).
func injectSingboxTunExclude(doc map[string]any, proxies []*Proxy) {
	seen := map[string]bool{}
	for _, p := range proxies {
		// route_exclude_address entries must be CIDR prefixes; only real public IPs
		// qualify. Bare domains (net.ParseIP == nil) would be invalid and break
		// the client config, so they are skipped here (see injectRouteExclude).
		if isPublicIP(p.Server) {
			seen[p.Server] = true
		}
	}
	if len(seen) == 0 {
		return
	}
	// Normalize the inbounds array — a custom template decodes as []any, not
	// []map[string]any, so a naked type assertion would silently skip the
	// exclusion and reintroduce the TUN routing loop.
	inbounds := mapSlice(doc["inbounds"])
	if inbounds == nil {
		return
	}
	for _, ib := range inbounds {
		if ib["type"] != "tun" {
			continue
		}
		// Same []any-not-[]string trap as the inbounds array above: a template
		// arrives via json.Unmarshal, so a naked []string assertion always fails
		// and the admin's own exclusions get overwritten with just the node IPs.
		existing, present := stringList(ib["route_exclude_address"])
		for _, ip := range sortedKeys(seen) {
			cidr := ensureCIDR(ip)
			if !present[cidr] {
				existing = append(existing, cidr)
				present[cidr] = true
			}
		}
		ib["route_exclude_address"] = existing
	}
}
