package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The pre-versioned release already wrote these historical markers. They stay
// intact so adoption never repeats security-sensitive classification or extends
// an existing credential grace period.
const legacyBaselineVersion = "000001_legacy_baseline"

type migration struct {
	id    int
	name  string
	apply func(*sql.Tx) error
}

func (m migration) version() string { return fmt.Sprintf("%06d_%s", m.id, m.name) }

// Append only. A version owns its DDL, data backfill and indexes together.
// The baseline adopts all previously released schemas without reinterpreting
// their historical one-shot flags. Future changes each get a new ID/function.
func (s *Store) migrations() []migration {
	return []migration{
		{1, "legacy_baseline", s.migrateLegacyBaseline},
		{2, "business_email_preferences", func(tx *sql.Tx) error { _, err := tx.Exec(emailNotificationSchema); return err }},
	}
}

// Migrate must complete before any request/background worker is started.
// Each version runs in its own BEGIN IMMEDIATE (configured by Open), with the
// applied marker committed in the very same transaction. A crash/failure rolls
// back both DDL and data; the next startup retries only unfinished versions.
func (s *Store) Migrate() error {
	if err := s.runMigrations(s.migrations()); err != nil {
		return err
	}
	// Expiry housekeeping is deliberately separate from one-shot migrations.
	// Deleting expired aliases never renews or invents compatibility credentials.
	if _, err := s.db.Exec(`DELETE FROM user_credential_aliases WHERE valid_until<=?`, time.Now().Unix()); err != nil {
		return fmt.Errorf("credential alias maintenance: %w", err)
	}
	return nil
}

var migrationName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func (s *Store) runMigrations(chain []migration) error {
	known := map[string]bool{stableProtocolCredentialMigration: true, finiteTrafficAggregateMigration: true}
	previous := 0
	for _, m := range chain {
		if m.id <= previous || m.id > 999999 || !migrationName.MatchString(m.name) || m.apply == nil {
			return fmt.Errorf("invalid migration %s: IDs must be positive, unique and increasing", m.version())
		}
		previous = m.id
		known[m.version()] = true
	}
	for _, m := range chain {
		err := s.migrationTransaction(func(tx *sql.Tx) error {
			if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
				return err
			}
			rows, err := tx.Query(`SELECT version FROM schema_migrations`)
			if err != nil {
				return err
			}
			applied := false
			for rows.Next() {
				var version string
				if err := rows.Scan(&version); err != nil {
					rows.Close()
					return err
				}
				if !known[version] {
					rows.Close()
					return fmt.Errorf("unknown schema version %q; use a compatible binary or restore its pre-update snapshot", version)
				}
				if version == m.version() {
					applied = true
				}
			}
			if err := rows.Close(); err != nil {
				return err
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if applied {
				return nil
			}
			if err := m.apply(tx); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, m.version(), time.Now().Unix()); err != nil {
				return fmt.Errorf("record version: %w", err)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", m.version(), err)
		}
	}
	return nil
}

func (s *Store) migrationTransaction(apply func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := apply(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateSettingsCache()
	return nil
}

// Only this compatibility baseline needs conditional DDL. Inspect SQLite's
// catalog rather than matching error messages: unknown SQL/IO errors must stop
// startup, not be mistaken for an already-applied statement.
func execLegacyStatement(tx *sql.Tx, stmt string) error {
	words := strings.Fields(stmt)
	if len(words) >= 6 && words[0] == "ALTER" && words[1] == "TABLE" {
		table, column := words[2], words[5]
		var tableExists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&tableExists); err != nil {
			return err
		}
		if tableExists != 1 {
			return fmt.Errorf("missing legacy table %q", table)
		}
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&exists); err != nil {
			return err
		}
		switch words[3] {
		case "ADD":
			if exists > 0 {
				return nil
			}
		case "RENAME":
			if len(words) != 8 || words[6] != "TO" {
				return fmt.Errorf("invalid legacy rename: %s", stmt)
			}
			var target int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, words[7]).Scan(&target); err != nil {
				return err
			}
			if exists == 0 && target == 1 {
				return nil
			}
			if exists == 0 || target > 0 {
				return fmt.Errorf("ambiguous legacy rename %s: source=%d target=%d", stmt, exists, target)
			}
		default:
			return fmt.Errorf("unsupported legacy ALTER: %s", stmt)
		}
	}
	_, err := tx.Exec(stmt)
	return err
}
