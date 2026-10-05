package store

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"qingzhou/internal/sbver"
)

func reportObservation(t *testing.T, st *Store, serverID, ts int64, kind string, linkID, userID, up, down int64, billable bool) {
	t.Helper()
	_, err := st.db.Exec(`INSERT INTO traffic_observations(poll_id,counter_name,server_id,ts,source_kind,link_id,user_id,up,down,billable) VALUES(?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("report:%d:%s:%d:%d:%d", serverID, kind, linkID, userID, ts), "test-observation", serverID, ts, kind, linkID, userID, up, down, billable)
	if err != nil {
		t.Fatal(err)
	}
}
func reportHealthy(t *testing.T, st *Store, serverID int64) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT INTO traffic_metering_state(server_id,status,last_success) VALUES(?,'ok',100) ON CONFLICT(server_id) DO UPDATE SET status='ok'`, serverID); err != nil {
		t.Fatal(err)
	}
}

func TestServiceTrafficAggregatesEachUserWithinEachMachine(t *testing.T) {
	st, uid := trafficLedgerUser(t)
	reportObservation(t, st, 7, 100, "direct_user", 0, uid, 10, 20, true)
	reportObservation(t, st, 7, 100, "relay_user", 11, uid, 20, 30, false)
	reportObservation(t, st, 7, 100, "relay_user", 12, uid, 30, 40, false)
	reportObservation(t, st, 7, 100, "diagnostic_outbound", 11, 0, 300, 600, false)
	reportObservation(t, st, 7, 50, "historical_user", 0, uid, 200, 400, false)
	reportObservation(t, st, 8, 100, "relay_user", 11, uid, 100, 900, false)
	reportHealthy(t, st, 7)
	reportHealthy(t, st, 8)
	a, err := st.ServerServiceTraffic(7, 100)
	if err != nil {
		t.Fatal(err)
	}
	if a.Total != 150 || a.BillableTotal != 30 || len(a.Users) != 1 || len(a.Sources) != 3 || len(a.OutboundLinks) != 1 || a.OutboundLinks[0].Total != 900 || a.UnallocatedTotal != 0 {
		t.Fatalf("unexpected report: %+v", a)
	}
	u := a.Users[0]
	if u.UserID != uid || u.Name != "ledger-user" || u.Up != 60 || u.Down != 90 || u.Total != 150 || u.DirectTotal != 30 || u.RelayTotal != 120 || u.BillableTotal != 30 {
		t.Fatalf("bad user aggregation: %+v", u)
	}
	if !a.HasRelay || !a.ObservedUserCoverageComplete || !a.AttributionReady || !a.UserCoverageComplete {
		t.Fatalf("attributable relay rows alone must not mark coverage partial: %+v", a)
	}
	b, err := st.ServerServiceTraffic(8, 100)
	if err != nil || b.Total != 1000 || b.BillableTotal != 0 || len(b.Users) != 1 || b.Users[0].Total != 1000 || b.Users[0].RelayTotal != 1000 {
		t.Fatalf("independent landing measurements changed: %+v %v", b, err)
	}
}

