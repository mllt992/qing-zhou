package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Monthly traffic reset (#96).
//
// A periodic份 (period_days>0) keeps traffic_limit/used_* describing only the
// CURRENT 30-day period. When the period ends its usage moves to life_used_*,
// the counters restart at zero and the quota refills. Leftover quota is not
// carried over. Periods are anchored on the份's activation instant, so a panel
// that was down for days catches up to the right period instead of drifting.
//
// 预支: when allowed, traffic_limit = period_base + period_quota as long as a
// next period exists (inside this份 or a queued one behind it), so a user who
// used up this period can keep going on next period's quota. Whatever was
// borrowed is deducted from the next period's base when it starts.

// advanceOverrideOrDefault is the allow_advance value stored for a new row.
func advanceOverrideOrDefault(b *Bucket) int64 {
	if b.PeriodDays > 0 && b.AdvanceOverride >= 0 {
		return b.AdvanceOverride
	}
	return -1
}

// queueAdvanceOverride carries an admin's per-user 预支 setting to every new份
// in the same renewal line, like auto_renew.
func queueAdvanceOverride(tx txLike, userID int64, queueKey string) (int64, error) {
	var v int64
	err := tx.QueryRow(`SELECT allow_advance FROM user_plans
		WHERE user_id=? AND kind='plan' AND queue_key=? AND status<>? AND period_days>0
		ORDER BY id DESC LIMIT 1`, userID, queueKey, StatusRetired).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil
	}
	return v, err
}

// borrowedFromLine is how much the periodic head being replaced in this line
// borrowed from its next period — which is the first period of the份 taking
// over. Only a head that ended within the last period counts: a line that sat
// empty for a while has no next period left to owe.
func borrowedFromLine(tx txLike, userID int64, queueKey string, newID int64) (int64, error) {
	now := time.Now().Unix()
	var over int64
	err := tx.QueryRow(`SELECT COALESCE(MAX(used_up+used_down-period_base),0) FROM user_plans
		WHERE user_id=? AND kind='plan' AND queue_key=? AND status='active' AND id<>?
		  AND period_days>0 AND expiry_at>0 AND expiry_at>?-period_days*86400`,
		userID, queueKey, newID, now).Scan(&over)
	if err != nil {
		return 0, err
	}
	if over < 0 {
		over = 0
	}
	return over, nil
}

// effectiveAdvanceSQL resolves allow_advance against the package default.
const effectiveAdvanceSQL = `(CASE WHEN user_plans.allow_advance>=0 THEN user_plans.allow_advance
	ELSE COALESCE((SELECT k.allow_advance FROM packages k WHERE k.id=user_plans.package_id),0) END)`

// hasNextPeriodSQL: this份 has another period after the current one, or a
// queued份 waits behind it in the same line. Borrowing never reaches beyond the
// last period the user actually holds.
const hasNextPeriodSQL = `(user_plans.expiry_at=0
	OR user_plans.period_start+user_plans.period_days*86400 < user_plans.expiry_at
	OR EXISTS (SELECT 1 FROM user_plans q WHERE q.user_id=user_plans.user_id AND q.kind='plan'
	           AND q.queue_key=user_plans.queue_key AND q.status='queued'))`

const refreshLimitSQL = `UPDATE user_plans SET traffic_limit = period_base +
	CASE WHEN ` + effectiveAdvanceSQL + `=1 AND ` + hasNextPeriodSQL + ` THEN period_quota ELSE 0 END
	WHERE kind='plan' AND status='active' AND period_days>0 AND period_start>0`

// refreshPeriodLimits re-derives the borrowable limit of a user's live periodic份.
func refreshPeriodLimits(tx txLike, userID, now int64) error {
	_, err := tx.Exec(refreshLimitSQL+` AND user_id=?`, userID)
	return err
}

// refreshPeriodLimitsForPackage does the same for everyone holding a package
// (its 预支 default just changed).
func refreshPeriodLimitsForPackage(tx txLike, packageID, now int64) error {
	_, err := tx.Exec(refreshLimitSQL+` AND package_id=?`, packageID)
	return err
}

