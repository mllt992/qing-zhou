package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

var (
	ErrPackageDisabled   = errors.New("商品已下架")
	ErrPackageNotAllowed = errors.New("该商品仅限指定用户组购买")
	ErrOutOfStock        = errors.New("商品库存不足")
	ErrPackageNoTraffic  = errors.New("商品流量必须大于 0")
	ErrUnknownPkgType    = errors.New("未知商品类型")
	ErrOrderNotFound     = errors.New("订单不存在")
	ErrAlreadyRefunded   = errors.New("该订单已退款")
)

type Order struct {
	ID              int64   `json:"id"`
	UserID          int64   `json:"user_id"`
	Username        string  `json:"username,omitempty"` // filled by admin queries only
	PackageID       int64   `json:"package_id"`
	Name            string  `json:"name"` // from snapshot (survives package deletion)
	Type            string  `json:"type"`
	PricePoints     int64   `json:"price_points"`
	Status          string  `json:"status"`
	CreatedAt       int64   `json:"created_at"`
	RefundedPoints  int64   `json:"refunded_points"`
	RefundedAt      int64   `json:"refunded_at"`
	RefundRatio     float64 `json:"refund_ratio"`
	RefundedTraffic int64   `json:"refunded_traffic"`
}

type PurchaseResult struct {
	Order *Order
	User  *User
}

// Purchase runs the full buy transaction atomically: validate funds/stock,
// deduct points, write the ledger row and order, apply the entitlement change,
// then (for node-affecting packages) call sync to push to sing-box INSIDE the
// transaction. If sync fails, the whole transaction rolls back — no points are
// lost and the user's quota is unchanged.
//
// sync receives the updated user snapshot and whether traffic counters should
// be reset (expired-plan renewal).
//
// Buys the package's default duration; PurchaseDuration takes the buyer's choice.
func (s *Store) Purchase(userID int64, pkg *Package, idemKey string, sync func(updated *User, resetUsed bool) error) (*PurchaseResult, error) {
	return s.PurchaseDuration(userID, pkg, 0, idemKey, sync)
}

