package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// hashProbeToken returns the hex SHA-256 of a probe token, used as the lookup
// key so the token itself can be stored encrypted at rest.
func hashProbeToken(tok string) string {
	if tok == "" {
		return ""
	}
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

type Server struct {
	ID        int64  `json:"id"`
	SortOrder int64  `json:"sort_order"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	// HostV6 是可选的 IPv6 订阅地址（IPv6 字面量或 AAAA 域名）。非空时订阅为该
	// 服务器的每个入站额外输出一条「原名-v6」节点；SSH 始终走 Host。
	HostV6      string `json:"host_v6"`
	Port        int    `json:"port"`
	SSHUser     string `json:"ssh_user"`
	SSHKey      string `json:"ssh_key"`
	SSHKeyPass  string `json:"ssh_key_pass"`
	SSHPassword string `json:"ssh_password"`
	ConfigPath  string `json:"config_path"`
	SystemdUnit string `json:"systemd_unit"`
	SingBoxBin  string `json:"sing_box_bin"`
	V2rayListen string `json:"v2ray_listen"`
	Enabled     bool   `json:"enabled"`
	Status      string `json:"status"`
	LastSeen    int64  `json:"last_seen"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	// Monitor probe fields
	ProbeEnabled bool   `json:"probe_enabled"`
	ProbeToken   string `json:"probe_token"`
	ExpiryDate   int64  `json:"expiry_date"`
	// Per-device expiry notification policy. Disabled by default so an upgrade
	// never starts sending asset details to existing ops recipients by surprise.
	ExpiryNotifyEnabled bool   `json:"expiry_notify_enabled"`
	ExpiryNotifyDays    int    `json:"expiry_notify_days"`
	ExpiryNotifyMode    string `json:"expiry_notify_mode"` // count | daily
	ExpiryNotifyCount   int    `json:"expiry_notify_count"`
	// Physical-interface monthly traffic budget. ResetMinute is minutes after
	// local midnight; ResetDay is clamped to the month's last day when needed.
	TrafficLimitBytes   int64 `json:"traffic_limit_bytes"`
	TrafficResetDay     int   `json:"traffic_reset_day"`
	TrafficResetMinute  int   `json:"traffic_reset_minute"`
	TrafficAlertPercent int   `json:"traffic_alert_percent"`
	// TrafficAccountingMode matches the provider's billing convention:
	// sum (IN+OUT), max (larger direction), rx (IN), or tx (OUT).
	TrafficAccountingMode string  `json:"traffic_accounting_mode"`
	Provider              string  `json:"provider"`
	Location              string  `json:"location"`
	Spec                  string  `json:"spec"`
	Price                 float64 `json:"price"`
	Notes                 string  `json:"notes"`
	// PublicVisible controls whether this machine is listed on the
	// unauthenticated status page. Independent of ProbeEnabled: an admin may
	// well want to watch a machine without announcing that it exists.
	PublicVisible bool `json:"public_visible"`
	// PublicShowTraffic and PublicShowPrice independently control which asset
	// details are disclosed once this machine is visible on the public page.
	PublicShowTraffic bool `json:"public_show_traffic"`
	PublicShowPrice   bool `json:"public_show_price"`
	// HostKey is the pinned SSH host key (authorized_keys line). Empty until the
	// first successful connection pins it (TOFU). Never exposed to the client.
	HostKey string `json:"-"`
	// UseSudo makes the panel prefix every privileged remote command with sudo.
	// Needed whenever SSHUser is not root: writing /etc/sing-box, installing the
	// config and restarting the unit all require root, so without this the whole
	// deploy path fails on a normal account.
	UseSudo bool `json:"use_sudo"`
	// SudoPassword feeds `sudo -S` over the session's stdin for accounts that do
	// not have NOPASSWD. Empty means passwordless sudo (`sudo -n`).
	SudoPassword string `json:"sudo_password"`
	// SSHKeyPath is a file NAME (never a path) inside the panel's configured SSH
	// key directory. Set, it takes precedence over the pasted SSHKey PEM and the
	// key never has to travel through the browser or sit in the database.
	SSHKeyPath string `json:"ssh_key_path"`
}

const serverCols = `id, sort_order, name, host, port, ssh_user, ssh_key, ssh_key_pass, ssh_password, config_path, systemd_unit, sing_box_bin, v2ray_listen, enabled, status, last_seen, created_at, updated_at, probe_enabled, probe_token, expiry_date, expiry_notify_enabled, expiry_notify_days, expiry_notify_mode, expiry_notify_count, traffic_limit_bytes, traffic_reset_day, traffic_reset_minute, traffic_alert_percent, traffic_accounting_mode, provider, location, spec, price, notes, host_key, public_visible, public_show_traffic, public_show_price, use_sudo, sudo_password, ssh_key_path, host_v6`

func (s *Store) ListServers() ([]*Server, error) {
	rows, err := s.db.Query(`SELECT ` + serverCols + ` FROM servers ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Server
	for rows.Next() {
		sv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		s.decryptServer(sv)
		out = append(out, sv)
	}
	return out, rows.Err()
}

func (s *Store) GetServer(id int64) (*Server, error) {
	sv, err := scanServer(s.db.QueryRow(`SELECT `+serverCols+` FROM servers WHERE id=?`, id))
	if sv != nil {
		s.decryptServer(sv)
	}
	return sv, err
}

func (s *Store) CreateServer(sv Server) (int64, error) {
	now := time.Now().Unix()
	if sv.Port == 0 {
		sv.Port = 22
	}
	if sv.SSHUser == "" {
		sv.SSHUser = "root"
	}
	if sv.ConfigPath == "" {
		sv.ConfigPath = "/etc/sing-box/config.json"
	}
	if sv.SystemdUnit == "" {
		sv.SystemdUnit = "sing-box"
	}
	// SingBoxBin left empty when not specified — the applier will auto-detect
	// the binary on the target host (local: FindSingBoxBin; remote: PATH lookup).
	if sv.V2rayListen == "" {
		sv.V2rayListen = "127.0.0.1:18080"
	}
	if sv.Status == "" {
		sv.Status = "unknown"
	}
	applyServerMonitorDefaults(&sv)
	// Public display flags are deliberately not listed: their column defaults
	// (1) apply,
	// so a newly added machine shows on the status page exactly as every server
	// did before the flag existed. Hiding one is an explicit act, done through
	// UpdateServer — and a bool field cannot tell "caller wants it hidden" from
	// "caller never filled this in", which is the whole reason it isn't here.
	res, err := s.db.Exec(`INSERT INTO servers (name, host, port, ssh_user, ssh_key, ssh_key_pass, ssh_password, config_path, systemd_unit, sing_box_bin, v2ray_listen, enabled, status, last_seen, created_at, updated_at, probe_enabled, probe_token, probe_token_hash, expiry_date, expiry_notify_enabled, expiry_notify_days, expiry_notify_mode, expiry_notify_count, traffic_limit_bytes, traffic_reset_day, traffic_reset_minute, traffic_alert_percent, provider, location, spec, price, notes, use_sudo, sudo_password, ssh_key_path, host_v6, sort_order)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,(SELECT COALESCE(MAX(sort_order),-1)+1 FROM servers))`,
		sv.Name, sv.Host, sv.Port, sv.SSHUser, s.encrypt(sv.SSHKey), s.encrypt(sv.SSHKeyPass), s.encrypt(sv.SSHPassword),
		sv.ConfigPath, sv.SystemdUnit, sv.SingBoxBin, sv.V2rayListen,
		b2i(sv.Enabled), sv.Status, sv.LastSeen, now, now,
		b2i(sv.ProbeEnabled), s.encrypt(sv.ProbeToken), hashProbeToken(sv.ProbeToken), sv.ExpiryDate,
		b2i(sv.ExpiryNotifyEnabled), sv.ExpiryNotifyDays, sv.ExpiryNotifyMode, sv.ExpiryNotifyCount,
		sv.TrafficLimitBytes, sv.TrafficResetDay, sv.TrafficResetMinute, sv.TrafficAlertPercent,
		sv.Provider, sv.Location, sv.Spec, sv.Price, sv.Notes,
		b2i(sv.UseSudo), s.encrypt(sv.SudoPassword), sv.SSHKeyPath, strings.TrimSpace(sv.HostV6))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateServer(sv Server) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	applyServerMonitorDefaults(&sv)
	_, err = tx.Exec(`UPDATE servers SET name=?, host=?, port=?, ssh_user=?, ssh_key=?, ssh_key_pass=?, ssh_password=?, config_path=?, systemd_unit=?, sing_box_bin=?, v2ray_listen=?, enabled=?, updated_at=?, probe_enabled=?, probe_token=?, probe_token_hash=?, expiry_date=?, expiry_notify_enabled=?, expiry_notify_days=?, expiry_notify_mode=?, expiry_notify_count=?, traffic_limit_bytes=?, traffic_reset_day=?, traffic_reset_minute=?, traffic_alert_percent=?, traffic_accounting_mode=?, provider=?, location=?, spec=?, price=?, notes=?, public_visible=?, public_show_traffic=?, public_show_price=?, use_sudo=?, sudo_password=?, ssh_key_path=?, host_v6=? WHERE id=?`,
		sv.Name, sv.Host, sv.Port, sv.SSHUser, s.encrypt(sv.SSHKey), s.encrypt(sv.SSHKeyPass), s.encrypt(sv.SSHPassword),
		sv.ConfigPath, sv.SystemdUnit, sv.SingBoxBin, sv.V2rayListen,
		b2i(sv.Enabled), now, b2i(sv.ProbeEnabled), s.encrypt(sv.ProbeToken), hashProbeToken(sv.ProbeToken), sv.ExpiryDate,
		b2i(sv.ExpiryNotifyEnabled), sv.ExpiryNotifyDays, sv.ExpiryNotifyMode, sv.ExpiryNotifyCount,
		sv.TrafficLimitBytes, sv.TrafficResetDay, sv.TrafficResetMinute, sv.TrafficAlertPercent, sv.TrafficAccountingMode,
		sv.Provider, sv.Location, sv.Spec, sv.Price, sv.Notes, b2i(sv.PublicVisible), b2i(sv.PublicShowTraffic), b2i(sv.PublicShowPrice),
		b2i(sv.UseSudo), s.encrypt(sv.SudoPassword), sv.SSHKeyPath, strings.TrimSpace(sv.HostV6), sv.ID)
	if err != nil {
		return err
	}
	if err := s.validateRelayTopologySave(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ReorderServers persists the complete administrator-facing server order.
// Server identity and inbound bindings use ids, so changing this field is a
// display-only operation and must never trigger a node rebuild.
func (s *Store) ReorderServers(ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var total int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&total); err != nil {
		return err
	}
	if len(ids) != total {
		return fmt.Errorf("server reorder contains %d ids, want %d", len(ids), total)
	}
	seen := make(map[int64]bool, len(ids))
	for i, id := range ids {
		if seen[id] {
			return fmt.Errorf("server reorder contains duplicate id %d", id)
		}
		seen[id] = true
		res, err := tx.Exec(`UPDATE servers SET sort_order=? WHERE id=?`, i, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("server reorder contains unknown id %d", id)
		}
	}
	return tx.Commit()
}

// EnableServerProbe updates only the probe authentication fields. One-click
// installation runs alongside node sync and asset editing, so routing it through
// UpdateServer's full-row write could restore stale SSH/sing-box values read a
// moment earlier and silently undo an unrelated concurrent change.
func (s *Store) EnableServerProbe(id int64, token string) error {
	res, err := s.db.Exec(`UPDATE servers SET probe_enabled=1, probe_token=?, probe_token_hash=?, updated_at=? WHERE id=?`,
		s.encrypt(token), hashProbeToken(token), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteServer(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Refuse deletion while inbounds/TLS are still bound to this server, else
	// they become undeployable orphans (server_id points at nothing).
	var n int
	err = tx.QueryRow(`SELECT
		(SELECT COUNT(*) FROM sb_inbounds WHERE server_id=?) +
		(SELECT COUNT(*) FROM sb_tls WHERE server_id=?)`, id, id).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w：仍有 %d 个入站/TLS 绑定到此服务器，请先改绑或删除", ErrInUse, n)
	}
	if _, err := tx.Exec(`DELETE FROM servers WHERE id=?`, id); err != nil {
		return err
	}
	// Drop the observed sing-box state with it; a row describing a server that
	// no longer exists would keep showing up in the node version list.
	if _, err := tx.Exec(`DELETE FROM node_singbox WHERE server_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM node_vision_runtime WHERE server_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateServerStatus(id int64, status string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`UPDATE servers SET status=?, last_seen=?, updated_at=? WHERE id=?`, status, now, now, id)
	return err
}

// GetServerByProbeToken finds a server by its probe token (used by the agent
// report endpoint). Returns nil, nil if not found.
func (s *Store) GetServerByProbeToken(token string) (*Server, error) {
	// Match on the token hash — the token is stored encrypted, so a DB leak no
	// longer yields usable bearer tokens for the report endpoint.
	sv, err := scanServer(s.db.QueryRow(`SELECT `+serverCols+` FROM servers WHERE probe_token_hash=? AND probe_enabled=1`, hashProbeToken(token)))
	if sv != nil {
		s.decryptServer(sv)
	}
	return sv, err
}

// SetServerSingBoxBin records where the node's sing-box binary actually is, as
// resolved by a live connection. Kept separate from UpdateServer so a background
// probe cannot clobber the twenty-odd other columns that handler writes.
func (s *Store) SetServerSingBoxBin(id int64, bin string) error {
	_, err := s.db.Exec(`UPDATE servers SET sing_box_bin=?, updated_at=? WHERE id=?`, bin, time.Now().Unix(), id)
	return err
}

// SetServerHostKey pins (or updates) the SSH host key for a server. Called on
// first successful connect (TOFU) so subsequent connects are verified against it.
func (s *Store) SetServerHostKey(id int64, hostKey string) error {
	_, err := s.db.Exec(`UPDATE servers SET host_key=? WHERE id=?`, hostKey, id)
	return err
}

// TouchProbeSeen updates last_seen to now for a probe-enabled server.
func (s *Store) TouchProbeSeen(id int64) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`UPDATE servers SET last_seen=?, updated_at=? WHERE id=?`, now, now, id)
	return err
}

