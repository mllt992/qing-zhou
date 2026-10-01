package store

import "testing"

// A DB written by a release that predates users.remark must survive the upgrade
// with its rows intact, and the note must be readable on them afterwards. This
// is the shape /internal/store/migrate_test.go guards generally; it is repeated
// per new column because the failure mode is a boot loop on a real deployment,
// not a red test.
func TestMigrate_UpgradesDBMissingRemark(t *testing.T) {
	st := openMigrated(t)
	uid, err := st.CreateUser(NewUser{Username: "old", PasswordHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`ALTER TABLE users DROP COLUMN remark`); err != nil {
		t.Fatalf("rewinding the schema: %v", err)
	}

	rewindVersionedBaseline(t, st)
	if err := st.Migrate(); err != nil {
		t.Fatalf("upgrading a DB that predates users.remark failed: %v", err)
	}

	u, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("the pre-existing user did not survive the upgrade")
	}
	// The default has to be "" and not NULL: scanUser reads it into a string.
	if u.Remark != "" {
		t.Errorf("a user from before the column should read back an empty note, got %q", u.Remark)
	}
	if err := st.SetUserRemark(uid, "升级后写的备注"); err != nil {
		t.Fatalf("column present but unusable: %v", err)
	}
	u, _ = st.UserByID(uid)
	if u.Remark != "升级后写的备注" {
		t.Errorf("remark = %q, want the note just written", u.Remark)
	}
}

// The note is searchable. An admin who writes "公司同事张三" on an account looks
// for it by that, not by the login name they have already forgotten.
func TestListUsers_SearchesRemark(t *testing.T) {
	st := openMigrated(t)
	hit, err := st.CreateUser(NewUser{Username: "u1", PasswordHash: "h", Remark: "公司同事张三"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(NewUser{Username: "u2", PasswordHash: "h", Remark: "个人小号"}); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListUsers("张三", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != hit {
		t.Fatalf("searching the remark returned %d users, want just the one noted 公司同事张三", len(got))
	}

	// Clearing it must actually remove it from the index the search reads.
	if err := st.SetUserRemark(hit, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ListUsers("张三", 50); len(got) != 0 {
		t.Errorf("a cleared remark is still searchable: %d hits", len(got))
	}
}
