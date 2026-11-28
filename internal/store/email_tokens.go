// [INPUT]: SQLite users, email tokens and login sessions.
// [OUTPUT]: Issues address-bound links and applies verification or recovery atomically.
// [POS]: Owns mailbox proof for registration, recovery and self-service changes.
// [PROTOCOL]: Update this header on change, then check AGENTS.md
package store

import (
	"database/sql"
	"errors"
	"time"
)

var ErrEmailUnavailable = errors.New("邮箱已被其他账号绑定")
var ErrEmailChanged = errors.New("账户信息已更新，请重新登录后再试")

// IssueEmailToken binds the link to the same address the caller will send to.
// The INSERT rechecks account state even if the caller read it before a rebind.
func (s *Store) IssueEmailToken(userID int64, address, token, purpose string, ttl time.Duration) (bool, error) {
	now := time.Now()
	res, err := s.db.Exec(`INSERT INTO email_tokens (user_id,address,token,purpose,expires_at,created_at)
		SELECT id,email,?,?,?,? FROM users WHERE id=? AND email=? AND email!=''
		AND ((?='verify' AND email_verified=0) OR (?='reset' AND email_verified=1))`,
		token, purpose, now.Add(ttl).Unix(), now.Unix(), userID, address, purpose, purpose)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// A rebind lives on the confirmation token until its recipient accepts it.
// A revoked session or intervening password/address change invalidates the request.
func (s *Store) RequestEmailChange(u *User, sessionID, address, token string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var taken bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=lower(?) AND id<>?)`, address, u.ID).Scan(&taken); err != nil {
		return err
	}
	if taken {
		return ErrEmailUnavailable
	}
	if _, err := tx.Exec(`DELETE FROM email_tokens WHERE user_id=? AND purpose='rebind'`, u.ID); err != nil {
		return err
	}
	now := time.Now()
	res, err := tx.Exec(`INSERT INTO email_tokens(user_id,address,token,purpose,expires_at,created_at)
		SELECT id,?,?,'rebind',?,? FROM users WHERE id=? AND password_hash=? AND email IS ? AND status='active'
		AND EXISTS(SELECT 1 FROM sessions WHERE user_id=users.id AND jti=?)`,
		address, token, now.Add(24*time.Hour).Unix(), now.Unix(), u.ID, u.PasswordHash, u.Email, sessionID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrEmailChanged
	}
	return tx.Commit()
}

func (s *Store) ConfirmEmail(token, purpose string) (int64, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var uid int64
	var address string
	err = tx.QueryRow(`SELECT user_id,address FROM email_tokens WHERE token=? AND used=0
		AND purpose=? AND purpose IN ('verify','rebind') AND expires_at>?`, token, purpose, time.Now().Unix()).Scan(&uid, &address)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	res, err := tx.Exec(`UPDATE users SET email=?, email_verified=1,updated_at=? WHERE id=?
		AND (email=? OR ?='rebind') AND NOT EXISTS(SELECT 1 FROM users WHERE lower(email)=lower(?) AND id<>?)`,
		address, time.Now().Unix(), uid, address, purpose, address, uid)
	if err != nil {
		return 0, false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return 0, false, err
	}
	if purpose == "rebind" {
		if _, err := tx.Exec(`DELETE FROM email_tokens WHERE user_id=? AND token<>?`, uid, token); err != nil {
			return 0, false, err
		}
	}
	if _, err := tx.Exec(`UPDATE email_tokens SET used=1 WHERE token=?`, token); err != nil {
		return 0, false, err
	}
	return uid, true, tx.Commit()
}

func (s *Store) EmailAlreadyConfirmed(token string) bool {
	var confirmed bool
	_ = s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM email_tokens t JOIN users u ON u.id=t.user_id
		WHERE t.token=? AND t.purpose IN ('verify','rebind') AND t.used=1 AND t.address=u.email
		AND t.expires_at>? AND u.email_verified=1)`, token, time.Now().Unix()).Scan(&confirmed)
	return confirmed
}

const resetOwner = `SELECT u.id FROM email_tokens t JOIN users u ON u.id=t.user_id
	WHERE t.token=? AND t.purpose='reset' AND t.used=0 AND t.expires_at>? AND t.address=u.email AND u.email_verified=1`

// Check the token cheaply before bcrypt. The password update rechecks it under the write lock.
func (s *Store) ResetTokenValid(token string) (bool, error) {
	var valid bool
	err := s.db.QueryRow("SELECT EXISTS("+resetOwner+")", token, time.Now().Unix()).Scan(&valid)
	return valid, err
}

func (s *Store) ResetPassword(token, hash string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var uid int64
	now := time.Now().Unix()
	err = tx.QueryRow("UPDATE users SET password_hash=?,updated_at=? WHERE id=("+resetOwner+") RETURNING id", hash, now, token, now).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM email_tokens WHERE user_id=? AND purpose IN ('rebind','reset')`, uid); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id=?`, uid); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) SetEmailVerified(userID int64) error {
	_, err := s.db.Exec(`UPDATE users SET email_verified=1, updated_at=? WHERE id=?`, time.Now().Unix(), userID)
	return err
}

func (s *Store) UpdatePassword(userID int64, passwordHash string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE users SET password_hash=?,updated_at=? WHERE id=?`, passwordHash, time.Now().Unix(), userID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM email_tokens WHERE user_id=? AND purpose IN ('reset','rebind')`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CleanupEmailTokens() {
	_, _ = s.db.Exec(`DELETE FROM email_tokens WHERE expires_at < ?`, time.Now().Unix())
}
