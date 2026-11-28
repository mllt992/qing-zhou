package store

import (
	"database/sql"
	"encoding/json"
	"strings"
)

const relaySemanticSpecPrefix = "json-v1:"

type relaySpecHashes struct {
	current, previous, legacy string
	managedCertificate        bool
}

// Match the production map decoder/marshaller: ignore whitespace, object key
// order and numeric spelling while retaining array order and actual values.
func canonicalRelayJSONObject(raw string) (string, error) {
	object := map[string]interface{}{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &object); err != nil {
			return "", err
		}
	}
	if object == nil {
		object = map[string]interface{}{}
	}
	canonical, err := json.Marshal(object)
	return string(canonical), err
}

// A missing/null server profile leaves inline TLS in place, whereas an explicit
// object (including {}) overrides it. Preserve this presence distinction; the
// normal options decoder can safely treat missing and empty objects alike.
func canonicalRelayTLSProfile(raw string) (string, error) {
	if trimmed := strings.TrimSpace(raw); trimmed == "" || trimmed == "null" {
		return "null", nil
	}
	return canonicalRelayJSONObject(raw)
}

// Only translate a legacy hash when it still proves the current desired raw
// configuration. An arbitrary/stale hash is never blessed as equivalent. Old
// cert_id-only hashes could not prove certificate contents, so those retain the
// same credentials but must re-accept the actual target before source switching.
func upgradeRelaySpecHash(tx *sql.Tx, link *RelayMeteringLink, hashes relaySpecHashes) (bool, error) {
	if strings.HasPrefix(link.SpecHash, relaySemanticSpecPrefix) ||
		(link.SpecHash != hashes.previous && link.SpecHash != hashes.legacy) {
		return false, nil
	}
	unprovenCertificate := hashes.managedCertificate && link.SpecHash != hashes.previous
	if unprovenCertificate {
		// accepted_at also proves that this credential was installed before an
		// opt-out. Keep that compatibility receipt; state/activated_at and the
		// link receipt below independently revoke readiness for the new spec.
		// A hash migration must never silently retire published credentials.
		if _, err := tx.Exec(`UPDATE relay_metering_users SET state='prepared',activated_at=0 WHERE link_id=? AND generation=? AND enabled=1`, link.ID, link.Generation); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`UPDATE relay_metering_links SET spec_hash=?,state='prepared',accepted_at=0,activated_at=0 WHERE id=?`, hashes.current, link.ID); err != nil {
			return false, err
		}
		link.State, link.AcceptedAt, link.ActivatedAt = "prepared", 0, 0
	} else {
		if _, err := tx.Exec(`UPDATE relay_metering_links SET spec_hash=? WHERE id=?`, hashes.current, link.ID); err != nil {
			return false, err
		}
	}
	link.SpecHash = hashes.current
	return true, nil
}

func (s *Store) migrateRelayProtocolAuthVerification(tx *sql.Tx) error {
	if err := s.rewrapComposeSecrets(tx, true); err != nil {
		return err
	}
	if _, err := tx.Exec(relayProtocolAuthSchema); err != nil {
		return err
	}
	// Startup runs migrations before Seed installs the jwt_secret fallback on
	// the live Store. Resolve it in this snapshot, with explicit QZ_SECRET_KEY
	// taking precedence, without changing shared key/cache state on failure.
	reader := &Store{secretKey: s.secretKey}
	if len(reader.secretKey) == 0 {
		var key string
		err := tx.QueryRow(`SELECT value FROM settings WHERE key='jwt_secret'`).Scan(&key)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if key != "" {
			reader.SetSecretKey([]byte(key))
		}
	}
	links, err := relayMeteringLinksWith(tx)
	if err != nil {
		return err
	}
	for _, link := range links {
		target, err := meteringInboundWith(tx, link.TargetInboundID)
		if err != nil {
			return err
		}
		if target == nil || target.ServerID != link.TargetServerID {
			continue
		}
		hashes, _, _, err := reader.relayTargetSpecHashesWith(tx, target)
		if err != nil {
			// A previously broken target remains unproven. Do not reinterpret a
			// missing certificate/server as a valid new hash or block upgrades.
			continue
		}
		if _, err = upgradeRelaySpecHash(tx, link, hashes); err != nil {
			return err
		}
	}
	return nil
}
