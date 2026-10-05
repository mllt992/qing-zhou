package store

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qingzhou/internal/sbstats"
	"qingzhou/internal/sbver"
	"qingzhou/internal/singbox"
)

func relayFixtureTLS(t *testing.T) (string, string, map[string]any) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "loopback-test-only"},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	// Trust is explicit in each generated client TLS block. Do not install a
	// system/process-wide CA override that could hide missing relay trust.
	server, err := json.Marshal(map[string]any{"enabled": true, "server_name": "localhost", "certificate": []string{cert}, "key": []string{privateKey}})
	if err != nil {
		t.Fatal(err)
	}
	clientTLS := map[string]any{"enabled": true, "server_name": "localhost", "certificate": []string{cert}}
	client, err := json.Marshal(clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	return string(server), string(client), clientTLS
}

func checkRelayFixtureConfig(t *testing.T, bin string, raw []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(bin, "check", "-c", path).CombinedOutput(); err != nil {
		t.Fatalf("fixture configuration check: %v %s", err, output)
	}
}

// Exercise RFC 1928 UDP ASSOCIATE through the existing mixed listener. No TUN,
// system routes, production endpoints or third-party proxy clients are used.
func relayFixtureUDPEcho(proxy, username, password string, target *net.UDPAddr, payload []byte, measured func(int, time.Duration)) error {
	control, err := net.DialTimeout("tcp", proxy, 5*time.Second)
	if err != nil {
		return err
	}
	defer control.Close()
	if err = control.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	method := byte(0)
	if username != "" {
		method = 2
	}
	if _, err = control.Write([]byte{5, 1, method}); err != nil {
		return err
	}
	var response [2]byte
	if _, err = io.ReadFull(control, response[:]); err != nil {
		return err
	}
	if response != [2]byte{5, method} {
		return fmt.Errorf("SOCKS method rejected: %v", response)
	}
	if method == 2 {
		auth := append([]byte{1, byte(len(username))}, []byte(username)...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, []byte(password)...)
		if _, err = control.Write(auth); err != nil {
			return err
		}
		if _, err = io.ReadFull(control, response[:]); err != nil {
			return err
		}
		if response != [2]byte{1, 0} {
			return fmt.Errorf("SOCKS fixture authentication rejected")
		}
	}
	if _, err = control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	var header [4]byte
	if _, err = io.ReadFull(control, header[:]); err != nil {
		return err
	}
	if header[0] != 5 || header[1] != 0 || header[2] != 0 {
		return fmt.Errorf("UDP association rejected: %v", header)
	}
	readAddress := func(reader io.Reader, atyp byte) (*net.UDPAddr, error) {
		var size int
		switch atyp {
		case 1:
			size = 4
		case 4:
			size = 16
		default:
			return nil, fmt.Errorf("fixture expected numeric SOCKS address, got type %d", atyp)
		}
		address := make([]byte, size+2)
		if _, err := io.ReadFull(reader, address); err != nil {
			return nil, err
		}
		return &net.UDPAddr{IP: net.IP(address[:size]), Port: int(binary.BigEndian.Uint16(address[size:]))}, nil
	}
	bound, err := readAddress(control, header[3])
	if err != nil {
		return err
	}
	if bound.IP.IsUnspecified() {
		bound.IP = net.IPv4(127, 0, 0, 1)
	}
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return err
	}
	defer udp.Close()
	if err = udp.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	packet := []byte{0, 0, 0, 1}
	packet = append(packet, target.IP.To4()...)
	packet = binary.BigEndian.AppendUint16(packet, uint16(target.Port))
	packet = append(packet, payload...)
	for i := 0; i < 3; i++ {
		started := time.Now()
		if _, err = udp.WriteToUDP(packet, bound); err != nil {
			return err
		}
		buffer := make([]byte, 4096)
		n, _, err := udp.ReadFromUDP(buffer)
		if err != nil {
			return err
		}
		if n < 4 || !bytes.Equal(buffer[:3], []byte{0, 0, 0}) {
			return fmt.Errorf("malformed UDP reply")
		}
		reader := bytes.NewReader(buffer[4:n])
		from, err := readAddress(reader, buffer[3])
		if err != nil {
			return err
		}
		body, err := io.ReadAll(reader)
		if err != nil || !from.IP.Equal(target.IP) || from.Port != target.Port || !bytes.Equal(body, payload) {
			return fmt.Errorf("UDP echo payload or destination mismatch: %v", err)
		}
		if measured != nil {
			measured(2*len(payload), time.Since(started))
		}
	}
	return nil
}

