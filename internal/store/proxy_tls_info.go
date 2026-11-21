package store

import (
	"encoding/json"
	"os"
	"strings"

	"qingzhou/internal/singbox"
)

// InboundTLSInfo is what a client needs to know to trust an inbound's TLS
// certificate. It is derived from the same resolved TLS block the node's
// sing-box config is built from (resolveTlsBlock), so it describes the
// certificate the node actually presents rather than a stored label.
//
// Every field is advisory: an unknown certificate (unreadable path, decrypt
// failure) leaves Known false and nothing here is allowed to hide a node.
type InboundTLSInfo struct {
	Known      bool     `json:"known"`       // certificate parsed; the fields below are meaningful
	SNI        string   `json:"sni"`         // server name clients should send/verify
	CertDomain string   `json:"cert_domain"` // managed cert's domain, else the leaf's first name
	CertNames  []string `json:"cert_names"`  // DNS + IP SANs (what Go clients actually match)
	SelfSigned bool     `json:"self_signed"`
	SHA256     string   `json:"sha256"`    // leaf SHA-256 (pin), self-signed only
	Insecure   bool     `json:"insecure"`  // profile tells clients to skip verification
	NotAfter   int64    `json:"not_after"` // unix seconds, 0 when unknown
	pem        string
}

// CoversHost reports whether a client verifying against host (no SNI override,
// as every plain HTTPS-proxy client does) would accept the certificate. known is
// false when that cannot be determined; callers must not warn then.
func (i *InboundTLSInfo) CoversHost(host string) (covered, known bool) {
	if i == nil || !i.Known {
		return false, false
	}
	return singbox.CertCoversHost(i.pem, host)
}

// PEM returns the leaf certificate bundle the info was derived from.
func (i *InboundTLSInfo) PEM() string {
	if i == nil {
		return ""
	}
	return i.pem
}

// inboundTLSInfo resolves an inbound's TLS profile the way the config builder
// does and summarises the presented certificate. nil when the inbound has no
// TLS profile or it cannot be resolved. Caches are optional.
func (s *Store) inboundTLSInfo(ib *SbInbound, tlsCache map[int64]*SbTls, certCache map[int64]*Cert) *InboundTLSInfo {
	if ib == nil || ib.TlsID == 0 {
		return nil
	}
	if tlsCache == nil {
		tlsCache = map[int64]*SbTls{}
	}
	if certCache == nil {
		certCache = map[int64]*Cert{}
	}
	block, err := s.resolveTlsBlock(ib.TlsID, ib.Tag, tlsCache, certCache)
	if err != nil || block == nil {
		return nil
	}
	if v, ok := block["enabled"].(bool); ok && !v {
		return nil
	}
	if _, isReality := block["reality"]; isReality {
		return nil
	}
	info := &InboundTLSInfo{SNI: mapStr(block, "server_name")}
	t := tlsCache[ib.TlsID]
	if t != nil {
		var client map[string]interface{}
		if json.Unmarshal([]byte(t.ClientJSON), &client) == nil {
			info.Insecure = mapBool(client, "insecure")
			if sni := mapStr(client, "server_name"); sni != "" {
				info.SNI = sni
			}
		}
		if t.CertID != 0 {
			if c := certCache[t.CertID]; c != nil {
				info.CertDomain = c.Domain
			}
		}
	}
	pem := relayCertificatePEM(block["certificate"])
	if pem == "" && ib.ServerID == 0 {
		// acme.sh-installed certificates on the panel host are referenced by
		// path; read it so the local node is described too. Remote paths are
		// not readable from here and simply stay unknown.
		if p := mapStr(block, "certificate_path"); p != "" {
			if b, err := os.ReadFile(p); err == nil {
				pem = string(b)
			}
		}
	}
	leaf := singbox.ParseLeafCert(pem)
	if leaf == nil {
		return info
	}
	info.Known = true
	info.pem = pem
	info.CertNames = singbox.CertNames(pem)
	info.NotAfter = leaf.NotAfter.Unix()
	if info.CertDomain == "" {
		if len(leaf.DNSNames) > 0 {
			info.CertDomain = leaf.DNSNames[0]
		} else if len(info.CertNames) > 0 {
			info.CertDomain = info.CertNames[0]
		} else {
			info.CertDomain = strings.TrimSpace(leaf.Subject.CommonName)
		}
	}
	if singbox.IsSelfSignedCert(pem) {
		info.SelfSigned = true
		info.SHA256 = singbox.CertFingerprintSHA256(pem)
	}
	return info
}

// InboundTLSInfo is the exported, uncached form for API handlers.
func (s *Store) InboundTLSInfo(ib *SbInbound) *InboundTLSInfo {
	return s.inboundTLSInfo(ib, nil, nil)
}
