package singbox

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestGenerateECHKeypair_RealTLS13Handshake(t *testing.T) {
	configPEM, keyPEM, err := GenerateECHKeypair("public.example.com")
	if err != nil {
		t.Fatalf("GenerateECHKeypair: %v", err)
	}
	if !strings.Contains(configPEM, "BEGIN ECH CONFIGS") {
		t.Fatalf("unexpected configPEM:\n%s", configPEM)
	}
	if !strings.Contains(keyPEM, "BEGIN ECH KEYS") {
		t.Fatalf("unexpected keyPEM:\n%s", keyPEM)
	}
	// Derive config back from keyPEM and confirm byte-identical base64 + public_name.
	derivedConfigPEM, pubName, err := ECHConfigFromKeyPEM(keyPEM)
	if err != nil {
		t.Fatalf("ECHConfigFromKeyPEM: %v", err)
	}
	if pubName != "public.example.com" {
		t.Fatalf("publicName = %q, want public.example.com", pubName)
	}
	b64 := ECHConfigBase64(configPEM)
	if b64 == "" || b64 != ECHConfigBase64(derivedConfigPEM) {
		t.Fatalf("base64 mismatch: %q vs %q", b64, ECHConfigBase64(derivedConfigPEM))
	}
	if got := ECHPublicNameFromConfig(PEMLines(configPEM)); got != "public.example.com" {
		t.Fatalf("ECHPublicNameFromConfig = %q, want public.example.com", got)
	}

	// Perform a real in-process TLS 1.3 ECH handshake with Go crypto/tls to
	// prove our generated ECHConfigList and ECHKeys are wire-valid.
	certPEM, certKeyPEM, err := GenerateSelfSignedCert("secret.inner.example", 7)
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert: %v", err)
	}
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(certKeyPEM))
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(certPEM))

	echKeys, err := ParseECHKeysPEM(keyPEM)
	if err != nil {
		t.Fatalf("ParseECHKeysPEM: %v", err)
	}
	cfgBlock, _ := pem.Decode([]byte(configPEM))
	if cfgBlock == nil {
		t.Fatal("failed to decode configPEM")
	}

	cConn, sConn := net.Pipe()
	defer cConn.Close()
	defer sConn.Close()

	srvErr := make(chan error, 1)
	srvSNI := make(chan string, 1)
	go func() {
		srv := tls.Server(sConn, &tls.Config{
			MinVersion:               tls.VersionTLS13,
			Certificates:             []tls.Certificate{cert},
			EncryptedClientHelloKeys: echKeys,
		})
		err := srv.Handshake()
		if err == nil {
			srvSNI <- srv.ConnectionState().ServerName
		}
		srvErr <- err
	}()

	cli := tls.Client(cConn, &tls.Config{
		MinVersion:                     tls.VersionTLS13,
		ServerName:                     "secret.inner.example",
		RootCAs:                        roots,
		EncryptedClientHelloConfigList: cfgBlock.Bytes,
	})
	if err := cli.Handshake(); err != nil {
		t.Fatalf("client ECH handshake failed: %v", err)
	}
	if !cli.ConnectionState().ECHAccepted {
		t.Fatal("expected ECHAccepted=true on client")
	}
	if err := <-srvErr; err != nil {
		t.Fatalf("server ECH handshake failed: %v", err)
	}
	if got := <-srvSNI; got != "secret.inner.example" {
		t.Fatalf("server saw SNI %q, want secret.inner.example", got)
	}
}

func TestParseECHLinkParam(t *testing.T) {
	configPEM, _, err := GenerateECHKeypair("public.example.com")
	if err != nil {
		t.Fatal(err)
	}
	b64 := ECHConfigBase64(configPEM)

	// Bare base64 round-trip (including space-substituted '+' from raw URL query).
	gotCfg, gotQSN := ParseECHLinkParam(strings.ReplaceAll(b64, "+", " "))
	if gotCfg != b64 || gotQSN != "" {
		t.Fatalf("base64 form: got (%q, %q), want (%q, \"\")", gotCfg, gotQSN, b64)
	}
	// Xray domain+DNS form.
	gotCfg, gotQSN = ParseECHLinkParam("cloudflare-ech.com+https://dns.example/dns-query")
	if gotCfg != "" || gotQSN != "cloudflare-ech.com" {
		t.Fatalf("domain+DNS form: got (%q, %q)", gotCfg, gotQSN)
	}
	// Garbage or structurally invalid ECHConfigList rejected (must not poison subscriptions).
	for _, bad := range []string{"", "not-base64!!!", "AAAA", "AAIAAA==", "AAb+DQAAAA==", "bad host+https://dns"} {
		if c, q := ParseECHLinkParam(bad); c != "" || q != "" {
			t.Errorf("ParseECHLinkParam(%q) = (%q, %q), want empty", bad, c, q)
		}
	}
}

func TestBuildShareLink_ECH(t *testing.T) {
	configPEM, _, err := GenerateECHKeypair("public.example.com")
	if err != nil {
		t.Fatal(err)
	}
	echB64 := ECHConfigBase64(configPEM)

	for _, typ := range []string{"vless", "trojan", "tuic", "hysteria2", "anytls", "hysteria"} {
		link := BuildShareLink(LinkParams{
			Type: typ, Tag: "n", Host: "1.2.3.4", Port: 443,
			UUID: "11111111-1111-1111-1111-111111111111", Password: "pw",
			TLS: true, SNI: "real.example.com", ECHConfig: echB64,
		})
		u, err := url.Parse(link)
		if err != nil {
			t.Fatalf("%s: url.Parse(%q): %v", typ, link, err)
		}
		if got := u.Query().Get("ech"); got != echB64 {
			t.Errorf("%s: ech query = %q, want %q (link=%s)", typ, got, echB64, link)
		}
	}

	// VMess carries ech in JSON payload.
	vmLink := BuildShareLink(LinkParams{
		Type: "vmess", Tag: "n", Host: "1.2.3.4", Port: 443,
		UUID: "11111111-1111-1111-1111-111111111111",
		TLS:  true, SNI: "real.example.com", ECHConfig: echB64,
	})
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(vmLink, "vmess://"))
	if err != nil {
		t.Fatalf("decode vmess: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal vmess: %v", err)
	}
	if obj["ech"] != echB64 {
		t.Errorf("vmess ech = %v, want %q", obj["ech"], echB64)
	}

	// Reality and plaintext must NEVER carry ech.
	reality := BuildShareLink(LinkParams{
		Type: "vless", Tag: "r", Host: "1.2.3.4", Port: 443,
		UUID:      "11111111-1111-1111-1111-111111111111",
		PublicKey: "pub", ShortID: "01", SNI: "www.microsoft.com", ECHConfig: echB64,
	})
	if strings.Contains(reality, "ech=") {
		t.Errorf("reality link leaked ech: %s", reality)
	}
	plain := BuildShareLink(LinkParams{
		Type: "vless", Tag: "p", Host: "1.2.3.4", Port: 80,
		UUID:        "11111111-1111-1111-1111-111111111111",
		TLSDisabled: true, ECHConfig: echB64,
	})
	if strings.Contains(plain, "ech=") {
		t.Errorf("plaintext link leaked ech: %s", plain)
	}
}
