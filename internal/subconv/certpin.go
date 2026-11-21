package subconv

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"qingzhou/internal/singbox"
)

// certPin is the node's pinned leaf-certificate SHA-256 (uppercase hex), or ""
// when the link carries none or a malformed one. Hysteria-family links use the
// standard pinSHA256; 轻舟 carries the same value as qz-pin elsewhere (and as a
// vmess JSON key). Validated here because mihomo rejects the whole config over
// one malformed `fingerprint`, and third-party links reach this code too.
func (p *Proxy) certPin() string {
	return singbox.NormalizeCertPin(p.tlsParam("qz-pin", "pinSHA256"))
}

// pinnedCertPEM is the self-signed trust anchor carried by qz-cert, as PEM, or
// "" when absent, unparseable, or contradicting the advertised pin. 轻舟 only
// emits it for certificates that verify for the link's server name (see
// singbox.SelfSignedTrustAnchor); a third-party link carrying one can only
// constrain trust in its own node.
func (p *Proxy) pinnedCertPEM() string {
	raw := strings.TrimRight(strings.TrimSpace(p.tlsParam("qz-cert")), "=")
	pem := singbox.TrustAnchorPEM(raw)
	if pem == "" {
		return ""
	}
	if pin := p.certPin(); pin != "" {
		der, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || !strings.EqualFold(pin, sha256Hex(der)) {
			return ""
		}
	}
	return pem
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// clashPinnable reports whether a rendered mihomo proxy talks plain TLS, i.e.
// a certificate fingerprint is meaningful for it (not REALITY, not plaintext).
func clashPinnable(p *Proxy, m map[string]any) bool {
	switch p.Protocol {
	case "trojan", "hysteria", "hysteria2", "tuic", "anytls":
		return true
	case "vless":
		return m["tls"] == true && p.param("security") == "tls"
	case "vmess":
		return m["tls"] == true
	}
	return false
}

// applyClashCertPin swaps "skip verification" for pinning the exact
// certificate (#116, #85): with `fingerprint` set mihomo accepts the leaf whose
// SHA-256 matches and nothing else, so skip-cert-verify is both unnecessary and
// misleading next to it.
func applyClashCertPin(p *Proxy, m map[string]any) {
	pin := p.certPin()
	if pin == "" || !clashPinnable(p, m) {
		return
	}
	m["fingerprint"] = pin
	delete(m, "skip-cert-verify")
}
