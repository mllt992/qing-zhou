package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// execer / txLike are satisfied by both *sql.DB and *sql.Tx, so bucket ops work
// standalone (migration) or inside a purchase transaction (which must read its
// own uncommitted writes — the pool-bucket lookup — on the same connection).
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}
type txLike interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

// enqueuePlanBucket mints a fresh, independently-metered plan bucket in the
// 'queued' state for a plan purchase. Unlike the old stacking model, buying a
// plan in the SAME queue_key does NOT merge into one bucket — each purchase is
// its own metered quota. A queued bucket does not count down
// (expiry_at=0) and is invisible to the config until advanceUserQueues promotes
// it. The caller runs advanceUserQueues right after, so a first purchase (no
// active head yet) activates immediately while a repeat purchase waits in line.
func enqueuePlanBucket(tx txLike, userID int64, username string, pkg *Package, orderID, now int64) error {
	// UUID/secret belong to the user. The package line keeps a stable internal
	// client_name for stats; user_plans.client_name is only a unique row label.
	_, _, err := ensureUserProtocolCredential(tx, userID, username, now)
	if err != nil {
		return err
	}
	if err := ensurePlanIdentity(tx, userID, pkg.ID, username, now); err != nil {
		return err
	}
	_, err = insertBucket(tx, &Bucket{
		UserID: userID, Kind: "plan", PackageID: pkg.ID, Name: pkg.Name,
		QueueKey:     effectiveQueueKey(pkg.ID, pkg.QueueKey),
		ClientName:   fmt.Sprintf("qz_%s_p%d", username, orderID),
		TrafficLimit: pkg.TrafficBytes,
		Status:       "queued", ExpiryAt: 0, DurationDays: pkg.DurationDays,
		OrderID: orderID, CreatedAt: now,
	})
	return err
}

// usableHeadPredicate matches the plan bucket that currently OWNS a package's
// queue slot: an active head that is neither expired nor out of quota. While one
// exists the queued份 behind it wait; when none does, the slot is free and the
// oldest queued份 is promoted.
//
// Kept in one place because every query that asks it must agree exactly: the
// promotion, the cheap "is anything due for this user?" check the read path runs,
// the stats attribution, and the migration. If any of those drifted, one would
// skip a份 another considers live — a silent version of the very bug this fixes.
//
// usableExpr renders it for a given table alias against SQLite's own clock;
// usableHeadPredicate is the promotion's variant, which also pins kind and takes
// `now` as a bound parameter because it runs inside a transaction that has
// already fixed a timestamp.
func usableExpr(alias string) string {
	a := alias + "."
	return `(` + a + `status='active'
		AND (` + a + `expiry_at=0 OR ` + a + `expiry_at>strftime('%s','now'))
		AND ` + a + `traffic_limit>0
		AND ` + a + `used_up+` + a + `used_down<` + a + `traffic_limit)`
}

const usableHeadPredicate = `h.kind='plan' AND h.status='active'
	AND (h.expiry_at=0 OR h.expiry_at>?)
	AND h.traffic_limit>0
	AND h.used_up+h.used_down<h.traffic_limit`

// StatusRetired marks a plan bucket that has finished its turn and handed the
// renewal line's slot to the next份.
//
// Such a份 used to keep status='active' forever, which conflated "is the current
// plan" with "was once the current plan" — and two things read that column as the
// former:
//
//   - mergeDuplicatePlanBuckets, the one-time repair for pre-queue accounts, saw
//     a progressed queue as same-package duplicates and collapsed every consumed
//     month into a single bucket on the next restart, deleting the live one;
//   - the credential carry-forward, once the live份 was removed by a refund,
//     picked a consumed份 as the credential holder and tried to hand it a name it
//     already had, failing the refund outright.
//
// Credentials have since moved to the user, so that second reason is
// gone — but the first stands on its own: without this status a progressed queue
// is indistinguishable from legacy duplicates and the merge eats it. Everything
// else keys off expiry/quota rather than this column, and a retired份 is by
// definition out of one or the other, so nothing else changes behaviour: it still
// renders as 已过期/已用尽 and still stays out of every config.
const StatusRetired = "retired"

// planIdentityCols is the credential set a subscription line carries.
const planIdentityCols = `client_name, client_uuid, client_secret,
	proxy_username, proxy_password, proxy_expires_at`

// PlanIdentity is one subscription line's internal stats/proxy identity. Its
// UUID/secret fields remain as online-upgrade sources; users.client_* is runtime
// protocol authority.
type PlanIdentity struct {
	ClientName     string
	ClientUUID     string
	ClientSecret   string
	ProxyUsername  string
	ProxyPassword  string
	ProxyExpiresAt int64
}

// ensurePlanIdentity mints this (user, package) stats line the first time the
// user buys the package, and reuses it every time after.
//
// A handoff never changes the user credential or line name. A refund/revoke can
// delete any份 — even the one in service — without orphaning accounting.
func ensurePlanIdentity(tx txLike, userID, packageID int64, username string, now int64) error {
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM plan_identities WHERE user_id=? AND package_id=?`,
		userID, packageID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO plan_identities
		(user_id, package_id, client_name, client_uuid, client_secret, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`,
		userID, packageID, fmt.Sprintf("qz_%s_s%d", username, packageID), "", "", now, now)
	return err
}

// queueDuePredicate matches a user who has a queued份 whose slot is free, i.e.
// someone whose next套餐 should be live right now. Expects `q` bound to
// user_plans and one parameter (now) for the head predicate inside.
const queueDuePredicate = `q.kind='plan' AND q.status='queued' AND q.package_id>0
	AND NOT EXISTS (SELECT 1 FROM user_plans h
	    WHERE h.user_id=q.user_id AND h.queue_key=q.queue_key AND ` + usableHeadPredicate + `)`