func TestServiceTrafficKeepsMixedSharedAndHistoricalBytesUnallocated(t *testing.T) {
	st, uid := trafficLedgerUser(t)
	reportObservation(t, st, 7, 100, "direct_user", 0, uid, 5, 20, true)
	// Report debit totals also defensively exclude relay observations if a bad
	// imported row ever carries billable=1.
	reportObservation(t, st, 7, 100, "relay_user", 11, uid, 10, 40, true)
	for i, row := range []struct {
		kind       string
		uid, total int64
	}{{"historical_user", uid, 11}, {"relay_link", 0, 20}, {"legacy_shared_relay", 0, 30}, {"unknown", 0, 40}, {"ambiguous_identity", 0, 9}, {"direct_user", 0, 5}} {
		reportObservation(t, st, 7, 101+int64(i), row.kind, 0, row.uid, 0, row.total, false)
	}
	reportHealthy(t, st, 7)
	r, err := st.ServerServiceTraffic(7, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 190 || r.UnallocatedTotal != 115 || r.BillableTotal != 25 || len(r.Users) != 1 || r.Users[0].Total != 75 || r.Users[0].BillableTotal != 25 || r.UserCoverageComplete || r.ObservedUserCoverageComplete || !slices.Contains(r.CoverageReasons, "unallocated_observations") {
		t.Fatalf("shared bytes were allocated or billed: %+v", r)
	}
	if r.Users[0].Total+r.UnallocatedTotal != r.Total {
		t.Fatal("machine service does not reconcile with users and explicit unallocated bytes")
	}
}

func TestServiceTrafficSeparatesObservedCoverageFromRelayReadiness(t *testing.T) {
	st, _, b, _, bi, _ := meteringRelayFixture(t)
	uid := mkUser(t, st, "relay-report-owner")
	if err := st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	links, err := st.RelayMeteringLinks()
	if err != nil || len(links) != 1 {
		t.Fatalf("links: %v %v", links, err)
	}
	link := links[0]
	if _, err = st.db.Exec(`UPDATE relay_metering_links SET state='active' WHERE id=?`, link.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.SetSetting("relay_user_metering", "true"); err != nil {
		t.Fatal(err)
	}
	// A Trojan landing needs runtime proof of the exact #87 marker; an installed
	// (planner) record alone is not attribution readiness.
	if r, e := st.ServerServiceTraffic(b, 0); e != nil || r.AttributionReady || !slices.Contains(r.CoverageReasons, "trojan_core_unverified") {
		t.Fatalf("Trojan landing without runtime proof was not flagged: %+v %v", r, e)
	}
	if err = st.SetNodeVisionRuntime(b, sbver.Parse("sing-box version "+sbver.TransportReadBufferFixVersion+"\nTags: with_v2ray_api")); err != nil {
		t.Fatal(err)
	}
	if r, e := st.ServerServiceTraffic(b, 0); e != nil || !slices.Contains(r.CoverageReasons, "trojan_core_unverified") {
		t.Fatalf("transport-only runtime marker satisfied the Trojan requirement: %+v %v", r, e)
	}
	if err = st.SetNodeVisionRuntime(b, sbver.Parse("sing-box version "+sbver.TrojanHandshakeFixVersion+"\nTags: with_v2ray_api")); err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.Exec(`INSERT INTO relay_metering_users(link_id,user_id,generation,identity_name,credential,state,created_at) VALUES(?,?,?,'report-relay-current','fixture','active',100)`, link.ID, uid, link.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.Exec(`UPDATE sb_inbounds SET relay_secret='fixture' WHERE id=?`, bi); err != nil {
		t.Fatal(err)
	}
	reportObservation(t, st, b, 100, "relay_user", link.ID, uid, 10, 20, false)
	reportHealthy(t, st, b)
	read := func() ServiceTraffic {
		t.Helper()
		r, e := st.ServerServiceTraffic(b, 0)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	r := read()
	if !r.ObservedUserCoverageComplete || r.AttributionReady || r.UserCoverageComplete || r.UnallocatedTotal != 0 || !slices.Contains(r.CoverageReasons, "shared_compatibility_active") {
		t.Fatalf("shared compatibility misrepresented observed bytes: %+v", r)
	}
	if _, err = st.db.Exec(`INSERT INTO relay_legacy_compatibility(server_id,inbound_id,state,updated_at) VALUES(?,?,'retired',100)`, b, bi); err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.Exec(`UPDATE relay_metering_generations SET retired_at=100,retirement_applied_at=0 WHERE link_id=?`, link.ID); err != nil {
		t.Fatal(err)
	}
	if read().AttributionReady {
		t.Fatal("requested retirement was treated as an applied retirement")
	}
	if _, err = st.db.Exec(`UPDATE relay_metering_generations SET retirement_applied_at=100 WHERE link_id=?`, link.ID); err != nil {
		t.Fatal(err)
	}
	// Old per-user generations retain immutable attribution but cannot block the
	// readiness of the currently deployed generation.
	if _, err = st.db.Exec(`INSERT INTO relay_metering_users(link_id,user_id,generation,identity_name,credential,state,created_at) VALUES(?,?,0,'report-relay-old','fixture','prepared',99)`, link.ID, uid); err != nil {
		t.Fatal(err)
	}
	r = read()
	if !r.HasRelay || !r.AttributionReady || !r.UserCoverageComplete {
		t.Fatalf("fully attributable current generation stayed blocked: %+v", r)
	}
	if _, err = st.db.Exec(`UPDATE relay_metering_users SET state='accepted' WHERE identity_name='report-relay-current'`); err != nil {
		t.Fatal(err)
	}
	r = read()
	if r.AttributionReady || !r.ObservedUserCoverageComplete || !slices.Contains(r.CoverageReasons, "relay_users_not_active") {
		t.Fatalf("unactivated user accepted as deployment-ready: %+v", r)
	}
}

func TestServiceTrafficRequiresActualManagedRouteReadiness(t *testing.T) {
	st, _, b, _, _, _ := meteringRelayFixture(t)
	uid := mkUser(t, st, "pending-route-owner")
	reportObservation(t, st, b, 100, "relay_user", 0, uid, 0, 25, false)
	reportHealthy(t, st, b)
	r, err := st.ServerServiceTraffic(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !r.ObservedUserCoverageComplete || r.AttributionReady || !slices.Contains(r.CoverageReasons, "relay_route_not_ready") || !slices.Contains(r.CoverageReasons, "per_user_metering_disabled") {
		t.Fatalf("unregistered active path called ready: %+v", r)
	}
}

func TestServiceTrafficMissingPollsAndGapsKeepObservedCoveragePartial(t *testing.T) {
	st, uid := trafficLedgerUser(t)
	reportObservation(t, st, 7, 100, "relay_user", 1, uid, 1, 2, false)
	reportHealthy(t, st, 7)
	if _, err := st.db.Exec(`INSERT INTO traffic_metering_gaps(server_id,ts,reason,poll_id) VALUES(7,100,'test-gap','test-gap')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO traffic_polls(id,sequence,server_id,observed_at,mode,payload,digest,state,created_at) VALUES('report-pending',1,7,100,'reset','{}','fixture','pending',100)`); err != nil {
		t.Fatal(err)
	}
	r, err := st.ServerServiceTraffic(7, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.UserCoverageComplete || r.ObservedUserCoverageComplete || r.UnallocatedTotal != 0 || r.Quality.Gaps != 1 || r.Quality.PendingPolls != 1 {
		t.Fatalf("missing bytes guessed from known user: %+v", r)
	}
}

func TestServiceTrafficReadSnapshotDoesNotReserveWriterLock(t *testing.T) {
	st, uid := trafficLedgerUser(t)
	reportObservation(t, st, 7, 100, "direct_user", 0, uid, 1, 2, true)
	reportHealthy(t, st, 7)
	// Hold the normal BEGIN IMMEDIATE writer open. A reporting BEGIN IMMEDIATE
	// would block here; a WAL read-only snapshot must still return old data.
	writer, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err = writer.Exec(`UPDATE traffic_observations SET down=12 WHERE server_id=7`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		r   ServiceTraffic
		err error
	}
	done := make(chan result, 1)
	go func() { r, e := st.ServerServiceTraffic(7, 0); done <- result{r, e} }()
	select {
	case got := <-done:
		if got.err != nil || got.r.Total != 3 || len(got.r.Users) != 1 || got.r.Users[0].Total != 3 {
			t.Fatalf("report did not read a consistent committed snapshot: %+v %v", got.r, got.err)
		}
	case <-time.After(2 * time.Second):
		writer.Rollback()
		<-done
		t.Fatal("read-only report blocked behind an unrelated writer")
	}
	if err = writer.Commit(); err != nil {
		t.Fatal(err)
	}
	r, err := st.ServerServiceTraffic(7, 0)
	if err != nil || r.Total != 13 || r.Users[0].Total != 13 {
		t.Fatalf("report did not see later commit: %+v %v", r, err)
	}
}
