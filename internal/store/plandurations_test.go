package store

import (
	"errors"
	"testing"
)

// mkMultiPlan creates a plan sold at several lengths. The first option is the
// default one; the store mirrors it onto the package's own columns.
func mkMultiPlan(t *testing.T, st *Store, name string, opts ...PlanOption) *Package {
	t.Helper()
	id, err := st.CreatePackage(Package{
		Type: "plan", Name: name, Stock: -1, Enabled: true, Options: opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetPackage(id)
	return p
}

// boughtBucket returns the bucket a package purchase/grant minted (package_id>0),
// as opposed to the shared pool or an admin manual grant.
func boughtBucket(t *testing.T, st *Store, uid int64) *Bucket {
	t.Helper()
	bs, err := st.ListBuckets(uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bs {
		if b.Kind == "plan" && b.PackageID > 0 {
			return b
		}
	}
	t.Fatal("no plan bucket")
	return nil
}

func points(t *testing.T, st *Store, uid int64) int64 {
	t.Helper()
	u, err := st.UserByID(uid)
	if err != nil || u == nil {
		t.Fatalf("load user: %v", err)
	}
	return u.Points
}

// The package's own columns must equal its first option — they are what the shop
// card, the admin list and a default grant read, so a stale combination there
// would sell a length at another length's price.
func TestPlanDurations_DefaultMirrorsFirstOption(t *testing.T) {
	st := newRefundStore(t)
	pkg := mkMultiPlan(t, st, "M",
		PlanOption{Days: 30, PricePoints: 100, TrafficBytes: 100 * giB},
		PlanOption{Days: 90, PricePoints: 270, TrafficBytes: 300 * giB},
	)
	if pkg.DurationDays != 30 || pkg.PricePoints != 100 || pkg.TrafficBytes != 100*giB {
		t.Fatalf("package columns = %d天/%d分/%dGiB, want the first option (30/100/100)",
			pkg.DurationDays, pkg.PricePoints, pkg.TrafficBytes/giB)
	}

	// Reordering the options moves the default with them.
	pkg.Options = []PlanOption{{Days: 90, PricePoints: 270, TrafficBytes: 300 * giB}, {Days: 30, PricePoints: 100, TrafficBytes: 100 * giB}}
	if err := st.UpdatePackage(*pkg); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetPackage(pkg.ID)
	if got.DurationDays != 90 || got.PricePoints != 270 {
		t.Errorf("after reorder: %d天/%d分, want 90/270", got.DurationDays, got.PricePoints)
	}
}

// Buying a non-default length must charge THAT length's price and grant its
// quota — the whole point of the feature, and the thing an out-of-date price
// path would get wrong.
func TestPlanDurations_BuyNonDefaultCharges(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "alice")
	before := points(t, st, uid)
	pkg := mkMultiPlan(t, st, "M",
		PlanOption{Days: 30, PricePoints: 100, TrafficBytes: 100 * giB},
		PlanOption{Days: 365, PricePoints: 900, TrafficBytes: 1200 * giB},
	)

	if _, err := st.PurchaseDuration(uid, pkg, 365, "", noopSync); err != nil {
		t.Fatal(err)
	}
	if spent := before - points(t, st, uid); spent != 900 {
		t.Errorf("charged %d points, want 900 (the 365-day option)", spent)
	}
	b := boughtBucket(t, st, uid)
	if b.DurationDays != 365 || b.TrafficLimit != 1200*giB {
		t.Errorf("bucket = %d天/%dGiB, want 365/1200", b.DurationDays, b.TrafficLimit/giB)
	}
}

// days == 0 keeps the old single-argument behaviour: buy the default.
func TestPlanDurations_ZeroBuysDefault(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "bob")
	before := points(t, st, uid)
	pkg := mkMultiPlan(t, st, "M",
		PlanOption{Days: 30, PricePoints: 100, TrafficBytes: 100 * giB},
		PlanOption{Days: 90, PricePoints: 270, TrafficBytes: 300 * giB},
	)

	if _, err := st.Purchase(uid, pkg, "", noopSync); err != nil {
		t.Fatal(err)
	}
	if spent := before - points(t, st, uid); spent != 100 {
		t.Errorf("charged %d points, want 100 (default option)", spent)
	}
	if b := boughtBucket(t, st, uid); b.DurationDays != 30 {
		t.Errorf("bucket duration = %d days, want 30", b.DurationDays)
	}
}

// A length that isn't on sale must be refused outright — no order, no charge —
// rather than falling back to some other option's price.
func TestPlanDurations_UnknownLengthRejected(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "carol")
	before := points(t, st, uid)
	pkg := mkMultiPlan(t, st, "M",
		PlanOption{Days: 30, PricePoints: 100, TrafficBytes: 100 * giB},
		PlanOption{Days: 90, PricePoints: 270, TrafficBytes: 300 * giB},
	)

	if _, err := st.PurchaseDuration(uid, pkg, 31, "", noopSync); !errors.Is(err, ErrOptionNotFound) {
		t.Fatalf("purchase of an unlisted length: err = %v, want ErrOptionNotFound", err)
	}
	if got := points(t, st, uid); got != before {
		t.Errorf("points changed to %d after a rejected purchase, want %d", got, before)
	}
	orders, _ := st.ListOrders(uid, 10)
	if len(orders) != 0 {
		t.Errorf("%d orders written for a rejected purchase, want 0", len(orders))
	}
}

