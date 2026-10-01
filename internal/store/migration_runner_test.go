package store

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openUnmigrated(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func loadMigrationFixture(t *testing.T, st *Store, name string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "migrations", name+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(string(content)); err != nil {
		t.Fatal(err)
	}
}

func scalar(t *testing.T, st *Store, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := st.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestVersionedMigrationHistoricalFixtures(t *testing.T) {
	for _, fixture := range []string{"empty", "pre_bucket_users", "pre_notification_channels"} {
		t.Run(fixture, func(t *testing.T) {
			st := openUnmigrated(t)
			if fixture != "empty" {
				loadMigrationFixture(t, st, fixture)
			}
			for pass := 0; pass < 2; pass++ {
				if err := st.Migrate(); err != nil {
					t.Fatalf("pass %d: %v", pass, err)
				}
				if n := scalar(t, st, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, legacyBaselineVersion); n != 1 {
					t.Fatalf("baseline markers=%d", n)
				}
				var check string
				if err := st.db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
					t.Fatalf("integrity: %s %v", check, err)
				}
				if fixture == "pre_bucket_users" {
					if n := scalar(t, st, `SELECT email_gate_exempt FROM users WHERE id=1`); n != 1 {
						t.Fatal("legacy provisioned account lost exemption")
					}
					if n := scalar(t, st, `SELECT COUNT(*) FROM user_plans WHERE user_id=1`); n != 2 {
						t.Fatalf("legacy pool/free buckets=%d", n)
					}
					if n := scalar(t, st, `SELECT COUNT(*) FROM user_plans WHERE user_id=2`); n != 0 {
						t.Fatal("unprovisioned account gained access")
					}
					if n := scalar(t, st, `SELECT traffic_limit FROM user_plans WHERE user_id=1 AND kind='pool'`); n != 1000 {
						t.Fatalf("lost quota %d", n)
					}
				}
				if fixture == "pre_notification_channels" {
					if n := scalar(t, st, `SELECT COUNT(*) FROM manual_notification_recipients WHERE notification_id=7 AND channel='telegram' AND status='sent' AND sent_at=11`); n != 1 {
						t.Fatal("lost historic notification state")
					}
					if _, err := st.db.Exec(`INSERT OR IGNORE INTO manual_notification_recipients(notification_id,user_id,channel) VALUES(7,42,'email')`); err != nil {
						t.Fatal(err)
					}
					if n := scalar(t, st, `SELECT COUNT(*) FROM manual_notification_recipients WHERE notification_id=7`); n != 2 {
						t.Fatal("channel absent from primary key")
					}
				}
			}
		})
	}
}

func TestVersionedMigrationAtomicFailureStages(t *testing.T) {
	for _, stage := range []string{"ddl", "backfill", "index", "version"} {
		t.Run(stage, func(t *testing.T) {
			st := openUnmigrated(t)
			if _, err := st.db.Exec(`CREATE TABLE original(id INTEGER PRIMARY KEY, value INTEGER); INSERT INTO original VALUES(1,10); CREATE TABLE schema_migrations(version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			if stage == "version" {
				if _, err := st.db.Exec(`CREATE TRIGGER deny_version BEFORE INSERT ON schema_migrations BEGIN SELECT RAISE(ABORT,'fixture version write failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			m := migration{1, "failure_fixture", func(tx *sql.Tx) error {
				if _, err := tx.Exec(`ALTER TABLE original ADD COLUMN added TEXT; CREATE TABLE new_table(id INTEGER)`); err != nil {
					return err
				}
				if stage == "ddl" {
					_, err := tx.Exec(`ALTER TABLE missing_table ADD COLUMN broken TEXT`)
					return err
				}
				if _, err := tx.Exec(`UPDATE original SET value=99`); err != nil {
					return err
				}
				if stage == "backfill" {
					_, err := tx.Exec(`UPDATE original SET nonexistent=1`)
					return err
				}
				if stage == "index" {
					_, err := tx.Exec(`CREATE INDEX fail_index ON original(nonexistent)`)
					return err
				}
				return nil
			}}
			err := st.runMigrations([]migration{m})
			if err == nil || !strings.Contains(err.Error(), "000001_failure_fixture") {
				t.Fatalf("missing contextual error: %v", err)
			}
			if n := scalar(t, st, `SELECT COUNT(*) FROM pragma_table_info('original') WHERE name='added'`); n != 0 {
				t.Fatal("DDL leaked")
			}
			if n := scalar(t, st, `SELECT value FROM original`); n != 10 {
				t.Fatal("partial backfill committed")
			}
			if n := scalar(t, st, `SELECT COUNT(*) FROM sqlite_schema WHERE name='new_table'`); n != 0 {
				t.Fatal("new table leaked")
			}
			if n := scalar(t, st, `SELECT COUNT(*) FROM schema_migrations`); n != 0 {
				t.Fatal("failed version recorded")
			}
		})
	}
}

func TestLegacyBaselineFailureRollsBackAllPhases(t *testing.T) {
	for _, stage := range []string{"backfill", "index", "version"} {
		t.Run(stage, func(t *testing.T) {
			st := openUnmigrated(t)
			loadMigrationFixture(t, st, "pre_bucket_users")
			if _, err := st.db.Exec(`CREATE TABLE schema_migrations(version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "backfill":
				_, err := st.db.Exec(`CREATE TRIGGER deny_classification BEFORE UPDATE ON users BEGIN SELECT RAISE(ABORT,'fixture backfill failure'); END`)
				if err != nil {
					t.Fatal(err)
				}
			case "index":
				_, err := st.db.Exec(`ALTER TABLE users ADD COLUMN proxy_username TEXT NOT NULL DEFAULT ''; UPDATE users SET proxy_username='duplicate'`)
				if err != nil {
					t.Fatal(err)
				}
			case "version":
				_, err := st.db.Exec(`CREATE TRIGGER deny_version BEFORE INSERT ON schema_migrations WHEN NEW.version='000001_legacy_baseline' BEGIN SELECT RAISE(ABORT,'fixture version failure'); END`)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Migrate(); err == nil || !strings.Contains(err.Error(), legacyBaselineVersion) {
				t.Fatalf("unexpected error: %v", err)
			}
			if n := scalar(t, st, `SELECT COUNT(*) FROM pragma_table_info('users') WHERE name='sui_client_id'`); n != 1 {
				t.Fatal("rename committed before failed version")
			}
			if n := scalar(t, st, `SELECT COUNT(*) FROM pragma_table_info('users') WHERE name='email_gate_exempt'`); n != 0 {
				t.Fatal("classification column leaked")
			}
			if n := scalar(t, st, `SELECT COUNT(*) FROM sqlite_schema WHERE name='user_plans'`); n != 0 {
				t.Fatal("partial buckets committed")
			}
			if n := scalar(t, st, `SELECT COUNT(*) FROM schema_migrations`); n != 0 {
				t.Fatal("partial historical markers committed")
			}
			// Correct only the corrupt fixture/failure, then the same migration retries.
			if _, err := st.db.Exec(`DROP TRIGGER IF EXISTS deny_classification; DROP TRIGGER IF EXISTS deny_version`); err != nil {
				t.Fatal(err)
			}
			if stage == "index" {
				if _, err := st.db.Exec(`UPDATE users SET proxy_username=''`); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Migrate(); err != nil {
				t.Fatalf("retry: %v", err)
			}
		})
	}
}

func TestVersionedMigrationRunsOnceAndSerializes(t *testing.T) {
	st := openUnmigrated(t)
	m := migration{1, "once", func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE once_only(value INTEGER); INSERT INTO once_only VALUES(1)`)
		return err
	}}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- st.runMigrations([]migration{m}) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := scalar(t, st, `SELECT COUNT(*) FROM once_only`); n != 1 {
		t.Fatalf("runs=%d", n)
	}
	if n := scalar(t, st, `SELECT COUNT(*) FROM schema_migrations`); n != 1 {
		t.Fatalf("markers=%d", n)
	}
}

func TestVersionedMigrationRejectsUnknownAndBadOrdering(t *testing.T) {
	st := openMigrated(t)
	if _, err := st.db.Exec(`INSERT INTO schema_migrations VALUES('999999_future',1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err == nil || !strings.Contains(err.Error(), "999999_future") {
		t.Fatalf("unknown schema: %v", err)
	}
	for _, ids := range [][]int{{0}, {2, 1}, {1, 1}} {
		chain := []migration{}
		for _, id := range ids {
			chain = append(chain, migration{id, "test", func(*sql.Tx) error { return nil }})
		}
		if err := st.runMigrations(chain); err == nil {
			t.Fatalf("accepted IDs %v", ids)
		}
	}
}

func TestVersionedMigrationReportsClosedDatabase(t *testing.T) {
	st := openUnmigrated(t)
	st.Close()
	if err := st.Migrate(); err == nil || !strings.Contains(err.Error(), legacyBaselineVersion) {
		t.Fatalf("missing I/O context: %v", err)
	}
}

func TestLegacyBaselinePreservesOneShotDecisions(t *testing.T) {
	st := openMigrated(t)
	id, err := st.CreateUser(NewUser{Username: "post-upgrade", PasswordHash: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE users SET client_id=17,email_gate_exempt=0 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO user_credential_aliases(user_id,source_name,client_uuid,client_secret,valid_until,created_at) VALUES(?,'old','fixture-uuid','fixture-secret',4102444800,1)`, id); err != nil {
		t.Fatal(err)
	}
	rewindVersionedBaseline(t, st)
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, st, `SELECT COUNT(*) FROM user_plans WHERE user_id=?`, id); n != 0 {
		t.Fatal("adoption resurrected a deprovisioned account")
	}
	if n := scalar(t, st, `SELECT email_gate_exempt FROM users WHERE id=?`, id); n != 0 {
		t.Fatal("adoption reclassified a post-upgrade user")
	}
	if n := scalar(t, st, `SELECT valid_until FROM user_credential_aliases WHERE user_id=? AND source_name='old'`, id); n != 4102444800 {
		t.Fatal("adoption extended credential grace")
	}
}

func TestLegacyStatementUnknownSQLErrorIsFatal(t *testing.T) {
	st := openMigrated(t)
	err := st.migrationTransaction(func(tx *sql.Tx) error { return execLegacyStatement(tx, `UPDATE users SET nonexistent_column=1`) })
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ignored unknown SQL error: %v", err)
	}
}

