// Package subconv parses proxy share-links and renders subscriptions in
// base64 (link list), Clash (mihomo) YAML, and sing-box JSON, injecting an
// anti-leak template (fake-ip DNS / TUN / sniffer) into the latter two.
package subconv

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Proxy is a normalized node parsed from a share link.
type Proxy struct {
	Raw      string
	Protocol string // vless | vmess | ss | trojan | hysteria2 | tuic
	Name     string
	Server   string
	Port     int
	UUID     string
	Password string
	Method   string // ss cipher
	AlterID  int    // vmess
	Params   url.Values
	VMess    map[string]any
	AI       bool // belongs to at least one accessible admin-marked AI group
}

func b64decode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("invalid base64")
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// ParseLink parses a single share URI into a Proxy, rejecting nodes that could
// never work (see validate).
func ParseLink(raw string) (*Proxy, error) {
	p, err := parseLinkRaw(raw)
	if err != nil {
		return nil, err
	}
	if err := validate(p); err != nil {
		return nil, err
	}
	return p, nil
}

// ssMethods are the shadowsocks ciphers both sing-box and mihomo accept.
//
// The cipher is whatever preceded the first ':' in the link's userinfo, i.e.
// fully attacker-controlled for an imported subscription. Both clients treat an
// unknown method as a fatal config error rather than skipping the one node, so
// letting it through would take down every subscriber's whole profile — the same
// failure mode already guarded for packet_encoding.
var ssMethods = map[string]bool{
	"aes-128-gcm": true, "aes-192-gcm": true, "aes-256-gcm": true,
	"aes-128-ctr": true, "aes-192-ctr": true, "aes-256-ctr": true,
	"aes-128-cfb": true, "aes-192-cfb": true, "aes-256-cfb": true,
	"chacha20-ietf-poly1305": true, "xchacha20-ietf-poly1305": true,
	"chacha20-ietf": true, "chacha20": true, "rc4-md5": true,
	"2022-blake3-aes-128-gcm": true, "2022-blake3-aes-256-gcm": true,
	"2022-blake3-chacha20-poly1305": true,
	"none":                          true, "plain": true,
}

// validate drops a parsed node that cannot produce a usable client config.
//
// Dropping one node is always better than emitting it: mihomo and sing-box both
// abort on the *whole* config when a single node is malformed, so one bad entry
// in an imported airport subscription would otherwise leave every user with no
// working profile at all.
func validate(p *Proxy) error {
	if p.Server == "" {
		return fmt.Errorf("missing server")
	}
	// sing-box's server_port is a uint16 and mihomo's port is an int: a value
	// outside this range makes the generated config fail to unmarshal. Neither
	// net.SplitHostPort nor url.Parse range-check it, so it has to happen here.
	if p.Port <= 0 || p.Port > 65535 {
		return fmt.Errorf("port %d out of range", p.Port)
	}
	if p.Protocol == "ss" && !ssMethods[strings.ToLower(strings.TrimSpace(p.Method))] {
		return fmt.Errorf("unsupported shadowsocks method %q", p.Method)
	}
	// mihomo's AnyTLSOption.Password carries no omitempty, so an anytls entry
	// without one fails the *whole* config rather than just this node — the same
	// all-or-nothing behaviour this function exists to guard against.
	if p.Protocol == "anytls" && p.Password == "" {
		return fmt.Errorf("anytls without password")
	}
	return nil
}

// hysteriaBandwidth renders mihomo's up/down value.
//
// Those two fields are the one place where "omit when unknown" is the wrong
// instinct: HysteriaOption declares them without omitempty, so mihomo's decoder
// reports `has unset fields: down, up` and refuses the ENTIRE config — every
// node gone, not just this one. A guessed number only costs some congestion
// control accuracy, so a default is strictly better than an omission here.
func hysteriaBandwidth(mbps string) string {
	if n := atoi(mbps); n > 0 {
		return strconv.Itoa(n) + " Mbps"
	}
	return "100 Mbps"
}

func parseLinkRaw(raw string) (*Proxy, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "vless://"):
		return parseURLStyle(raw, "vless")
	case strings.HasPrefix(raw, "trojan://"):
		return parseURLStyle(raw, "trojan")
	case strings.HasPrefix(raw, "hysteria2://"):
		return parseURLStyle(raw, "hysteria2")
	case strings.HasPrefix(raw, "hy2://"):
		return parseURLStyle(strings.Replace(raw, "hy2://", "hysteria2://", 1), "hysteria2")
	case strings.HasPrefix(raw, "anytls://"):
		return parseURLStyle(raw, "anytls")
	case strings.HasPrefix(raw, "hysteria://"):
		return parseURLStyle(raw, "hysteria")
	case strings.HasPrefix(raw, "tuic://"):
		return parseTuic(raw)
	case strings.HasPrefix(raw, "vmess://"):
		return parseVmess(raw)
	case strings.HasPrefix(raw, "ss://"):
		return parseSS(raw)
	default:
		return nil, fmt.Errorf("unsupported scheme")
	}
}

