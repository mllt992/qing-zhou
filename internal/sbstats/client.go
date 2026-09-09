// Package sbstats reads per-user traffic from sing-box's v2ray_api gRPC
// StatsService (B2 integration model). It speaks gRPC-over-h2c by hand (just
// x/net/http2 + a tiny hand-rolled protobuf codec for the two messages we
// need) so 轻舟 takes neither a grpc-go dependency nor any sing-box import —
// keeping the binary lean and the license independent.
//
// sing-box tracks per-user counters named:
//
//	user>>>NAME>>>traffic>>>uplink
//	user>>>NAME>>>traffic>>>downlink
//
// We QueryStats with reset=true, so each poll returns the DELTA since the last
// poll and zeroes the counter — 轻舟 just accumulates the deltas into each
// user's usage (counters also reset whenever sing-box restarts).
package sbstats

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

// Traffic is a user's up/down delta in bytes.
type Traffic struct {
	Up   int64
	Down int64
}

const fullMethod = "/v2ray.core.app.stats.command.StatsService/QueryStats"

// Client queries a sing-box v2ray_api gRPC endpoint over cleartext HTTP/2.
//
// A Client owns its transport and every connection that transport dialled, so a
// caller that builds one per poll MUST Close it — see Close for why.
type Client struct {
	addr string // host:port of experimental.v2ray_api.listen
	hc   *http.Client
	tr   *http2.Transport

	// dialed records every connection handed to the transport so Close can
	// force them shut. The transport pools connections and never drops them on
	// its own, and nothing here is reachable once the Client goes out of scope —
	// GC does not close sockets.
	mu     sync.Mutex
	dialed []net.Conn
}

// DialFunc opens the transport connection to the stats endpoint.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// New builds a client for the given v2ray_api listen address (e.g.
// 127.0.0.1:18080), dialled directly.
func New(addr string) *Client {
	return NewWithDialer(addr, func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	})
}

// NewWithDialer builds a client that reaches the stats endpoint through dial.
// Remote nodes bind the stats API on loopback, so their traffic can only be
// collected through an SSH tunnel — see sshctl.RemoteManager.DialTunnel.
func NewWithDialer(addr string, dial DialFunc) *Client {
	c := &Client{addr: addr}
	c.tr = &http2.Transport{
		AllowHTTP: true, // h2c: prior-knowledge HTTP/2 over plaintext TCP
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			conn, err := dial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			c.dialed = append(c.dialed, conn)
			c.mu.Unlock()
			return conn, nil
		},
	}
	c.hc = &http.Client{Transport: c.tr, Timeout: 10 * time.Second}
	return c
}

// Close releases every connection this Client opened. Callers that build a
// Client per poll must call it; a long-lived Client (the local instance, which
// dials 127.0.0.1 directly) can skip it and keep its pooled connection.
//
// This matters far beyond a stray socket when the dialer is an SSH tunnel:
// sshctl's tunnelConn owns the *ssh.Client carrying it, so a connection that is
// never closed keeps a full SSH session alive on the remote node. Stats polling
// runs every minute per server, so skipping Close leaks one sshd process per
// node per minute — enough to exhaust a small VPS in hours.
func (c *Client) Close() error {
	// Graceful path: hands pooled connections back so the transport stops
	// tracking them.
	c.tr.CloseIdleConnections()

	// Force path: a request that failed mid-flight (context deadline, tunnel
	// hiccup) can leave its connection marked busy, where CloseIdleConnections
	// will not touch it. Closing directly is what actually guarantees the SSH
	// session underneath goes away. Errors are ignored: a connection the
	// graceful path already closed reports net.ErrClosed here, which is the
	// expected case rather than a failure.
	c.mu.Lock()
	conns := c.dialed
	c.dialed = nil
	c.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	return nil
}

// QueryUserTraffic returns each user's up/down delta since the previous call
// (reset=true). Keys are the user names registered in
// experimental.v2ray_api.stats.users.
func (c *Client) QueryUserTraffic(ctx context.Context) (map[string]*Traffic, error) {
	// Request: patterns=["user>>>"], reset=true → only user counters, reset them.
	req := encodeQueryRequest("user>>>", true)
	frame := frameMessage(req)

	url := "http://" + c.addr + fullMethod
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/grpc")
	httpReq.Header.Set("te", "trailers")

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Bound the read: the peer is normally the local sing-box, but a
	// malfunctioning/hostile listener on the configured address could otherwise
	// stream an unbounded body and OOM the panel.
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stats HTTP %d", resp.StatusCode)
	}
	const maxStatsBytes = 16 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxStatsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxStatsBytes {
		return nil, fmt.Errorf("stats response exceeds 16 MiB")
	}
	// gRPC status lives in trailers; non-zero = error.
	if st := resp.Trailer.Get("grpc-status"); st != "" && st != "0" {
		return nil, fmt.Errorf("grpc-status %s: %s", st, resp.Trailer.Get("grpc-message"))
	}
	msg, err := unframeMessage(body)
	if err != nil {
		return nil, err
	}
	stats, err := decodeQueryResponse(msg)
	if err != nil {
		return nil, err
	}

	out := map[string]*Traffic{}
	for name, val := range stats {
		// name = user>>>NAME>>>traffic>>>uplink|downlink
		parts := splitName(name)
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" {
			continue
		}
		t := out[parts[1]]
		if t == nil {
			t = &Traffic{}
			out[parts[1]] = t
		}
		switch parts[3] {
		case "uplink":
			t.Up += val
		case "downlink":
			t.Down += val
		}
	}
	return out, nil
}

