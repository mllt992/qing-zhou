package store

import (
	"testing"
	"time"
)

func mkMonthly(t *testing.T, st *Store, name string, monthlyGiB, days int64, advance bool) *Package {
	t.Helper()
	p := Package{Type: "plan", Name: name, PricePoints: 100, MonthlyReset: true, MonthlyBytes: monthlyGiB * giB,
		DurationDays: days, Stock: -1, Enabled: true, AllowAdvance: advance}
	p.NormalizeMonthly()
	id, err := st.CreatePackage(p)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetPackage(id)
	return got
}

func activeHead(t *testing.T, st *Store, uid int64) *Bucket {
	t.Helper()
	for _, b := range chainBuckets(t, st, uid) {
		if b.Status == "active" {
			return b
		}
	}
	t.Fatal("no active份")
	return nil
}

func setUsed(t *testing.T, st *Store, id, bytes int64) {
	t.Helper()
	if _, err := st.db.Exec(`UPDATE user_plans SET used_up=0, used_down=? WHERE id=?`, bytes, id); err != nil {
		t.Fatal(err)
	}
}

// shiftStart moves the份's activation back by secs (period_start and expiry).
func shiftStart(t *testing.T, st *Store, id, secs int64) {
	t.Helper()
	if _, err := st.db.Exec(`UPDATE user_plans SET period_start=period_start-?, expiry_at=expiry_at-? WHERE id=?`, secs, secs, id); err != nil {
		t.Fatal(err)
	}
}

func TestPeriod_ShowsMonthlyQuotaNotSum(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "m1")
	pkg := mkMonthly(t, st, "季付", 200, 90, false)
	if pkg.TrafficBytes != 600*giB {
		t.Fatalf("total=%d, want 600G", pkg.TrafficBytes/giB)
	}
	buy(t, st, uid, pkg)
	h := activeHead(t, st, uid)
	if !h.Periodic() || h.TrafficLimit != 200*giB || h.ShownQuota() != 200*giB {
		t.Fatalf("head limit=%dG periodic=%v", h.TrafficLimit/giB, h.Periodic())
	}
	if r := h.NextResetAt(); r != h.PeriodStart+30*86400 {
		t.Fatalf("next reset=%d want start+30d", r)
	}
}

func TestPeriod_ExhaustionPausesButDoesNotAdvanceQueue(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "m2")
	pkg := mkMonthly(t, st, "月付", 100, 30, false)
	buy(t, st, uid, pkg)
	buy(t, st, uid, pkg)
	h := activeHead(t, st, uid)
	setUsed(t, st, h.ID, 100*giB)
	if _, err := st.AdvanceQueueFor(uid); err != nil {
		t.Fatal(err)
	}
	h2 := activeHead(t, st, uid)
	if h2.ID != h.ID {
		t.Fatal("queue advanced on traffic exhaustion; must wait for time expiry")
	}
	if h2.Active(time.Now().Unix()) {
		t.Fatal("exhausted period must pause the份")
	}
	// Time expiry hands over.
	rollOver(t, st, uid)
	if activeHead(t, st, uid).ID == h.ID {
		t.Fatal("queue did not advance on expiry")
	}
}

func TestPeriod_ResetRefillsAndRevives(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "m3")
	pkg := mkMonthly(t, st, "季付", 200, 90, false)
	buy(t, st, uid, pkg)
	h := activeHead(t, st, uid)
	setUsed(t, st, h.ID, 200*giB)
	start := h.PeriodStart
	shiftStart(t, st, h.ID, 31*86400)
	revived, err := st.ResetDuePeriods()
	if err != nil {
		t.Fatal(err)
	}
	if len(revived) != 1 || revived[0] != uid {
		t.Fatalf("revived=%v", revived)
	}
	h = activeHead(t, st, uid)
	if h.Used() != 0 || h.LifeUsedDown != 200*giB || h.TrafficLimit != 200*giB {
		t.Fatalf("after reset used=%d life=%d limit=%d", h.Used(), h.LifeUsedDown, h.TrafficLimit)
	}
	if h.PeriodStart != start-31*86400+30*86400 {
		t.Fatalf("period anchored wrong: %d", h.PeriodStart)
	}
	if !h.Active(time.Now().Unix()) {
		t.Fatal("reset did not restore service")
	}
	logs, _ := st.ListPeriodResets(uid, 10)
	if len(logs) != 1 || logs[0].Used != 200*giB {
		t.Fatalf("reset log=%+v", logs)
	}
	// Idempotent.
	if r, _ := st.ResetDuePeriods(); len(r) != 0 {
		t.Fatal("second sweep reset again")
	}
}

