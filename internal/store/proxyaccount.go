package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Account-level mixed (HTTP/SOCKS5) proxy credential.
//
// The per-bucket credential (see SetBucketProxyCred) belongs to一份套餐, which is
// the right home for a metering identity but the wrong one for something a human
// pastes into 1Panel / Docker / git: which bucket owns a node is decided by node
// group (pickOwner), so moving a node to another group — or simply renewing onto
// a different份 — handed the user a different username/password and silently
// broke every place they had saved it.
//
// This credential belongs to the USER instead: one login, valid on every node
// they are entitled to, changing only when they change it. Entitlement is
// untouched — it is injected only into inbounds a bucket of theirs already owns,
// so moving a node between groups still decides WHETHER the node is theirs, just
// no longer WHICH password it wants.
//
// Two consequences worth stating plainly:
//
//   - Metering. sing-box reports traffic per username with no inbound dimension
//     ("user>>>NAME>>>traffic>>>uplink"), and CollectStats sums a name across
//     every server. One stable name therefore yields one number, which has to
//     land on one bucket — see accountMeterBucket for which one and why.
//   - The free bucket keeps its own credential. Free-group traffic is
//     deliberately metered apart from paid quota (see KindFree), and routing it
//     through an account identity charged to a paid bucket would reintroduce
//     exactly the bug that split them.
type proxyAcct struct {
	name      string
	password  string
	expiresAt int64 // 0 = permanent
}

func (a proxyAcct) active(now int64) bool {
	return a.name != "" && (a.expiresAt == 0 || a.expiresAt > now)
}

// ProxyCredActive reports whether the user's account-level proxy credential
// exists and has not hit its own (optional, separate from any plan) expiry.
func (u *User) ProxyCredActive(now int64) bool {
	return proxyAcct{name: u.ProxyUsername, expiresAt: u.ProxyExpiresAt}.active(now)
}

// proxyAccounts loads every user's account-level credential, keyed by user id.
// One query for the whole config build — BuildUsersByTag needs it per user and
// per inbound, and a lookup per (user, inbound) pair would be thousands of round
// trips on a busy panel.
func (s *Store) proxyAccounts() (map[int64]proxyAcct, error) {
	rows, err := s.db.Query(`SELECT id, proxy_username, proxy_password, proxy_expires_at
		FROM users WHERE proxy_username <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]proxyAcct{}
	for rows.Next() {
		var id int64
		var a proxyAcct
		if err := rows.Scan(&id, &a.name, &a.password, &a.expiresAt); err != nil {
			return nil, err
		}
		out[id] = a
	}
	return out, rows.Err()
}

// proxyNameOwner is the row a uniqueness check must ignore: the one being
// written, which is allowed to keep the name it already holds. Zero means "no
// such row is being written" for the id fields; the line pair uses -1 because 0
// is a real package_id (the admin grant).
type proxyNameOwner struct {
	userID   int64 // users row being written
	bucketID int64 // user_plans row being written
	lineUser int64 // plan_identities row being written
	linePkg  int64
}

// proxyNameTaken reports whether username already exists as a proxy identity
// anywhere. All three tables share ONE identity namespace, because the stats
// resolver looks a name up across all of them — two rows with the same name
// would meter each other's traffic. client_name is included as belt and
// braces; the qz_ ban in ValidateProxyUsername already excludes it.
func (s *Store) proxyNameTaken(username string, self proxyNameOwner) (bool, error) {
	return s.proxyNameTakenWith(s.db, username, self)
}

func (s *Store) proxyNameTakenWith(db txLike, username string, self proxyNameOwner) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT
		  (SELECT COUNT(*) FROM users      WHERE proxy_username=? AND id<>?)
		+ (SELECT COUNT(*) FROM user_plans WHERE (proxy_username=? AND id<>?) OR client_name=?)
		+ (SELECT COUNT(*) FROM plan_identities
		    WHERE (proxy_username=? OR client_name=?) AND NOT (user_id=? AND package_id=?))`,
		username, self.userID,
		username, self.bucketID, username,
		username, username, self.lineUser, self.linePkg).Scan(&n)
	return n > 0, err
}

// proxyAccountPrefix marks a system-minted account name. Not reserved (a user
// may still choose one): uniqueness is checked on every mint and every edit, so
// a collision is refused rather than needing a banned prefix to prevent it.
const proxyAccountPrefix = "px_"

// genProxyAccountCreds mints a name/password pair for a fresh account credential.
// The password is full-entropy random rather than something memorable: it is
// copied, never typed, and it authenticates a plain HTTP proxy on the open
// internet.
func genProxyAccountCreds() (name, password string) {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return proxyAccountPrefix + hex.EncodeToString(b[:8]), hex.EncodeToString(b[8:])
}

