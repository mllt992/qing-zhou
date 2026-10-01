package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"qingzhou/internal/singbox"
)

func newCertTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	st.SetSecretKey([]byte("test-secret"))
	return st
}

func inboundTLS(cfg map[string]any, tag string) map[string]any {
	ibs, _ := cfg["inbounds"].([]any)
	for _, x := range ibs {
		m, ok := x.(map[string]any)
		if ok && m["tag"] == tag {
			tls, _ := m["tls"].(map[string]any)
			return tls
		}
	}
	return nil
}

// TestCertStoreAndInjection covers the managed-certificate lifecycle: encrypted
// at rest, referenced by a TLS profile via cert_id, injected into the built
// sing-box config, and protected against deletion while referenced.
func TestCertStoreAndInjection(t *testing.T) {
	st := newCertTestStore(t)

	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert("cert.example.com", 30)
	if err != nil {
		t.Fatal(err)
	}
	certID, err := st.SaveCert(&Cert{
		Name: "le-cert", Domain: "cert.example.com", Source: "acme",
		AcmeMethod: "dns-cf", AcmeProfile: "auto", CertPEM: certPEM, KeyPEM: keyPEM,
		ActualKeyType: "ECDSA-P-256", ActualChain: "cert.example.com",
		LastVerifyAt: 123, AutoRenew: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// PEM must be encrypted at rest.
	var rawCert string
	if err := st.db.QueryRow(`SELECT cert_pem FROM certificates WHERE id=?`, certID).Scan(&rawCert); err != nil {
		t.Fatal(err)
	}
	if rawCert == certPEM {
		t.Error("cert_pem stored in cleartext")
	}
	// GetCert returns decrypted bytes and a parsed expiry.
	c, err := st.GetCert(certID)
	if err != nil || c == nil {
		t.Fatalf("GetCert: %v", err)
	}
	if c.CertPEM != certPEM || c.KeyPEM != keyPEM {
		t.Error("GetCert did not return decrypted PEM")
	}
	if c.NotAfter == 0 {
		t.Error("not_after was not parsed from cert_pem")
	}
	if c.AcmeProfile != "auto" || c.ActualKeyType != "ECDSA-P-256" || c.ActualChain != "cert.example.com" || c.LastVerifyAt != 123 {
		t.Errorf("certificate policy metadata did not round-trip: %+v", c)
	}

	// A TLS profile referencing the cert carries no inline PEM.
	tlsID, err := st.SaveSbTls(&SbTls{
		Name: "tls-via-cert", Mode: "tls", CertID: certID,
		ServerJSON: `{"enabled":true,"server_name":"placeholder"}`,
		ClientJSON: `{"insecure":false}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbID, err := st.SaveSbInbound(&SbInbound{
		Type: "trojan", Tag: "trojan-cert", ListenPort: 443,
		Options: `{}`, Enabled: true, TlsID: tlsID,
	})
	if err != nil {
		t.Fatal(err)
	}

	users := map[string][]singbox.User{"trojan-cert": {{Name: "u1", UUID: "11111111-1111-1111-1111-111111111111"}}}
	cfgBytes, err := st.BuildSingboxConfig(singbox.DefaultBaseConfig, "", users)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		t.Fatal(err)
	}
	tls := inboundTLS(cfg, "trojan-cert")
	if tls == nil {
		t.Fatalf("built inbound has no tls block:\n%s", cfgBytes)
	}
	if tls["certificate"] != certPEM || tls["key"] != keyPEM {
		t.Error("cert PEM was not injected into the tls block")
	}
	if tls["server_name"] != "cert.example.com" {
		t.Errorf("server_name = %v, want the cert domain", tls["server_name"])
	}

	// Deleting a referenced cert must be refused; after the profile is gone it works.
	if err := st.DeleteCert(certID); err == nil {
		t.Error("DeleteCert should refuse while a TLS profile references it")
	}
	if _, err := st.DeleteSbInbound(inbID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSbTls(tlsID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCert(certID); err != nil {
		t.Errorf("DeleteCert after unreference: %v", err)
	}
}

func TestMigrateCertificatePolicyPreservesLegacyRows(t *testing.T) {
	st := newCertTestStore(t)
	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert("legacy-policy.example.com", 30)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.SaveCert(&Cert{Name: "legacy", Domain: "legacy-policy.example.com", Source: "acme", CertPEM: certPEM, KeyPEM: keyPEM})
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"acme_profile", "actual_key_type", "actual_chain", "last_verify_at", "verify_error"} {
		if _, err := st.db.Exec(`ALTER TABLE certificates DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	rewindVersionedBaseline(t, st)
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	c, err := st.GetCert(id)
	if err != nil || c == nil {
		t.Fatalf("GetCert after migration: %v", err)
	}
	if c.AcmeProfile != "" || c.ActualKeyType != "" || c.ActualChain != "" || c.LastVerifyAt != 0 || c.VerifyError != "" {
		t.Fatalf("legacy row was assigned a new policy during migration: %+v", c)
	}
}

// TestBackfillCertsIdempotent verifies that a legacy inline-PEM TLS profile is
// extracted into a certificates row exactly once, and re-running is a no-op.
func TestBackfillCertsIdempotent(t *testing.T) {
	st := newCertTestStore(t)

	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert("legacy.example.com", 30)
	if err != nil {
		t.Fatal(err)
	}
	sj, _ := json.Marshal(map[string]any{
		"enabled": true, "server_name": "legacy.example.com",
		"certificate": certPEM, "key": keyPEM,
	})
	tlsID, err := st.SaveSbTls(&SbTls{Name: "legacy-tls", Mode: "tls", ServerJSON: string(sj)})
	if err != nil {
		t.Fatal(err)
	}

	if err := st.backfillCerts(); err != nil {
		t.Fatal(err)
	}
	certs, _ := st.ListCerts()
	if len(certs) != 1 {
		t.Fatalf("expected 1 extracted cert, got %d", len(certs))
	}
	profile, _ := st.GetSbTls(tlsID)
	if profile.CertID == 0 {
		t.Error("profile was not repointed to the extracted cert")
	}
	if certs[0].Domain != "legacy.example.com" || certs[0].CertPEM != certPEM {
		t.Errorf("extracted cert malformed: %+v", certs[0])
	}

	// Second run must not create a duplicate.
	if err := st.backfillCerts(); err != nil {
		t.Fatal(err)
	}
	if certs2, _ := st.ListCerts(); len(certs2) != 1 {
		t.Fatalf("backfill not idempotent: %d certs after second run", len(certs2))
	}
}