func TestLegacyBaselineResumesPartialBucketBackfill(t *testing.T) {
	st := openUnmigrated(t)
	loadMigrationFixture(t, st, "pre_bucket_users")
	// Simulate a killed older release: DDL done, only one other user seeded.
	if _, err := st.db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO user_plans(user_id,kind,name,client_name,client_uuid,client_secret,created_at,updated_at) VALUES(99,'pool','other user','other_client','','',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, st, `SELECT COUNT(*) FROM user_plans WHERE user_id=1 AND kind='pool' AND traffic_limit=1000`); n != 1 {
		t.Fatal("remaining legacy account was skipped")
	}
	if n := scalar(t, st, `SELECT COUNT(*) FROM user_plans WHERE user_id=99`); n != 1 {
		t.Fatal("already-seeded account changed")
	}
}

func TestLegacyBaselineRepairsPartialNotificationDDL(t *testing.T) {
	st := openMigrated(t)
	// Child table is already migrated but parent was not: old early-return
	// logic incorrectly treated the child's PK as proof the whole unit was done.
	if _, err := st.db.Exec(`ALTER TABLE manual_notifications DROP COLUMN channel`); err != nil {
		t.Fatal(err)
	}
	rewindVersionedBaseline(t, st)
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, st, `SELECT COUNT(*) FROM pragma_table_info('manual_notifications') WHERE name='channel'`); n != 1 {
		t.Fatal("parent channel missing")
	}
}

