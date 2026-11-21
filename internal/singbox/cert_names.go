package singbox

import (
	"fmt"
	"strings"
)

// unbracketHost strips a matched [] pair (IPv6 literal written URL-style).
func unbracketHost(h string) string {
	h = strings.TrimSpace(h)
	if len(h) > 1 && h[0] == '[' && h[len(h)-1] == ']' {
		return h[1 : len(h)-1]
	}
	return h
}

// leafSANs lists DNS then IP SANs of the leaf.
func leafSANs(certPEM string) []string {
	c := leafCert(certPEM)
	if c == nil {
		return nil
	}
	out := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

// CertMatchesName reports whether a Go client verifying the leaf for name (an
// IP literal or DNS name, a wildcard pattern matching itself) accepts it.
// False for an unparseable PEM or an empty name.
func CertMatchesName(certPEM, name string) bool {
	c := leafCert(certPEM)
	name = unbracketHost(name)
	return c != nil && name != "" && c.VerifyHostname(name) == nil
}

// ValidateCertForName is the save-time gate for a newly supplied certificate
// (#119): it must carry SANs at all — Go 1.15+ clients (mihomo, sing-box,
// Xray) ignore the subject CN — and, when name is given, cover it. Errors are
// user-facing Chinese messages. Never applied to certificates already stored,
// so existing nodes keep working exactly as before.
func ValidateCertForName(certPEM, name string) error {
	c := leafCert(certPEM)
	if c == nil {
		return fmt.Errorf("证书解析失败")
	}
	sans := leafSANs(certPEM)
	if len(sans) == 0 {
		return fmt.Errorf("证书没有 SAN（只有 CN=%s）：mihomo、sing-box、Xray 等 Go 客户端自 Go 1.15 起不认 CN，会拒绝连接。请重新签发带 subjectAltName 的证书", c.Subject.CommonName)
	}
	if name = unbracketHost(name); name != "" && c.VerifyHostname(name) != nil {
		return fmt.Errorf("证书 SAN（%s）不包含 %s，客户端校验会失败。请换用覆盖该名称的证书，或把 SNI / 域名改成证书里的名称", strings.Join(sans, "、"), name)
	}
	return nil
}

// DefaultCertDomain picks the name a certificate should be filed under when
// the admin gave none: the CN when the certificate's SANs cover it, else the
// first DNS SAN, else the first IP SAN, else the CN (SAN-less legacy cert).
// The old rule — always the CN — produced an SNI the certificate's SANs did
// not contain whenever the CN was not repeated there.
func DefaultCertDomain(certPEM string) string {
	c := leafCert(certPEM)
	if c == nil {
		return ""
	}
	cn := strings.TrimSpace(c.Subject.CommonName)
	if cn != "" && c.VerifyHostname(cn) == nil {
		return cn
	}
	if len(c.DNSNames) > 0 {
		return c.DNSNames[0]
	}
	if len(c.IPAddresses) > 0 {
		return c.IPAddresses[0].String()
	}
	return cn
}

// EffectiveServerName is the SNI for a TLS profile that references a managed
// certificate: the profile's own server name when the certificate covers it
// (e.g. a concrete host under a wildcard certificate), otherwise the
// certificate's domain — never a literal "*.example" pattern when a concrete
// covered name is available. Shared by the node config builder, the relay and
// subscription links so all three agree (#119, #121).
func EffectiveServerName(stored, certDomain, certPEM string) string {
	stored = strings.TrimSpace(stored)
	if stored != "" && !strings.HasPrefix(stored, "*.") && CertMatchesName(certPEM, stored) {
		return stored
	}
	if certDomain != "" {
		return certDomain
	}
	return stored
}
