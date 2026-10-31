package store

import "testing"

// #101: a database that recorded 000001_legacy_baseline before v0.2.89 must
// still receive the monthly-reset columns, and a database created by
// v0.2.89/v0.2.90 (columns already present, no 000011 marker) must not fail.
func TestMonthlyResetMigrationUpgradesExistingDatabases(t *testing.T) {
	st := openUnmigrated(t)
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	// Case 1: v0.2.89/v0.2.90 fresh install — columns exist, marker missing.
	if _, err := st.db.Exec(`DELETE FROM schema_migrations WHERE version='000011_monthly_reset'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("re-run with columns present: %v", err)
	}
	// Case 2: pre-v0.2.89 database — baseline applied, columns never added.
	for _, stmt := range []string{
		`DELETE FROM schema_migrations WHERE version='000011_monthly_reset'`,
		`DROP INDEX idx_plan_period_resets_user`,
		`DROP TABLE plan_period_resets`,
		`ALTER TABLE packages DROP COLUMN monthly_reset`,
		`ALTER TABLE packages DROP COLUMN monthly_bytes`,
		`ALTER TABLE packages DROP COLUMN allow_advance`,
		`ALTER TABLE user_plans DROP COLUMN period_days`,
		`ALTER TABLE user_plans DROP COLUMN period_quota`,
		`ALTER TABLE user_plans DROP COLUMN period_start`,
		`ALTER TABLE user_plans DROP COLUMN period_base`,
		`ALTER TABLE user_plans DROP COLUMN allow_advance`,
		`ALTER TABLE user_plans DROP COLUMN life_used_up`,
		`ALTER TABLE user_plans DROP COLUMN life_used_down`,
	} {
		if _, err := st.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	for _, c := range []struct{ table, column string }{
		{"packages", "monthly_reset"}, {"packages", "monthly_bytes"}, {"packages", "allow_advance"},
		{"user_plans", "period_days"}, {"user_plans", "period_quota"}, {"user_plans", "period_start"},
		{"user_plans", "period_base"}, {"user_plans", "allow_advance"},
		{"user_plans", "life_used_up"}, {"user_plans", "life_used_down"},
	} {
		if n := scalar(t, st, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, c.table, c.column); n != 1 {
			t.Fatalf("%s.%s missing after upgrade", c.table, c.column)
		}
	}
	if n := scalar(t, st, `SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('plan_period_resets','idx_plan_period_resets_user')`); n != 2 {
		t.Fatalf("plan_period_resets table/index missing: %d", n)
	}
	if n := scalar(t, st, `SELECT COUNT(*) FROM schema_migrations WHERE version='000011_monthly_reset'`); n != 1 {
		t.Fatalf("marker=%d", n)
	}
}
