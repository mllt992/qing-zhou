package store

import (
	"encoding/json"
	"testing"

	"qingzhou/internal/singbox"
)

// bindMixedTLS attaches a TLS profile to the scene's mixed inbound.
func bindMixedTLS(t *testing.T, sc *proxyScene, tls *SbTls) {
	t.Helper()
	id, err := sc.st.SaveSbTls(tls)
	if err != nil {
		t.Fatal(err)
	}
	ibs, err := sc.st.ListSbInbounds()
	if err != nil {
		t.Fatal(err)
	}
	for _, ib := range ibs {
		if ib.Tag == "mixed-proxy" {
			ib.TlsID = id
			if _, err := sc.st.SaveSbInbound(ib); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("mixed-proxy inbound missing")
}

func inlineTLS(t *testing.T, certPEM, keyPEM, sni string, insecure bool) *SbTls {
	t.Helper()
	sj, _ := json.Marshal(map[string]interface{}{"enabled": true, "server_name": sni, "certificate": certPEM, "key": keyPEM})
	cj, _ := json.Marshal(map[string]interface{}{"insecure": insecure})
	return &SbTls{Name: "t", Mode: "tls", ServerJSON: string(sj), ClientJSON: string(cj)}
}

// The reported case (#115): a certificate shared by every node, issued only for
// a domain, served on a node users reach by address. The proxy URL cannot carry
// an SNI, so the page must say the certificate does not cover that address and
// what it IS issued for, plus the pin of the self-signed certificate.
func TestUserProxyReportsSharedDomainCertMismatch(t *testing.T) {
	sc := newProxyScene(t)
	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert("ssl.mllt.vip", 30)
	if err != nil {
		t.Fatal(err)
	}
	bindMixedTLS(t, sc, inlineTLS(t, certPEM, keyPEM, "ssl.mllt.vip", true))

	p := sc.proxy(t) // advertised host: example.com
	if !p.TLS {
		t.Fatal("TLS-bound mixed inbound must be an HTTPS proxy")
	}
	if p.SNI != "ssl.mllt.vip" || p.CertDomain != "ssl.mllt.vip" {
		t.Fatalf("sni=%q cert_domain=%q, want ssl.mllt.vip", p.SNI, p.CertDomain)
	}
	if !p.SelfSigned || p.CertSHA256 != singbox.CertFingerprintSHA256(certPEM) {
		t.Fatalf("self_signed=%v sha256=%q", p.SelfSigned, p.CertSHA256)
	}
	if !p.Insecure {
		t.Fatal("profile insecure flag must be reported")
	}
	if !p.HostMismatch {
		t.Fatal("certificate for ssl.mllt.vip does not cover example.com; must be flagged")
	}
	if len(p.CertNames) != 1 || p.CertNames[0] != "ssl.mllt.vip" {
		t.Fatalf("cert_names=%v", p.CertNames)
	}
}

func TestUserProxyNoMismatchWhenCertCoversHost(t *testing.T) {
	sc := newProxyScene(t)
	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert("example.com", 30)
	if err != nil {
		t.Fatal(err)
	}
	bindMixedTLS(t, sc, inlineTLS(t, certPEM, keyPEM, "example.com", false))
	if p := sc.proxy(t); p.HostMismatch || !p.SelfSigned {
		t.Fatalf("host_mismatch=%v self_signed=%v", p.HostMismatch, p.SelfSigned)
	}
}

// A managed (certificate-centre) certificate is resolved exactly as the node
// config resolves it: the cert row supplies the PEM and its domain.
func TestUserProxyManagedCertResolved(t *testing.T) {
	sc := newProxyScene(t)
	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert("example.com", 30)
	if err != nil {
		t.Fatal(err)
	}
	cid, err := sc.st.SaveCert(&Cert{Name: "c", Domain: "example.com", Source: "selfsigned", CertPEM: certPEM, KeyPEM: keyPEM})
	if err != nil {
		t.Fatal(err)
	}
	bindMixedTLS(t, sc, &SbTls{Name: "m", Mode: "tls", CertID: cid,
		ServerJSON: `{"enabled":true,"server_name":"example.com"}`, ClientJSON: `{"insecure":true}`})
	p := sc.proxy(t)
	if p.CertDomain != "example.com" || !p.SelfSigned || p.CertSHA256 == "" || p.HostMismatch {
		t.Fatalf("unexpected %+v", p)
	}
}

// Plain HTTP/SOCKS5 proxies carry no TLS details at all.
func TestUserProxyPlainHasNoTLSInfo(t *testing.T) {
	sc := newProxyScene(t)
	p := sc.proxy(t)
	if p.TLS || p.SNI != "" || p.SelfSigned || p.HostMismatch || p.CertSHA256 != "" {
		t.Fatalf("plain proxy carries TLS info: %+v", p)
	}
}

func TestCertCoversHost(t *testing.T) {
	certPEM, _, err := singbox.GenerateSelfSignedCert("203.0.113.7", 30)
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]bool{"203.0.113.7": true, "[203.0.113.7]": true, "198.51.100.1": false, "ssl.mllt.vip": false} {
		got, known := singbox.CertCoversHost(certPEM, host)
		if !known || got != want {
			t.Errorf("CertCoversHost(%q)=%v,%v want %v", host, got, known, want)
		}
	}
	if _, known := singbox.CertCoversHost("garbage", "x"); known {
		t.Error("unparseable PEM must be unknown, never a mismatch")
	}
}
