// Package singbox generates a sing-box server config.json from 轻舟's own data
// (B2 integration model: 轻舟 generates the config, sing-box runs as a separate
// process, per-user stats are read back over the v2ray_api gRPC StatsService).
//
// The assembly model: each inbound carries a pre-rendered sing-box JSON body;
// active users are
// injected into its `users[]`; every user name is also listed under
// experimental.v2ray_api.stats.users so the StatsService tracks it per user.
package singbox

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
)

// DefaultBaseConfig is the log/dns/route/outbounds template used when the admin
// hasn't set a custom sb_base_config. Inbounds/experimental are filled by
// GenerateConfig.
//
// No legacy domain_strategy / dns.strategy here: sing-box ≥1.12 deprecated those
// "domain strategy options" and ≥1.14 removes them, making `sing-box check` fail
// fatally. Modern sing-box dials with happy-eyeballs (concurrent A/AAAA), so the
// old prefer_ipv4 hint that avoided AAAA-stall on IPv6-broken VPS is no longer
// needed. GenerateConfig also strips these fields from any custom sb_base_config
// override, so configs stay valid across server versions.
const DefaultBaseConfig = `{
  "log": {"level": "warn", "timestamp": true},
  "dns": {"servers": [{"type": "local", "tag": "local"}]},
  "outbounds": [{"type": "direct", "tag": "direct"}],
  "route": {"rules": [], "final": "direct"}
}`

// User is one 轻舟 user's proxy credentials. A single user can appear in several
// inbounds; each inbound renders only the fields its protocol needs.
type User struct {
	Name     string // identity used in users[] AND as the v2ray stats key
	UUID     string // vless / vmess / tuic
	Password string // hysteria2 / tuic / trojan
}

// Inbound is one sing-box inbound: its protocol Type, the pre-rendered body
// (type/tag/listen_port/tls/transport/...) as Base, and the users entitled to it.
type Inbound struct {
	Type  string
	Base  map[string]interface{}
	Users []User
}

// renderUser maps a 轻舟 user to the protocol-specific user object sing-box
// expects. hasTLS/hasTransport gate the VLESS flow: xtls-rprx-vision is only
// valid on raw-TLS (Reality) VLESS — it must be dropped when there is no TLS or
// when a transport (ws/grpc/...) is present (matches sing-box's stripVision rule).
// The flow preference is read from ib["flow"]: "none" disables it, "vision" or
// empty (default) enables xtls-rprx-vision. The value "vision" (sing-box 1.10+
// new name) is normalized back to "xtls-rprx-vision" for maximum compatibility
// with both the server (which accepts the legacy name) and client software
// (which widely only recognizes the legacy name in subscription links).
func renderUser(t string, u User, ib map[string]interface{}) map[string]interface{} {
	_, hasTLS := ib["tls"]
	hasTransport := false
	if tr, ok := ib["transport"].(map[string]interface{}); ok && len(tr) > 0 {
		hasTransport = true
	}
	switch t {
	case "vless":
		m := map[string]interface{}{"name": u.Name, "uuid": u.UUID}
		if hasTLS && !hasTransport {
			flow, _ := ib["flow"].(string)
			if flow == "" || flow == "vision" {
				flow = "xtls-rprx-vision" // 统一用旧名，兼容客户端和服务端
			}
			if flow != "none" {
				m["flow"] = flow
			}
		}
		return m
	case "vmess":
		return map[string]interface{}{"name": u.Name, "uuid": u.UUID, "alterId": 0}
	case "tuic":
		return map[string]interface{}{"name": u.Name, "uuid": u.UUID, "password": u.Password}
	case "hysteria2":
		return map[string]interface{}{"name": u.Name, "password": u.Password}
	case "trojan":
		return map[string]interface{}{"name": u.Name, "password": u.Password}
	case "shadowsocks":
		method, _ := ib["method"].(string)
		return map[string]interface{}{"name": u.Name, "password": DeriveSSKey(u.Password, method)}
	case "anytls":
		return map[string]interface{}{"name": u.Name, "password": u.Password}
	case "hysteria":
		return map[string]interface{}{"name": u.Name, "auth_str": u.Password}
	case "mixed":
		// mixed inbound (HTTP + SOCKS5) authenticates by username/password. The
		// 轻舟 user identity doubles as the username so the v2ray_api stats key
		// (which lists u.Name) matches the authenticated user for per-user metering.
		return map[string]interface{}{"username": u.Name, "password": u.Password}
	default:
		return map[string]interface{}{"name": u.Name}
	}
}

