package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSafeDialValidatesAllAnswersBeforeDialing(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "::ffff:127.0.0.1"} {
		t.Run(ip, func(t *testing.T) {
			called := false
			dial := safeFetchDialContext(func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}, {IP: net.ParseIP(ip)}}, nil
			}, func(context.Context, string, string) (net.Conn, error) {
				called = true
				return nil, errors.New("unexpected")
			})
			if _, err := dial(context.Background(), "tcp", "source.example:443"); err == nil || called {
				t.Fatalf("unsafe answer dialed: err=%v called=%v", err, called)
			}
		})
	}
}

func TestSafeDialPinsIPsAndFallsBackWithoutResolvingAgain(t *testing.T) {
	lookups := 0
	var addresses []string
	sentinel := errors.New("unreachable")
	dial := safeFetchDialContext(func(context.Context, string) ([]net.IPAddr, error) {
		lookups++
		return []net.IPAddr{{IP: net.ParseIP("2001:4860:4860::8888")}, {IP: net.ParseIP("1.1.1.1")}}, nil
	}, func(ctx context.Context, network, addr string) (net.Conn, error) {
		addresses = append(addresses, addr)
		return nil, sentinel
	})
	_, err := dial(context.Background(), "tcp", "source.example:443")
	if !errors.Is(err, sentinel) || lookups != 1 || strings.Join(addresses, ",") != "[2001:4860:4860::8888]:443,1.1.1.1:443" {
		t.Fatalf("err=%v lookups=%d addresses=%v", err, lookups, addresses)
	}
	empty := safeFetchDialContext(func(context.Context, string) ([]net.IPAddr, error) { return nil, nil }, nil)
	if _, err := empty(context.Background(), "tcp", "empty.example:443"); err == nil {
		t.Fatal("empty DNS result accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dial(ctx, "tcp", "source.example:443"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if len(addresses) != 2 {
		t.Fatal("dialed after cancellation")
	}
}

func TestSafeFetchRedirectAndTLSGuards(t *testing.T) {
	client := safeFetchClient()
	defer client.CloseIdleConnections()
	tr := client.Transport.(*http.Transport)
	if tr.Proxy != nil || tr.TLSClientConfig != nil || tr.MaxConnsPerHost != 4 || tr.MaxIdleConns != 32 || tr.IdleConnTimeout <= 0 || tr.TLSHandshakeTimeout <= 0 || tr.ResponseHeaderTimeout <= 0 {
		t.Fatal("unsafe or unbounded transport configuration")
	}
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer tlsServer.Close()
	// Map a public fixture name to our self-signed server; hostname/TLS checks
	// must still run after the custom dial, not be bypassed by address pinning.
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, tlsServer.Listener.Addr().String())
	}
	if _, err := client.Get("https://source.example/"); err == nil {
		t.Fatal("accepted untrusted TLS certificate")
	}

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/private", http.StatusFound)
	}))
	defer redirect.Close()
	client = safeFetchClient()
	defer client.CloseIdleConnections()
	tr = client.Transport.(*http.Transport)
	var dialed []string
	tr.DialContext = safeFetchDialContext(func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if host == "source.example" {
			return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
		}
		return net.DefaultResolver.LookupIPAddr(ctx, host)
	}, func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		return (&net.Dialer{}).DialContext(ctx, network, redirect.Listener.Addr().String())
	})
	if _, err := client.Get("http://source.example/"); err == nil {
		t.Fatal("redirect bypassed SSRF guard")
	}
	if len(dialed) != 1 {
		t.Fatalf("redirect dialed private target: %v", dialed)
	}
}

func TestSafeFetchIdleConnectionsExpire(t *testing.T) {
	closed := make(chan struct{}, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	srv.Start()
	defer srv.Close()
	client := safeFetchClient()
	defer client.CloseIdleConnections()
	tr := client.Transport.(*http.Transport)
	tr.IdleConnTimeout = 25 * time.Millisecond
	tr.DialContext = (&net.Dialer{}).DialContext // fixture-only loopback bypass
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("idle connection not released")
	}
}
