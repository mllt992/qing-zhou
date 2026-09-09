package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

const encPrefix = "enc:v1:"

// encKeys are settings stored encrypted at rest.
var encKeys = map[string]bool{"oauth2_config": true, "smtp_pass": true, "cf_api_token": true, "telegram_bot_token": true}

// SetSecretKey derives the AES key used to encrypt secret settings. Pass
// QZ_SECRET_KEY (recommended, kept outside the DB) or fall back to jwt_secret.
func (s *Store) SetSecretKey(raw []byte) {
	h := sha256.Sum256(raw)
	s.secretKey = h[:]
}

// encrypt returns the ciphertext for a secret setting. It fails CLOSED: on any
// cipher/RNG error it returns "" rather than the plaintext, so a failure can
// never silently persist a secret in cleartext. (With a 32-byte key these error
// paths are effectively unreachable, but the behavior must be safe if they hit.)
func (s *Store) encrypt(plain string) string {
	if len(s.secretKey) == 0 || plain == "" || strings.HasPrefix(plain, encPrefix) {
		return plain
	}
	block, err := aes.NewCipher(s.secretKey)
	if err != nil {
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ""
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ""
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(ct)
}

// decryptOK returns the plaintext of an encrypted setting and reports whether
// decryption succeeded. Non-prefixed values are legacy cleartext and returned
// as-is with ok=true. It fails CLOSED: once the enc:v1: prefix is present, any
// decode/decrypt failure returns ("", false) instead of handing the raw
// ciphertext blob back to the caller. Callers that must not silently downgrade a
// secret to empty (e.g. the sing-box config builder, which would otherwise emit
// a plaintext inbound) MUST check ok — see decrypt for the lossy convenience form.
func (s *Store) decryptOK(val string) (string, bool) {
	if len(s.secretKey) == 0 || !strings.HasPrefix(val, encPrefix) {
		return val, true
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(val, encPrefix))
	if err != nil {
		return "", false
	}
	block, err := aes.NewCipher(s.secretKey)
	if err != nil {
		return "", false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(raw) < gcm.NonceSize() {
		return "", false
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", false
	}
	return string(pt), true
}

// CountUndecryptableSecrets reports how many at-rest encrypted values fail to
// decrypt with the current key. It is a startup self-check for a wrong/changed
// QZ_SECRET_KEY: when non-zero, affected sing-box TLS inbounds refuse to deploy
// (never downgraded to plaintext) and SMTP silently stops, so the operator must
// be told loudly rather than left to debug "nodes mysteriously down".
func (s *Store) CountUndecryptableSecrets() int {
	n := 0
	if rows, err := s.db.Query(`SELECT server_json FROM sb_tls`); err == nil {
		for rows.Next() {
			var v string
			if rows.Scan(&v) == nil {
				if _, ok := s.decryptOK(v); !ok {
					n++
				}
			}
		}
		rows.Close()
	}
	// Managed certificates hold encrypted cert_pem/key_pem; an undecryptable one
	// makes its referencing inbounds refuse to build (never plaintext), so count
	// it the same way as sb_tls above.
	if rows, err := s.db.Query(`SELECT cert_pem, key_pem FROM certificates`); err == nil {
		for rows.Next() {
			var cp, kp string
			if rows.Scan(&cp, &kp) == nil {
				if _, ok := s.decryptOK(cp); !ok {
					n++
				} else if _, ok := s.decryptOK(kp); !ok {
					n++
				}
			}
		}
		rows.Close()
	}
	for key := range encKeys {
		if raw, ok, err := s.cachedSettingRaw(key); err == nil && ok {
			if _, dok := s.decryptOK(raw); !dok {
				n++
			}
		}
	}
	return n
}

// decrypt is the lossy convenience form of decryptOK: a decrypt failure is
// indistinguishable from a genuinely empty value. Do NOT use it where an
// undecryptable secret must not be treated as "no value" — use decryptOK there.
func (s *Store) decrypt(val string) string {
	pt, _ := s.decryptOK(val)
	return pt
}
