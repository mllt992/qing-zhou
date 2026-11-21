package api

import (
	"testing"

	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
)

// #116: a pasted certificate that is actually self-signed (e.g. one shared
// across nodes) must be allowed to tell clients to skip verification; the
// decision follows the certificate, not its source label.
func TestManagedCertClientInsecure(t *testing.T) {
	self, _, err := singbox.GenerateSelfSignedCert("ssl.mllt.vip", 30)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"paste", "selfsigned", "acme"} {
		c := &store.Cert{Source: src, CertPEM: self}
		if !managedCertClientInsecure(c, true) {
			t.Errorf("self-signed (%s) must honor insecure=true", src)
		}
		if managedCertClientInsecure(c, false) {
			t.Errorf("self-signed (%s) must honor insecure=false", src)
		}
	}
	if managedCertClientInsecure(&store.Cert{Source: "paste", CertPEM: "not a cert"}, true) {
		t.Error("an unreadable/CA certificate must never skip verification")
	}
	if managedCertClientInsecure(&store.Cert{Source: "selfsigned", CertPEM: self, DecryptFailed: true}, true) {
		t.Error("a certificate that failed to decrypt must never skip verification")
	}
	if managedCertClientInsecure(nil, true) {
		t.Error("nil certificate must never skip verification")
	}
}
