package singbox

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// legacyCert issues a self-signed cert with the given CN and DNS SANs (none =
// CN-only, the pre-Go-1.15 style).
func legacyCert(t *testing.T, cn string, dns ...string) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: cn}, DNSNames: dns,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestValidateCertForName(t *testing.T) {
	cnOnly := legacyCert(t, "legacy.example")
	if err := ValidateCertForName(cnOnly, ""); err == nil || !strings.Contains(err.Error(), "SAN") {
		t.Fatalf("CN-only certificate must be rejected with a SAN message, got %v", err)
	}
	good, _, _ := genTestCert(t, "ssl.mllt.vip")
	if err := ValidateCertForName(good, "ssl.mllt.vip"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCertForName(good, "217.216.188.164"); err == nil || !strings.Contains(err.Error(), "ssl.mllt.vip") {
		t.Fatalf("uncovered name must be rejected naming the SANs, got %v", err)
	}
	if err := ValidateCertForName(good, ""); err != nil {
		t.Fatalf("no name: only SAN presence is checked, got %v", err)
	}
	wild, _, _ := genTestCert(t, "*.mllt.vip")
	for _, n := range []string{"*.mllt.vip", "a.mllt.vip", "mllt.vip"} {
		if err := ValidateCertForName(wild, n); err != nil {
			t.Errorf("wildcard cert must cover %s: %v", n, err)
		}
	}
	if ValidateCertForName("junk", "") == nil {
		t.Fatal("garbage must fail")
	}
}

func TestDefaultCertDomain(t *testing.T) {
	if d := DefaultCertDomain(legacyCert(t, "cn.example", "san.example")); d != "san.example" {
		t.Fatalf("CN not in SANs: want first SAN, got %q", d)
	}
	if d := DefaultCertDomain(legacyCert(t, "b.example", "a.example", "b.example")); d != "b.example" {
		t.Fatalf("CN in SANs: want CN, got %q", d)
	}
	if d := DefaultCertDomain(legacyCert(t, "only.example")); d != "only.example" {
		t.Fatalf("SAN-less: falls back to CN, got %q", d)
	}
}

func TestEffectiveServerName(t *testing.T) {
	wild, _, _ := genTestCert(t, "*.mllt.vip")
	if got := EffectiveServerName("node1.mllt.vip", "*.mllt.vip", wild); got != "node1.mllt.vip" {
		t.Fatalf("concrete covered name must be kept, got %q", got)
	}
	if got := EffectiveServerName("other.example", "*.mllt.vip", wild); got != "*.mllt.vip" {
		t.Fatalf("uncovered name falls back to the cert domain, got %q", got)
	}
	if got := EffectiveServerName("*.mllt.vip", "*.mllt.vip", wild); got != "*.mllt.vip" {
		t.Fatalf("pattern stays the cert domain, got %q", got)
	}
	single, _, _ := genTestCert(t, "ssl.mllt.vip")
	if got := EffectiveServerName("ssl.mllt.vip", "ssl.mllt.vip", single); got != "ssl.mllt.vip" {
		t.Fatalf("existing data (stored == domain) unchanged, got %q", got)
	}
	if got := EffectiveServerName("", "ssl.mllt.vip", "junk"); got != "ssl.mllt.vip" {
		t.Fatalf("unreadable PEM keeps the old rule, got %q", got)
	}
}

func genTestCert(t *testing.T, name string) (string, string, error) {
	t.Helper()
	c, k, err := GenerateSelfSignedCert(name, 30)
	if err != nil {
		t.Fatal(err)
	}
	return c, k, nil
}