// The mixed clients below are local test drivers, not billed machines. Both
// original customer credentials enter the SAME physical inbound and traverse
// the SAME relay links; only their downstream wire identities distinguish owners.
func TestMeteringRelayRealSingboxSharedUserPath(t *testing.T) {
	bin, coreInfo := relayFixtureCore(t)
	for _, legacy := range []struct {
		entryType string
		machines  int
		tlsMode   string
		checkOnly bool
	}{
		{"mixed", 2, "plain", false}, {"vless", 2, "plain", false}, {"vless", 3, "plain", false},
		{"vless", 2, "tls-vision", true}, {"vless", 2, "tls-vision", false},
		{"vless", 2, "ws-tls", true}, {"vless", 2, "ws-tls", false},
	} {
		phase := "traffic"
		if legacy.checkOnly {
			phase = "config-check"
		}
		scenario := relayTrafficScenario{checkOnly: legacy.checkOnly}
		for hop := 0; hop < legacy.machines; hop++ {
			kind := "vless"
			if hop == 0 {
				kind = legacy.entryType
			}
			scenario.hops = append(scenario.hops, relayProtocolFixture{protocol: kind, tlsMode: legacy.tlsMode})
		}
		t.Run(fmt.Sprintf("%s/%d-machines/%s/%s", legacy.entryType, legacy.machines, legacy.tlsMode, phase), func(t *testing.T) {
			runRelaySharedUserPath(t, bin, coreInfo, scenario)
		})
	}
}

