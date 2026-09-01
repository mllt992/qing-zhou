package store

import (
	"errors"
	"testing"
)

// Adding and subtracting on a live plan bucket must move traffic_limit and the
// users.* mirror together, without touching used bytes or the order row.
func TestAdjustBucketTraffic_AddAndSubtract(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "ada")
	pkg := mkPlan(t, st, "100G/30d", 100, 100, 30)
	buy(t, st, uid, pkg)
	bs := planBuckets(t, st, uid, pkg.ID)
	if len(bs) != 1 {
		t.Fatalf("setup: want 1 bucket, got %d", len(bs))
	}

	got, err := st.AdjustBucketTraffic(uid, bs[0].ID, 20*giB)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrafficLimit != 120*giB {
		t.Errorf("after +20G: limit = %d GiB, want 120", got.TrafficLimit/giB)
	}
	if got.Used() != 0 {
		t.Errorf("used changed to %d, want 0", got.Used())
	}

	got, err = st.AdjustBucketTraffic(uid, bs[0].ID, -50*giB)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrafficLimit != 70*giB {
		t.Errorf("after -50G: limit = %d GiB, want 70", got.TrafficLimit/giB)
	}

	var agg int64
	st.db.QueryRow(`SELECT traffic_limit FROM users WHERE id=?`, uid).Scan(&agg)
	if agg != 70*giB {
		t.Errorf("users.traffic_limit = %d GiB, want 70", agg/giB)
	}
}

// Spent traffic cannot be un-spent by shrinking the limit below used.
func TestAdjustBucketTraffic_RefusesBelowUsed(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "bea")
	pkg := mkPlan(t, st, "100G/30d", 100, 100, 30)
	buy(t, st, uid, pkg)
	bs := planBuckets(t, st, uid, pkg.ID)
	setBucketUsed(t, st, uid, "plan", 0, 40*giB)

	if _, err := st.AdjustBucketTraffic(uid, bs[0].ID, -70*giB); !errors.Is(err, ErrTrafficFloor) {
		t.Errorf("err = %v, want ErrTrafficFloor", err)
	}
	// Exactly down to used is allowed: remaining becomes 0.
	got, err := st.AdjustBucketTraffic(uid, bs[0].ID, -60*giB)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrafficLimit != 40*giB {
		t.Errorf("limit = %d GiB, want 40 (clamped to used)", got.TrafficLimit/giB)
	}
}

// Shrinking the active head onto its used bytes exhausts it, so a queued份
// behind it must promote in the same transaction.
func TestAdjustBucketTraffic_ExhaustPromotesQueued(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "cara")
	pkg := mkPlan(t, st, "100G/30d", 100, 100, 30)
	buy(t, st, uid, pkg)
	buy(t, st, uid, pkg)
	bs := planBuckets(t, st, uid, pkg.ID)
	if len(bs) != 2 || bs[0].Status != "active" || bs[1].Status != "queued" {
		t.Fatalf("setup: want active+queued, got %+v", bs)
	}
	// Spend 99G on the head only (queued stays unused), then take the last 1G of
	// quota so it lands exactly on used and is no longer a usable head.
	if _, err := st.db.Exec(`UPDATE user_plans SET used_down=? WHERE id=?`, 99*giB, bs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AdjustBucketTraffic(uid, bs[0].ID, -1*giB); err != nil {
		t.Fatal(err)
	}

	after := planBuckets(t, st, uid, pkg.ID)
	if len(after) != 2 {
		t.Fatalf("want both份 kept, got %d", len(after))
	}
	var head, next *Bucket
	for _, b := range after {
		switch b.ID {
		case bs[0].ID:
			head = b
		case bs[1].ID:
			next = b
		}
	}
	if next == nil || next.Status != "active" {
		t.Fatalf("queued份 status = %v, want active (promoted)", next)
	}
	if next.ExpiryAt == 0 {
		t.Error("promoted份 has no expiry — its countdown never started")
	}
	if head == nil || head.Status != StatusRetired {
		t.Errorf("exhausted head status = %v, want retired", head)
	}
}

