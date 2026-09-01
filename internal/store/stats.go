package store

import (
	"strings"
	"time"

	"qingzhou/internal/intervalcfg"
)

type Overview struct {
	TotalUsers   int64 `json:"total_users"`
	ActiveUsers  int64 `json:"active_users"` // have a provisioned client
	NewToday     int64 `json:"new_today"`
	TotalTraffic int64 `json:"total_traffic"` // sum of used up+down
	PointsIssued int64 `json:"points_issued"`
	PackagesOn   int64 `json:"packages_on"`
}

// DayTraffic is one calendar day's up/down totals (bytes).
type DayTraffic struct {
	Date string `json:"date"`
	Up   int64  `json:"up"`
	Down int64  `json:"down"`
}

// UserDailyTraffic returns a user's per-day traffic over the last `days` days,
// from the traffic_samples time-series (native sing-box era). Sparse: only days
// with traffic are returned; callers fill the window.
func (s *Store) UserDailyTraffic(userID int64, days int) ([]DayTraffic, error) {
	return s.dailyTraffic(`SELECT strftime('%Y-%m-%d', ts, 'unixepoch', 'localtime') d,
		COALESCE(SUM(up),0), COALESCE(SUM(down),0)
		FROM traffic_samples WHERE user_id=? AND ts>=? GROUP BY d ORDER BY d`,
		userID, time.Now().AddDate(0, 0, -days).Unix())
}

// SiteDailyTraffic returns site-wide per-day traffic over the last `days` days.
func (s *Store) SiteDailyTraffic(days int) ([]DayTraffic, error) {
	return s.dailyTraffic(`SELECT strftime('%Y-%m-%d', ts, 'unixepoch', 'localtime') d,
		COALESCE(SUM(up),0), COALESCE(SUM(down),0)
		FROM traffic_samples WHERE ts>=? GROUP BY d ORDER BY d`,
		time.Now().AddDate(0, 0, -days).Unix())
}

func (s *Store) dailyTraffic(q string, args ...any) ([]DayTraffic, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayTraffic
	for rows.Next() {
		var d DayTraffic
		if err := rows.Scan(&d.Date, &d.Up, &d.Down); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// OnlineCount counts users seen transferring traffic within the last `withinSec`
// seconds (a stats poll with non-zero delta updates last_online_at).
func (s *Store) OnlineCount(withinSec int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE last_online_at >= ?`,
		time.Now().Unix()-withinSec).Scan(&n)
	return n, err
}

// OnlineUsers returns usernames seen online within the last `withinSec` seconds,
// most-recent first.
func (s *Store) OnlineUsers(withinSec int64, limit int) ([]NameValue, error) {
	return s.nameValues(`SELECT username, last_online_at FROM users
		WHERE last_online_at >= ? ORDER BY last_online_at DESC LIMIT ?`,
		time.Now().Unix()-withinSec, limit)
}

// PruneTrafficSamples deletes samples older than `keepDays` days.
func (s *Store) PruneTrafficSamples(keepDays int) error {
	before := time.Now().AddDate(0, 0, -keepDays).Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM traffic_samples WHERE ts < ?`, before); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM server_user_traffic_samples WHERE ts < ?`, before); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Overview() (*Overview, error) {
	o := &Overview{}
	// Every user-facing count is role='user': the admin account is not a
	// customer, and counting it in one place but not another produced "今日新增
	// 19" sitting next to "14天新增 18".
	row := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM users WHERE role='user'),
		(SELECT COUNT(*) FROM users WHERE role='user' AND client_name IS NOT NULL AND client_name<>''),
		(SELECT COUNT(*) FROM users WHERE role='user' AND created_at >= ?),
		(SELECT COALESCE(SUM(used_up+used_down),0) FROM users WHERE role='user'),
		(SELECT COALESCE(SUM(amount),0) FROM point_transactions WHERE amount>0),
		(SELECT COUNT(*) FROM packages WHERE enabled=1)`,
		startOfToday())
	if err := row.Scan(&o.TotalUsers, &o.ActiveUsers, &o.NewToday, &o.TotalTraffic, &o.PointsIssued, &o.PackagesOn); err != nil {
		return nil, err
	}
	return o, nil
}

type NameValue struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// The former TopByTraffic / TopBySpend / PackageSales trio lived here and was
// removed with the /api/admin/stats/top endpoint that was their only caller.
// Nothing in the panel ever called it, and each was superseded by a query that
// answers the same question correctly:
//
//   - top-by-traffic summed users.used_up+used_down — the legacy aggregate
//     mirror, which folds in queued份 the user cannot spend and expired份 that
//     hand out nothing. UserStats(Sort:"traffic") reads the buckets.
//   - top-by-spend summed point_transactions type='purchase' without netting
//     refunds, so a bought-and-refunded order still counted as spend.
//     UserStats(Sort:"spend") matches the 净支出 the orders pages report.
//   - package-sales was a strict subset of PackageStats, which the 概览 already
//     loads.
//
// Restoring any of them means restoring a second set of numbers that disagrees
// with the ones on screen. Use UserStats / PackageStats instead.

