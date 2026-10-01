package store

import (
	"database/sql"
	"fmt"
)

const emailNotificationSchema = `CREATE TABLE IF NOT EXISTS user_notification_preferences (
 user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 business_email INTEGER NOT NULL DEFAULT 1 CHECK (business_email IN (0,1))
);`

// Preferences apply only to business reminders and manual broadcasts. Authentication and
// security mail deliberately never consults this table.
func (s *Store) BusinessEmailEnabled(userID int64) (bool, error) {
	var enabled bool
	err := s.db.QueryRow(`SELECT business_email FROM user_notification_preferences WHERE user_id=?`, userID).Scan(&enabled)
	if err == sql.ErrNoRows {
		return true, nil
	}
	return enabled, err
}

func (s *Store) SetBusinessEmailEnabled(userID int64, enabled bool) error {
	_, err := s.db.Exec(`INSERT INTO user_notification_preferences(user_id,business_email) VALUES(?,?)
 ON CONFLICT(user_id) DO UPDATE SET business_email=excluded.business_email`, userID, enabled)
	return err
}

// Keyset pagination bounds memory without silently omitting users after a fixed
// list limit. Include muted users so traffic recovery clears stale claims.
func (s *Store) BusinessEmailUsersAfter(after int64, limit int) ([]int64, error) {
	rows, err := s.db.Query(`SELECT id FROM users WHERE id>? AND status='active'
 AND email_verified=1 AND email IS NOT NULL AND email<>'' ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func notifyChannelKind(channel, kind string) (string, error) {
	switch channel {
	case "telegram":
		return kind, nil // Preserve pre-email claims across upgrades.
	case "email":
		return "email:" + kind, nil
	default:
		return "", fmt.Errorf("unknown notification channel")
	}
}
func (s *Store) ClaimNotifyChannel(userID int64, channel, kind, subject string) (bool, error) {
	key, err := notifyChannelKind(channel, kind)
	if err != nil {
		return false, err
	}
	return s.ClaimNotify(userID, key, subject)
}
func (s *Store) ClearNotifyChannel(userID int64, channel, kind, subject string) error {
	key, err := notifyChannelKind(channel, kind)
	if err != nil {
		return err
	}
	return s.ClearNotify(userID, key, subject)
}
