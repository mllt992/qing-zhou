package api

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
	"qingzhou/internal/tlsprobe"
)

func serveTestTLS(t *testing.T, certPEM, keyPEM string) int {
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
	return ln.Addr().(*net.TCPAddr).Port
}

func runTLSCheck(t *testing.T, a *API, id int64) inboundTLSCheckResult {
	t.Helper()
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/", nil), "id", strconv.FormatInt(id, 10))
	w := httptest.NewRecorder()
	a.handleAdminInboundTLSCheck(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Data inboundTLSCheckResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Data
}

// #118: the HTTPS proxy from the bug report — TCP-reachable, TLS handshake
// fine, certificate issued only for a shared domain — gets a TLS error, while
// QUIC and non-TLS inbounds are skipped rather than failed.
func TestInboundTLSCheck(t *testing.T) {
	a, st := newUserEditAPI(t)
	certPEM, keyPEM, _ := singbox.GenerateSelfSignedCert("ssl.mllt.vip", 3650)
	port := serveTestTLS(t, certPEM, keyPEM)
	sid, err := st.CreateServer(store.Server{Name: "n", Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	cid, _ := st.SaveCert(&store.Cert{Name: "c", Domain: "ssl.mllt.vip", Source: "paste", CertPEM: certPEM, KeyPEM: keyPEM})
	tid, _ := st.SaveSbTls(&store.SbTls{Name: "c", Mode: "tls", CertID: cid, ServerJSON: `{"enabled":true,"server_name":"ssl.mllt.vip"}`, ClientJSON: `{}`})
	mixed, err := st.SaveSbInbound(&store.SbInbound{ServerID: sid, Type: "mixed", Tag: "px", Listen: "::", ListenPort: port, TlsID: tid, Options: "{}", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	res := runTLSCheck(t, a, mixed)
	if res.Status != tlsprobe.StatusError || len(res.Results) != 1 || !res.Results[0].Handshake {
		t.Fatalf("mixed with shared domain cert by IP must be a TLS error: %+v", res)
	}
	if pm := res.Results[0].PinMatch; pm == nil || !*pm {
		t.Fatalf("presented certificate equals the managed one: %+v", res.Results[0])
	}

	trojan, _ := st.SaveSbInbound(&store.SbInbound{ServerID: sid, Type: "trojan", Tag: "tj", Listen: "::", ListenPort: port + 0, TlsID: tid, Options: "{}", Enabled: true})
	if res := runTLSCheck(t, a, trojan); res.Status != tlsprobe.StatusOK {
		t.Fatalf("SNI-carrying protocol with matching SNI must be ok: %+v", res)
	}
	tuic, _ := st.SaveSbInbound(&store.SbInbound{ServerID: sid, Type: "tuic", Tag: "tu", Listen: "::", ListenPort: 9443, TlsID: tid, Options: "{}", Enabled: true})
	if res := runTLSCheck(t, a, tuic); res.Status != tlsprobe.StatusSkipped {
		t.Fatalf("QUIC must be skipped: %+v", res)
	}
	plain, _ := st.SaveSbInbound(&store.SbInbound{ServerID: sid, Type: "mixed", Tag: "plain", Listen: "::", ListenPort: 7890, Options: "{}", Enabled: true})
	if res := runTLSCheck(t, a, plain); res.Status != tlsprobe.StatusSkipped {
		t.Fatalf("non-TLS must be skipped: %+v", res)
	}
}

// #120 × #118: an inbound with inline options.tls is a TLS inbound for the
// probe too (same rule as subscriptions and the user page); inline
// enabled:false stays "no TLS".
func TestInboundTLSCheckInlineTLS(t *testing.T) {
	a, st := newUserEditAPI(t)
	certPEM, keyPEM, _ := singbox.GenerateSelfSignedCert("127.0.0.1", 3650)
	port := serveTestTLS(t, certPEM, keyPEM)
	sid, err := st.CreateServer(store.Server{Name: "n", Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	opts, _ := json.Marshal(map[string]any{"tls": map[string]any{"enabled": true, "certificate": certPEM, "key": keyPEM}})
	id, err := st.SaveSbInbound(&store.SbInbound{ServerID: sid, Type: "mixed", Tag: "inl", Listen: "::", ListenPort: port, Options: string(opts), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	res := runTLSCheck(t, a, id)
	if res.Status != tlsprobe.StatusWarning || len(res.Results) != 1 || !res.Results[0].Handshake || !res.Results[0].SelfSigned {
		t.Fatalf("inline self-signed HTTPS proxy must be probed and warned: %+v", res)
	}
	off, _ := st.SaveSbInbound(&store.SbInbound{ServerID: sid, Type: "mixed", Tag: "off", Listen: "::", ListenPort: 7891, Options: `{"tls":{"enabled":false}}`, Enabled: true})
	if res := runTLSCheck(t, a, off); res.Status != tlsprobe.StatusSkipped {
		t.Fatalf("inline enabled:false must be skipped: %+v", res)
	}
}
