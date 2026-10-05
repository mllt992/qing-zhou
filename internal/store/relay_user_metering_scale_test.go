package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
	"qingzhou/internal/sbver"
	"qingzhou/internal/singbox"
)

// Instrument the actual SQLite driver, not an inferred query count. Counts are
// executed SQL statements; transaction BEGIN/COMMIT/ROLLBACK are excluded.
type meteringCountDriver struct {
	base  driver.Driver
	count *atomic.Int64
}

func (d meteringCountDriver) Open(name string) (driver.Conn, error) {
	c, e := d.base.Open(name)
	if e != nil {
		return nil, e
	}
	return &meteringCountConn{Conn: c, count: d.count}, nil
}

type meteringCountConn struct {
	driver.Conn
	count *atomic.Int64
}

func (c *meteringCountConn) ExecContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	c.count.Add(1)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, a)
}
func (c *meteringCountConn) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	c.count.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, a)
}
func (c *meteringCountConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
}
func meterSQL(t *testing.T, st *Store) *atomic.Int64 {
	t.Helper()
	if err := st.db.Close(); err != nil {
		t.Fatal(err)
	}
	count := new(atomic.Int64)
	name := fmt.Sprintf("metering-count-%d", time.Now().UnixNano())
	sql.Register(name, meteringCountDriver{base: &sqlite.Driver{}, count: count})
	db, err := sql.Open(name, st.path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	st.db = db
	return count
}

// SQL/config-only runs explicitly model an installed capability for planner
// preflight. Runs with a binary instead probe that exact candidate executable;
// neither branch records a running-process observation before startup succeeds.
func meteringScaleCore(t *testing.T) (string, sbver.Info) {
	t.Helper()
	if os.Getenv("QZ_SINGBOX_TEST_BIN") != "" || os.Getenv("QZ_SINGBOX_REQUIRE_STATS") == "1" {
		return relayFixtureCore(t)
	}
	info := sbver.Parse("sing-box version " + sbver.TrojanHandshakeFixVersion + "\nTags: with_v2ray_api")
	info.Raw = "synthetic planner-only scale capability; no executable or running process was observed"
	t.Log("SQL/config-only scale fixture uses explicitly synthetic installed capability; no running-core or traffic evidence")
	return "", info
}

func recordMeteringScaleCapabilities(t *testing.T, st *Store, info sbver.Info, serverIDs ...int64) {
	t.Helper()
	for _, id := range serverIDs {
		if err := st.SetNodeSingbox(id, info); err != nil {
			t.Fatal(err)
		}
	}
}

// Opt-in scale diagnostics are reproducible local/CI evidence, not a promise
// about a production VPS, WAN transport, or concurrent application workload.
func TestMeteringRelayUserScale(t *testing.T) {
	if os.Getenv("QZ_METERING_SCALE") != "1" {
		t.Skip("set QZ_METERING_SCALE=1 for 100/1000-user diagnostics")
	}
	bin, coreInfo := meteringScaleCore(t)
	for _, protocol := range []meteringProtocolCase{
		{name: "vless", protocol: "vless"},
		{name: "anytls", protocol: "anytls"},
		{name: "tuic", protocol: "tuic"},
		{name: "hysteria2", protocol: "hysteria2"},
	} {
		for _, size := range []int{100, 1000} {
			t.Run(fmt.Sprintf("%s/%d", protocol.name, size), func(t *testing.T) {
				st := newRefundStore(t)
				st.SetSecretKey([]byte("isolated-scale-fixture"))
				a, _ := st.CreateServer(Server{Name: "entry", Host: "192.0.2.1", Enabled: true})
				b, _ := st.CreateServer(Server{Name: "landing", Host: "192.0.2.2", Enabled: true})
				aAPI, bAPI := fmt.Sprintf("127.0.0.1:%d", meteringTestPort(t)), fmt.Sprintf("127.0.0.1:%d", meteringTestPort(t))
				landing := saveMeteringProtocolInbound(t, st, &SbInbound{ServerID: b, Tag: "scale-landing", Listen: "127.0.0.1", ListenPort: meteringTestPort(t), Enabled: true}, protocol)
				saveMeteringProtocolInbound(t, st, &SbInbound{ServerID: a, Tag: "scale-entry", Listen: "127.0.0.1", ListenPort: meteringTestPort(t), Enabled: true, UpstreamInboundID: landing}, protocol)
				var err error
				pkg := mkPlan(t, st, "scale", 1, 100, 30)
				bindPlanToInbound(t, st, pkg.ID, "scale-entry")
				for i := 0; i < size; i++ {
					trafficCompatCustomer(t, st, pkg, "account", fmt.Sprintf("scale_user_%04d", i))
				}
				recordMeteringScaleCapabilities(t, st, coreInfo, a, b)
				if err = st.ConfigureTrafficMetering(true, true, true); err != nil {
					t.Fatal(err)
				}
				count := meterSQL(t, st)
				count.Store(0)
				started := time.Now()
				if err = st.PrepareRelayMetering(); err != nil {
					t.Fatal(err)
				}
				prepareTime, prepareSQL := time.Since(started), count.Load()
				users, err := st.BuildUsersByTag(time.Now().Unix())
				if err != nil {
					t.Fatal(err)
				}
				down, err := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, bAPI, users)
				if err != nil {
					t.Fatal(err)
				}
				count.Store(0)
				started = time.Now()
				if err = st.RecordRelayConfigApplied(b, down); err != nil {
					t.Fatal(err)
				}
				landingAckTime, landingAckSQL := time.Since(started), count.Load()
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				count.Store(0)
				started = time.Now()
				up, err := st.BuildSingboxConfigForServer(a, singbox.DefaultBaseConfig, aAPI, users)
				if err != nil {
					t.Fatal(err)
				}
				buildTime, buildSQL := time.Since(started), count.Load()
				runtime.ReadMemStats(&after)
				var cfg struct {
					Outbounds []json.RawMessage `json:"outbounds"`
					Route     struct {
						Rules []json.RawMessage `json:"rules"`
					} `json:"route"`
				}
				if err = json.Unmarshal(up, &cfg); err != nil {
					t.Fatal(err)
				}
				if len(cfg.Outbounds) != size+2 || len(cfg.Route.Rules) != size+2 {
					t.Fatalf("per-user config missing branches: outbounds=%d rules=%d, want %d each", len(cfg.Outbounds), len(cfg.Route.Rules), size+2)
				}
				// An unchanged prepare/build must converge to byte-identical configuration.
				count.Store(0)
				started = time.Now()
				if err = st.RecordRelayConfigApplied(a, up); err != nil {
					t.Fatal(err)
				}
				sourceAckTime, sourceAckSQL := time.Since(started), count.Load()
				var active int
				if err = st.db.QueryRow(`SELECT COUNT(*) FROM relay_metering_users WHERE enabled=1 AND state='active'`).Scan(&active); err != nil || active != size {
					t.Fatalf("source apply acknowledged %d/%d identities: %v", active, size, err)
				}
				count.Store(0)
				started = time.Now()
				if err = st.PrepareRelayMetering(); err != nil {
					t.Fatal(err)
				}
				unchanged, err := st.BuildSingboxConfigForServer(a, singbox.DefaultBaseConfig, aAPI, users)
				if err != nil {
					t.Fatal(err)
				}
				noopTime, noopSQL := time.Since(started), count.Load()
				if string(up) != string(unchanged) {
					t.Fatal("no-op compile changed config")
				}
				rows, err := st.db.Query(`SELECT identity_name FROM relay_metering_users ORDER BY id`)
				if err != nil {
					t.Fatal(err)
				}
				traffic := map[string]UsageDelta{}
				first := ""
				for rows.Next() {
					var name string
					if err = rows.Scan(&name); err != nil {
						t.Fatal(err)
					}
					traffic[name] = UsageDelta{Up: 100, Down: 1000}
					first = name
				}
				if err = rows.Close(); err != nil {
					t.Fatal(err)
				}
				if len(traffic) != size {
					t.Fatalf("per-user identities %d want %d", len(traffic), size)
				}
				poll := NewTrafficPoll(b, traffic)
				poll.Mode = "cumulative"
				poll.Epoch = "scale-epoch"
				count.Store(0)
				started = time.Now()
				if _, err = st.RecordTrafficPoll(poll); err != nil {
					t.Fatal(err)
				}
				ingestTime, ingestSQL := time.Since(started), count.Load()
				count.Store(0)
				started = time.Now()
				report, err := st.ServerServiceTraffic(b, 0)
				if err != nil {
					t.Fatal(err)
				}
				reportTime, reportSQL := time.Since(started), count.Load()
				if len(report.Users) != size || report.Total != int64(size*1100) || report.BillableTotal != 0 {
					t.Fatalf("scale accounting: users=%d total=%d charged=%d", len(report.Users), report.Total, report.BillableTotal)
				}
				plan, err := st.db.Query(`EXPLAIN QUERY PLAN SELECT r.link_id,r.user_id,l.target_server_id FROM relay_metering_users r LEFT JOIN relay_metering_links l ON l.id=r.link_id WHERE r.identity_name=?`, first)
				if err != nil {
					t.Fatal(err)
				}
				var details []string
				for plan.Next() {
					var id, parent, unused int
					var detail string
					if err = plan.Scan(&id, &parent, &unused, &detail); err != nil {
						t.Fatal(err)
					}
					details = append(details, detail)
				}
				plan.Close()
				for _, detail := range details {
					if strings.Contains(detail, "SCAN ") {
						t.Fatalf("identity lookup scans: %v", details)
					}
				}
				if bin != "" {
					started = time.Now()
					landingCore := startMeteringBox(t, bin, down, bAPI)
					landingStart := time.Since(started)
					if err = st.SetNodeVisionRuntime(b, coreInfo); err != nil {
						t.Fatal(err)
					}
					started = time.Now()
					entryCore := startMeteringBox(t, bin, up, aAPI)
					entryStart := time.Since(started)
					if err = st.SetNodeVisionRuntime(a, coreInfo); err != nil {
						t.Fatal(err)
					}
					t.Logf("synthetic loopback idle cores protocol=%s users=%d landing_check_start=%s landing_RSS_KiB=%d entry_check_start=%s entry_RSS_KiB=%d (instant RSS, not peak, active-connection capacity or production budget)", protocol.name, size, landingStart, meteringBoxRSSKiB(t, landingCore), entryStart, meteringBoxRSSKiB(t, entryCore))
				}
				t.Logf("landing_ack=%s/%dSQL source_ack=%s/%dSQL", landingAckTime, landingAckSQL, sourceAckTime, sourceAckSQL)
				t.Logf("protocol=%s users=%d prepare=%s/%dSQL compile=%s/%dSQL alloc=%dBytes entry_config=%dBytes landing_config=%dBytes outbounds=%d rules=%d noop=%s/%dSQL ingest=%s/%dSQL report=%s/%dSQL plan=%v", protocol.name, size, prepareTime, prepareSQL, buildTime, buildSQL, after.TotalAlloc-before.TotalAlloc, len(up), len(down), len(cfg.Outbounds), len(cfg.Route.Rules), noopTime, noopSQL, ingestTime, ingestSQL, reportTime, reportSQL, details)
			})
		}
	}
}

