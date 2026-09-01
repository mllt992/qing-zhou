package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrCredsResetCooldown is returned by RotateNodeCredentials when the user's
// last rotation is too recent to allow another. See that function for why the
// action is rate-limited at all.
var ErrCredsResetCooldown = errors.New("节点凭据重置冷却中")

// NoCredsResetCooldown is the lastResetBefore that waives RotateNodeCredentials'
// cooldown entirely. Used by the operator path, which exists to serve exactly
// the user whose own rotation is already spent. A far-future cutoff rather than
// "now" so the waiver holds regardless of clock skew.
const NoCredsResetCooldown int64 = math.MaxInt64

type User struct {
	ID            int64
	Username      string
	Email         sql.NullString
	PasswordHash  string
	Role          string
	Status        string
	EmailVerified bool
	// EmailGateExempt is a persisted compatibility decision made at account
	// admission/migration time. It must not be inferred from a later purchase.
	EmailGateExempt bool
	Points          int64
	ClientID        sql.NullInt64
	ClientName      sql.NullString
	ClientUUID      sql.NullString
	ClientSecret    sql.NullString
	SubToken        sql.NullString
	CurrentPlanID   sql.NullInt64
	TrafficLimit    int64
	UsedUp          int64
	UsedDown        int64
	ExpiryAt        int64
	// CredsResetAt is when the user last rotated their node credentials, 0 if
	// never. Backs the cooldown on that action (see RotateNodeCredentials).
	CredsResetAt int64
	// Account-level mixed (HTTP/SOCKS5) proxy credential — one login valid on
	// every node the user is entitled to, stable across group moves and renewals.
	// ProxyExpiresAt 0 = permanent. See proxyaccount.go.
	ProxyUsername  string
	ProxyPassword  string
	ProxyExpiresAt int64
	// Remark is the admin's free-form note on this account. Panel-side only:
	// it never reaches sing-box config, a subscription, or the user's own pages.
	Remark    string
	CreatedAt int64
	UpdatedAt int64
	// LastOnlineAt is bumped by the stats poll whenever this user shows a
	// non-zero traffic delta, so it doubles as the proxy-side liveness signal.
	// Panel logins do not touch it — see sessions for that.
	LastOnlineAt int64
	// Subscription fetch telemetry is deliberately coarse. SubLastClient is a
	// bounded category selected by the API, not the raw User-Agent or an IP.
	SubLastFetchedAt int64
	SubLastFormat    string
	SubLastClient    string
}

const userCols = `id, username, email, password_hash, role, status, email_verified, email_gate_exempt, points,
	client_id, client_name, client_uuid, client_secret, sub_token, current_plan_id,
	traffic_limit, used_up, used_down, expiry_at, created_at, updated_at,
	last_online_at, sub_last_fetched_at, sub_last_format, sub_last_client,
	creds_reset_at, proxy_username, proxy_password, proxy_expires_at, remark`

type scanner interface{ Scan(...any) error }

func scanUser(sc scanner) (*User, error) {
	var u User
	err := sc.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Status,
		&u.EmailVerified, &u.EmailGateExempt, &u.Points, &u.ClientID, &u.ClientName, &u.ClientUUID,
		&u.ClientSecret, &u.SubToken, &u.CurrentPlanID, &u.TrafficLimit,
		&u.UsedUp, &u.UsedDown, &u.ExpiryAt, &u.CreatedAt, &u.UpdatedAt, &u.LastOnlineAt,
		&u.SubLastFetchedAt, &u.SubLastFormat, &u.SubLastClient,
		&u.CredsResetAt, &u.ProxyUsername, &u.ProxyPassword, &u.ProxyExpiresAt, &u.Remark)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) UserByUsername(username string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE username=?`, username))
}

func (s *Store) UserByID(id int64) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func (s *Store) UserByEmail(email string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE email=?`, email))
}

