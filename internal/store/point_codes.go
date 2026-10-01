package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const PointCodeSchema = `
CREATE TABLE IF NOT EXISTS point_codes (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 code_hash TEXT NOT NULL UNIQUE,
 code_hint TEXT NOT NULL,
 points INTEGER NOT NULL CHECK(points>0),
 note TEXT NOT NULL DEFAULT '',
 expires_at INTEGER NOT NULL DEFAULT 0,
 disabled_at INTEGER NOT NULL DEFAULT 0,
 redeemed_by INTEGER NOT NULL DEFAULT 0,
 redeemed_username TEXT NOT NULL DEFAULT '',
 redeemed_at INTEGER NOT NULL DEFAULT 0,
 created_by INTEGER NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS point_code_attempts (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 failures INTEGER NOT NULL DEFAULT 0,
 locked_until INTEGER NOT NULL DEFAULT 0
);
`

var (
	ErrPointCodeMissing     = errors.New("兑换码不存在")
	ErrPointCodeUsed        = errors.New("兑换码已使用，次数已用尽")
	ErrPointCodeExpired     = errors.New("兑换码已过期")
	ErrPointCodeDisabled    = errors.New("兑换码已停用")
	ErrPointCodeLocked      = errors.New("连续兑换失败过多，请 15 分钟后重试")
	ErrPointBalanceOverflow = errors.New("积分余额超出上限，请联系管理员")
)

type PointCode struct {
	ID               int64  `json:"id"`
	Hint             string `json:"hint"`
	Points           int64  `json:"points"`
	Note             string `json:"note"`
	ExpiresAt        int64  `json:"expires_at"`
	DisabledAt       int64  `json:"disabled_at"`
	RedeemedBy       int64  `json:"redeemed_by"`
	RedeemedUsername string `json:"redeemed_username"`
	RedeemedAt       int64  `json:"redeemed_at"`
	CreatedBy        int64  `json:"created_by"`
	CreatedAt        int64  `json:"created_at"`
	Status           string `json:"status"`
}

type GeneratedPointCode struct {
	PointCode
	Code string `json:"code"`
}

func normalizePointCode(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.NewReplacer("-", "", " ", "", "\t", "", "\n", "", "\r", "").Replace(s)
	return s
}
func pointCodeHash(code string) string {
	h := sha256.Sum256([]byte(normalizePointCode(code)))
	return hex.EncodeToString(h[:])
}

