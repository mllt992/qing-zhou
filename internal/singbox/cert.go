package singbox

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

// leafCert parses the first CERTIFICATE block of a PEM bundle. In a fullchain
// that is the leaf — the certificate the server actually presents, and the only
// one a fingerprint may be taken from.
func leafCert(certPEM string) *x509.Certificate {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return c
}

// CertFingerprintSHA256 returns the leaf certificate's SHA-256 fingerprint as
// uppercase hex — the value clients call `pinSHA256`. Empty when the PEM does
// not parse.
//
// This is the hash of the whole DER certificate, not of its public key. The two
// are routinely confused and are not interchangeable: sing-box's
// `certificate_public_key_sha256` wants a base64 SPKI hash instead, and feeding
// it this value would reject every connection.
func CertFingerprintSHA256(certPEM string) string {
	c := leafCert(certPEM)
	if c == nil {
		return ""
	}
	sum := sha256.Sum256(c.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// IsSelfSignedCert reports whether the leaf is its own issuer, i.e. no public CA
// vouches for it and a client can only trust it by pinning (or by disabling
// verification altogether).
//
// Asking the certificate rather than the `source` column is deliberate: it also
// classifies the legacy inline-PEM profiles that predate the certificate centre,
// and it cannot drift from reality the way a stored label can.
func IsSelfSignedCert(certPEM string) bool {
	c := leafCert(certPEM)
	if c == nil {
		return false
	}
	if c.Issuer.String() != c.Subject.String() {
		return false
	}
	// A matching subject/issuer is not proof on its own — verify the signature is
	// actually the leaf's own, so a cert merely *claiming* to be self-issued is
	// not mistaken for one.
	//
	// CheckSignature, not CheckSignatureFrom: the latter additionally insists the
	// signer be a CA, and a self-signed *server* certificate is by construction
	// not one — it would reject every certificate this function exists to detect.
	return c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil
}

// GenerateSelfSignedCert issues a self-signed ECDSA (P-256) certificate for the
// given server name and returns the certificate and private key as PEM strings.
//
// It is intended for the "证书 TLS" mode with protocols whose clients accept a
// self-signed cert (TUIC / Hysteria2 with insecure or certificate pinning). The
// SAN is set from serverName: an IP literal becomes an IP SAN, otherwise a DNS
// SAN (plus its bare apex when serverName is a wildcard). validityDays is
// clamped to [1, 3650]; 0 means the default of 3650 (≈10 years).
func GenerateSelfSignedCert(serverName string, validityDays int) (certPEM, keyPEM string, err error) {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return "", "", fmt.Errorf("server name is required")
	}
	if validityDays <= 0 {
		validityDays = 3650
	}
	if validityDays > 3650 {
		validityDays = 3650
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}

	// Serial: a random 128-bit positive integer.
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("generate serial: %w", err)
	}

	notBefore := time.Now().Add(-1 * time.Hour) // tolerate minor clock skew
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: serverName},
		NotBefore:             notBefore,
		NotAfter:              notBefore.Add(time.Duration(validityDays) * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(serverName); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{serverName}
		// For a wildcard SNI, also cover the bare apex so both match.
		if strings.HasPrefix(serverName, "*.") {
			tmpl.DNSNames = append(tmpl.DNSNames, strings.TrimPrefix(serverName, "*."))
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("marshal key: %w", err)
	}

	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM, nil
}

// trustAnchorMinValidity is how long a self-signed certificate must still be
// valid for 轻舟 to hand it to clients as a trust anchor. A subscription is
// cached client-side; embedding a certificate that expires while cached would
// make sing-box reject a node that `insecure` used to accept.
const trustAnchorMinValidity = 30 * 24 * time.Hour

// SelfSignedTrustAnchor returns the self-signed leaf as base64url DER (no
// padding) when a client that trusts exactly this certificate — sing-box's
// `tls.certificate` — would accept it for serverName right now and for the next
// 30 days. Empty otherwise (CA-issued, unparseable, SAN-less/CN-only, name not
// covered, expired or expiring), in which case callers keep today's behavior.
//
// This is deliberately a real x509 verification rather than a set of field
// checks: it is the same decision the client will make, so a certificate that
// passes here cannot turn a working `insecure` node into a failing one.
func SelfSignedTrustAnchor(certPEM, serverName string, now time.Time) string {
	if !IsSelfSignedCert(certPEM) {
		return ""
	}
	c := leafCert(certPEM)
	serverName = strings.TrimSpace(serverName)
	if len(serverName) > 1 && serverName[0] == '[' && serverName[len(serverName)-1] == ']' {
		serverName = serverName[1 : len(serverName)-1]
	}
	if c == nil || serverName == "" {
		return ""
	}
	roots := x509.NewCertPool()
	roots.AddCert(c)
	for _, at := range []time.Time{now, now.Add(trustAnchorMinValidity)} {
		if _, err := c.Verify(x509.VerifyOptions{Roots: roots, DNSName: serverName, CurrentTime: at}); err != nil {
			return ""
		}
	}
	return base64.RawURLEncoding.EncodeToString(c.Raw)
}

// TrustAnchorPEM decodes a base64url DER certificate (the qz-cert link
// extension) back to PEM. Empty when it is not a parseable certificate.
func TrustAnchorPEM(b64 string) string {
	der, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(b64), "="))
	if err != nil {
		return ""
	}
	if _, err := x509.ParseCertificate(der); err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// NormalizeCertPin returns a SHA-256 certificate fingerprint as uppercase hex
// without separators, or "" when it is not exactly 32 bytes of hex. mihomo
// rejects the whole proxy (and so the whole config) on a malformed
// `fingerprint`, so an unvalidated value from a third-party link must never
// reach it.
func NormalizeCertPin(v string) string {
	v = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(v), ":", ""))
	b, err := hex.DecodeString(v)
	if err != nil || len(b) != sha256.Size {
		return ""
	}
	return v
}

// ParseLeafCert is the exported form of leafCert: the first CERTIFICATE block
// of a PEM bundle, or nil when it does not parse.
func ParseLeafCert(certPEM string) *x509.Certificate {
	return leafCert(certPEM)
}

// CertNames lists the names a leaf certificate is valid for, as a client
// verifying it sees them: DNS SANs, then IP SANs. The subject CN is deliberately
// absent — Go clients (mihomo, sing-box, Xray) have ignored it since Go 1.15, so
// listing it would promise a match the client will refuse.
func CertNames(certPEM string) []string {
	c := leafCert(certPEM)
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.DNSNames)+len(c.IPAddresses))
	out = append(out, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

// CertCoversHost reports whether a client that verifies the certificate against
// host (an IP literal, bracketed or not, or a DNS name) would accept it — the
// check a plain HTTPS-proxy client makes, since it cannot send any other SNI.
// known is false when the PEM does not parse or host is empty, so callers can
// avoid warning about something they could not actually check.
func CertCoversHost(certPEM, host string) (covered, known bool) {
	host = strings.TrimSpace(host)
	if len(host) > 1 && host[0] == '[' && host[len(host)-1] == ']' {
		host = host[1 : len(host)-1]
	}
	c := leafCert(certPEM)
	if c == nil || host == "" {
		return false, false
	}
	return c.VerifyHostname(host) == nil, true
}