// EnsureProxyAccount mints the user's account-level proxy credential if they
// have none. Idempotent, so it doubles as the backfill for accounts provisioned
// before this existed.
//
// Every user gets one at provision time rather than lazily on first view: the
// credential is an identity in the generated sing-box config, so minting it on a
// panel read would mean the login the page just showed does not work until the
// next config rebuild.
func (s *Store) EnsureProxyAccount(userID int64) error {
	return s.ensureProxyAccountWith(s.db, userID)
}

func (s *Store) ensureProxyAccountWith(db txLike, userID int64) error {
	var cur string
	if err := db.QueryRow(`SELECT proxy_username FROM users WHERE id=?`, userID).Scan(&cur); err != nil {
		return err
	}
	if cur != "" {
		return nil
	}
	now := time.Now().Unix()
	var lastErr error
	for i := 0; i < 5; i++ {
		name, pw := genProxyAccountCreds()
		taken, err := s.proxyNameTakenWith(db, name, proxyNameOwner{userID: userID, lineUser: -1, linePkg: -1})
		if err != nil {
			return err
		}
		if taken {
			continue
		}
		// The proxy_username='' guard makes concurrent mints settle on one winner
		// instead of the second overwriting the first — the loser's user is already
		// holding the name the winner wrote.
		res, err := db.Exec(`UPDATE users SET proxy_username=?, proxy_password=?, updated_at=?
			WHERE id=? AND proxy_username=''`, name, pw, now, userID)
		if err != nil {
			lastErr = err
			continue // most likely lost a race on the unique index — mint another name
		}
		if aff, _ := res.RowsAffected(); aff == 0 {
			return nil // already minted concurrently; that credential is as good as this one
		}
		return nil
	}
	// Every attempt collided or failed. Carry the last write error rather than
	// swallowing it: a DB fault looks identical to bad luck from the outside, and
	// this runs during provisioning, where the cause matters.
	if lastErr != nil {
		return fmt.Errorf("生成代理账号失败: %w", lastErr)
	}
	return errors.New("生成代理账号失败，请重试")
}

// SetUserProxyCred replaces the user's account-level proxy credential: a
// proxy-only account (username/password, unrelated to the login account) with an
// optional expiry. expiresAt 0 = permanent.
func (s *Store) SetUserProxyCred(userID int64, username, password string, expiresAt int64) error {
	if err := ValidateProxyUsername(username); err != nil {
		return err
	}
	if len(password) < 6 || len(password) > 128 {
		return errors.New("密码需 6-128 位")
	}
	if expiresAt < 0 {
		return errors.New("有效期非法")
	}
	taken, err := s.proxyNameTaken(username, proxyNameOwner{userID: userID, lineUser: -1, linePkg: -1})
	if err != nil {
		return err
	}
	if taken {
		return errors.New("该用户名已被占用，请换一个")
	}
	res, err := s.db.Exec(`UPDATE users SET proxy_username=?, proxy_password=?, proxy_expires_at=?, updated_at=?
		WHERE id=?`, username, password, expiresAt, time.Now().Unix(), userID)
	if err != nil {
		return errors.New("保存失败，用户名可能已被占用") // unique-index guard against a concurrent duplicate
	}
	// Never report success on a write that matched nothing: the user would be told
	// their proxy login was saved and then fail to connect with it.
	if aff, _ := res.RowsAffected(); aff == 0 {
		return errors.New("用户不存在")
	}
	return nil
}

// UserProxyAccount is the account-level credential as the panel presents it.
type UserProxyAccount struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	ExpiresAt int64  `json:"expires_at"` // 0 = permanent
	Expired   bool   `json:"expired"`
	// MeterPlan is the份 this credential's traffic is charged to, "" when the
	// user has nothing chargeable (in which case it authenticates nowhere).
	MeterPlan string `json:"meter_plan"`
	// Idle is true when the credential is fine but currently opens nothing: the
	// user has no usable paid份, so there is no bucket to charge it to and
	// BuildUsersByTag withholds it from every inbound. Their remaining nodes are
	// free-group ones, which keep authenticating with the free bucket's own
	// credential — deliberately, so free bytes never land on a paid counter.
	//
	// Reported rather than left for the page to infer from an empty MeterPlan:
	// "所有节点通用" printed over a login that works on none of them is the same
	// class of lie as hiding a credential that works, and the two states differ
	// by exactly one thing the user needs to be told.
	Idle bool `json:"idle"`
}