// ParseList parses a newline/whitespace-separated list, skipping unparseable
// lines. Accepts a base64-encoded blob or raw text.
func ParseList(blob string) []*Proxy {
	blob = strings.TrimSpace(blob)
	// Clash/mihomo YAML config (proxies: …) — convert each node to a share link.
	if looksLikeClash(blob) {
		if ps := ParseClashYAML(blob); len(ps) > 0 {
			return ps
		}
	}
	if !strings.Contains(blob, "://") {
		if dec, err := b64decode(blob); err == nil {
			blob = string(dec)
		}
	}
	var out []*Proxy
	for _, line := range strings.FieldsFunc(blob, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "://") {
			continue
		}
		if p, err := ParseLink(line); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func parseURLStyle(raw, proto string) (*Proxy, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	p := &Proxy{
		Raw:      raw,
		Protocol: proto,
		Name:     frag(u),
		Server:   u.Hostname(),
		Port:     atoi(u.Port()),
		Params:   u.Query(),
	}
	switch proto {
	case "vless":
		p.UUID = u.User.Username()
	case "trojan", "hysteria2", "anytls":
		// password lives in the userinfo
		if pw := u.User.Username(); pw != "" {
			p.Password = pw
		}
	case "hysteria":
		// hysteria v1 has no userinfo at all — the credential is the `auth`
		// query param (per the v1 URI scheme, which the upstream docs adopted
		// from Shadowrocket). It also spells SNI `peer`; normalise that to `sni`
		// here so every renderer and sbTLS can look it up the usual way instead
		// of special-casing this one protocol.
		if peer := p.Params.Get("peer"); peer != "" && p.Params.Get("sni") == "" {
			p.Params.Set("sni", peer)
		}
	}
	if p.Name == "" {
		p.Name = p.Server
	}
	return p, nil
}

func parseTuic(raw string) (*Proxy, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	pw, _ := u.User.Password()
	p := &Proxy{
		Raw:      raw,
		Protocol: "tuic",
		Name:     frag(u),
		Server:   u.Hostname(),
		Port:     atoi(u.Port()),
		UUID:     u.User.Username(),
		Password: pw,
		Params:   u.Query(),
	}
	if p.Name == "" {
		p.Name = p.Server
	}
	return p, nil
}

func parseVmess(raw string) (*Proxy, error) {
	dec, err := b64decode(strings.TrimPrefix(raw, "vmess://"))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(dec, &m); err != nil {
		return nil, err
	}
	p := &Proxy{
		Raw:      raw,
		Protocol: "vmess",
		Name:     str(m["ps"]),
		Server:   str(m["add"]),
		Port:     atoi(str(m["port"])),
		UUID:     str(m["id"]),
		AlterID:  atoi(str(m["aid"])),
		VMess:    m,
	}
	if p.Name == "" {
		p.Name = p.Server
	}
	return p, nil
}

func parseSS(raw string) (*Proxy, error) {
	body := strings.TrimPrefix(raw, "ss://")
	name := ""
	if i := strings.Index(body, "#"); i >= 0 {
		name, _ = url.QueryUnescape(body[i+1:])
		body = body[:i]
	}
	// SIP002 puts options in the query (plugin=..., and 轻舟's own qz-udp).
	// This used to be discarded wholesale, which was fine while nothing read
	// it; keep the parsed form so ss nodes see the same params as URL-style
	// links.
	var params url.Values
	if i := strings.Index(body, "?"); i >= 0 {
		params, _ = url.ParseQuery(body[i+1:])
		body = body[:i]
	}
	var method, password, server string
	var port int
	if at := strings.LastIndex(body, "@"); at >= 0 {
		// ss://base64(method:password)@host:port
		userPart := body[:at]
		hostPart := body[at+1:]
		if dec, err := b64decode(userPart); err == nil {
			userPart = string(dec)
		}
		method, password = splitColon(userPart)
		server, port = splitHostPort(hostPart)
	} else {
		// ss://base64(method:password@host:port)
		dec, err := b64decode(body)
		if err != nil {
			return nil, err
		}
		s := string(dec)
		at2 := strings.LastIndex(s, "@")
		if at2 < 0 {
			return nil, fmt.Errorf("bad ss")
		}
		method, password = splitColon(s[:at2])
		server, port = splitHostPort(s[at2+1:])
	}
	if name == "" {
		name = server
	}
	return &Proxy{Raw: raw, Protocol: "ss", Name: name, Server: server, Port: port, Method: method, Password: password, Params: params}, nil
}

// DecodeLinks returns the raw share-link lines from a subscription blob
// (base64-encoded or plain text).
func DecodeLinks(blob string) []string {
	blob = strings.TrimSpace(blob)
	if !strings.Contains(blob, "://") {
		if dec, err := b64decode(blob); err == nil {
			blob = string(dec)
		}
	}
	var out []string
	for _, line := range strings.FieldsFunc(blob, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "://") {
			out = append(out, line)
		}
	}
	return out
}