// SSKeyLen returns the byte length of the per-user key for a shadowsocks 2022
// method (the only family that supports multi-user). 0 for unknown.
func SSKeyLen(method string) int {
	switch method {
	case "2022-blake3-aes-128-gcm":
		return 16
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		return 32
	}
	return 32
}

// DeriveSSKey deterministically derives a valid base64 shadowsocks-2022 key of
// the method's required length from the user's secret, so 轻舟's single
// per-user secret maps cleanly onto SS without storing extra credentials.
func DeriveSSKey(secret, method string) string {
	h := sha256.Sum256([]byte("qz-ss:" + secret))
	return base64.StdEncoding.EncodeToString(h[:SSKeyLen(method)])
}

// Relay is one relay wiring on a server: an extra Outbound that dials a landing
// inbound, plus the tags of local (relay) inbounds whose traffic must be routed
// into it. Used to chain 客户端 → 线路机 → 落地机 → 互联网.
type Relay struct {
	Outbound    map[string]interface{} // sing-box outbound object; must carry "tag"
	InboundTags []string               // local relay inbound tags routed to Outbound
	// AuthUsers narrows the rule to authenticated users on a shared physical
	// inbound. Empty preserves the legacy whole-inbound steering behaviour.
	AuthUsers []string
	// RejectUDP drops UDP from InboundTags instead of handing it to Outbound,
	// for a third-party proxy egress that cannot carry UDP reliably. Note this
	// is a drop from the client's point of view, not a refusal it can react to —
	// see SbEgress.UDPMode, which documents what it does and does not buy.
	RejectUDP bool
}

// GenerateConfig assembles the full sing-box config: it starts from base (the
// log/dns/route/ntp/outbounds template), appends the inbounds with users
// injected, and wires experimental.v2ray_api.stats to track every user.
// v2rayListen is the gRPC listen address (default 127.0.0.1:18080).
func GenerateConfig(base json.RawMessage, inbounds []Inbound, v2rayListen string) ([]byte, error) {
	return GenerateConfigWithRelays(base, inbounds, v2rayListen, nil)
}

// GenerateConfigWithRelays is GenerateConfig plus relay wiring: each Relay's
// Outbound is appended to outbounds[] and a route rule sending its InboundTags
// to that outbound is added, so a relay inbound's traffic exits via the landing
// instead of the default "direct" final.
func GenerateConfigWithRelays(base json.RawMessage, inbounds []Inbound, v2rayListen string, relays []Relay) ([]byte, error) {
	return GenerateConfigWithOptions(base, inbounds, Options{V2RayListen: v2rayListen, Relays: relays})
}

// Options carries the assembly knobs that are not per-inbound.
type Options struct {
	// V2RayListen is the v2ray_api gRPC listen address; "" skips the block
	// entirely (a node whose sing-box lacks the plugin).
	V2RayListen string
	Relays      []Relay
	// BlockPrivate rejects user traffic destined for non-public addresses.
	// See injectPrivateBlock for why this is not merely hardening.
	BlockPrivate bool
}

