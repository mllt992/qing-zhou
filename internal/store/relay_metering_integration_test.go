package store

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/sbstats"
	"qingzhou/internal/singbox"
)

func meteringTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}
func startMeteringBox(t *testing.T, bin string, raw []byte, api string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "check", "-c", path).CombinedOutput(); err != nil {
		t.Fatalf("config check: %v %s", err, out)
	}
	log, err := os.Create(filepath.Join(dir, "box.log"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "run", "-c", path)
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		cancel()
		log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		cancel()
		<-done
		log.Close()
		if t.Failed() {
			output, _ := os.ReadFile(filepath.Join(dir, "box.log"))
			t.Logf("sing-box fixture %s: %s", api, output)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", api, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	output, _ := os.ReadFile(filepath.Join(dir, "box.log"))
	t.Fatalf("sing-box fixture did not expose stats API: %s", output)
}

// This is a real two-process TCP relay on loopback only. The generated edge
// credential is distinct from the user's; both machines observe traffic but
// only the client's entry debits its quota. No production host is contacted.
func TestMeteringRelayRealSingboxTraffic(t *testing.T) {
	bin := os.Getenv("QZ_SINGBOX_TEST_BIN")
	if bin == "" {
		t.Skip("set QZ_SINGBOX_TEST_BIN for loopback integration")
	}
	st, uid := trafficLedgerUser(t)
	st.SetSecretKey([]byte("fixture-only-encryption"))
	if err := st.SetSetting(SettingBlockPrivateEgress, "0"); err != nil {
		t.Fatal(err)
	}
	a, _ := st.CreateServer(Server{Name: "entry", Host: "127.0.0.1", Enabled: true})
	b, _ := st.CreateServer(Server{Name: "landing", Host: "127.0.0.1", Enabled: true})
	entryPort, landingPort := meteringTestPort(t), meteringTestPort(t)
	aAPI, bAPI := fmt.Sprintf("127.0.0.1:%d", meteringTestPort(t)), fmt.Sprintf("127.0.0.1:%d", meteringTestPort(t))
	bi, err := st.SaveSbInbound(&SbInbound{ServerID: b, Type: "vless", Tag: "integration-landing", Listen: "127.0.0.1", ListenPort: landingPort, Options: `{}`, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.SaveSbInbound(&SbInbound{ServerID: a, Type: "mixed", Tag: "integration-entry", Listen: "127.0.0.1", ListenPort: entryPort, Options: `{}`, Enabled: true, UpstreamInboundID: bi})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.ConfigureTrafficMetering(true, true); err != nil {
		t.Fatal(err)
	}
	if err = st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	users := map[string][]singbox.User{"integration-entry": {{Name: "qz_ledger_user", Password: "fixture-password"}}}
	landing, err := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, bAPI, users)
	if err != nil {
		t.Fatal(err)
	}
	startMeteringBox(t, bin, landing, bAPI)
	if err = st.RecordRelayConfigApplied(b, landing); err != nil {
		t.Fatal(err)
	}
	entry, err := st.BuildSingboxConfigForServer(a, singbox.DefaultBaseConfig, aAPI, users)
	if err != nil {
		t.Fatal(err)
	}
	startMeteringBox(t, bin, entry, aAPI)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, strings.Repeat("x", 65536)) }))
	defer destination.Close()
	proxy, _ := url.Parse(fmt.Sprintf("http://qz_ledger_user:fixture-password@127.0.0.1:%d", entryPort))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get(destination.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || len(body) != 65536 {
		t.Fatalf("relay response status=%s bytes=%d %v", response.Status, len(body), err)
	}
	aStats, bStats := sbstats.New(aAPI), sbstats.New(bAPI)
	defer aStats.Close()
	defer bStats.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	from, err := aStats.QueryTraffic(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	to, err := bStats.QueryTraffic(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	links, _ := st.RelayMeteringLinks()
	link := links[0]
	if from["qz_ledger_user"] == nil || from["qz_ledger_user"].Down < 65536 || from["outbound:"+link.outboundTag()] == nil || to[link.IdentityName] == nil || to[link.IdentityName].Down < 65536 {
		t.Fatalf("missing real counters entry=%+v landing=%+v", from, to)
	}
	deltas := func(m map[string]*sbstats.Traffic) map[string]UsageDelta {
		out := map[string]UsageDelta{}
		for name, v := range m {
			out[name] = UsageDelta{Up: v.Up, Down: v.Down}
		}
		return out
	}
	pa := NewTrafficPoll(a, deltas(from))
	pa.Mode = "cumulative"
	pa.Epoch = "fixture-entry"
	pb := NewTrafficPoll(b, deltas(to))
	pb.Mode = "cumulative"
	pb.Epoch = "fixture-landing"
	if _, err = st.RecordTrafficPoll(pa); err != nil {
		t.Fatal(err)
	}
	if _, err = st.RecordTrafficPoll(pb); err != nil {
		t.Fatal(err)
	}
	want := from["qz_ledger_user"].Up + from["qz_ledger_user"].Down
	if got := ledgerUsed(t, st, uid); got != want {
		t.Fatalf("double billed relay: got %d want %d", got, want)
	}
	ra, err := st.ServerServiceTraffic(a, 0)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := st.ServerServiceTraffic(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ra.Total != want || rb.Total == 0 || rb.BillableTotal != 0 {
		t.Fatalf("ledgers entry=%+v landing=%+v", ra, rb)
	}
	// QueryStats reset=false must leave cumulative values unchanged, and replay
	// of the same observation must never re-debit the user.
	again, err := aStats.QueryTraffic(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if again["qz_ledger_user"].Down != from["qz_ledger_user"].Down {
		t.Fatal("cumulative read reset counter")
	}
	if _, err = st.RecordTrafficPoll(pa); err != nil {
		t.Fatal(err)
	}
	if ledgerUsed(t, st, uid) != want {
		t.Fatal("replay re-debited quota")
	}
}
