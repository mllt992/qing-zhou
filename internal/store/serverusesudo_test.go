package store

import "testing"

// use_sudo carries a one-time backfill, so it is added by its own transaction
// rather than by the best-effort ALTER list — that list re-runs on every boot.
//
// Both halves matter and they pull in opposite directions: the backfill has to
// reach rows that predate the column (a non-root row is broken without sudo), and
// it must never run a second time (an admin who turns sudo back off would have it
// re-enabled on every restart, forever). Rewinding a migrated DB is what makes
// the upgrade path reachable here; a test DB is otherwise built by CREATE TABLE
// and never takes it.
func TestMigrateServerUseSudo_BackfillsOnceOnly(t *testing.T) {
	st := openMigrated(t)

	if _, err := st.db.Exec(`ALTER TABLE servers DROP COLUMN use_sudo`); err != nil {
		t.Fatalf("rewinding the schema: %v", err)
	}
	for _, user := range []string{"root", "deploy"} {
		if _, err := st.db.Exec(
			`INSERT INTO servers (name, host, ssh_user, created_at, updated_at) VALUES (?,?,?,0,0)`,
			user+"-box", "10.0.0.1", user); err != nil {
			t.Fatalf("seeding a %s row: %v", user, err)
		}
	}

	rewindVersionedBaseline(t, st)
	if err := st.Migrate(); err != nil {
		t.Fatalf("upgrading a DB that predates use_sudo: %v", err)
	}

	useSudo := func(user string) int {
		t.Helper()
		var v int
		if err := st.db.QueryRow(`SELECT use_sudo FROM servers WHERE ssh_user=?`, user).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// A non-root row cannot mkdir /etc/sing-box or restart the unit, so it is
	// already failing every deploy — switching sudo on can only unbreak it.
	if got := useSudo("deploy"); got != 1 {
		t.Errorf("non-root row: use_sudo = %d, want 1 (backfill did not reach it)", got)
	}
	// root needs no sudo, and wrapping its commands would change what ships to
	// every existing installation.
	if got := useSudo("root"); got != 0 {
		t.Errorf("root row: use_sudo = %d, want 0", got)
	}

	// The admin decides this row does not want sudo after all.
	if _, err := st.db.Exec(`UPDATE servers SET use_sudo=0 WHERE ssh_user='deploy'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if got := useSudo("deploy"); got != 0 {
		t.Errorf("use_sudo = %d after the admin turned it off and the panel restarted, want 0 — "+
			"the backfill re-ran, which it must do exactly once", got)
	}
}

// The three new columns are read by serverCols on every server query, so a DB
// that predates them has to come out of Migrate with all three present.
func TestMigrateServer_SudoAndKeyPathColumnsSurviveUpgrade(t *testing.T) {
	st := openMigrated(t)
	for _, col := range []string{"use_sudo", "sudo_password", "ssh_key_path"} {
		if _, err := st.db.Exec(`ALTER TABLE servers DROP COLUMN ` + col); err != nil {
			t.Fatalf("rewinding %s: %v", col, err)
		}
	}
	rewindVersionedBaseline(t, st)
	if err := st.Migrate(); err != nil {
		t.Fatalf("upgrading: %v", err)
	}
	var n int
	if err := st.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('servers') WHERE name IN
		 ('use_sudo','sudo_password','ssh_key_path')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("only %d of the 3 columns came back; every server query selects all of them", n)
	}
	// Round-trip through the real accessors: a column present but missing from
	// serverCols/scanServer would still fail here.
	id, err := st.CreateServer(Server{
		Name: "n", Host: "h", SSHUser: "deploy",
		UseSudo: true, SudoPassword: "s3cr3t", SSHKeyPath: "deploy.key",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.GetServer(id)
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if !got.UseSudo || got.SudoPassword != "s3cr3t" || got.SSHKeyPath != "deploy.key" {
		t.Errorf("round-trip lost fields: use_sudo=%v sudo_password=%q ssh_key_path=%q",
			got.UseSudo, got.SudoPassword, got.SSHKeyPath)
	}
}

// The sudo password opens a root shell on the node, so it must be encrypted at
// rest exactly like the SSH password beside it — while the key file NAME must
// not be, or an admin cannot see which key a failing row points at.
func TestServerSudoPasswordEncryptedAtRest(t *testing.T) {
	st := openMigrated(t)
	st.SetSecretKey([]byte("test-secret"))
	id, err := st.CreateServer(Server{Name: "n", Host: "h", SudoPassword: "hunter2", SSHKeyPath: "deploy.key"})
	if err != nil {
		t.Fatal(err)
	}
	var stored, path string
	if err := st.db.QueryRow(`SELECT sudo_password, ssh_key_path FROM servers WHERE id=?`, id).Scan(&stored, &path); err != nil {
		t.Fatal(err)
	}
	if stored == "hunter2" {
		t.Error("sudo_password is stored in plaintext")
	}
	if path != "deploy.key" {
		t.Errorf("ssh_key_path = %q, want it stored as-is (not a secret)", path)
	}
}