// GenerateConfigWithOptions is the full assembly entry point.
func GenerateConfigWithOptions(base json.RawMessage, inbounds []Inbound, opt Options) ([]byte, error) {
	v2rayListen, relays := opt.V2RayListen, opt.Relays
	cfg := map[string]interface{}{}
	if len(base) > 0 {
		if err := json.Unmarshal(base, &cfg); err != nil {
			return nil, err
		}
	}

	names := []string{}
	seen := map[string]bool{}
	ibList := []interface{}{}
	// Tags of the inbounds that actually make it into the config. Not the same
	// as the input list: a userless mixed inbound is dropped above, and a route
	// rule naming an inbound the config does not define is at best dead weight.
	emittedTags := []string{}
	for _, ib := range inbounds {
		m := map[string]interface{}{}
		for k, v := range ib.Base {
			m[k] = v
		}
		// An empty transport object (sing-box stores `"transport": {}`) is NOT a real
		// transport: drop it so it doesn't strip the VLESS Reality vision flow and
		// doesn't confuse sing-box at runtime.
		if tr, ok := m["transport"].(map[string]interface{}); ok && len(tr) == 0 {
			delete(m, "transport")
		}
		// Sort users by name so the generated config is byte-deterministic
		// (callers may pass users in nondeterministic DB order); this lets the
		// process manager skip reloads when nothing actually changed.
		su := append([]User(nil), ib.Users...)
		sort.Slice(su, func(i, j int) bool { return su[i].Name < su[j].Name })
		users := []interface{}{}
		for _, u := range su {
			users = append(users, renderUser(ib.Type, u, m))
			if !seen[u.Name] {
				seen[u.Name] = true
				names = append(names, u.Name)
			}
		}
		// A mixed (HTTP/SOCKS5) inbound authenticates via its users[]; sing-box
		// treats an empty users list as "no auth", turning the port into an OPEN
		// public proxy. Never emit a userless mixed inbound — during an entitlement
		// gap (nobody entitled / all expired) it must not listen at all rather than
		// expose an unauthenticated proxy. Circumvention protocols don't need this:
		// their empty users[] just means nobody can connect, which is harmless.
		if ib.Type == "mixed" && len(users) == 0 {
			continue
		}
		m["users"] = users
		// flow 是 per-user 字段，从 inbound 级别移除，否则 sing-box check 会报错
		delete(m, "flow")
		// Hy2 client-only hints live in inbound options so share links / subconv
		// can mirror them, but they are outbound fields — strip before emit.
		delete(m, "disable_chrome_parrot")
		delete(m, "hop_interval")
		delete(m, "hop_interval_max")
		ibList = append(ibList, m)
		if tag, _ := m["tag"].(string); tag != "" {
			emittedTags = append(emittedTags, tag)
		}
	}
	cfg["inbounds"] = ibList
	sort.Strings(names)

	// Must run before the relay rules are appended so the reject sits ahead of
	// every steering rule, including a relay's.
	if opt.BlockPrivate {
		direct, exclude := directResolveScopes(emittedTags, relays)
		injectPrivateBlock(cfg, direct, exclude)
	}

	// Relay wiring: append each landing outbound and a route rule steering the
	// relay inbounds' traffic into it (evaluated before route.final="direct").
	if len(relays) > 0 {
		obs, _ := cfg["outbounds"].([]interface{})
		route, _ := cfg["route"].(map[string]interface{})
		if route == nil {
			route = map[string]interface{}{}
			cfg["route"] = route
		}
		rules, _ := route["rules"].([]interface{})
		// Whether a UDP-blocking egress should also get its DNS rescued — decided
		// once, before any rule is appended, so it reflects the admin's config
		// rather than what this loop has already added. See hijackDNSRule.
		rescueDNS := !hasDNSHijackRule(rules) && hasDNSServer(cfg)
		seenOut := map[string]bool{}
		for _, r := range relays {
			if r.Outbound == nil || len(r.InboundTags) == 0 {
				continue
			}
			tag, _ := r.Outbound["tag"].(string)
			if tag == "" {
				continue
			}
			if !seenOut[tag] {
				seenOut[tag] = true
				obs = append(obs, r.Outbound)
			}
			// Ahead of the steering rule, never after it: route rules are
			// first-match, so behind the steering rule this would never be
			// consulted and the UDP would reach the outbound anyway.
			if r.RejectUDP {
				if rescueDNS {
					rules = append(rules, hijackDNSRule(r.InboundTags))
				}
				rules = append(rules, map[string]interface{}{
					"inbound": r.InboundTags,
					"network": "udp",
					"action":  "reject",
				})
			}
			rule := map[string]interface{}{"inbound": r.InboundTags, "outbound": tag}
			if len(r.AuthUsers) > 0 {
				rule["auth_user"] = r.AuthUsers
			}
			rules = append(rules, rule)
		}
		cfg["outbounds"] = obs
		route["rules"] = rules
	}

	// v2rayListen="" means skip v2ray_api (used for remote servers without the plugin).
	if v2rayListen != "" {
		exp, _ := cfg["experimental"].(map[string]interface{})
		if exp == nil {
			exp = map[string]interface{}{}
		}
		exp["v2ray_api"] = map[string]interface{}{
			"listen": v2rayListen,
			"stats":  map[string]interface{}{"enabled": true, "users": names},
		}
		cfg["experimental"] = exp
	}

	stripLegacyDomainStrategy(cfg)
	return json.MarshalIndent(cfg, "", "  ")
}

// hijackDNSRule answers plain UDP:53 from these inbounds on the node itself,
// instead of letting it reach the outbound.
//
// It exists because the UDP reject that follows it is otherwise a foot-gun. A
// client's DNS is UDP too, so blocking UDP on an egress takes the client's name
// resolution with it — and a client with no DNS reports "no network" for
// everything, which looks nothing like "this egress can't carry UDP" and sends
// the admin looking at the line instead of the rule.
//
// The failure is invisible to whoever flips the switch, because it depends on
// the client: 轻舟's own Clash and sing-box subscriptions configure DoH/DoT
// (TCP), so those users see nothing wrong, while a v2rayN/v2rayNG user — who
// gets the bare link list, no DNS config, and whose client defaults to UDP —
// loses everything. Same panel, same node, same egress; the admin hears "some
// people are fine, some can't open anything."
//
// Scoped to UDP:53 and to these inbounds only. Nothing else about the egress
// changes: real UDP traffic still hits the reject on the next rule, and inbounds
// on a passthrough egress keep resolving through the proxy, which is what that
// setting asked for.
//
// Resolving at the node rather than at the proxy is not a leak: the DNS answer
// only decides which address the node dials, and that connection still leaves
// through the egress. The exit IP the destination sees is unchanged.
func hijackDNSRule(inboundTags []string) map[string]interface{} {
	return map[string]interface{}{
		"inbound": inboundTags,
		"network": "udp",
		"port":    53,
		"action":  "hijack-dns",
	}
}

