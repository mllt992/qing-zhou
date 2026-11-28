// [INPUT]: SQLite migration transaction and the configured encryption key.
// [OUTPUT]: Re-encrypts credentials protected by the retired Compose example.
// [POS]: One-time migration for stored secrets.
// [PROTOCOL]: Update this header on change, then check AGENTS.md
package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"

	"qingzhou/internal/config"
)

func (s *Store) replacePublicComposeKey(tx *sql.Tx) error {
	return s.rewrapComposeSecrets(tx, false)
}

// During adoption an older schema can lack columns and its data backfills need
// the original plaintext. Rewrap existing columns before those backfills run.
func (s *Store) rewrapComposeSecrets(tx *sql.Tx, historical bool) error {
	old := &Store{}
	old.SetSecretKey([]byte(config.LegacyComposeKey))
	migrated, unreadable := false, false
	for table, columns := range map[string][]string{
		"settings": {"value"}, "certificates": {"cert_pem", "key_pem"},
		"sb_tls": {"server_json"}, "sb_egresses": {"password"},
		"servers":      {"ssh_key", "ssh_key_pass", "ssh_password", "sudo_password", "probe_token"},
		"oauth_states": {"verifier"}, "relay_metering_links": {"credential"},
		"relay_metering_generations": {"credential"}, "relay_metering_users": {"credential"},
	} {
		for _, column := range columns {
			if historical {
				var exists bool
				if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name=?)", table, column).Scan(&exists); err != nil {
					return err
				}
				if !exists {
					continue
				}
			}
			// The names above are fixed schema identifiers. Collect replacements
			// before writing so the scan cannot revisit a modified SQLite row.
			fieldKey := "''"
			if table == "settings" {
				fieldKey = "key"
			}
			rows, err := tx.Query("SELECT rowid, " + fieldKey + ", " + column + " FROM " + table + " WHERE " + column + " GLOB 'enc:v1:*'")
			if err != nil {
				return err
			}
			replacements := map[int64]string{}
			for rows.Next() {
				var id int64
				var field, value string
				if err := rows.Scan(&id, &field, &value); err != nil {
					rows.Close()
					return err
				}
				if table == "settings" && !encKeys[field] {
					continue
				}
				plain, ok := old.decryptOK(value)
				if !ok {
					if _, valid := s.decryptOK(value); !valid {
						unreadable = true
					}
					continue
				}
				if len(s.secretKey) == 0 || bytes.Equal(s.secretKey, old.secretKey) {
					rows.Close()
					return errors.New("数据库使用旧版 Compose 示例密钥，请备份数据库并设置独立的 QZ_SECRET_KEY 后重试")
				}
				sealed := s.encrypt(plain)
				roundTrip, valid := s.decryptOK(sealed)
				if !valid || sealed == "" || roundTrip != plain {
					rows.Close()
					return fmt.Errorf("重新加密 %s.%s 失败", table, column)
				}
				replacements[id] = sealed
				migrated = true
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for id, sealed := range replacements {
				if _, err := tx.Exec("UPDATE "+table+" SET "+column+"=? WHERE rowid=?", sealed, id); err != nil {
					return err
				}
			}
		}
	}
	if migrated && unreadable {
		return errors.New("数据库包含无法使用原示例密钥或目标密钥解密的凭据，迁移已中止")
	}
	return nil
}
