package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// PlanOption is one purchasable duration of a plan package — "30 天 / 100GB /
// 500 分". A package with options sells the same subscription at several lengths
// so the buyer picks one at checkout instead of the admin publishing a separate
// package per length. Traffic is per-option because a bucket's quota does not
// reset inside its period: a 90-day option that kept the 30-day quota would be a
// worse deal, not a longer one.
type PlanOption struct {
	Days         int64 `json:"days"`
	PricePoints  int64 `json:"price_points"`
	TrafficBytes int64 `json:"traffic_bytes"`
}

type Package struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // traffic | plan
	Name string `json:"name"`
	// QueueKey groups plans that renew one another. Blank means this package is
	// its own line ("pkg:<id>"). Two different products may deliberately share a
	// key, e.g. an old-price and a new-price edition of the same service.
	QueueKey     string       `json:"queue_key"`
	Description  string       `json:"description"`
	Highlights   []string     `json:"highlights"` // selling-point bullets shown in the shop
	PricePoints  int64        `json:"price_points"`
	TrafficBytes int64        `json:"traffic_bytes"`
	DurationDays int64        `json:"duration_days"`
	Options      []PlanOption `json:"options"` // selectable durations; empty = single-duration package
	Stock        int64        `json:"stock"`   // -1 = unlimited
	Enabled      bool         `json:"enabled"`
	SortOrder    int64        `json:"sort_order"`
	CreatedAt    int64        `json:"created_at"`
	GroupIDs     []int64      `json:"group_ids,omitempty"`      // plan↔node-groups: which nodes it grants (not a column)
	UserGroupIDs []int64      `json:"user_group_ids,omitempty"` // package↔user-groups: who may buy it; empty = public (not a column)
	Subscribers  int64        `json:"subscribers,omitempty"`    // users currently on this plan (not a column)
}

const pkgCols = `id, type, name, queue_key, description, highlights, price_points, traffic_bytes,
	duration_days, duration_options, stock, enabled, sort_order, created_at`