// advanceUserQueues promotes queued plan buckets whose slot is now free. For each
// renewal line the user has a queued bucket for, if there is no currently-USABLE
// active head (same queue_key, status='active', not expired, has quota), the
// oldest queued bucket is promoted: status='active' and its expiry_at starts counting now
// (now + duration_days). Idempotent — a usable head means no change. Returns
// whether anything was promoted so callers can trigger a config rebuild.
func advanceUserQueues(tx txLike, userID, now int64) (bool, error) {
	rows, err := tx.Query(`SELECT DISTINCT queue_key FROM user_plans
		WHERE user_id=? AND kind='plan' AND status='queued' AND package_id>0`, userID)
	if err != nil {
		return false, err
	}
	var queueKeys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return false, err
		}
		queueKeys = append(queueKeys, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	changed := false
	for _, queueKey := range queueKeys {
		// A usable active head blocks promotion: not expired AND has quota.
		var usable int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM user_plans h
			WHERE h.user_id=? AND h.queue_key=? AND `+usableHeadPredicate,
			userID, queueKey, now).Scan(&usable); err != nil {
			return changed, err
		}
		if usable > 0 {
			continue
		}
		// Promote the oldest queued bucket for this renewal line.
		var id, dur int64
		err := tx.QueryRow(`SELECT id, duration_days FROM user_plans
			WHERE user_id=? AND kind='plan' AND queue_key=? AND status='queued'
			ORDER BY id LIMIT 1`, userID, queueKey).Scan(&id, &dur)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return changed, err
		}
		newExpiry := int64(0) // duration 0 = unlimited-duration plan → never expires
		if dur > 0 {
			newExpiry = now + dur*86400
		}
		if _, err := tx.Exec(`UPDATE user_plans SET status='active', expiry_at=?, updated_at=? WHERE id=?`,
			newExpiry, now, id); err != nil {
			return changed, err
		}
		// Retire whoever was holding the slot. Nothing is moved: the credentials
		// live on the user, so the份 taking over already has them.
		if _, err := tx.Exec(`UPDATE user_plans SET status=?, updated_at=?
			WHERE user_id=? AND kind='plan' AND queue_key=? AND status='active' AND id<>?`,
			StatusRetired, now, userID, queueKey, id); err != nil {
			return changed, err
		}
		changed = true
	}
	// A promotion changes the user's effective expiry (the newly-active份 starts its
	// countdown now). Recompute the legacy users.* aggregate so the dashboard's
	// top-line expiry/"已过期" alert reflects the fresh plan instead of the retired
	// head's now-past date. Enforcement reads buckets and is already correct; this
	// only keeps the display mirror in sync when promotion happens outside a
	// purchase/refund (i.e. the periodic ticker).
	if changed {
		if _, _, _, _, err := recomputeUserAggregate(tx, userID, now); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// AdvanceAllQueues promotes due queued buckets across every user (exhausted or
// expired heads free their slot). Returns the users whose queue changed, so the
// caller can push fresh config only where an identity actually activated.
//
// One user's failure must not abort the rest. The loop used to return on the
// first error, which turned a single unlucky transaction into a PERMANENT outage
// for every user behind them in id order: the next tick started from the same
// user and failed the same way, so their paid份 never activated and they stayed
// on an expired套餐 indefinitely. Failures are collected and reported, never
// allowed to short-circuit the sweep.
//
// Only users who actually have a promotion pending get a transaction. Open() sets
// _txlock=immediate, so the old version — which opened one for every user holding
// any queued份, including the majority whose head was still fine — took SQLite's
// single write lock every two minutes against the stats poll, which is where
// those unlucky failures came from in the first place.
func (s *Store) AdvanceAllQueues() ([]int64, error) {
	now := time.Now().Unix()
	rows, err := s.db.Query(`SELECT DISTINCT q.user_id FROM user_plans q
		WHERE `+queueDuePredicate+` ORDER BY q.user_id`, now)
	if err != nil {
		return nil, err
	}
	var userIDs []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			rows.Close()
			return nil, err
		}
		userIDs = append(userIDs, uid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var changed []int64
	var firstErr error
	failed := 0
	for _, uid := range userIDs {
		ch, err := s.advanceOne(uid, now)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("user %d: %w", uid, err)
			}
			continue
		}
		if ch {
			changed = append(changed, uid)
		}
	}
	if firstErr != nil {
		return changed, fmt.Errorf("%d of %d due user(s) failed to advance; first: %w", failed, len(userIDs), firstErr)
	}
	return changed, nil
}

// advanceOne runs one user's promotion in its own transaction.
func (s *Store) advanceOne(userID, now int64) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	ch, err := advanceUserQueues(tx, userID, now)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return ch, nil
}

// AdvanceQueueFor promotes this one user's due queued份 right now, and reports
// whether anything activated.
//
// This is the fix that actually guarantees the user gets what they paid for. A
// periodic sweep is a hope, not a guarantee — it can be behind, it can have
// failed on this user, it does not run at all in a process where the background
// loops were never started — and while it is not running the user sits looking at
// an expired套餐 with the next one already bought. Calling this before answering
// a read means the answer is never "已到期" while a paid份 is sitting due.
//
// Cheap when there is nothing to do: the due check is a plain indexed read
// (user_plans has an index on user_id), so no write transaction is opened in the
// common case.
func (s *Store) AdvanceQueueFor(userID int64) (bool, error) {
	now := time.Now().Unix()
	var due int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_plans q
		WHERE q.user_id=? AND `+queueDuePredicate, userID, now).Scan(&due); err != nil {
		return false, err
	}
	if due == 0 {
		return false, nil
	}
	return s.advanceOne(userID, now)
}

// reversePlanBucket undoes one plan order's contribution to its package bucket
// (used on refund): it subtracts the package's traffic quota (clamped so the
// limit never drops below what's already used) and one duration period from the
// expiry. If nothing is left (no remaining quota and expired), the bucket is
// removed; otherwise it survives with the reduced allowances — correct whether
// the bucket held a single purchase or several stacked renewals.
func reversePlanBucket(tx txLike, userID, packageID, trafficBytes, durationDays, now int64) error {
	var id, limit, used, expiry int64
	err := tx.QueryRow(`SELECT id, traffic_limit, used_up+used_down, expiry_at FROM user_plans
		WHERE user_id=? AND kind='plan' AND package_id=? ORDER BY id LIMIT 1`, userID, packageID).Scan(&id, &limit, &used, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	newLimit := limit - trafficBytes
	if newLimit < used {
		newLimit = used
	}
	if newLimit < 0 {
		newLimit = 0
	}
	newExpiry := expiry - durationDays*86400
	if newLimit <= used && newExpiry <= now {
		_, err = tx.Exec(`DELETE FROM user_plans WHERE id=?`, id)
		return err
	}
	_, err = tx.Exec(`UPDATE user_plans SET traffic_limit=?, expiry_at=?, updated_at=? WHERE id=?`, newLimit, newExpiry, now, id)
	return err
}

// reverseOrderBucket undoes ONE order's plan entitlement on refund. In the queue
// model each purchase is its own bucket, so a refund removes exactly that bucket
// (found by order_id) and then advances the queue — if the removed bucket was the
// active head, the next queued bucket in its renewal line takes over. Falls back to the
// legacy package-based clamped reversal (reversePlanBucket) when no bucket carries
// this order_id (a pre-queue account) or when the matched bucket still holds more
// than this one order's quota (a legacy merged bucket whose order_id points here),
// so refunding an old stacked order never wipes several orders' entitlement.
func reverseOrderBucket(tx txLike, orderID, userID, packageID, trafficBytes, durationDays, now int64) error {
	var id, limit int64
	var status string
	err := tx.QueryRow(`SELECT id, traffic_limit, status FROM user_plans
		WHERE kind='plan' AND user_id=? AND order_id=? ORDER BY id LIMIT 1`, userID, orderID).Scan(&id, &limit, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return reversePlanBucket(tx, userID, packageID, trafficBytes, durationDays, now)
	}
	if err != nil {
		return err
	}
	// A single-order (queue-model) bucket holds exactly this order's quota; a legacy
	// merged bucket holds the sum of several stacked orders (limit > this order's
	// bytes) — reverse that the old, clamped way instead of deleting it wholesale.
	if status == "active" && trafficBytes > 0 && limit > trafficBytes {
		return reversePlanBucket(tx, userID, packageID, trafficBytes, durationDays, now)
	}
	if _, err := tx.Exec(`DELETE FROM user_plans WHERE id=?`, id); err != nil {
		return err
	}
	_, err = advanceUserQueues(tx, userID, now)
	return err
}

// addToPool tops up the user's traffic-package pool, creating it if absent.
func addToPool(tx txLike, userID int64, username string, addBytes, now int64) error {
	var id int64
	err := tx.QueryRow(`SELECT id FROM user_plans WHERE user_id=? AND kind='pool' ORDER BY id LIMIT 1`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		_, _, cerr := ensureUserProtocolCredential(tx, userID, username, now)
		if cerr != nil {
			return cerr
		}
		_, err = insertBucket(tx, &Bucket{
			UserID: userID, Kind: "pool", Name: "通用流量",
			ClientName:   fmt.Sprintf("qz_%s_pool", username),
			TrafficLimit: addBytes, CreatedAt: now,
		})
		return err
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE user_plans SET traffic_limit=traffic_limit+?, updated_at=? WHERE id=?`, addBytes, now, id)
	return err
}

// subFromPool reverses a traffic-package top-up (refund), clamped so the pool
// limit never drops below what's already been used, nor below zero.
func subFromPool(tx txLike, userID int64, subBytes, now int64) error {
	var id, limit, used int64
	err := tx.QueryRow(`SELECT id, traffic_limit, used_up+used_down FROM user_plans WHERE user_id=? AND kind='pool' ORDER BY id LIMIT 1`, userID).Scan(&id, &limit, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	nl := limit - subBytes
	if nl < used {
		nl = used
	}
	if nl < 0 {
		nl = 0
	}
	_, err = tx.Exec(`UPDATE user_plans SET traffic_limit=?, updated_at=? WHERE id=?`, nl, now, id)
	return err
}

// recomputeUserAggregate rebuilds the legacy users.* aggregate columns from the
// authoritative buckets after a bucket change (e.g. a refund), so the dashboard
// totals stay consistent with what enforcement actually sees. traffic_limit /
// used_up / used_down are summed across positive, non-free buckets; expiry_at is
// the latest positive plan-bucket expiry (0 with no finite plan entitlement).
//
// Free buckets are excluded from all three sums: they carry no paid limit, and
// their separately metered usage must not inflate the user's finite paid-quota
// totals or dashboard percentage.
func recomputeUserAggregate(tx txLike, userID, now int64) (limit, up, down, expiry int64, err error) {
	err = tx.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN kind=? OR traffic_limit<=0 THEN 0 ELSE traffic_limit END),0),
		COALESCE(SUM(CASE WHEN kind=? OR traffic_limit<=0 THEN 0 ELSE used_up END),0),
		COALESCE(SUM(CASE WHEN kind=? OR traffic_limit<=0 THEN 0 ELSE used_down END),0),
		COALESCE(MAX(CASE WHEN kind='plan' AND traffic_limit>0 THEN expiry_at ELSE 0 END),0)
		FROM user_plans WHERE user_id=?`,
		KindFree, KindFree, KindFree, userID).Scan(&limit, &up, &down, &expiry)
	if err != nil {
		return
	}
	_, err = tx.Exec(`UPDATE users SET traffic_limit=?, used_up=?, used_down=?, expiry_at=?, updated_at=? WHERE id=?`,
		limit, up, down, expiry, now, userID)
	return
}

const finiteTrafficAggregateMigration = "finite-traffic-aggregate-v1"

// migrateFiniteTrafficAggregates repairs accounts created while zero traffic was
// treated as an uncapped grant. The buckets are authoritative: discard only the
// synthetic zero-byte welcome row, retain commercial/admin rows for audit and
// manual repair, then rebuild every legacy users.* summary with finite semantics.
func (s *Store) migrateFiniteTrafficAggregates() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var applied int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, finiteTrafficAggregateMigration).Scan(&applied); err != nil {
		return err
	}
	if applied > 0 {
		return tx.Commit()
	}
	if _, err := tx.Exec(`DELETE FROM user_plans WHERE kind='plan' AND package_id=? AND traffic_limit<=0`, WelcomePackageID); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT id FROM users ORDER BY id`)
	if err != nil {
		return err
	}
	var userIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		userIDs = append(userIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, id := range userIDs {
		if _, _, _, _, err := recomputeUserAggregate(tx, id, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(?,?)`, finiteTrafficAggregateMigration, now); err != nil {
		return err
	}
	return tx.Commit()
}

