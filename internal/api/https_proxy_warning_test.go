package api

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
)

func TestHTTPSProxyHostWarning(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert("ssl.mllt.vip", 30)
	if err != nil {
		t.Fatal(err)
	}
	cid, err := st.SaveCert(&store.Cert{Name: "shared", Domain: "ssl.mllt.vip", Source: "paste", CertPEM: certPEM, KeyPEM: keyPEM})
	if err != nil {
		t.Fatal(err)
	}
	tid, err := st.SaveSbTls(&store.SbTls{Name: "shared", Mode: "tls", CertID: cid, ServerJSON: `{"enabled":true,"server_name":"ssl.mllt.vip"}`, ClientJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	ib := &store.SbInbound{Type: "mixed", Tag: "px", Listen: "::", ListenPort: 7648, TlsID: tid, Options: "{}", Enabled: true}
	info := st.InboundTLSInfo(ib)
	if info == nil || !info.Known {
		t.Fatal("managed certificate must be inspectable")
	}
	warn := httpsProxyHostWarning(info, []string{"217.216.188.164", "2402:1920:fc:af::2"})
	for _, want := range []string{"ssl.mllt.vip", "217.216.188.164", "2402:1920:fc:af::2"} {
		if !strings.Contains(warn, want) {
			t.Fatalf("warning %q lacks %q", warn, want)
		}
	}
	if w := httpsProxyHostWarning(info, []string{"ssl.mllt.vip", ""}); w != "" {
		t.Fatalf("covered host must not warn: %q", w)
	}
	if w := httpsProxyHostWarning(nil, []string{"1.2.3.4"}); w != "" {
		t.Fatal("unknown certificate must never warn")
	}
}

// #120: the inbound TLS summary follows the effective state — inline
// options.tls counts, a disabled profile does not.
func TestInboundTLSInfoFollowsEffectiveState(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert("ssl.mllt.vip", 30)
	if err != nil {
		t.Fatal(err)
	}
	opts, _ := json.Marshal(map[string]any{"tls": map[string]any{"enabled": true, "server_name": "ssl.mllt.vip", "certificate": certPEM, "key": keyPEM}})
	inline := &store.SbInbound{Type: "mixed", Tag: "px", ListenPort: 7648, Options: string(opts), Enabled: true}
	info := st.InboundTLSInfo(inline)
	if info == nil || !info.Known || info.SNI != "ssl.mllt.vip" || !info.SelfSigned {
		t.Fatalf("inline TLS must be described: %+v", info)
	}
	if httpsProxyHostWarning(info, []string{"217.216.188.164"}) == "" {
		t.Fatal("inline TLS certificate not covering the host must warn")
	}
	if st.InboundTLSInfo(&store.SbInbound{Type: "mixed", Tag: "px2", Options: `{"tls":{"enabled":false}}`}) != nil {
		t.Fatal("inline enabled:false is plaintext")
	}
	sj, _ := json.Marshal(map[string]any{"enabled": false, "certificate": certPEM, "key": keyPEM})
	tid, err := st.SaveSbTls(&store.SbTls{Name: "off", Mode: "tls", ServerJSON: string(sj), ClientJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if st.InboundTLSInfo(&store.SbInbound{Type: "mixed", Tag: "px3", TlsID: tid, Options: "{}"}) != nil {
		t.Fatal("disabled TLS profile is plaintext")
	}
}
