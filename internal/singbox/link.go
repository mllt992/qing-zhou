package singbox

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// LinkParams holds everything needed to render a client share-link for a
// self-built node, so 轻舟 can build subscriptions from its own data instead of
// fetching them from sing-box's sub server.
type LinkParams struct {
	Type     string // vless | tuic | hysteria2
	Tag      string // node remark (becomes the #fragment)
	Host     string // dial address advertised to clients (origin IP)
	Port     int
	UUID     string
	Password string

	// TLS reports whether the inbound actually has a TLS config attached
	// (sb_inbounds.tls_id != 0). It is NOT derivable from SNI: server_name is
	// optional and routinely empty on self-signed or bare-IP inbounds, so
	// inferring TLS from "SNI != ''" silently renders those nodes as plaintext.
	// vless/trojan sidestep this by being unconditionally TLS; vmess is the one
	// protocol whose link has to carry the flag explicitly.
	TLS         bool
	SNI         string
	PublicKey   string // reality pbk
	ShortID     string // reality sid
	Fingerprint string // utls fp, default chrome
	ALPN        string // comma-joined, e.g. h3,h2,http/1.1
	Congestion  string // tuic congestion_control
	ZeroRTT     bool   // tuic 0-RTT handshake (must match the inbound to save a RTT)
	Insecure    bool
	Flow        bool // vless reality vision

	// PinSHA256 is the leaf certificate's SHA-256 fingerprint (uppercase hex),
	// set only for self-signed certificates. It rides along on the hysteria-family
	// links as the standard `pinSHA256` parameter, which lets a client verify the
	// exact certificate instead of trusting whatever is presented.
	//
	// It is additive, never a replacement for Insecure: clients that do not know
	// the parameter ignore it and behave exactly as before, so advertising it
	// cannot take a working node offline. It is deliberately absent from the
	// Clash and sing-box renderers — pinning there needs sing-box ≥1.13's
	// certificate_public_key_sha256 (a *different* hash, and an unknown field on
	// the 1.12 clients the subscription still targets).
	PinSHA256 string

	// transport (vless/vmess/trojan): Network is ws|grpc|httpupgrade ("" or tcp = none)
	Network     string
	Path        string // ws/httpupgrade path
	WSHost      string // ws/httpupgrade Host header
	ServiceName string // grpc service name

	// ws 0-RTT early data. Both must match the inbound; carried so the client
	// gets the same 1-RTT-saving handshake the server advertises.
	WSMaxEarlyData    int
	WSEarlyDataHeader string

	// shadowsocks
	Method    string // ss method (2022-blake3-*)
	ServerKey string // ss-2022 server PSK (inbound password)

	// hysteria v1 bandwidth (Mbps)
	UpMbps   int
	DownMbps int

	// hysteria2 obfs (salamander / gecko). Type+password must be set on the
	// client link or the handshake fails when the inbound has obfs enabled.
	Obfs         string // obfs type, e.g. "salamander" | "gecko"
	ObfsPassword string
	// Gecko-only on-wire packet size hints (sing-box 1.14+). 0 = omit (defaults).
	ObfsMinPacket int
	ObfsMaxPacket int
	// Optional BBR profile mirrored from the inbound (sing-box 1.14+).
	BBRProfile string

	// TCP dial tuning (TCP-based protocols only). TCP Fast Open and MPTCP each
	// need BOTH ends enabled to do anything, so 轻舟 mirrors the inbound's
	// setting onto the client link.
	TCPFastOpen bool // -> tcp_fast_open / tfo
	MPTCP       bool // -> tcp_multi_path / mptcp

	// Multiplex + TCP Brutal congestion control (vless/vmess/trojan). Multiplex
	// must be enabled on BOTH ends; Brutal is a sub-option that only takes
	// effect when the client also turns it on with per-endpoint bandwidths.
	// BrutalUp/BrutalDown are the CLIENT's uplink/downlink in Mbps (the caller
	// mirrors the server's values across the link).
	Mux        bool
	BrutalUp   int
	BrutalDown int

	// NoUDP marks a node that cannot relay UDP: its inbound is bound to a proxy
	// egress whose UDP mode is block, so the node itself rejects every UDP
	// packet (see singbox.Relay.RejectUDP). Without this flag the subscription
	// still advertises the node as UDP-capable — clash `udp: true`, sing-box
	// unrestricted network — and clients duly send UDP (QUIC, STUN) into what
	// is, from their side, a silent black hole: nothing refuses the traffic, so
	// every attempt runs out its own timeout before falling back to TCP.
	//
	// Carried as the custom `qz-udp=block` param (same convention as tfo/mux:
	// 轻舟's own renderers read it back, unknown-key-tolerant clients ignore
	// it), which 轻舟's renderers turn into clash `udp: false`, sing-box
	// `"network": "tcp"` and Surge `udp-relay=false` — the client then refuses
	// UDP locally, instantly, and applications take their TCP path without
	// waiting on a timeout. Per-node by construction: nodes on other exits are
	// untouched.
	//
	// Each renderer applies it only where its own core has a field for it (see
	// singboxOutbound and clashProxy) — sing-box's anytls outbound and mihomo's
	// hysteria/hysteria2/tuic proxies have none, and an unknown key there fails
	// the whole config for every subscriber holding that node.
	NoUDP bool
}

