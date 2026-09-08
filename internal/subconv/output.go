package subconv

import (
	"encoding/base64"
	"strings"
)

// Format constants.
const (
	FormatBase64  = "base64"
	FormatClash   = "clash"
	FormatSingbox = "singbox"
	FormatSurge   = "surge"
)

// NormalizeFormat maps user-facing format names to canonical ones.
//
// Anything not named below yields the base64 link list, so "base64", "v2ray",
// "v2rayn" and friends all resolve to it. They are documented here rather than
// as explicit cases because a case list returning the same value as the default
// would be dead code that documents nothing the code actually enforces — a typo
// like "?format=v2rya" behaves identically either way. Falling back rather than
// erroring is deliberate: a subscription that 404s on a typo is worse than one
// that returns the format every client can read.
func NormalizeFormat(f string) string {
	switch strings.ToLower(f) {
	case "clash", "mihomo", "meta":
		return FormatClash
	case "singbox", "sing-box", "json":
		return FormatSingbox
	case "surge":
		return FormatSurge
	default:
		return FormatBase64
	}
}

// singboxAppUAs are the official sing-box GUI clients, which do NOT put
// "sing-box" in their User-Agent — SFA (Android), SFI (iOS), SFM (macOS) and
// SFT (tvOS) all identify by initials, e.g. "SFI/1.11.0 (io.nekohasekai.sfa)".
//
// Matching only the string "sing-box" therefore missed the entire first-party
// client family: they were served the base64 link list, so they got working
// nodes but none of the config that makes the sing-box format worth choosing —
// no fake-ip DNS, no CN/ads rule-sets, and no generated policy groups. Since the panel
// advertises UA-based format selection, that read as the feature not working.
//
// Matched as a prefix, not a substring: "SFI" is three letters and would
// otherwise collide with unrelated agents.
var singboxAppUAs = []string{"sfa/", "sfi/", "sfm/", "sft/"}

// FormatForUA picks a sensible subscription format from a client's User-Agent,
// so users importing into Clash/sing-box/Surge get a native config without
// having to append ?format=. Anything unrecognised (v2rayN, Shadowrocket,
// Quantumult, …) falls back to the universal base64 link list.
//
// Clash is tested first on purpose: NekoBox states its preference in its own
// UA ("NekoBox/Android/1.3.6 (Prefer ClashMeta Format)"), and that preference
// has to win over the fact that NekoBox is itself sing-box based.
func FormatForUA(ua string) string {
	s := strings.ToLower(ua)
	switch {
	case strings.Contains(s, "clash") || strings.Contains(s, "mihomo") || strings.Contains(s, "stash"):
		return FormatClash
	case strings.Contains(s, "sing-box") || strings.Contains(s, "singbox"):
		return FormatSingbox
	case strings.Contains(s, "surge"):
		return FormatSurge
	}
	for _, p := range singboxAppUAs {
		if strings.HasPrefix(s, p) {
			return FormatSingbox
		}
	}
	return FormatBase64
}

// Base64 joins raw share links and base64-encodes them (the universal format).
func Base64(links []string) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))
}

// Render produces a subscription body for the given format. clashTpl/singboxTpl
// may be empty to use the built-in anti-leak templates. aiNodes marks links
// belonging to at least one accessible admin-marked AI group; it may be nil.
// subURL, if set, is the subscription's own URL (used by Surge's MANAGED-CONFIG
// auto-update header).
func Render(format string, links []string, aiNodes map[string]bool, clashTpl, singboxTpl, subURL string) (body string, contentType string, err error) {
	switch NormalizeFormat(format) {
	case FormatClash:
		out, e := Clash(parseWithAI(links, aiNodes), clashTpl)
		return out, "text/yaml; charset=utf-8", e
	case FormatSingbox:
		out, e := Singbox(parseWithAI(links, aiNodes), singboxTpl)
		return out, "application/json; charset=utf-8", e
	case FormatSurge:
		return Surge(parseWithAI(links, aiNodes), subURL), "text/plain; charset=utf-8", nil
	default:
		return Base64(links), "text/plain; charset=utf-8", nil
	}
}

// RenderWithProfile renders a newly selected routing profile. A legacy (empty)
// profile delegates to Render so existing subscription URLs keep their exact
// pre-profile behavior, including Surge's historical CN bypass and base64's
// node-only output.
func RenderWithProfile(format string, links []string, aiNodes map[string]bool, clashTpl, singboxTpl, subURL string, profile RoutingProfile) (body string, contentType string, err error) {
	return RenderWithOptions(format, links, aiNodes, clashTpl, singboxTpl, subURL, RenderOptions{Profile: profile})
}