// Zero is an empty finite balance for both pool and plan buckets, so an admin can
// add traffic to either one.
func TestAdjustBucketTraffic_ZeroBucketsCanBeFunded(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "dina")
	if err := st.EnsurePoolBucket(uid, "qz_dina_pool", "u", "s"); err != nil {
		t.Fatal(err)
	}
	var poolID int64
	if err := st.db.QueryRow(`SELECT id FROM user_plans WHERE user_id=? AND kind='pool'`, uid).Scan(&poolID); err != nil {
		t.Fatal(err)
	}
	got, err := st.AdjustBucketTraffic(uid, poolID, 10*giB)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrafficLimit != 10*giB {
		t.Errorf("pool limit = %d GiB, want 10", got.TrafficLimit/giB)
	}

	pkg := mkPlan(t, st, "待补额度", 100, 1, 30)
	buy(t, st, uid, pkg)
	bs := planBuckets(t, st, uid, pkg.ID)
	if len(bs) != 1 {
		t.Fatalf("setup: want 1 plan, got %d", len(bs))
	}
	if _, err := st.db.Exec(`UPDATE user_plans SET traffic_limit=0 WHERE id=?`, bs[0].ID); err != nil {
		t.Fatal(err)
	}
	got, err = st.AdjustBucketTraffic(uid, bs[0].ID, 10*giB)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrafficLimit != 10*giB {
		t.Errorf("plan limit = %d GiB, want 10", got.TrafficLimit/giB)
	}
}

// A time-expired or retired份 will never spend again. Growing its number would
// only inflate the users.* mirror. A still-active exhausted head is different:
// with nothing queued, adding quota is how it comes back.
func TestAdjustBucketTraffic_RefusesFinished(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "faye")
	pkg := mkPlan(t, st, "100G/30d", 100, 100, 30)
	buy(t, st, uid, pkg)
	bs := planBuckets(t, st, uid, pkg.ID)
	if _, err := st.db.Exec(`UPDATE user_plans SET expiry_at=1 WHERE id=?`, bs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AdjustBucketTraffic(uid, bs[0].ID, giB); !errors.Is(err, ErrBucketFinished) {
		t.Errorf("expired plan err = %v, want ErrBucketFinished", err)
	}

	uid2 := mkUser(t, st, "gail")
	buy(t, st, uid2, pkg)
	buy(t, st, uid2, pkg)
	pair := planBuckets(t, st, uid2, pkg.ID)
	if _, err := st.db.Exec(`UPDATE user_plans SET used_down=? WHERE id=?`, 99*giB, pair[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AdjustBucketTraffic(uid2, pair[0].ID, -1*giB); err != nil {
		t.Fatal(err)
	}
	after := planBuckets(t, st, uid2, pkg.ID)
	var retired *Bucket
	for _, b := range after {
		if b.ID == pair[0].ID {
			retired = b
		}
	}
	if retired == nil || retired.Status != StatusRetired {
		t.Fatalf("setup: head status = %v, want retired", retired)
	}
	if _, err := st.AdjustBucketTraffic(uid2, retired.ID, giB); !errors.Is(err, ErrBucketFinished) {
		t.Errorf("retired plan err = %v, want ErrBucketFinished", err)
	}

	uid3 := mkUser(t, st, "hope")
	buy(t, st, uid3, pkg)
	lone := planBuckets(t, st, uid3, pkg.ID)
	if _, err := st.db.Exec(`UPDATE user_plans SET used_down=? WHERE id=?`, 100*giB, lone[0].ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.AdjustBucketTraffic(uid3, lone[0].ID, 10*giB)
	if err != nil {
		t.Fatalf("exhausted but still-active head: %v", err)
	}
	if got.TrafficLimit != 110*giB {
		t.Errorf("rescued head limit = %d GiB, want 110", got.TrafficLimit/giB)
	}
}

func TestAdjustBucketTraffic_RefusesFreeAndZero(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "eve")
	if err := st.EnsureFreeBucket(uid, "eve"); err != nil {
		t.Fatal(err)
	}
	var fid int64
	if err := st.db.QueryRow(`SELECT id FROM user_plans WHERE user_id=? AND kind=?`, uid, KindFree).Scan(&fid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AdjustBucketTraffic(uid, fid, giB); !errors.Is(err, ErrBucketProtected) {
		t.Errorf("free bucket err = %v, want ErrBucketProtected", err)
	}

	pkg := mkPlan(t, st, "50G/30d", 50, 50, 30)
	buy(t, st, uid, pkg)
	bs := planBuckets(t, st, uid, pkg.ID)
	if _, err := st.AdjustBucketTraffic(uid, bs[0].ID, 0); !errors.Is(err, ErrZeroDelta) {
		t.Errorf("zero delta err = %v, want ErrZeroDelta", err)
	}
	if _, err := st.AdjustBucketTraffic(uid+1, bs[0].ID, giB); !errors.Is(err, ErrBucketNotFound) {
		t.Errorf("wrong owner err = %v, want ErrBucketNotFound", err)
	}
}
