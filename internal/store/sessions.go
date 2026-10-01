package store

import "time"

type Session struct {
	ID        int64  `json:"id"`
	Jti       string `json:"-"`
	IP        string `json:"ip"`
	UserAgent string `json:"user_agent"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  int64  `json:"last_seen"`
	Current   bool   `json:"current"`
}

func (s *Store) CreateSession(userID int64, jti, ip, ua string) error {
	now := time.Now().Unix()
	// Collapse repeated logins from the same device (same IP + user-agent) into a
	// single session row, so refreshing/re-logging-in doesn't pile up duplicates.
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE user_id=? AND ip=? AND user_agent=?`, userID, ip, ua)
	_, err := s.db.Exec(`INSERT INTO sessions (user_id, jti, ip, user_agent, created_at, last_seen)
		VALUES (?,?,?,?,?,?)`, userID, jti, ip, ua, now, now)
	return err
}

// SessionValid reports whether a session for jti still exists (not revoked).
func (s *Store) SessionValid(jti string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT 1 FROM sessions WHERE jti=?`, jti).Scan(&n)
	return n == 1
}

// SessionValidForUser verifies ownership as well as revocation. Security proofs
// are bound to both fields, including when sessions are changed outside the API.
func (s *Store) SessionValidForUser(jti string, userID int64) bool {
	var n int
	err := s.db.QueryRow(`SELECT 1 FROM sessions WHERE jti=? AND user_id=?`, jti, userID).Scan(&n)
	return err == nil && n == 1
}

// TouchSession bumps last_seen at most once per minute.
func (s *Store) TouchSession(jti string) {
	now := time.Now().Unix()
	_, _ = s.db.Exec(`UPDATE sessions SET last_seen=? WHERE jti=? AND last_seen < ?`, now, jti, now-60)
}

func (s *Store) ListSessions(userID int64) ([]*Session, error) {
	rows, err := s.db.Query(`SELECT id, jti, ip, user_agent, created_at, last_seen
		FROM sessions WHERE user_id=? ORDER BY last_seen DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Session{}
	for rows.Next() {
		var se Session
		if err := rows.Scan(&se.ID, &se.Jti, &se.IP, &se.UserAgent, &se.CreatedAt, &se.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, &se)
	}
	return out, rows.Err()
}

// PurgeExpiredSessions deletes sessions whose login token has expired
// (created_at < minCreatedAt) — those JWTs can never authenticate again, so the
// rows are dead weight. Lazy GC called when listing.
func (s *Store) PurgeExpiredSessions(minCreatedAt int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE created_at < ?`, minCreatedAt)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListActiveSessions returns the user's still-valid sessions (token not expired,
// i.e. created_at >= minCreatedAt), collapsed to one row per device (same ip +
// user_agent) keeping the most-recently-active one. The current session's row is
// always the representative for its device so the "本机" flag stays correct.
func (s *Store) ListActiveSessions(userID, minCreatedAt int64, currentJti string) ([]*Session, error) {
	rows, err := s.db.Query(`SELECT id, jti, ip, user_agent, created_at, last_seen
		FROM sessions WHERE user_id=? AND created_at >= ? ORDER BY last_seen DESC, id DESC`, userID, minCreatedAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := map[string]int{}
	out := []*Session{}
	for rows.Next() {
		var se Session
		if err := rows.Scan(&se.ID, &se.Jti, &se.IP, &se.UserAgent, &se.CreatedAt, &se.LastSeen); err != nil {
			return nil, err
		}
		key := se.IP + "\x00" + se.UserAgent
		if i, ok := idx[key]; ok {
			if se.Jti == currentJti { // prefer the current device's own row
				cur := se
				out[i] = &cur
			}
			continue
		}
		cur := se
		idx[key] = len(out)
		out = append(out, &cur)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSessionByJti(jti string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE jti=?`, jti)
	return err
}

// DeleteUserSession removes a session by id, scoped to the owner.
func (s *Store) DeleteUserSession(userID, id int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id=? AND user_id=?`, id, userID)
	return err
}

// DeleteUserSessionsExcept logs out all of a user's sessions but keepJti.
func (s *Store) DeleteUserSessionsExcept(userID int64, keepJti string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id=? AND jti<>?`, userID, keepJti)
	return err
}

// DeleteUserSessions logs out all of a user's sessions.
func (s *Store) DeleteUserSessions(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

// CleanupSessions deletes sessions inactive since before cutoff (unix seconds).
func (s *Store) CleanupSessions(cutoff int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE last_seen < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
