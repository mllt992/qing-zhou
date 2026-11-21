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
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestCertFingerprintMatchesLeafDER(t *testing.T) {
	certPEM, _, err := GenerateSelfSignedCert("node.example.com", 30)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	got := CertFingerprintSHA256(certPEM)

	block, _ := pem.Decode([]byte(certPEM))
	sum := sha256.Sum256(block.Bytes)
	want := strings.ToUpper(hex.EncodeToString(sum[:]))
	if got != want {
		t.Errorf("fingerprint = %q, want %q", got, want)
	}
	if len(got) != 64 {
		t.Errorf("fingerprint length = %d, want 64 hex chars", len(got))
	}
	// The value goes into a URI query; anything outside uppercase hex would mean
	// clients see a percent-escaped pin they cannot compare.
	if strings.Trim(got, "0123456789ABCDEF") != "" {
		t.Errorf("fingerprint is not plain uppercase hex: %q", got)
	}
}

func TestSelfSignedDetection(t *testing.T) {
	certPEM, _, err := GenerateSelfSignedCert("node.example.com", 30)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !IsSelfSignedCert(certPEM) {
		t.Error("self-signed certificate not recognised as such")
	}
}

// A CA-issued leaf must never be pinned: it rotates on renewal, and a
// subscription cached across one would pin a certificate that no longer exists.
func TestCAIssuedCertIsNotSelfSigned(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	ca, _ := x509.ParseCertificate(caDER)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "leaf.example.com"},
		DNSNames:     []string{"leaf.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("sign leaf: %v", err)
	}
	issued := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	if IsSelfSignedCert(issued) {
		t.Error("CA-issued certificate was misclassified as self-signed")
	}
	// The fingerprint helper still works on it — the guard against pinning a
	// rotating certificate is the self-signed test, not a parse failure.
	if CertFingerprintSHA256(issued) == "" {
		t.Error("fingerprint of a valid CA-issued cert should still compute")
	}
}

func TestCertHelpersRejectGarbage(t *testing.T) {
	for _, in := range []string{"", "not a pem", "-----BEGIN CERTIFICATE-----\nzzzz\n-----END CERTIFICATE-----\n"} {
		if got := CertFingerprintSHA256(in); got != "" {
			t.Errorf("CertFingerprintSHA256(%q) = %q, want empty", in, got)
		}
		if IsSelfSignedCert(in) {
			t.Errorf("IsSelfSignedCert(%q) = true, want false", in)
		}
	}
}

// A self-signed cert must reach hysteria links as the standard pinSHA256 (other
// protocols carry it as qz-pin; see TestPinReachesEveryTLSProtocol).
func TestSelfSignedPinReachesHysteriaLinks(t *testing.T) {
	certPEM, _, err := GenerateSelfSignedCert("node.example.com", 30)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	pin := CertFingerprintSHA256(certPEM)

	hy2 := BuildShareLink(LinkParams{
		Type: "hysteria2", Tag: "n", Host: "1.2.3.4", Port: 443,
		Password: "pw", TLS: true, SNI: "node.example.com",
		Insecure: true, PinSHA256: pin,
	})
	if !strings.Contains(hy2, "pinSHA256="+pin) {
		t.Errorf("hysteria2 link missing pin: %s", hy2)
	}
	// Additive only: the existing insecure flag must survive, or a client that
	// ignores the pin loses its only way to accept the certificate.
	if !strings.Contains(hy2, "insecure=1") {
		t.Errorf("hysteria2 link dropped insecure=1: %s", hy2)
	}

	hy1 := BuildShareLink(LinkParams{
		Type: "hysteria", Tag: "n", Host: "1.2.3.4", Port: 443,
		Password: "pw", TLS: true, SNI: "node.example.com", PinSHA256: pin,
	})
	if !strings.Contains(hy1, "pinSHA256="+pin) {
		t.Errorf("hysteria link missing pin: %s", hy1)
	}
}

// No pin, no parameter — a CA-issued node's link must be byte-identical to what
// it was before pinning existed.
func TestNoPinNoParam(t *testing.T) {
	link := BuildShareLink(LinkParams{
		Type: "hysteria2", Tag: "n", Host: "1.2.3.4", Port: 443,
		Password: "pw", TLS: true, SNI: "node.example.com",
	})
	if strings.Contains(link, "pinSHA256") {
		t.Errorf("unpinned link carries a pin parameter: %s", link)
	}
}
