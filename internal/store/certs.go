package store

import (
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// Cert is a managed TLS certificate — a first-class, reusable resource that
// many sb_tls (mode=tls) profiles reference by id. cert_pem/key_pem are stored
// encrypted at rest and returned decrypted from these methods.
type Cert struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Domain        string `json:"domain"`
	Source        string `json:"source"`       // acme | paste | selfsigned
	AcmeMethod    string `json:"acme_method"`  // dns-cf | http-01 | webroot
	AcmeProfile   string `json:"acme_profile"` // empty legacy | auto | compatible | modern
	CertPEM       string `json:"cert_pem"`
	KeyPEM        string `json:"key_pem"`
	NotAfter      int64  `json:"not_after"`
	ActualKeyType string `json:"actual_key_type"`
	ActualChain   string `json:"actual_chain"`
	LastVerifyAt  int64  `json:"last_verify_at"`
	VerifyError   string `json:"verify_error"`
	AutoRenew     bool   `json:"auto_renew"`
	LastRenewAt   int64  `json:"last_renew_at"`
	LastError     string `json:"last_error"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
	// DecryptFailed is set when cert_pem/key_pem were stored encrypted but could
	// not be decrypted (wrong/changed QZ_SECRET_KEY). The config builder MUST
	// refuse to emit an inbound referencing such a cert rather than downgrade it
	// to plaintext — see resolveTlsBlock.
	DecryptFailed bool `json:"-"`
}

// certNotAfter parses a fullchain/leaf PEM and returns the leaf's expiry (unix),
// or 0 when the PEM is unparseable. The leaf is the first CERTIFICATE block.
func certNotAfter(certPEM string) int64 {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return 0
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return 0
	}
	return c.NotAfter.Unix()
}

func (s *Store) scanCert(row interface{ Scan(...any) error }) (*Cert, error) {
	var c Cert
	var autoRenew int
	if err := row.Scan(&c.ID, &c.Name, &c.Domain, &c.Source, &c.AcmeMethod, &c.AcmeProfile,
		&c.CertPEM, &c.KeyPEM, &c.NotAfter, &c.ActualKeyType, &c.ActualChain,
		&c.LastVerifyAt, &c.VerifyError, &autoRenew, &c.LastRenewAt, &c.LastError,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.AutoRenew = autoRenew != 0
	cp, ok1 := s.decryptOK(c.CertPEM)
	kp, ok2 := s.decryptOK(c.KeyPEM)
	c.CertPEM, c.KeyPEM = cp, kp
	c.DecryptFailed = !ok1 || !ok2
	return &c, nil
}

const certCols = `id, name, domain, source, acme_method, acme_profile, cert_pem, key_pem, not_after, actual_key_type, actual_chain, last_verify_at, verify_error, auto_renew, last_renew_at, last_error, created_at, updated_at`

func (s *Store) ListCerts() ([]*Cert, error) {
	rows, err := s.db.Query(`SELECT ` + certCols + ` FROM certificates ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Cert{}
	for rows.Next() {
		c, err := s.scanCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCert(id int64) (*Cert, error) {
	c, err := s.scanCert(s.db.QueryRow(`SELECT `+certCols+` FROM certificates WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// SaveCert inserts (id==0) or updates a certificate. cert_pem/key_pem are
// encrypted; not_after is derived from cert_pem so the caller never sets it.
func (s *Store) SaveCert(c *Cert) (int64, error) {
	return s.saveCertWith(s.db, c)
}

func (s *Store) saveCertWith(db txLike, c *Cert) (int64, error) {
	now := time.Now().Unix()
	c.NotAfter = certNotAfter(c.CertPEM)
	encCert := s.encrypt(c.CertPEM)
	encKey := s.encrypt(c.KeyPEM)
	if c.ID == 0 {
		res, err := db.Exec(`INSERT INTO certificates
			(name, domain, source, acme_method, acme_profile, cert_pem, key_pem, not_after,
			 actual_key_type, actual_chain, last_verify_at, verify_error,
			 auto_renew, last_renew_at, last_error, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			c.Name, c.Domain, c.Source, c.AcmeMethod, c.AcmeProfile, encCert, encKey, c.NotAfter,
			c.ActualKeyType, c.ActualChain, c.LastVerifyAt, c.VerifyError,
			b2i(c.AutoRenew), c.LastRenewAt, c.LastError, now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := db.Exec(`UPDATE certificates SET
		name=?, domain=?, source=?, acme_method=?, acme_profile=?, cert_pem=?, key_pem=?, not_after=?,
		actual_key_type=?, actual_chain=?, last_verify_at=?, verify_error=?,
		auto_renew=?, last_renew_at=?, last_error=?, updated_at=?
		WHERE id=?`,
		c.Name, c.Domain, c.Source, c.AcmeMethod, c.AcmeProfile, encCert, encKey, c.NotAfter,
		c.ActualKeyType, c.ActualChain, c.LastVerifyAt, c.VerifyError,
		b2i(c.AutoRenew), c.LastRenewAt, c.LastError, now, c.ID)
	return c.ID, err
}

// SetCertRenewError records a renewal failure without touching the cert bytes,
// so a transient acme.sh error is visible in the UI but doesn't blank the cert.
func (s *Store) SetCertRenewError(id int64, msg string) error {
	_, err := s.db.Exec(`UPDATE certificates SET last_error=?, updated_at=? WHERE id=?`,
		msg, time.Now().Unix(), id)
	return err
}

// SetCertVerifyError records a rejected candidate without replacing the PEM
// currently used by live nodes.
func (s *Store) SetCertVerifyError(id int64, msg string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`UPDATE certificates SET verify_error=?, last_verify_at=?, updated_at=? WHERE id=?`,
		msg, now, now, id)
	return err
}

// DeleteCert refuses deletion while any sb_tls profile still references the cert:
// nulling it out would strip TLS from live inbounds. Mirrors DeleteSbTls.
func (s *Store) DeleteCert(id int64) error {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sb_tls WHERE cert_id=?`, id).Scan(&n)
	if n > 0 {
		return fmt.Errorf("%w：仍有 %d 个 TLS 配置在使用此证书", ErrInUse, n)
	}
	_, err := s.db.Exec(`DELETE FROM certificates WHERE id=?`, id)
	return err
}

// CertServerIDs returns the distinct sb_inbounds.server_id of every inbound
// whose TLS profile references this certificate. Used after a renewal to
// re-push config only to the servers that actually carry the cert.
func (s *Store) CertServerIDs(certID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT DISTINCT i.server_id
		FROM sb_inbounds i JOIN sb_tls t ON i.tls_id = t.id
		WHERE t.cert_id = ?`, certID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// backfillCerts extracts inline-PEM sb_tls (mode=tls) profiles into managed
// certificates rows and repoints them via cert_id. Idempotent: a profile is
// only migrated while cert_id is still 0, so a second run is a no-op.
func (s *Store) backfillCerts() error {
	return s.migrationTransaction(s.backfillCertsTx)
}

func (s *Store) backfillCertsTx(tx *sql.Tx) error {
	list, err := s.listSbTlsWith(tx)
	if err != nil {
		return err
	}
	migrated := 0
	for _, t := range list {
		if t.Mode != "tls" || t.CertID != 0 {
			continue
		}
		if t.DecryptFailed || strings.HasPrefix(t.ServerJSON, encPrefix) {
			return fmt.Errorf("TLS profile %d: cannot decrypt legacy certificate; check QZ_SECRET_KEY", t.ID)
		}
		if t.ServerJSON == "" {
			continue
		}
		var sj map[string]interface{}
		if json.Unmarshal([]byte(t.ServerJSON), &sj) != nil {
			continue
		}
		certPEM, _ := sj["certificate"].(string)
		keyPEM, _ := sj["key"].(string)
		if certPEM == "" || keyPEM == "" {
			continue // e.g. a path-based (certificate_path) profile — leave as is
		}
		domain, _ := sj["server_name"].(string)
		name := t.Name
		if name == "" {
			name = "迁移证书-" + domain
		}
		cid, err := s.saveCertWith(tx, &Cert{
			Name:      name,
			Domain:    domain,
			Source:    "paste", // provenance unknown at migration time
			CertPEM:   certPEM,
			KeyPEM:    keyPEM,
			AutoRenew: false, // pasted certs aren't ours to renew
		})
		if err != nil {
			return err
		}
		// Repoint the profile. Keep the inline PEM in server_json untouched so a
		// rollback still has a working cert; the builder prefers cert_id.
		t.CertID = cid
		if _, err := tx.Exec(`UPDATE sb_tls SET cert_id=?, updated_at=? WHERE id=?`, cid, time.Now().Unix(), t.ID); err != nil {
			return err
		}
		migrated++
	}
	if migrated > 0 {
		log.Printf("migrate: extracted %d inline-PEM TLS profiles into managed certificates", migrated)
	}
	return nil
}
