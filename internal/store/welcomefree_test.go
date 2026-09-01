package store

import (
	"testing"
	"time"
)

// mkPack creates a traffic package (tops up the pool, no expiry of its own).
func mkPack(t *testing.T, st *Store, name string, price, trafficGiB int64) *Package {
	t.Helper()
	id, err := st.CreatePackage(Package{
		Type: "traffic", Name: name, PricePoints: price,
		TrafficBytes: trafficGiB * giB, Stock: -1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetPackage(id)
	return p
}

func bucketOfKind(t *testing.T, st *Store, uid int64, kind string) *Bucket {
	t.Helper()
	bs, err := st.ListBuckets(uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bs {
		if b.Kind == kind {
			return b
		}
	}
	return nil
}

func welcomeBucket(t *testing.T, st *Store, uid int64) *Bucket {
	t.Helper()
	bs, err := st.ListBuckets(uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bs {
		if b.Kind == "plan" && b.PackageID == WelcomePackageID {
			return b
		}
	}
	return nil
}

// The signup grant must land in a real bucket. Writing it to users.traffic_limit
// / users.expiry_at (what registration used to do) left it invisible to
// enforcement, and the first purchase's recompute then reset expiry_at to the max
// *plan* expiry — 0 for a pool-only buyer — which handleSub reads as "never
// expires", silently voiding the trial deadline.
func TestWelcomeGrant_LandsInBucketAndSurvivesPurchase(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "wendy")
	expiry := time.Now().Unix() + 30*86400

	if err := st.EnsureWelcomeBucket(uid, "wendy", 10*giB, expiry); err != nil {
		t.Fatal(err)
	}
	b := welcomeBucket(t, st, uid)
	if b == nil || b.TrafficLimit != 10*giB || b.ExpiryAt != expiry {
		t.Fatalf("welcome bucket = %+v, want 10GiB expiring at %d", b, expiry)
	}
	if !b.Active(time.Now().Unix()) {
		t.Error("a fresh signup grant must be active, or the user reaches no inbound")
	}

	var limit, gotExpiry int64
	st.db.QueryRow(`SELECT traffic_limit, expiry_at FROM users WHERE id=?`, uid).Scan(&limit, &gotExpiry)
	if limit != 10*giB || gotExpiry != expiry {
		t.Errorf("aggregate = %d bytes / expiry %d, want 10GiB / %d", limit, gotExpiry, expiry)
	}

	// Buying a traffic pack recomputes the aggregate. The trial deadline must
	// survive — this is the regression that silently gave trial users unlimited time.
	pack := mkPack(t, st, "T", 100, 50)
	if _, err := st.Purchase(uid, pack, "", noopSync); err != nil {
		t.Fatal(err)
	}
	st.db.QueryRow(`SELECT traffic_limit, expiry_at FROM users WHERE id=?`, uid).Scan(&limit, &gotExpiry)
	if gotExpiry != expiry {
		t.Errorf("expiry_at = %d after purchase, want the trial deadline %d", gotExpiry, expiry)
	}
	if limit != 60*giB {
		t.Errorf("traffic_limit = %d GiB, want 60 (10 grant + 50 pack)", limit/giB)
	}
	if welcomeBucket(t, st, uid) == nil {
		t.Error("signup grant vanished after a purchase")
	}
}

// Granting twice must not stack — provisionClient runs on register, on email
// verification, and on an admin re-provision of the same account.
func TestWelcomeGrant_Idempotent(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "walt")
	expiry := time.Now().Unix() + 30*86400
	for i := 0; i < 3; i++ {
		if err := st.EnsureWelcomeBucket(uid, "walt", 10*giB, expiry); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM user_plans WHERE user_id=? AND package_id=?`, uid, WelcomePackageID).Scan(&n)
	if n != 1 {
		t.Errorf("got %d signup grants, want 1", n)
	}
	var limit int64
	st.db.QueryRow(`SELECT traffic_limit FROM users WHERE id=?`, uid).Scan(&limit)
	if limit != 10*giB {
		t.Errorf("aggregate = %d GiB, want 10 — the grant stacked", limit/giB)
	}
}

// A zero-traffic signup grant must not create a bucket even when a duration is
// still configured; zero is zero, not "unlimited for N days".
func TestWelcomeGrant_SkippedWhenDisabled(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "wilma")
	future := time.Now().Unix() + 30*86400
	if _, err := st.db.Exec(`UPDATE users SET used_up=?, expiry_at=? WHERE id=?`, giB, future, uid); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureWelcomeBucket(uid, "wilma", 0, future); err != nil {
		t.Fatal(err)
	}
	if welcomeBucket(t, st, uid) != nil {
		t.Error("no grant configured, but a bucket was created")
	}
	u, err := st.UserByID(uid)
	if err != nil || u == nil {
		t.Fatalf("UserByID: %v %#v", err, u)
	}
	if u.TrafficLimit != 0 || u.UsedUp != 0 || u.ExpiryAt != 0 {
		t.Fatalf("stale aggregate survived disabled grant: %+v", u)
	}
}

func TestFiniteTrafficMigration_RemovesZeroWelcomeAndRepairsAggregate(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "legacy-zero-welcome")
	future := time.Now().Unix() + 30*86400
	if _, err := insertBucket(st.db, &Bucket{
		UserID: uid, Kind: "plan", PackageID: WelcomePackageID, Name: "注册赠送",
		ClientName: "qz_legacy_zero_welcome", TrafficLimit: 0, ExpiryAt: future,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE users SET traffic_limit=0, used_up=?, expiry_at=? WHERE id=?`, giB, future, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM schema_migrations WHERE version=?`, finiteTrafficAggregateMigration); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if welcomeBucket(t, st, uid) != nil {
		t.Fatal("zero-byte welcome bucket survived finite-traffic migration")
	}
	u, err := st.UserByID(uid)
	if err != nil || u == nil {
		t.Fatalf("UserByID: %v %#v", err, u)
	}
	if u.TrafficLimit != 0 || u.UsedUp != 0 || u.UsedDown != 0 || u.ExpiryAt != 0 {
		t.Fatalf("aggregate after migration = %+v, want zero quota/usage/expiry", u)
	}
	// The marker makes the data migration idempotent.
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
}

// Free-group traffic must not be debited from the paid pool. Metering is
// identity-based, so while the pool covered the free group every free byte ate
// paid balance — and because a top-up only raises traffic_limit and never clears
// used_*, traffic burned on free nodes before a purchase came straight out of it.
func TestFreeBucket_UsageNotChargedToPaidQuota(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "fred")
	if err := st.EnsureFreeBucket(uid, "fred"); err != nil {
		t.Fatal(err)
	}
	free := bucketOfKind(t, st, uid, KindFree)
	if free == nil {
		t.Fatal("no free bucket")
	}

	// Burn 50 GiB on free nodes before buying anything.
	if _, err := st.AddUsageBatch(map[string]UsageDelta{
		free.ClientName: {Up: 25 * giB, Down: 25 * giB},
	}); err != nil {
		t.Fatal(err)
	}

	// Then buy a 50 GiB traffic pack.
	pack := mkPack(t, st, "T", 100, 50)
	if _, err := st.Purchase(uid, pack, "", noopSync); err != nil {
		t.Fatal(err)
	}

	pool := bucketOfKind(t, st, uid, "pool")
	if pool == nil {
		t.Fatal("no pool bucket")
	}
	if pool.Used() != 0 {
		t.Errorf("pool used = %d GiB, want 0 — free traffic was charged to paid balance", pool.Used()/giB)
	}
	if !pool.HasQuota() {
		t.Error("the just-bought pack is already exhausted by free-node traffic")
	}

	// The aggregate that gates handleSub must ignore free usage too, or the
	// coupling just moves from the bucket to the serviceable check.
	var limit, up, down int64
	st.db.QueryRow(`SELECT traffic_limit, used_up, used_down FROM users WHERE id=?`, uid).Scan(&limit, &up, &down)
	if up+down != 0 {
		t.Errorf("aggregate used = %d GiB, want 0 — free usage counted against paid quota", (up+down)/giB)
	}
	if limit != 50*giB {
		t.Errorf("aggregate limit = %d GiB, want 50", limit/giB)
	}

	// Usage is still recorded on the free bucket itself, for display.
	if got := bucketOfKind(t, st, uid, KindFree).Used(); got != 50*giB {
		t.Errorf("free bucket used = %d GiB, want 50 — free traffic should still be metered, just not billed", got/giB)
	}
}

// The free bucket carries the free group; the pool must not, or its identity
// picks the traffic back up. A free bucket is always active — it has no limit.
func TestOrderBuckets_FreeGroupRidesFreeBucket(t *testing.T) {
	now := time.Now().Unix()
	const freeGroup int64 = 7
	pool := &Bucket{Kind: "pool", ClientName: "u_pool", TrafficLimit: 0}
	free := &Bucket{Kind: KindFree, ClientName: "u_free"}

	ord := orderBuckets([]*Bucket{pool, free}, now, freeGroup, func(int64) []int64 { return nil })

	var carrier string
	for _, ob := range ord {
		if ob.groups[freeGroup] {
			if carrier != "" {
				t.Errorf("free group carried by both %q and %q", carrier, ob.b.ClientName)
			}
			carrier = ob.b.ClientName
		}
	}
	if carrier != free.ClientName {
		t.Errorf("free group carried by %q, want the free bucket %q", carrier, free.ClientName)
	}
}

// An empty pool with no free bucket must reach nothing: falling back to the pool
// would restore the very billing coupling the free bucket exists to break.
func TestOrderBuckets_NoFreeBucketNoFreeGroup(t *testing.T) {
	pool := &Bucket{Kind: "pool", ClientName: "u_pool", TrafficLimit: 0}
	ord := orderBuckets([]*Bucket{pool}, time.Now().Unix(), 7, func(int64) []int64 { return nil })
	for _, ob := range ord {
		if ob.groups[7] {
			t.Errorf("%q picked up the free group without a free bucket", ob.b.ClientName)
		}
	}
}

// The signup grant has no package of its own, so it must be scoped like the admin
// comp (free group + the union of the user's plan groups) rather than to the
// empty group list its package_id would resolve to.
func TestOrderBuckets_WelcomeGrantIsScopedLikeTheComp(t *testing.T) {
	now := time.Now().Unix()
	const freeGroup, planGroup int64 = 7, 9
	welcome := &Bucket{Kind: "plan", PackageID: WelcomePackageID, ClientName: "u_welcome", TrafficLimit: 10 * giB}
	paid := &Bucket{Kind: "plan", PackageID: 5, ClientName: "u_plan", TrafficLimit: giB}

	ord := orderBuckets([]*Bucket{welcome, paid}, now, freeGroup, func(pkg int64) []int64 {
		if pkg == 5 {
			return []int64{planGroup}
		}
		return nil
	})

	var got map[int64]bool
	for _, ob := range ord {
		if ob.b.ClientName == welcome.ClientName {
			got = ob.groups
		}
	}
	if got == nil {
		t.Fatal("signup grant missing from the ordering — the user would reach no inbound")
	}
	if !got[freeGroup] || !got[planGroup] {
		t.Errorf("signup grant scoped to %v, want both the free group %d and the plan group %d",
			got, freeGroup, planGroup)
	}
}