// ProxyAccountView returns the user's account-level credential, or nil if they
// have none.
//
// Reported even when expired, deliberately: the page renders this block instead
// of deriving it from the node list, so an expired credential stays visible and
// editable. Deriving it from the nodes would make an expired one vanish from the
// panel — the nodes fall back to their bucket credential — and the only way to
// renew the account one would be to not have let it expire.
func (s *Store) ProxyAccountView(u *User) *UserProxyAccount {
	if u == nil || u.ProxyUsername == "" {
		return nil
	}
	now := time.Now().Unix()
	v := &UserProxyAccount{
		Username:  u.ProxyUsername,
		Password:  u.ProxyPassword,
		ExpiresAt: u.ProxyExpiresAt,
		Expired:   !u.ProxyCredActive(now),
	}
	if !v.Expired {
		b, err := s.accountMeterBucket(u.ID)
		if err != nil || b == nil {
			v.Idle = true
		} else {
			v.MeterPlan = s.bucketPlanName(b)
		}
	}
	return v
}

// AccountMeterPlanName is the display name of the份 an account-level credential's
// traffic is charged to, or "" when the user has nothing chargeable (in which
// case the credential is in no config either). The live package name wins over
// the bucket's stored snapshot, matching how the份 is labelled everywhere else.
func (s *Store) AccountMeterPlanName(userID int64) string {
	b, err := s.accountMeterBucket(userID)
	if err != nil || b == nil {
		return ""
	}
	return s.bucketPlanName(b)
}

// bucketPlanName is a份's display name, live package name winning over the
// bucket's stored snapshot to match how it is labelled everywhere else.
func (s *Store) bucketPlanName(b *Bucket) string {
	var name string
	if err := s.db.QueryRow(`SELECT COALESCE(NULLIF(pk.name,''), p.name)
		FROM user_plans p LEFT JOIN packages pk ON pk.id = p.package_id
		WHERE p.id = ?`, b.ID).Scan(&name); err != nil {
		return b.Name
	}
	return name
}

// accountTargets pre-resolves the account-level identities in one stats poll to
// the bucket each is charged to, keyed by stats name.
//
// Deciding that bucket costs several reads (the user's whole bucket order), and
// AddUsageBatch exists to hold SQLite's single write lock for as short a time as
// possible — doing this per identity inside the transaction would put exactly
// the kind of lookup back under the lock that the batch was written to remove.
// Resolving here is also strictly fewer queries: one pass over the account names
// tells the resolver which stats names are account identities at all, so the
// ordinary bucket identities no longer probe the users table on their way past.
//
// A per-user failure is skipped rather than aborting the poll — the counters
// were already reset on the node, so discarding the whole batch would throw away
// everyone else's traffic too. The first such error is returned for the caller
// to report alongside whatever did land.
func (s *Store) accountTargets(deltas map[string]UsageDelta) (map[string]*Bucket, error) {
	accounts, err := s.proxyAccounts()
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Bucket, len(accounts))
	if len(accounts) == 0 {
		return out, nil
	}
	byName := make(map[string]int64, len(accounts))
	for uid, a := range accounts {
		byName[a.name] = uid
	}
	var firstErr error
	for name := range deltas {
		uid, ok := byName[name]
		if !ok {
			continue
		}
		b, err := s.accountMeterBucket(uid)
		if errors.Is(err, sql.ErrNoRows) {
			continue // nothing chargeable — the identity is in no config either
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out[name] = b
	}
	return out, firstErr
}

// accountUserID maps an account-level proxy username to its user, or ErrNoRows.
func accountUserID(tx txLike, statName string) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM users WHERE proxy_username <> '' AND proxy_username = ?`,
		statName).Scan(&id)
	return id, err
}

// accountMeterBucket is the bucket an account-level credential's traffic is
// charged to, or ErrNoRows if the user has nothing chargeable.
//
// One stable username spans every node the user can reach, so unlike a bucket
// identity it does not name its own bucket — sing-box hands back a single number
// per username with no inbound dimension, so there is nothing to split by node
// even in principle. The charge target is therefore whichever bucket OWNERSHIP
// itself prefers, asked of the same function that decides ownership: the
// soonest-expiring usable份 (grants sort among them), then the pool. Only the
// free bucket is skipped — this credential is never injected into a free-owned
// inbound, precisely so free bytes stay off a paid counter.
//
// With one plan — the ordinary case — this is exact: that plan owns every node
// and gets every byte. With several running at once it concentrates HTTP proxy
// traffic on the one closest to running out, rather than on whichever node
// happened to carry it. The subscription page states which份 that is instead of
// leaving the user to infer it.
//
// Deliberately reuses userBucketOrder rather than re-deriving the priority in
// SQL for the stats path: two encodings of "who owns this" drift, and the way
// that shows up is a user's traffic quietly landing on the wrong套餐. The extra
// read is one query per account identity per poll, on a connection separate from
// the stats write transaction (the pool is sized for exactly this nesting).
func (s *Store) accountMeterBucket(userID int64) (*Bucket, error) {
	ord, _, err := s.userBucketOrder(userID, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	for _, ob := range ord {
		if ob.b.Kind != KindFree {
			return ob.b, nil
		}
	}
	return nil, sql.ErrNoRows
}