// volatileParams are client-dial tuning knobs a share link may carry that say
// nothing about *which* node it is. 轻舟 mirrors the inbound's current settings
// onto every self-built link, so they change whenever an admin retunes a node —
// or whenever 轻舟 starts emitting a key it didn't before. Excluding them keeps
// NodeKey pointing at the same node across those changes.
var volatileParams = map[string]bool{
	"packetEncoding": true, "packet_encoding": true,
	"tfo": true, "mptcp": true, "mux": true,
	"brutal_up": true, "brutal_down": true,
	// The no-UDP marker follows the bound egress's udp_mode; an admin flipping
	// that (or unbinding the egress) must not re-key the node and silently
	// undo every user's blocklist entry for it.
	"qz-udp": true,
}

// NodeKey returns a stable per-node identifier derived from a share link,
// ignoring the #remark (which carries volatile bits like an airport's live
// speed or the user's remaining-traffic suffix) and the tuning params above.
// Used so a user can disable specific nodes without storing the
// credential-bearing link a second time.
//
// Self-built links are re-rendered from the sing-box config on every request
// rather than stored, so anything the hash covers is a key the user's blocklist
// is pinned to — see NodeDisabled for the compatibility read path.
func NodeKey(link string) string {
	return hashKey(canonicalLink(link))
}

// legacyNodeKey is NodeKey as it hashed before volatile params were excluded:
// the whole link minus its fragment. Blocklist rows written by older builds
// still carry these, and the links themselves are gone, so the only way to
// honor those rows is to recompute the old key from the live link.
func legacyNodeKey(link string) string {
	return hashKey(stripFragment(link))
}

// NodeKeys returns every key a link may already be stored under in a blocklist:
// the current one first, then the legacy one when it differs. Un-blocking has to
// clear all of them — deleting only the current key would leave a row written by
// an older build behind, and the node would stay hidden however many times the
// user flips the toggle.
func NodeKeys(link string) []string {
	k, legacy := NodeKey(link), legacyNodeKey(link)
	if k == legacy {
		return []string{k}
	}
	return []string{k, legacy}
}

// NodeDisabled reports whether link is in a user's blocklist, accepting both
// the current key and the legacy one. Always resolve a link against a blocklist
// through this rather than indexing the map with NodeKey directly.
func NodeDisabled(disabled map[string]bool, link string) bool {
	if len(disabled) == 0 {
		return false
	}
	for _, k := range NodeKeys(link) {
		if disabled[k] {
			return true
		}
	}
	return false
}

func hashKey(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:8]) // 16 hex chars
}

func stripFragment(link string) string {
	if i := strings.LastIndex(link, "#"); i >= 0 {
		link = link[:i]
	}
	return strings.TrimSpace(link)
}

// canonicalLink drops the #remark and the volatile tuning params, then sorts
// what's left so param order can't shift the key either. Params are kept in
// their raw (still-escaped) form — reserialising through url.Values would make
// the key sensitive to escaping style instead. Links with no query (vmess://
// carries a base64 JSON blob, whose alphabet has no '?') are hashed whole.
func canonicalLink(link string) string {
	s := stripFragment(link)
	i := strings.IndexByte(s, '?')
	if i < 0 {
		return s
	}
	base, rawQuery := s[:i], s[i+1:]
	parts := strings.Split(rawQuery, "&")
	kept := make([]string, 0, len(parts))
	for _, kv := range parts {
		k := kv
		if j := strings.IndexByte(kv, '='); j >= 0 {
			k = kv[:j]
		}
		if !volatileParams[k] {
			kept = append(kept, kv)
		}
	}
	if len(kept) == 0 {
		return base
	}
	sort.Strings(kept)
	return base + "?" + strings.Join(kept, "&")
}