// PurchaseDuration is Purchase for a package that sells several durations:
// days picks one of pkg.Options (0 = the default one). The choice is re-resolved
// against the fresh in-tx package, so the price charged and the quota granted are
// the ones currently published for that duration — never the ones the client saw.
func (s *Store) PurchaseDuration(userID int64, pkg *Package, days int64, idemKey string, sync func(updated *User, resetUsed bool) error) (*PurchaseResult, error) {
	if !pkg.Enabled {
		return nil, ErrPackageDisabled
	}
	if pkg.Stock == 0 {
		return nil, ErrOutOfStock
	}

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

	u, err := scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, userID))
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, ErrUserNotFound
	}

	// Idempotency: if this purchase intent (client-supplied key) already produced an
	// order, return that order without charging again. Guards against a network
	// retry where the first request committed but its response was lost — the classic
	// double-charge. The BEGIN IMMEDIATE lock serializes a concurrent duplicate; the
	// unique (user_id, idempotency_key) index is the backstop if two race past here.
	if idemKey != "" {
		var oid int64
		qerr := tx.QueryRow(`SELECT id FROM orders WHERE user_id=? AND idempotency_key=?`, userID, idemKey).Scan(&oid)
		if qerr == nil {
			_ = tx.Rollback() // no writes yet — release the lock
			o, _ := s.GetOrder(oid)
			return &PurchaseResult{Order: o, User: u}, nil
		}
		if !errors.Is(qerr, sql.ErrNoRows) {
			return nil, qerr
		}
	}

	// Re-read the package INSIDE the transaction. The pkg the handler passed was
	// loaded before the tx and may be stale — disabled, repriced, or sold out by
	// a concurrent buyer. Authoritative price/stock/traffic come from here; the
	// BEGIN IMMEDIATE write lock keeps a competing buyer from racing the stock.
	fresh, err := scanPackage(tx.QueryRow(`SELECT `+pkgCols+` FROM packages WHERE id=?`, pkg.ID))
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, ErrPackageDisabled
	}
	fresh.GroupIDs = pkg.GroupIDs // not a column; preserve caller-supplied bindings
	// Apply the buyer's duration choice to the fresh row: from here on pkg carries
	// the price, traffic and duration of the selected option, so the funds check,
	// the bucket, the snapshot and the ledger all agree on one combination.
	if pkg, err = fresh.forDuration(days); err != nil {
		return nil, err
	}
	if pkg.TrafficBytes <= 0 {
		return nil, ErrPackageNoTraffic
	}
	if !pkg.Enabled {
		return nil, ErrPackageDisabled
	}
	if pkg.Stock == 0 {
		return nil, ErrOutOfStock
	}
	// Group gate, checked in-tx alongside enabled/stock for the same reason: the
	// shop listing hides restricted packages, but a client can POST any id, and
	// membership may be revoked between the listing and this call.
	allowed, err := canBuyPackageTx(tx, userID, pkg.ID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrPackageNotAllowed
	}
	if u.Points < pkg.PricePoints {
		return nil, ErrInsufficientFunds
	}

	now := time.Now().Unix()
	setPlan := pkg.Type == "plan"
	switch pkg.Type {
	case "traffic", "plan":
		// Entitlement is applied by the bucket ops below (a plan renews/stacks its
		// own metered bucket; traffic tops up the pool). The legacy users.* columns
		// are recomputed from the buckets afterward so the aggregate never drifts.
	default:
		return nil, ErrUnknownPkgType
	}

	// Deduct points.
	newPoints := u.Points - pkg.PricePoints
	if _, err = tx.Exec(`UPDATE users SET points=?, updated_at=? WHERE id=?`, newPoints, now, userID); err != nil {
		return nil, err
	}

	// Order (with package snapshot).
	snap, _ := json.Marshal(pkg)
	res, err := tx.Exec(`INSERT INTO orders (user_id, package_id, package_snapshot, price_points, status, idempotency_key, created_at)
		VALUES (?,?,?,?, 'success', ?, ?)`, userID, pkg.ID, string(snap), pkg.PricePoints, idemKey, now)
	if err != nil {
		return nil, err
	}
	orderID, _ := res.LastInsertId()

	// Bucket model (per-plan independent metering): a plan purchase mints a fresh
	// independently-metered bucket with its own identity; a traffic purchase tops
	// up the shared pool. The legacy users.* fields above are kept as a rough
	// aggregate for back-compat; buckets are authoritative for enforcement.
	if pkg.Type == "plan" {
		if err = enqueuePlanBucket(tx, userID, u.Username, pkg, orderID, now); err != nil {
			return nil, err
		}
		// Promote immediately when there's no active head yet (first purchase of this
		// package); a repeat purchase stays queued behind the current head.
		if _, err = advanceUserQueues(tx, userID, now); err != nil {
			return nil, err
		}
	} else if pkg.Type == "traffic" {
		if err = addToPool(tx, userID, u.Username, pkg.TrafficBytes, now); err != nil {
			return nil, err
		}
	}

	// Ledger.
	if _, err = tx.Exec(`INSERT INTO point_transactions
		(user_id, amount, type, balance_after, ref_id, note, operator_id, created_at)
		VALUES (?,?, 'purchase', ?, ?, ?, 0, ?)`,
		userID, -pkg.PricePoints, newPoints, orderID, "购买: "+pkg.Name, now); err != nil {
		return nil, err
	}

	// Recompute the legacy users.* aggregate from the authoritative buckets, so a
	// renewal (stacked bucket) or a second independent plan is reflected exactly,
	// with no drift from ad-hoc column math.
	newTraffic, newUp, newDown, newExpiry, err := recomputeUserAggregate(tx, userID, now)
	if err != nil {
		return nil, err
	}
	if setPlan {
		if _, err = tx.Exec(`UPDATE users SET current_plan_id=? WHERE id=?`, pkg.ID, userID); err != nil {
			return nil, err
		}
	}

	// Decrement stock if limited. Verify a row was actually affected — a 0-row
	// result means the item sold out between our in-tx read and here; abort so we
	// never oversell or charge for an unavailable item.
	if pkg.Stock > 0 {
		res, err := tx.Exec(`UPDATE packages SET stock=stock-1 WHERE id=? AND stock>0`, pkg.ID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, ErrOutOfStock
		}
	}

	// Build the updated snapshot for the sync callback.
	updated := *u
	updated.Points = newPoints
	updated.TrafficLimit = newTraffic
	updated.UsedUp = newUp
	updated.UsedDown = newDown
	updated.ExpiryAt = newExpiry
	if setPlan {
		updated.CurrentPlanID = sql.NullInt64{Int64: pkg.ID, Valid: true}
	}

	// External sync inside the tx.
	if sync != nil {
		if err = sync(&updated, false); err != nil {
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	committed = true

	return &PurchaseResult{
		Order: &Order{ID: orderID, UserID: userID, PackageID: pkg.ID, PricePoints: pkg.PricePoints, Status: "success", CreatedAt: now},
		User:  &updated,
	}, nil
}

// AssignPackage grants a package to a user WITHOUT charging points — an admin
// comp/manual activation. It applies the same entitlement change as Purchase
// (traffic + expiry, and current plan for "plan" packages), records a 0-price
// order for audit, and runs sync inside the transaction so a sync failure rolls
// the whole thing back. Package enabled/stock are ignored (admin override).
//
// Grants the package's default duration; AssignPackageDuration takes a choice.
func (s *Store) AssignPackage(userID int64, pkg *Package, operatorID int64, sync func(updated *User, resetUsed bool) error) (*PurchaseResult, error) {
	return s.AssignPackageDuration(userID, pkg, 0, operatorID, sync)
}

// AssignPackageDuration is AssignPackage with an explicit length. days==0 is
// the package default. A listed option is granted as published (its own
// traffic). Any other positive length is a custom grant: default-option
// traffic, caller-chosen days. Traffic packages ignore days (pool top-up
// has no expiry). The shop still rejects unpublished lengths.
func (s *Store) AssignPackageDuration(userID int64, pkg *Package, days, operatorID int64, sync func(updated *User, resetUsed bool) error) (*PurchaseResult, error) {
	pkg, err := pkg.forAdminDuration(days)
	if err != nil {
		return nil, err
	}
	if pkg.TrafficBytes <= 0 {
		return nil, ErrPackageNoTraffic
	}
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

	u, err := scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, userID))
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, ErrUserNotFound
	}

	now := time.Now().Unix()
	setPlan := pkg.Type == "plan"
	switch pkg.Type {
	case "traffic", "plan":
		// Same model as Purchase: entitlement flows through the bucket ops; the
		// users.* aggregate is recomputed from buckets afterward.
	default:
		return nil, ErrUnknownPkgType
	}

	// Order (with package snapshot), price 0 = admin grant.
	snap, _ := json.Marshal(pkg)
	res, err := tx.Exec(`INSERT INTO orders (user_id, package_id, package_snapshot, price_points, status, created_at)
		VALUES (?,?,?,0, 'success', ?)`, userID, pkg.ID, string(snap), now)
	if err != nil {
		return nil, err
	}
	orderID, _ := res.LastInsertId()

	// Bucket model (same as Purchase): plan → renew/create metered bucket, traffic → pool.
	if pkg.Type == "plan" {
		if err = enqueuePlanBucket(tx, userID, u.Username, pkg, orderID, now); err != nil {
			return nil, err
		}
		// Promote immediately when there's no active head yet (first purchase of this
		// package); a repeat purchase stays queued behind the current head.
		if _, err = advanceUserQueues(tx, userID, now); err != nil {
			return nil, err
		}
	} else if pkg.Type == "traffic" {
		if err = addToPool(tx, userID, u.Username, pkg.TrafficBytes, now); err != nil {
			return nil, err
		}
	}

	// Ledger note (0 points) for audit trail of the manual activation.
	if _, err = tx.Exec(`INSERT INTO point_transactions
		(user_id, amount, type, balance_after, ref_id, note, operator_id, created_at)
		VALUES (?,0, 'admin_grant', ?, ?, ?, ?, ?)`,
		userID, u.Points, orderID, "管理员开通: "+pkg.Name, operatorID, now); err != nil {
		return nil, err
	}

	newTraffic, newUp, newDown, newExpiry, err := recomputeUserAggregate(tx, userID, now)
	if err != nil {
		return nil, err
	}
	if setPlan {
		if _, err = tx.Exec(`UPDATE users SET current_plan_id=? WHERE id=?`, pkg.ID, userID); err != nil {
			return nil, err
		}
	}

	updated := *u
	updated.TrafficLimit = newTraffic
	updated.UsedUp = newUp
	updated.UsedDown = newDown
	updated.ExpiryAt = newExpiry
	if setPlan {
		updated.CurrentPlanID = sql.NullInt64{Int64: pkg.ID, Valid: true}
	}

	if sync != nil {
		if err = sync(&updated, false); err != nil {
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	committed = true

	return &PurchaseResult{
		Order: &Order{ID: orderID, UserID: userID, PackageID: pkg.ID, PricePoints: 0, Status: "success", CreatedAt: now},
		User:  &updated,
	}, nil
}

// GetOrder loads a single order with its snapshot fields decoded.
func (s *Store) GetOrder(id int64) (*Order, error) {
	var o Order
	var snap string
	err := s.db.QueryRow(`SELECT id, user_id, package_id, package_snapshot, price_points, status, created_at,
		refunded_points, refunded_at, refund_ratio, refunded_traffic
		FROM orders WHERE id=?`, id).Scan(&o.ID, &o.UserID, &o.PackageID, &snap, &o.PricePoints, &o.Status, &o.CreatedAt,
		&o.RefundedPoints, &o.RefundedAt, &o.RefundRatio, &o.RefundedTraffic)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // genuinely not found
	}
	if err != nil {
		// A real DB fault must not masquerade as "order not found" — callers turn a
		// nil order into a 404, which would hide the error and (for refund) look like
		// the order vanished.
		return nil, err
	}
	var sp struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(snap), &sp) == nil {
		o.Name, o.Type = sp.Name, sp.Type
	}
	return &o, nil
}