// KindFree is the bucket holding a user's free-group (unmetered) allowance.
//
// It exists so free traffic gets its own sing-box stats identity. Metering is
// identity-based, so when the pool covered the free group the free bytes were
// debited from the user's PAID balance — and since a top-up only raises
// traffic_limit and never clears used_*, traffic burned on free nodes before a
// purchase silently ate into that purchase. A free bucket has no limit and never
// expires; its usage is recorded for display but excluded from the user's quota
// aggregate (see recomputeUserAggregate).
const KindFree = "free"

// WelcomePackageID marks the signup-grant bucket. It is a plan bucket (so the
// trial actually expires) but belongs to no package, like the admin grant's
// package_id=0 — orderBuckets scopes both the same way. Distinct from 0 so an
// admin grant and a signup grant can coexist instead of overwriting each other.
const WelcomePackageID = -1

// HasLivePaidPlan reports whether the user holds a purchased or admin-assigned
// plan that is still in play (active or queued). Used by the email-verify
// subscription gate: a live paid plan is a real entitlement and must release
// the nodes that plan unlocks, even if the verify mail is unclicked. The
// signup grant (WelcomePackageID = -1) and the free-group bucket do not
// count — those are minted for every provisioned account and must not punch
// a hole in the gate by themselves.
func (s *Store) HasLivePaidPlan(userID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM user_plans
		WHERE user_id=? AND kind='plan' AND package_id>=0
		  AND status IN ('active','queued')
		  AND (expiry_at=0 OR expiry_at>?)`,
		userID, time.Now().Unix()).Scan(&n)
	return n > 0, err
}

// EnsureFreeBucket creates the user's unmetered free-group bucket if it has none
// yet. Idempotent, so it doubles as the backfill for accounts provisioned before
// the free bucket existed.
func (s *Store) EnsureFreeBucket(userID int64, username string) error {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM user_plans WHERE user_id=? AND kind=? ORDER BY id LIMIT 1`,
		userID, KindFree).Scan(&id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, _, err = ensureUserProtocolCredential(s.db, userID, username, time.Now().Unix())
	if err != nil {
		return err
	}
	_, err = insertBucket(s.db, &Bucket{
		UserID: userID, Kind: KindFree, Name: "免费流量",
		ClientName: fmt.Sprintf("qz_%s_free", username),
	})
	return err
}

