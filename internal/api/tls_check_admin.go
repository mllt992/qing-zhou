package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
	"qingzhou/internal/tlsprobe"
)

// tlsProbeQUIC lists inbound types whose TLS runs over QUIC; a TCP handshake
// cannot reach them, so they are reported as skipped, never as failures.
var tlsProbeQUIC = map[string]bool{"tuic": true, "hysteria": true, "hysteria2": true}

// POST /api/admin/sb/inbounds/{id}/tls-check — one TLS handshake per address
// clients are handed (host, then host_v6), judged as a separate "TLS" status
// (#118). It never alters the inbound's reachability/health verdict.
func (a *API) handleAdminInboundTLSCheck(w http.ResponseWriter, r *http.Request) {
	ib, err := a.st.GetSbInbound(atoi(chi.URLParam(r, "id")))
	if err != nil || ib == nil {
		fail(w, http.StatusNotFound, "入站不存在")
		return
	}
	host, hostV6 := "", ""
	if ib.ServerID == 0 {
		host = a.nodeHost()
	} else if sv, _ := a.st.GetServer(ib.ServerID); sv != nil {
		host, hostV6 = strings.TrimSpace(sv.Host), strings.TrimSpace(sv.HostV6)
		if host == "" {
			host = a.nodeHost()
		}
	}
	ok(w, a.inboundTLSCheck(r, ib, host, hostV6))
}

type inboundTLSCheckResult struct {
	Status  string            `json:"status"` // worst of Results; "skipped" when nothing applies
	Results []tlsprobe.Result `json:"results"`
}

func (a *API) inboundTLSCheck(r *http.Request, ib *store.SbInbound, host, hostV6 string) inboundTLSCheckResult {
	port := strconv.Itoa(ib.ListenPort)
	out := inboundTLSCheckResult{Status: tlsprobe.StatusSkipped}
	addrOf := func(h string) string {
		h = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(h), "["), "]")
		return net.JoinHostPort(h, port)
	}
	// Inline options.tls counts as TLS exactly as on the subscription and the
	// user page (#120); only an inbound with neither is "no TLS".
	info := a.st.InboundTLSInfo(ib)
	if ib.TlsID == 0 && info == nil {
		out.Results = append(out.Results, tlsprobe.Skipped(addrOf(host), "入站未启用 TLS"))
		return out
	}
	if tlsProbeQUIC[ib.Type] {
		out.Results = append(out.Results, tlsprobe.Skipped(addrOf(host), "QUIC 协议（"+ib.Type+"）无法用 TCP 握手检测，已跳过"))
		return out
	}
	if info == nil {
		out.Results = append(out.Results, tlsprobe.Skipped(addrOf(host), "TLS 配置为 Reality 或已禁用，跳过证书检测"))
		return out
	}
	expected := ""
	if info.Known {
		expected = singbox.CertFingerprintSHA256(info.PEM())
	}
	base := tlsprobe.Target{
		SNI: info.SNI, MustCoverHost: ib.Type == "mixed", ExpectedSHA256: expected,
		SelfSigned: info.SelfSigned, Insecure: info.Insecure, Timeout: 5 * time.Second,
	}
	if ib.Type == "mixed" {
		// HTTPS-proxy clients cannot send an SNI; they verify the dialed host.
		base.SNI = ""
	}
	now := time.Now()
	worst := ""
	for i, h := range []string{host, hostV6} {
		if strings.TrimSpace(h) == "" {
			continue
		}
		t := base
		t.Addr, t.Host = addrOf(h), h
		t.Lenient = i == 1 // the panel itself may have no IPv6 route
		res := tlsprobe.Probe(r.Context(), t, now)
		out.Results = append(out.Results, res)
		worst = worseStatus(worst, res.Status)
	}
	if worst != "" {
		out.Status = worst
	}
	return out
}

func worseStatus(a, b string) string {
	rank := map[string]int{"": 0, tlsprobe.StatusSkipped: 1, tlsprobe.StatusOK: 2, tlsprobe.StatusWarning: 3, tlsprobe.StatusError: 4}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
