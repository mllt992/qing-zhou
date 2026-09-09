package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type OAuthState struct {
	StateHash, BrowserHash, Nonce, Verifier, ConfigHash, SessionID string
	UserID                                                         int64
	ExpiresAt                                                      int64
}

func (s *Store) CreateOAuthState(ctx context.Context, v OAuthState) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM oauth_states WHERE expires_at<=?`, time.Now().Unix()); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_states`).Scan(&count); err != nil {
		return err
	}
	if count >= 4096 {
		return fmt.Errorf("登录请求过多，请稍后再试")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO oauth_states(state_hash,browser_hash,nonce,verifier,config_hash,user_id,session_id,expires_at) VALUES(?,?,?,?,?,?,?,?)`, v.StateHash, v.BrowserHash, v.Nonce, s.encrypt(v.Verifier), v.ConfigHash, v.UserID, v.SessionID, v.ExpiresAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Browser binding, expiry and single consumption are checked by one statement.
func (s *Store) ConsumeOAuthState(ctx context.Context, stateHash, browserHash string) (*OAuthState, error) {
	v := &OAuthState{}
	err := s.db.QueryRowContext(ctx, `DELETE FROM oauth_states WHERE state_hash=? AND browser_hash=? AND expires_at>? RETURNING nonce,verifier,config_hash,user_id,session_id,expires_at`, stateHash, browserHash, time.Now().Unix()).Scan(&v.Nonce, &v.Verifier, &v.ConfigHash, &v.UserID, &v.SessionID, &v.ExpiresAt)
	if err != nil {
		return nil, err
	}
	verifier, ok := s.decryptOK(v.Verifier)
	if !ok {
		return nil, fmt.Errorf("登录状态无法解密")
	}
	v.Verifier = verifier
	return v, nil
}

var ErrOAuthEmailExists = errors.New("邮箱已有轻舟账号，请先使用原账号登录，再到个人中心绑定认证中心")
var ErrOAuthSignupClosed = errors.New("该认证中心账号尚未绑定，请先注册轻舟账号并在个人中心绑定")

type OAuthIdentity struct {
	Issuer      string `json:"issuer"`
	Subject     string `json:"subject"`
	UserID      int64  `json:"-"`
	Provisioned bool   `json:"-"`
}

func (s *Store) OAuthIdentityForUser(userID int64, issuer string) (*OAuthIdentity, error) {
	v := &OAuthIdentity{Issuer: issuer, UserID: userID}
	err := s.db.QueryRow(`SELECT subject,provisioned FROM oauth_identities WHERE user_id=? AND issuer=?`, userID, issuer).Scan(&v.Subject, &v.Provisioned)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

// Only a verified (issuer, subject) identifies an account. An email collision
// never authorizes merging, and provider roles are deliberately not accepted.
func (s *Store) OAuthLoginUser(ctx context.Context, issuer, subject string, nu *NewUser, emailVerified bool, uuid, secret string) (int64, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var id int64
	var provisioned bool
	err = tx.QueryRowContext(ctx, `SELECT user_id,provisioned FROM oauth_identities WHERE issuer=? AND subject=?`, issuer, subject).Scan(&id, &provisioned)
	if err == nil {
		return id, !provisioned, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	if nu == nil {
		return 0, false, ErrOAuthSignupClosed
	}
	if nu.Email != "" {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE lower(email)=lower(?)`, nu.Email).Scan(&exists); err != nil {
			return 0, false, err
		}
		if exists > 0 {
			return 0, false, ErrOAuthEmailExists
		}
	}
	now := time.Now().Unix()
	res, err := tx.ExecContext(ctx, `INSERT INTO users(username,email,password_hash,role,status,email_verified,points,sub_token,client_name,client_uuid,client_secret,created_at,updated_at) VALUES(?,?,?,'user','active',?,?,?,?,?,?,?,?)`, nu.Username, nullStr(nu.Email), nu.PasswordHash, emailVerified, nu.Points, nu.SubToken, "qz_"+nu.Username, uuid, secret, now, now)
	if err != nil {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO oauth_identities(issuer,subject,user_id,provisioned) VALUES(?,?,?,0)`, issuer, subject, id); err != nil {
		return 0, false, err
	}
	if err = tx.Commit(); err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func (s *Store) BindOAuthIdentity(ctx context.Context, userID int64, issuer, subject string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM users WHERE id=?`, userID).Scan(&status); err != nil {
		return err
	}
	if status != "active" {
		return fmt.Errorf("账号不可用")
	}
	var existing int64
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM oauth_identities WHERE issuer=? AND subject=?`, issuer, subject).Scan(&existing)
	if err == nil && existing == userID {
		return nil
	}
	if err == nil {
		return fmt.Errorf("此认证中心账号已绑定其他轻舟账号")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO oauth_identities(issuer,subject,user_id,provisioned) VALUES(?,?,?,1)`, issuer, subject, userID)
	if err != nil {
		return fmt.Errorf("账号已有绑定，不能覆盖")
	}
	return tx.Commit()
}

func (s *Store) MarkOAuthProvisioned(ctx context.Context, issuer, subject string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE oauth_identities SET provisioned=1 WHERE issuer=? AND subject=?`, issuer, subject)
	return err
}
