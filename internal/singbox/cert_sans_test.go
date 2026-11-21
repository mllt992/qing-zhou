package singbox

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func parseTestLeaf(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// #117: one certificate valid for the node's IPv4, IPv6 and domain at once.
func TestGenerateSelfSignedCertSANs(t *testing.T) {
	certPEM, keyPEM, err := GenerateSelfSignedCertSANs([]string{"217.216.188.164", "[2402:1920:fc:af::2]", " node.example.com ", "*.mllt.vip", "217.216.188.164", ""}, 30)
	if err != nil || keyPEM == "" {
		t.Fatal(err)
	}
	c := parseTestLeaf(t, certPEM)
	if c.Subject.CommonName != "217.216.188.164" {
		t.Errorf("CN = %q, want the first name", c.Subject.CommonName)
	}
	for _, host := range []string{"217.216.188.164", "2402:1920:fc:af::2", "node.example.com", "a.mllt.vip", "mllt.vip"} {
		if err := c.VerifyHostname(host); err != nil {
			t.Errorf("%s not covered: %v", host, err)
		}
	}
	if len(c.IPAddresses) != 2 || len(c.DNSNames) != 3 {
		t.Errorf("ips=%v dns=%v (duplicates must collapse)", c.IPAddresses, c.DNSNames)
	}
	if !IsSelfSignedCert(certPEM) {
		t.Error("must be self-signed")
	}
	if _, _, err := GenerateSelfSignedCertSANs([]string{"", "  "}, 30); err == nil {
		t.Error("no usable name must be an error")
	}
}

// The single-name wrapper keeps its exact old behavior.
func TestGenerateSelfSignedCertSingleName(t *testing.T) {
	certPEM, _, err := GenerateSelfSignedCert("ssl.mllt.vip", 30)
	if err != nil {
		t.Fatal(err)
	}
	c := parseTestLeaf(t, certPEM)
	if len(c.DNSNames) != 1 || c.DNSNames[0] != "ssl.mllt.vip" || len(c.IPAddresses) != 0 {
		t.Fatalf("dns=%v ips=%v", c.DNSNames, c.IPAddresses)
	}
	if _, _, err := GenerateSelfSignedCert("", 30); err == nil {
		t.Fatal("empty name must fail")
	}
}
