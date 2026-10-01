package store

import (
	"testing"
)

// TestMigrateAddsNotifyOpsToAnOlderDB walks the upgrade path this column
// actually takes: a database created before it existed.
//
// The panel migrates itself on boot, so a migration that fails here does not
// fail a test — it fails a running panel, which then cannot start to be fixed
// from its own update page. Dropping the column reproduces the old shape
// exactly, rather than trusting that an ALTER guarded by "duplicate column
// name" must therefore work on a table that lacks it.
func TestMigrateAddsNotifyOpsToAnOlderDB(t *testing.T) {
	st := openMigrated(t)

	uid, err := st.CreateUser(NewUser{Username: "legacy", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindTelegram(uid, 555, 555, "legacy_tg", "Legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`ALTER TABLE telegram_binds DROP COLUMN notify_ops`); err != nil {
		t.Fatalf("could not reproduce the pre-upgrade schema: %v", err)
	}

	rewindVersionedBaseline(t, st)
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrating a pre-upgrade database failed: %v", err)
	}

	// The binding survives, and defaults to NOT receiving ops alerts: upgrading
	// must never start sending node failure details to someone who only ever
	// bound Telegram for their own expiry notices.
	b, err := st.TelegramBindByUser(uid)
	if err != nil || b == nil {
		t.Fatalf("binding lost across the upgrade: %v %v", b, err)
	}
	if b.NotifyOps {
		t.Fatal("an existing binding was opted into ops alerts by the upgrade")
	}

	// And the new flag works on the migrated row.
	if bound, err := st.SetTelegramNotifyOps(uid, true); err != nil || !bound {
		t.Fatalf("flagging a migrated binding: bound=%v err=%v", bound, err)
	}
	rcpts, err := st.ListOpsRecipients()
	if err != nil || len(rcpts) != 1 || rcpts[0].ChatID != 555 {
		t.Fatalf("migrated binding not usable as a recipient: %v %v", rcpts, err)
	}
}
