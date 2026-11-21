package api

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qingzhou/internal/store"
)

// #117: the one-click self-signed certificate for an HTTPS proxy must cover the
// server's IPv4 host AND its host_v6 — an HTTPS-proxy client verifies against
// the address it dials, so the "-v6" entry otherwise always fails.
func TestQuickSelfSignedCoversHostAndHostV6(t *testing.T) {
	a, st := newUserEditAPI(t)
	sid, err := st.CreateServer(store.Server{Name: "n1", Host: "217.216.188.164", HostV6: "2402:1920:fc:af::2"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"server_id": sid, "sans": []string{"node1.example.com", "[2402:1920:fc:af::2]"}})
	w := httptest.NewRecorder()
	a.handleAdminQuickSelfSignedTls(w, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data store.SbTls `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	saved, err := st.GetSbTls(resp.Data.ID)
	if err != nil || saved == nil {
		t.Fatalf("saved TLS entry missing: %v", err)
	}
	var server map[string]any
	if err := json.Unmarshal([]byte(saved.ServerJSON), &server); err != nil {
		t.Fatal(err)
	}
	if server["server_name"] != "217.216.188.164" {
		t.Fatalf("SNI should be the primary host, got %v", server["server_name"])
	}
	certPEM, _ := server["certificate"].(string)
	for _, host := range []string{"217.216.188.164", "2402:1920:fc:af::2", "[2402:1920:fc:af::2]", "node1.example.com"} {
		if ok, known := testCertCovers(t, certPEM, host); !known || !ok {
			t.Errorf("certificate does not cover %s (names %v)", host, testCertNames(t, certPEM))
		}
	}
	if n := len(testCertNames(t, certPEM)); n != 3 {
		t.Errorf("duplicate SANs not collapsed: %v", testCertNames(t, certPEM))
	}
}

func TestQuickCertNames(t *testing.T) {
	got := quickCertNames([]string{"", " a.example ", "[::1]", "A.example", "::1", "1.2.3.4"})
	want := []string{"a.example", "::1", "1.2.3.4"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func testLeaf(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatal("no PEM block")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testCertCovers(t *testing.T, certPEM, host string) (bool, bool) {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return testLeaf(t, certPEM).VerifyHostname(host) == nil, true
}

func testCertNames(t *testing.T, certPEM string) []string {
	c := testLeaf(t, certPEM)
	out := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}
