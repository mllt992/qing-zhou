package store

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// openMigrated returns a Store on a throwaway file, migrated once.
func openMigrated(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// The v0.2.48 outage, as a test. That release added users.proxy_username via
// ALTER *and* put a unique index on it in the base schema, which runs first: on
// an existing DB the CREATE TABLE is a no-op, the index named a column that was
// not there yet, the whole schema Exec failed, and main log.Fatal'd. systemd
// restarted it into the same wall ~650 times. Every test passed the whole time,
// because a test DB is created by CREATE TABLE and therefore never takes the
// upgrade path.
//
// Rewinding a migrated DB is what makes that path reachable here: the assertion
// is not about these three columns, it is that a column added by ALTER is usable
// by the time the backfills run.
func TestMigrate_UpgradesDBMissingAlterAddedColumns(t *testing.T) {
	st := openMigrated(t)
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_users_proxy_username`,
		`ALTER TABLE users DROP COLUMN proxy_username`,
		`ALTER TABLE users DROP COLUMN proxy_password`,
		`ALTER TABLE users DROP COLUMN proxy_expires_at`,
	} {
		if _, err := st.db.Exec(stmt); err != nil {
			t.Fatalf("rewinding the schema with %q: %v", stmt, err)
		}
	}

	if err := st.Migrate(); err != nil {
		t.Fatalf("upgrading a DB that predates the column failed: %v", err)
	}

	// Re-added, not merely tolerated: the backfills and every user query read it.
	var n int
	if err := st.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('users') WHERE name IN
		 ('proxy_username','proxy_password','proxy_expires_at')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("users has %d of the 3 proxy columns after the upgrade", n)
	}
	if _, err := st.db.Exec(`SELECT proxy_username FROM users LIMIT 1`); err != nil {
		t.Errorf("column present but unusable: %v", err)
	}
}

// Migrating twice must be a no-op, which is what every restart after a normal
// upgrade actually does.
func TestMigrate_Idempotent(t *testing.T) {
	st := openMigrated(t)
	if err := st.Migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestMigrate_AddsAndBackfillsQueueKeys(t *testing.T) {
	st := openMigrated(t)
	uid := mkUser(t, st, "old-queue-schema")
	pkg := mkPlan(t, st, "旧套餐", 100, 100, 30)
	buy(t, st, uid, pkg)
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_user_plans_queue`,
		`ALTER TABLE user_plans DROP COLUMN queue_key`,
		`ALTER TABLE packages DROP COLUMN queue_key`,
	} {
		if _, err := st.db.Exec(stmt); err != nil {
			t.Fatalf("rewind queue schema with %q: %v", stmt, err)
		}
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("upgrade queue schema: %v", err)
	}
	var key string
	if err := st.db.QueryRow(`SELECT queue_key FROM user_plans WHERE user_id=? AND package_id=?`, uid, pkg.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	want := effectiveQueueKey(pkg.ID, "")
	if key != want {
		t.Fatalf("backfilled queue_key = %q, want %q", key, want)
	}
	if got, err := st.GetPackage(pkg.ID); err != nil || got == nil || got.QueueKey != "" {
		t.Fatalf("upgraded package = %+v, %v", got, err)
	}
}

func TestMigrate_AddsNodeGroupAIFlagWithoutChangingExistingGroups(t *testing.T) {
	st := openMigrated(t)
	if _, err := st.db.Exec(`INSERT INTO node_groups (name, description, is_ai, sort_order, created_at)
		VALUES ('旧分组', '', 0, 0, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`ALTER TABLE node_groups DROP COLUMN is_ai`); err != nil {
		t.Fatalf("rewind node_groups schema: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate old node_groups: %v", err)
	}
	groups, err := st.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].IsAI {
		t.Fatalf("legacy group was unexpectedly marked AI: %+v", groups)
	}
}

func TestMigrate_FreshDBDoesNotBackfillEmailGateExemption(t *testing.T) {
	st := openMigrated(t)
	uid, err := st.CreateUser(NewUser{Username: "freshdb", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	pkgID, err := st.CreatePackage(Package{Type: "plan", Name: "fresh plan", TrafficBytes: 1 << 30, DurationDays: 30, Stock: -1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pkg, _ := st.GetPackage(pkgID)
	if _, err := st.AssignPackage(uid, pkg, 0, func(*User, bool) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil { // fresh schema already had the column
		t.Fatal(err)
	}
	u, _ := st.UserByID(uid)
	if u == nil || u.EmailGateExempt {
		t.Fatal("fresh DB user was mistaken for a legacy exemption")
	}
}

func TestMigrate_EmailGateExemptionOnlyWhenColumnAdded(t *testing.T) {
	st := openMigrated(t)
	legacy, err := st.CreateUser(NewUser{Username: "legacy", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE users SET client_id=7 WHERE id=?`, legacy); err != nil {
		t.Fatal(err)
	}
	// Rewind only this feature: this is the exact pre-upgrade shape. The next
	// Migrate must both add the column and classify rows that already existed.
	if _, err := st.db.Exec(`ALTER TABLE users DROP COLUMN email_gate_exempt`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByID(legacy)
	if u == nil || !u.EmailGateExempt {
		t.Fatal("historical provisioned account was not preserved when column was added")
	}

	// This account and purchase happen after the column exists. A normal restart
	// runs Migrate again, but must not reinterpret the now-paid user as historical.
	fresh, err := st.CreateUser(NewUser{Username: "fresh", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	pkgID, err := st.CreatePackage(Package{Type: "plan", Name: "新购套餐", TrafficBytes: 1 << 30, DurationDays: 30, Stock: -1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pkg, _ := st.GetPackage(pkgID)
	if _, err := st.AssignPackage(fresh, pkg, 0, func(*User, bool) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil { // restart
		t.Fatal(err)
	}
	u, _ = st.UserByID(fresh)
	if u == nil || u.EmailGateExempt {
		t.Fatal("new unverified buyer became exempt after restart")
	}
}

func TestMigrate_ZeroConfigPreservesOnlyHistoricalPaidPackages(t *testing.T) {
	st := openMigrated(t)
	paid, err := st.CreatePackage(Package{Type: "plan", Name: "旧套餐", TrafficBytes: 1 << 30, DurationDays: 30, Stock: -1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(NewUser{Username: "paid", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetPackage(paid)
	if _, err := st.AssignPackage(uid, p, 0, func(*User, bool) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSbInbound(&SbInbound{Type: "vless", Tag: "legacy-in", Listen: "::", ListenPort: 443, Options: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// Historical zero-config installs did not need a nodes row: the old fallback
	// walked sb_inbounds directly. Also keep an external node around to prove the
	// compatibility group does not broaden the old self-built-only grant.
	externalID, err := st.CreateNode(Node{Type: "external", Name: "不应迁移", ShareLink: "ss://YWVzLTEyOC1nY206cGFzcw@example.com:8388", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM settings WHERE key='migrated_zero_config_v1'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	byTag, err := st.BuildUsersByTag(time.Now().Unix())
	if err != nil || len(byTag["legacy-in"]) == 0 {
		t.Fatalf("historical paid user lost zero-config access: %v %#v", err, byTag)
	}
	var externalMembership int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM node_group_members WHERE node_id=?`, externalID).Scan(&externalMembership); err != nil {
		t.Fatal(err)
	}
	if externalMembership != 0 {
		t.Fatal("zero-config migration broadened legacy access to an external node")
	}

	bare, err := st.CreateUser(NewUser{Username: "bare", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureWelcomeBucket(bare, "bare", 1<<30, time.Now().Unix()+86400); err != nil {
		t.Fatal(err)
	}
	gids, err := st.AccessibleGroupIDs(&User{ID: bare})
	if err != nil {
		t.Fatal(err)
	}
	if len(gids) != 0 {
		t.Fatalf("plan-less user inherited migrated all-node group: %v", gids)
	}

	newPkg, err := st.CreatePackage(Package{Type: "plan", Name: "新套餐", TrafficBytes: 1 << 30, DurationDays: 30, Stock: -1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if gs, _ := st.PlanGroupIDs(newPkg); len(gs) != 0 {
		t.Fatalf("package created after migration silently inherited all nodes: %v", gs)
	}
}

var schemaIndexStmt = regexp.MustCompile(`(?mi)^\s*CREATE\s+(UNIQUE\s+)?INDEX`)

// The invariant behind the fix, checked on the source rather than on behavior:
// index DDL lives in `indexes` (applied after the ALTER phase), never in
// `schema` (applied before it). Adding one to schema next to a brand-new column
// looks harmless — CREATE TABLE right above it declares the column — and is
// invisible until someone upgrades a real DB, which is exactly too late.
func TestMigrate_SchemaDeclaresNoIndexes(t *testing.T) {
	if loc := schemaIndexStmt.FindStringIndex(schema); loc != nil {
		line := strings.SplitN(strings.TrimSpace(schema[loc[0]:]), "\n", 2)[0]
		t.Errorf("schema declares an index: %q\nMove it to the `indexes` const —"+
			" schema runs before the ALTER TABLE phase, so an index there cannot"+
			" name a column that phase adds, and the failure is a boot loop.", line)
	}
	if !schemaIndexStmt.MatchString(indexes) {
		t.Error("`indexes` holds no index DDL — did it get emptied?")
	}
}
