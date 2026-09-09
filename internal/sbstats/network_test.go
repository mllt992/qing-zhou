package sbstats

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type statsRoundTrip func(*http.Request) (*http.Response, error)

func (f statsRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestStatsRejectsHTTPErrorAndOversizedBody(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP error with valid frame", 502, string(frameMessage(nil))},
		{"oversized valid frame prefix", 200, string(frameMessage(nil)) + strings.Repeat("x", 16<<20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New("unused:80")
			c.hc = &http.Client{Transport: statsRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			if _, err := c.QueryUserTraffic(context.Background()); err == nil {
				t.Fatal("invalid stats response accepted")
			}
		})
	}
}