func scanServer(sc scanner) (*Server, error) {
	var sv Server
	var enabled, probeEnabled, expiryNotifyEnabled, publicVisible, publicShowTraffic, publicShowPrice, useSudo int
	err := sc.Scan(&sv.ID, &sv.SortOrder, &sv.Name, &sv.Host, &sv.Port, &sv.SSHUser, &sv.SSHKey, &sv.SSHKeyPass, &sv.SSHPassword,
		&sv.ConfigPath, &sv.SystemdUnit, &sv.SingBoxBin, &sv.V2rayListen,
		&enabled, &sv.Status, &sv.LastSeen, &sv.CreatedAt, &sv.UpdatedAt,
		&probeEnabled, &sv.ProbeToken, &sv.ExpiryDate, &expiryNotifyEnabled, &sv.ExpiryNotifyDays, &sv.ExpiryNotifyMode, &sv.ExpiryNotifyCount,
		&sv.TrafficLimitBytes, &sv.TrafficResetDay, &sv.TrafficResetMinute, &sv.TrafficAlertPercent, &sv.TrafficAccountingMode,
		&sv.Provider, &sv.Location, &sv.Spec, &sv.Price, &sv.Notes, &sv.HostKey,
		&publicVisible, &publicShowTraffic, &publicShowPrice, &useSudo, &sv.SudoPassword, &sv.SSHKeyPath, &sv.HostV6)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sv.Enabled = enabled == 1
	sv.ProbeEnabled = probeEnabled == 1
	sv.ExpiryNotifyEnabled = expiryNotifyEnabled == 1
	sv.PublicVisible = publicVisible == 1
	sv.PublicShowTraffic = publicShowTraffic == 1
	sv.PublicShowPrice = publicShowPrice == 1
	sv.UseSudo = useSudo == 1
	return &sv, nil
}

