package tlsprobe

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/singbox"
)

// serveTLS runs a TLS listener presenting certPEM/keyPEM and returns its addr.
func serveTLS(t *testing.T, certPEM, keyPEM string) string {
	t.Helper()
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// A shared domain certificate on an HTTPS proxy reached by IP (or an IPv6
// address the cert never covers). The handshake succeeds and the address
// mismatch is only a warning naming the address and the certificate's names —
// never an error.
func TestProbeHTTPSProxySharedDomainCert(t *testing.T) {
	certPEM, keyPEM, _ := singbox.GenerateSelfSignedCert("ssl.mllt.vip", 3650)
	addr := serveTLS(t, certPEM, keyPEM)
	pin := singbox.CertFingerprintSHA256(certPEM)
	r := Probe(context.Background(), Target{Addr: addr, Host: "127.0.0.1", MustCoverHost: true, ExpectedSHA256: pin, SelfSigned: true}, time.Now())
	if !r.Handshake || r.Status != StatusWarning || len(r.Problems) != 0 {
		t.Fatalf("status=%s handshake=%v %+v", r.Status, r.Handshake, r)
	}
	if r.PinMatch == nil || !*r.PinMatch || r.HostCovered == nil || *r.HostCovered {
		t.Fatalf("pin/host flags wrong: %+v", r)
	}
	if !strings.Contains(strings.Join(r.Warnings, ""), "ssl.mllt.vip") {
		t.Fatalf("warning must name the certificate's names: %v", r.Warnings)
	}
	// Non-HTTPS protocols without an SNI (hy2, tuic…) do not verify the
	// address: informational note only, status stays ok.
	r = Probe(context.Background(), Target{Addr: addr, Host: "127.0.0.1", ExpectedSHA256: pin, SelfSigned: true}, time.Now())
	if r.Status != StatusOK || len(r.Notes) == 0 {
		t.Fatalf("non-HTTPS protocol must be ok with a note: %+v", r)
	}
	// Same certificate on a protocol that sends the SNI: fine, only a note.
	r = Probe(context.Background(), Target{Addr: addr, SNI: "ssl.mllt.vip", Host: "127.0.0.1", ExpectedSHA256: pin, SelfSigned: true}, time.Now())
	if r.Status != StatusOK || len(r.Notes) == 0 || r.SNICovered == nil || !*r.SNICovered {
		t.Fatalf("SNI-carrying protocol must be ok with a note: %+v", r)
	}
	// insecure clients are not broken by the name mismatch: warning only.
	r = Probe(context.Background(), Target{Addr: addr, Host: "127.0.0.1", MustCoverHost: true, Insecure: true}, time.Now())
	if r.Status != StatusWarning {
		t.Fatalf("insecure profile must degrade to warning: %+v", r)
	}
}

func TestProbePinMismatch(t *testing.T) {
	certPEM, keyPEM, _ := singbox.GenerateSelfSignedCert("127.0.0.1", 3650)
	other, _, _ := singbox.GenerateSelfSignedCert("127.0.0.1", 3650)
	addr := serveTLS(t, certPEM, keyPEM)
	r := Probe(context.Background(), Target{Addr: addr, Host: "127.0.0.1", ExpectedSHA256: singbox.CertFingerprintSHA256(other), SelfSigned: true}, time.Now())
	if r.Status != StatusError || r.PinMatch == nil || *r.PinMatch {
		t.Fatalf("self-signed pin mismatch must be an error: %+v", r)
	}
	r = Probe(context.Background(), Target{Addr: addr, Host: "127.0.0.1", ExpectedSHA256: singbox.CertFingerprintSHA256(other)}, time.Now())
	if r.Status != StatusWarning {
		t.Fatalf("CA cert mismatch (renewal lag) must only warn: %+v", r)
	}
	r = Probe(context.Background(), Target{Addr: addr, Host: "127.0.0.1", ExpectedSHA256: singbox.CertFingerprintSHA256(certPEM), SelfSigned: true}, time.Now())
	if r.Status != StatusOK {
		t.Fatalf("matching IP cert must be ok: %+v", r)
	}
}

func TestProbeExpiry(t *testing.T) {
	certPEM, keyPEM, _ := singbox.GenerateSelfSignedCert("127.0.0.1", 10)
	addr := serveTLS(t, certPEM, keyPEM)
	if r := Probe(context.Background(), Target{Addr: addr, Host: "127.0.0.1"}, time.Now()); r.Status != StatusWarning {
		t.Fatalf("expiring in 10 days must warn: %+v", r)
	}
	if r := Probe(context.Background(), Target{Addr: addr, Host: "127.0.0.1"}, time.Now().Add(20*24*time.Hour)); r.Status != StatusError {
		t.Fatalf("expired must be an error: %+v", r)
	}
}

func TestProbeHandshakeFailure(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	r := Probe(context.Background(), Target{Addr: addr, Timeout: time.Second}, time.Now())
	if r.Status != StatusError || r.Handshake {
		t.Fatalf("closed port must be an error: %+v", r)
	}
	r = Probe(context.Background(), Target{Addr: addr, Timeout: time.Second, Lenient: true}, time.Now())
	if r.Status != StatusWarning {
		t.Fatalf("lenient (IPv6 path) failure must only warn: %+v", r)
	}
}

func TestEvaluateCNOnlyCert(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "legacy.example"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	leaf, _ := x509.ParseCertificate(der)
	var r Result
	Evaluate(&r, leaf, Target{SNI: "legacy.example"}, time.Now())
	if finalStatus(r) != StatusError || !strings.Contains(strings.Join(r.Problems, ""), "SAN") {
		t.Fatalf("CN-only certificate must be an error: %+v", r)
	}
}

func TestSkippedIsNotFailure(t *testing.T) {
	if s := Skipped("x:1", "quic"); s.Status != StatusSkipped || len(s.Problems) != 0 {
		t.Fatalf("%+v", s)
	}
}