// WithLinkRemark overrides only the display name, preserving credentials and
// protocol options. An empty remark keeps the original link byte-for-byte.
func WithLinkRemark(link, remark string) string {
	remark = strings.TrimSpace(remark)
	if remark == "" || link == "" {
		return link
	}
	if strings.HasPrefix(link, "vmess://") {
		payload, _, _ := strings.Cut(strings.TrimPrefix(link, "vmess://"), "#")
		decoded, err := b64decode(payload)
		if err != nil {
			return link
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(decoded, &fields) != nil || fields == nil {
			return link
		}
		fields["ps"], _ = json.Marshal(remark)
		encoded, err := json.Marshal(fields)
		if err != nil {
			return link
		}
		return "vmess://" + base64.StdEncoding.EncodeToString(encoded)
	}
	base, _, _ := strings.Cut(link, "#")
	return base + "#" + url.QueryEscape(remark)
}

// LinkRemark returns the (url-decoded) #fragment remark of a share link.
func LinkRemark(link string) string {
	if i := strings.LastIndex(link, "#"); i >= 0 {
		if s, err := url.QueryUnescape(link[i+1:]); err == nil {
			return s
		}
		return link[i+1:]
	}
	return ""
}

func frag(u *url.URL) string {
	s, err := url.QueryUnescape(u.Fragment)
	if err != nil {
		return u.Fragment
	}
	return s
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

func splitColon(s string) (string, string) {
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func splitHostPort(s string) (string, int) {
	// net.SplitHostPort handles bracketed IPv6 ([::1]:8388) and strips the
	// brackets; a naive LastIndex(":") would split a bare IPv6 at the wrong colon.
	if host, port, err := net.SplitHostPort(s); err == nil {
		return host, atoi(port)
	}
	// No port (or malformed): strip brackets from a bare IPv6 literal if present.
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return s[1 : len(s)-1], 0
	}
	return s, 0
}

func (p *Proxy) param(keys ...string) string {
	for _, k := range keys {
		if v := p.Params.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// udpBlocked reports whether the link marks its node as unable to relay UDP
// (`qz-udp=block`, emitted for inbounds bound to a UDP-blocking proxy egress —
// see singbox.LinkParams.NoUDP). The node drops UDP either way; this flag lets
// the renderers say so in the client config, so the refusal happens instantly
// client-side instead of as a silent server-side black hole each application
// must time out against. Read from the query for URL-style links and from the
// JSON map for vmess (safe to check both: the key is 轻舟's own, so the "type"
// namespace collision that keeps param() and the vmess map apart can't occur).
func (p *Proxy) udpBlocked() bool {
	if p.param("qz-udp") == "block" {
		return true
	}
	return p.VMess != nil && str(p.VMess["qz-udp"]) == "block"
}

// tlsParam reads a TLS-related setting from whichever namespace the proxy has:
// the URL query for url-style links, or the vmess JSON map, which is where a
// vmess:// link keeps the same information.
//
// This is deliberately NOT folded into param(). The two namespaces collide on
// "type" — in a url-style link it names the transport (ws/grpc/httpupgrade),
// while in vmess JSON it is the obfuscation header type ("none") — so a blanket
// fallthrough would make sbTransport read a vmess node's header type as its
// transport. Only the TLS keys, which mean the same thing in both, fall through.
func (p *Proxy) tlsParam(keys ...string) string {
	if v := p.param(keys...); v != "" {
		return v
	}
	if p.VMess != nil {
		for _, k := range keys {
			if v := str(p.VMess[k]); v != "" {
				return v
			}
		}
	}
	return ""
}

// tlsInsecure reports whether the node opts out of certificate verification.
//
// Centralised for two reasons. The spelling varies by dialect — url-style links
// use allowInsecure or insecure, sing-box config uses allow_insecure — and each
// renderer previously accepted its own subset, so the same imported node came
// out with skip-cert-verify in one format and without it in another.
//
// The value form varies too. 轻舟 writes the string "1", but a vmess link from
// another panel routinely carries a JSON boolean, which str() renders as "true";
// comparing against "1" alone silently dropped the exemption and left a
// self-signed node failing certificate verification with nothing to explain why.
func (p *Proxy) tlsInsecure() bool {
	switch p.tlsParam("insecure", "allowInsecure", "allow_insecure") {
	case "1", "true":
		return true
	}
	return false
}

// tuning is the TCP/multiplex client-dial tuning carried by a link (see
// singbox.LinkParams.tuningQuery). Values live in Params for url-style protocols
// and in the vmess JSON map, so tuning() reads from whichever a Proxy has.
type tuning struct {
	tfo, mptcp, mux bool
	brutalUp        int
	brutalDown      int
}

func (p *Proxy) tuning() tuning {
	val := func(k string) string {
		if p.Params != nil {
			if v := p.Params.Get(k); v != "" {
				return v
			}
		}
		if p.VMess != nil {
			return str(p.VMess[k])
		}
		return ""
	}
	return tuning{
		tfo:        val("tfo") == "1",
		mptcp:      val("mptcp") == "1",
		mux:        val("mux") == "1",
		brutalUp:   atoi(val("brutal_up")),
		brutalDown: atoi(val("brutal_down")),
	}
}