// hasDNSHijackRule reports whether the config already routes DNS somewhere
// deliberate. An admin who wrote their own handling into sb_base_config keeps
// it: theirs is evaluated first anyway (base rules land ahead of the ones this
// file appends), so adding ours would be dead weight that grows the config on
// every save — and if they routed DNS somewhere on purpose, a second rule is a
// second thing to reason about when it misbehaves.
func hasDNSHijackRule(rules []interface{}) bool {
	for _, it := range rules {
		m, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		if m["action"] == "hijack-dns" {
			return true
		}
		// A rule that names port 53 is the admin steering DNS by hand, whatever
		// it does with it — leave that alone too.
		switch p := m["port"].(type) {
		case int:
			if p == 53 {
				return true
			}
		case float64: // survives a JSON round-trip through sb_base_config
			if p == 53 {
				return true
			}
		}
	}
	return false
}

// hasDNSServer reports whether the config has a DNS server for hijack-dns to
// answer from. Same rule as injectPrivateBlock's resolver check: a node that
// cannot start is worse than one missing this, so when there is nothing to
// point at, the rule is not emitted and the UDP reject stands alone.
func hasDNSServer(cfg map[string]interface{}) bool {
	dns, _ := cfg["dns"].(map[string]interface{})
	if dns == nil {
		return false
	}
	servers, _ := dns["servers"].([]interface{})
	return len(servers) > 0
}

// privateBlockRule is the route rule that keeps proxied traffic on the public
// internet. Tagged so it can be recognised on a re-generate and never stacked.
func privateBlockRule() map[string]interface{} {
	return map[string]interface{}{
		"ip_is_private": true,
		"action":        "reject",
	}
}

// directResolveScopes returns the inbounds whose traffic can leave from THIS
// machine. A shared inbound needs a scoped resolve rule: resolve ordinary users
// here, but exclude auth users whose logical route exits through a landing.
// emittedTags must be the tags actually present in the generated config, not
// the ones asked for: an inbound dropped during assembly (a userless mixed one)
// would otherwise be named by a rule that has nothing to match.
func directResolveScopes(emittedTags []string, relays []Relay) ([]string, map[string][]string) {
	fullySteered := map[string]bool{}
	scoped := map[string]map[string]bool{}
	for _, r := range relays {
		for _, t := range r.InboundTags {
			if len(r.AuthUsers) == 0 {
				fullySteered[t] = true
				continue
			}
			if scoped[t] == nil {
				scoped[t] = map[string]bool{}
			}
			for _, name := range r.AuthUsers {
				scoped[t][name] = true
			}
		}
	}
	var out []string
	exclude := map[string][]string{}
	for _, tag := range emittedTags {
		if fullySteered[tag] {
			continue
		}
		if users := scoped[tag]; len(users) > 0 {
			for name := range users {
				exclude[tag] = append(exclude[tag], name)
			}
			sort.Strings(exclude[tag])
			continue
		}
		out = append(out, tag)
	}
	sort.Strings(out) // deterministic config → no spurious reloads
	return out, exclude
}