// RenderOptions carries subscription render toggles beyond format selection.
type RenderOptions struct {
	Profile RoutingProfile
	// ClashDisableUDP turns off Clash udp:true advertisement (setting sub_clash_udp=0).
	ClashDisableUDP bool
}

// RenderWithOptions is the full render entry used by the subscription handler.
func RenderWithOptions(format string, links []string, aiNodes map[string]bool, clashTpl, singboxTpl, subURL string, opt RenderOptions) (body string, contentType string, err error) {
	profile := opt.Profile
	clashOpt := ClashOptions{DisableUDP: opt.ClashDisableUDP}
	if profile == ProfileLegacy && !opt.ClashDisableUDP {
		return Render(format, links, aiNodes, clashTpl, singboxTpl, subURL)
	}
	if profile == ProfileLegacy {
		// Legacy profile but with Clash UDP overridden — still use ClashWithOptions.
		switch NormalizeFormat(format) {
		case FormatClash:
			out, e := ClashWithOptions(parseWithAI(links, aiNodes), clashTpl, ProfileLegacy, clashOpt)
			return out, "text/yaml; charset=utf-8", e
		case FormatSingbox:
			out, e := Singbox(parseWithAI(links, aiNodes), singboxTpl)
			return out, "application/json; charset=utf-8", e
		case FormatSurge:
			return Surge(parseWithAI(links, aiNodes), subURL), "text/plain; charset=utf-8", nil
		default:
			return Base64(links), "text/plain; charset=utf-8", nil
		}
	}
	switch NormalizeFormat(format) {
	case FormatClash:
		out, e := ClashWithOptions(parseWithAI(links, aiNodes), clashTpl, profile, clashOpt)
		return out, "text/yaml; charset=utf-8", e
	case FormatSingbox:
		out, e := SingboxWithProfile(parseWithAI(links, aiNodes), singboxTpl, profile)
		return out, "application/json; charset=utf-8", e
	case FormatSurge:
		return SurgeWithProfile(parseWithAI(links, aiNodes), subURL, profile), "text/plain; charset=utf-8", nil
	default:
		// A base64 subscription contains nodes only. Routing belongs to the local
		// client, so claiming to apply a server-side profile would be misleading.
		return Base64(links), "text/plain; charset=utf-8", nil
	}
}

func parseWithAI(links []string, aiNodes map[string]bool) []*Proxy {
	ps := ParseLinks(links)
	if len(aiNodes) > 0 {
		for _, p := range ps {
			p.AI = aiNodes[p.Raw]
		}
	}
	return ps
}

