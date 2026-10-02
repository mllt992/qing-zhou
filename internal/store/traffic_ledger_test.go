package store

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func trafficLedgerUser(t *testing.T) (*Store, int64) {
	t.Helper()
	st := newRefundStore(t)
	uid := mkUser(t, st, "ledger-user")
	if err := st.EnsurePoolBucket(uid, "qz_ledger_user", "uuid", "secret"); err != nil {
		t.Fatal(err)
	}
	return st, uid
}
func ledgerUsed(t *testing.T, st *Store, uid int64) int64 {
	t.Helper()
	var n int64
	if err := st.db.QueryRow(`SELECT used_up+used_down FROM users WHERE id=?`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestTrafficLedgerKeepsRelayAndUnknownWithoutBilling(t *testing.T) {
	st, uid := trafficLedgerUser(t)
	sid, _ := st.CreateServer(Server{Name: "landing", Host: "example.test", Enabled: true})
	ib, err := st.SaveSbInbound(&SbInbound{ServerID: sid, Type: "trojan", Tag: "ledger-landing", ListenPort: 8443, Enabled: true, Options: `{}`, RelaySecret: "fixture-only"})
	if err != nil {
		t.Fatal(err)
	}
	p := NewTrafficPoll(sid, map[string]UsageDelta{"qz_ledger_user": {Up: 10, Down: 20}, fmt.Sprintf("relay_%d", ib): {Up: 50, Down: 70}, "removed-user": {Up: 3, Down: 4}})
	if _, err = st.RecordTrafficPoll(p); err != nil {
		t.Fatal(err)
	}
	if n := ledgerUsed(t, st, uid); n != 30 {
		t.Fatalf("charged %d, want 30", n)
	}
	r, err := st.ServerServiceTraffic(sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 157 || r.BillableTotal != 30 || len(r.Sources) != 3 || r.UserCoverageComplete {
		t.Fatalf("report %+v", r)
	}
	for i := 0; i < 3; i++ {
		if _, err = st.RecordTrafficPoll(p); err != nil {
			t.Fatal(err)
		}
	}
	if n := ledgerUsed(t, st, uid); n != 30 {
		t.Fatalf("replay charged %d", n)
	}
	p.Traffic["qz_ledger_user"] = UsageDelta{Up: 40}
	if _, err = st.RecordTrafficPoll(p); err == nil {
		t.Fatal("mutable poll ID accepted")
	}
}
func TestTrafficLedgerConcurrentIdempotency(t *testing.T) {
	st, uid := trafficLedgerUser(t)
	p := NewTrafficPoll(0, map[string]UsageDelta{"qz_ledger_user": {Up: 100, Down: 200}})
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := st.RecordTrafficPoll(p); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := ledgerUsed(t, st, uid); n != 300 {
		t.Fatalf("charged %d", n)
	}
}
func TestTrafficLedgerCumulativeEpochAndOrdering(t *testing.T) {
	st, uid := trafficLedgerUser(t)
	now := time.Now().Unix()
	poll := func(id, epoch string, at, up, down int64, baseline bool) {
		t.Helper()
		_, err := st.RecordTrafficPoll(TrafficPoll{ID: id, ServerID: 0, ObservedAt: at, Mode: "cumulative", Epoch: epoch, Baseline: baseline, Traffic: map[string]UsageDelta{"qz_ledger_user": {Up: up, Down: down}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	poll("a", "epoch1", now, 100, 200, true)
	poll("b", "epoch1", now+10, 140, 260, false)
	poll("c", "epoch1", now+5, 120, 250, false)
	if n := ledgerUsed(t, st, uid); n != 100 {
		t.Fatalf("charged %d", n)
	}
	poll("d", "epoch1", now+20, 3, 5, false)
	poll("e", "epoch1", now+30, 8, 10, false)
	poll("f", "epoch2", now+40, 20, 30, false)
	if n := ledgerUsed(t, st, uid); n != 150 {
		t.Fatalf("epoch charged %d", n)
	}
	r, err := st.ServerServiceTraffic(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Quality.Gaps != 5 {
		t.Fatalf("gaps %+v", r.Quality)
	}
}
func TestTrafficLedgerPartialPollRetriesWithoutDoubleDebit(t *testing.T) {
	st, a := trafficLedgerUser(t)
	b := mkUser(t, st, "broken-ledger")
	if err := st.EnsurePoolBucket(b, "qz_broken_ledger", "uuid2", "sec2"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER fail_one_usage BEFORE UPDATE OF used_up ON user_plans WHEN OLD.user_id=` + fmt.Sprint(b) + ` BEGIN SELECT RAISE(FAIL,'test write failure'); END`); err != nil {
		t.Fatal(err)
	}
	p := NewTrafficPoll(0, map[string]UsageDelta{"qz_ledger_user": {Up: 10}, "qz_broken_ledger": {Down: 20}})
	if _, err := st.RecordTrafficPoll(p); err == nil {
		t.Fatal("expected pending write failure")
	}
	if n := ledgerUsed(t, st, a); n != 10 {
		t.Fatalf("successful identity lost: %d", n)
	}
	if _, err := st.db.Exec(`DROP TRIGGER fail_one_usage`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RetryPendingTrafficPolls(); err != nil {
		t.Fatal(err)
	}
	if n := ledgerUsed(t, st, a); n != 10 {
		t.Fatalf("successful identity replayed: %d", n)
	}
	if n := ledgerUsed(t, st, b); n != 20 {
		t.Fatalf("pending identity not recovered: %d", n)
	}
}
func TestTrafficLedgerRejectsInvalidCounters(t *testing.T) {
	st, _ := trafficLedgerUser(t)
	for _, d := range []UsageDelta{{Up: -1}, {Down: -1}, {Up: 1 << 62, Down: 1 << 62}} {
		if _, err := st.RecordTrafficPoll(NewTrafficPoll(0, map[string]UsageDelta{"qz_ledger_user": d})); err == nil {
			t.Fatalf("accepted %+v", d)
		}
	}
	for _, name := range []string{"relay_123", "qzr_test"} {
		if ValidateProxyUsername(name) == nil {
			t.Fatalf("reserved identity accepted: %s", name)
		}
	}
}

func TestTrafficLedgerLegacyNameCollisionNeverDebitsCustomer(t *testing.T) {
	st, uid := trafficLedgerUser(t)
	sid, _ := st.CreateServer(Server{Name: "landing", Host: "192.0.2.1", Enabled: true})
	ib, err := st.SaveSbInbound(&SbInbound{ServerID: sid, Type: "trojan", Tag: "collision", ListenPort: 8443, Options: `{}`, Enabled: true, RelaySecret: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("relay_%d", ib)
	// Simulate a pre-upgrade customer-chosen username; new writes reject it.
	if _, err = st.db.Exec(`UPDATE user_plans SET proxy_username=? WHERE user_id=?`, name, uid); err != nil {
		t.Fatal(err)
	}
	if _, err = st.RecordTrafficPoll(NewTrafficPoll(sid, map[string]UsageDelta{name: {Down: 500}})); err != nil {
		t.Fatal(err)
	}
	if n := ledgerUsed(t, st, uid); n != 0 {
		t.Fatalf("relay collision billed customer %d", n)
	}
	r, err := st.ServerServiceTraffic(sid, 0)
	if err != nil || len(r.Sources) != 1 || r.Sources[0].Kind != "ambiguous_identity" {
		t.Fatalf("collision not isolated %+v %v", r, err)
	}
}
func TestTrafficLedgerRetainsDedupeAfterRawPrune(t *testing.T) {
	st, uid := trafficLedgerUser(t)
	p := NewTrafficPoll(0, map[string]UsageDelta{"qz_ledger_user": {Down: 100}})
	p.ObservedAt = time.Now().AddDate(0, 0, -100).Unix()
	if _, err := st.RecordTrafficPoll(p); err != nil {
		t.Fatal(err)
	}
	if err := st.PruneTrafficLedger(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordTrafficPoll(p); err != nil {
		t.Fatal(err)
	}
	if n := ledgerUsed(t, st, uid); n != 100 {
		t.Fatalf("pruned replay charged %d", n)
	}
	var daily int64
	if err := st.db.QueryRow(`SELECT SUM(down) FROM machine_traffic_daily`).Scan(&daily); err != nil || daily != 100 {
		t.Fatalf("daily history lost %d %v", daily, err)
	}
}

func TestTrafficLedgerSameSecondLateEpochNeverRewinds(t *testing.T) {
	st, uid := trafficLedgerUser(t)
	now := time.Now().Unix()
	makePoll := func(epoch string, down int64) TrafficPoll {
		seq, err := st.NextTrafficSequence()
		if err != nil {
			t.Fatal(err)
		}
		p := NewTrafficPoll(0, map[string]UsageDelta{"qz_ledger_user": {Down: down}})
		p.Sequence = seq
		p.ObservedAt = now
		p.Mode = "cumulative"
		p.Epoch = epoch
		return p
	}
	record := func(p TrafficPoll) {
		t.Helper()
		if _, err := st.RecordTrafficPoll(p); err != nil {
			t.Fatal(err)
		}
	}
	record(makePoll("old", 100))
	late := makePoll("old", 120)
	record(makePoll("new", 10))
	record(late)
	record(makePoll("new", 15))
	if got := ledgerUsed(t, st, uid); got != 135 {
		t.Fatalf("late epoch double debit: %d", got)
	}
	_, epoch, err := st.TrafficCollectionState(0)
	if err != nil || epoch != "new" {
		t.Fatalf("current epoch rewound: %s %v", epoch, err)
	}
	// Same-second decreased stock cannot lower the cursor and recharge the rebound.
	record(makePoll("new", 5))
	record(makePoll("new", 16))
	if got := ledgerUsed(t, st, uid); got != 136 {
		t.Fatalf("counter rebound charged old stock: %d", got)
	}
	old := makePoll("new", 18)
	recent := makePoll("new", 20)
	record(recent)
	record(old)
	if got := ledgerUsed(t, st, uid); got != 140 {
		t.Fatalf("same-second reordered snapshot: %d", got)
	}
	// Wall clock moves backwards while the durable sequence still increases.
	backward := makePoll("new", 22)
	backward.ObservedAt = now - 10
	record(backward)
	if got := ledgerUsed(t, st, uid); got != 142 {
		t.Fatalf("clock rollback lost current usage: %d", got)
	}
}

func TestTrafficLedgerOldPendingEpochRecoversAfterNewEpoch(t *testing.T) {
	st, uid := trafficLedgerUser(t)
	if _, err := st.db.Exec(`CREATE TRIGGER fail_epoch_usage BEFORE UPDATE OF used_up ON user_plans BEGIN SELECT RAISE(FAIL,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	p := NewTrafficPoll(0, map[string]UsageDelta{"qz_ledger_user": {Down: 100}})
	p.Mode = "cumulative"
	p.Epoch = "old"
	if _, err := st.RecordTrafficPoll(p); err == nil {
		t.Fatal("expected pending journal")
	}
	if _, err := st.db.Exec(`DROP TRIGGER fail_epoch_usage`); err != nil {
		t.Fatal(err)
	}
	next := NewTrafficPoll(0, map[string]UsageDelta{"qz_ledger_user": {Down: 10}})
	next.Mode = "cumulative"
	next.Epoch = "new"
	next.ObservedAt = p.ObservedAt
	if _, err := st.RecordTrafficPoll(next); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RetryPendingTrafficPolls(); err != nil {
		t.Fatal(err)
	}
	next.ID = "after-old-replay"
	next.Traffic["qz_ledger_user"] = UsageDelta{Down: 15}
	if _, err := st.RecordTrafficPoll(next); err != nil {
		t.Fatal(err)
	}
	if got := ledgerUsed(t, st, uid); got != 115 {
		t.Fatalf("old epoch replay lost or duplicated: %d", got)
	}
	_, epoch, err := st.TrafficCollectionState(0)
	if err != nil || epoch != "new" {
		t.Fatalf("old pending poll changed current epoch: %s %v", epoch, err)
	}
}

func TestTrafficLedgerDeletedLegacyRelayCannotBecomeCustomer(t *testing.T) {
	st, uid := trafficLedgerUser(t)
	if _, err := st.db.Exec(`UPDATE user_plans SET proxy_username='relay_999999' WHERE user_id=?`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordTrafficPoll(NewTrafficPoll(7, map[string]UsageDelta{"relay_999999": {Down: 500}})); err != nil {
		t.Fatal(err)
	}
	if got := ledgerUsed(t, st, uid); got != 0 {
		t.Fatalf("unmapped reserved legacy identity billed %d", got)
	}
	r, err := st.ServerServiceTraffic(7, 0)
	if err != nil || r.Total != 500 || r.BillableTotal != 0 || r.Sources[0].Kind != "unknown" {
		t.Fatalf("late unknown relay not preserved %+v %v", r, err)
	}
}

func TestTrafficLedgerPendingBindingSurvivesRenameAndTransfer(t *testing.T) {
	for _, account := range []bool{false, true} {
		t.Run(fmt.Sprint("account=", account), func(t *testing.T) {
			st, a := trafficLedgerUser(t)
			b := mkUser(t, st, "next-name-owner")
			if err := st.EnsurePoolBucket(b, "qz_next_owner", "uuid-next", "fixture"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.Exec(`UPDATE user_plans SET traffic_limit=100000,expiry_at=?`, time.Now().Add(time.Hour).Unix()); err != nil {
				t.Fatal(err)
			}
			table := "user_plans"
			key := "user_id"
			if account {
				plan := mkPlan(t, st, "account-ledger", 10, 100, 30)
				buy(t, st, a, plan)
				buy(t, st, b, plan)
				table = "users"
				key = "id"
			}
			if _, err := st.db.Exec(`UPDATE `+table+` SET proxy_username='shared_name' WHERE `+key+`=?`, a); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.Exec(`CREATE TRIGGER fail_bound BEFORE UPDATE OF used_up ON user_plans WHEN OLD.user_id=` + fmt.Sprint(a) + ` BEGIN SELECT RAISE(FAIL,'fixture'); END`); err != nil {
				t.Fatal(err)
			}
			p := NewTrafficPoll(0, map[string]UsageDelta{"shared_name": {Down: 100}})
			if _, err := st.RecordTrafficPoll(p); err == nil {
				t.Fatal("expected pending")
			}
			if _, err := st.db.Exec(`UPDATE `+table+` SET proxy_username='renamed_owner' WHERE `+key+`=?`, a); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.Exec(`UPDATE `+table+` SET proxy_username='shared_name' WHERE `+key+`=?`, b); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.Exec(`DROP TRIGGER fail_bound`); err != nil {
				t.Fatal(err)
			}
			if _, err := st.RetryPendingTrafficPolls(); err != nil {
				t.Fatal(err)
			}
			if got := ledgerUsed(t, st, a); got != 100 {
				t.Fatalf("original owner lost sample %d", got)
			}
			if got := ledgerUsed(t, st, b); got != 0 {
				t.Fatalf("new name owner charged old sample %d", got)
			}
			if _, err := st.RecordTrafficPoll(p); err != nil {
				t.Fatal(err)
			}
			if ledgerUsed(t, st, a) != 100 || ledgerUsed(t, st, b) != 0 {
				t.Fatal("binding replay not idempotent")
			}
		})
	}
}

func TestTrafficLedgerUnavailableAccountDoesNotBlockOtherUser(t *testing.T) {
	st, a := trafficLedgerUser(t)
	b := mkUser(t, st, "no-entitlement")
	if _, err := st.db.Exec(`UPDATE users SET proxy_username='unfunded_account' WHERE id=?`, b); err != nil {
		t.Fatal(err)
	}
	p := NewTrafficPoll(0, map[string]UsageDelta{"qz_ledger_user": {Down: 10}, "unfunded_account": {Down: 20}})
	if _, err := st.RecordTrafficPoll(p); err == nil {
		t.Fatal("unavailable account must remain pending")
	}
	if ledgerUsed(t, st, a) != 10 || ledgerUsed(t, st, b) != 0 {
		t.Fatal("one unavailable binding blocked unrelated identity")
	}
	if _, err := st.RetryPendingTrafficPolls(); err == nil {
		t.Fatal("unresolved owner lost pending state")
	}
	if ledgerUsed(t, st, a) != 10 {
		t.Fatal("replay duplicated successful identity")
	}
}