// periodDuePredicate matches a live periodic份 whose current period has ended
// while the份 itself still has time left.
const periodDuePredicate = `kind='plan' AND status='active' AND period_days>0 AND period_start>0
	AND period_start+period_days*86400<=?
	AND (expiry_at=0 OR expiry_at>period_start+period_days*86400)`

// ResetDuePeriods starts the next period for every份 whose period ended. It
// returns the users that were cut off for using up their quota and are back in
// service, so the caller can rebuild their config.
func (s *Store) ResetDuePeriods() ([]int64, error) {
	now := time.Now().Unix()
	rows, err := s.db.Query(`SELECT DISTINCT user_id FROM user_plans WHERE `+periodDuePredicate+` ORDER BY user_id`, now)
	if err != nil {
		return nil, err
	}
	var users []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			rows.Close()
			return nil, err
		}
		users = append(users, uid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var revived []int64
	var firstErr error
	for _, uid := range users {
		ch, err := s.resetUserPeriodsTx(uid, now)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("user %d: %w", uid, err)
			}
			continue
		}
		if ch {
			revived = append(revived, uid)
		}
	}
	return revived, firstErr
}

func (s *Store) resetUserPeriodsTx(userID, now int64) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	ch, err := resetUserPeriods(tx, userID, now)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	return ch, tx.Commit()
}

// resetUserPeriods rolls every due period of one user forward. Returns whether
// any份 that had run out of quota got a fresh one (config must be rebuilt).
func resetUserPeriods(tx txLike, userID, now int64) (bool, error) {
	rows, err := tx.Query(`SELECT id, period_days, period_quota, period_start, period_base,
		used_up, used_down, traffic_limit, expiry_at FROM user_plans WHERE user_id=? AND `+periodDuePredicate,
		userID, now)
	if err != nil {
		return false, err
	}
	type due struct{ id, days, quota, start, base, up, down, limit, expiry int64 }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.days, &d.quota, &d.start, &d.base, &d.up, &d.down, &d.limit, &d.expiry); err != nil {
			rows.Close()
			return false, err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	revived := false
	for _, d := range list {
		p := d.days * 86400
		n := (now - d.start) / p
		newStart := d.start + n*p
		used := d.up + d.down
		next := d.quota
		if n == 1 {
			// Only the period right after this one can have been borrowed from;
			// after a longer outage that period is already gone.
			if over := used - d.base; over > 0 {
				next -= over
			}
		}
		if next < 0 {
			next = 0
		}
		if _, err := tx.Exec(`UPDATE user_plans SET life_used_up=life_used_up+used_up,
			life_used_down=life_used_down+used_down, used_up=0, used_down=0,
			period_start=?, period_base=?, traffic_limit=?, updated_at=? WHERE id=?`,
			newStart, next, next, now, d.id); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`INSERT INTO plan_period_resets
			(bucket_id, user_id, reset_at, period_start, used, quota, next_quota) VALUES (?,?,?,?,?,?,?)`,
			d.id, userID, now, newStart, used, d.base, next); err != nil {
			return false, err
		}
		if d.limit <= 0 || used >= d.limit {
			revived = true
		}
	}
	if len(list) == 0 {
		return false, nil
	}
	if err := refreshPeriodLimits(tx, userID, now); err != nil {
		return false, err
	}
	if _, _, _, _, err := recomputeUserAggregate(tx, userID, now); err != nil {
		return false, err
	}
	return revived, nil
}

// PeriodReset is one logged period rollover.
type PeriodReset struct {
	BucketID    int64 `json:"bucket_id"`
	ResetAt     int64 `json:"reset_at"`
	PeriodStart int64 `json:"period_start"`
	Used        int64 `json:"used"`
	Quota       int64 `json:"quota"`
	NextQuota   int64 `json:"next_quota"`
}

