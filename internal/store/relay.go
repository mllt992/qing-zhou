package store

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"qingzhou/internal/singbox"
)

// Relay chaining lets an inbound (线路机/relay) forward its traffic to another
// inbound (落地机/landing) instead of exiting to the internet directly:
// 客户端 → 线路机入站 → [upstream outbound] → 落地机入站 → 互联网.
//
// A relay inbound serves many users but its single upstream outbound presents
// one identity, so per-user accounting stays at the relay entry. That identity
// is a dedicated relay credential derived from the landing inbound's own
// relay_secret; the same secret is used to inject a matching user into the
// landing inbound's users[], so relay and landing always agree without the admin
// hand-configuring a tunnel.

// relayCred derives the deterministic relay credential from a landing inbound's
// relay_secret: a UUID (vless/vmess/tuic) and a password (the rest). Both the
// relay's upstream outbound and the injected landing user derive from the same
// secret, so they always match.
func relayCred(secret string) (uuid, password string) {
	h := sha256.Sum256([]byte("qz-relay:" + secret))
	uuid = formatUUID(h[:16])
	password = hex.EncodeToString(h[16:32])
	return
}

// formatUUID renders 16 bytes as a canonical UUID string.
func formatUUID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ensureRelaySecret returns the landing inbound's relay_secret, lazily
// generating and persisting a random one the first time a relay needs it.
func (s *Store) ensureRelaySecret(ib *SbInbound) (string, error) {
	if ib.RelaySecret != "" {
		return ib.RelaySecret, nil
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	sec := hex.EncodeToString(raw)
	if _, err := s.db.Exec(`UPDATE sb_inbounds SET relay_secret=? WHERE id=?`, sec, ib.ID); err != nil {
		return "", err
	}
	ib.RelaySecret = sec
	return sec, nil
}

// mergeRelayUser appends the landing inbound's relay user (if this tag is a
// relay target) to its entitled user list, so the relay can authenticate.
func mergeRelayUser(users []singbox.User, landingUsers map[string][]singbox.User, tag string) []singbox.User {
	if ru, ok := landingUsers[tag]; ok {
		return append(append([]singbox.User(nil), users...), ru...)
	}
	return users
}

// buildRelayWiring computes, for the inbounds being built on one server:
//   - relays: the upstream outbounds + route rules for this server's relay
//     inbounds (those with a non-zero, valid UpstreamInboundID);
//   - landingUsers: the relay user to inject (by inbound tag) into this server's
//     landing inbounds that are targeted by a relay anywhere.
//
// allInbounds spans every server so a relay can target a landing on a different
// machine. Relay inbounds whose upstream is missing/disabled or whose landing
// protocol has no outbound renderer are skipped (their traffic falls through to
// route.final), never failing the whole build.
func (s *Store) buildRelayWiring(serverInbounds, allInbounds []*SbInbound, usersByTag map[string][]singbox.User) ([]singbox.Relay, map[string][]singbox.User, error) {
	byID := make(map[int64]*SbInbound, len(allInbounds))
	byTag := make(map[string]*SbInbound, len(allInbounds))
	targeted := map[int64]bool{}
	for _, ib := range allInbounds {
		byID[ib.ID] = ib
		byTag[ib.Tag] = ib
		if ib.Enabled && ib.UpstreamInboundID != 0 {
			targeted[ib.UpstreamInboundID] = true
		}
	}
	nodes, _ := s.ListNodes()
	for _, n := range nodes {
		if n.Enabled && n.Type == "self_built" && n.RouteUpstreamInboundID != 0 && !n.RouteUpstreamBroken {
			targeted[n.RouteUpstreamInboundID] = true
		}
	}

	// Relay users to inject into this server's targeted landing inbounds.
	landingUsers := map[string][]singbox.User{}
	for _, ib := range serverInbounds {
		if !ib.Enabled || !targeted[ib.ID] {
			continue
		}
		compat, err := s.LegacyRelayCompatibility(ib.ServerID, ib.ID)
		if err != nil {
			return nil, nil, err
		}
		if compat == "retiring" || compat == "retired" {
			continue
		}
		sec, err := s.ensureRelaySecret(ib)
		if err != nil {
			return nil, nil, err
		}
		uuid, pw := relayCred(sec)
		statsName, err := s.legacyRelayStatsName(ib.ServerID, ib.ID)
		if err != nil {
			return nil, nil, err
		}
		landingUsers[ib.Tag] = []singbox.User{{Name: statsName, UUID: uuid, Password: pw, Relay: true}}
	}

	metered := s.RelayMeteringEnabled()
	{ // Keep prepared/active credentials accepted during opt-out as well.
		links, err := s.relayMeteringAcceptedGenerations()
		if err != nil {
			return nil, nil, err
		}
		for _, link := range links {
			for _, ib := range serverInbounds {
				if ib.Enabled && ib.ID == link.TargetInboundID && ib.ServerID == link.TargetServerID {
					u, err := s.meteringRelayUser(link)
					if err != nil {
						return nil, nil, err
					}
					landingUsers[ib.Tag] = append(landingUsers[ib.Tag], u)
				}
			}
		}
	}
	userLandings, err := s.relayUserLandingUsers()
	if err != nil {
		return nil, nil, err
	}
	for _, ib := range serverInbounds {
		if ib.Enabled {
			landingUsers[ib.Tag] = append(landingUsers[ib.Tag], userLandings[ib.ID]...)
		}
	}
	perUser := s.RelayUserMeteringEnabled()

	// Upstream outbounds for this server's relay inbounds, grouped by landing so
	// several relay inbounds pointing at the same landing share one outbound.
	serverCache := map[int64]*Server{}
	tlsCache := map[int64]*SbTls{}
	serverTags := map[string]bool{}
	for _, ib := range serverInbounds {
		serverTags[ib.Tag] = true
	}
	// Logical routes come first: route rules are first-match, so the auth_user
	// branches must run before a legacy rule that steers the entire inbound.
	relays := make([]singbox.Relay, 0)
	logicalOutbound := map[int64]map[string]interface{}{}
	for _, n := range nodes {
		if !n.Enabled || n.Type != "self_built" || n.RouteUpstreamInboundID == 0 || n.RouteUpstreamBroken || !serverTags[n.InboundTag] {
			continue
		}
		entry, landing := byTag[n.InboundTag], byID[n.RouteUpstreamInboundID]
		if entry == nil || !entry.Enabled || !s.serverEnabled(serverCache, entry.ServerID) || landing == nil || !landing.Enabled || !s.serverEnabled(serverCache, landing.ServerID) {
			continue
		}
		auth := make([]string, 0)
		routeUsers := make([]singbox.User, 0)
		for _, u := range usersByTag[entry.Tag] {
			if isRouteIdentityFor(u.Name, n.ID) {
				auth = append(auth, u.Name)
				routeUsers = append(routeUsers, u)
			}
		}
		if len(auth) == 0 {
			continue
		}
		sort.Strings(auth)
		if perUser {
			userRelays, err := s.meteredUserRelays(entry, n.ID, landing, routeUsers, serverCache, tlsCache)
			if err != nil {
				return nil, nil, err
			}
			relays = append(relays, userRelays...)
			continue
		}
		cacheID := landing.ID
		if metered {
			cacheID = n.ID
		}
		ob := logicalOutbound[cacheID]
		if ob == nil {
			var err error
			if metered {
				ob, err = s.meteredRelayOutbound(entry, n.ID, landing, serverCache, tlsCache)
			} else {
				ob, err = s.relayOutbound(landing, serverCache, tlsCache)
			}
			if metered && err != nil {
				return nil, nil, err
			}
			if err != nil || ob == nil {
				continue
			}
			logicalOutbound[cacheID] = ob
		}
		relays = append(relays, singbox.Relay{Outbound: ob, InboundTags: []string{entry.Tag}, AuthUsers: auth})
	}

	byLanding := map[int64]*singbox.Relay{}
	var order []int64
	for _, r := range serverInbounds {
		if !r.Enabled || r.UpstreamInboundID == 0 {
			continue
		}
		landing := byID[r.UpstreamInboundID]
		if landing == nil || !landing.Enabled || !s.serverEnabled(serverCache, landing.ServerID) {
			continue // dangling/disabled upstream — traffic falls through to final
		}
		if perUser {
			var logicalIDs []int64
			for _, n := range nodes {
				if n.Enabled && n.Type == "self_built" && n.InboundTag == r.Tag && n.RouteUpstreamInboundID != 0 && !n.RouteUpstreamBroken {
					logicalIDs = append(logicalIDs, n.ID)
				}
			}
			var sourceUsers []singbox.User
			for _, user := range mergeRelayUser(usersByTag[r.Tag], landingUsers, r.Tag) {
				if !user.Relay && matchesLogicalRelay(user.Name, logicalIDs) {
					continue
				}
				sourceUsers = append(sourceUsers, user)
			}
			userRelays, err := s.meteredUserRelays(r, 0, landing, sourceUsers, serverCache, tlsCache)
			if err != nil {
				return nil, nil, err
			}
			relays = append(relays, userRelays...)
		}
		cacheID := landing.ID
		if metered {
			cacheID = r.ID
		}
		if existing, ok := byLanding[cacheID]; ok {
			existing.InboundTags = append(existing.InboundTags, r.Tag)
			continue
		}
		var ob map[string]interface{}
		var err error
		if metered {
			ob, err = s.meteredRelayOutbound(r, 0, landing, serverCache, tlsCache)
		} else {
			ob, err = s.relayOutbound(landing, serverCache, tlsCache)
		}
		if metered && err != nil {
			return nil, nil, err
		}
		if err != nil || ob == nil {
			continue // unsupported landing protocol / bad config — skip this relay
		}
		byLanding[cacheID] = &singbox.Relay{Outbound: ob, InboundTags: []string{r.Tag}}
		order = append(order, cacheID)
	}
	for _, id := range order {
		relays = append(relays, *byLanding[id])
	}

	// Third-party proxy egresses: inbounds with EgressID exit through a purchased
	// SOCKS5/HTTP proxy (e.g. a static IP). Same wiring shape as a relay — one
	// outbound per egress shared by all its inbounds, plus a route rule. A
	// dangling egress id is skipped (traffic falls through to route.final); the
	// admin API refuses to delete an egress still in use, so that shouldn't occur.
	egCache := map[int64]*SbEgress{}
	byEgress := map[int64]*singbox.Relay{}
	var egOrder []int64
	for _, r := range serverInbounds {
		if !r.Enabled || r.EgressID == 0 {
			continue
		}
		if existing, ok := byEgress[r.EgressID]; ok {
			existing.InboundTags = append(existing.InboundTags, r.Tag)
			continue
		}
		eg, ok := egCache[r.EgressID]
		if !ok {
			eg, _ = s.GetSbEgress(r.EgressID)
			egCache[r.EgressID] = eg
		}
		if eg == nil {
			continue
		}
		// A TLS egress may pin a managed certificate as its trust anchor. Resolved
		// here (once per egress — the byEgress check above short-circuits repeats)
		// rather than inside egressOutbound, which stays a pure renderer.
		var trustPEM string
		if eg.TLSEnabled && eg.Type == "http" && eg.TLSCertID != 0 {
			if c, _ := s.GetCert(eg.TLSCertID); c != nil && !c.DecryptFailed {
				trustPEM = c.CertPEM
			}
		}
		byEgress[r.EgressID] = &singbox.Relay{
			Outbound:    egressOutbound(eg, trustPEM),
			InboundTags: []string{r.Tag},
			RejectUDP:   eg.EffectiveUDPMode() == UDPModeBlock,
		}
		egOrder = append(egOrder, r.EgressID)
	}
	for _, id := range egOrder {
		relays = append(relays, *byEgress[id])
	}
	return relays, landingUsers, nil
}

// egressOutbound renders a proxy egress as a sing-box socks/http outbound. On a
// DecryptFailed egress the password is empty, so traffic fails closed at the
// proxy instead of silently exiting with the wrong (direct) IP.
//
// trustPEM, when non-empty, is the certificate the proxy is verified against
// instead of the system roots (see SbEgress.TLSCertID). An egress that asked for
// an anchor but whose cert has gone missing or undecryptable arrives here with
// trustPEM empty: the tls block is still emitted, so the handshake fails against
// the system roots rather than the hop silently reverting to plaintext and
// handing the proxy credentials to the network.
func egressOutbound(e *SbEgress, trustPEM string) map[string]interface{} {
	ob := map[string]interface{}{
		"type":        e.Type,
		"tag":         fmt.Sprintf("egress-%d", e.ID),
		"server":      e.Host,
		"server_port": e.Port,
		// Bound the TCP connect to the proxy. When a provider drops an account
		// (quota spent, IP no longer whitelisted) the port usually goes silent
		// rather than refusing, and with no timeout every connection then sits on
		// the OS retry schedule — minutes, during which the user's tab neither
		// loads nor errors. Seconds instead at least lets the client retry and
		// makes the outage legible.
		//
		// Scope, precisely: this is a DialerOptions field, so it covers reaching
		// the proxy's port. A proxy that completes the TCP handshake and then
		// stalls mid-SOCKS5/CONNECT is not covered by it — nothing in a sing-box
		// outbound bounds that, and the concurrency probe is what surfaces it.
		"connect_timeout": fmt.Sprintf("%dms", e.EffectiveConnectTimeoutMS()),
	}
	if e.Type == "socks" {
		ob["version"] = "5"
	}
	if e.Username != "" {
		ob["username"] = e.Username
	}
	if e.Password != "" || e.DecryptFailed {
		ob["password"] = e.Password
	}
	// Guarded on http: sing-box's socks outbound has no tls option, so emitting
	// one there would be silently ignored — a config that looks encrypted and
	// isn't. The admin API rejects the combination; this is the second gate.
	if e.TLSEnabled && e.Type == "http" {
		t := map[string]interface{}{"enabled": true}
		if e.SNI != "" {
			t["server_name"] = e.SNI
		}
		if trustPEM != "" {
			t["certificate"] = trustPEM
		}
		if e.TLSInsecure {
			t["insecure"] = true
		}
		ob["tls"] = t
	}
	return ob
}

// relayOutbound builds a native sing-box outbound for a managed landing.
// Share links are deliberately not an intermediate representation: they cannot
// encode every native transport, TLS trust option, or protocol setting.
func (s *Store) relayOutbound(landing *SbInbound, serverCache map[int64]*Server, tlsCache map[int64]*SbTls) (map[string]interface{}, error) {
	return s.relayOutboundWithIdentity(landing, serverCache, tlsCache, nil, fmt.Sprintf("relay-to-%d", landing.ID))
}

// With an explicit identity and populated caches this is a pure renderer. In
// particular it must not initialize the legacy relay secret: acknowledgements
// render the expected outbound from a transaction snapshot without database IO.
func (s *Store) relayOutboundWithIdentity(landing *SbInbound, serverCache map[int64]*Server, tlsCache map[int64]*SbTls, identity *singbox.User, outboundTag string) (map[string]interface{}, error) {
	host := "127.0.0.1"
	if landing.ServerID != 0 {
		sv, ok := serverCache[landing.ServerID]
		if !ok {
			var err error
			sv, err = s.GetServer(landing.ServerID)
			if err != nil {
				return nil, err
			}
			serverCache[landing.ServerID] = sv
		}
		if sv != nil && sv.Host != "" {
			host = sv.Host
		}
	}

	var uuid, password string
	if identity != nil {
		uuid, password = identity.UUID, identity.Password
	} else {
		secret, err := s.ensureRelaySecret(landing)
		if err != nil {
			return nil, err
		}
		uuid, password = relayCred(secret)
	}

	var opts, serverTLS, clientTLS map[string]interface{}
	if landing.Options != "" {
		if err := json.Unmarshal([]byte(landing.Options), &opts); err != nil {
			return nil, fmt.Errorf("relay: invalid options for landing inbound %d: %w", landing.ID, err)
		}
	}
	// The profile overrides inline TLS, exactly as the inbound builder does.
	serverTLS, _ = opts["tls"].(map[string]interface{})
	if landing.TlsID != 0 {
		t, ok := tlsCache[landing.TlsID]
		if !ok {
			// Resolve managed certificate bytes and SNI for ordinary builds too.
			// The acknowledgement path supplies this already-resolved cache.
			var err error
			_, _, t, err = s.relayTargetSpec(landing)
			if err != nil {
				return nil, err
			}
			tlsCache[landing.TlsID] = t
		}
		if t == nil || t.DecryptFailed {
			return nil, fmt.Errorf("relay: TLS profile %d for landing inbound %d is unavailable", landing.TlsID, landing.ID)
		}
		if t.ServerJSON != "" {
			var profileTLS map[string]interface{}
			if err := json.Unmarshal([]byte(t.ServerJSON), &profileTLS); err != nil {
				return nil, fmt.Errorf("relay: invalid server TLS for landing inbound %d: %w", landing.ID, err)
			}
			if profileTLS != nil {
				serverTLS = profileTLS
			}
		}
		if t.ClientJSON != "" {
			if err := json.Unmarshal([]byte(t.ClientJSON), &clientTLS); err != nil {
				return nil, fmt.Errorf("relay: invalid client TLS for landing inbound %d: %w", landing.ID, err)
			}
		}
		if serverTLS == nil {
			return nil, fmt.Errorf("relay: TLS profile %d for landing inbound %d has no effective server TLS", landing.TlsID, landing.ID)
		}
	}
	requiresTLS := landing.Type == "tuic" || landing.Type == "hysteria" || landing.Type == "hysteria2" || landing.Type == "anytls"
	if requiresTLS && !mapBool(serverTLS, "enabled") {
		return nil, fmt.Errorf("relay: %s landing inbound %d requires enabled server TLS", landing.Type, landing.ID)
	}
	ob := map[string]interface{}{
		"type": landing.Type, "tag": outboundTag, "server": host, "server_port": landing.ListenPort,
	}
	switch landing.Type {
	case "vless":
		ob["uuid"], ob["packet_encoding"] = uuid, "xudp"
	case "vmess":
		ob["uuid"], ob["security"], ob["alter_id"] = uuid, "auto", 0
	case "trojan", "anytls":
		ob["password"] = password
	case "tuic":
		ob["uuid"], ob["password"], ob["udp_relay_mode"] = uuid, password, "native"
		relayCopyFields(ob, opts, "congestion_control", "zero_rtt_handshake", "heartbeat")
	case "hysteria":
		ob["auth_str"] = password
		relayCopyFields(ob, opts, "obfs")
		// Rates describe the local endpoint: the relay uploads what the landing
		// receives, and downloads what the landing sends. Preserve the native
		// string rates too; sing-box gives up/down precedence over *_mbps.
		for from, to := range map[string]string{"up": "down", "up_mbps": "down_mbps", "down": "up", "down_mbps": "up_mbps"} {
			if v, ok := opts[from]; ok {
				ob[to] = v
			}
		}
	case "hysteria2":
		ob["password"] = password
		relayCopyFields(ob, opts, "obfs", "bbr_profile", "disable_chrome_parrot", "hop_interval", "hop_interval_max")
		// A bandwidth-enforcing landing rejects the default BBR client (Rx=0).
		// Mirror known endpoint rates only when the server requires them. If
		// its transmit rate is unlimited, use its known receive rate as the
		// conservative relay download budget instead of inventing a faster one.
		if mapBool(opts, "ignore_client_bandwidth") && mapInt(opts, "down_mbps") > 0 {
			up, down := mapInt(opts, "down_mbps"), mapInt(opts, "up_mbps")
			if down <= 0 {
				down = up
			}
			ob["up_mbps"], ob["down_mbps"] = up, down
		}
	case "shadowsocks":
		method := mapStr(opts, "method")
		ob["method"] = method
		ob["password"] = mapStr(opts, "password") + ":" + singbox.DeriveSSKey(password, method)
		// Without multiplex, native payloads need the corresponding listener
		// network. A TCP listener with mux can carry both TCP and UDP payloads.
		mx, _ := opts["multiplex"].(map[string]interface{})
		if !mapBool(mx, "enabled") || !relayNetworkIncludes(opts["network"], "tcp") {
			relayCopyFields(ob, opts, "network")
		}
	default:
		return nil, fmt.Errorf("relay: unsupported landing protocol %q", landing.Type)
	}

	switch landing.Type {
	case "vless", "vmess", "trojan":
		if tr, ok := opts["transport"].(map[string]interface{}); ok && len(tr) > 0 {
			// Native inbound and outbound transport schemas are identical. Keep
			// HTTP host/header lists, WS early data, and gRPC tuning intact.
			switch mapStr(tr, "type") {
			case "http", "ws", "quic", "grpc", "httpupgrade":
				ob["transport"] = tr
			default:
				return nil, fmt.Errorf("relay: unsupported transport %q for landing inbound %d", mapStr(tr, "type"), landing.ID)
			}
		}
	}
	switch landing.Type {
	case "vless", "vmess", "trojan", "shadowsocks":
		relayCopyFields(ob, opts, "tcp_fast_open", "tcp_multi_path")
		if mx, ok := opts["multiplex"].(map[string]interface{}); ok && mapBool(mx, "enabled") && (landing.Type != "shadowsocks" || relayNetworkIncludes(opts["network"], "tcp")) {
			outMux := map[string]interface{}{"enabled": true}
			relayCopyFields(outMux, mx, "padding")
			// Brutal rates configured for subscribers do not describe the relay
			// machine. Mirroring those would flood or throttle the managed hop.
			ob["multiplex"] = outMux
		}
	case "tuic", "hysteria", "hysteria2":
		relayCopyFields(ob, opts, "idle_timeout", "keep_alive_period", "stream_receive_window", "connection_receive_window", "max_concurrent_streams", "initial_packet_size", "disable_path_mtu_discovery")
	}

	if landing.Type != "shadowsocks" && (landing.TlsID != 0 || serverTLS != nil) {
		transport, _ := opts["transport"].(map[string]interface{})
		quic := landing.Type == "tuic" || landing.Type == "hysteria" || landing.Type == "hysteria2" || mapStr(transport, "type") == "quic"
		tls, err := relayClientTLS(serverTLS, clientTLS, landing.Type, quic)
		if err != nil {
			return nil, fmt.Errorf("relay: landing inbound %d: %w", landing.ID, err)
		}
		if requiresTLS && !mapBool(tls, "enabled") {
			return nil, fmt.Errorf("relay: %s landing inbound %d requires enabled client TLS", landing.Type, landing.ID)
		}
		ob["tls"] = tls
	}
	if landing.Type == "vless" {
		flowSpec := map[string]interface{}{"flow": opts["flow"], "transport": opts["transport"]}
		if _, hasTLS := ob["tls"]; hasTLS {
			flowSpec["tls"] = true
		}
		if flow := singbox.VLESSUserFlow(flowSpec); flow != "" {
			ob["flow"] = flow
			delete(ob, "multiplex")
		}
	}
	return ob, nil
}

func relayCopyFields(dst, src map[string]interface{}, keys ...string) {
	for _, key := range keys {
		if value, ok := src[key]; ok {
			dst[key] = value
		}
	}
}

// relayClientTLS selects the dial-side fields without ever copying the
// landing's private key, certificate-provider config or client-auth policy.
// Explicit client settings take precedence, including certificate/pin trust,
// SNI overrides and disabled uTLS. A profile is decoded afresh for each render,
// so neither this map nor its nested values alias the supplied caches.
func relayClientTLS(server, client map[string]interface{}, protocol string, quic bool) (map[string]interface{}, error) {
	if quic {
		// QUIC needs the native Go TLS config at dial time. REALITY and
		// platform TLS engines cannot provide it; reject those combinations.
		// The shared profile's TCP-only uTLS fingerprint is omitted below.
		serverReality, _ := server["reality"].(map[string]interface{})
		clientReality, _ := client["reality"].(map[string]interface{})
		if mapBool(serverReality, "enabled") || mapBool(clientReality, "enabled") {
			return nil, fmt.Errorf("QUIC relay does not support REALITY")
		}
		if engine := mapStr(client, "engine"); engine != "" && engine != "go" {
			return nil, fmt.Errorf("QUIC relay requires the Go TLS engine, got %q", engine)
		}
	}
	tls := map[string]interface{}{"enabled": true}
	relayCopyFields(tls, server, "enabled", "server_name", "alpn", "min_version", "max_version", "cipher_suites", "curve_preferences", "handshake_timeout")
	if !quic && (protocol == "vless" || protocol == "vmess" || protocol == "trojan" || protocol == "anytls") {
		tls["utls"] = map[string]interface{}{"enabled": true, "fingerprint": "chrome"}
	}
	relayCopyFields(tls, client,
		"enabled", "engine", "disable_sni", "server_name", "insecure", "alpn", "min_version", "max_version",
		"cipher_suites", "curve_preferences", "certificate", "certificate_path", "certificate_public_key_sha256",
		"client_certificate", "client_certificate_path", "client_key", "client_key_path", "fragment", "fragment_fallback_delay",
		"record_fragment", "spoof", "spoof_method", "kernel_tx", "kernel_rx", "handshake_timeout", "ech", "utls", "reality")
	if quic {
		// TLS profiles are shared with TCP protocols and normally include a
		// default browser fingerprint. uTLS is TCP-only, not a QUIC security
		// setting: retain certificate verification, SNI, ALPN and other native
		// TLS parameters while using Go TLS for this hop.
		delete(tls, "utls")
	}
	// Managed self-signed certificates can be trusted exactly, without the
	// client having to disable verification. Never replace explicit trust
	// settings, or pin a public-CA certificate that can rotate independently.
	_, hasCertificate := client["certificate"]
	_, hasCertificatePath := client["certificate_path"]
	_, hasPin := client["certificate_public_key_sha256"]
	if !mapBool(tls, "insecure") && !hasCertificate && !hasCertificatePath && !hasPin {
		pem := relayCertificatePEM(server["certificate"])
		if singbox.IsSelfSignedCert(pem) {
			tls["certificate"] = pem
		}
	}
	if reality, ok := server["reality"].(map[string]interface{}); ok && mapBool(reality, "enabled") {
		r, _ := tls["reality"].(map[string]interface{})
		if r == nil {
			r = map[string]interface{}{}
		}
		r["enabled"] = true
		if mapStr(r, "public_key") == "" {
			// Inline REALITY profiles need no second stored copy of the public
			// key. Derive it locally; only the public half leaves this renderer.
			key, err := base64.RawURLEncoding.DecodeString(mapStr(reality, "private_key"))
			if err != nil {
				return nil, fmt.Errorf("invalid REALITY private key: %w", err)
			}
			private, err := ecdh.X25519().NewPrivateKey(key)
			if err != nil {
				return nil, fmt.Errorf("invalid REALITY private key: %w", err)
			}
			r["public_key"] = base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes())
		}
		if _, ok := r["short_id"]; !ok {
			if sid, ok := reality["short_id"].(string); ok {
				r["short_id"] = sid
			} else {
				r["short_id"] = firstShortID(reality["short_id"])
			}
		}
		tls["reality"] = r
		if _, ok := tls["utls"]; !ok {
			tls["utls"] = map[string]interface{}{"enabled": true, "fingerprint": "chrome"}
		}
	}
	return tls, nil
}

// TLS certificate is a Listable[string] in the native schema.
func relayCertificatePEM(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case []string:
		return strings.Join(v, "\n")
	case []interface{}:
		var lines []string
		for _, line := range v {
			if text, ok := line.(string); ok {
				lines = append(lines, text)
			}
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

// NetworkList accepts a string or a list; absent/empty means both networks.
func relayNetworkIncludes(value interface{}, network string) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == "" || v == network
	case []string:
		if len(v) == 0 {
			return true
		}
		for _, item := range v {
			if item == network {
				return true
			}
		}
	case []interface{}:
		if len(v) == 0 {
			return true
		}
		for _, item := range v {
			if item == network {
				return true
			}
		}
	}
	return false
}
