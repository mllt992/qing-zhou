package store

import (
	"sort"
	"strconv"
	"testing"
	"time"
)

func ownedTags(t *testing.T, st *Store, uid int64) map[string]string {
	t.Helper()
	o, err := st.UserOwnedInbounds(uid, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for tag, b := range o {
		out[tag] = b.Kind
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	r := []string{}
	for k := range m {
		r = append(r, k)
	}
	sort.Strings(r)
	return r
}

// 流量包只补流量，不扩大节点范围：套餐过期后流量包不能继续用该套餐的节点；
// 套餐有效但流量用完时流量包顶上；续费恢复；免费分组不受影响。
func TestPoolFollowsPlanValidity(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "poolv")
	if err := st.EnsureFreeBucket(uid, "poolv"); err != nil {
		t.Fatal(err)
	}
	pkg := mkPlan(t, st, "P", 10, 100, 30)
	other := mkPlan(t, st, "Q", 10, 100, 30)
	buy(t, st, uid, pkg)
	for i, tag := range []string{"a-in", "b-in", "free-in"} {
		if _, err := st.SaveSbInbound(&SbInbound{Type: "vless", Tag: tag, Listen: "::", ListenPort: 9000 + i, Options: "{}", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	bindPlanToInbound(t, st, pkg.ID, "a-in")
	bindPlanToInbound(t, st, other.ID, "b-in")
	fg, err := st.CreateGroup(NodeGroup{Name: "免费"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("free_group_id", strconv.FormatInt(fg, 10)); err != nil {
		t.Fatal(err)
	}
	nid, err := st.CreateNode(Node{Type: "self_built", Name: "免费", Protocol: "vless", InboundTag: "free-in", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeGroups(nid, []int64{fg}); err != nil {
		t.Fatal(err)
	}
	buy(t, st, uid, mkPack(t, st, "T", 5, 50))
	now := time.Now().Unix()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	check := func(step string, want map[string]string) {
		t.Helper()
		got := ownedTags(t, st, uid)
		if len(got) != len(want) {
			t.Fatalf("%s: got %v want %v", step, got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("%s: got %v want %v", step, got, want)
			}
		}
		if _, ok := got["b-in"]; ok {
			t.Fatalf("%s: 不应获得未购买套餐的节点: %v", step, sortedKeys(got))
		}
	}

	check("套餐有效", map[string]string{"a-in": "plan", "free-in": KindFree})

	exec(`UPDATE user_plans SET used_down=traffic_limit WHERE kind='plan' AND user_id=?`, uid)
	check("套餐流量用完", map[string]string{"a-in": "pool", "free-in": KindFree})

	exec(`UPDATE user_plans SET used_down=0, expiry_at=? WHERE kind='plan' AND user_id=?`, now-10, uid)
	check("套餐过期", map[string]string{"free-in": KindFree})

	exec(`UPDATE user_plans SET used_down=traffic_limit, expiry_at=? WHERE kind='plan' AND user_id=?`, now+86400, uid)
	check("续费后（流量仍用完）", map[string]string{"a-in": "pool", "free-in": KindFree})
}