// noUDPParam is the query key/value advertising that a node cannot carry UDP.
const noUDPParam = "qz-udp=block"

// tuicUDPRelayModeNative is the UDP relay mode every mainstream TUIC
// implementation already defaults to. It is emitted explicitly, never inferred.
const tuicUDPRelayModeNative = "native"

// tuningQuery renders the TCP/multiplex tuning params shared by the URL-style
// TCP protocols (vless/trojan). These are custom query keys understood by
// 轻舟's own subscription renderer; unknown-key-tolerant clients ignore them.
// allowMux gates multiplex: it must be dropped when xtls-rprx-vision flow is
// active (sing-box rejects multiplex together with vision).
func (p LinkParams) tuningQuery(allowMux bool) []string {
	var q []string
	if p.TCPFastOpen {
		q = append(q, "tfo=1")
	}
	if p.MPTCP {
		q = append(q, "mptcp=1")
	}
	if allowMux && p.Mux {
		q = append(q, "mux=1")
		if p.BrutalUp > 0 && p.BrutalDown > 0 {
			q = append(q, "brutal_up="+strconv.Itoa(p.BrutalUp), "brutal_down="+strconv.Itoa(p.BrutalDown))
		}
	}
	return q
}

// transportQuery appends the transport-specific query params for a share link.
func (p LinkParams) transportQuery() []string {
	switch p.Network {
	case "ws":
		q := []string{"type=ws"}
		if p.Path != "" {
			q = append(q, "path="+url.QueryEscape(p.Path))
		}
		if p.WSHost != "" {
			q = append(q, "host="+url.QueryEscape(p.WSHost))
		}
		if p.WSMaxEarlyData > 0 {
			q = append(q, "max_early_data="+strconv.Itoa(p.WSMaxEarlyData))
			if p.WSEarlyDataHeader != "" {
				q = append(q, "early_data_header_name="+url.QueryEscape(p.WSEarlyDataHeader))
			}
		}
		return q
	case "httpupgrade":
		q := []string{"type=httpupgrade"}
		if p.Path != "" {
			q = append(q, "path="+url.QueryEscape(p.Path))
		}
		if p.WSHost != "" {
			q = append(q, "host="+url.QueryEscape(p.WSHost))
		}
		return q
	case "grpc":
		q := []string{"type=grpc"}
		if p.ServiceName != "" {
			q = append(q, "serviceName="+url.QueryEscape(p.ServiceName))
		}
		return q
	default:
		return []string{"type=tcp"}
	}
}

// joinHostPort builds the authority of a share-link URI, bracketing an IPv6
// literal as RFC 3986 requires.
//
// Two traps make this more than a call to net.JoinHostPort.
//
// First, plain concatenation is not actually harmless here. Go's url.Parse is
// forgiving — it splits host and port at the *last* colon, so it recovers
// "…@2001:db8::1:443" correctly and 轻舟's own pipeline round-trips it — but
// that tolerance is Go's, not the spec's. v2rayN, mihomo and sing-box parse the
// authority per RFC 3986, where an unbracketed IPv6 literal is malformed. So the
// symptom is a node that looks fine in the panel and fails only in the client.
//
// Second, net.JoinHostPort brackets any host containing a colon without checking
// whether it is already bracketed. An admin who writes [2001:db8::1] — which is
// the natural way to type it, and what a Clash YAML often carries — would get
// [[2001:db8::1]]:443, which really is unparseable everywhere, including here.
// Strip a matched pair first.
func joinHostPort(host, port string) string {
	if len(host) > 1 && host[0] == '[' && host[len(host)-1] == ']' {
		host = host[1 : len(host)-1]
	}
	return net.JoinHostPort(host, port)
}

