package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/store"
)

func TestSubscriptionQuotaIgnoresExpiredHistory(t *testing.T) {
	a, st := newResetSubAPI(t)
	uid, err := st.CreateUser(store.NewUser{Username: "quota-fixture", PasswordHash: "x", SubToken: "QUOTA_FIXTURE"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, row := range []struct {
		name                 string
		quota, up, down, end int64
	}{
		{"retired", 1024 * 1024, 200000, 300000, now - 86400},
		{"current", 2 * 1024 * 1024, 1000, 2000, now + 86400},
	} {
		_, err := st.DB().Exec(`INSERT INTO user_plans
			(user_id,kind,client_name,traffic_limit,used_up,used_down,expiry_at,status,created_at,updated_at)
			VALUES (?,'plan',?,?,?,?,?,'active',?,?)`, uid, row.name, row.quota, row.up, row.down, row.end, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = st.DB().Exec(`UPDATE users SET traffic_limit=?,used_up=?,used_down=?,expiry_at=? WHERE id=?`,
		3*1024*1024, 201000, 302000, now+86400, uid)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := st.ListBuckets(uid)
	for _, format := range []string{"clash", "singbox", "surge", "base64"} {
		w := httptest.NewRecorder()
		a.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sub/QUOTA_FIXTURE?format="+format, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d", format, w.Code)
		}
		want := "upload=1000; download=2000; total=2097152; expire=" + itoa(now+86400)
		if got := w.Header().Get("Subscription-Userinfo"); got != want {
			t.Errorf("%s quota header=%q, want %q", format, got, want)
		}
	}
	w := httptest.NewRecorder()
	a.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sub/QUOTA_FIXTURE?format=info", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "2.93 KiB") || strings.Contains(w.Body.String(), "491.21 KiB") {
		t.Errorf("browser info must describe current usage, status=%d", w.Code)
	}
	after, _ := st.ListBuckets(uid)
	if len(before) != len(after) {
		t.Fatal("subscription read changed historical bucket count")
	}
	for i := range before {
		if before[i].Used() != after[i].Used() || before[i].TrafficLimit != after[i].TrafficLimit {
			t.Fatal("subscription read changed bucket accounting")
		}
	}
	u, _ := st.UserByID(uid)
	if u.UsedUp != 201000 || u.UsedDown != 302000 {
		t.Fatal("subscription read reset lifetime accounting")
	}
}

func TestDashboardTrafficSubscriptionMetadata(t *testing.T) {
	now := time.Now().Unix()
	live := func(kind, status string, quota, up, down, expiry int64) *store.Bucket {
		return &store.Bucket{Kind: kind, Status: status, TrafficLimit: quota, UsedUp: up, UsedDown: down, ExpiryAt: expiry}
	}
	for _, tc := range []struct {
		name    string
		buckets []*store.Bucket
		want    dashTraffic
	}{
		{"empty", nil, dashTraffic{}},
		{"ineligible", []*store.Bucket{
			live("plan", "active", 100, 1, 2, now-1),
			live("plan", "queued", 200, 3, 4, now+100),
			live(store.KindFree, "active", 300, 5, 6, 0),
			live("pool", "active", 0, 7, 8, 0),
		}, dashTraffic{}},
		{"finite", []*store.Bucket{live("plan", "active", 100, 3, 7, now+100)},
			dashTraffic{Total: 100, Used: 10, Remaining: 90, Upload: 3, Download: 7, ExpiryAt: now + 100}},
		{"exhausted", []*store.Bucket{live("plan", "active", 100, 50, 70, now+100)},
			dashTraffic{Total: 100, Used: 120, Upload: 50, Download: 70, ExpiryAt: now + 100}},
		{"multiple", []*store.Bucket{
			live("plan", "active", 100, 3, 7, now+100),
			live("plan", "active", 200, 4, 6, now+200),
		}, dashTraffic{Total: 300, Used: 20, Remaining: 280, Upload: 7, Download: 13, ExpiryAt: now + 200}},
		{"permanent-after-finite", []*store.Bucket{
			live("plan", "active", 100, 3, 7, now+100),
			live("pool", "active", 200, 4, 6, 0),
		}, dashTraffic{Total: 300, Used: 20, Remaining: 280, Upload: 7, Download: 13}},
		{"permanent-before-finite", []*store.Bucket{
			live("pool", "active", 200, 4, 6, 0),
			live("plan", "active", 100, 3, 7, now+100),
		}, dashTraffic{Total: 300, Used: 20, Remaining: 280, Upload: 7, Download: 13}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dashboardTraffic(tc.buckets); got != tc.want {
				t.Errorf("quota metadata=%+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSubscriptionQuotaReadFailureDoesNotAdvertiseLegacyValues(t *testing.T) {
	a, st := newResetSubAPI(t)
	if _, err := st.CreateUser(store.NewUser{Username: "quota-error", PasswordHash: "x", SubToken: "QUOTA_ERROR"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec("DROP TABLE user_plans"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sub/QUOTA_ERROR?format=clash", nil))
	if w.Code != http.StatusInternalServerError || w.Header().Get("Subscription-Userinfo") != "" {
		t.Fatalf("failed quota read: status=%d header=%q", w.Code, w.Header().Get("Subscription-Userinfo"))
	}
}
