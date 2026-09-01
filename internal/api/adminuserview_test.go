package api

import (
	"testing"
	"time"

	"qingzhou/internal/store"
)

// The admin list's traffic must be the roll-up, not the users.* mirror: queued
// and expired/zero份 carry quota the user cannot spend. The old card summed all of them and reported e.g.
// "0 B / 210 GiB" for a user whose only usable份 was 10 GiB.
func TestAdminUserView_TrafficIgnoresQueuedAndExpired(t *testing.T) {
	now := time.Now().Unix()
	buckets := []*store.Bucket{
		{ID: 1, Kind: "plan", PackageID: 5, Name: "在用", Status: "active", TrafficLimit: 10 << 30, UsedUp: 2 << 30, ExpiryAt: now + 86400},
		{ID: 2, Kind: "plan", PackageID: 5, Name: "排队", Status: "queued", TrafficLimit: 100 << 30},
		{ID: 3, Kind: "plan", PackageID: 7, Name: "过期", Status: "active", TrafficLimit: 100 << 30, UsedDown: 1 << 30, ExpiryAt: now - 86400},
		{ID: 4, Kind: "plan", PackageID: 0, Name: "管理员额度", Status: "active", TrafficLimit: 0, UsedUp: 3 << 30},
		{ID: 5, Kind: store.KindFree, Name: "免费流量", UsedDown: 5 << 30},
	}
	u := &store.User{ID: 1, Username: "kim", Status: "active",
		TrafficLimit: 210 << 30, UsedUp: 5 << 30, UsedDown: 6 << 30} // the naive mirror

	v := adminUserView(u, nil, buckets)
	tr, ok := v["traffic"].(J)
	if !ok {
		t.Fatal("view has no traffic object")
	}
	if got := tr["total"].(int64); got != 10<<30 {
		t.Errorf("total = %d GiB, want 10 (only the usable capped份)", got>>30)
	}
	if got := tr["used"].(int64); got != 2<<30 {
		t.Errorf("used = %d GiB, want 2", got>>30)
	}
	if got := tr["remaining"].(int64); got != 8<<30 {
		t.Errorf("remaining = %d GiB, want 8", got>>30)
	}
	if tr["unlimited"] != false {
		t.Error("compatibility unlimited flag must stay false")
	}
	if got := tr["free_used"].(int64); got != 5<<30 {
		t.Errorf("free_used = %d GiB, want 5", got>>30)
	}
}

// The per-user plan counts split by what the份 can actually do, and next_expiry
// is the SOONEST usable份 — users.expiry_at is a MAX, which answers the opposite
// question once several份 coexist.
func TestAdminPlanRollup_CountsAndNextExpiry(t *testing.T) {
	now := time.Now().Unix()
	soon, late := now+3*86400, now+30*86400
	buckets := []*store.Bucket{
		{Kind: "plan", PackageID: 5, Name: "香港", Status: "active", TrafficLimit: 10 << 30, ExpiryAt: late},
		{Kind: "plan", PackageID: 6, Name: "日本", Status: "active", TrafficLimit: 10 << 30, ExpiryAt: soon},
		{Kind: "plan", PackageID: 5, Name: "香港", Status: "queued", TrafficLimit: 10 << 30},
		{Kind: "plan", PackageID: 7, Name: "已用尽", Status: "active", TrafficLimit: 10 << 30, UsedUp: 10 << 30, ExpiryAt: late},
		{Kind: "plan", PackageID: 8, Name: "已过期", Status: "active", TrafficLimit: 10 << 30, ExpiryAt: now - 10},
		{Kind: "pool", Name: "通用流量", TrafficLimit: 50 << 30, UsedDown: 4 << 30},
		{Kind: store.KindFree, Name: "免费流量", UsedUp: 1 << 30},
	}

	s := adminPlanRollupOf(buckets)
	if s.Active != 2 || s.Queued != 1 || s.Finished != 2 {
		t.Errorf("counts = active %d / queued %d / finished %d, want 2/1/2", s.Active, s.Queued, s.Finished)
	}
	if s.NextExpiryAt != soon {
		t.Errorf("next_expiry_at = %d, want the soonest usable份 (%d)", s.NextExpiryAt, soon)
	}
	if len(s.ActiveNames) != 2 || s.ActiveNames[0] != "香港" || s.ActiveNames[1] != "日本" {
		t.Errorf("active_names = %v, want [香港 日本]", s.ActiveNames)
	}
	if s.PoolLimit != 50<<30 || s.PoolUsed != 4<<30 {
		t.Errorf("pool = %d/%d GiB, want 4/50", s.PoolUsed>>30, s.PoolLimit>>30)
	}
}

func TestAdminUserView_OnlineTracksStatsWindow(t *testing.T) {
	now := time.Now().Unix()
	u := &store.User{ID: 1, Username: "kim", Status: "active", LastOnlineAt: now - 8*60}
	v := adminUserViewWithWindow(u, nil, []*store.Bucket{}, 20*60+30)
	if v["online"] != true {
		t.Fatal("8 minutes ago must still count as online under the default 10-minute stats poll")
	}
	v = adminUserViewWithWindow(u, nil, []*store.Bucket{}, 300)
	if v["online"] != false {
		t.Fatal("the old 5-minute window must not keep painting a 10-minute poll as online")
	}
}

// A user who holds nothing gets a zero roll-up (a fact); a user whose buckets
// could not be read gets no traffic object at all, so the UI shows "—" instead
// of a fabricated 0 B / 0 B.
func TestAdminUserView_NilBucketsOmitsTraffic(t *testing.T) {
	u := &store.User{ID: 1, Username: "lee", Status: "active"}
	if _, present := adminUserView(u, nil, nil)["traffic"]; present {
		t.Error("traffic reported despite unreadable buckets")
	}
	v := adminUserView(u, nil, []*store.Bucket{})
	tr, ok := v["traffic"].(J)
	if !ok {
		t.Fatal("a user with no buckets should still get a zero roll-up")
	}
	if tr["total"].(int64) != 0 || tr["unlimited"] != false {
		t.Errorf("empty roll-up = %+v, want zeros", tr)
	}
	if s := v["plan_summary"].(adminPlanRollup); s.Active != 0 || s.ActiveNames == nil {
		t.Errorf("plan_summary = %+v, want zero counts and a non-nil name list", s)
	}
}