// A single-duration package still accepts its own length (and only that) — the
// shop posts 0, but an older client posting the visible number must not break.
func TestPlanDurations_SingleDurationPackage(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "dave")
	pkg := mkPlan(t, st, "S", 100, 50, 30)

	if _, err := st.PurchaseDuration(uid, pkg, 30, "", noopSync); err != nil {
		t.Fatalf("buying the package's own length: %v", err)
	}
	if _, err := st.PurchaseDuration(uid, pkg, 60, "", noopSync); !errors.Is(err, ErrOptionNotFound) {
		t.Errorf("buying a length it doesn't sell: err = %v, want ErrOptionNotFound", err)
	}
}

func TestPlanDurations_ZeroTrafficCannotBePurchasedOrAssigned(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "zero-package")
	pkgID, err := st.CreatePackage(Package{
		Type: "plan", Name: "零额度旧商品", TrafficBytes: 0, DurationDays: 30,
		PricePoints: 100, Stock: -1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := st.GetPackage(pkgID)
	if err != nil || pkg == nil {
		t.Fatalf("GetPackage: %v %#v", err, pkg)
	}
	before := points(t, st, uid)
	if _, err := st.PurchaseDuration(uid, pkg, 0, "", noopSync); !errors.Is(err, ErrPackageNoTraffic) {
		t.Fatalf("purchase error = %v, want ErrPackageNoTraffic", err)
	}
	if got := points(t, st, uid); got != before {
		t.Fatalf("points changed after rejected zero-byte purchase: %d -> %d", before, got)
	}
	if _, err := st.AssignPackageDuration(uid, pkg, 30, 0, noopSync); !errors.Is(err, ErrPackageNoTraffic) {
		t.Fatalf("assignment error = %v, want ErrPackageNoTraffic", err)
	}
	if buckets := planBuckets(t, st, uid, pkgID); len(buckets) != 0 {
		t.Fatalf("zero-byte package created buckets: %+v", buckets)
	}
}

// The refund prorates against the length actually bought (carried in the order
// snapshot), not the package's default one.
func TestPlanDurations_RefundUsesPurchasedLength(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "erin")
	pkg := mkMultiPlan(t, st, "M",
		PlanOption{Days: 30, PricePoints: 100, TrafficBytes: 100 * giB},
		PlanOption{Days: 365, PricePoints: 900, TrafficBytes: 1200 * giB},
	)

	res, err := st.PurchaseDuration(uid, pkg, 365, "", noopSync)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing used yet → a full-value refund of what was actually paid.
	_, q, err := st.RefundOrder(res.Order.ID, 0, "prorated", noopSync)
	if err != nil {
		t.Fatal(err)
	}
	if q.RefundPoints != 900 {
		t.Errorf("refund = %d points, want 900 (the 365-day price that was charged)", q.RefundPoints)
	}
}