func (s *Store) nameValues(q string, args ...any) ([]NameValue, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NameValue{}
	for rows.Next() {
		var nv NameValue
		if err := rows.Scan(&nv.Name, &nv.Value); err != nil {
			return nil, err
		}
		out = append(out, nv)
	}
	return out, rows.Err()
}

// StatusDistribution returns counts of users by status plus expiry buckets.
func (s *Store) StatusDistribution() (map[string]int64, error) {
	now := time.Now().Unix()
	out := map[string]int64{}
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM users WHERE role='user' GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var c int64
		if err := rows.Scan(&st, &c); err != nil {
			rows.Close()
			return nil, err
		}
		out["status_"+st] = c
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	buckets := []struct {
		key  string
		cond string
		args []any
	}{
		{"expired", `expiry_at>0 AND expiry_at<=?`, []any{now}},
		{"expire_7d", `expiry_at>? AND expiry_at<=?`, []any{now, now + 7*86400}},
		{"expire_30d", `expiry_at>? AND expiry_at<=?`, []any{now + 7*86400, now + 30*86400}},
	}
	for _, b := range buckets {
		var c int64
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='user' AND `+b.cond, b.args...).Scan(&c); err != nil {
			return nil, err
		}
		out[b.key] = c
	}
	return out, nil
}

type DayPoint struct {
	Date string `json:"date"`
	A    int64  `json:"a"`
	B    int64  `json:"b"`
}

// RegistrationTrend returns new-user counts per day for the last n days.
func (s *Store) RegistrationTrend(days int) ([]DayPoint, error) {
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	rows, err := s.db.Query(`SELECT date(created_at,'unixepoch','localtime') d, COUNT(*)
		FROM users WHERE created_at>=? GROUP BY d ORDER BY d`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DayPoint{}
	for rows.Next() {
		var dp DayPoint
		if err := rows.Scan(&dp.Date, &dp.A); err != nil {
			return nil, err
		}
		out = append(out, dp)
	}
	return out, rows.Err()
}

// RevenueTrend returns points issued (a) and consumed (b) per day.
func (s *Store) RevenueTrend(days int) ([]DayPoint, error) {
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	rows, err := s.db.Query(`SELECT date(created_at,'unixepoch','localtime') d,
		COALESCE(SUM(CASE WHEN amount>0 THEN amount ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN amount<0 THEN -amount ELSE 0 END),0)
		FROM point_transactions WHERE created_at>=? GROUP BY d ORDER BY d`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DayPoint{}
	for rows.Next() {
		var dp DayPoint
		if err := rows.Scan(&dp.Date, &dp.A, &dp.B); err != nil {
			return nil, err
		}
		out = append(out, dp)
	}
	return out, rows.Err()
}

func startOfToday() int64 {
	now := time.Now()
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).Unix()
}

// PeriodStat is one window's totals, used in pairs (current vs the window
// immediately before it) so the UI can show a trend instead of a bare number.
// A number with nothing to compare it to tells an operator very little.
type PeriodStat struct {
	Traffic  int64 `json:"traffic"`
	NewUsers int64 `json:"new_users"`
	Orders   int64 `json:"orders"`
	Revenue  int64 `json:"revenue"`
}

// PeriodStats returns totals for the last `days` days and for the equally-long
// window before it.
func (s *Store) PeriodStats(days int) (cur, prev PeriodStat, err error) {
	if days <= 0 {
		days = 7
	}
	now := time.Now()
	curFrom := now.AddDate(0, 0, -days).Unix()
	prevFrom := now.AddDate(0, 0, -2*days).Unix()

	read := func(from, to int64) (PeriodStat, error) {
		var p PeriodStat
		err := s.db.QueryRow(`SELECT
			(SELECT COALESCE(SUM(up+down),0) FROM traffic_samples WHERE ts>=? AND ts<?),
			(SELECT COUNT(*) FROM users WHERE role='user' AND created_at>=? AND created_at<?),
			(SELECT COUNT(*) FROM orders WHERE status='success' AND created_at>=? AND created_at<?),
			(SELECT COALESCE(SUM(price_points),0) FROM orders WHERE status='success' AND created_at>=? AND created_at<?)`,
			from, to, from, to, from, to, from, to).Scan(&p.Traffic, &p.NewUsers, &p.Orders, &p.Revenue)
		return p, err
	}
	if cur, err = read(curFrom, now.Unix()+1); err != nil {
		return
	}
	prev, err = read(prevFrom, curFrom)
	return
}

// ---- 套餐维度 ----

// PackageStat is one package's commercial and consumption picture. Sales and
// revenue come from orders; everything else from the buckets those orders
// created, which is what actually carries traffic.
type PackageStat struct {
	PackageID int64  `json:"package_id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Price     int64  `json:"price"`
	Orders    int64  `json:"orders"`  // successful orders, all time
	Revenue   int64  `json:"revenue"` // points taken by those orders
	// Buckets/Holders count live plan buckets, not orders: a user who bought
	// twice holds two buckets but is one holder.
	Buckets      int64 `json:"buckets"`
	Holders      int64 `json:"holders"`
	ActiveBucket int64 `json:"active_buckets"` // not expired and not over quota
	Traffic      int64 `json:"traffic"`        // used bytes across those buckets
	Quota        int64 `json:"quota"`          // granted finite bytes
	Unlimited    int64 `json:"unlimited"`      // compatibility field; always zero
	Expiring7d   int64 `json:"expiring_7d"`
}

// PackageStats aggregates every package that either still exists or has ever
// been sold. Left-joined from packages so a package with no sales still shows up
// (that it sells nothing is exactly what an operator needs to see), and unioned
// with order-only package ids so a deleted package's history is not lost.
func (s *Store) PackageStats() ([]PackageStat, error) {
	now := time.Now().Unix()
	rows, err := s.db.Query(`
		WITH ids AS (
			SELECT id AS pid FROM packages
			UNION
			SELECT DISTINCT package_id FROM orders WHERE status='success' AND package_id>0
			UNION
			SELECT DISTINCT package_id FROM user_plans WHERE kind='plan' AND package_id>0
		)
		SELECT ids.pid,
			COALESCE(p.name, (SELECT name FROM user_plans WHERE package_id=ids.pid AND name<>'' LIMIT 1), 'pkg#'||ids.pid),
			COALESCE(p.enabled,0), COALESCE(p.price_points,0),
			(SELECT COUNT(*) FROM orders o WHERE o.package_id=ids.pid AND o.status='success'),
			(SELECT COALESCE(SUM(o.price_points),0) FROM orders o WHERE o.package_id=ids.pid AND o.status='success'),
			(SELECT COUNT(*) FROM user_plans b WHERE b.package_id=ids.pid AND b.kind='plan'),
			(SELECT COUNT(DISTINCT b.user_id) FROM user_plans b WHERE b.package_id=ids.pid AND b.kind='plan'),
			(SELECT COUNT(*) FROM user_plans b WHERE b.package_id=ids.pid AND b.kind='plan'
				AND (b.expiry_at=0 OR b.expiry_at>?)
				AND b.traffic_limit>0 AND b.used_up+b.used_down < b.traffic_limit),
			(SELECT COALESCE(SUM(b.used_up+b.used_down),0) FROM user_plans b WHERE b.package_id=ids.pid AND b.kind='plan'),
			(SELECT COALESCE(SUM(b.traffic_limit),0) FROM user_plans b WHERE b.package_id=ids.pid AND b.kind='plan'),
			0,
			(SELECT COUNT(*) FROM user_plans b WHERE b.package_id=ids.pid AND b.kind='plan'
				AND b.traffic_limit>0 AND b.expiry_at>? AND b.expiry_at<=?)
		FROM ids LEFT JOIN packages p ON p.id=ids.pid
		ORDER BY 6 DESC, 5 DESC`,
		now, now, now+7*86400)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PackageStat{}
	for rows.Next() {
		var p PackageStat
		var enabled int
		if err := rows.Scan(&p.PackageID, &p.Name, &enabled, &p.Price, &p.Orders, &p.Revenue,
			&p.Buckets, &p.Holders, &p.ActiveBucket, &p.Traffic, &p.Quota, &p.Unlimited, &p.Expiring7d); err != nil {
			return nil, err
		}
		p.Enabled = enabled == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- 用户维度 ----

// UserStat is one user's row in the analysis table. RangeTraffic is what they
// moved inside the selected window (from traffic_samples), as opposed to Traffic
// which is the lifetime counter — the two answer different questions and the UI
// shows both.
type UserStat struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	Status       string `json:"status"`
	Packages     string `json:"packages"` // comma-joined bucket names
	Traffic      int64  `json:"traffic"`
	RangeTraffic int64  `json:"range_traffic"`
	TrafficLimit int64  `json:"traffic_limit"`
	ExpiryAt     int64  `json:"expiry_at"`
	LastOnlineAt int64  `json:"last_online_at"`
	Online       bool   `json:"online"`
	Spend        int64  `json:"spend"`
	Points       int64  `json:"points"`
	CreatedAt    int64  `json:"created_at"`
}

// UserStatFilter narrows and orders the user analysis table. Zero values mean
// "no filter", so the empty struct lists everyone.
type UserStatFilter struct {
	Query     string // username / email substring
	Status    string // users.status
	PackageID int64  // holds a live bucket of this package
	Expiry    string // expired | expiring_7d | active
	Online    bool   // seen within the online window
	Days      int    // window for RangeTraffic
	Sort      string // range_traffic | traffic | expiry | last_online | spend | created
	Desc      bool
	Limit     int
	Offset    int
}

// userStatSorts whitelists ORDER BY targets. The sort key arrives from the
// query string, so it is mapped through a fixed table rather than interpolated.
var userStatSorts = map[string]string{
	"range_traffic": "range_traffic",
	"traffic":       "u.used_up+u.used_down",
	"expiry":        "u.expiry_at",
	"last_online":   "u.last_online_at",
	"spend":         "spend",
	"created":       "u.created_at",
	"username":      "u.username",
}

// UserStats returns the filtered user table plus the total row count before
// paging, so the UI can show "共 N 人" without a second round trip.
func (s *Store) UserStats(f UserStatFilter) ([]UserStat, int64, error) {
	now := time.Now().Unix()
	onlineWindow := int64(intervalcfg.UserOnlineWindow(s) / time.Second)
	days := f.Days
	if days <= 0 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -days).Unix()

	where := []string{"u.role='user'"}
	args := []any{}
	if f.Query != "" {
		where = append(where, "(u.username LIKE ? OR COALESCE(u.email,'') LIKE ?)")
		like := "%" + f.Query + "%"
		args = append(args, like, like)
	}
	if f.Status != "" {
		where = append(where, "u.status=?")
		args = append(args, f.Status)
	}
	if f.PackageID > 0 {
		where = append(where, `EXISTS (SELECT 1 FROM user_plans b
			WHERE b.user_id=u.id AND b.kind='plan' AND b.package_id=?)`)
		args = append(args, f.PackageID)
	}
	switch f.Expiry {
	case "expired":
		where = append(where, "u.expiry_at>0 AND u.expiry_at<=?")
		args = append(args, now)
	case "expiring_7d":
		where = append(where, "u.expiry_at>? AND u.expiry_at<=?")
		args = append(args, now, now+7*86400)
	case "active":
		where = append(where, "(u.expiry_at=0 OR u.expiry_at>?)")
		args = append(args, now)
	}
	if f.Online {
		where = append(where, "u.last_online_at>=?")
		args = append(args, now-onlineWindow)
	}
	cond := "WHERE " + strings.Join(where, " AND ")

	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users u `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	order := userStatSorts[f.Sort]
	if order == "" {
		order = "range_traffic"
	}
	dir := "ASC"
	if f.Desc {
		dir = "DESC"
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	// since/spend subqueries run per row; the traffic_samples index on
	// (user_id, ts) keeps the range sum cheap.
	q := `SELECT u.id, u.username, u.status,
			COALESCE((SELECT GROUP_CONCAT(b.name, '、') FROM user_plans b
				WHERE b.user_id=u.id AND b.kind='plan' AND b.name<>''), ''),
			u.used_up+u.used_down,
			COALESCE((SELECT SUM(t.up+t.down) FROM traffic_samples t
				WHERE t.user_id=u.id AND t.ts>=?),0) AS range_traffic,
			u.traffic_limit, u.expiry_at, u.last_online_at,
			COALESCE((SELECT -SUM(pt.amount) FROM point_transactions pt
				WHERE pt.user_id=u.id AND pt.type='purchase'),0) AS spend,
			u.points, u.created_at
		FROM users u ` + cond + `
		ORDER BY ` + order + ` ` + dir + `, u.id ASC LIMIT ? OFFSET ?`
	rows, err := s.db.Query(q, append([]any{since}, append(args, limit, f.Offset)...)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []UserStat{}
	for rows.Next() {
		var u UserStat
		if err := rows.Scan(&u.ID, &u.Username, &u.Status, &u.Packages, &u.Traffic, &u.RangeTraffic,
			&u.TrafficLimit, &u.ExpiryAt, &u.LastOnlineAt, &u.Spend, &u.Points, &u.CreatedAt); err != nil {
			return nil, 0, err
		}
		u.Online = u.LastOnlineAt > 0 && now-u.LastOnlineAt <= onlineWindow
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// UserOnlineWindowSec is how recently last_online_at must have been bumped for
// a user to count as online. It tracks the live stats-poll cadence.
func (s *Store) UserOnlineWindowSec() int64 {
	return int64(intervalcfg.UserOnlineWindow(s) / time.Second)
}
