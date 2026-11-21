package singbox

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
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
// It is GenerateSelfSignedCertSANs with a single name.
func GenerateSelfSignedCert(serverName string, validityDays int) (certPEM, keyPEM string, err error) {
	return GenerateSelfSignedCertSANs([]string{serverName}, validityDays)
}

// GenerateSelfSignedCertSANs issues a self-signed ECDSA (P-256) certificate
// valid for every name given (#117): each IP literal (IPv4 or IPv6, bracketed
// or not) becomes an IP SAN, anything else a DNS SAN (a wildcard also covers
// its bare apex). Duplicates and blanks are dropped; the first name is the CN.
// A node reached over both IPv4 and IPv6 — or by IP and by domain — needs all
// of them, because a client with no SNI override (every HTTPS-proxy client)
// verifies against the address it dials. validityDays is clamped to [1, 3650];
// 0 means the default of 3650 (≈10 years).
func GenerateSelfSignedCertSANs(names []string, validityDays int) (certPEM, keyPEM string, err error) {
	var dns []string
	var ips []net.IP
	seen := map[string]bool{}
	cn := ""
	add := func(n string) {
		key := strings.ToLower(n)
		if n == "" || seen[key] {
			return
		}
		seen[key] = true
		if ip := net.ParseIP(n); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, n)
		}
	}
	for _, raw := range names {
		n := strings.TrimSpace(raw)
		if len(n) > 1 && n[0] == '[' && n[len(n)-1] == ']' {
			n = n[1 : len(n)-1]
		}
		if n == "" {
			continue
		}
		if cn == "" {
			cn = n
		}
		add(n)
		if strings.HasPrefix(n, "*.") {
			add(strings.TrimPrefix(n, "*."))
		}
	}
	if cn == "" {
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
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notBefore,
		NotAfter:              notBefore.Add(time.Duration(validityDays) * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dns,
		IPAddresses:           ips,
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