// An admin comp can hand out the short trial length instead of the headline one.
func TestPlanDurations_AssignPicksLength(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "frank")
	pkg := mkMultiPlan(t, st, "M",
		PlanOption{Days: 30, PricePoints: 100, TrafficBytes: 100 * giB},
		PlanOption{Days: 7, PricePoints: 30, TrafficBytes: 25 * giB},
	)

	if _, err := st.AssignPackageDuration(uid, pkg, 7, 0, noopSync); err != nil {
		t.Fatal(err)
	}
	b := boughtBucket(t, st, uid)
	if b.DurationDays != 7 || b.TrafficLimit != 25*giB {
		t.Errorf("granted %d天/%dGiB, want 7/25", b.DurationDays, b.TrafficLimit/giB)
	}
}

// Admin grants accept any positive length, not just published options — a 3-day
// comp or a 14-day makeup shouldn't require adding a shop SKU first. Traffic
// stays the default option's (or the matched option's); only duration changes.
func TestPlanDurations_AssignCustomLength(t *testing.T) {
	st := newRefundStore(t)
	pkg := mkMultiPlan(t, st, "M",
		PlanOption{Days: 30, PricePoints: 100, TrafficBytes: 100 * giB},
		PlanOption{Days: 7, PricePoints: 30, TrafficBytes: 25 * giB},
	)

	uid := mkUser(t, st, "gina")
	if _, err := st.AssignPackageDuration(uid, pkg, 14, 0, noopSync); err != nil {
		t.Fatal(err)
	}
	b := boughtBucket(t, st, uid)
	if b.DurationDays != 14 || b.TrafficLimit != 100*giB {
		t.Errorf("custom grant %d天/%dGiB, want 14/100 (default-option traffic)", b.DurationDays, b.TrafficLimit/giB)
	}

	uid2 := mkUser(t, st, "hank")
	single := mkPlan(t, st, "S", 100, 50, 30)
	if _, err := st.AssignPackageDuration(uid2, single, 3, 0, noopSync); err != nil {
		t.Fatal(err)
	}
	b2 := boughtBucket(t, st, uid2)
	if b2.DurationDays != 3 || b2.TrafficLimit != 50*giB {
		t.Errorf("single-duration custom grant %d天/%dGiB, want 3/50", b2.DurationDays, b2.TrafficLimit/giB)
	}

	// The shop still refuses unpublished lengths — only the admin path is loose.
	if _, err := st.PurchaseDuration(uid2, single, 3, "", noopSync); !errors.Is(err, ErrOptionNotFound) {
		t.Errorf("shop buying a custom length: err = %v, want ErrOptionNotFound", err)
	}
	if _, err := st.PurchaseDuration(uid, pkg, 14, "", noopSync); !errors.Is(err, ErrOptionNotFound) {
		t.Errorf("shop buying an unlisted multi-option length: err = %v, want ErrOptionNotFound", err)
	}

	if _, err := st.AssignPackageDuration(uid, pkg, -1, 0, noopSync); !errors.Is(err, ErrInvalidAssignDays) {
		t.Errorf("negative grant: err = %v, want ErrInvalidAssignDays", err)
	}
	if _, err := st.AssignPackageDuration(uid, pkg, MaxAdminAssignDays+1, 0, noopSync); !errors.Is(err, ErrInvalidAssignDays) {
		t.Errorf("oversized grant: err = %v, want ErrInvalidAssignDays", err)
	}
}