func (s *Store) UserBySubToken(token string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE sub_token=?`, token))
}

// NewUser holds the fields needed to create a panel user.
type NewUser struct {
	Username     string
	Email        string
	PasswordHash string
	Role         string // defaults to "user"
	Points       int64
	SubToken     string
	TrafficLimit int64
	ExpiryAt     int64
	Remark       string
	// EmailGateExempt is set only by trusted admission paths (invite/admin).
	// Ordinary open registration leaves it false, regardless of later purchases.
	EmailGateExempt bool
}

func (s *Store) CreateUser(nu NewUser) (int64, error) {
	role := nu.Role
	if role == "" {
		role = "user"
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO users
		(username, email, password_hash, role, status, email_verified, email_gate_exempt, points,
		 sub_token, traffic_limit, expiry_at, remark, created_at, updated_at)
		VALUES (?,?,?,?,'active',0,?,?,?,?,?,?,?,?)`,
		nu.Username, nullStr(nu.Email), nu.PasswordHash, role, nu.EmailGateExempt, nu.Points,
		nullStr(nu.SubToken), nu.TrafficLimit, nu.ExpiryAt, nu.Remark, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetUserClient records the provisioned sing-box client identity (including the
// credential secret, needed to rebuild config for edits without rotating it).
func (s *Store) SetUserClient(userID, clientID int64, name, uuid, secret string) error {
	_, err := s.db.Exec(
		`UPDATE users SET client_id=?, client_name=?, client_uuid=?, client_secret=?, updated_at=? WHERE id=?`,
		clientID, name, uuid, secret, time.Now().Unix(), userID)
	return err
}

// SetSubTokenIfEmpty gives a user their first subscription token — and only
// that. It never overwrites an existing token, so two concurrent callers can't
// hand out different links, and it can't be mistaken for a revocation: rotating
// a live token belongs in ResetSubscription, which also rotates the credentials
// the old link already handed out. Reports whether it wrote.
//
// Needed because not every account is born with a token: the first-boot admin is
// INSERTed by Seed, which predates the subscription column, so its sub_token is
// NULL and its subscription URL rendered empty.
func (s *Store) SetSubTokenIfEmpty(userID int64, token string) (bool, error) {
	res, err := s.db.Exec(`UPDATE users SET sub_token=?, updated_at=?
		WHERE id=? AND (sub_token IS NULL OR sub_token='')`,
		token, time.Now().Unix(), userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

const subscriptionFetchWriteInterval int64 = 3600

// RecordSubscriptionFetch records a successfully written subscription response.
// The conditional UPDATE is both the one-hour write throttle and the concurrency
// guard: two simultaneous refreshes that read the same old User can never both
// advance the row. Observational telemetry intentionally does not touch
// users.updated_at, which describes account changes elsewhere in the panel.
func (s *Store) RecordSubscriptionFetch(userID, fetchedAt int64, format, client string) (bool, error) {
	if userID <= 0 || fetchedAt <= 0 {
		return false, fmt.Errorf("invalid subscription fetch identity/time")
	}
	if format == "" || len(format) > 24 || client == "" || len(client) > 24 {
		return false, fmt.Errorf("invalid subscription fetch format/client")
	}
	res, err := s.db.Exec(`UPDATE users
		SET sub_last_fetched_at=?, sub_last_format=?, sub_last_client=?
		WHERE id=? AND (sub_last_fetched_at=0 OR sub_last_fetched_at<=?)`,
		fetchedAt, format, client, userID, fetchedAt-subscriptionFetchWriteInterval)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RotateSubToken changes the address a user's subscription is served at, and
// nothing else. Panel-only: the token is not part of any node's config, so no
// server is touched, nobody's connection drops, and the swap is effective the
// instant it commits.
//
// What this does NOT do is revoke access. Whoever fetched the old address still
// holds the node links it served, and those authenticate with the user's
// UUID/password — see RotateNodeCredentials for the half that cuts them off.
// Callers must not describe this as making the old links stop working.
func (s *Store) RotateSubToken(userID int64, subToken string) error {
	_, err := s.db.Exec(`UPDATE users SET sub_token=?, updated_at=? WHERE id=?`,
		subToken, time.Now().Unix(), userID)
	return err
}

// RotateNodeCredentials replaces the user's stable sing-box credential, which
// is what actually revokes links a leaked subscription already handed out.
//
// Two things are deliberately preserved:
//   - every client_name — the sing-box stats identity usage is metered against
//     (BucketByClientName). Rotating it would orphan the user's traffic history.
//   - the mixed (HTTP/SOCKS5) proxy password, which merely *falls back* to
//     client_secret when the user hasn't set a custom one. That credential is
//     excluded from the subscription, so it never leaked with the link; pinning
//     the current effective value before rotating keeps the user's 1Panel/Docker
//     proxies working instead of silently breaking them.
//
// Unlike RotateSubToken this is NOT panel-only: the new credentials only take
// effect once they reach the nodes, which happens on the controller's periodic
// rebuild. Applying it restarts sing-box on every server carrying the user's
// nodes, dropping every other user's live connections with it — hence the
// cooldown, which lives here rather than in the caller.
//
// lastResetBefore is that cooldown, as the newest creds_reset_at that may still
// rotate: a user whose stamp is newer is refused with ErrCredsResetCooldown. The
// check is the WHERE clause of the stamping UPDATE, not a separate read, so two
// concurrent requests cannot both pass it. Pass NoCredsResetCooldown to waive it.
func (s *Store) RotateNodeCredentials(userID, lastResetBefore int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	// Capture the currently effective protocol secret before rotating it. Bucket
	// mixed-proxy accounts may fall back to this value and are intentionally not
	// revoked by a subscription credential reset.
	var oldSecret sql.NullString
	if err := tx.QueryRow(`SELECT client_secret FROM users WHERE id=?`, userID).Scan(&oldSecret); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		return err
	}
	// Claim the rotation first: the users row carries creds_reset_at, so its
	// conditional UPDATE is both the cooldown gate and the stamp. A refused caller
	// therefore changes nothing at all.
	//
	// The users row is also the seed for the pool bucket of accounts provisioned
	// later (EnsurePoolBucket) and is the legacy identity, so leaving the leaked
	// value there would resurrect it.
	uu, ss := genBucketCreds()
	res, err := tx.Exec(`UPDATE users SET client_uuid=?, client_secret=?, creds_reset_at=?, updated_at=?
		WHERE id=? AND creds_reset_at<=?`, uu, ss, now, now, userID, lastResetBefore)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// No row matched: either the user is gone, or the cooldown has not
		// elapsed. Only the latter is a race, so tell them apart.
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM users WHERE id=?`, userID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		} else if err != nil {
			return err
		}
		return ErrCredsResetCooldown
	}

	// Pin the effective proxy credential before client_secret moves out from
	// under the fallback (see ProxySecret). Both places a credential can live have
	// to be pinned, or the line's proxy login silently changes with the rotation.
	if _, err := tx.Exec(`UPDATE user_plans SET proxy_password=?, updated_at=?
		WHERE user_id=? AND proxy_password=''`, oldSecret.String, now, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE plan_identities SET proxy_password=?, updated_at=?
		WHERE user_id=? AND proxy_password=''`, oldSecret.String, now, userID); err != nil {
		return err
	}
	// A reset is an explicit revocation, so migration aliases must not keep any
	// old subscription link alive. Bucket/line client_* columns are historical
	// storage only; runtime authentication reads the user's primary pair.
	if _, err := tx.Exec(`DELETE FROM user_credential_aliases WHERE user_id=?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetUserEmail changes a user's email and marks it unverified (pending re-verify).
// SetUserEmail rebinds a user's address and drops any verification token still
// outstanding for them.
//
// Invalidating the old tokens is the security-relevant half. A verify token
// carries only user_id — no address — and SetEmailVerified marks whatever
// address the row currently holds. So without this, a user could request a token
// for an address they own, not click it, rebind to someone else's address, then
// redeem the old token and end up email_verified on an address they never
// controlled — squatting it permanently, since both registration and rebinding
// reject an address already held by another account.
//
// Both statements share one transaction: leaving the address changed while the
// old tokens survived would be exactly the state this defends against.
func (s *Store) SetUserEmail(userID int64, email string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`UPDATE users SET email=?, email_verified=0, updated_at=? WHERE id=?`,
		nullStr(email), time.Now().Unix(), userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM email_tokens WHERE user_id=? AND purpose='verify' AND used=0`,
		userID); err != nil {
		return err
	}
	return tx.Commit()
}

// AdminSetUserEmail rebinds an address on an admin's authority: no verification
// round-trip, the same way an admin-created account is pre-verified at creation.
// An empty address unbinds (NULL, and unverified — there is nothing to verify).
//
// It is separate from SetUserEmail because the two differ on exactly the field
// that matters: SetUserEmail is the *user* claiming an address they must then
// prove they own, so it lands unverified. Here the admin is asserting it, and
// the user typically cannot receive mail at the old address — which is the
// situation that brought the admin here. Leaving it unverified would show the
// user a "邮箱未验证" prompt for an address they never chose and cannot confirm.
//
// Outstanding verify tokens are dropped for the same reason SetUserEmail drops
// them: a token carries only user_id, so one minted for the previous address
// would otherwise still redeem against this one. See SetUserEmail.
func (s *Store) AdminSetUserEmail(userID int64, email string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	verified := 0
	if email != "" {
		verified = 1
	}
	if _, err := tx.Exec(`UPDATE users SET email=?, email_verified=?, updated_at=? WHERE id=?`,
		nullStr(email), verified, time.Now().Unix(), userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM email_tokens WHERE user_id=? AND purpose='verify' AND used=0`,
		userID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateEntitlement persists traffic/expiry/plan/used changes (used by purchase
// and admin edits) and returns nothing; callers sync to sing-box separately.
func (s *Store) UpdateEntitlement(userID, trafficLimit, expiryAt int64) error {
	_, err := s.db.Exec(
		`UPDATE users SET traffic_limit=?, expiry_at=?, updated_at=? WHERE id=?`,
		trafficLimit, expiryAt, time.Now().Unix(), userID)
	return err
}

// UpdateUserUsage stores the latest used up/down bytes from sing-box.
func (s *Store) UpdateUserUsage(userID, up, down int64) error {
	_, err := s.db.Exec(
		`UPDATE users SET used_up=?, used_down=?, updated_at=? WHERE id=?`,
		up, down, time.Now().Unix(), userID)
	return err
}

func (s *Store) DeleteUser(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Clean up the user's operational rows so they don't linger as orphans.
	// Financial/audit history (orders, point_transactions) and the deliberately
	// snapshotted reg_code_uses are kept intentionally. user_plans (buckets) and
	// traffic_samples MUST go — there is no FK cascade, and a leftover bucket still
	// resolves via BucketByClientName (stale identity) while its samples accumulate.
	for _, q := range []string{
		`DELETE FROM sessions WHERE user_id=?`,
		`DELETE FROM email_tokens WHERE user_id=?`,
		`DELETE FROM user_disabled_nodes WHERE user_id=?`,
		`DELETE FROM device_addons WHERE user_id=?`,
		`DELETE FROM announcement_reads WHERE user_id=?`,
		`DELETE FROM user_group_members WHERE user_id=?`,
		`DELETE FROM user_plans WHERE user_id=?`,
		`DELETE FROM plan_identities WHERE user_id=?`,
		`DELETE FROM user_credential_aliases WHERE user_id=?`,
		`DELETE FROM traffic_samples WHERE user_id=?`,
		// The daily rollup is keyed by user_id too, and unlike the samples it is
		// never pruned by age — leaving it behind would keep a deleted account's
		// bytes in every site-wide usage total forever, and hand them to whoever
		// is next assigned that id.
		`DELETE FROM traffic_daily WHERE user_id=?`,
		`DELETE FROM telegram_binds WHERE user_id=?`,
		`DELETE FROM telegram_bind_tokens WHERE user_id=?`,
		`DELETE FROM user_notify_log WHERE user_id=?`,
		`DELETE FROM users WHERE id=?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetUserRemark stores the admin's note on an account. It is deliberately its
// own statement rather than a field on AdminUpdateUser: the note is pure panel
// metadata, so it must not be able to fail (or roll back) an edit that also
// moves quota, and it is written from the create path too.
func (s *Store) SetUserRemark(id int64, remark string) error {
	_, err := s.db.Exec(`UPDATE users SET remark=?, updated_at=? WHERE id=?`,
		remark, time.Now().Unix(), id)
	return err
}

// ManualGrant describes an admin's manual "general allowance" for a user. Enabled
// false removes any existing grant; an enabled grant has a positive finite Traffic;
// Expiry 0 = never. A nil *ManualGrant passed to AdminUpdateUser leaves the grant
// untouched (for edits that only change status/reset).
type ManualGrant struct {
	Enabled bool
	Traffic int64
	Expiry  int64
}

// AdminUpdateUser applies an admin's edits to a user: status (ban/unban), an
// optional usage reset, and the manual allowance grant.
//
// The manual grant lives in a dedicated, real, metered bucket (kind='plan',
// package_id=0, "管理员额度") rather than the legacy users.traffic_limit column.
// That column is only a display mirror recomputed from the buckets, so writing it
// directly did nothing to enforcement and was silently overwritten on the user's
// next purchase/refund. Routing the grant into a bucket makes it actually usable
// (scoped like the pool: free group + the user's plan groups — see orderBuckets)
// and durable.
func (s *Store) AdminUpdateUser(id int64, status string, resetUsed bool, manual *ManualGrant) error {
	now := time.Now().Unix()
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

	if _, err := tx.Exec(`UPDATE users SET status=?, updated_at=? WHERE id=?`, status, now, id); err != nil {
		return err
	}
	if resetUsed {
		// Zero the authoritative bucket counters (not just the users.* mirror, which a
		// recompute would immediately overwrite back from the buckets).
		if _, err := tx.Exec(`UPDATE user_plans SET used_up=0, used_down=0, updated_at=? WHERE user_id=?`, now, id); err != nil {
			return err
		}
	}

	if manual != nil {
		if err := applyManualGrant(tx, id, manual, now); err != nil {
			return err
		}
	}

	// Recompute the legacy users.* aggregate so the dashboard mirrors the buckets.
	if _, _, _, _, err := recomputeUserAggregate(tx, id, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// applyManualGrant upserts (Enabled) or removes (!Enabled) the user's package_id=0
// admin-grant bucket within the caller's transaction.
func applyManualGrant(tx txLike, userID int64, g *ManualGrant, now int64) error {
	if g.Enabled && g.Traffic <= 0 {
		return errors.New("管理员额度的流量必须大于 0")
	}
	var bid int64
	qerr := tx.QueryRow(`SELECT id FROM user_plans WHERE user_id=? AND kind='plan' AND package_id=0 ORDER BY id LIMIT 1`, userID).Scan(&bid)
	switch {
	case errors.Is(qerr, sql.ErrNoRows):
		if g.Enabled {
			var uname string
			if err := tx.QueryRow(`SELECT username FROM users WHERE id=?`, userID).Scan(&uname); err != nil {
				return err
			}
			_, _, err := ensureUserProtocolCredential(tx, userID, uname, now)
			if err != nil {
				return err
			}
			if _, err := insertBucket(tx, &Bucket{
				UserID: userID, Kind: "plan", PackageID: 0, Name: "管理员额度",
				ClientName:   fmt.Sprintf("qz_%s_admin", uname),
				TrafficLimit: g.Traffic, ExpiryAt: g.Expiry, CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		return nil
	case qerr != nil:
		return qerr
	default:
		if g.Enabled {
			_, err := tx.Exec(`UPDATE user_plans SET traffic_limit=?, expiry_at=?, updated_at=? WHERE id=?`,
				g.Traffic, g.Expiry, now, bid)
			return err
		}
		_, err := tx.Exec(`DELETE FROM user_plans WHERE id=?`, bid)
		return err
	}
}

// ListUsers returns users for the admin list, newest first, optional search.
func (s *Store) ListUsers(search string, limit int) ([]*User, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT ` + userCols + ` FROM users`
	args := []any{}
	if search != "" {
		// The remark is searchable too: an admin who writes "公司同事张三" on an
		// account expects to find it by that, not only by the login name.
		q += ` WHERE username LIKE ? OR email LIKE ? OR remark LIKE ?`
		args = append(args, "%"+search+"%", "%"+search+"%", "%"+search+"%")
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UsersWithClient returns users that have a provisioned sing-box client.
func (s *Store) UsersWithClient() ([]*User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users
		WHERE client_name IS NOT NULL AND client_name <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