// EnsureWelcomeBucket lands the configured signup grant (default_traffic /
// default_expiry_days) in a real bucket.
//
// Writing it to users.traffic_limit / users.expiry_at — which is what
// registration used to do — did nothing: those columns are a display mirror
// recomputed from the buckets, and enforcement reads buckets only. So the grant
// was invisible to sing-box (a user with no free group configured landed in no
// inbound at all), and the moment the user bought anything the recompute
// overwrote expiry_at with the max *plan* expiry — zero for a pool-only buyer —
// which handleSub reads as "never expires". Same class of bug as the admin grant
// fixed in 5bea5ad; this is the registration path it missed.
//
// Creates nothing when the grant has no traffic, regardless of the configured
// expiry, and still recomputes the legacy aggregate to clear values staged by an
// older registration path. A positive grant is idempotent. In either case the
// aggregate mirrors the authoritative buckets when this returns.
func (s *Store) EnsureWelcomeBucket(userID int64, username string, traffic, expiry int64) error {
	// Zero is zero quota, never an implicit unlimited grant. In particular, an
	// operator may leave the trial duration configured while setting its traffic
	// to zero; that combination means "no signup grant", not "unlimited for N days".
	if traffic <= 0 {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, _, _, _, err := recomputeUserAggregate(tx, userID, time.Now().Unix()); err != nil {
			return err
		}
		return tx.Commit()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var id int64
	err = tx.QueryRow(`SELECT id FROM user_plans WHERE user_id=? AND kind='plan' AND package_id=? ORDER BY id LIMIT 1`,
		userID, WelcomePackageID).Scan(&id)
	switch {
	case err == nil:
		return nil // already granted
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	_, _, err = ensureUserProtocolCredential(tx, userID, username, time.Now().Unix())
	if err != nil {
		return err
	}
	if _, err = insertBucket(tx, &Bucket{
		UserID: userID, Kind: "plan", PackageID: WelcomePackageID, Name: "注册赠送",
		ClientName:   fmt.Sprintf("qz_%s_welcome", username),
		TrafficLimit: traffic, ExpiryAt: expiry,
	}); err != nil {
		return err
	}
	if _, _, _, _, err = recomputeUserAggregate(tx, userID, time.Now().Unix()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// EnsurePoolBucket creates the user's pool bucket with the given identity if it
// has none yet (called when a new user is provisioned).
func (s *Store) EnsurePoolBucket(userID int64, name, clientUUID, clientSecret string) error {
	// Provisioning normally wrote users first and the pool second. Keep this
	// helper safe for imports/tests/admin flows that arrive in the opposite order:
	// the first supplied credential becomes the user's stable protocol identity.
	if _, _, err := ensureSpecificUserProtocolCredential(s.db, userID, name, clientUUID, clientSecret, time.Now().Unix()); err != nil {
		return err
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM user_plans WHERE user_id=? AND kind='pool' ORDER BY id LIMIT 1`, userID).Scan(&id)
	if err == nil {
		return nil // already has one
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = insertBucket(s.db, &Bucket{
		UserID: userID, Kind: "pool", Name: "通用流量",
		ClientName: name,
	})
	return err
}

// Bucket is an independently-metered unit a user holds: a purchased plan
// (Kind="plan") or the shared traffic-package pool (Kind="pool"). ClientName is
// its stats identity; ClientUUID/ClientSecret resolve to the stable user pair.
type Bucket struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	Kind         string `json:"kind"` // plan | pool
	PackageID    int64  `json:"package_id"`
	QueueKey     string `json:"queue_key,omitempty"`
	Name         string `json:"name"`
	ClientName   string `json:"-"`
	ClientUUID   string `json:"-"`
	ClientSecret string `json:"-"`
	TrafficLimit int64  `json:"traffic_limit"`
	UsedUp       int64  `json:"used_up"`
	UsedDown     int64  `json:"used_down"`
	ExpiryAt     int64  `json:"expiry_at"`
	LastOnlineAt int64  `json:"last_online_at"`
	OrderID      int64  `json:"order_id"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	// Status is the queue state of a plan bucket: 'active' (the current head that
	// meters/renders) or 'queued' (a same-queue purchase waiting its turn; not
	// yet counting down and not in any config). pool/free are always 'active'.
	// DurationDays is the package duration a queued bucket applies to its expiry
	// when it is promoted to active.
	Status       string `json:"status"`
	DurationDays int64  `json:"duration_days"`
	// Mixed (HTTP/SOCKS5) proxy credential — a proxy-only account, unrelated to the
	// login account. Empty ProxyUsername → fall back to ClientName/ClientSecret.
	// ProxyExpiresAt 0 = permanent. See migrate.go for the schema.
	ProxyUsername  string `json:"-"`
	ProxyPassword  string `json:"-"`
	ProxyExpiresAt int64  `json:"-"`
}

// ProxyName is the mixed-proxy stats identity for this bucket: the custom
// username if set, else the system ClientName. This is the name sing-box tracks
// (and AddBucketUsage meters) for the bucket's mixed inbounds.
func (b *Bucket) ProxyName() string {
	if b.ProxyUsername != "" {
		return b.ProxyUsername
	}
	return b.ClientName
}

// ProxySecret is the mixed-proxy password: the custom one if set, else the
// system ClientSecret.
func (b *Bucket) ProxySecret() string {
	if b.ProxyPassword != "" {
		return b.ProxyPassword
	}
	return b.ClientSecret
}

// ProxyActive reports whether the mixed-proxy credential is usable now: the
// bucket itself must be able to carry traffic AND the proxy credential must not
// have hit its own (separate, optional) expiry.
func (b *Bucket) ProxyActive(now int64) bool {
	if b.ProxyExpiresAt != 0 && b.ProxyExpiresAt <= now {
		return false
	}
	return b.Active(now)
}

// Used is the bucket's total consumed bytes.
func (b *Bucket) Used() int64 { return b.UsedUp + b.UsedDown }

// HasQuota reports whether the bucket has a positive amount of traffic left.
// A zero limit is an empty bucket, never an implicit unlimited entitlement.
func (b *Bucket) HasQuota() bool { return b.TrafficLimit > 0 && b.Used() < b.TrafficLimit }

// NotExpired reports whether the bucket is still within its time window.
func (b *Bucket) NotExpired(now int64) bool { return b.ExpiryAt == 0 || b.ExpiryAt > now }

// Active reports whether the bucket can currently carry traffic. A pool is only
// active when it has a positive, non-exhausted balance (an empty pool is inert);
// a plan is active while not expired and not over quota; a free bucket is always
// active — it is the unmetered free-group allowance and has no limit to exhaust.
func (b *Bucket) Active(now int64) bool {
	switch b.Kind {
	case "pool":
		return b.TrafficLimit > 0 && b.Used() < b.TrafficLimit && b.NotExpired(now)
	case KindFree:
		return true
	}
	return b.NotExpired(now) && b.HasQuota()
}

// bucketCols selects a bucket with its credentials already resolved.
//
// The internal stats name still comes from the package line (or the bucket for
// pool/free/grants), while UUID/secret always come from users. Thus a queue or
// node owner can change without changing the credential already in the client.
//
// Resolving here rather than at each call site means everything downstream —
// config generation, link building, ownership — keeps reading b.ClientUUID and
// simply gets the stable value.
//
// The proxy account falls back as a UNIT and only when the line has none: a
// plain COALESCE would not do, because an identity row stores an empty string
// rather than NULL for "no proxy account", and empty is a value — it would shadow
// account still sitting on the bucket row instead of falling through to it.
const bucketCols = `p.id, p.user_id, p.kind, p.package_id, p.queue_key, p.name,
	COALESCE(i.client_name, p.client_name),
	COALESCE(u.client_uuid,''), COALESCE(u.client_secret,''),
	p.traffic_limit, p.used_up, p.used_down, p.expiry_at, p.last_online_at, p.order_id,
	p.created_at, p.updated_at,
	CASE WHEN COALESCE(i.proxy_username,'')<>'' THEN i.proxy_username ELSE p.proxy_username END,
	CASE WHEN COALESCE(i.proxy_username,'')<>'' THEN i.proxy_password ELSE p.proxy_password END,
	CASE WHEN COALESCE(i.proxy_username,'')<>'' THEN i.proxy_expires_at ELSE p.proxy_expires_at END,
	p.status, p.duration_days`

// bucketFrom is the FROM clause bucketCols expects. The join is scoped to real
// plan份 (package_id>0); pool/free and package-less grants keep their row stats
// names. The users join supplies protocol authentication for every kind.
const bucketFrom = ` FROM user_plans p
	LEFT JOIN plan_identities i
	  ON i.user_id=p.user_id AND i.package_id=p.package_id AND p.kind='plan' AND p.package_id>0
	LEFT JOIN users u ON u.id=p.user_id`

func scanBucket(sc scanner) (*Bucket, error) {
	var b Bucket
	err := sc.Scan(&b.ID, &b.UserID, &b.Kind, &b.PackageID, &b.QueueKey, &b.Name, &b.ClientName, &b.ClientUUID,
		&b.ClientSecret, &b.TrafficLimit, &b.UsedUp, &b.UsedDown, &b.ExpiryAt, &b.LastOnlineAt,
		&b.OrderID, &b.CreatedAt, &b.UpdatedAt,
		&b.ProxyUsername, &b.ProxyPassword, &b.ProxyExpiresAt, &b.Status, &b.DurationDays)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ListBuckets returns all of a user's buckets (plans + pool), oldest first.
func (s *Store) ListBuckets(userID int64) ([]*Bucket, error) {
	rows, err := s.db.Query(`SELECT `+bucketCols+bucketFrom+` WHERE p.user_id=? ORDER BY p.id`, userID)
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

// ListBucketsBulk returns the buckets of several users in one query, keyed by
// user id. The admin user list needs every user's buckets to roll up traffic
// correctly (the users.* columns are a naive sum — see AdminUserTraffic), and
// one ListBuckets per row would be a query per user.
func (s *Store) ListBucketsBulk(userIDs []int64) (map[int64][]*Bucket, error) {
	out := map[int64][]*Bucket{}
	if len(userIDs) == 0 {
		return out, nil
	}
	q := `SELECT ` + bucketCols + bucketFrom + ` WHERE p.user_id IN (?` +
		strings.Repeat(`,?`, len(userIDs)-1) + `) ORDER BY p.user_id, p.id`
	args := make([]any, len(userIDs))
	for i, id := range userIDs {
		args[i] = id
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		b, err := scanBucket(rows)
		if err != nil {
			return nil, err
		}
		out[b.UserID] = append(out[b.UserID], b)
	}
	return out, rows.Err()
}

var (
	// ErrBucketNotFound — no such bucket for that user (wrong id, or already gone).
	ErrBucketNotFound = errors.New("该套餐不存在")
	// ErrBucketProtected — the free bucket is the account's unmetered metering
	// identity, not something the user was granted; removing it would silently
	// re-route free-group traffic onto the paid pool (the very coupling it exists
	// to break), so it is not removable.
	ErrBucketProtected = errors.New("该额度是系统内部计量身份，不能移除")
	// ErrZeroDelta — an adjust of 0 bytes is a no-op the admin almost certainly
	// did not mean to submit.
	ErrZeroDelta = errors.New("调整量不能为 0")
	// ErrTrafficFloor — subtracting would drop the limit below what has already
	// been used (or below zero). Used bytes cannot be un-spent this way; reset
	// the counters first if that is the intent.
	ErrTrafficFloor = errors.New("扣减后额度会低于已用流量")
	// ErrBucketFinished — a retired or time-expired份 will never spend again.
	// Growing its limit would inflate the users.* mirror without giving the user
	// anything they can actually use. Top up the live/queued份, or grant a new one.
	ErrBucketFinished = errors.New("已结束的套餐无法调整流量，请调整使用中或排队中的份")
)

// DeleteBucket removes one of a user's buckets — an admin pulling back a plan份
// or the traffic pool. Returns the removed bucket so the caller can report what
// went.
//
// The bucket's quota goes with it. A queued (not-yet-active)份 has never been
// spendable, so deleting it is a full claw-back of that purchase's traffic; an
// active份 takes its remaining unused quota with it. This is a revocation, not a
// refund: no points are returned and the order row (if any) stays as it was.
// Refunding is ListOrders → RefundOrder, which reverses exactly one order's
// entitlement and gives the points back.
//
// Removing the active head of a package queue frees the slot, so advanceUserQueues
// promotes the next queued份 in the same transaction — otherwise a user whose
// current份 was pulled would sit with a paid-but-invisible份 until the periodic
// ticker noticed.
func (s *Store) DeleteBucket(userID, bucketID int64) (*Bucket, error) {
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	b, err := scanBucket(tx.QueryRow(`SELECT `+bucketCols+bucketFrom+` WHERE p.id=? AND p.user_id=?`, bucketID, userID))
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrBucketNotFound
	}
	if b.Kind == KindFree {
		return nil, ErrBucketProtected
	}
	if _, err := tx.Exec(`DELETE FROM user_plans WHERE id=? AND user_id=?`, bucketID, userID); err != nil {
		return nil, err
	}
	if _, err := advanceUserQueues(tx, userID, now); err != nil {
		return nil, err
	}
	// advanceUserQueues only recomputes when it promoted something; the deletion
	// itself always changes the aggregate, so recompute unconditionally.
	if _, _, _, _, err := recomputeUserAggregate(tx, userID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return b, nil
}

// AdjustBucketTraffic adds (delta>0) or subtracts (delta<0) bytes from one
// bucket's traffic_limit. Returns the bucket after the change.
//
// This is the admin "流量调整" counterpart of AdjustPoints: a gift or a claw-back
// that does not touch the order row or the points ledger. The free bucket is
// protected (same reason as DeleteBucket). An uncapped plan (limit==0) has no
// finite number to grow or shrink; an empty pool does, and adding is how it
// gets a balance. The new limit is never allowed below what has already been
// used — spent traffic cannot be un-spent here.
//
// Shrinking an active head onto (or below) its used bytes exhausts it, so
// advanceUserQueues runs in the same transaction and promotes the next queued
// same-renewal-line份 — otherwise the user would sit over-quota with a paid份 waiting
// until the periodic ticker noticed.
func (s *Store) AdjustBucketTraffic(userID, bucketID, delta int64) (*Bucket, error) {
	if delta == 0 {
		return nil, ErrZeroDelta
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	b, err := scanBucket(tx.QueryRow(`SELECT `+bucketCols+bucketFrom+` WHERE p.id=? AND p.user_id=?`, bucketID, userID))
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrBucketNotFound
	}
	if b.Kind == KindFree {
		return nil, ErrBucketProtected
	}
	// Retired heads have already handed the slot to the next份. Time-expired
	// plans will not come back online just because the number got bigger.
	// A still-active exhausted head is different: adding quota is how it
	// returns to service when nothing is queued behind it.
	if b.Status == StatusRetired || (b.Kind == "plan" && !b.NotExpired(now)) {
		return nil, ErrBucketFinished
	}
	newLimit := b.TrafficLimit + delta
	if newLimit < b.Used() || newLimit < 0 {
		return nil, ErrTrafficFloor
	}
	if _, err := tx.Exec(`UPDATE user_plans SET traffic_limit=?, updated_at=? WHERE id=? AND user_id=?`,
		newLimit, now, bucketID, userID); err != nil {
		return nil, err
	}
	if _, err := advanceUserQueues(tx, userID, now); err != nil {
		return nil, err
	}
	if _, _, _, _, err := recomputeUserAggregate(tx, userID, now); err != nil {
		return nil, err
	}
	updated, err := scanBucket(tx.QueryRow(`SELECT `+bucketCols+bucketFrom+` WHERE p.id=? AND p.user_id=?`, bucketID, userID))
	if err != nil {
		return nil, err
	}
	if updated == nil {
		// Promotion of a different份 does not delete this one; a missing row here
		// means the UPDATE targeted nothing, which the earlier read already ruled out.
		return nil, ErrBucketNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return updated, nil
}

// BucketByClientName resolves a sing-box stats identity to its bucket.
//
// A whole subscription line shares one name, so this has to pick the same份
// resolveStatsIdentity would — the one in service — rather than whichever row the
// query happened to return first.
func (s *Store) BucketByClientName(name string) (*Bucket, error) {
	name, _ = canonicalStatsIdentity(name)
	return scanBucket(s.db.QueryRow(`SELECT `+bucketCols+bucketFrom+`
		WHERE COALESCE(i.client_name, p.client_name)=?
		ORDER BY `+usableExpr("p")+` DESC, p.id DESC
		LIMIT 1`, name))
}

// proxyUsernameRe restricts a custom mixed-proxy username to safe characters: it
// lands verbatim in the sing-box config and becomes a stats identity, so no
// whitespace/quotes/control chars. Length 2-64, must start alphanumeric.
var proxyUsernameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{1,63}$`)

// ValidateProxyUsername checks a user-chosen mixed-proxy username. The "qz_"
// prefix is reserved for system-generated client_names, so banning it guarantees
// a custom username can never collide with any bucket's client_name identity.
func ValidateProxyUsername(name string) error {
	if !proxyUsernameRe.MatchString(name) {
		return errors.New("用户名需 2-64 位，仅限字母/数字/ _.@- ，且以字母或数字开头")
	}
	if strings.HasPrefix(name, "qz_") {
		return errors.New("用户名不能以 qz_ 开头（系统保留前缀）")
	}
	return nil
}

// SetBucketProxyCred sets a bucket's mixed-proxy credential (a proxy-only
// account, unrelated to the login account). expiresAt 0 = permanent. Ownership is
// enforced via userID. The username must not collide with another bucket's
// proxy_username or any client_name.
func (s *Store) SetBucketProxyCred(bucketID, userID int64, username, password string, expiresAt int64) error {
	if err := ValidateProxyUsername(username); err != nil {
		return err
	}
	if len(password) < 6 || len(password) > 128 {
		return errors.New("密码需 6-128 位")
	}
	if expiresAt < 0 {
		return errors.New("有效期非法")
	}
	// A plan份's proxy account belongs to its subscription line, like the rest of
	// its credentials — written to the row it would be lost at the next handoff,
	// which is the whole class of bug the line exists to prevent. pool/free and the
	// package-less grants have no line and keep it on the row.
	var pkgID int64
	var kind string
	switch err := s.db.QueryRow(`SELECT package_id, kind FROM user_plans WHERE id=? AND user_id=?`,
		bucketID, userID).Scan(&pkgID, &kind); {
	case errors.Is(err, sql.ErrNoRows):
		return errors.New("套餐不存在或无权修改")
	case err != nil:
		return err
	}
	onLine := kind == "plan" && pkgID > 0
	// Reject a username already taken anywhere in the proxy identity namespace —
	// another bucket, another line, another user's account credential, or any
	// client_name. The row/line being written is excluded, so re-saving the same
	// username is not a collision with itself.
	lineUser, linePkg := int64(-1), int64(-1)
	if onLine {
		lineUser, linePkg = userID, pkgID
	}
	taken, err := s.proxyNameTaken(username, proxyNameOwner{bucketID: bucketID, lineUser: lineUser, linePkg: linePkg})
	if err != nil {
		return err
	}
	if taken {
		return errors.New("该用户名已被占用，请换一个")
	}
	now := time.Now().Unix()
	if onLine {
		res, err := s.db.Exec(`UPDATE plan_identities SET proxy_username=?, proxy_password=?,
			proxy_expires_at=?, updated_at=? WHERE user_id=? AND package_id=?`,
			username, password, expiresAt, now, userID, pkgID)
		if err != nil {
			return errors.New("保存失败，用户名可能已被占用")
		}
		// Never report success on a write that matched nothing: the user would be
		// told their proxy login was saved and then fail to connect with it.
		if aff, _ := res.RowsAffected(); aff == 0 {
			return errors.New("套餐不存在或无权修改")
		}
		return nil
	}
	res, err := s.db.Exec(`UPDATE user_plans SET proxy_username=?, proxy_password=?, proxy_expires_at=?, updated_at=? WHERE id=? AND user_id=?`,
		username, password, expiresAt, now, bucketID, userID)
	if err != nil {
		return errors.New("保存失败，用户名可能已被占用") // unique-index guard against a concurrent duplicate
	}
	if aff, _ := res.RowsAffected(); aff == 0 {
		return errors.New("套餐不存在或无权修改")
	}
	return nil
}

// PoolBucket returns the user's traffic-package pool bucket (or nil).
func (s *Store) PoolBucket(userID int64) (*Bucket, error) {
	return scanBucket(s.db.QueryRow(`SELECT `+bucketCols+bucketFrom+` WHERE p.user_id=? AND p.kind='pool' ORDER BY p.id LIMIT 1`, userID))
}

// genBucketCreds mints a fresh sing-box identity (mirrors idgen.NewCredentials).
func genBucketCreds() (uuidStr, secret string) {
	id, _ := uuid.NewRandom()
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return id.String(), hex.EncodeToString(b)
}

// insertBucket writes a bucket row via the given execer and returns its id.
func insertBucket(ex execer, b *Bucket) (int64, error) {
	now := time.Now().Unix()
	if b.CreatedAt == 0 {
		b.CreatedAt = now
	}
	status := b.Status
	if status == "" {
		status = "active" // pool/free/grant buckets are active on creation
	}
	res, err := ex.Exec(`INSERT INTO user_plans
		(user_id, kind, package_id, queue_key, name, client_name,
		 traffic_limit, used_up, used_down, expiry_at, last_online_at, order_id, status, duration_days, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.UserID, b.Kind, b.PackageID, b.QueueKey, b.Name, b.ClientName,
		b.TrafficLimit, b.UsedUp, b.UsedDown, b.ExpiryAt, b.LastOnlineAt, b.OrderID, status, b.DurationDays, b.CreatedAt, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AddBucketUsage applies a sing-box stats delta to the matching bucket, and
// mirrors it onto the owning user (aggregate counters + last_online + the
// per-user time-series) so the dashboard totals/charts and online detection
// keep working. Called once per stats poll per active identity.
func (s *Store) AddBucketUsage(statName string, up, down int64) error {
	if statName == "" || (up == 0 && down == 0) {
		return nil
	}
	statName, _ = canonicalStatsIdentity(statName)
	now := time.Now().Unix()
	// One transaction: the bucket counter, the mirrored user aggregate, and the
	// time-series sample must all land together (they may otherwise run on
	// different pooled connections and diverge if one fails).
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Resolve the stats identity to its bucket. A bucket has up to two identities:
	// client_name (used by every protocol) and, for mixed inbounds, an optional
	// custom proxy_username. Both meter the same bucket. proxy_username can never
	// equal a client_name (client_names are qz_-prefixed, proxy_usernames may not
	// be) and is globally unique, so at most one row matches.
	bucketID, userID, packageID, kind, err := s.resolveStatsIdentity(tx, statName, nil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // unknown identity (e.g. a just-removed bucket) — ignore, rolls back
	}
	if err != nil {
		return err
	}
	if err = applyBucketUsage(tx, bucketID, userID, packageID, kind, up, down, now); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// UsageDelta is one identity's per-poll traffic delta, keyed by its stats name in
// AddUsageBatch.
type UsageDelta struct {
	Up   int64
	Down int64
}

// AddUsageBatch applies a whole stats poll's per-identity deltas in ONE
// transaction. Previously each identity opened its own write transaction, so a
// 100-user poll grabbed the single SQLite/WAL write lock ~100-200 times a minute,
// contending with live subscription/purchase writes; this collapses that to one
// lock acquisition. Each identity's three writes (bucket + mirrored user aggregate
// + time-series sample) are wrapped in a SAVEPOINT so one failure rolls back just
// that identity — the poll used reset=true, so the rest of the deltas must still
// land rather than being discarded with it. Returns how many identities applied,
// and (if any failed) an error naming the first.
func (s *Store) AddUsageBatch(deltas map[string]UsageDelta) (int, error) {
	return s.addUsageBatches(map[int64]map[string]UsageDelta{-1: deltas}, false)
}

// AddUsageBatchesByServer meters the same deltas as AddUsageBatch while also
// retaining their collection source (0 = panel machine, >0 = remote server).
// Billing writes and source attribution share a transaction/savepoint, so a
// source chart can never claim bytes that failed to reach the user's quota.
func (s *Store) AddUsageBatchesByServer(sources map[int64]map[string]UsageDelta) (int, error) {
	return s.addUsageBatches(sources, true)
}

func canonicalUsageDeltas(deltas map[string]UsageDelta) map[string]UsageDelta {
	canonical := make(map[string]UsageDelta, len(deltas))
	for name, d := range deltas {
		name, _ = canonicalStatsIdentity(name)
		cur := canonical[name]
		cur.Up += d.Up
		cur.Down += d.Down
		canonical[name] = cur
	}
	return canonical
}

func (s *Store) addUsageBatches(sources map[int64]map[string]UsageDelta, recordServer bool) (int, error) {
	if len(sources) == 0 {
		return 0, nil
	}
	// Several logical routes can share one physical inbound. Their auth_user
	// names carry a reversible node suffix; merge them back into the underlying
	// bucket/account identity within each collection source. Keep sources apart:
	// their sum is billable, but their separation is the analysis feature.
	canonicalSources := make(map[int64]map[string]UsageDelta, len(sources))
	all := map[string]UsageDelta{}
	for serverID, deltas := range sources {
		canonical := canonicalUsageDeltas(deltas)
		canonicalSources[serverID] = canonical
		for name, d := range canonical {
			cur := all[name]
			cur.Up += d.Up
			cur.Down += d.Down
			all[name] = cur
		}
	}
	if len(all) == 0 {
		return 0, nil
	}
	// Before the write lock, not under it: account-level identities need the
	// owner's whole bucket order to know which份 they spend.
	acctTargets, acctErr := s.accountTargets(all)
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	applied, failed := 0, 0
	// A pre-pass failure is reported, not fatal: those identities fall back to
	// resolving inside the loop (acctTargets nil) or are simply skipped, and the
	// rest of the poll must still land.
	firstErr := acctErr
	for serverID, deltas := range canonicalSources {
		for name, d := range deltas {
			if name == "" || (d.Up == 0 && d.Down == 0) {
				continue
			}
			bucketID, userID, packageID, kind, rerr := s.resolveStatsIdentity(tx, name, acctTargets)
			if errors.Is(rerr, sql.ErrNoRows) {
				continue // unknown / just-removed identity — skip, not a failure
			}
			if rerr != nil {
				if firstErr == nil {
					firstErr = rerr
				}
				failed++
				continue
			}
			if _, err := tx.Exec(`SAVEPOINT usg`); err != nil {
				return applied, err // savepoint itself failing means the tx is unusable
			}
			werr := applyBucketUsage(tx, bucketID, userID, packageID, kind, d.Up, d.Down, now)
			if werr == nil && recordServer {
				_, werr = tx.Exec(`INSERT INTO server_user_traffic_samples (server_id,user_id,ts,up,down)
					VALUES (?,?,?,?,?)`, serverID, userID, now, d.Up, d.Down)
			}
			if werr != nil {
				_, _ = tx.Exec(`ROLLBACK TO usg`)
				_, _ = tx.Exec(`RELEASE usg`)
				if firstErr == nil {
					firstErr = werr
				}
				failed++
				continue
			}
			_, _ = tx.Exec(`RELEASE usg`)
			applied++
		}
	}
	if err := tx.Commit(); err != nil {
		return applied, err
	}
	committed = true
	if firstErr != nil {
		return applied, fmt.Errorf("stats: %d identity updates failed, first: %w", failed, firstErr)
	}
	return applied, nil
}

// resolveStatsIdentity maps a sing-box stats name to the bucket it meters.
// Three kinds of name arrive here: a bucket's client_name (every protocol), its
// optional custom proxy_username (mixed inbounds), and the user's account-level
// proxy username (mixed inbounds, one login across all their nodes). The first
// two name their own bucket; the third names a user, and the bucket it spends is
// decided by ownership priority (see accountMeterBucket).
//
// All three share one uniqueness namespace (proxyNameTaken), so at most one
// matches and the order below is a matter of cost, not correctness.
//
// packageID rides along for the traffic_daily rollup: resolving it here costs
// nothing (the row is already being read) and saves a second lookup inside the
// hot per-identity write path.
//
// acctTargets, when non-nil, is this poll's pre-resolved account-level
// identities (see accountTargets) — non-nil means "the account question has
// already been asked and answered", so a name missing from it is simply not an
// account identity and no further lookup is due.
func (s *Store) resolveStatsIdentity(tx txLike, statName string, acctTargets map[string]*Bucket) (bucketID, userID, packageID int64, kind string, err error) {
	// A subscription line's name is shared by every份 that has passed through it,
	// so it resolves to the line first and then to the份 that is actually in
	// service — that is what makes the bytes land on the right month. Preference
	// order within the line: the usable份, else the most recent one, so traffic
	// arriving in the moment a份 runs out is still attributed rather than dropped.
	err = tx.QueryRow(`SELECT p.id, p.user_id, p.package_id, p.kind
		FROM plan_identities i
		JOIN user_plans p ON p.user_id=i.user_id AND p.package_id=i.package_id
		                 AND p.kind='plan' AND p.status<>'queued'
		WHERE i.client_name=? OR (i.proxy_username<>'' AND i.proxy_username=?)
		ORDER BY `+usableExpr("p")+` DESC, p.id DESC
		LIMIT 1`, statName, statName).Scan(&bucketID, &userID, &packageID, &kind)
	if err == nil {
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return
	}
	// Not a plan line: the pool, the free bucket and the package-less grants keep
	// their credentials on the row itself.
	err = tx.QueryRow(`SELECT id, user_id, package_id, kind FROM user_plans
		WHERE client_name=? OR (proxy_username<>'' AND proxy_username=?)`,
		statName, statName).Scan(&bucketID, &userID, &packageID, &kind)
	if !errors.Is(err, sql.ErrNoRows) {
		return
	}
	// Last: the account-level HTTP/SOCKS5 credential, which belongs to a user
	// rather than to a bucket and so has to be told which bucket it spends.
	// Reached only after the bucket identities miss, which is safe because all
	// three live in one uniqueness namespace (proxyNameTaken) — a name can never
	// be both.
	var b *Bucket
	if acctTargets != nil {
		b = acctTargets[statName]
		if b == nil {
			return 0, 0, 0, "", sql.ErrNoRows
		}
	} else {
		if userID, err = accountUserID(tx, statName); err != nil {
			return
		}
		if b, err = s.accountMeterBucket(userID); err != nil {
			return
		}
	}
	return b.ID, b.UserID, b.PackageID, b.Kind, nil
}

// applyBucketUsage writes one identity's delta: the bucket counter, the mirrored
// user aggregate + last_online, the per-user time-series sample, and the daily
// per-bucket rollup. Caller owns the transaction (or savepoint) so these land
// together or not at all.
// The users.used_* mirror deliberately skips FREE buckets, matching
// recomputeUserAggregate — those two are the only writers of that counter and
// they have to agree. They did not: the recompute excluded free usage while this
// per-poll path added it, so between entitlement events unmetered free-group
// traffic quietly piled onto the user's paid counter, until the quota check
// tripped and the subscription started answering with an empty node list.
// last_online_at, the trend samples and the daily rollup still record free
// traffic — it really happened, it just is not billable.
func applyBucketUsage(tx txLike, bucketID, userID, packageID int64, kind string, up, down, now int64) error {
	if _, err := tx.Exec(`UPDATE user_plans SET used_up=used_up+?, used_down=used_down+?, last_online_at=?, updated_at=? WHERE id=?`,
		up, down, now, now, bucketID); err != nil {
		return err
	}
	mUp, mDown := up, down
	if kind == KindFree {
		mUp, mDown = 0, 0
	}
	if _, err := tx.Exec(`UPDATE users SET used_up=used_up+?, used_down=used_down+?, last_online_at=?, updated_at=? WHERE id=?`,
		mUp, mDown, now, now, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO traffic_samples (user_id, ts, up, down) VALUES (?, ?, ?, ?)`,
		userID, now, up, down); err != nil {
		return err
	}
	// The day is derived in SQL from the same `now` the rest of the row uses,
	// with the same 'localtime' modifier the daily read queries apply — deriving
	// it in Go instead would put the boundary in a second place to keep in sync,
	// and the two would disagree for anyone whose process TZ differs from
	// SQLite's.
	if _, err := tx.Exec(`
		INSERT INTO traffic_daily (day, user_id, bucket_id, package_id, up, down)
		VALUES (strftime('%Y-%m-%d', ?, 'unixepoch', 'localtime'), ?, ?, ?, ?, ?)
		ON CONFLICT(day, user_id, bucket_id) DO UPDATE SET
		  up = up + excluded.up,
		  down = down + excluded.down,
		  package_id = excluded.package_id`,
		now, userID, bucketID, packageID, up, down); err != nil {
		return err
	}
	return nil
}

// backfillPlanIdentities lifts each existing subscription line's credentials out
// of its buckets and into plan_identities.
//
// Which份 to take them from matters more than anything else here: it has to be
// the one whose uuid/password every client is CURRENTLY holding, or the upgrade
// itself would disconnect everyone.
//
// The two halves need OPPOSITE orderings, which is why this switches on the flag
// rather than applying one rule throughout:
//
//   - among USABLE份, whichever orderBuckets/pickOwner would hand a node to —
//     soonest expiry (0 = never sorts last), then lowest id. A normal chain has
//     exactly one usable份 so any ordering agrees, but backfillRetiredBuckets
//     deliberately leaves two usable份 alone when it finds them, and there a
//     different order would lift credentials nobody is using.
//   - when NONE is usable the account is already being served nothing, so the
//     credentials worth keeping are the ones it last authenticated with: the most
//     recently activated份, i.e. the highest id. Buying the package again reuses
//     the line, so getting this right is the difference between the user's client
//     resuming immediately and staying broken until its next refresh.
//
// Queued份 are never a source: their credentials were minted but never rendered
// into a config or a link, so nobody is holding them.
//
// Idempotent: a line that already has a row is left untouched, so this can run on
// every boot and re-running it can never rotate a live credential.
func (s *Store) backfillPlanIdentities() error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT OR IGNORE INTO plan_identities
		(user_id, package_id, `+planIdentityCols+`, created_at, updated_at)
		SELECT p.user_id, p.package_id, p.client_name, p.client_uuid, p.client_secret,
		       p.proxy_username, p.proxy_password, p.proxy_expires_at, ?, ?
		FROM user_plans p
		WHERE p.kind='plan' AND p.package_id>0 AND p.status<>'queued'
		  AND p.id = (SELECT x.id FROM user_plans x
		      WHERE x.user_id=p.user_id AND x.package_id=p.package_id
		        AND x.kind='plan' AND x.status<>'queued'
		      ORDER BY `+usableExpr("x")+` DESC,
		               CASE WHEN `+usableExpr("x")+`
		                    THEN (CASE WHEN x.expiry_at=0 THEN 9223372036854775807 ELSE x.expiry_at END)
		                    ELSE 0 END ASC,
		               CASE WHEN `+usableExpr("x")+` THEN x.id ELSE -x.id END ASC
		      LIMIT 1)`, now, now)
	return err
}

// backfillRetiredBuckets marks the已用完份 of a queue chain that predate the
// 'retired' status, so existing accounts get the same protection new ones do.
//
// A queue-era份 is retired iff a NEWER same-package份 has already taken over
// (promotion is in id order, so "newer" means a higher id). Those are exactly the
// rows mergeDuplicatePlanBuckets would otherwise see as duplicates and flatten.
//
// Runs before the merge and is idempotent: once marked, they no longer match.
// Legacy (duration_days=0) rows are left alone — the merge is still the right
// answer for those.
//
// The份 must ALSO be finished (out of time or out of quota) to be marked. Having
// a newer sibling is how the chain is recognised, but it is not on its own proof
// that this份 is spent, and a migration that shelved a份 the user could still
// spend would be quietly taking entitlement away — the one thing a backfill must
// never do. In a healthy chain the two conditions coincide; where they do not,
// this errs toward leaving the份 alone.
func (s *Store) backfillRetiredBuckets() error {
	_, err := s.db.Exec(`UPDATE user_plans SET status=? WHERE id IN (
		SELECT b.id FROM user_plans b
		WHERE b.kind='plan' AND b.package_id>0 AND b.status='active' AND b.duration_days>0
		  AND NOT `+usableExpr("b")+`
		  AND EXISTS (SELECT 1 FROM user_plans n
		      WHERE n.user_id=b.user_id AND n.package_id=b.package_id AND n.kind='plan'
		        AND n.status='active' AND n.duration_days>0 AND n.id>b.id))`, StatusRetired)
	return err
}

// mergeDuplicatePlanBuckets collapses pre-existing duplicate plan buckets (same
// user + package) into one, summing traffic quota and usage and taking the
// latest expiry. This repairs accounts that repurchased a plan before renewal
// stacking existed (which minted a new bucket each time). The survivor is the
// oldest bucket, so it keeps a stable identity; the rest are deleted and their
// sing-box identities drop out on the next rebuild. Idempotent: once merged,
// each (user, package) has a single row and the query matches nothing. The
// users.* aggregate is unchanged because the survivor holds the summed totals.
func (s *Store) mergeDuplicatePlanBuckets() error {
	// Only collapse legacy 'active' duplicates. Never touch 'queued' buckets —
	// merging them would re-create the very stacking the queue model removes.
	// duration_days is the discriminator: a queue-era bucket always carries the
	// package's duration (plan packages must have a positive one), while the rows
	// this repair exists for predate the column and got 0 from its ALTER TABLE
	// default. Without this the pass stops being a one-time legacy repair and
	// starts eating live queues — a user holding six monthly份 has several
	// same-package buckets by design, and merging them destroys the per-month
	// accounting and deletes whichever份 is currently in service.
	rows, err := s.db.Query(`SELECT user_id, package_id, MIN(id),
		SUM(traffic_limit), SUM(used_up), SUM(used_down), MAX(expiry_at)
		FROM user_plans WHERE kind='plan' AND package_id>0 AND status='active' AND duration_days=0
		GROUP BY user_id, package_id HAVING COUNT(*)>1`)
	if err != nil {
		return err
	}
	type dup struct {
		userID, packageID, keepID, limit, up, down, expiry int64
	}
	var dups []dup
	for rows.Next() {
		var d dup
		if err := rows.Scan(&d.userID, &d.packageID, &d.keepID, &d.limit, &d.up, &d.down, &d.expiry); err != nil {
			rows.Close()
			return err
		}
		dups = append(dups, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, d := range dups {
		if _, err := s.db.Exec(`UPDATE user_plans SET traffic_limit=?, used_up=?, used_down=?, expiry_at=?, updated_at=? WHERE id=?`,
			d.limit, d.up, d.down, d.expiry, now, d.keepID); err != nil {
			return err
		}
		// Same scoping as the SELECT above — the delete must not reach past the
		// legacy rows the merge was computed from.
		if _, err := s.db.Exec(`DELETE FROM user_plans WHERE kind='plan' AND user_id=? AND package_id=? AND id<>?
			AND status='active' AND duration_days=0`,
			d.userID, d.packageID, d.keepID); err != nil {
			return err
		}
	}
	return nil
}

// backfillUserPlans seeds the bucket model from the legacy single-plan columns
// on first run (idempotent: skipped once any bucket exists). Existing clients
// keep working because a plan bucket reuses the user's current identity; the
// pool starts empty. Protocol credentials now live on users; the legacy bucket
// credential columns are deliberately left empty on new writes.
func (s *Store) backfillUserPlans() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_plans`).Scan(&n); err != nil || n > 0 {
		return err
	}
	rows, err := s.db.Query(`SELECT id, username, client_name, client_uuid, client_secret,
		current_plan_id, traffic_limit, used_up, used_down, expiry_at FROM users`)
	if err != nil {
		return err
	}
	type urec struct {
		id                      int64
		username                string
		name, cuuid, csecret    sql.NullString
		planID                  sql.NullInt64
		limit, up, down, expiry int64
	}
	var us []urec
	for rows.Next() {
		var u urec
		if err := rows.Scan(&u.id, &u.username, &u.name, &u.cuuid, &u.csecret,
			&u.planID, &u.limit, &u.up, &u.down, &u.expiry); err != nil {
			rows.Close()
			return err
		}
		us = append(us, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, u := range us {
		primaryName := u.name.String
		if primaryName == "" {
			primaryName = fmt.Sprintf("qz_%s", u.username)
		}
		if u.planID.Valid && u.planID.Int64 > 0 {
			// The migrated plan keeps its stats name; the pool is new and empty.
			name := "套餐"
			if p, _ := s.GetPackage(u.planID.Int64); p != nil {
				name = p.Name
			}
			if _, err := insertBucket(s.db, &Bucket{
				UserID: u.id, Kind: "plan", PackageID: u.planID.Int64, Name: name,
				ClientName:   primaryName,
				TrafficLimit: u.limit, UsedUp: u.up, UsedDown: u.down, ExpiryAt: u.expiry,
			}); err != nil {
				return err
			}
			if _, err := insertBucket(s.db, &Bucket{
				UserID: u.id, Kind: "pool", Name: "通用流量",
				ClientName: primaryName + "_pool",
			}); err != nil {
				return err
			}
		} else {
			// No plan: the pool carries the legacy balance and stats name.
			if _, err := insertBucket(s.db, &Bucket{
				UserID: u.id, Kind: "pool", Name: "通用流量",
				ClientName:   primaryName,
				TrafficLimit: u.limit, UsedUp: u.up, UsedDown: u.down,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