func applyServerMonitorDefaults(sv *Server) {
	if sv.ExpiryNotifyDays <= 0 {
		sv.ExpiryNotifyDays = 3
	}
	if sv.ExpiryNotifyMode != "daily" {
		sv.ExpiryNotifyMode = "count"
	}
	if sv.ExpiryNotifyCount <= 0 {
		sv.ExpiryNotifyCount = 1
	}
	if sv.TrafficResetDay <= 0 {
		sv.TrafficResetDay = 1
	}
	if sv.TrafficAlertPercent <= 0 {
		sv.TrafficAlertPercent = 80
	}
	sv.TrafficAccountingMode = NormalizeTrafficAccountingMode(sv.TrafficAccountingMode)
}

func (s *Store) decryptServer(sv *Server) {
	sv.SSHKey = s.decrypt(sv.SSHKey)
	sv.SSHKeyPass = s.decrypt(sv.SSHKeyPass)
	sv.SSHPassword = s.decrypt(sv.SSHPassword)
	sv.ProbeToken = s.decrypt(sv.ProbeToken)
	sv.SudoPassword = s.decrypt(sv.SudoPassword)
	// SSHKeyPath is deliberately NOT encrypted: it is a file name, not a secret,
	// and encrypting it only means you cannot see which key a row points at when
	// you are trying to work out why that row will not connect.
}
