package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// isInternalIP reports whether ip is loopback, private, link-local, unspecified,
// or CGNAT — addresses an admin-supplied fetch URL must not be allowed to reach
// (SSRF: cloud metadata endpoints, localhost admin ports, LAN services).
func isInternalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	// 100.64.0.0/10 (CGNAT) — not covered by IsPrivate.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

// safeFetchClient returns an http.Client that verifies TLS and refuses to
// connect to internal addresses. The guard runs at dial time (resolving the
// host and dialing a vetted IP directly) so it also defeats DNS rebinding and
// redirects that point at internal hosts.
func safeFetchClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   2,
			MaxConnsPerHost:       4,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			DialContext:           safeFetchDialContext(net.DefaultResolver.LookupIPAddr, dialer.DialContext),
		},
	}
}

// Resolve exactly once, validate every answer, then dial only vetted IPs. No
// environment proxy is used: it would move DNS and SSRF enforcement elsewhere.
func safeFetchDialContext(lookup func(context.Context, string) ([]net.IPAddr, error), dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Transport may keep a dial alive for reuse after a request is canceled.
		// Bound DNS and all fallback addresses together, not just each TCP dial.
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("主机没有可用地址")
		}
		for _, ip := range ips {
			if isInternalIP(ip.IP) {
				return nil, fmt.Errorf("拒绝连接到内网地址 %s", ip.IP)
			}
		}
		// Dial a vetted IP directly to avoid a re-resolve (rebinding) window;
		// TLS ServerName still verifies against the original hostname.
		var lastErr error
		for _, ip := range ips {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			conn, err := dial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// maxBodyMiddleware wraps every request body in an http.MaxBytesReader so a
// decode of an oversized body fails cleanly instead of buffering it all.
func maxBodyMiddleware(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// validFetchURL parses a user-supplied fetch URL and rejects non-HTTP schemes
// (file://, gopher://, etc.). Returns an error message, empty when acceptable.
func validFetchURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "URL 格式错误"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "仅支持 http/https 链接"
	}
	if u.Host == "" {
		return "URL 缺少主机名"
	}
	return ""
}