// ParseLinks parses each link, skipping unparseable ones.
func ParseLinks(links []string) []*Proxy {
	var out []*Proxy
	for _, l := range links {
		if p, err := ParseLink(l); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// DefaultClashTemplate carries the anti-leak config (fake-ip DNS + sniffer +
// TUN + rules). The TUN section includes route-exclude-address with private
// ranges; proxy server IPs are dynamically injected by injectRouteExclude()
// at render time — this is server-side data (the nodes the server defines),
// not client-side configuration.
const DefaultClashTemplate = `
# The real IPv6 master switch. mihomo's parseIPV6 nulls out BOTH dns.fake-ip-range6
# and tun.inet6-address unless this is true AND verifyIP6() finds a global-unicast
# IPv6 on a host interface. That runtime probe is why this is safe to ship on by
# default: a v4-only client silently collapses to the old "no IPv6 anywhere"
# behavior instead of failing to start. true is already the built-in default —
# stated explicitly because everything below is inert without it.
ipv6: true
# Persist across client restarts. Without a profile block at all (the previous
# state), every restart threw away the user's manually picked node and reset the
# selector to its default — a daily annoyance for anyone who switches nodes, and
# one that reads as "this service is unstable" rather than as a missing config
# key. store-fake-ip additionally keeps the fake-ip mapping stable, so apps and
# OS caches still holding a pre-restart fake address resolve to the right domain
# instead of misrouting until the entry is re-learned.
profile:
  store-selected: true
  store-fake-ip: true
# Measure health checks consistently (excluding the initial handshake) so the
# generated fallback policies compare nodes on equal terms. A single-node user
# gains nothing.
#
# Deliberately NOT setting tcp-concurrent here, though it is commonly paired
# with the above: it races connections across the several IPs a hostname
# resolves to, and in this topology there are none. With fake-ip and no CN
# bypass, mihomo never resolves the destination locally — it hands the hostname
# to the node — so the only address it dials itself is the proxy server's. The
# option would be inert. Left out on purpose; do not add it back as an
# oversight.
unified-delay: true
dns:
  enable: true
  # Route the DNS module's own upstream connections through the same rule
  # table as regular traffic (GEOSITE/GEOIP/MATCH below), instead of dialing
  # nameserver directly off-tunnel. Without this, every DNS query exits via
  # the real network interface, which is what a DNS-leak-test site actually
  # measures (it sees the resolver's egress ASN, not whether the query bytes
  # were encrypted). With no CN bypass in the rules below, every nameserver
  # entry falls through to MATCH and is tunneled through the proxy.
  #
  # Requires proxy-server-nameserver below to avoid a resolution loop when
  # dialing the proxy node itself: that key (and default-nameserver) is always
  # dialed directly, never through this rule table, which is what breaks the
  # circularity of "resolve the node's address by asking through the node".
  respect-rules: true
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  # Without a v6 pool, fake-ip answers AAAA with an empty record and every
  # IPv6-only hostname is unreachable. With it, AAAA gets a *fake* v6 that must
  # traverse TUN and is re-resolved at the node — so no real IPv6 ever reaches
  # the application, and a v4-only node still serves the domain over v4.
  #
  # This value must stay in the fdfe:dcba:9876::/64 prefix. Unlike IPv4 — where
  # tun.inet4-address is derived from fake-ip-range narrowed to /30 — mihomo does
  # NOT derive inet6-address from this key; it is an independent hardcoded default
  # (fdfe:dcba:9876::1/126) that merely happens to sit inside this prefix.
  #
  # What the overlap buys is pool hygiene, not routability: mihomo masks the
  # prefix and starts allocating at ::4 (component/fakeip/pool.go), so the ::0-::3
  # block the TUN itself occupies can never be handed out as a fake address. That
  # mirrors the IPv4 arrangement exactly (198.18.0.1/16 pool, 198.18.0.1/30 TUN,
  # first allocation 198.18.0.4). Point this key elsewhere and you must set
  # tun.inet6-address to match, or the pool can allocate the TUN's own address.
  #
  # Unknown to cores older than v1.19.16, and mihomo parses config with non-strict
  # yaml.Unmarshal (no KnownFields), so old clients drop the key and degrade to the
  # previous empty-AAAA behavior rather than failing to load.
  fake-ip-range6: fdfe:dcba:9876::1/64
  fake-ip-filter:
    - "*.lan"
    - "*.local"
    - "+.msftconnecttest.com"
    - "+.msftncsi.com"
    - "time.*.com"
    - "ntp.*.com"
    - "+.pool.ntp.org"
    # These STUN entries keep NAT traversal working for games and voice/video
    # apps — they are NOT a WebRTC-leak defense, and nothing in this file can
    # be. A browser gathers WebRTC host candidates by enumerating OS network
    # interfaces directly: no packet is sent, so there is nothing for TUN to
    # capture or for a rule to route. Private IPv4 candidates are mDNS-masked
    # by modern browsers, but a global IPv6 address on the physical NIC is not,
    # and a leak-test page will read it via JS. Fix it in the browser
    # (Firefox media.peerconnection.ice.default_address_only=true; Chrome
    # WebRtcIPHandling=disable_non_proxied_udp) or disable IPv6 on the adapter.
    - "+.stun.*.*"
    - "+.stun.*.*.*"
    - "+.stun.*.*.*.*"
    - "+.stun.*.*.*.*.*"
    - "+.ntp.org.cn"
    - "+.srv.nintendo.net"
    - "+.stun.playstation.net"
    - "+.xboxlive.com"
    - "localhost.ptlogin2.qq.com"
  # NOT a global IPv6 kill switch, despite the name. mihomo threads this flag to
  # withResolver only — withFakeIP never sees it (dns/middleware.go), so with
  # fake-ip-range6 set above, fake AAAA answers are unaffected. What it still
  # governs is the one escape hatch: a domain matched by fake-ip-filter skips
  # fake-ip, reaches the real resolver, and would otherwise be handed a genuine
  # IPv6. Keeping it false collapses those to an empty answer.
  #
  # That is exactly the split we want — fake v6 for tunneled traffic, no real v6
  # for the direct/local names in the filter list above.
  #
  # KNOWN COST, and the first thing to check if a domain node is unreachable:
  # injectNodeDomains appends every domain-based node's hostname to the
  # fake-ip-filter list above, which puts those hostnames on this same
  # withResolver path — so their AAAA answers are emptied too. A node whose
  # domain is dual-stack is fine (it falls back to the A record), but a node
  # whose domain has ONLY an AAAA record cannot be resolved at all, and the
  # client reports it as a plain connection failure with nothing pointing here.
  # The tradeoff is deliberate: AAAA-only node domains are rare, whereas the
  # filter list's direct/local names exist on every client. mihomo has no
  # per-domain v6 switch, so the alternative is flipping this to true and
  # handing real IPv6 to *.lan / NTP / Xbox as well. Node domains must have an
  # A record.
  ipv6: false
  default-nameserver:
    - 223.5.5.5
    - 119.29.29.29
  # DoH/DoT only — mihomo races every nameserver entry concurrently, so a plain
  # UDP:53 entry here leaks every queried domain in cleartext on the local
  # network (it isn't tunneled through the proxy). default-nameserver/
  # proxy-server-nameserver stay plain IP: they only bootstrap-resolve the DoH
  # hostnames and the node's own address, which is low-sensitivity.
  #
  # These are all foreign resolvers, spanning two vendors on purpose — a single
  # provider outage or a single provider logging everything are both worth
  # avoiding. There is deliberately no CN resolver here: with no CN bypass in
  # the rules below, a domestic DoH would be dialed through a foreign node,
  # which is slower and pointless.
  #
  # No fallback/fallback-filter block: that mechanism exists to cross-check a
  # domestic resolver's answer against GeoIP and re-query abroad when it looks
  # polluted. With no domestic resolver and no CN bypass, there is nothing to
  # cross-check — every answer already comes from these servers over the tunnel.
  nameserver:
    - https://1.1.1.1/dns-query
    - https://dns.google/dns-query
    - tls://1.1.1.1:853
    - tls://8.8.8.8:853
  proxy-server-nameserver:
    - 223.5.5.5
    - 119.29.29.29
sniffer:
  enable: true
  sniff:
    TLS:
      ports: [443, 8443]
    HTTP:
      ports: [80, 8080-8880]
    QUIC:
      ports: [443]
tun:
  enable: true
  stack: gvisor
  auto-route: true
  auto-detect-interface: true
  # TCP:53 as well as UDP. This is a consistency patch, NOT a leak fix — worth
  # being precise about, because it looks like one. A DNS server inside
  # route-exclude-address below (the usual case: the LAN router at
  # 192.168.1.1) never enters TUN at all, so no dns-hijack entry can reach it
  # on either protocol; only strict-route's firewall rules stop that path. What
  # this does cover is a DNS server that IS routed into TUN — a public resolver
  # statically configured on an adapter, say. Without the tcp:// entry, its
  # TCP:53 queries pass through and get a real answer instead of a fake-ip one,
  # which is inconsistent rather than leaky.
  dns-hijack:
    - "any:53"
    - "tcp://any:53"
  # Windows sends DNS queries to every active network adapter at once ("smart
  # multi-homed name resolution") — physical NIC included — and dns-hijack
  # alone doesn't stop that. strict-route makes mihomo add the Windows
  # firewall rules that block the physical adapter's own DNS queries, so they
  # can't race the TUN interface and leak straight to the ISP's resolver
  # (this is a documented mihomo/clash-verge-rev gap: MetaCubeX/mihomo docs,
  # clash-verge-rev#3133). false only helped avoid breaking niche apps like
  # VirtualBox; the leak it causes is worse.
  strict-route: true
  # NOTE: there is deliberately no "ipv6" key here. mihomo has never had a
  # tun.ipv6 option at any version — it is a sing-box option name, and yaml.v3
  # non-strict parsing meant the "ipv6: false" that used to sit here was dropped
  # on the floor, suppressing nothing while reading as if it did. The real
  # controls are the top-level ipv6 switch and tun.inet6-address, whose default
  # (fdfe:dcba:9876::1/126) already matches fake-ip-range6 above and so is left
  # implicit.
  #
  # The route-exclude-address list written below is IPv4-only on purpose, but
  # the RENDERED config is not: injectRouteExclude appends each node's own
  # server IP, and ensureCIDR gives v6 literals a /128 — so a v6 node produces
  # a v6 entry here. That is wanted (it is the anti-loop carve-out for the
  # node's own address) and it is safe, because a /128 excludes exactly one
  # host and can never swallow the fake-ip pool the way a prefix would.
  #
  # What stays IPv4-only is the hand-written private-range block. auto-route does
  # install a v6 default route once inet6-address exists, but the kernel's own
  # more-specific on-link routes (fe80::/64 dev <if>) win in the main table, so
  # neighbor discovery and mDNS keep working without an explicit carve-out; any
  # remaining private v6 destination is sent DIRECT by the GEOIP,private rule.
  # (The sing-box default inbound does list fe80::/10 and ff00::/8 — same
  # reasoning, it is legibility there rather than necessity.)
  #
  # Above all, fc00::/7 must never be added here: fake-ip-range6 is allocated
  # from inside it, and excluding the parent prefix would route every fake IPv6
  # back out of the tunnel — undoing the fix entirely.
  route-exclude-address:
    - 192.168.0.0/16
    - 10.0.0.0/8
    - 172.16.0.0/12
    - 169.254.0.0/16
    - 224.0.0.0/4
    - 255.255.255.255/32
# No default CN bypass: there is deliberately no "GEOSITE,cn,DIRECT" or
# "GEOIP,CN,DIRECT" here, so everything that isn't ad-blocked or LAN-local
# falls through to MATCH and goes out the proxy. Domestic sites are therefore
# slower and a few geo-restricted ones (banking, payments, licensed video) may
# refuse a foreign egress IP outright — that is the accepted cost of having no
# split, not a bug to work around by re-adding a CN rule.
#
# The two "private" rules below are NOT a geographic bypass and must stay: they
# keep the LAN reachable (router admin page, NAS, printers) and stop TUN from
# looping traffic aimed at the local network back through itself.
rules:
  - GEOSITE,category-ads-all,REJECT
  - GEOSITE,private,DIRECT
  # no-resolve: match only an ALREADY-known IP (e.g. literal-IP connections),
  # never force a fresh local DNS resolution just to evaluate this rule.
  # Without it, every fake-ip'd domain would be resolved locally via nameserver
  # before it could even reach MATCH — and mihomo has an open bug where
  # respect-rules doesn't reliably tunnel that resolution
  # (MetaCubeX/mihomo#2971), so it leaks out the real network instead. With
  # no-resolve, those domains skip straight to MATCH untouched; the destination
  # hostname travels through the tunnel and is resolved server-side by the node,
  # never locally.
  - GEOIP,private,DIRECT,no-resolve
`

// DefaultSingboxTemplate carries the sing-box anti-leak dns+route. Ads are
// rejected and LAN traffic stays direct; all public traffic, including CN
// destinations, otherwise falls through to the proxy. Rule-sets are fetched
// once and cached.
//
// Targets sing-box ≥1.12. The DNS block uses the typed server format that 1.12
// introduced and that 1.14 makes mandatory — the legacy `address` strings this
// replaced are not deprecated-but-working in 1.14, they are gone, and a profile
// still carrying them fails to load. There is no formulation that satisfies both
// 1.11 and 1.14, so the floor moved. Admin-stored templates are rewritten to
// this shape at render time by modernizeSingboxDNS, so an install that pasted
// the old default is carried across too.
//
// `independent_cache` and `experimental.cache_file.store_rdrc` below are
// deprecated as of 1.14 (removal in 1.16) but still honored, and dropping them
// today would change caching behavior on the 1.12/1.13 clients most users are
// actually running. They stay until the floor moves again.
const DefaultSingboxTemplate = `{
  "dns": {
    "servers": [
      {"tag": "remote", "type": "https", "server": "1.1.1.1", "detour": "proxy"},
      {"tag": "local", "type": "https", "server": "223.5.5.5"},
      {"tag": "fake", "type": "fakeip", "inet4_range": "198.18.0.0/15", "inet6_range": "fc00::/18"}
    ],
    "rules": [
      {"query_type": ["A", "AAAA"], "server": "fake"}
    ],
    "independent_cache": true,
    "final": "remote"
  },
  "route": {
    "auto_detect_interface": true,
    "final": "proxy",
    "default_domain_resolver": "local",
    "rules": [
      {"action": "sniff"},
      {"protocol": "dns", "action": "hijack-dns"},
      {"rule_set": "geosite-ads", "action": "reject"},
      {"ip_is_private": true, "outbound": "direct"}
    ],
    "rule_set": [
      {"type": "remote", "tag": "geosite-ads", "format": "binary", "download_detour": "proxy",
       "url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads-all.srs"}
    ]
  },
  "experimental": {
    "cache_file": {"enabled": true, "store_rdrc": true}
  }
}`