func TestMeteringScaleFixtureCapabilityPreflightRemainsStrict(t *testing.T) {
	t.Setenv("QZ_SINGBOX_TEST_BIN", "")
	t.Setenv("QZ_SINGBOX_REQUIRE_STATS", "")
	bin, info := meteringScaleCore(t)
	if bin != "" || !info.HasTransportReadBufferFix || !strings.Contains(info.Raw, "synthetic planner-only") {
		t.Fatalf("SQL-only scale capability was not explicitly labelled: bin=%q info=%+v", bin, info)
	}
	for _, test := range []string{"reviewed-fixture", "missing-node", "old-vision", "without-stats"} {
		t.Run(test, func(t *testing.T) {
			f := visionCapabilityFixture(t, 2, `{"transport":{"type":"ws","path":"/scale"}}`)
			recordMeteringScaleCapabilities(t, f.st, info, f.servers...)
			switch test {
			case "missing-node":
				if err := f.st.DeleteNodeSingbox(f.servers[1]); err != nil {
					t.Fatal(err)
				}
			case "old-vision":
				old := sbver.Parse("sing-box version " + sbver.VisionFramingFixVersion + "\nTags: with_v2ray_api")
				recordMeteringScaleCapabilities(t, f.st, old, f.servers[1])
			case "without-stats":
				withoutAPI := info
				withoutAPI.HasV2RayAPI = false
				recordMeteringScaleCapabilities(t, f.st, withoutAPI, f.servers[1])
			}
			err := f.st.ConfigureTrafficMetering(true, true, true)
			if test == "reviewed-fixture" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "WebSocket/HTTPUpgrade") || f.st.RelayUserMeteringEnabled() {
				t.Fatalf("scale setup weakened the transport gate for %s: %v", test, err)
			}
			var runtimeObservations int
			if err := f.st.db.QueryRow(`SELECT COUNT(*) FROM node_vision_runtime`).Scan(&runtimeObservations); err != nil || runtimeObservations != 0 {
				t.Fatalf("planner preflight manufactured running-process evidence: count=%d err=%v", runtimeObservations, err)
			}
		})
	}
}

