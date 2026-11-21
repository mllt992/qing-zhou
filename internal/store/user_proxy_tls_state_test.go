package store

import (
	"encoding/json"
	"testing"

	"qingzhou/internal/singbox"
)

// setMixedOptions replaces the scene's mixed inbound options verbatim.
func setMixedOptions(t *testing.T, sc *proxyScene, options string) {
	t.Helper()
	ibs, err := sc.st.ListSbInbounds()
	if err != nil {
		t.Fatal(err)
	}
	for _, ib := range ibs {
		if ib.Tag == "mixed-proxy" {
			ib.Options = options
			if _, err := sc.st.SaveSbInbound(ib); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("mixed-proxy inbound missing")
}

func userProxies(t *testing.T, sc *proxyScene) []UserProxy {
	t.Helper()
	u, err := sc.st.UserByID(sc.uid)
	if err != nil || u == nil {
		t.Fatalf("UserByID: %v", err)
	}
	return sc.st.BuildUserProxies(u, "example.com")
}

// #120: a bound TLS profile with enabled:false makes sing-box serve plaintext,
// so the user page must not advertise an https:// proxy.
func TestUserProxyDisabledTLSProfileIsPlain(t *testing.T) {
	sc := newProxyScene(t)
	certPEM, keyPEM, _ := singbox.GenerateSelfSignedCert("example.com", 30)
	sj, _ := json.Marshal(map[string]any{"enabled": false, "server_name": "example.com", "certificate": certPEM, "key": keyPEM})
	bindMixedTLS(t, sc, &SbTls{Name: "off", Mode: "tls", ServerJSON: string(sj), ClientJSON: `{}`})
	p := sc.proxy(t)
	if p.TLS || p.SNI != "" || p.SelfSigned || p.CertSHA256 != "" {
		t.Fatalf("disabled TLS profile must be a plain proxy, got %+v", p)
	}
}

// Inline options.tls is passed to sing-box verbatim, so it is an HTTPS proxy
// and its certificate details are reported like a profile's.
func TestUserProxyInlineTLSIsHTTPS(t *testing.T) {
	sc := newProxyScene(t)
	certPEM, keyPEM, _ := singbox.GenerateSelfSignedCert("ssl.mllt.vip", 30)
	opts, _ := json.Marshal(map[string]any{"tls": map[string]any{"enabled": true, "server_name": "ssl.mllt.vip", "certificate": certPEM, "key": keyPEM}})
	setMixedOptions(t, sc, string(opts))
	p := sc.proxy(t)
	if !p.TLS {
		t.Fatal("inline TLS mixed inbound must be an HTTPS proxy")
	}
	if p.SNI != "ssl.mllt.vip" || !p.SelfSigned || p.CertSHA256 != singbox.CertFingerprintSHA256(certPEM) || !p.HostMismatch {
		t.Fatalf("inline TLS details not reported: %+v", p)
	}
}

func TestUserProxyInlineTLSDisabledIsPlain(t *testing.T) {
	sc := newProxyScene(t)
	setMixedOptions(t, sc, `{"tls":{"enabled":false}}`)
	if p := sc.proxy(t); p.TLS || p.SNI != "" {
		t.Fatalf("inline enabled:false must be plain, got %+v", p)
	}
}

// An inline block that cannot be classified (not an object / no boolean
// enabled) or unparsable options: never guess — the proxy is not advertised.
func TestUserProxyInvalidInlineTLSHidden(t *testing.T) {
	for _, opts := range []string{`{"tls":"yes"}`, `{"tls":{"server_name":"x"}}`, `{not json`} {
		sc := newProxyScene(t)
		setMixedOptions(t, sc, opts)
		if list := userProxies(t, sc); len(list) != 0 {
			t.Errorf("options %s: proxy must not be advertised, got %+v", opts, list)
		}
	}
}
