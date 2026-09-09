//go:build linux

// qingzhou-probe is a lightweight monitoring agent for the 轻舟 panel.
// It collects CPU, memory, disk, network, load, and process metrics from
// /proc and reports them via HTTP POST to the panel's /api/monitor/report.
//
// Usage:
//
//	qingzhou-probe -server https://panel.example.com -token <probe_token> [-interval 60] [-insecure]
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"qingzhou/internal/intervalcfg"
	"qingzhou/internal/sysmetrics"
	"qingzhou/internal/version"
)

var (
	flagServer   = flag.String("server", "", "Panel URL (required)")
	flagToken    = flag.String("token", "", "Probe authentication token (required)")
	flagInterval = flag.Int("interval", 60, "Initial collection interval in seconds (panel may update it live)")
	flagInsecure = flag.Bool("insecure", false, "Skip TLS certificate verification")
	flagVersion  = flag.Bool("version", false, "Print probe version and exit")
)

func main() {
	flag.Parse()
	if *flagVersion {
		fmt.Println(version.Current())
		return
	}

	// Also accept env vars as fallback (useful for systemd EnvironmentFile).
	server := *flagServer
	if server == "" {
		server = os.Getenv("QZ_PROBE_SERVER")
	}
	token := *flagToken
	if token == "" {
		token = os.Getenv("QZ_PROBE_TOKEN")
	}
	if server == "" || token == "" {
		fmt.Fprintf(os.Stderr, "Usage: qingzhou-probe -server <url> -token <token> [-interval 60] [-insecure]\n")
		fmt.Fprintf(os.Stderr, "  Or set QZ_PROBE_SERVER and QZ_PROBE_TOKEN environment variables.\n")
		os.Exit(1)
	}

	interval := *flagInterval
	if interval < 5 {
		interval = 5
	}
	if interval > int(intervalcfg.MaxProbeSeconds) {
		interval = int(intervalcfg.MaxProbeSeconds)
	}

	// A token passed on the command line is visible to any local user via
	// ps / /proc/<pid>/cmdline. The installer uses QZ_PROBE_TOKEN (systemd
	// EnvironmentFile, mode 600) instead — prefer that.
	if *flagToken != "" {
		log.Printf("WARNING: -token on the command line is visible via ps/proc; prefer the QZ_PROBE_TOKEN env var")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Build HTTP client.
	client := newReportClient(*flagInsecure)
	defer client.CloseIdleConnections()
	if *flagInsecure {
		log.Printf("WARNING: -insecure disables TLS certificate verification; the probe token and metrics are exposed to a man-in-the-middle. Use only for local testing.")
	}

	reportURL := server + "/api/monitor/report"
	log.Printf("qingzhou-probe starting: server=%s interval=%ds", server, interval)

	// CPU percentage and network speed are deltas between two reads, so the
	// first sample carries neither. Prime the sampler and throw that one away
	// rather than reporting a snapshot that claims the machine is idle.
	sampler := &sysmetrics.Sampler{}
	sampler.Sample()

	// Report once almost immediately so a one-click install becomes visible in
	// the panel without waiting a full collection interval. The sampler was
	// primed above, so even this first report has a real CPU/network delta.
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		m := sampler.Sample()
		m.ProbeVersion = version.Current()
		next, err := reportContext(ctx, client, reportURL, token, m)
		if err != nil {
			failures++
			log.Printf("report failed: %v", err)
		} else {
			failures = 0
			if next >= int(intervalcfg.MinProbeSeconds) && next <= int(intervalcfg.MaxProbeSeconds) && next != interval {
				log.Printf("collection interval updated by panel: %ds -> %ds", interval, next)
				interval = next
			}
		}
		// Never replay a metrics POST: a lost response may already be committed.
		// Back off the next fresh sample instead, without a queue of stale reports.
		timer.Reset(nextReportDelay(time.Duration(interval)*time.Second, failures, rand.Float64()))
	}
}

// report returns a panel-selected interval in seconds. A zero value means the
// server is an older version (or intentionally omitted the field), so the probe
// keeps its current interval. The unified API envelope is decoded explicitly;
// treating the top-level object as the payload would silently ignore updates.
func report(client *http.Client, url, token string, m sysmetrics.Metrics) (int, error) {
	return reportContext(context.Background(), client, url, token, m)
}

func reportContext(ctx context.Context, client *http.Client, url, token string, m sysmetrics.Metrics) (int, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return 0, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("server returned %d", resp.StatusCode)
	}
	if readErr != nil {
		return 0, fmt.Errorf("read response: %w", readErr)
	}
	if len(respBody) > 64<<10 {
		return 0, fmt.Errorf("response exceeds 64 KiB")
	}
	var envelope struct {
		Data struct {
			ProbeIntervalSeconds int `json:"probe_interval_seconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}
	return envelope.Data.ProbeIntervalSeconds, nil
}

// Exponential equal jitter, bounded between the configured interval and five
// minutes (or the interval if longer). Success resets to the live panel cadence.
func nextReportDelay(base time.Duration, failures int, random float64) time.Duration {
	if failures <= 0 {
		return base
	}
	ceiling := min(base*time.Duration(1<<min(failures, 10)), max(base, 5*time.Minute))
	floor := max(base, ceiling/2)
	return floor + time.Duration(float64(ceiling-floor)*random)
}

func newReportClient(insecure bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 2
	transport.MaxIdleConnsPerHost = 2
	transport.MaxConnsPerHost = 2
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = 10 * time.Second
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{
		Timeout: 15 * time.Second, Transport: transport,
		// A 307/308 could replay a committed metrics POST. Use the configured panel
		// endpoint directly; redirects are failures and only a new sample is sent.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