func TestLegacyBaselineDecryptsBeforeSnapshotAdoption(t *testing.T) {
	for _, externalKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "jwt_fallback", true: "configured_key"}[externalKey], func(t *testing.T) {
			st := openMigrated(t)
			fixtureKey := []byte("fixture-encryption-key")
			st.SetSecretKey(fixtureKey)
			if !externalKey {
				if err := st.SetSetting("jwt_secret", string(fixtureKey)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.db.Exec(`INSERT INTO servers(name,host,port,ssh_user,probe_token,probe_token_hash,created_at,updated_at) VALUES('fixture','example.com',22,'root',?,'',1,1)`, st.encrypt("fixture-probe-token")); err != nil {
				t.Fatal(err)
			}
			_, err := st.SaveSbTls(&SbTls{Name: "legacy encrypted", Mode: "tls", ServerJSON: `{"certificate":"fixture-cert","key":"fixture-private-key"}`, ClientJSON: "{}"})
			if err != nil {
				t.Fatal(err)
			}
			rewindVersionedBaseline(t, st)
			if !externalKey {
				st.secretKey = nil
			} // exactly startup before Seed
			if err := st.Migrate(); err != nil {
				t.Fatal(err)
			}
			var hash string
			if err := st.db.QueryRow(`SELECT probe_token_hash FROM servers`).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			if hash != hashProbeToken("fixture-probe-token") {
				t.Fatal("hashed ciphertext instead of token")
			}
			if n := scalar(t, st, `SELECT COUNT(*) FROM sb_tls WHERE cert_id>0`); n != 1 {
				t.Fatal("encrypted inline certificate was skipped")
			}
			st.SetSecretKey(fixtureKey)
			certs, err := st.ListCerts()
			if err != nil || len(certs) != 1 || certs[0].KeyPEM != "fixture-private-key" {
				t.Fatalf("certificate not preserved: %v %v", certs, err)
			}
		})
	}
}

