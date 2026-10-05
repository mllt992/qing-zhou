package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"qingzhou/internal/sbstats"
	"qingzhou/internal/singbox"
)

type meteringPortReservation struct {
	owner *testing.T
	tcp   net.Listener
	udp   net.PacketConn
}

var meteringPorts = struct {
	sync.Mutex
	next     int
	reserved map[int]meteringPortReservation
	used     map[int]bool
}{next: 20000, reserved: map[int]meteringPortReservation{}, used: map[int]bool{}}

func meteringEphemeralPortRange(t *testing.T) (int, int) {
	t.Helper()
	if runtime.GOOS != "linux" {
		// Linux CI verifies the kernel's actual range below. On other systems
		// keep test listeners in the low non-privileged band and hold both
		// socket reservations until startup; do not assume their system source
		// port range was observed or changed by this fixture.
		return 32768, 65535
	}
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		t.Fatalf("read Linux ephemeral port range without changing network settings: %v", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		t.Fatalf("invalid Linux ephemeral port range %q", raw)
	}
	low, lowErr := strconv.Atoi(fields[0])
	high, highErr := strconv.Atoi(fields[1])
	if lowErr != nil || highErr != nil || low < 1 || high > 65535 || low > high {
		t.Fatalf("invalid Linux ephemeral port range %q", raw)
	}
	return low, high
}

func meteringTestPort(t *testing.T) int {
	t.Helper()
	low, high := meteringEphemeralPortRange(t)
	meteringPorts.Lock()
	defer meteringPorts.Unlock()
	// No port is issued twice during this test process, including ports of
	// already-stopped cores. Reserving both transports also protects native
	// QUIC and SS2022 listeners, not just the TCP stats endpoint.
	for attempt := 0; attempt < 65535-1024+1; attempt++ {
		port := meteringPorts.next
		meteringPorts.next++
		if meteringPorts.next > 65535 {
			meteringPorts.next = 1024
		}
		if (port >= low && port <= high) || meteringPorts.used[port] {
			continue
		}
		address := fmt.Sprintf("127.0.0.1:%d", port)
		tcp, err := net.Listen("tcp4", address)
		if err != nil {
			continue // another process owns this candidate; no traffic was tried
		}
		udp, err := net.ListenPacket("udp4", address)
		if err != nil {
			tcp.Close()
			continue
		}
		meteringPorts.reserved[port] = meteringPortReservation{owner: t, tcp: tcp, udp: udp}
		meteringPorts.used[port] = true
		t.Cleanup(func() {
			meteringPorts.Lock()
			defer meteringPorts.Unlock()
			if lease, ok := meteringPorts.reserved[port]; ok && lease.owner == t {
				lease.tcp.Close()
				lease.udp.Close()
				delete(meteringPorts.reserved, port)
			}
		})
		return port
	}
	t.Fatal("no unused TCP+UDP loopback test port outside the ephemeral range")
	return 0
}

// Release only the imminent core's listening sockets after its config check,
// immediately before cmd.Start. Other cores' planned ports remain reserved.
// Keeping listener ports outside Linux's actual ephemeral range closes the
// remaining release-to-bind window against our own readiness/gRPC dialers.
func releaseMeteringConfigPorts(t *testing.T, raw []byte, api string) {
	t.Helper()
	var config struct {
		Inbounds []struct {
			ListenPort int `json:"listen_port"`
		} `json:"inbounds"`
		Experimental struct {
			V2RayAPI struct {
				Listen string `json:"listen"`
			} `json:"v2ray_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config.Experimental.V2RayAPI.Listen != api {
		t.Fatalf("fixture API %q differs from its actual configured listener %q", api, config.Experimental.V2RayAPI.Listen)
	}
	_, apiPort, err := net.SplitHostPort(api)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(apiPort)
	if err != nil {
		t.Fatal(err)
	}
	ports := map[int]bool{port: true}
	for _, inbound := range config.Inbounds {
		ports[inbound.ListenPort] = true
	}
	meteringPorts.Lock()
	defer meteringPorts.Unlock()
	// Validate the complete set before releasing any of it.
	for port := range ports {
		lease, ok := meteringPorts.reserved[port]
		if !ok || lease.owner != t {
			t.Fatalf("fixture port %d has no reservation owned by this test", port)
		}
	}
	for port := range ports {
		lease := meteringPorts.reserved[port]
		if err := lease.tcp.Close(); err != nil {
			t.Fatal(err)
		}
		if err := lease.udp.Close(); err != nil {
			t.Fatal(err)
		}
		delete(meteringPorts.reserved, port)
	}
}

func startMeteringBox(t *testing.T, bin string, raw []byte, api string) *exec.Cmd {
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
	releaseMeteringConfigPorts(t, raw, api)
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
			return cmd
		}
		time.Sleep(30 * time.Millisecond)
	}
	output, _ := os.ReadFile(filepath.Join(dir, "box.log"))
	t.Fatalf("sing-box fixture did not expose stats API: %s", output)
	return nil
}

// relayFixtureHTTP2LostDownloadWrites counts download writes that this
// fixture core itself logged as failed with x/net's errStreamClosed. Only the
// exact route message is counted; any other error never earns an allowance.
func relayFixtureHTTP2LostDownloadWrites(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	f, ok := cmd.Stdout.(*os.File)
	if !ok {
		return 0
	}
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read fixture log: %v", err)
	}
	return bytes.Count(raw, []byte("connection download closed: http2: stream closed"))
}

// Only inspect the test process started above. This is a point-in-time Linux
// RSS measurement, not peak memory or an estimate for a production VPS.
func meteringBoxRSSKiB(t *testing.T, cmd *exec.Cmd) int64 {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Logf("core RSS measurement unavailable on %s", runtime.GOOS)
		return 0
	}
	if cmd == nil || cmd.Process == nil {
		t.Fatal("RSS requested without a started fixture core")
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "VmRSS:" && fields[2] == "kB" {
			n, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatal("running fixture core has no VmRSS measurement")
	return 0
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