func TestPeriod_CatchUpAfterDowntimeNoCarry(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "m4")
	pkg := mkMonthly(t, st, "季付", 200, 90, true)
	buy(t, st, uid, pkg)
	h := activeHead(t, st, uid)
	setUsed(t, st, h.ID, 250*giB) // borrowed 50G
	start := h.PeriodStart
	shiftStart(t, st, h.ID, 65*86400)
	if _, err := st.ResetDuePeriods(); err != nil {
		t.Fatal(err)
	}
	h = activeHead(t, st, uid)
	if h.PeriodStart != start-65*86400+60*86400 {
		t.Fatal("did not catch up to the right period")
	}
	if h.PeriodBase != 200*giB {
		t.Fatalf("base=%dG; the borrowed period already elapsed, no deduction", h.PeriodBase/giB)
	}
}

func TestPeriod_AdvanceBorrowsNextPeriod(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "m5")
	pkg := mkMonthly(t, st, "季付", 200, 90, true)
	buy(t, st, uid, pkg)
	h := activeHead(t, st, uid)
	if h.TrafficLimit != 400*giB || h.ShownQuota() != 200*giB {
		t.Fatalf("limit=%dG shown=%dG", h.TrafficLimit/giB, h.ShownQuota()/giB)
	}
	setUsed(t, st, h.ID, 250*giB)
	h = activeHead(t, st, uid)
	if h.Advanced() != 50*giB || !h.Active(time.Now().Unix()) {
		t.Fatalf("advanced=%d", h.Advanced())
	}
	shiftStart(t, st, h.ID, 30*86400)
	if _, err := st.ResetDuePeriods(); err != nil {
		t.Fatal(err)
	}
	h = activeHead(t, st, uid)
	if h.PeriodBase != 150*giB {
		t.Fatalf("next base=%dG want 150G", h.PeriodBase/giB)
	}
}

func TestPeriod_AdvanceNeverBeyondLastPeriod(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "m6")
	pkg := mkMonthly(t, st, "月付", 100, 30, true)
	buy(t, st, uid, pkg)
	h := activeHead(t, st, uid)
	if h.TrafficLimit != 100*giB {
		t.Fatalf("single last period must not be borrowable: limit=%dG", h.TrafficLimit/giB)
	}
	buy(t, st, uid, pkg) // a queued month makes a next period exist
	if h = activeHead(t, st, uid); h.TrafficLimit != 200*giB {
		t.Fatalf("with a queued month limit=%dG want 200G", h.TrafficLimit/giB)
	}
	setUsed(t, st, h.ID, 130*giB)
	rollOver(t, st, uid)
	if h2 := activeHead(t, st, uid); h2.PeriodBase != 70*giB || h2.TrafficLimit != 70*giB {
		t.Fatalf("next month base=%dG limit=%dG want 70G", h2.PeriodBase/giB, h2.TrafficLimit/giB)
	}
}

func TestPeriod_PerUserOverride(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "m7")
	pkg := mkMonthly(t, st, "季付", 200, 90, false)
	buy(t, st, uid, pkg)
	h := activeHead(t, st, uid)
	if err := st.SetLineAdvance(uid, h.QueueKey, 1); err != nil {
		t.Fatal(err)
	}
	if h = activeHead(t, st, uid); h.TrafficLimit != 400*giB || !h.AllowAdvance {
		t.Fatalf("override on: limit=%dG", h.TrafficLimit/giB)
	}
	buy(t, st, uid, pkg)
	for _, b := range chainBuckets(t, st, uid) {
		if b.AdvanceOverride != 1 {
			t.Fatal("new份 did not inherit the per-user override")
		}
	}
	if err := st.SetLineAdvance(uid, h.QueueKey, -1); err != nil {
		t.Fatal(err)
	}
	if h = activeHead(t, st, uid); h.TrafficLimit != 200*giB {
		t.Fatal("back to package default must drop borrowing")
	}
}