// injectPrivateBlock prepends the rules that keep proxied traffic on the public
// internet.
//
// Without them, `route.final: "direct"` means a subscriber can dial anything the
// landing machine can reach: its own loopback (where the panel, the v2ray_api
// gRPC port and any other local-only service listen), the provider's private
// network, and — the sharp edge — 169.254.169.254, the cloud metadata endpoint
// that hands out instance credentials to whoever asks. A proxy is supposed to
// forward users to the internet, not to lend them the host's LAN identity.
//
// TWO rules are needed, not one. `ip_is_private` only ever sees an address, and
// a proxy protocol delivers whatever the client asked for — usually a hostname.
// Verified against sing-box 1.13: with the reject rule alone, dialling
// 127.0.0.1 is refused but dialling "localhost" sails straight through, and
// neither `default_domain_resolver` nor anything else changes that. So an
// attacker only has to point a DNS record at 169.254.169.254 to walk around it.
// The `resolve` action turns the hostname into addresses first, which is what
// gives the reject something to match.
//
// The resolve is scoped to direct-exit inbounds on purpose. Applying it to a
// relay inbound would rewrite the destination to an IP chosen by *this* machine
// and hand that to the landing — so a user relaying through Hong Kong to a US
// landing would be pinned to whichever CDN node is closest to Hong Kong. Nothing
// is lost by the exclusion: the landing machine receives that traffic on one of
// its own direct-exit inbounds and applies these same rules there, which is also
// where the private network actually being protected lives.
//
// Rules are prepended because route rules are first-match: behind any existing
// steering rule they would never be consulted.
func injectPrivateBlock(cfg map[string]interface{}, directTags []string, excludeAuth map[string][]string) {
	route, _ := cfg["route"].(map[string]interface{})
	if route == nil {
		route = map[string]interface{}{}
		cfg["route"] = route
	}
	rules, _ := route["rules"].([]interface{})
	// An admin who wrote their own private-space rule into sb_base_config keeps
	// it; duplicating would be harmless at runtime but makes the generated config
	// grow by one rule on every save.
	for _, it := range rules {
		if m, ok := it.(map[string]interface{}); ok {
			if m["ip_is_private"] == true && m["action"] == "reject" {
				return
			}
		}
	}

	head := []interface{}{privateBlockRule()}
	// The resolve needs a DNS server to resolve *with*. When the base config has
	// no referenceable one, emit the reject alone rather than a config sing-box
	// would refuse: partial protection beats a node that cannot start.
	if resolver := dnsResolverTag(cfg); resolver != "" && (len(directTags) > 0 || len(excludeAuth) > 0) {
		if _, set := route["default_domain_resolver"]; !set {
			route["default_domain_resolver"] = resolver
		}
		head = []interface{}{}
		if len(directTags) > 0 {
			head = append(head, map[string]interface{}{"inbound": directTags, "action": "resolve"})
		}
		var tags []string
		for tag := range excludeAuth {
			tags = append(tags, tag)
		}
		sort.Strings(tags)
		for _, tag := range tags {
			head = append(head, map[string]interface{}{
				"type": "logical", "mode": "and", "action": "resolve",
				"rules": []interface{}{
					map[string]interface{}{"inbound": []string{tag}},
					map[string]interface{}{"auth_user": excludeAuth[tag], "invert": true},
				},
			})
		}
		head = append(head, privateBlockRule())
	}
	route["rules"] = append(head, rules...)
}

// dnsResolverTag picks a DNS server tag usable for resolving destinations, or ""
// when the config has none to point at.
func dnsResolverTag(cfg map[string]interface{}) string {
	dns, _ := cfg["dns"].(map[string]interface{})
	if dns == nil {
		return ""
	}
	servers, _ := dns["servers"].([]interface{})
	var first string
	for _, it := range servers {
		m, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := m["tag"].(string)
		// A fake-ip server would hand every destination a 198.18.x address,
		// turning the reject into a block on everything.
		if tag == "" || m["type"] == "fakeip" {
			continue
		}
		if tag == "local" {
			return tag
		}
		if first == "" {
			first = tag
		}
	}
	return first
}

// stripLegacyDomainStrategy removes the pre-1.12 "domain strategy" knobs that
// sing-box ≥1.12 rejects on `sing-box check` (FATAL unless
// ENABLE_DEPRECATED_LEGACY_DOMAIN_STRATEGY_OPTIONS=true): the global dns.strategy
// and per-inbound/outbound/route-rule domain_strategy. They were optional
// dial/resolve hints; modern sing-box does happy-eyeballs by default, so dropping
// them is safe and keeps the generated config valid whatever a server's version.
// Applied to the fully-assembled config so it covers a custom sb_base_config
// override too (which we may not control).
func stripLegacyDomainStrategy(cfg map[string]interface{}) {
	if dns, ok := cfg["dns"].(map[string]interface{}); ok {
		delete(dns, "strategy")
	}
	for _, key := range []string{"outbounds", "inbounds"} {
		if list, ok := cfg[key].([]interface{}); ok {
			for _, it := range list {
				if m, ok := it.(map[string]interface{}); ok {
					delete(m, "domain_strategy")
				}
			}
		}
	}
	if route, ok := cfg["route"].(map[string]interface{}); ok {
		if rules, ok := route["rules"].([]interface{}); ok {
			for _, it := range rules {
				if m, ok := it.(map[string]interface{}); ok {
					delete(m, "domain_strategy")
				}
			}
		}
	}
}
