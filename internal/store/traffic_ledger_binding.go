package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Ownership is captured with the immutable journal entry, before any quota
// update can fail. Mutable proxy names are never resolved again during replay.
func (s *Store) bindTrafficIdentity(tx *sql.Tx, p TrafficPoll, name string) error {
	kind, linkID, err := relayObservationIdentity(tx, p.ServerID, name)
	if err != nil {
		return err
	}
	var bucketID, userID, pkgID int64
	var bucketKind string
	if kind == "legacy_shared_relay" {
		var collision int
		err = tx.QueryRow(`SELECT (SELECT COUNT(*) FROM users WHERE proxy_username=? OR client_name=?)+(SELECT COUNT(*) FROM user_plans WHERE proxy_username=? OR client_name=?)+(SELECT COUNT(*) FROM plan_identities WHERE proxy_username=? OR client_name=?)`, name, name, name, name, name, name).Scan(&collision)
		if err != nil {
			return err
		}
		if collision > 0 {
			kind = "ambiguous_identity"
		}
	} else if kind == "" && strings.HasPrefix(name, "qzr_") {
		kind = "unknown"
	}
	if kind == "" {
		canonical, _ := canonicalStatsIdentity(name)
		bucketID, userID, pkgID, bucketKind, err = s.resolveStatsIdentity(tx, canonical, map[string]*Bucket{})
		if errors.Is(err, sql.ErrNoRows) {
			// Account identities don't name a bucket. Freeze their owner now; the
			// existing entitlement selector chooses and freezes a bucket before debit.
			userID, err = accountUserID(tx, canonical)
		}
		if errors.Is(err, sql.ErrNoRows) {
			kind = "unknown"
			userID = 0
		} else if err != nil {
			return err
		} else {
			kind = "direct_user"
		}
	}
	_, err = tx.Exec(`INSERT INTO traffic_poll_bindings(poll_id,counter_name,source_kind,link_id,user_id,bucket_id,package_id,bucket_kind) VALUES(?,?,?,?,?,?,?,?)`, p.ID, name, kind, linkID, userID, bucketID, pkgID, bucketKind)
	return err
}

func (s *Store) boundTrafficAccountTargets(pollID string) (map[int64]*Bucket, error) {
	rows, err := s.db.Query(`SELECT DISTINCT user_id FROM traffic_poll_bindings WHERE poll_id=? AND source_kind='direct_user' AND bucket_id=0`, pollID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := map[int64]*Bucket{}
	var firstErr error
	for _, id := range ids {
		b, e := s.accountMeterBucket(id)
		if e != nil {
			if firstErr == nil {
				firstErr = e
			}
			continue
		}
		out[id] = b
	}
	return out, firstErr
}