// splitName splits a v2ray stat name on the ">>>" separator.
func splitName(name string) []string { return strings.Split(name, ">>>") }

// ---- gRPC length-prefixed framing ----

func frameMessage(msg []byte) []byte {
	buf := make([]byte, 5+len(msg))
	buf[0] = 0 // not compressed
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(msg)))
	copy(buf[5:], msg)
	return buf
}

func unframeMessage(body []byte) ([]byte, error) {
	if len(body) < 5 {
		return nil, fmt.Errorf("grpc frame too short (%d bytes)", len(body))
	}
	n := binary.BigEndian.Uint32(body[1:5])
	if int(n) > len(body)-5 {
		return nil, fmt.Errorf("grpc frame length %d exceeds body %d", n, len(body)-5)
	}
	return body[5 : 5+n], nil
}

// ---- minimal protobuf for StatsService ----
//
// QueryStatsRequest { string pattern=1; bool reset=2; repeated string patterns=3; bool regexp=4 }
// QueryStatsResponse { repeated Stat stat=1 }   Stat { string name=1; int64 value=2 }

func encodeQueryRequest(pattern string, reset bool) []byte {
	var b []byte
	// field 3 (patterns), wire type 2 (length-delimited)
	b = append(b, 0x1A)
	b = appendVarint(b, uint64(len(pattern)))
	b = append(b, pattern...)
	if reset {
		// field 2 (reset), wire type 0 (varint)
		b = append(b, 0x10, 0x01)
	}
	return b
}

// decodeQueryResponse parses repeated Stat (field 1) → name:value.
func decodeQueryResponse(b []byte) (map[string]int64, error) {
	out := map[string]int64{}
	for len(b) > 0 {
		tag, n := readVarint(b)
		if n == 0 {
			return nil, fmt.Errorf("bad response tag")
		}
		b = b[n:]
		field := tag >> 3
		wire := tag & 7
		if field == 1 && wire == 2 { // Stat message
			l, n := readVarint(b)
			if n == 0 || !fitsIn(l, len(b)-n) {
				return nil, fmt.Errorf("bad Stat length")
			}
			b = b[n:]
			name, value := decodeStat(b[:l])
			if name != "" {
				out[name] = value
			}
			b = b[l:]
		} else {
			var err error
			b, err = skipField(b, wire)
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func decodeStat(b []byte) (name string, value int64) {
	for len(b) > 0 {
		tag, n := readVarint(b)
		if n == 0 {
			return
		}
		b = b[n:]
		switch field, wire := tag>>3, tag&7; {
		case field == 1 && wire == 2: // name
			l, n := readVarint(b)
			if n == 0 || !fitsIn(l, len(b)-n) {
				return
			}
			b = b[n:]
			name = string(b[:l])
			b = b[l:]
		case field == 2 && wire == 0: // value
			v, n := readVarint(b)
			if n == 0 {
				return
			}
			value = int64(v)
			b = b[n:]
		default:
			var err error
			b, err = skipField(b, wire)
			if err != nil {
				return
			}
		}
	}
	return
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// readVarint decodes a protobuf varint, returning (value, bytes consumed) or
// (0, 0) on a malformed one.
//
// The 10th byte is rejected unless it is 0 or 1. Without that check a varint can
// carry bit 63, and every caller here turns the result into an int to bounds-check
// it — which goes negative, sails past the check, and then panics (or indexes
// backwards) on the slice that follows. This decoder is pointed at whatever is
// listening on sb_v2ray_listen, so a hostile or merely buggy listener there must
// not be able to take the panel down; nothing in this package runs under a
// recover().
func readVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b); i++ {
		if i == 9 && b[i] > 1 {
			return 0, 0 // would overflow uint64
		}
		v |= uint64(b[i]&0x7F) << (7 * i)
		if b[i] < 0x80 {
			return v, i + 1
		}
		if i >= 9 {
			break
		}
	}
	return 0, 0
}

// fitsIn reports whether a length varint can address that many bytes of the
// remaining buffer. Compared as uint64 on both sides — converting the length to
// int first is what made the old bounds checks unsound.
func fitsIn(l uint64, remaining int) bool {
	return remaining >= 0 && l <= uint64(remaining)
}

func skipField(b []byte, wire uint64) ([]byte, error) {
	switch wire {
	case 0: // varint
		_, n := readVarint(b)
		if n == 0 {
			return nil, fmt.Errorf("bad varint")
		}
		return b[n:], nil
	case 1: // 64-bit
		if len(b) < 8 {
			return nil, fmt.Errorf("short 64-bit")
		}
		return b[8:], nil
	case 2: // length-delimited
		l, n := readVarint(b)
		if n == 0 || !fitsIn(l, len(b)-n) {
			return nil, fmt.Errorf("bad length-delimited")
		}
		return b[n+int(l):], nil
	case 5: // 32-bit
		if len(b) < 4 {
			return nil, fmt.Errorf("short 32-bit")
		}
		return b[4:], nil
	default:
		return nil, fmt.Errorf("unknown wire type %d", wire)
	}
}