// Traffic packages top up the shared pool and have no per-grant expiry. A days
// value in the request must not mint a plan bucket or rewrite the snapshot length.
func TestPlanDurations_AssignTrafficIgnoresDays(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "ivy")
	id, err := st.CreatePackage(Package{
		Type: "traffic", Name: "加油包", PricePoints: 10,
		TrafficBytes: 20 * giB, DurationDays: 30, Stock: -1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := st.GetPackage(id)
	if err != nil || pkg == nil {
		t.Fatalf("load traffic package: %v", err)
	}
	if _, err := st.AssignPackageDuration(uid, pkg, 14, 0, noopSync); err != nil {
		t.Fatal(err)
	}
	bs, err := st.ListBuckets(uid)
	if err != nil {
		t.Fatal(err)
	}
	var pool *Bucket
	for _, b := range bs {
		if b.Kind == "plan" && b.PackageID == pkg.ID {
			t.Fatalf("traffic grant minted a plan bucket (%d days) — days should have been ignored", b.DurationDays)
		}
		if b.Kind == "pool" {
			pool = b
		}
	}
	if pool == nil {
		t.Fatal("no pool bucket after traffic grant")
	}
	if pool.TrafficLimit != 20*giB {
		t.Errorf("pool limit = %d GiB, want 20", pool.TrafficLimit/giB)
	}
}

func TestPackage_ForAdminDuration(t *testing.T) {
	pkg := &Package{
		Type: "plan", DurationDays: 30, TrafficBytes: 100 * giB, PricePoints: 100,
		Options: []PlanOption{
			{Days: 30, PricePoints: 100, TrafficBytes: 100 * giB},
			{Days: 7, PricePoints: 30, TrafficBytes: 25 * giB},
		},
	}
	got, err := pkg.forAdminDuration(0)
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if got.DurationDays != 30 || got.TrafficBytes != 100*giB {
		t.Fatalf("default: %d天/%dGiB, want 30/100", got.DurationDays, got.TrafficBytes/giB)
	}
	got, err = pkg.forAdminDuration(7)
	if err != nil {
		t.Fatalf("listed: %v", err)
	}
	if got.DurationDays != 7 || got.TrafficBytes != 25*giB {
		t.Fatalf("listed: %d天/%dGiB, want 7/25", got.DurationDays, got.TrafficBytes/giB)
	}
	got, err = pkg.forAdminDuration(14)
	if err != nil {
		t.Fatalf("custom: %v", err)
	}
	if got.DurationDays != 14 || got.TrafficBytes != 100*giB {
		t.Fatalf("custom: %d天/%dGiB, want 14/100", got.DurationDays, got.TrafficBytes/giB)
	}
	if _, err = pkg.forAdminDuration(-1); !errors.Is(err, ErrInvalidAssignDays) {
		t.Errorf("negative: err = %v, want ErrInvalidAssignDays", err)
	}
	if _, err = pkg.forAdminDuration(MaxAdminAssignDays + 1); !errors.Is(err, ErrInvalidAssignDays) {
		t.Errorf("too long: err = %v, want ErrInvalidAssignDays", err)
	}
	got, err = pkg.forAdminDuration(MaxAdminAssignDays)
	if err != nil {
		t.Fatalf("cap: %v", err)
	}
	if got.DurationDays != MaxAdminAssignDays || got.TrafficBytes != 100*giB {
		t.Errorf("cap: %d天/%dGiB, want %d/100", got.DurationDays, got.TrafficBytes/giB, MaxAdminAssignDays)
	}
	// Mutating the result must not rewrite the original default columns.
	got.DurationDays = 1
	if pkg.DurationDays != 30 {
		t.Errorf("forAdminDuration mutated the source package duration to %d", pkg.DurationDays)
	}

	// A published option may exceed the custom-length cap — the admin is
	// picking a shop SKU, not typing a free-form number.
	long := &Package{
		Type: "plan", DurationDays: 4000, TrafficBytes: 200 * giB, PricePoints: 200,
		Options: []PlanOption{{Days: 4000, PricePoints: 200, TrafficBytes: 200 * giB}},
	}
	got, err = long.forAdminDuration(4000)
	if err != nil {
		t.Fatalf("listed over cap: %v", err)
	}
	if got.DurationDays != 4000 || got.TrafficBytes != 200*giB {
		t.Errorf("listed over cap: %d天/%dGiB, want 4000/200", got.DurationDays, got.TrafficBytes/giB)
	}
	if _, err = long.forAdminDuration(4001); !errors.Is(err, ErrInvalidAssignDays) {
		t.Errorf("custom over cap: err = %v, want ErrInvalidAssignDays", err)
	}

	// Traffic packages ignore the requested length (pool has no expiry).
	traffic := &Package{Type: "traffic", DurationDays: 30, TrafficBytes: 10 * giB}
	got, err = traffic.forAdminDuration(14)
	if err != nil {
		t.Fatalf("traffic custom: %v", err)
	}
	if got.DurationDays != 30 {
		t.Errorf("traffic custom days = %d, want the package default 30 (ignored)", got.DurationDays)
	}
}
