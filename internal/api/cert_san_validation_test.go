package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
)

func legacyCertPair(t *testing.T, cn string, dns ...string) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: cn}, DNSNames: dns,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalPKCS8PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}))
}

func postJSON(t *testing.T, h http.HandlerFunc, body any, id int64) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	if id != 0 {
		req = withURLParam(req, "id", strconv.FormatInt(id, 10))
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

// #119: certificate-centre paste refuses SAN-less certificates and defaults the
// domain to a name the certificate actually covers.
func TestCertPasteValidatesSAN(t *testing.T) {
	a, st := newUserEditAPI(t)
	c, k := legacyCertPair(t, "legacy.example")
	w := postJSON(t, a.handleAdminCertPaste, map[string]any{"name": "x", "certificate": c, "key": k}, 0)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "SAN") {
		t.Fatalf("CN-only paste must be rejected: %d %s", w.Code, w.Body.String())
	}
	c, k = legacyCertPair(t, "cn.example", "san.example")
	w = postJSON(t, a.handleAdminCertPaste, map[string]any{"name": "y", "certificate": c, "key": k}, 0)
	if w.Code != http.StatusOK {
		t.Fatalf("paste failed: %s", w.Body.String())
	}
	list, _ := st.ListCerts()
	if len(list) != 1 || list[0].Domain != "san.example" {
		t.Fatalf("domain must default to a covered SAN, got %+v", list)
	}
	w = postJSON(t, a.handleAdminCertPaste, map[string]any{"name": "z", "domain": "other.example", "certificate": c, "key": k}, 0)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("explicit uncovered domain must be rejected: %s", w.Body.String())
	}
}

// Inline TLS profiles: a new certificate must cover the SNI (unless insecure),
// but editing a stored legacy profile without touching its certificate works.
func TestInlineTLSSaveValidatesSAN(t *testing.T) {
	a, st := newUserEditAPI(t)
	good, gk, _ := singbox.GenerateSelfSignedCert("ssl.mllt.vip", 30)
	w := postJSON(t, a.handleAdminSaveCertTls, map[string]any{"name": "a", "server_name": "217.216.188.164", "certificate": good, "key": gk}, 0)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("SNI not covered must be rejected: %s", w.Body.String())
	}
	w = postJSON(t, a.handleAdminSaveCertTls, map[string]any{"name": "a", "server_name": "217.216.188.164", "certificate": good, "key": gk, "insecure": true}, 0)
	if w.Code != http.StatusOK {
		t.Fatalf("insecure profile only needs SANs: %s", w.Body.String())
	}
	w = postJSON(t, a.handleAdminSaveCertTls, map[string]any{"name": "b", "server_name": "ssl.mllt.vip", "certificate": good, "key": gk}, 0)
	if w.Code != http.StatusOK {
		t.Fatalf("covered SNI must save: %s", w.Body.String())
	}
	// A legacy CN-only profile already in the database keeps loading and can be
	// edited (e.g. ALPN) without re-supplying the certificate.
	lc, lk := legacyCertPair(t, "legacy.example")
	sj, _ := json.Marshal(map[string]any{"enabled": true, "server_name": "legacy.example", "certificate": lc, "key": lk})
	id, err := st.SaveSbTls(&store.SbTls{Name: "legacy", Mode: "tls", ServerJSON: string(sj), ClientJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	w = postJSON(t, a.handleAdminSaveCertTls, map[string]any{"name": "legacy", "server_name": "legacy.example", "alpn": []string{"h2"}}, id)
	if w.Code != http.StatusOK {
		t.Fatalf("editing a stored legacy profile must keep working: %s", w.Body.String())
	}
	w = postJSON(t, a.handleAdminSaveCertTls, map[string]any{"name": "legacy", "server_name": "legacy.example", "certificate": lc, "key": lk}, id)
	if w.Code != http.StatusOK {
		t.Fatalf("re-submitting the unchanged stored certificate must keep working: %s", w.Body.String())
	}
	c2, k2 := legacyCertPair(t, "legacy2.example")
	w = postJSON(t, a.handleAdminSaveCertTls, map[string]any{"name": "legacy", "server_name": "legacy2.example", "certificate": c2, "key": k2}, id)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a newly supplied CN-only certificate must be rejected: %s", w.Body.String())
	}
}

// Referencing a wildcard certificate keeps a concrete covered SNI.
func TestManagedWildcardKeepsConcreteSNI(t *testing.T) {
	a, st := newUserEditAPI(t)
	c, k, _ := singbox.GenerateSelfSignedCert("*.mllt.vip", 30)
	cid, _ := st.SaveCert(&store.Cert{Name: "w", Domain: "*.mllt.vip", Source: "paste", CertPEM: c, KeyPEM: k})
	w := postJSON(t, a.handleAdminSaveCertTls, map[string]any{"name": "w", "server_name": "node1.mllt.vip", "cert_id": cid}, 0)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	var resp struct {
		Data store.SbTls `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp.Data.ServerJSON, `"server_name":"node1.mllt.vip"`) {
		t.Fatalf("concrete SNI must be kept: %s", resp.Data.ServerJSON)
	}
	w = postJSON(t, a.handleAdminSaveCertTls, map[string]any{"name": "w2", "server_name": "other.example", "cert_id": cid}, 0)
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp.Data.ServerJSON, `"server_name":"*.mllt.vip"`) {
		t.Fatalf("uncovered SNI falls back to the cert domain: %s", resp.Data.ServerJSON)
	}
}