// BuildShareLink renders a vless/tuic/hysteria2 URI matching the format clients
// already use, so existing imports keep working after the cutover.
func BuildShareLink(p LinkParams) string {
	if p.Host == "" || p.Port == 0 {
		return ""
	}
	fp := p.Fingerprint
	if fp == "" {
		fp = "chrome"
	}
	// esc escapes a value interpolated into a query param or URI userinfo. The
	// values are system-generated today (UUIDs / hex secrets), but escaping
	// prevents a stray reserved char (#/?&@) from producing a malformed or
	// ambiguous link.
	esc := url.QueryEscape
	hp := joinHostPort(p.Host, strconv.Itoa(p.Port))
	frag := "#" + url.QueryEscape(p.Tag)

	tcp := p.Network == "" || p.Network == "tcp"
	switch p.Type {
	case "vless":
		q := p.transportQuery()
		visionActive := false
		if p.PublicKey != "" { // Reality
			q = append(q, "security=reality", "pbk="+esc(p.PublicKey), "sid="+esc(p.ShortID), "fp="+esc(fp), "sni="+esc(p.SNI))
			if p.Flow && tcp { // vision only on raw-TCP Reality
				q = append(q, "flow=xtls-rprx-vision")
				visionActive = true
			}
		} else { // plain TLS
			q = append(q, "security=tls", "fp="+esc(fp), "sni="+esc(p.SNI))
			if p.Insecure {
				q = append(q, "allowInsecure=1")
			}
		}
		// VLESS is the only protocol here whose UDP needs an explicit packet
		// encoding; without it each client picks its own default and UDP fails
		// silently while TCP stays healthy (QUIC downloads hang, browsing is
		// fine). Pin xudp so both ends agree.
		q = append(q, "packetEncoding=xudp")
		q = append(q, p.tuningQuery(!visionActive)...) // mux is invalid with vision flow
		if p.NoUDP {
			q = append(q, noUDPParam)
		}
		return "vless://" + esc(p.UUID) + "@" + hp + "?" + strings.Join(q, "&") + frag
	case "trojan":
		q := p.transportQuery()
		q = append(q, "security=tls", "fp="+esc(fp), "sni="+esc(p.SNI))
		if p.Insecure {
			q = append(q, "allowInsecure=1")
		}
		q = append(q, p.tuningQuery(true)...)
		if p.NoUDP {
			q = append(q, noUDPParam)
		}
		return "trojan://" + esc(p.Password) + "@" + hp + "?" + strings.Join(q, "&") + frag
	case "tuic":
		q := []string{"security=tls"}
		if p.Insecure {
			q = append(q, "insecure=1")
		}
		q = append(q, "fp="+esc(fp), "sni="+esc(p.SNI))
		if p.ALPN != "" {
			q = append(q, "alpn="+esc(p.ALPN))
		}
		if p.Congestion != "" {
			q = append(q, "congestion_control="+esc(p.Congestion))
		}
		if p.ZeroRTT {
			q = append(q, "zero_rtt=1")
		}
		// TUIC has two UDP relay modes and the two ends must agree. sing-box,
		// mihomo and the reference client all default to "native", but not every
		// client does — and when they disagree, TCP works while UDP silently
		// fails, which reads as a broken node rather than a mismatched option.
		// Stating the value that is already everyone's default costs nothing and
		// removes the ambiguity.
		q = append(q, "udp_relay_mode="+tuicUDPRelayModeNative)
		if p.NoUDP {
			q = append(q, noUDPParam)
		}
		return "tuic://" + esc(p.UUID) + ":" + esc(p.Password) + "@" + hp + "?" + strings.Join(q, "&") + frag
	case "hysteria2":
		q := []string{"security=tls"}
		if p.Insecure {
			q = append(q, "insecure=1")
		}
		q = append(q, "fp="+esc(fp), "sni="+esc(p.SNI), "fastopen=0")
		if p.PinSHA256 != "" {
			q = append(q, "pinSHA256="+esc(p.PinSHA256))
		}
		// obfs must be advertised to clients, otherwise the handshake fails
		// when the inbound has salamander obfs enabled.
		if p.Obfs != "" {
			q = append(q, "obfs="+esc(p.Obfs))
			if p.ObfsPassword != "" {
				q = append(q, "obfs-password="+url.QueryEscape(p.ObfsPassword))
			}
			if p.ObfsMinPacket > 0 {
				q = append(q, "obfs-min-packet="+strconv.Itoa(p.ObfsMinPacket))
			}
			if p.ObfsMaxPacket > 0 {
				q = append(q, "obfs-max-packet="+strconv.Itoa(p.ObfsMaxPacket))
			}
		}
		if p.BBRProfile != "" {
			q = append(q, "bbr-profile="+esc(p.BBRProfile))
		}
		if p.NoUDP {
			q = append(q, noUDPParam)
		}
		return "hysteria2://" + esc(p.Password) + "@" + hp + "?" + strings.Join(q, "&") + frag
	case "vmess":
		m := map[string]interface{}{
			"v": "2", "ps": p.Tag, "add": p.Host, "port": strconv.Itoa(p.Port),
			"id": p.UUID, "aid": "0", "scy": "auto", "type": "none",
			"net": p.Network, "host": p.WSHost, "path": p.Path,
		}
		if m["net"] == "" || m["net"] == nil {
			m["net"] = "tcp"
		}
		if p.Network == "grpc" {
			m["path"] = p.ServiceName
		}
		// Drive TLS off the inbound's real tls_id, not off SNI. An inbound with a
		// certificate but no server_name (self-signed, bare IP) used to render
		// tls:"" here, and every renderer downstream keys off this field —
		// clash.go, singbox.go and surge.go all test VMess["tls"] == "tls" — so
		// one empty server_name produced a plaintext node in *all four*
		// subscription formats, not just the base64 one.
		if p.TLS {
			m["tls"] = "tls"
			if p.SNI != "" {
				m["sni"] = p.SNI
			}
			// Unconditional, matching vless/trojan/tuic/hysteria2, which all
			// append fp= with the same "chrome" default. fp is never empty here
			// (it is defaulted above), so vmess nodes now always advertise a uTLS
			// fingerprint where they previously advertised none.
			m["fp"] = fp
			if p.ALPN != "" {
				m["alpn"] = p.ALPN
			}
			if p.Insecure {
				// Not part of the v2rayN vmess schema (it has no such key), but
				// 轻舟's own renderers read it back via Proxy.param, which falls
				// through to the vmess JSON map. Clients that don't know it ignore it.
				m["allowInsecure"] = "1"
			}
		} else {
			m["tls"] = ""
		}
		// Custom tuning keys (ignored by clients that don't understand them);
		// 轻舟's own Clash/sing-box renderers read them back.
		if p.TCPFastOpen {
			m["tfo"] = "1"
		}
		if p.MPTCP {
			m["mptcp"] = "1"
		}
		if p.Mux {
			m["mux"] = "1"
			if p.BrutalUp > 0 && p.BrutalDown > 0 {
				m["brutal_up"] = strconv.Itoa(p.BrutalUp)
				m["brutal_down"] = strconv.Itoa(p.BrutalDown)
			}
		}
		if p.NoUDP {
			m["qz-udp"] = "block" // JSON-map twin of the URL-style noUDPParam
		}
		b, _ := json.Marshal(m)
		return "vmess://" + base64.StdEncoding.EncodeToString(b)
	case "shadowsocks":
		// SIP002: ss://base64url(method:password)@host:port  — for 2022 multi-user
		// password = serverPSK:userPSK.
		userKey := DeriveSSKey(p.Password, p.Method)
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(p.Method + ":" + p.ServerKey + ":" + userKey))
		if p.NoUDP {
			// SIP002 URIs carry a query (the spec puts `plugin` there), so a
			// custom key rides in the same place as on the URL-style links. The
			// spec's optional "/" before it is deliberately omitted: canonicalLink
			// splits at the first '?', so a slash would land in the base and give
			// the node a different NodeKey than the same node unmarked — silently
			// dropping every user's blocklist entry for it whenever an admin
			// flips udp_mode.
			return "ss://" + userinfo + "@" + hp + "?" + noUDPParam + frag
		}
		return "ss://" + userinfo + "@" + hp + frag
	case "anytls":
		q := []string{"security=tls", "fp=" + esc(fp), "sni=" + esc(p.SNI)}
		if p.Insecure {
			q = append(q, "insecure=1")
		}
		if p.NoUDP {
			q = append(q, noUDPParam)
		}
		return "anytls://" + esc(p.Password) + "@" + hp + "?" + strings.Join(q, "&") + frag
	case "hysteria":
		q := []string{"protocol=udp", "auth=" + esc(p.Password), "peer=" + esc(p.SNI)}
		if p.Insecure {
			q = append(q, "insecure=1")
		}
		if p.UpMbps > 0 {
			q = append(q, "upmbps="+strconv.Itoa(p.UpMbps))
		}
		if p.DownMbps > 0 {
			q = append(q, "downmbps="+strconv.Itoa(p.DownMbps))
		}
		if p.ALPN != "" {
			q = append(q, "alpn="+esc(p.ALPN))
		}
		if p.PinSHA256 != "" {
			q = append(q, "pinSHA256="+esc(p.PinSHA256))
		}
		if p.NoUDP {
			q = append(q, noUDPParam)
		}
		return "hysteria://" + hp + "?" + strings.Join(q, "&") + frag
	}
	return ""
}
