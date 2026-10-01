package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// protocolCredentialGrace is long enough for ordinary subscription clients to
// refresh many times after an online upgrade. The old values are server-side
// aliases only: every newly rendered subscription uses users.client_*.
const protocolCredentialGrace = 90 * 24 * time.Hour

// UserCredentialAlias is a protocol credential a client may still hold from a
// pre-stable-identity release. source_name is not an authentication field on a
// normal inbound; it is retained solely to reproduce the legacy per-route
// credential derivation exactly.
type UserCredentialAlias struct {
	ID           int64
	UserID       int64
	SourceName   string
	ClientUUID   string
	ClientSecret string
	ValidUntil   int64
}

// ensureUserProtocolCredential returns the single protocol credential owned by
// the user, creating it when an admin/import flow grants a plan before normal
// provisioning has run. Metering names remain per bucket; only the secret
// material is user-level.
func ensureUserProtocolCredential(tx txLike, userID int64, username string, now int64) (string, string, error) {
	var name sql.NullString
	var uu, secret sql.NullString
	err := tx.QueryRow(`SELECT client_name, client_uuid, client_secret FROM users WHERE id=?`, userID).
		Scan(&name, &uu, &secret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrUserNotFound
	}
	if err != nil {
		return "", "", err
	}
	if uu.String != "" && secret.String != "" {
		return uu.String, secret.String, nil
	}
	newUUID, newSecret := genBucketCreds()
	clientName := name.String
	if clientName == "" {
		clientName = "qz_" + username
	}
	if _, err := tx.Exec(`UPDATE users SET client_name=?, client_uuid=?, client_secret=?, updated_at=?
		WHERE id=?`, clientName, newUUID, newSecret, now, userID); err != nil {
		return "", "", err
	}
	return newUUID, newSecret, nil
}

// ensureSpecificUserProtocolCredential is the provisioning/import counterpart:
// preserve the exact credential the caller already chose, but never overwrite a
// user that has a primary identity.
func ensureSpecificUserProtocolCredential(tx txLike, userID int64, name, clientUUID, clientSecret string, now int64) (string, string, error) {
	var uu, secret sql.NullString
	if err := tx.QueryRow(`SELECT client_uuid, client_secret FROM users WHERE id=?`, userID).Scan(&uu, &secret); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrUserNotFound
		}
		return "", "", err
	}
	if uu.String != "" && secret.String != "" {
		return uu.String, secret.String, nil
	}
	if clientUUID == "" || clientSecret == "" {
		clientUUID, clientSecret = genBucketCreds()
	}
	if _, err := tx.Exec(`UPDATE users SET client_name=CASE WHEN COALESCE(client_name,'')='' THEN ? ELSE client_name END,
		client_uuid=?, client_secret=?, updated_at=? WHERE id=?`, name, clientUUID, clientSecret, now, userID); err != nil {
		return "", "", err
	}
	return clientUUID, clientSecret, nil
}