func scanPackage(sc scanner) (*Package, error) {
	var p Package
	var highlights, options string
	err := sc.Scan(&p.ID, &p.Type, &p.Name, &p.QueueKey, &p.Description, &highlights, &p.PricePoints, &p.TrafficBytes,
		&p.DurationDays, &options, &p.Stock, &p.Enabled, &p.SortOrder, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Highlights = decodeHighlights(highlights)
	p.Options = decodeOptions(options)
	return &p, nil
}

// decodeOptions parses the stored JSON array of durations. A blank value (a
// package created before options existed, or one that sells a single duration)
// yields nil — the caller then falls back to the package's own duration/price.
func decodeOptions(s string) []PlanOption {
	if s == "" {
		return nil
	}
	var out []PlanOption
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// encodeOptions serialises the duration list for storage, dropping entries with
// no duration (a blank row left in the admin form). Empty list → "" (not "[]"),
// so a single-duration package reads back as nil.
func encodeOptions(opts []PlanOption) string {
	clean := make([]PlanOption, 0, len(opts))
	for _, o := range opts {
		if o.Days > 0 {
			clean = append(clean, o)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	b, _ := json.Marshal(clean)
	return string(b)
}

// applyDefaultOption keeps the package's own duration/price/traffic columns equal
// to its FIRST option, so everything that reads a package without caring about
// the choice — the admin list, the shop card's headline price, an order snapshot,
// a grant that takes the default — sees a real, sellable combination instead of a
// stale leftover from before the options were edited.
func (p *Package) applyDefaultOption() {
	p.Options = decodeOptions(encodeOptions(p.Options)) // drop blank rows once, here
	if len(p.Options) == 0 {
		return
	}
	first := p.Options[0]
	p.DurationDays = first.Days
	p.PricePoints = first.PricePoints
	p.TrafficBytes = first.TrafficBytes
}

// ErrOptionNotFound means the requested duration is not (or is no longer) on sale
// for this package — an edited package, or a client posting an arbitrary length.
var ErrOptionNotFound = errors.New("所选时长不可用")

// MaxAdminAssignDays bounds a free-form admin grant. Shop purchases stay limited
// to published options; this only stops a typo minting a multi-millennium bucket.
// A listed option may still exceed it — the admin is picking a published length.
const MaxAdminAssignDays int64 = 3650

// ErrInvalidAssignDays is a negative or too-large custom duration on an admin
// grant. 0 still means "the package default" and is not this error.
var ErrInvalidAssignDays = errors.New("分配天数须在 1–3650 之间")

// forDuration returns the package as it should be charged and granted for the
// chosen duration: a copy whose price/traffic/duration are the selected option's.
// days == 0 means "the default" (the first option, or the package itself when it
// sells a single duration). Everything downstream — the points charged, the
// bucket minted, the order snapshot the refund later prorates against — reads
// these fields, so resolving here is the only place the choice is applied.
func (p *Package) forDuration(days int64) (*Package, error) {
	if len(p.Options) == 0 {
		if days == 0 || days == p.DurationDays {
			return p, nil
		}
		return nil, ErrOptionNotFound
	}
	if days == 0 {
		days = p.Options[0].Days
	}
	for _, o := range p.Options {
		if o.Days == days {
			eff := *p
			eff.DurationDays = o.Days
			eff.PricePoints = o.PricePoints
			eff.TrafficBytes = o.TrafficBytes
			return &eff, nil
		}
	}
	return nil, ErrOptionNotFound
}

// forAdminDuration is the admin-grant counterpart of forDuration. A listed
// length still carries that option's traffic; any other positive length clones
// the default option and only overrides DurationDays. Traffic packages have no
// duration (they top up the pool), so any days value collapses to the default.
// The shop must keep using forDuration — selling an unpublished length would
// bypass the price table.
func (p *Package) forAdminDuration(days int64) (*Package, error) {
	if days < 0 {
		return nil, ErrInvalidAssignDays
	}
	if p.Type != "plan" {
		return p.forDuration(0)
	}
	if eff, err := p.forDuration(days); err == nil {
		return eff, nil
	}
	if days > MaxAdminAssignDays {
		return nil, ErrInvalidAssignDays
	}
	base, err := p.forDuration(0)
	if err != nil {
		return nil, err
	}
	eff := *base
	eff.DurationDays = days
	return &eff, nil
}

// decodeHighlights parses the stored JSON array of selling points. A blank value
// (legacy rows, or a package with none) yields nil rather than an error.
func decodeHighlights(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// encodeHighlights serialises the selling-point list for storage, dropping blank
// entries so the shop never renders an empty bullet. Empty list → "" (not "[]"),
// so a package with no highlights reads back as nil.
func encodeHighlights(h []string) string {
	clean := make([]string, 0, len(h))
	for _, x := range h {
		if t := strings.TrimSpace(x); t != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	b, _ := json.Marshal(clean)
	return string(b)
}

func (s *Store) GetPackage(id int64) (*Package, error) {
	return scanPackage(s.db.QueryRow(`SELECT `+pkgCols+` FROM packages WHERE id=?`, id))
}

// ListPackages returns every package ordered for display (admin view). The
// user-facing shop uses ListPackagesForUser, which also applies the on-sale and
// user-group filters.
func (s *Store) ListPackages() ([]*Package, error) {
	rows, err := s.db.Query(`SELECT ` + pkgCols + ` FROM packages ORDER BY sort_order ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Package
	for rows.Next() {
		p, err := scanPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PackageNames returns id→current name for every package, so a user's plan
// buckets can display the package's live name instead of the snapshot taken at
// purchase (renaming a package should propagate to everyone who holds it). Only
// real packages appear; buckets for the pool/free/welcome/admin grants have no
// package row and keep their own name.
func (s *Store) PackageNames() (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT id, name FROM packages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// ListPackagesForUser returns the on-sale packages userID is allowed to buy:
// the public ones (no user-group bindings) plus those bound to a group the user
// belongs to. Restricted packages the user has no claim on are hidden outright.
//
// This is only the display half of the rule — Purchase re-checks inside its
// transaction, so hiding a package here is not what makes it unbuyable.
func (s *Store) ListPackagesForUser(userID int64) ([]*Package, error) {
	rows, err := s.db.Query(`SELECT `+pkgCols+` FROM packages p
		WHERE p.enabled=1 AND p.traffic_bytes>0 AND (p.stock<0 OR p.stock>0)
		  AND (NOT EXISTS (SELECT 1 FROM package_user_groups g WHERE g.package_id=p.id)
		       OR EXISTS (SELECT 1 FROM package_user_groups g
		                  JOIN user_group_members m ON m.group_id=g.group_id
		                  WHERE g.package_id=p.id AND m.user_id=?))
		ORDER BY p.sort_order ASC, p.id ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Package
	for rows.Next() {
		p, err := scanPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreatePackage(p Package) (int64, error) {
	p.applyDefaultOption()
	res, err := s.db.Exec(`INSERT INTO packages
		(type, name, queue_key, description, highlights, price_points, traffic_bytes, duration_days, duration_options, stock, enabled, sort_order, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.Type, p.Name, strings.TrimSpace(p.QueueKey), p.Description, encodeHighlights(p.Highlights), p.PricePoints, p.TrafficBytes,
		p.DurationDays, encodeOptions(p.Options), p.Stock, boolToInt(p.Enabled), p.SortOrder, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdatePackage(p Package) error {
	p.applyDefaultOption()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	key := strings.TrimSpace(p.QueueKey)
	if _, err := tx.Exec(`UPDATE packages SET
		type=?, name=?, queue_key=?, description=?, highlights=?, price_points=?, traffic_bytes=?,
		duration_days=?, duration_options=?, stock=?, enabled=?, sort_order=? WHERE id=?`,
		p.Type, p.Name, key, p.Description, encodeHighlights(p.Highlights), p.PricePoints, p.TrafficBytes,
		p.DurationDays, encodeOptions(p.Options), p.Stock, boolToInt(p.Enabled), p.SortOrder, p.ID); err != nil {
		return err
	}
	// queue_key is snapshotted on every entitlement so package edits/deletions do
	// not make an existing queue ambiguous. When an admin deliberately changes the
	// key, move all still-held shares with the product into the new line as one
	// atomic operation. Existing parallel active shares remain active; future
	// grants wait until all of them finish, preserving already-started time.
	if _, err := tx.Exec(`UPDATE user_plans SET queue_key=?, updated_at=?
		WHERE kind='plan' AND package_id=?`, effectiveQueueKey(p.ID, key), time.Now().Unix(), p.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// effectiveQueueKey gives a plan with no explicit renewal group a stable,
// collision-free line of its own. Keeping this derived default out of the
// package row makes the admin field genuinely optional while user_plans still
// carries a complete snapshot.
func effectiveQueueKey(packageID int64, key string) string {
	key = strings.TrimSpace(key)
	if key != "" {
		return key
	}
	return "pkg:" + strconv.FormatInt(packageID, 10)
}

// ReorderPackages sets sort_order to each id's position in the given slice, so
// the shop and admin list (both ORDER BY sort_order) render in this exact order.
// Ids not present keep their old sort_order and thus sort after the reordered
// ones (or interleave by their stale value) — callers pass the full list.
func (s *Store) ReorderPackages(ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE packages SET sort_order=? WHERE id=?`, i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeletePackage(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Drop the package's group bindings so plan_groups doesn't keep rows for a
	// package that no longer exists. (Callers guard against deleting a package
	// that still has subscribers, so current_plan_id is not orphaned here.)
	if _, err := tx.Exec(`DELETE FROM plan_groups WHERE package_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM package_user_groups WHERE package_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM packages WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetPackageEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE packages SET enabled=? WHERE id=?`, boolToInt(enabled), id)
	return err
}

// PackagePlanHolders returns the ids of users who hold a live plan bucket for
// this package. Unlike a lookup on the legacy users.current_plan_id pointer (which
// records only the SINGLE most recently purchased plan), this reflects the bucket
// model: a user may hold several plans at once, so retire/delete must act on the
// real buckets or they silently skip stacked/older subscribers.
func (s *Store) PackagePlanHolders(pkgID int64) ([]int64, error) {
	return queryInts(s.db, `SELECT DISTINCT user_id FROM user_plans
		WHERE kind='plan' AND package_id=? ORDER BY user_id`, pkgID)
}

// PlanSubscriberCounts maps package id → number of users holding that plan,
// counted from the authoritative buckets (not current_plan_id).
func (s *Store) PlanSubscriberCounts() (map[int64]int64, error) {
	rows, err := s.db.Query(`SELECT package_id, COUNT(DISTINCT user_id) FROM user_plans
		WHERE kind='plan' AND package_id>0 GROUP BY package_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var pid, n int64
		if err := rows.Scan(&pid, &n); err != nil {
			return nil, err
		}
		out[pid] = n
	}
	return out, rows.Err()
}

// RefundableOrdersForPackage returns every non-refunded successful order id for
// (user, package), oldest first, so retire can refund each stacked purchase — not
// merely the latest one.
func (s *Store) RefundableOrdersForPackage(userID, pkgID int64) ([]int64, error) {
	return queryInts(s.db, `SELECT id FROM orders WHERE user_id=? AND package_id=? AND status='success'
		ORDER BY created_at, id`, userID, pkgID)
}

// ClearPlanBucket removes any plan bucket the user holds for this package, nulls
// the legacy current_plan_id pointer if it still points here, and recomputes the
// user aggregate — all in one transaction. Used by retire to fully revoke a
// package after its orders are refunded (refunds shrink the bucket but may leave a
// used-up remnant), and idempotent so re-running is safe.
func (s *Store) ClearPlanBucket(userID, pkgID int64) error {
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
	if _, err := tx.Exec(`DELETE FROM user_plans WHERE user_id=? AND kind='plan' AND package_id=?`, userID, pkgID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE users SET current_plan_id=NULL WHERE id=? AND current_plan_id=?`, userID, pkgID); err != nil {
		return err
	}
	if _, _, _, _, err := recomputeUserAggregate(tx, userID, time.Now().Unix()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
