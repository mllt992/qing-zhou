//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qingzhou/internal/sysmetrics"
)

func TestReportBackoffJitterBoundsAndReset(t *testing.T) {
	for _, base := range []time.Duration{5 * time.Second, time.Minute, 10 * time.Minute} {
		for n := 1; n <= 100; n++ {
			low := nextReportDelay(base, n, 0)
			high := nextReportDelay(base, n, 0.999)
			if low < base || high < low || high > max(base, 5*time.Minute) {
				t.Fatalf("base=%s attempt=%d range=%s..%s", base, n, low, high)
			}
		}
		if got := nextReportDelay(base, 0, 0.5); got != base {
			t.Fatalf("success did not reset: %s", got)
		}
	}
	if nextReportDelay(time.Minute, 2, 0) == nextReportDelay(time.Minute, 2, 0.9) {
		t.Fatal("backoff has no jitter")
	}
}

func TestReportRedirectDoesNotReplayMetrics(t *testing.T) {
	var replays atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { replays.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	client := newReportClient(false)
	defer client.CloseIdleConnections()
	if _, err := report(client, srv.URL, "token", sysmetrics.Metrics{}); err == nil {
		t.Fatal("redirect accepted")
	}
	if replays.Load() != 0 {
		t.Fatal("redirect replayed metrics POST")
	}
}

func TestReportFailureNeverReplaysPOST(t *testing.T) {
	for _, status := range []int{429, 503, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(status)
				// 200 with a malformed response models a committed POST whose response
				// was lost. Retrying it would insert the same metrics snapshot again.
				_, _ = io.WriteString(w, "invalid response")
			}))
			defer srv.Close()
			if _, err := report(srv.Client(), srv.URL, "token", sysmetrics.Metrics{}); err == nil {
				t.Fatal("invalid response accepted")
			}
			if calls.Load() != 1 {
				t.Fatalf("POST repeated %d times", calls.Load())
			}
		})
	}
}

func TestReportCancellationAndResponseLimit(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := reportContext(ctx, srv.Client(), srv.URL, "token", sysmetrics.Metrics{})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request not started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("report ignored cancellation")
	}
	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{}}`+strings.Repeat(" ", 64<<10))
	}))
	defer large.Close()
	if _, err := report(large.Client(), large.URL, "token", sysmetrics.Metrics{}); err == nil {
		t.Fatal("oversized response accepted")
	}
}