func (s *Store) activeCredentialAliases(now int64) (map[int64][]UserCredentialAlias, error) {
	rows, err := s.db.Query(`SELECT id, user_id, source_name, client_uuid, client_secret, valid_until
		FROM user_credential_aliases WHERE valid_until>? ORDER BY id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]UserCredentialAlias{}
	for rows.Next() {
		var a UserCredentialAlias
		if err := rows.Scan(&a.ID, &a.UserID, &a.SourceName, &a.ClientUUID, &a.ClientSecret, &a.ValidUntil); err != nil {
			return nil, err
		}
		out[a.UserID] = append(out[a.UserID], a)
	}
	return out, rows.Err()
}

// Alias stats names are reversible, so traffic authenticated by an old UUID is
// still charged to the bucket that currently owns the node. '~' cannot occur in
// system client names or user proxy names (see ValidateProxyUsername).
const credentialAliasMarker = "~qza"

func credentialAliasStatsName(base string, aliasID int64) string {
	return base + credentialAliasMarker + strconv.FormatInt(aliasID, 10)
}

func baseCredentialAliasIdentity(name string) (string, int64, bool) {
	i := strings.LastIndex(name, credentialAliasMarker)
	if i <= 0 {
		return name, 0, false
	}
	id, err := strconv.ParseInt(name[i+len(credentialAliasMarker):], 10, 64)
	if err != nil || id <= 0 {
		return name, 0, false
	}
	return name[:i], id, true
}

// canonicalStatsIdentity peels the two suffixes emitted by the config builder.
// Their order is fixed: bucket~alias~route.
func canonicalStatsIdentity(name string) (base string, aliasID int64) {
	base = name
	if b, ok := baseRouteIdentity(base); ok {
		base = b
	}
	if b, id, ok := baseCredentialAliasIdentity(base); ok {
		base, aliasID = b, id
	}
	return
}

const stableProtocolCredentialMigration = "20260826_stable_protocol_credentials_v1"

// migrateStableProtocolCredentials is the online-upgrade bridge. It runs once:
// existing bucket/line credentials become 90-day aliases, while users.client_*
// becomes the sole primary credential returned by new subscriptions. A fresh DB
// has no provisioned users, so it records the migration without creating any
// compatibility rows.
func (s *Store) migrateStableProtocolCredentials() error {
	return s.migrationTransaction(s.migrateStableProtocolCredentialsTx)
}

func (s *Store) migrateStableProtocolCredentialsTx(tx *sql.Tx) error {

	var applied int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, stableProtocolCredentialMigration).Scan(&applied); err != nil {
		return err
	}
	if applied > 0 {
		// Expired rows are inert; prune them on an ordinary restart so compatibility
		// storage disappears without a permanent background subsystem.
		if _, err := tx.Exec(`DELETE FROM user_credential_aliases WHERE valid_until<=?`, time.Now().Unix()); err != nil {
			return err
		}
		return nil
	}

	now := time.Now().Unix()
	validUntil := now + int64(protocolCredentialGrace/time.Second)
	type userRow struct {
		id             int64
		username, name string
		uuid, secret   sql.NullString
	}
	rows, err := tx.Query(`SELECT id, username, COALESCE(client_name,''), client_uuid, client_secret
		FROM users u WHERE EXISTS (SELECT 1 FROM user_plans p WHERE p.user_id=u.id) ORDER BY id`)
	if err != nil {
		return err
	}
	var users []userRow
	for rows.Next() {
		var u userRow
		if err := rows.Scan(&u.id, &u.username, &u.name, &u.uuid, &u.secret); err != nil {
			rows.Close()
			return err
		}
		users = append(users, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	type oldCred struct{ name, uuid, secret string }
	for _, u := range users {
		credRows, err := tx.Query(`
			SELECT COALESCE(client_name,''), client_uuid, client_secret FROM users
			 WHERE id=? AND client_uuid<>'' AND client_secret<>''
			UNION
			SELECT client_name, client_uuid, client_secret FROM plan_identities
			 WHERE user_id=? AND client_uuid<>'' AND client_secret<>''
			UNION
			SELECT client_name, client_uuid, client_secret FROM user_plans
			 WHERE user_id=? AND client_uuid<>'' AND client_secret<>''`, u.id, u.id, u.id)
		if err != nil {
			return err
		}
		var old []oldCred
		for credRows.Next() {
			var c oldCred
			if err := credRows.Scan(&c.name, &c.uuid, &c.secret); err != nil {
				credRows.Close()
				return err
			}
			old = append(old, c)
		}
		credRows.Close()
		if err := credRows.Err(); err != nil {
			return err
		}

		primaryUUID, primarySecret := u.uuid.String, u.secret.String
		if primaryUUID == "" || primarySecret == "" {
			if len(old) > 0 {
				primaryUUID, primarySecret = old[0].uuid, old[0].secret
			} else {
				primaryUUID, primarySecret = genBucketCreds()
			}
			name := u.name
			if name == "" {
				name = "qz_" + u.username
			}
			if _, err := tx.Exec(`UPDATE users SET client_name=?, client_uuid=?, client_secret=?, updated_at=? WHERE id=?`,
				name, primaryUUID, primarySecret, now, u.id); err != nil {
				return err
			}
		}

		// Include every historical source name, even when its raw credential equals
		// the primary. Old logical-route credentials hashed that name into the wire
		// credential, so equality before derivation does not make it redundant.
		for _, c := range old {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO user_credential_aliases
				(user_id, source_name, client_uuid, client_secret, valid_until, created_at)
				VALUES (?,?,?,?,?,?)`, u.id, c.name, c.uuid, c.secret, validUntil, now); err != nil {
				return err
			}
		}
	}

	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(?,?)`,
		stableProtocolCredentialMigration, now); err != nil {
		return fmt.Errorf("record stable credential migration: %w", err)
	}
	return nil
}