func TestPeriod_FinalExpiryCoversQueue(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "m8")
	pkg := mkMonthly(t, st, "月付", 100, 30, false)
	for i := 0; i < 3; i++ {
		buy(t, st, uid, pkg)
	}
	h := activeHead(t, st, uid)
	bs, _ := st.ListBuckets(uid)
	end, never, queued := FinalExpiry(bs, time.Now().Unix())
	if never || queued != 2 || end != h.ExpiryAt+60*86400 {
		t.Fatalf("final=%d queued=%d want head+60d", end, queued)
	}
	u, _ := st.UserByID(uid)
	if u.ExpiryAt != end {
		t.Fatalf("users.expiry_at=%d want final %d", u.ExpiryAt, end)
	}
}

func TestPeriod_LegacyPackageKeepsOldBehaviour(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "m9")
	pkg := mkPlan(t, st, "旧套餐", 100, 300, 90) // monthly reset off
	buy(t, st, uid, pkg)
	buy(t, st, uid, pkg)
	h := activeHead(t, st, uid)
	if h.Periodic() || h.TrafficLimit != 300*giB {
		t.Fatal("legacy package changed")
	}
	setUsed(t, st, h.ID, 300*giB)
	if _, err := st.AdvanceQueueFor(uid); err != nil {
		t.Fatal(err)
	}
	if activeHead(t, st, uid).ID == h.ID {
		t.Fatal("legacy份 must still hand over on exhaustion")
	}
}

func TestPeriod_ConvertLegacyLine(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "m10")
	pkg := mkPlan(t, st, "旧季付", 100, 300, 90)
	buy(t, st, uid, pkg)
	buy(t, st, uid, pkg)
	h := activeHead(t, st, uid)
	setUsed(t, st, h.ID, 50*giB)
	n, err := st.ConvertLineToPeriodic(uid, h.QueueKey)
	if err != nil || n != 2 {
		t.Fatalf("convert n=%d err=%v", n, err)
	}
	for _, b := range chainBuckets(t, st, uid) {
		if !b.Periodic() || b.PeriodQuota != 100*giB {
			t.Fatalf("份%d periodic=%v quota=%dG", b.ID, b.Periodic(), b.PeriodQuota/giB)
		}
	}
	h = activeHead(t, st, uid)
	if h.Used() != 0 || h.LifeUsedDown != 50*giB || h.PeriodStart <= 0 {
		t.Fatalf("active份 conversion: used=%d life=%d start=%d", h.Used(), h.LifeUsedDown, h.PeriodStart)
	}
}

func TestPeriod_AdminAdjustIsTemporary(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "m11")
	pkg := mkMonthly(t, st, "季付", 200, 90, false)
	buy(t, st, uid, pkg)
	h := activeHead(t, st, uid)
	if _, err := st.AdjustBucketTraffic(uid, h.ID, 50*giB); err != nil {
		t.Fatal(err)
	}
	if h = activeHead(t, st, uid); h.ShownQuota() != 250*giB || h.TrafficLimit != 250*giB {
		t.Fatalf("adjusted shown=%dG limit=%dG", h.ShownQuota()/giB, h.TrafficLimit/giB)
	}
	shiftStart(t, st, h.ID, 30*86400)
	if _, err := st.ResetDuePeriods(); err != nil {
		t.Fatal(err)
	}
	if h = activeHead(t, st, uid); h.PeriodBase != 200*giB {
		t.Fatalf("after reset base=%dG want monthly 200G", h.PeriodBase/giB)
	}
	if err := st.SetLineMonthlyQuota(uid, h.QueueKey, 300*giB); err != nil {
		t.Fatal(err)
	}
	if h = activeHead(t, st, uid); h.PeriodQuota != 300*giB || h.PeriodBase != 300*giB {
		t.Fatalf("monthly change quota=%dG base=%dG", h.PeriodQuota/giB, h.PeriodBase/giB)
	}
}

func TestPeriod_RefundByRemainingTime(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "m12")
	pkg := mkMonthly(t, st, "季付", 200, 90, false)
	oid := buy(t, st, uid, pkg)
	h := activeHead(t, st, uid)
	setUsed(t, st, h.ID, 200*giB) // traffic fully used this period
	q, err := st.RefundPreview(oid, "prorate")
	if err != nil {
		t.Fatal(err)
	}
	if q.TrafficRatio != -1 || q.TimeRatio < 0.99 {
		t.Fatalf("quote traffic=%v time=%v, want time-only", q.TrafficRatio, q.TimeRatio)
	}
}
