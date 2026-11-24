package subconv

import (
	"qingzhou/internal/singbox"
)

// applySingboxECH populates outbound tls.ech for plain-TLS nodes (never REALITY
// or plaintext) from the link's `ech` param (bare base64 ECHConfigList or
// Xray's "domain+DNS" form). Coexists cleanly with self-signed cert pinning.
func applySingboxECH(p *Proxy, tls map[string]any) {
	cfg, qsn := singbox.ParseECHLinkParam(p.tlsParam("ech"))
	if cfg == "" && qsn == "" {
		return
	}
	ech := map[string]any{"enabled": true}
	if cfg != "" {
		ech["config"] = singbox.ECHConfigPEMLines(cfg)
	} else {
		ech["query_server_name"] = qsn
	}
	tls["ech"] = ech
}

// applyClashECH populates mihomo's `ech-opts` for plain-TLS proxies, carrying
// the bare base64 ECHConfigList without PEM armour lines (or query-server-name
// when the link uses Xray's domain+DNS form).
func applyClashECH(p *Proxy, m map[string]any) {
	if !clashPinnable(p, m) {
		return
	}
	cfg, qsn := singbox.ParseECHLinkParam(p.tlsParam("ech"))
	if cfg == "" && qsn == "" {
		return
	}
	opts := map[string]any{"enable": true}
	if cfg != "" {
		opts["config"] = cfg
	} else {
		opts["query-server-name"] = qsn
	}
	m["ech-opts"] = opts
}

// clashECHParam extracts an ECH link parameter from a Clash/mihomo proxy's
// `ech-opts` block for round-trip import into share links.
func clashECHParam(m map[string]any) string {
	opts, ok := m["ech-opts"].(map[string]any)
	if !ok || !cbool(opts["enable"]) {
		return ""
	}
	if cfg := singbox.ECHConfigBase64(opts["config"]); cfg != "" {
		return cfg
	}
	if qsn := str(opts["query-server-name"]); qsn != "" {
		if valid, err := singbox.ValidateECHPublicName(qsn); err == nil && valid != "" {
			return valid + "+"
		}
	}
	return ""
}