func TestLegacyBaselineWrongSecretFailsAtomically(t *testing.T) {
	st := openMigrated(t)
	st.SetSecretKey([]byte("fixture-correct-key"))
	_, err := st.SaveSbTls(&SbTls{Name: "encrypted", Mode: "tls", ServerJSON: `{"certificate":"fixture-cert","key":"fixture-private-key"}`, ClientJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	rewindVersionedBaseline(t, st)
	st.SetSecretKey([]byte("fixture-wrong-key"))
	if err := st.Migrate(); err == nil || !strings.Contains(err.Error(), "cannot decrypt") || !strings.Contains(err.Error(), legacyBaselineVersion) {
		t.Fatalf("wrong key was ignored: %v", err)
	}
	if n := scalar(t, st, `SELECT COUNT(*) FROM certificates`); n != 0 {
		t.Fatal("partial certificate migrated")
	}
	if n := scalar(t, st, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, legacyBaselineVersion); n != 0 {
		t.Fatal("failed version marked complete")
	}
}

// An abrupt process exit bypasses every defer. SQLite must still roll back
// transactional DDL/data and leave the version retryable on the next startup.
func TestVersionedMigrationProcessCrash(t *testing.T) {
	if path := os.Getenv("QZ_TEST_CRASH_DB"); path != "" {
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		err = st.runMigrations([]migration{{1, "crash_fixture", func(tx *sql.Tx) error {
			if _, err := tx.Exec(`ALTER TABLE original ADD COLUMN added TEXT; UPDATE original SET value=99`); err != nil {
				return err
			}
			os.Exit(23)
			return nil
		}}})
		t.Fatalf("expected child crash, got %v", err)
	}
	st := openUnmigrated(t)
	if _, err := st.db.Exec(`CREATE TABLE original(value INTEGER); INSERT INTO original VALUES(10)`); err != nil {
		t.Fatal(err)
	}
	path := st.Path()
	st.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestVersionedMigrationProcessCrash$")
	command.Env = append(os.Environ(), "QZ_TEST_CRASH_DB="+path)
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("child exit: %v, %s", err, output)
	}
	recovered, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if n := scalar(t, recovered, `SELECT value FROM original`); n != 10 {
		t.Fatal("crash committed data")
	}
	if n := scalar(t, recovered, `SELECT COUNT(*) FROM pragma_table_info('original') WHERE name='added'`); n != 0 {
		t.Fatal("crash committed DDL")
	}
	if n := scalar(t, recovered, `SELECT COUNT(*) FROM sqlite_schema WHERE name='schema_migrations'`); n != 0 {
		t.Fatal("crash committed migration bookkeeping")
	}
	if err := recovered.runMigrations([]migration{{1, "crash_fixture", func(tx *sql.Tx) error {
		_, err := tx.Exec(`ALTER TABLE original ADD COLUMN added TEXT; UPDATE original SET value=99`)
		return err
	}}}); err != nil {
		t.Fatalf("retry after process crash: %v", err)
	}
}
