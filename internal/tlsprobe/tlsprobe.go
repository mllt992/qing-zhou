// Package tlsprobe performs a single TLS handshake against a node's inbound and
// reports, as a separate "TLS" status, whether the certificate it presents is
// one clients will accept (#118). The panel's reachability check is a bare TCP
// dial: it stays green while a wrong SNI, a stale or expired certificate, or a
// certificate that does not cover the advertised address makes every client
// fail. This package never changes that health verdict; it only adds detail.
//
// It verifies nothing on the wire itself (InsecureSkipVerify): the point is to
// look at what the node presents and judge it the way each client would, not
// to fail the handshake on the first problem and report nothing else.
package tlsprobe

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"
)

// Status values, from best to worst. "skipped" means the probe does not apply
// (e.g. QUIC inbounds) and must never be read as a failure.
const (
	StatusOK      = "ok"
	StatusWarning = "warning"
	StatusError   = "error"
	StatusSkipped = "skipped"
)

// ExpiryWarning is how close to expiry a certificate is reported as a warning.
const ExpiryWarning = 30 * 24 * time.Hour

// Target describes one probe.
type Target struct {
	Addr string // host:port to dial
	// SNI is the server name clients send. Empty or an IP literal means no SNI
	// is sent (Go never puts an IP in SNI); verification is then against Host.
	SNI string
	// Host is the address clients are handed. MustCoverHost marks protocols
	// whose clients cannot override the name they verify (HTTPS proxies): for
	// them a certificate that does not cover Host is a real failure; for the
	// rest it is only informational.
	Host          string
	MustCoverHost bool
	// ExpectedSHA256 is the leaf fingerprint the panel believes the node
	// serves (uppercase hex); empty skips the comparison. SelfSigned marks a
	// certificate subscriptions pin — a mismatch then breaks pinned clients.
	ExpectedSHA256 string
	SelfSigned     bool
	// Insecure: the profile tells clients to skip verification, so name
	// problems degrade to warnings for them.
	Insecure bool
	// Lenient downgrades a handshake failure to a warning — for paths the
	// panel itself may not be able to reach (e.g. IPv6 from an IPv4-only panel).
	Lenient bool
	Timeout time.Duration
}