// ListPeriodResets returns a user's most recent period rollovers, newest first.
func (s *Store) ListPeriodResets(userID int64, limit int) ([]PeriodReset, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT bucket_id, reset_at, period_start, used, quota, next_quota
		FROM plan_period_resets WHERE user_id=? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PeriodReset{}
	for rows.Next() {
		var r PeriodReset
		if err := rows.Scan(&r.BucketID, &r.ResetAt, &r.PeriodStart, &r.Used, &r.Quota, &r.NextQuota); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LineEnd is the end of one renewal line: when its last份 (queued ones
// included) runs out of time.
type LineEnd struct {
	QueueKey string
	EndAt    int64 // 0 with Never=false means nothing live
	Never    bool  // some份 never expires
	Queued   int   // queued份 behind the current one
	Current  int64 // expiry of the份 in service now (0 = none / never)
}

func lineKey(b *Bucket) string {
	if b.QueueKey != "" {
		return b.QueueKey
	}
	return fmt.Sprintf("pkg:%d", b.PackageID)
}

// LineEnds walks each plan line: from the live head's expiry (or now when the
// head already ended) add every queued份's duration. Because periodic heads
// hand over only on time expiry, the result is exact for them; for legacy份
// it is the latest possible date.
func LineEnds(buckets []*Bucket, now int64) map[string]*LineEnd {
	out := map[string]*LineEnd{}
	for _, b := range buckets {
		if b.Kind != "plan" || b.PackageID <= 0 || b.Status == StatusRetired {
			continue
		}
		k := lineKey(b)
		e := out[k]
		if e == nil {
			e = &LineEnd{QueueKey: k}
			out[k] = e
		}
		if b.Status == "active" && b.NotExpired(now) {
			if b.ExpiryAt == 0 {
				e.Never = true
			} else if b.ExpiryAt > e.Current {
				e.Current = b.ExpiryAt
			}
		}
	}
	for k, e := range out {
		cursor := e.Current
		if cursor == 0 {
			cursor = now
		}
		for _, b := range buckets {
			if b.Kind != "plan" || b.Status != "queued" || lineKey(b) != k {
				continue
			}
			e.Queued++
			if b.DurationDays <= 0 {
				e.Never = true
			}
			cursor += b.DurationDays * 86400
		}
		if e.Current > 0 || e.Queued > 0 {
			e.EndAt = cursor
		}
	}
	return out
}

// FinalExpiry is the latest end over every finite line plus whether any live
// line never expires. Admin grants and pools are not lines: a finite admin
// grant still counts by its own expiry.
func FinalExpiry(buckets []*Bucket, now int64) (end int64, never bool, queued int) {
	for _, e := range LineEnds(buckets, now) {
		if e.Never {
			never = true
		}
		queued += e.Queued
		if e.EndAt > end {
			end = e.EndAt
		}
	}
	for _, b := range buckets {
		if b.Kind == "plan" && b.PackageID <= 0 && b.Status == "active" && b.ExpiryAt > end && b.TrafficLimit > 0 && b.NotExpired(now) {
			end = b.ExpiryAt
		}
	}
	return
}

func listBucketsTx(tx txLike, userID int64) ([]*Bucket, error) {
	rows, err := tx.Query(`SELECT `+bucketCols+bucketFrom+` WHERE p.user_id=? ORDER BY p.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Bucket
	for rows.Next() {
		b, err := scanBucket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ErrLineNotFound means the user holds no live份 in that renewal line.
var ErrLineNotFound = errors.New("该用户没有这个套餐")

// ErrLineNotPeriodic means the line has no monthly-reset份 to act on.
var ErrLineNotPeriodic = errors.New("这个套餐没有按月重置")

func (s *Store) lineTx(userID int64, fn func(tx *sql.Tx, now int64) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if err := fn(tx, now); err != nil {
		return err
	}
	if _, err := advanceUserQueues(tx, userID, now); err != nil {
		return err
	}
	if _, _, _, _, err := recomputeUserAggregate(tx, userID, now); err != nil {
		return err
	}
	return tx.Commit()
}

// SetLineAdvance sets one user's 预支 override for a renewal line:
// mode -1 follows the package, 0 off, 1 on.
func (s *Store) SetLineAdvance(userID int64, queueKey string, mode int64) error {
	if mode < -1 || mode > 1 {
		return errors.New("预支设置无效")
	}
	return s.lineTx(userID, func(tx *sql.Tx, now int64) error {
		res, err := tx.Exec(`UPDATE user_plans SET allow_advance=?, updated_at=?
			WHERE user_id=? AND kind='plan' AND queue_key=? AND status<>? AND period_days>0`,
			mode, now, userID, queueKey, StatusRetired)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrLineNotPeriodic
		}
		return nil
	})
}

// SetLineMonthlyQuota changes the per-period quota of one user's line. The
// current period is adjusted by the same difference (never below zero), later
// periods use the new figure.
func (s *Store) SetLineMonthlyQuota(userID int64, queueKey string, bytes int64) error {
	if bytes <= 0 {
		return errors.New("每月流量必须大于 0")
	}
	return s.lineTx(userID, func(tx *sql.Tx, now int64) error {
		res, err := tx.Exec(`UPDATE user_plans SET
			period_base=MAX(period_base+?-period_quota,0),
			traffic_limit=CASE WHEN status='queued' THEN ? ELSE MAX(period_base+?-period_quota,0) END,
			period_quota=?, updated_at=?
			WHERE user_id=? AND kind='plan' AND queue_key=? AND status<>? AND period_days>0`,
			bytes, bytes, bytes, bytes, now, userID, queueKey, StatusRetired)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrLineNotPeriodic
		}
		return nil
	})
}

// ConvertLineToPeriodic switches one user's already-sold (legacy, lump-sum)份
// in a line to monthly reset. Only an admin does this, deliberately: each份's
// total is split evenly over its periods; the份 in service starts a fresh
// period at the boundary its activation implies, with earlier usage archived.
func (s *Store) ConvertLineToPeriodic(userID int64, queueKey string) (int, error) {
	converted := 0
	err := s.lineTx(userID, func(tx *sql.Tx, now int64) error {
		rows, err := tx.Query(`SELECT id, status, traffic_limit, duration_days, expiry_at FROM user_plans
			WHERE user_id=? AND kind='plan' AND package_id>0 AND queue_key=? AND period_days=0
			  AND (status='queued' OR (status='active' AND (expiry_at=0 OR expiry_at>?)))
			ORDER BY id`, userID, queueKey, now)
		if err != nil {
			return err
		}
		type row struct {
			id                 int64
			status             string
			limit, dur, expiry int64
		}
		var list []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.status, &r.limit, &r.dur, &r.expiry); err != nil {
				rows.Close()
				return err
			}
			list = append(list, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(list) == 0 {
			return ErrLineNotFound
		}
		p := PeriodDays * 86400
		for _, r := range list {
			q := r.limit / PeriodCount(r.dur)
			if q <= 0 {
				q = r.limit
			}
			if r.status == "queued" {
				_, err = tx.Exec(`UPDATE user_plans SET period_days=?, period_quota=?, period_base=?, traffic_limit=?, updated_at=? WHERE id=?`,
					PeriodDays, q, q, q, now, r.id)
			} else {
				act := now
				if r.expiry > 0 && r.dur > 0 {
					act = r.expiry - r.dur*86400
				}
				if act > now {
					act = now
				}
				start := act + ((now-act)/p)*p
				_, err = tx.Exec(`UPDATE user_plans SET period_days=?, period_quota=?, period_base=?, traffic_limit=?,
					period_start=?, life_used_up=life_used_up+used_up, life_used_down=life_used_down+used_down,
					used_up=0, used_down=0, updated_at=? WHERE id=?`,
					PeriodDays, q, q, q, start, now, r.id)
			}
			if err != nil {
				return err
			}
			converted++
		}
		return nil
	})
	return converted, err
}