// UserExists reports whether a user id still exists (used to detect orphaned
// orders left behind by a deleted account).
func (s *Store) UserExists(id int64) bool {
	var n int
	_ = s.db.QueryRow(`SELECT 1 FROM users WHERE id=?`, id).Scan(&n)
	return n == 1
}

// DeleteOrder permanently removes a single order record. Intended for cleaning
// up orphaned orders whose user has been deleted; the entitlement was already
// consumed, so this only drops the historical row.
func (s *Store) DeleteOrder(id int64) error {
	_, err := s.db.Exec(`DELETE FROM orders WHERE id=?`, id)
	return err
}

// RefundOrder reverses a successful purchase: refunds the points spent (prorated
// to the unused portion, or in full — see mode), undoes the entitlement this
// order granted (traffic + plan duration, clamped so they never go negative),
// marks the order 'refunded' (data is kept, not deleted), and pushes the new
// entitlement to sing-box inside the transaction. Idempotent guard: an
// already-refunded order returns ErrAlreadyRefunded.
//
// mode is "" (use the store's configured policy), "prorated", or "full". Under
// prorated, the refunded points = round(price × unused-fraction) where the
// fraction is derived per the configured basis (traffic / time / min). The
// entitlement reversal is identical in both modes — only the points differ.
func (s *Store) RefundOrder(orderID, operatorID int64, mode string, sync func(updated *User, resetUsed bool) error) (*User, *RefundQuote, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var (
		userID, pkgID, price, createdAt int64
		snap, status                    string
	)
	err = tx.QueryRow(`SELECT user_id, package_id, package_snapshot, price_points, status, created_at
		FROM orders WHERE id=?`, orderID).Scan(&userID, &pkgID, &snap, &price, &status, &createdAt)
	if err != nil {
		return nil, nil, ErrOrderNotFound
	}
	if status == "refunded" {
		return nil, nil, ErrAlreadyRefunded
	}

	var sp orderSnapshot
	_ = json.Unmarshal([]byte(snap), &sp)

	u, err := scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, userID))
	if err != nil {
		return nil, nil, err
	}
	if u == nil {
		return nil, nil, ErrUserNotFound
	}

	now := time.Now().Unix()

	// Compute the prorated refund from the CURRENT (pre-reversal) bucket state,
	// inside the tx so it sees any prior uncommitted writes. Must run before the
	// bucket reversal below, which mutates the same bucket.
	pol := s.refundPolicy()
	if mode == "full" || mode == "prorated" {
		pol.Mode = mode
	}
	quote, err := computeRefundQuote(tx, orderID, userID, pkgID, sp, price, pol, now)
	if err != nil {
		return nil, nil, err
	}
	quote.OrderID = orderID
	refundPts := quote.RefundPoints

	// Refund points (if any are due), then always write the refund ledger row.
	newPoints := u.Points + refundPts
	if refundPts != 0 {
		if _, err = tx.Exec(`UPDATE users SET points=? WHERE id=?`, newPoints, userID); err != nil {
			return nil, nil, err
		}
	}
	// Write the ledger row even for a zero-value refund (a fully-consumed order, or
	// a 0-point admin grant): the order is being marked refunded, and reconciling
	// order status against the ledger must never turn up a refunded order with no
	// corresponding flow. amount 0 is a legitimate, auditable event.
	{
		note := "退款: " + sp.Name
		if quote.Ratio < 1 {
			note = "退款(" + strconv.Itoa(int(math.Round(quote.Ratio*100))) + "%): " + sp.Name
		}
		if _, err = tx.Exec(`INSERT INTO point_transactions
			(user_id, amount, type, balance_after, ref_id, note, operator_id, created_at)
			VALUES (?,?, 'refund', ?, ?, ?, ?, ?)`,
			userID, refundPts, newPoints, orderID, note, operatorID, now); err != nil {
			return nil, nil, err
		}
	}

	// Bucket model: reverse this order's contribution to the package's (possibly
	// renewed/stacked) plan bucket, or claw the traffic top-up back out of the
	// pool (both clamped to what's already used).
	if sp.Type == "plan" {
		if err = reverseOrderBucket(tx, orderID, userID, pkgID, sp.TrafficBytes, sp.DurationDays, now); err != nil {
			return nil, nil, err
		}
		// If the bucket was fully removed and the current-plan pointer referenced
		// this package, clear it so it doesn't dangle. (If the bucket survives the
		// reversal, the pointer stays valid.)
		var stillExists int
		_ = tx.QueryRow(`SELECT 1 FROM user_plans WHERE user_id=? AND kind='plan' AND package_id=? LIMIT 1`, userID, pkgID).Scan(&stillExists)
		if stillExists == 0 {
			if _, err = tx.Exec(`UPDATE users SET current_plan_id=NULL WHERE id=? AND current_plan_id=?`, userID, pkgID); err != nil {
				return nil, nil, err
			}
		}
	} else if sp.Type == "traffic" {
		if err = subFromPool(tx, userID, sp.TrafficBytes, now); err != nil {
			return nil, nil, err
		}
	}

	// Recompute the legacy users.* aggregate from the surviving buckets instead of
	// doing independent clamped arithmetic on the columns — the latter drifted out
	// of sync with the (clamped) bucket values after a refund. Buckets are
	// authoritative; this keeps the dashboard totals consistent with them.
	newTraffic, newUp, newDown, newExpiry, err := recomputeUserAggregate(tx, userID, now)
	if err != nil {
		return nil, nil, err
	}

	// Mark the order refunded atomically, recording the actual refunded amount
	// and applied fraction for reporting/audit. The WHERE status='success' guard
	// makes a concurrent double-refund a no-op (0 rows) rather than a double
	// reversal.
	res, err := tx.Exec(`UPDATE orders SET status='refunded',
		refunded_points=?, refunded_at=?, refund_ratio=?, refunded_traffic=?
		WHERE id=? AND status='success'`,
		refundPts, now, quote.Ratio, quote.RefundTraffic, orderID)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, nil, ErrAlreadyRefunded
	}

	updated := *u
	updated.Points = newPoints
	updated.TrafficLimit = newTraffic
	updated.UsedUp = newUp
	updated.UsedDown = newDown
	updated.ExpiryAt = newExpiry
	if sp.Type == "plan" && u.CurrentPlanID.Valid && u.CurrentPlanID.Int64 == pkgID {
		updated.CurrentPlanID = sql.NullInt64{}
	}

	if sync != nil {
		if err = sync(&updated, false); err != nil {
			return nil, nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	committed = true
	return &updated, quote, nil
}

// ListOrdersAdmin returns recent orders across all users, joined with the
// username, optionally filtered by a username/order-id search term.
func (s *Store) ListOrdersAdmin(q string, limit int) ([]*Order, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var rows *sql.Rows
	var err error
	base := `SELECT o.id, o.user_id, COALESCE(u.username,''), o.package_id, o.package_snapshot, o.price_points, o.status, o.created_at,
		o.refunded_points, o.refunded_at, o.refund_ratio, o.refunded_traffic
		FROM orders o LEFT JOIN users u ON u.id=o.user_id`
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + q + "%"
		rows, err = s.db.Query(base+` WHERE u.username LIKE ? ORDER BY o.id DESC LIMIT ?`, like, limit)
	} else {
		rows, err = s.db.Query(base+` ORDER BY o.id DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Order{}
	for rows.Next() {
		var o Order
		var snap string
		if err := rows.Scan(&o.ID, &o.UserID, &o.Username, &o.PackageID, &snap, &o.PricePoints, &o.Status, &o.CreatedAt,
			&o.RefundedPoints, &o.RefundedAt, &o.RefundRatio, &o.RefundedTraffic); err != nil {
			return nil, err
		}
		var sp struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(snap), &sp) == nil {
			o.Name, o.Type = sp.Name, sp.Type
		}
		out = append(out, &o)
	}
	return out, rows.Err()
}

// ListOrders returns a user's orders, or all orders when userID == 0 (admin).
func (s *Store) ListOrders(userID int64, limit int) ([]*Order, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	const cols = `id, user_id, package_id, package_snapshot, price_points, status, created_at,
		refunded_points, refunded_at, refund_ratio, refunded_traffic`
	if userID == 0 {
		rows, err = s.db.Query(`SELECT `+cols+`
			FROM orders ORDER BY id DESC LIMIT ?`, limit)
	} else {
		rows, err = s.db.Query(`SELECT `+cols+`
			FROM orders WHERE user_id=? ORDER BY id DESC LIMIT ?`, userID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Order
	for rows.Next() {
		var o Order
		var snap string
		if err := rows.Scan(&o.ID, &o.UserID, &o.PackageID, &snap, &o.PricePoints, &o.Status, &o.CreatedAt,
			&o.RefundedPoints, &o.RefundedAt, &o.RefundRatio, &o.RefundedTraffic); err != nil {
			return nil, err
		}
		var sp struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(snap), &sp) == nil {
			o.Name, o.Type = sp.Name, sp.Type
		}
		out = append(out, &o)
	}
	return out, rows.Err()
}
