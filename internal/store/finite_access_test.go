package store

import (
	"strconv"
	"testing"
)

// A zero plan is exhausted, not uncapped. It must not grant its node groups;
// an explicitly configured free group remains a separate entitlement.
func TestAccessibleGroupIDs_ZeroPlanGrantsNothing(t *testing.T) {
	st := newRefundStore(t)
	planGroup, err := st.CreateGroup(NodeGroup{Name: "付费"})
	if err != nil {
		t.Fatal(err)
	}
	freeGroup, err := st.CreateGroup(NodeGroup{Name: "免费"})
	if err != nil {
		t.Fatal(err)
	}
	pkgID, err := st.CreatePackage(Package{
		Type: "plan", Name: "有限套餐", TrafficBytes: giB, DurationDays: 30,
		Stock: -1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPlanGroups(pkgID, []int64{planGroup}); err != nil {
		t.Fatal(err)
	}
	uid := mkUser(t, st, "finite-access")
	pkg, _ := st.GetPackage(pkgID)
	if _, err := st.AssignPackage(uid, pkg, 0, noopSync); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE user_plans SET traffic_limit=0 WHERE user_id=? AND package_id=?`, uid, pkgID); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByID(uid)
	groups, err := st.AccessibleGroupIDs(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("zero plan granted groups %v", groups)
	}
	if err := st.EnsurePoolBucket(uid, "qz_finite_pool", "finite-uuid", "finite-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE user_plans SET traffic_limit=? WHERE user_id=? AND kind='pool'`, 10*giB, uid); err != nil {
		t.Fatal(err)
	}
	groups, err = st.AccessibleGroupIDs(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("funded pool revived zero-plan groups %v", groups)
	}
	if err := st.SetSetting("free_group_id", strconv.FormatInt(freeGroup, 10)); err != nil {
		t.Fatal(err)
	}
	groups, err = st.AccessibleGroupIDs(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0] != freeGroup {
		t.Fatalf("groups = %v, want only free group %d", groups, freeGroup)
	}
}

func TestAccessibleGroupIDs_FundedPoolRestoresExhaustedPositivePlan(t *testing.T) {
	st := newRefundStore(t)
	groupID, err := st.CreateGroup(NodeGroup{Name: "付费"})
	if err != nil {
		t.Fatal(err)
	}
	pkgID, err := st.CreatePackage(Package{
		Type: "plan", Name: "有限套餐", TrafficBytes: giB, DurationDays: 30,
		Stock: -1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPlanGroups(pkgID, []int64{groupID}); err != nil {
		t.Fatal(err)
	}
	uid := mkUser(t, st, "finite-pool-access")
	pkg, _ := st.GetPackage(pkgID)
	if _, err := st.AssignPackage(uid, pkg, 0, noopSync); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE user_plans SET used_down=traffic_limit WHERE user_id=? AND package_id=?`, uid, pkgID); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByID(uid)
	groups, err := st.AccessibleGroupIDs(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("exhausted plan granted groups %v without fallback traffic", groups)
	}
	if err := st.EnsurePoolBucket(uid, "qz_finite_pool_2", "finite-uuid-2", "finite-secret-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE user_plans SET traffic_limit=? WHERE user_id=? AND kind='pool'`, 10*giB, uid); err != nil {
		t.Fatal(err)
	}
	groups, err = st.AccessibleGroupIDs(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0] != groupID {
		t.Fatalf("groups = %v, want pool-backed group %d", groups, groupID)
	}
}