// Plaintext bearer codes are returned once, never stored. 128 random bits per
// code means neither adjacent IDs nor batch sizes help predict another code.
func (s *Store) GeneratePointCodes(operator, points int64, count, days int, note string) ([]GeneratedPointCode, error) {
	if points < 1 || points > 1_000_000_000 || count < 1 || count > 100 || days < 1 || days > 3650 || len([]rune(note)) > 256 {
		return nil, errors.New("面额须为 1–10 亿积分，数量 1–100，有效期 1–3650 天，备注最多 256 字")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	out := make([]GeneratedPointCode, 0, count)
	for i := 0; i < count; i++ {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return nil, err
		}
		raw := strings.ToUpper(hex.EncodeToString(random))
		code := "QZ-" + raw[:8] + "-" + raw[8:16] + "-" + raw[16:24] + "-" + raw[24:]
		hint := "QZ-••••-" + raw[24:]
		res, err := tx.Exec(`INSERT INTO point_codes(code_hash,code_hint,points,note,expires_at,created_by,created_at) VALUES(?,?,?,?,?,?,?)`, pointCodeHash(code), hint, points, strings.TrimSpace(note), now+int64(days)*86400, operator, now)
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		out = append(out, GeneratedPointCode{PointCode: PointCode{ID: id, Hint: hint, Points: points, Note: strings.TrimSpace(note), ExpiresAt: now + int64(days)*86400, CreatedBy: operator, CreatedAt: now, Status: "unused"}, Code: code})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListPointCodes(status string, before int64, limit int) ([]PointCode, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	where := "1=1"
	now := time.Now().Unix()
	switch status {
	case "", "all":
	case "used":
		where = "redeemed_at>0"
	case "disabled":
		where = "redeemed_at=0 AND disabled_at>0"
	case "expired":
		where = fmt.Sprintf("redeemed_at=0 AND disabled_at=0 AND expires_at<=%d", now)
	case "unused":
		where = fmt.Sprintf("redeemed_at=0 AND disabled_at=0 AND expires_at>%d", now)
	default:
		return nil, errors.New("状态筛选无效")
	}
	if before <= 0 {
		before = math.MaxInt64
	}
	rows, err := s.db.Query(`SELECT id,code_hint,points,note,expires_at,disabled_at,redeemed_by,redeemed_username,redeemed_at,created_by,created_at FROM point_codes WHERE `+where+` AND id<? ORDER BY id DESC LIMIT ?`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PointCode{}
	for rows.Next() {
		var c PointCode
		if err := rows.Scan(&c.ID, &c.Hint, &c.Points, &c.Note, &c.ExpiresAt, &c.DisabledAt, &c.RedeemedBy, &c.RedeemedUsername, &c.RedeemedAt, &c.CreatedBy, &c.CreatedAt); err != nil {
			return nil, err
		}
		switch {
		case c.RedeemedAt > 0:
			c.Status = "used"
		case c.DisabledAt > 0:
			c.Status = "disabled"
		case c.ExpiresAt <= now:
			c.Status = "expired"
		default:
			c.Status = "unused"
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DisablePointCodes(ids []int64) (int64, error) {
	if len(ids) < 1 || len(ids) > 100 {
		return 0, errors.New("每次请选择 1–100 个兑换码")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var count int64
	for _, id := range ids {
		if id <= 0 {
			return 0, errors.New("兑换码编号无效")
		}
		r, err := tx.Exec(`UPDATE point_codes SET disabled_at=? WHERE id=? AND redeemed_at=0 AND disabled_at=0`, time.Now().Unix(), id)
		if err != nil {
			return 0, err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return 0, err
		}
		count += n
	}
	return count, tx.Commit()
}

// RedeemPointCode commits code consumption, balance and the audit ledger in one
// IMMEDIATE transaction. Invalid guesses update a durable per-user lockout;
// infrastructure errors roll back and never consume either code or allowance.
func (s *Store) RedeemPointCode(userID int64, raw string) (int64, int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	var balance int64
	var username, status string
	if err := tx.QueryRow(`SELECT points,username,status FROM users WHERE id=?`, userID).Scan(&balance, &username, &status); err != nil {
		return 0, 0, err
	}
	if status != "active" {
		return 0, 0, ErrUserNotFound
	}
	var failures int
	var lockedUntil int64
	err = tx.QueryRow(`SELECT failures,locked_until FROM point_code_attempts WHERE user_id=?`, userID).Scan(&failures, &lockedUntil)
	if err != nil && err != sql.ErrNoRows {
		return 0, 0, err
	}
	if lockedUntil > now {
		return 0, 0, ErrPointCodeLocked
	}
	if lockedUntil > 0 {
		failures = 0
	}
	fail := func(reason error) (int64, int64, error) {
		failures++
		lockedUntil = 0
		if failures >= 5 {
			lockedUntil = now + 15*60
		}
		if _, err := tx.Exec(`INSERT INTO point_code_attempts(user_id,failures,locked_until) VALUES(?,?,?) ON CONFLICT(user_id) DO UPDATE SET failures=excluded.failures,locked_until=excluded.locked_until`, userID, failures, lockedUntil); err != nil {
			return 0, 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, 0, err
		}
		if lockedUntil > now {
			return 0, 0, ErrPointCodeLocked
		}
		return 0, 0, reason
	}
	if len(raw) > 128 {
		return fail(ErrPointCodeMissing)
	}
	var id, points, expires, disabled, redeemed int64
	err = tx.QueryRow(`SELECT id,points,expires_at,disabled_at,redeemed_at FROM point_codes WHERE code_hash=?`, pointCodeHash(raw)).Scan(&id, &points, &expires, &disabled, &redeemed)
	if err == sql.ErrNoRows {
		return fail(ErrPointCodeMissing)
	}
	if err != nil {
		return 0, 0, err
	}
	if redeemed > 0 {
		return fail(ErrPointCodeUsed)
	}
	if disabled > 0 {
		return fail(ErrPointCodeDisabled)
	}
	if expires <= now {
		return fail(ErrPointCodeExpired)
	}
	if points <= 0 || balance > math.MaxInt64-points {
		return 0, 0, ErrPointBalanceOverflow
	}
	balance += points
	if _, err := tx.Exec(`UPDATE users SET points=?,updated_at=? WHERE id=?`, balance, now, userID); err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(`UPDATE point_codes SET redeemed_by=?,redeemed_username=?,redeemed_at=? WHERE id=?`, userID, username, now, id); err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(`INSERT INTO point_transactions(user_id,amount,type,balance_after,ref_id,note,operator_id,created_at) VALUES(?,?,'redeem',?,?,?,0,?)`, userID, points, balance, id, "兑换码积分到账", now); err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(`DELETE FROM point_code_attempts WHERE user_id=?`, userID); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return balance, points, nil
}