func runRelaySharedUserPath(t *testing.T, bin string, coreInfo sbver.Info, scenario relayTrafficScenario) {
	t.Helper()
	st := newRefundStore(t)
	baseConfig := singbox.DefaultBaseConfig
	logLevel := "warn"
	if scenario.hasVision() && os.Getenv("QZ_RELAY_TEST_TRACE") == "1" {
		// Diagnostic logging changes no route, load, body-size assertion or
		// retry behavior in the failing Vision path.
		logLevel = "trace"
		var base map[string]any
		if err := json.Unmarshal([]byte(baseConfig), &base); err != nil {
			t.Fatal(err)
		}
		base["log"] = map[string]any{"level": logLevel, "timestamp": true}
		raw, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		baseConfig = string(raw)
	}
	st.SetSecretKey([]byte("per-user-relay-fixture"))
	if err := st.SetSetting(SettingBlockPrivateEgress, "0"); err != nil {
		t.Fatal(err)
	}
	var tlsServer, tlsClient string
	var clientTLS map[string]any
	if scenario.hasTLS() {
		tlsServer, tlsClient, clientTLS = relayFixtureTLS(t)
	}
	type machine struct {
		id       int64
		inbound  int64
		port     int
		tag      string
		api      string
		stats    *sbstats.Client
		process  *exec.Cmd
		protocol relayProtocolFixture
	}
	machines := make([]machine, len(scenario.hops))
	for i := range machines {
		id, err := st.CreateServer(Server{Name: fmt.Sprintf("fixture-hop-%d", i), Host: "127.0.0.1", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		machines[i] = machine{id: id, port: meteringTestPort(t), tag: fmt.Sprintf("user-relay-hop-%d", i), api: fmt.Sprintf("127.0.0.1:%d", meteringTestPort(t)), protocol: scenario.hops[i]}
	}
	for i := len(machines) - 1; i >= 0; i-- {
		m := &machines[i]
		kind := m.protocol.protocol
		var upstream int64
		if i+1 < len(machines) {
			upstream = machines[i+1].inbound
		}
		var err error
		var tlsID int64
		if m.protocol.hasTLS() {
			tlsID, err = st.SaveSbTls(&SbTls{ServerID: m.id, Name: "fixture-tls", Mode: "tls", ServerJSON: relayFixtureTLSProfile(t, tlsServer, m.protocol), ClientJSON: relayFixtureTLSProfile(t, tlsClient, m.protocol)})
			if err != nil {
				t.Fatal(err)
			}
		}
		optionsRaw, err := json.Marshal(m.protocol.inboundOptions())
		if err != nil {
			t.Fatal(err)
		}
		options := string(optionsRaw)
		m.inbound, err = st.SaveSbInbound(&SbInbound{ServerID: m.id, Type: kind, Tag: m.tag, Listen: "127.0.0.1", ListenPort: m.port, TlsID: tlsID, Options: options, Enabled: true, UpstreamInboundID: upstream})
		if err != nil {
			t.Fatal(err)
		}
	}
	pkg := mkPlan(t, st, "shared-path", 10, 10, 30)
	bindPlanToInbound(t, st, pkg.ID, machines[0].tag)
	type customer struct {
		uid           int64
		bucketID      int64
		original      *User
		wire          singbox.User
		client        *http.Client
		payload       int
		entryHop      int
		socksAddress  string
		socksUsername string
		socksPassword string
	}
	customerCount := 2
	if len(scenario.hops) == 3 {
		customerCount = 3
	}
	customers := make([]customer, customerCount)
	for i := range customers {
		entryHop := 0
		customerPlan := pkg
		if i == 2 {
			entryHop = 1
			customerPlan = mkPlan(t, st, "direct-middle", 10, 10, 30)
			bindPlanToInbound(t, st, customerPlan.ID, machines[1].tag)
		}
		uid, bid := trafficCompatCustomer(t, st, customerPlan, "account", fmt.Sprintf("fixture_customer_%d", i))
		if _, err := st.SetSubTokenIfEmpty(uid, fmt.Sprintf("fixture-subscription-%d", i)); err != nil {
			t.Fatal(err)
		}
		u, err := st.UserByID(uid)
		if err != nil {
			t.Fatal(err)
		}
		customers[i] = customer{uid: uid, bucketID: bid, original: u, payload: (i + 1) * 65536, entryHop: entryHop}
	}
	users, err := st.BuildUsersByTag(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	for i := range customers {
		c := &customers[i]
		mixedEntry := machines[c.entryHop].protocol.protocol == "mixed"
		for _, u := range users[machines[c.entryHop].tag] {
			if (!mixedEntry && u.UUID == c.original.ClientUUID.String && u.Password == c.original.ClientSecret.String) || (mixedEntry && u.Name == c.original.ProxyUsername) {
				c.wire = u
				break
			}
		}
		if c.wire.Name == "" || c.wire.OwnerID != c.uid {
			t.Fatalf("original customer %d absent or misattributed at shared entry", c.uid)
		}
	}
	// Probe the actual candidate executable, never invent a fixed marker.
	for _, machine := range machines {
		if err = st.SetNodeSingbox(machine.id, coreInfo); err != nil {
			t.Fatal(err)
		}
	}
	if err = st.ConfigureTrafficMetering(true, true, true); err != nil {
		t.Fatal(err)
	}
	if err = st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	for i := len(machines) - 1; i >= 0; i-- {
		m := &machines[i]
		raw, err := st.BuildSingboxConfigForServer(m.id, baseConfig, m.api, users)
		if err != nil {
			t.Fatal(err)
		}
		var next *relayProtocolFixture
		if i+1 < len(machines) {
			next = &machines[i+1].protocol
		}
		checkRelayFixtureProtocols(t, raw, m.protocol, next)
		if scenario.checkOnly {
			// Renderer/ack unit fixture only; this is explicitly not evidence
			// of a running core or working TLS/transport connections.
			checkRelayFixtureConfig(t, bin, raw)
		} else {
			m.process = startMeteringBox(t, bin, raw, m.api)
			if err = st.SetNodeVisionRuntime(m.id, coreInfo); err != nil {
				t.Fatal(err)
			}
			t.Logf("fixture machine hop=%d server_id=%d pid=%d stats=%s transport=%s", i, m.id, m.process.Process.Pid, m.api, scenario.description())
		}
		if err = st.RecordRelayConfigApplied(m.id, raw); err != nil {
			t.Fatal(err)
		}
		if !scenario.checkOnly {
			m.stats = sbstats.New(m.api)
			t.Cleanup(func() { m.stats.Close() })
		}
	}
	// A single physical link per hop must carry both users independently.
	links, err := st.RelayMeteringLinks()
	if err != nil || len(links) != len(machines)-1 {
		t.Fatalf("expected shared path with %d links: %+v %v", len(machines)-1, links, err)
	}
	counterNames := make([]map[int64]string, len(machines))
	for i := range machines {
		counterNames[i] = map[int64]string{}
		for _, c := range customers {
			if c.entryHop == i {
				counterNames[i][c.uid] = c.wire.Name
			}
		}
		if i == 0 {
			continue
		}
		rows, err := st.db.Query(`SELECT u.user_id,u.identity_name FROM relay_metering_users u JOIN relay_metering_links l ON l.id=u.link_id WHERE l.target_server_id=? AND u.enabled=1`, machines[i].id)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var uid int64
			var name string
			if err = rows.Scan(&uid, &name); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if _, exists := counterNames[i][uid]; exists {
				t.Fatalf("owner %d has duplicate wire mappings on hop %d", uid, i)
			}
			counterNames[i][uid] = name
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
		if err = rows.Err(); err != nil || len(counterNames[i]) != len(customers) || counterNames[i][customers[0].uid] == counterNames[i][customers[1].uid] {
			t.Fatalf("hop %d did not retain distinct owners: %v %v", i, counterNames[i], err)
		}
	}
	var wsProxy *relayWSReverseProxy
	if scenario.wsStability {
		if len(machines) != 2 || len(customers) != 2 || machines[0].protocol.tlsMode != "ws-tls" {
			t.Fatal("WS stability must extend the two-user/two-machine WS TLS path")
		}
		wsProxy = newRelayWSReverseProxy(t, machines[0].port, tlsServer)
	}
	for i := range customers {
		c := &customers[i]
		proxyPort := machines[c.entryHop].port
		proxy := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", proxyPort), User: url.UserPassword(c.wire.Name, c.wire.Password)}
		c.socksUsername, c.socksPassword = c.wire.Name, c.wire.Password
		if machines[c.entryHop].protocol.protocol != "mixed" {
			proxyPort = meteringTestPort(t)
			api := fmt.Sprintf("127.0.0.1:%d", meteringTestPort(t))
			entryPort := machines[c.entryHop].port
			if wsProxy != nil {
				entryPort = wsProxy.port
			}
			outbound := machines[c.entryHop].protocol.clientOutbound(c.original, entryPort, clientTLS)
			clientConfig := map[string]any{
				"log":          map[string]any{"level": logLevel, "timestamp": true},
				"inbounds":     []any{map[string]any{"type": "mixed", "tag": "fixture-driver", "listen": "127.0.0.1", "listen_port": proxyPort}},
				"outbounds":    []any{outbound},
				"route":        map[string]any{"final": "original-customer"},
				"experimental": map[string]any{"v2ray_api": map[string]any{"listen": api, "stats": map[string]any{"enabled": true, "outbounds": []string{"original-customer"}}}},
			}
			raw, err := json.Marshal(clientConfig)
			if err != nil {
				t.Fatal(err)
			}
			checkRelayFixtureProtocols(t, raw, relayProtocolFixture{protocol: "mixed", tlsMode: "plain"}, &machines[c.entryHop].protocol)
			if scenario.checkOnly {
				checkRelayFixtureConfig(t, bin, raw)
			} else {
				process := startMeteringBox(t, bin, raw, api)
				t.Logf("fixture client owner=%d pid=%d proxy=127.0.0.1:%d stats=%s entry_hop=%d", c.uid, process.Process.Pid, proxyPort, api, c.entryHop)
			}
			proxy = &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", proxyPort)}
			c.socksUsername, c.socksPassword = "", ""
		}
		c.socksAddress = fmt.Sprintf("127.0.0.1:%d", proxyPort)
		transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
		t.Cleanup(transport.CloseIdleConnections)
		c.client = &http.Client{Transport: transport, Timeout: 5 * time.Second}
	}
	if scenario.checkOnly {
		t.Logf("%d original-customer client configurations and %d generated machine configurations passed binary check; no traffic executed in this subtest", len(customers), len(machines))
		return
	}
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := r.Header.Get("X-QZ-Fixture-Request-ID")
		uploadBody, err := io.ReadAll(r.Body)
		uploaded := len(uploadBody)
		if err != nil {
			t.Logf("fixture destination id=%s remote=%s uploaded=%d want_upload=%d upload_error=%v elapsed=%s", requestID, r.RemoteAddr, uploaded, r.ContentLength, err, time.Since(started))
			http.Error(w, "fixture upload failed", http.StatusBadRequest)
			return
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		if n != 65536 && n != 131072 && n != 196608 {
			http.Error(w, "unexpected fixture size", http.StatusBadRequest)
			return
		}
		if !bytes.Equal(uploadBody, bytes.Repeat([]byte{'u'}, n/256)) {
			http.Error(w, "fixture upload payload mismatch", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(n))
		written, writeErr := w.Write(bytes.Repeat([]byte{'x'}, n))
		t.Logf("fixture destination id=%s remote=%s uploaded=%d want_upload=%d written=%d want_response=%d write_error=%v request_context=%v elapsed=%s", requestID, r.RemoteAddr, uploaded, r.ContentLength, written, n, writeErr, r.Context().Err(), time.Since(started))
	}))
	defer destination.Close()
	var metricsMu sync.Mutex
	var tcpCompletedBytes, udpCompletedBytes int64
	var requestSequence atomic.Int64
	request := func(c customer) error {
		started := time.Now()
		requestID := fmt.Sprintf("u%d-r%d", c.uid, requestSequence.Add(1))
		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/?bytes=%d", destination.URL, c.payload), bytes.NewReader(bytes.Repeat([]byte{'u'}, c.payload/256)))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-QZ-Fixture-Request-ID", requestID)
		trace := &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) {
				t.Logf("fixture client id=%s local=%s remote=%s reused=%t idle=%t idle_time=%s", requestID, info.Conn.LocalAddr(), info.Conn.RemoteAddr(), info.Reused, info.WasIdle, info.IdleTime)
			},
			WroteRequest: func(info httptrace.WroteRequestInfo) {
				t.Logf("fixture client id=%s wrote_request_error=%v elapsed=%s", requestID, info.Err, time.Since(started))
			},
			GotFirstResponseByte: func() {
				t.Logf("fixture client id=%s first_response_byte_elapsed=%s", requestID, time.Since(started))
			},
		}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
		response, err := c.client.Do(req)
		if err != nil {
			return fmt.Errorf("request %s owner %d before response, elapsed=%s: %w", requestID, c.uid, time.Since(started), err)
		}
		defer func() {
			t.Logf("fixture client id=%s body_close_error=%v", requestID, response.Body.Close())
		}()
		body, err := io.ReadAll(response.Body)
		t.Logf("fixture client id=%s response_status=%s protocol=%s declared_length=%d actual_length=%d expected_length=%d close=%t transfer_encoding=%v read_error_type=%T read_error=%v elapsed=%s", requestID, response.Status, response.Proto, response.ContentLength, len(body), c.payload, response.Close, response.TransferEncoding, err, err, time.Since(started))
		if err != nil || response.StatusCode != http.StatusOK || len(body) != c.payload || !bytes.Equal(body, bytes.Repeat([]byte{'x'}, c.payload)) {
			return fmt.Errorf("request %s owner %d response status=%s bytes=%d want=%d error=%v", requestID, c.uid, response.Status, len(body), c.payload, err)
		}
		elapsed := time.Since(started)
		completed := int64(len(body) + c.payload/256)
		metricsMu.Lock()
		tcpCompletedBytes += completed
		metricsMu.Unlock()
		t.Logf("synthetic_loopback TCP id=%s user=%d completed_application_bytes=%d request_latency=%s", requestID, c.uid, completed, elapsed)
		return nil
	}
	type snapshot []map[string]*sbstats.Traffic
	read := func() snapshot {
		t.Helper()
		out := make(snapshot, len(machines))
		for i, m := range machines {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var err error
			out[i], err = m.stats.QueryTraffic(ctx, false)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	get := func(s snapshot, hop int, uid int64) UsageDelta {
		if hop >= len(s) {
			return UsageDelta{}
		}
		v := s[hop][counterNames[hop][uid]]
		if v == nil {
			return UsageDelta{}
		}
		return UsageDelta{Up: v.Up, Down: v.Down}
	}
	readSettled := func(phase string, before snapshot, completed map[int64]UsageDelta) snapshot {
		t.Helper()
		started := time.Now()
		state := relayFixtureCounterSettler{
			targets:  map[relayFixtureCounterID]relayFixtureCounterTarget{},
			deadline: started.Add(2 * time.Second), stableFor: 50 * time.Millisecond,
		}
		for hop := range machines {
			for _, c := range customers {
				base := get(before, hop, c.uid)
				delta, active := completed[c.uid]
				active = active && hop >= c.entryHop
				target := relayFixtureCounterTarget{Before: base, Minimum: base, Frozen: !active}
				if active {
					target.Minimum.Up += delta.Up
					target.Minimum.Down += delta.Down
				}
				state.targets[relayFixtureCounterID{Hop: hop, Owner: c.uid}] = target
			}
		}
		for {
			actual := read()
			sample := relayFixtureCounterSample{}
			for key := range state.targets {
				sample[key] = get(actual, key.Hop, key.Owner)
			}
			ready, err := state.observe(time.Now(), sample)
			if err != nil {
				t.Fatalf("counter visibility phase=%s samples=%d elapsed=%s first=%+v last=%+v targets=%+v: %v", phase, state.samples, time.Since(started), state.first, state.last, state.targets, err)
			}
			if ready {
				t.Logf("counter visibility phase=%s samples=%d elapsed=%s first=%+v settled=%+v (read-only polling, no traffic retried)", phase, state.samples, time.Since(started), state.first, state.last)
				return actual
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	tcpStarted := time.Now()
	if err := request(customers[0]); err != nil {
		t.Fatal(err)
	}
	first := readSettled("TCP-first-user", nil, map[int64]UsageDelta{customers[0].uid: {Up: int64(customers[0].payload / 256), Down: int64(customers[0].payload)}})
	for hop := range machines {
		a, b := get(first, hop, customers[0].uid), get(first, hop, customers[1].uid)
		if a.Down < int64(customers[0].payload) || a.Up == 0 || b != (UsageDelta{}) {
			t.Fatalf("first user crossed attribution on hop %d: A=%+v B=%+v", hop, a, b)
		}
	}
	if err := request(customers[1]); err != nil {
		t.Fatal(err)
	}
	second := readSettled("TCP-second-user", first, map[int64]UsageDelta{customers[1].uid: {Up: int64(customers[1].payload / 256), Down: int64(customers[1].payload)}})
	for hop := range machines {
		a, b := get(second, hop, customers[0].uid), get(second, hop, customers[1].uid)
		if a != get(first, hop, customers[0].uid) || b.Down < int64(customers[1].payload) || b.Up == 0 || b.Down <= a.Down {
			t.Fatalf("second user altered another owner's counter on hop %d: A=%+v B=%+v", hop, a, b)
		}
	}
	sequentialTCP := second
	if len(customers) == 3 {
		if err := request(customers[2]); err != nil {
			t.Fatal(err)
		}
		third := readSettled("TCP-middle-entry-user", second, map[int64]UsageDelta{customers[2].uid: {Up: int64(customers[2].payload / 256), Down: int64(customers[2].payload)}})
		sequentialTCP = third
		if get(third, 0, customers[2].uid) != (UsageDelta{}) {
			t.Fatal("direct-middle customer appeared on the upstream machine")
		}
		for hop := 1; hop < len(machines); hop++ {
			if get(third, hop, customers[2].uid).Down < int64(customers[2].payload) {
				t.Fatalf("direct-middle customer missing on hop %d", hop)
			}
			for _, c := range customers[:2] {
				if get(third, hop, c.uid) != get(second, hop, c.uid) {
					t.Fatal("direct-middle customer changed a relayed customer's counter")
				}
			}
		}
	}
	// Repeated simultaneous independent connections exercise auth routing,
	// rather than merely showing that one warm connection can pass bytes.
	batches := 1
	stressBatches := func(name string) {
		if os.Getenv(name) == "" {
			return
		}
		n, parseErr := strconv.Atoi(os.Getenv(name))
		if parseErr != nil || n < 1 || n > 1000 {
			t.Fatal(name + " must be 1..1000")
		}
		if n > batches {
			batches = n
		}
	}
	if scenario.hasVision() {
		stressBatches("QZ_VISION_STRESS_BATCHES")
	}
	// #87: sustained concurrent pressure on every Trojan path. Each request is
	// independent and every failure in a batch is reported (never retried).
	// CI/Release already set QZ_VISION_STRESS_BATCHES for real-core runs; Trojan
	// paths reuse that count unless QZ_TROJAN_STRESS_BATCHES overrides it.
	if scenario.hasTrojan() {
		if os.Getenv("QZ_TROJAN_STRESS_BATCHES") != "" {
			stressBatches("QZ_TROJAN_STRESS_BATCHES")
		} else {
			stressBatches("QZ_VISION_STRESS_BATCHES")
		}
	}
	for batch := 0; batch < batches; batch++ {
		var wg sync.WaitGroup
		errors := make(chan error, 4*len(customers))
		for round := 0; round < 4; round++ {
			for _, c := range customers {
				wg.Add(1)
				go func(c customer) { defer wg.Done(); errors <- request(c) }(c)
			}
		}
		wg.Wait()
		close(errors)
		failed := 0
		for err := range errors {
			if err != nil {
				failed++
				t.Errorf("batch %d/%d: %v", batch+1, batches, err)
			}
		}
		if failed > 0 {
			t.Fatalf("batch %d/%d: %d of %d concurrent requests failed (no retry)", batch+1, batches, failed, 4*len(customers))
		}
	}
	t.Logf("completed stress batches=%d concurrent_requests_per_batch=%d without retry", batches, 4*len(customers))
	tcpElapsed := time.Since(tcpStarted)
	t.Logf("synthetic_loopback TCP completed_application_bytes=%d traffic_phase_elapsed=%s application_MiB_per_second=%.3f (includes sequential probes and concurrent requests; not WAN capacity)", tcpCompletedBytes, tcpElapsed, float64(tcpCompletedBytes)/(1<<20)/tcpElapsed.Seconds())
	tcpCompleted := map[int64]UsageDelta{}
	for _, c := range customers {
		tcpCompleted[c.uid] = UsageDelta{Up: int64(4 * batches * c.payload / 256), Down: int64(4 * batches * c.payload)}
	}
	finalTCP := readSettled("TCP-concurrent-users", sequentialTCP, tcpCompleted)
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err = echo.WriteTo(buf[:n], from); err != nil {
				return
			}
		}
	}()
	beforeUDP := finalTCP
	udpStarted := time.Now()
	for i, c := range customers {
		payload := bytes.Repeat([]byte{byte('a' + i)}, 257+i*252)
		if err = relayFixtureUDPEcho(c.socksAddress, c.socksUsername, c.socksPassword, echo.LocalAddr().(*net.UDPAddr), payload, func(completed int, elapsed time.Duration) {
			udpCompletedBytes += int64(completed)
			t.Logf("synthetic_loopback UDP user=%d completed_application_bytes=%d echo_latency=%s", c.uid, completed, elapsed)
		}); err != nil {
			t.Fatalf("owner %d UDP: %v", c.uid, err)
		}
		afterUDP := readSettled(fmt.Sprintf("UDP-owner-%d", c.uid), beforeUDP, map[int64]UsageDelta{c.uid: {Up: int64(3 * len(payload)), Down: int64(3 * len(payload))}})
		for hop := range machines {
			for _, other := range customers {
				got, before := get(afterUDP, hop, other.uid), get(beforeUDP, hop, other.uid)
				if other.uid != c.uid || hop < c.entryHop {
					if got != before {
						t.Fatalf("UDP owner %d changed unrelated hop=%d owner=%d", c.uid, hop, other.uid)
					}
				} else if got.Up-before.Up < int64(3*len(payload)) || got.Down-before.Down < int64(3*len(payload)) {
					t.Fatalf("UDP owner %d not independently measured at hop %d: before=%+v after=%+v", c.uid, hop, before, got)
				}
			}
		}
		beforeUDP = afterUDP
	}
	// Independent simultaneous UDP associations exercise shared QUIC and
	// multiplex listeners after the A-only/B-only attribution probes.
	var udpWG sync.WaitGroup
	udpErrors := make(chan error, len(customers))
	for i, c := range customers {
		payload := bytes.Repeat([]byte{byte('k' + i)}, 313+i*214)
		udpWG.Add(1)
		go func(c customer, payload []byte) {
			defer udpWG.Done()
			err := relayFixtureUDPEcho(c.socksAddress, c.socksUsername, c.socksPassword, echo.LocalAddr().(*net.UDPAddr), payload, func(completed int, elapsed time.Duration) {
				metricsMu.Lock()
				udpCompletedBytes += int64(completed)
				metricsMu.Unlock()
				t.Logf("synthetic_loopback concurrent UDP user=%d completed_application_bytes=%d echo_latency=%s", c.uid, completed, elapsed)
			})
			if err != nil {
				udpErrors <- fmt.Errorf("concurrent UDP owner %d: %w", c.uid, err)
			}
		}(c, payload)
	}
	udpWG.Wait()
	close(udpErrors)
	for err := range udpErrors {
		t.Fatal(err)
	}
	udpCompleted := map[int64]UsageDelta{}
	for i, c := range customers {
		udpCompleted[c.uid] = UsageDelta{Up: int64(3 * (313 + i*214)), Down: int64(3 * (313 + i*214))}
	}
	afterConcurrentUDP := readSettled("UDP-concurrent-users", beforeUDP, udpCompleted)
	for i, c := range customers {
		for hop := range machines {
			got, before := get(afterConcurrentUDP, hop, c.uid), get(beforeUDP, hop, c.uid)
			if hop < c.entryHop {
				if got != before {
					t.Fatalf("concurrent UDP middle-entry owner %d appeared on upstream hop %d", c.uid, hop)
				}
			} else if got.Up-before.Up < int64(3*(313+i*214)) || got.Down-before.Down < int64(3*(313+i*214)) {
				t.Fatalf("concurrent UDP owner %d not independently measured on hop %d: before=%+v after=%+v", c.uid, hop, before, got)
			}
		}
	}
	beforeUDP = afterConcurrentUDP
	udpElapsed := time.Since(udpStarted)
	t.Logf("synthetic_loopback UDP completed_application_bytes=%d traffic_phase_elapsed=%s application_MiB_per_second=%.3f (includes associations and counter reads; not WAN capacity)", udpCompletedBytes, udpElapsed, float64(udpCompletedBytes)/(1<<20)/udpElapsed.Seconds())
	final := beforeUDP
	if scenario.wsStability {
		drivers := make([]relayWSStabilityClient, len(customers))
		for i, c := range customers {
			drivers[i] = relayWSStabilityClient{owner: c.uid, client: c.client}
		}
		runRelayWSStabilityTraffic(t, drivers, wsProxy, func(phase string, completed map[int64]UsageDelta) {
			final = readSettled("WS-stability-"+phase, final, completed)
		})
	}
	for _, data := range []snapshot{first, second, finalTCP, final} {
		for i, m := range machines {
			deltas := map[string]UsageDelta{}
			for name, v := range data[i] {
				deltas[name] = UsageDelta{Up: v.Up, Down: v.Down}
			}
			p := NewTrafficPoll(m.id, deltas)
			p.Mode, p.Epoch = "cumulative", fmt.Sprintf("fixture-process-%d", i)
			trafficCompatRecord(t, st, p)
			trafficCompatRecord(t, st, p)
		}
	}
	for i, m := range machines {
		t.Logf("synthetic_loopback core hop=%d pid=%d resident_memory_KiB=%d (point-in-time RSS, not peak or VPS sizing)", i, m.process.Process.Pid, meteringBoxRSSKiB(t, m.process))
		report, err := st.ServerServiceTraffic(m.id, 0)
		if err != nil {
			t.Fatal(err)
		}
		byUser := map[int64]UsageDelta{}
		for _, src := range report.Sources {
			got := byUser[src.UserID]
			byUser[src.UserID] = UsageDelta{Up: got.Up + src.Up, Down: got.Down + src.Down}
		}
		var total int64
		var wantBillable int64
		for _, c := range customers {
			want := get(final, i, c.uid)
			if (i >= c.entryHop && want.Down < int64(5*c.payload)) || byUser[c.uid] != want {
				t.Fatalf("hop %d owner %d report=%+v raw=%+v", i, c.uid, byUser[c.uid], want)
			}
			total += want.Up + want.Down
			if c.entryHop == i {
				wantBillable += want.Up + want.Down
			}
			t.Logf("hop=%d user=%d raw_up=%d raw_down=%d report_up=%d report_down=%d", i, c.uid, want.Up, want.Down, byUser[c.uid].Up, byUser[c.uid].Down)
		}
		if report.Total != total || report.BillableTotal != wantBillable || !report.ObservedUserCoverageComplete {
			t.Fatalf("hop %d report total=%d billable=%d observed_complete=%t; want total=%d billable=%d observed_complete=true", i, report.Total, report.BillableTotal, report.ObservedUserCoverageComplete, total, wantBillable)
		}
		t.Logf("hop=%d observed_complete=%t attribution_ready=%t coverage_reasons=%v", i, report.ObservedUserCoverageComplete, report.AttributionReady, report.CoverageReasons)
	}
	for _, c := range customers {
		want := get(final, c.entryHop, c.uid)
		trafficCompatTotals(t, st, c.uid, c.bucketID, want.Up, want.Down)
		u, err := st.UserByID(c.uid)
		if err != nil || u.ClientUUID != c.original.ClientUUID || u.ClientSecret != c.original.ClientSecret || u.ProxyUsername != c.original.ProxyUsername || u.ProxyPassword != c.original.ProxyPassword || u.SubToken != c.original.SubToken {
			t.Fatalf("owner %d original credentials/subscription changed: %v", c.uid, err)
		}
		t.Logf("user=%d quota=%d entry_raw=%d (downstream excluded)", c.uid, ledgerUsed(t, st, c.uid), want.Up+want.Down)
	}
}
