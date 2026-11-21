package store

import (
	"encoding/json"
	"testing"

	"qingzhou/internal/singbox"
)

// #119: resolveTlsBlock keeps a concrete SNI covered by a wildcard managed cert
// instead of overwriting it with the "*.x" pattern; uncovered names still fall
// back to the cert domain, so existing single-name data is unchanged.
func TestResolveTlsBlockWildcardKeepsConcreteSNI(t *testing.T) {
	s := newCertTestStore(t)
	c, k, err := singbox.GenerateSelfSignedCert("*.mllt.vip", 30)
	if err != nil {
		t.Fatal(err)
	}
	cid, err := s.SaveCert(&Cert{Name: "w", Domain: "*.mllt.vip", Source: "paste", CertPEM: c, KeyPEM: k})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ stored, want string }{
		{"node1.mllt.vip", "node1.mllt.vip"},
		{"other.example", "*.mllt.vip"},
		{"", "*.mllt.vip"},
	} {
		sj, _ := json.Marshal(map[string]any{"enabled": true, "server_name": tc.stored})
		id, err := s.SaveSbTls(&SbTls{Name: "t-" + tc.stored, Mode: "tls", CertID: cid, ServerJSON: string(sj), ClientJSON: `{}`})
		if err != nil {
			t.Fatal(err)
		}
		block, err := s.resolveTlsBlock(id, "in", map[int64]*SbTls{}, map[int64]*Cert{})
		if err != nil {
			t.Fatal(err)
		}
		if got := block["server_name"]; got != tc.want {
			t.Errorf("stored %q: server_name = %v, want %q", tc.stored, got, tc.want)
		}
	}
}