func TestMeteringScaleFixtureUsesActualCandidateObservation(t *testing.T) {
	if os.Getenv("QZ_SINGBOX_TEST_BIN") == "" {
		t.Skip("set QZ_SINGBOX_TEST_BIN to verify actual scale candidate version observation")
	}
	bin, info := meteringScaleCore(t)
	if bin != os.Getenv("QZ_SINGBOX_TEST_BIN") || strings.Contains(info.Raw, "synthetic") || !info.HasTransportReadBufferFix {
		t.Fatalf("scale fixture did not use its actual candidate probe: bin=%q info=%+v", bin, info)
	}
	f := visionCapabilityFixture(t, 2, `{"transport":{"type":"ws","path":"/scale"}}`)
	recordMeteringScaleCapabilities(t, f.st, info, f.servers...)
	if err := f.st.ConfigureTrafficMetering(true, true, true); err != nil {
		t.Fatal(err)
	}
	observations, err := f.st.NodeSingboxAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range f.servers {
		got := observations[id]
		if got == nil || got.Version != info.Version || got.Raw != info.Raw || !got.HasTransportReadBufferFix || !got.HasV2RayAPI {
			t.Fatalf("server %d did not retain actual candidate observation: %+v", id, got)
		}
	}
	var runtimeObservations int
	if err := f.st.db.QueryRow(`SELECT COUNT(*) FROM node_vision_runtime`).Scan(&runtimeObservations); err != nil || runtimeObservations != 0 {
		t.Fatalf("version probe was misrepresented as a running process: count=%d err=%v", runtimeObservations, err)
	}
}
