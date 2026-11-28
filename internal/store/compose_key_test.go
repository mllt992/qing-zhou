// [INPUT]: Versioned SQLite fixtures with legacy encrypted settings.
// [OUTPUT]: Regression checks for key replacement and migration rollback.
// [POS]: Tests migration of the retired Compose key.
// [PROTOCOL]: Update this header on change, then check AGENTS.md
package store

import (
	"testing"

	"qingzhou/internal/config"
)

func TestPublicComposeKeyMigration(t *testing.T) {
	st := openUnmigrated(t)
	old := &Store{}
	old.SetSecretKey([]byte(config.LegacyComposeKey))
	if err := st.runMigrations(st.migrations()[:16]); err != nil {
		t.Fatal(err)
	}

	original := old.encrypt("mail-secret")
	if _, err := st.db.Exec("INSERT INTO settings(key,value) VALUES('smtp_pass',?)", original); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec("INSERT INTO certificates(name,cert_pem,key_pem,created_at,updated_at) VALUES('test',?,?,0,0)", old.encrypt("chain"), old.encrypt("private-key")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec("INSERT INTO email_tokens(user_id,token,purpose,expires_at,created_at) VALUES(1,'old-link','reset',9999999999,1)"); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err == nil {
		t.Fatal("migrated a legacy database without an independent key")
	}

	st.SetSecretKey([]byte("independent-key-for-test"))
	if _, err := st.db.Exec("CREATE TRIGGER reject_marker BEFORE INSERT ON schema_migrations WHEN NEW.version='000017_replace_public_compose_key' BEGIN SELECT RAISE(ABORT,'test failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err == nil {
		t.Fatal("failed migration reported success")
	}
	var persisted string
	if err := st.db.QueryRow("SELECT value FROM settings WHERE key='smtp_pass'").Scan(&persisted); err != nil || persisted != original {
		t.Fatal("old ciphertext was not preserved")
	}
	if _, err := st.db.Exec("DROP TRIGGER reject_marker"); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, st, "SELECT COUNT(*) FROM email_tokens WHERE token='old-link'"); n != 0 {
		t.Fatal("legacy addressless link still exists")
	}
	for query, want := range map[string]string{
		"SELECT value FROM settings WHERE key='smtp_pass'":    "mail-secret",
		"SELECT key_pem FROM certificates WHERE name='test'":  "private-key",
		"SELECT cert_pem FROM certificates WHERE name='test'": "chain",
	} {
		if err := st.db.QueryRow(query).Scan(&persisted); err != nil {
			t.Fatal(err)
		}
		if got, ok := st.decryptOK(persisted); !ok || got != want {
			t.Fatal("new key failed")
		}
		if _, ok := old.decryptOK(persisted); ok {
			t.Fatal("public key still works")
		}
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
}