// Result is the outcome of one probe. Problems lower the status to error,
// Warnings to warning; Notes are informational only.
type Result struct {
	Addr            string   `json:"addr"`
	Status          string   `json:"status"`
	Handshake       bool     `json:"handshake"`
	TLSVersion      string   `json:"tls_version,omitempty"`
	ALPN            string   `json:"alpn,omitempty"`
	PresentedSHA256 string   `json:"presented_sha256,omitempty"`
	CertNames       []string `json:"cert_names,omitempty"`
	SelfSigned      bool     `json:"self_signed"`
	NotAfter        int64    `json:"not_after,omitempty"`
	DaysLeft        int      `json:"days_left"`
	PinMatch        *bool    `json:"pin_match,omitempty"`
	SNICovered      *bool    `json:"sni_covered,omitempty"`
	HostCovered     *bool    `json:"host_covered,omitempty"`
	Problems        []string `json:"problems,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
	Notes           []string `json:"notes,omitempty"`
	ElapsedMS       int64    `json:"elapsed_ms"`
}

func boolp(b bool) *bool { return &b }

func unbracket(h string) string {
	h = strings.TrimSpace(h)
	if len(h) > 1 && h[0] == '[' && h[len(h)-1] == ']' {
		return h[1 : len(h)-1]
	}
	return h
}

// Skipped is the result for an inbound the probe does not apply to.
func Skipped(addr, why string) Result {
	return Result{Addr: addr, Status: StatusSkipped, Notes: []string{why}}
}

// Probe dials t.Addr, completes a TLS handshake and evaluates the presented
// certificate at time now.
func Probe(ctx context.Context, t Target, now time.Time) Result {
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	r := Result{Addr: t.Addr}
	start := time.Now()
	sni := unbracket(t.SNI)
	cfg := &tls.Config{InsecureSkipVerify: true} // judged below, see package doc
	if sni != "" && net.ParseIP(sni) == nil {
		cfg.ServerName = sni
	}
	d := &net.Dialer{Timeout: timeout}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(dctx, "tcp", t.Addr)
	r.ElapsedMS = time.Since(start).Milliseconds()
	if err != nil {
		msg := "TLS 握手失败：" + err.Error()
		if t.Lenient {
			r.Warnings = append(r.Warnings, msg+"（面板可能无法经此地址族连接，未判定为故障）")
		} else {
			r.Problems = append(r.Problems, msg)
		}
		r.Status = finalStatus(r)
		return r
	}
	defer conn.Close()
	state := conn.(*tls.Conn).ConnectionState()
	r.Handshake = true
	r.TLSVersion = tls.VersionName(state.Version)
	r.ALPN = state.NegotiatedProtocol
	if len(state.PeerCertificates) == 0 {
		r.Problems = append(r.Problems, "节点未出示证书")
		r.Status = finalStatus(r)
		return r
	}
	Evaluate(&r, state.PeerCertificates[0], t, now)
	r.Status = finalStatus(r)
	return r
}

// Evaluate fills r from the presented leaf. Split out so the judgement can be
// tested without a network.
func Evaluate(r *Result, leaf *x509.Certificate, t Target, now time.Time) {
	sum := sha256.Sum256(leaf.Raw)
	r.PresentedSHA256 = strings.ToUpper(hex.EncodeToString(sum[:]))
	r.CertNames = append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		r.CertNames = append(r.CertNames, ip.String())
	}
	r.SelfSigned = leaf.Issuer.String() == leaf.Subject.String() &&
		leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil
	r.NotAfter = leaf.NotAfter.Unix()
	r.DaysLeft = int(leaf.NotAfter.Sub(now).Hours() / 24)

	nameIssue := func(msg string) {
		if t.Insecure {
			r.Warnings = append(r.Warnings, msg+"（客户端配置为跳过校验，暂不影响连接）")
		} else {
			r.Problems = append(r.Problems, msg)
		}
	}

	if exp := strings.ToUpper(strings.ReplaceAll(t.ExpectedSHA256, ":", "")); exp != "" {
		match := exp == r.PresentedSHA256
		r.PinMatch = boolp(match)
		if !match {
			if t.SelfSigned {
				r.Problems = append(r.Problems, "节点出示的证书与面板记录的自签证书不一致：订阅里按指纹固定的客户端会拒绝连接。请重新下发节点配置")
			} else {
				r.Warnings = append(r.Warnings, "节点出示的证书与面板记录的不一致（可能刚续期尚未下发）。请重新下发节点配置")
			}
		}
	}

	if len(leaf.DNSNames) == 0 && len(leaf.IPAddresses) == 0 {
		nameIssue("证书没有任何 SAN（只有 CN）：Go 1.15+ 客户端（mihomo、sing-box、Xray）一律拒绝")
	}

	sni := unbracket(t.SNI)
	if sni != "" {
		ok := leaf.VerifyHostname(sni) == nil
		r.SNICovered = boolp(ok)
		if !ok {
			nameIssue(fmt.Sprintf("证书不包含下发给客户端的 SNI %s（证书签发给 %s）", sni, namesOrNone(r.CertNames)))
		}
	}
	if host := unbracket(t.Host); host != "" {
		ok := leaf.VerifyHostname(host) == nil
		r.HostCovered = boolp(ok)
		if !ok {
			msg := fmt.Sprintf("证书不包含节点地址 %s（证书签发给 %s）", host, namesOrNone(r.CertNames))
			// A certificate that does not cover the advertised address is
			// never an error: certificates usually cover the domain / IPv4
			// only, and only HTTPS-proxy clients verify the dialed address
			// at all. Hy2/TUIC/VLESS-style clients validate the SNI (or skip
			// verification), so for them it is merely informational.
			switch {
			case t.MustCoverHost:
				r.Warnings = append(r.Warnings, msg+"：HTTPS 代理客户端会按连接地址校验证书，按此地址连接的客户端需改用域名或跳过校验（仅提醒，不影响其他地址）")
			case sni == "":
				r.Notes = append(r.Notes, msg+"；该协议客户端通常不校验地址，仅提醒")
			default:
				r.Notes = append(r.Notes, msg+"；客户端按 SNI 校验，不影响连接")
			}
		}
	}

	if t.MustCoverHost && r.SelfSigned {
		r.Warnings = append(r.Warnings, "这是自签证书，不在系统受信任 CA 列表中：curl、Docker、1Panel、Git、浏览器等标准 HTTPS 代理客户端默认会直接拒连（self-signed certificate）。建议给服务器配置子域名并改用 ACME 真实证书（如泛域名证书）")
	}

	switch {
	case now.After(leaf.NotAfter):
		r.Problems = append(r.Problems, fmt.Sprintf("证书已于 %s 过期", leaf.NotAfter.Format("2006-01-02")))
	case now.Before(leaf.NotBefore):
		r.Problems = append(r.Problems, fmt.Sprintf("证书要到 %s 才生效（检查节点时钟）", leaf.NotBefore.Format("2006-01-02")))
	case leaf.NotAfter.Sub(now) <= ExpiryWarning:
		r.Warnings = append(r.Warnings, fmt.Sprintf("证书将在 %d 天后（%s）过期", r.DaysLeft, leaf.NotAfter.Format("2006-01-02")))
	}
}

func namesOrNone(n []string) string {
	if len(n) == 0 {
		return "（无 SAN）"
	}
	return strings.Join(n, "、")
}

func finalStatus(r Result) string {
	switch {
	case len(r.Problems) > 0:
		return StatusError
	case len(r.Warnings) > 0:
		return StatusWarning
	}
	return StatusOK
}
