package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"qingzhou/internal/singbox"
)

// SbTls is a TLS/Reality profile for native sing-box inbounds (B2). ServerJSON
// (the sing-box "tls" block, including the Reality private_key) is stored
// encrypted at rest; it is returned decrypted from these methods.
type SbTls struct {
	ID         int64  `json:"id"`
	ServerID   int64  `json:"server_id"`
	Name       string `json:"name"`
	Mode       string `json:"mode"` // reality | tls
	ServerJSON string `json:"server_json"`
	ClientJSON string `json:"client_json"`
	// CertID references a managed certificate (certificates.id) for mode=tls
	// profiles. 0 = none / legacy inline PEM held in ServerJSON.
	CertID int64 `json:"cert_id"`
	// SortOrder is the admin list's display order. Written only by ReorderSbTls —
	// SaveSbTls leaves the column alone so an edit (which posts no sort_order)
	// can't silently send the profile back to the top of the list.
	SortOrder int   `json:"sort_order"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
	// DecryptFailed is set when ServerJSON was stored encrypted but could not be
	// decrypted (typically a changed/wrong QZ_SECRET_KEY). The config builder MUST
	// refuse to emit such an inbound rather than downgrade it to a plaintext one.
	DecryptFailed bool `json:"-"`
}

// SbInbound is a native sing-box server inbound owned by 轻舟.
type SbInbound struct {
	ID         int64  `json:"id"`
	ServerID   int64  `json:"server_id"`
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Listen     string `json:"listen"`
	ListenPort int    `json:"listen_port"`
	TlsID      int64  `json:"tls_id"`
	Options    string `json:"options"` // JSON object of extra inbound fields
	Enabled    bool   `json:"enabled"`
	SortOrder  int    `json:"sort_order"`
	// UpstreamInboundID makes this inbound a relay: instead of exiting to the
	// internet, its traffic is forwarded to the landing inbound with this id
	// (0 = direct exit / landing). See BuildSingboxConfigForServer relay wiring.
	UpstreamInboundID int64 `json:"upstream_inbound_id"`
	// EgressID routes this inbound's traffic out through a third-party proxy
	// egress (sb_egresses, e.g. a purchased static-IP SOCKS5/HTTP proxy) instead
	// of exiting directly. Mutually exclusive with UpstreamInboundID (0 = unused).
	EgressID int64 `json:"egress_id"`
	// RelaySecret is a landing inbound's own auth secret, generated lazily when a
	// relay first targets it. Both the relay's upstream outbound and the relay
	// user injected into this inbound derive their credential from it.
	RelaySecret string `json:"-"`
	// UpstreamBroken marks an inbound whose landing was deleted out from under
	// it. DeleteSbInbound clears upstream_inbound_id so the stored chain matches
	// the config actually pushed, but that silently turns a relay into a direct
	// exit — the traffic now leaves from the relay machine's IP instead of the
	// landing's. This flag keeps that visible in 链路拓扑 until an admin saves the
	// inbound again (which clears it), rather than letting the downgrade vanish.
	UpstreamBroken bool  `json:"upstream_broken"`
	CreatedAt      int64 `json:"created_at"`
	UpdatedAt      int64 `json:"updated_at"`
}

// ---- sb_tls ----

func (s *Store) ListSbTls() ([]*SbTls, error) {
	rows, err := s.db.Query(`SELECT id, server_id, name, mode, server_json, client_json, cert_id, sort_order, created_at, updated_at
		FROM sb_tls ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SbTls{}
	for rows.Next() {
		var t SbTls
		if err := rows.Scan(&t.ID, &t.ServerID, &t.Name, &t.Mode, &t.ServerJSON, &t.ClientJSON, &t.CertID, &t.SortOrder, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		var ok bool
		t.ServerJSON, ok = s.decryptOK(t.ServerJSON)
		t.DecryptFailed = !ok
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (s *Store) GetSbTls(id int64) (*SbTls, error) {
	var t SbTls
	err := s.db.QueryRow(`SELECT id, server_id, name, mode, server_json, client_json, cert_id, sort_order, created_at, updated_at
		FROM sb_tls WHERE id=?`, id).Scan(&t.ID, &t.ServerID, &t.Name, &t.Mode, &t.ServerJSON, &t.ClientJSON, &t.CertID, &t.SortOrder, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		// Don't mask a real DB error as "not found" — the config builder would
		// otherwise silently drop this inbound's TLS block and emit it plaintext.
		return nil, err
	}
	var ok bool
	t.ServerJSON, ok = s.decryptOK(t.ServerJSON)
	t.DecryptFailed = !ok
	return &t, nil
}

// SaveSbTls inserts (id==0) or updates a TLS profile. ServerJSON is encrypted.
func (s *Store) SaveSbTls(t *SbTls) (int64, error) {
	now := time.Now().Unix()
	enc := s.encrypt(t.ServerJSON)
	if t.ID == 0 {
		// New rows land at the end of the list. Leaving sort_order at 0 would drop
		// them into the middle of a manually ordered list (0 ties with whatever the
		// admin put first), which reads as the list reshuffling itself.
		res, err := s.db.Exec(`INSERT INTO sb_tls (server_id, name, mode, server_json, client_json, cert_id, sort_order, created_at, updated_at)
			VALUES (?,?,?,?,?,?,(SELECT COALESCE(MAX(sort_order),0)+1 FROM sb_tls),?,?)`, t.ServerID, t.Name, t.Mode, enc, t.ClientJSON, t.CertID, now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE sb_tls SET server_id=?, name=?, mode=?, server_json=?, client_json=?, cert_id=?, updated_at=? WHERE id=?`,
		t.ServerID, t.Name, t.Mode, enc, t.ClientJSON, t.CertID, now, t.ID)
	return t.ID, err
}

// ReorderSbTls sets sort_order to each id's position in the given slice, so
// ListSbTls (ORDER BY sort_order, id) renders in this exact order. The admin
// page groups TLS profiles by machine but sort_order is global, so callers
// reorder by swapping global positions; ids not listed keep their old value.
func (s *Store) ReorderSbTls(ids []int64) error {
	return s.reorderByID("sb_tls", ids)
}

// ReorderSbInbounds does the same for the inbound list. Inbound order has no
// effect on a running node (sing-box dispatches by listen port and tag); it only
// decides where the inbound appears in the generated inbounds array.
func (s *Store) ReorderSbInbounds(ids []int64) error {
	return s.reorderByID("sb_inbounds", ids)
}

// reorderByID rewrites sort_order for the given ids in one transaction. table is
// never caller-supplied — it comes from the two wrappers above.
func (s *Store) reorderByID(table string, ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE `+table+` SET sort_order=? WHERE id=?`, i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ErrInUse is returned when a delete is refused because other rows still
// reference the target. Handlers surface its message to the client.
var ErrInUse = errors.New("仍被引用，无法删除")

func (s *Store) DeleteSbTls(id int64) error {
	// Refuse deletion while an inbound still references this TLS: nulling it out
	// would silently strip encryption from a live inbound (e.g. a VLESS Reality
	// node), which is worse than a clear error.
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sb_inbounds WHERE tls_id=?`, id).Scan(&n)
	if n > 0 {
		return fmt.Errorf("%w：仍有 %d 个入站在使用此 TLS", ErrInUse, n)
	}
	_, err := s.db.Exec(`DELETE FROM sb_tls WHERE id=?`, id)
	return err
}

// resolveTlsBlock builds the sing-box "tls" block for an inbound's TLS profile.
// When the profile references a managed certificate (cert_id != 0) it injects
// that cert's certificate/key/server_name into the block, so one renewed cert
// flows to every inbound that uses it. Returns (nil, nil) when the inbound has
// no TLS. Fails CLOSED on any undecryptable secret or a dangling cert reference:
// emitting the inbound without its TLS block would downgrade it to plaintext and
// leak all its traffic, which is worse than refusing to build. The caches are
// per-build to avoid re-decrypting the same profile/cert for every inbound.
func (s *Store) resolveTlsBlock(tlsID int64, tag string, tlsCache map[int64]*SbTls, certCache map[int64]*Cert) (map[string]interface{}, error) {
	if tlsID == 0 {
		return nil, nil
	}
	tls, ok := tlsCache[tlsID]
	if !ok {
		tls, _ = s.GetSbTls(tlsID)
		tlsCache[tlsID] = tls
	}
	if tls == nil {
		return nil, nil
	}
	if tls.DecryptFailed {
		return nil, fmt.Errorf("入站 %s 的 TLS 配置(id=%d)无法解密，已拒绝生成配置以避免降级为明文入站——请确认 QZ_SECRET_KEY 与加密时一致", tag, tlsID)
	}
	var tj map[string]interface{}
	if tls.ServerJSON != "" {
		if err := json.Unmarshal([]byte(tls.ServerJSON), &tj); err != nil {
			tj = nil
		}
	}
	if tls.CertID != 0 {
		cert, ok := certCache[tls.CertID]
		if !ok {
			cert, _ = s.GetCert(tls.CertID)
			certCache[tls.CertID] = cert
		}
		if cert == nil {
			return nil, fmt.Errorf("入站 %s 的 TLS 配置(id=%d)引用的证书(id=%d)不存在，已拒绝生成配置", tag, tlsID, tls.CertID)
		}
		if cert.DecryptFailed {
			return nil, fmt.Errorf("入站 %s 引用的证书(id=%d)无法解密，已拒绝生成配置以避免降级为明文入站——请确认 QZ_SECRET_KEY 与加密时一致", tag, tls.CertID)
		}
		if tj == nil {
			tj = map[string]interface{}{"enabled": true}
		}
		// A managed cert supplies inline PEM; drop any stale path fields that
		// would otherwise shadow it, and pin the SNI to the cert's domain.
		delete(tj, "certificate_path")
		delete(tj, "key_path")
		tj["certificate"] = cert.CertPEM
		tj["key"] = cert.KeyPEM
		if cert.Domain != "" {
			tj["server_name"] = cert.Domain
		}
	}
	return tj, nil
}

// ---- sb_inbounds ----

func (s *Store) ListSbInbounds() ([]*SbInbound, error) {
	rows, err := s.db.Query(`SELECT id, server_id, type, tag, listen, listen_port, tls_id, options, enabled, sort_order, upstream_inbound_id, relay_secret, egress_id, upstream_broken, created_at, updated_at
		FROM sb_inbounds ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SbInbound{}
	for rows.Next() {
		var n SbInbound
		var enabled, broken int
		if err := rows.Scan(&n.ID, &n.ServerID, &n.Type, &n.Tag, &n.Listen, &n.ListenPort, &n.TlsID, &n.Options, &enabled, &n.SortOrder, &n.UpstreamInboundID, &n.RelaySecret, &n.EgressID, &broken, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		n.Enabled = enabled == 1
		n.UpstreamBroken = broken == 1
		out = append(out, &n)
	}
	return out, rows.Err()
}

func (s *Store) GetSbInbound(id int64) (*SbInbound, error) {
	var n SbInbound
	var enabled, broken int
	err := s.db.QueryRow(`SELECT id, server_id, type, tag, listen, listen_port, tls_id, options, enabled, sort_order, upstream_inbound_id, relay_secret, egress_id, upstream_broken, created_at, updated_at
		FROM sb_inbounds WHERE id=?`, id).Scan(&n.ID, &n.ServerID, &n.Type, &n.Tag, &n.Listen, &n.ListenPort, &n.TlsID, &n.Options, &enabled, &n.SortOrder, &n.UpstreamInboundID, &n.RelaySecret, &n.EgressID, &broken, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n.Enabled = enabled == 1
	n.UpstreamBroken = broken == 1
	return &n, nil
}

func (s *Store) GetSbInboundByTag(tag string) (*SbInbound, error) {
	var n SbInbound
	var enabled, broken int
	err := s.db.QueryRow(`SELECT id, server_id, type, tag, listen, listen_port, tls_id, options, enabled, sort_order, upstream_inbound_id, relay_secret, egress_id, upstream_broken, created_at, updated_at
		FROM sb_inbounds WHERE tag=?`, tag).Scan(&n.ID, &n.ServerID, &n.Type, &n.Tag, &n.Listen, &n.ListenPort, &n.TlsID, &n.Options, &enabled, &n.SortOrder, &n.UpstreamInboundID, &n.RelaySecret, &n.EgressID, &broken, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	n.Enabled = enabled == 1
	n.UpstreamBroken = broken == 1
	return &n, err
}

func (s *Store) SaveSbInbound(n *SbInbound) (int64, error) {
	now := time.Now().Unix()
	if n.Options == "" {
		n.Options = "{}"
	}
	if n.Listen == "" {
		n.Listen = "::"
	}
	if n.ID == 0 {
		if n.SortOrder == 0 {
			// Append rather than tie with the first manually ordered row — see the
			// same note in SaveSbTls.
			_ = s.db.QueryRow(`SELECT COALESCE(MAX(sort_order),0)+1 FROM sb_inbounds`).Scan(&n.SortOrder)
		}
		res, err := s.db.Exec(`INSERT INTO sb_inbounds (server_id, type, tag, listen, listen_port, tls_id, options, enabled, sort_order, upstream_inbound_id, relay_secret, egress_id, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			n.ServerID, n.Type, n.Tag, n.Listen, n.ListenPort, n.TlsID, n.Options, b2i(n.Enabled), n.SortOrder, n.UpstreamInboundID, n.RelaySecret, n.EgressID, now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	// Self-built nodes link to an inbound by its tag (inbound_tag is a copy
	// of the value). If the tag changes, that linkage — and the group/subscription
	// matching built on it — silently breaks. Cascade the rename atomically.
	var oldTag string
	_ = s.db.QueryRow(`SELECT tag FROM sb_inbounds WHERE id=?`, n.ID).Scan(&oldTag)
	tagChanged := oldTag != "" && oldTag != n.Tag

	tx, err := s.db.Begin()
	if err != nil {
		return n.ID, err
	}
	defer tx.Rollback()
	// upstream_broken clears only when this save gives the inbound a real exit
	// again — a new landing, or an egress. Not on every save: enable/disable and
	// the batch toggle go through here too, and an unrelated toggle must not
	// dismiss a warning about traffic leaving from the wrong machine. Accepting
	// the direct exit as-is is the other way out, via AckUpstreamBroken.
	rechained := 0
	if n.UpstreamInboundID != 0 || n.EgressID != 0 {
		rechained = 1
	}
	if _, err := tx.Exec(`UPDATE sb_inbounds SET server_id=?, type=?, tag=?, listen=?, listen_port=?, tls_id=?, options=?, enabled=?, sort_order=?, upstream_inbound_id=?, relay_secret=?, egress_id=?,
		upstream_broken=CASE WHEN ?=1 THEN 0 ELSE upstream_broken END, updated_at=? WHERE id=?`,
		n.ServerID, n.Type, n.Tag, n.Listen, n.ListenPort, n.TlsID, n.Options, b2i(n.Enabled), n.SortOrder, n.UpstreamInboundID, n.RelaySecret, n.EgressID, rechained, now, n.ID); err != nil {
		return n.ID, err
	}
	if tagChanged {
		// Re-point linked self-built nodes to the new tag, and keep the node's
		// display name in sync when it was the auto-derived default (== old tag);
		// leave custom names alone.
		if _, err := tx.Exec(`UPDATE nodes SET name=? WHERE type='self_built' AND inbound_tag=? AND name=?`, n.Tag, oldTag, oldTag); err != nil {
			return n.ID, err
		}
		if _, err := tx.Exec(`UPDATE nodes SET inbound_tag=? WHERE type='self_built' AND inbound_tag=?`, n.Tag, oldTag); err != nil {
			return n.ID, err
		}
	}
	return n.ID, tx.Commit()
}

// AckUpstreamBroken clears the downgrade warning on an inbound whose landing was
// deleted — the admin looked at it and accepted the direct exit. Re-pointing the
// inbound at a new landing or an egress clears it too (see SaveSbInbound); this
// is the other outcome, where nothing about the config changes and only the
// admin's knowledge of it does.
func (s *Store) AckUpstreamBroken(id int64) error {
	_, err := s.db.Exec(`UPDATE sb_inbounds SET upstream_broken=0, updated_at=? WHERE id=?`,
		time.Now().Unix(), id)
	return err
}

// DeleteSbInbound removes an inbound plus everything that only exists because of
// it, and returns the ids of the servers hosting relay inbounds that were
// un-chained by the deletion (see below) so the caller can rebuild them.
func (s *Store) DeleteSbInbound(id int64) ([]int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// A self-built node's physical entry is linked by tag. Without that listener
	// the logical node is non-functional, so remove every node using it — otherwise
	// they linger as zombies and silently revive if a same-tag inbound is recreated.
	var tag string
	_ = tx.QueryRow(`SELECT tag FROM sb_inbounds WHERE id=?`, id).Scan(&tag)
	if tag != "" {
		if _, err := tx.Exec(`DELETE FROM node_group_members WHERE node_id IN (SELECT id FROM nodes WHERE type='self_built' AND inbound_tag=?)`, tag); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM nodes WHERE type='self_built' AND inbound_tag=?`, tag); err != nil {
			return nil, err
		}
	}
	// Un-chain the relays that used this inbound as their landing. buildRelayWiring
	// already skips a dangling upstream (that traffic exits directly from the relay
	// machine), so leaving the id behind stores a link that no longer exists — and
	// the 链路拓扑 page keeps drawing the deleted inbound as「落地已失效」forever.
	// Clearing it makes the stored chain match the config that is actually pushed.
	//
	// upstream_broken records that this happened. Un-chaining is not a neutral
	// edit: those relays now exit from their own machine's IP instead of the
	// landing's, and without the flag that change would be invisible the moment
	// the admin closes the confirm dialog.
	relayServers, err := serverIDsRelayingTo(tx, id)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE sb_inbounds SET upstream_inbound_id=0, upstream_broken=1 WHERE upstream_inbound_id=?`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE nodes SET route_upstream_inbound_id=0, route_upstream_broken=1 WHERE route_upstream_inbound_id=?`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM sb_inbounds WHERE id=?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return relayServers, nil
}

// serverIDsRelayingTo lists the distinct servers hosting inbounds that relay to
// the given landing inbound. Those servers must rebuild when the landing goes
// away, to drop the upstream outbound that dialed it.
func serverIDsRelayingTo(tx *sql.Tx, landingID int64) ([]int64, error) {
	rows, err := tx.Query(`SELECT DISTINCT server_id FROM (
		SELECT server_id FROM sb_inbounds WHERE upstream_inbound_id=?
		UNION
		SELECT i.server_id FROM nodes n JOIN sb_inbounds i ON i.tag=n.inbound_tag
		 WHERE n.type='self_built' AND n.route_upstream_inbound_id=?
	)`, landingID, landingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var sid int64
		if err := rows.Scan(&sid); err != nil {
			return nil, err
		}
		out = append(out, sid)
	}
	return out, rows.Err()
}

func (s *Store) ListSbInboundsByServer(serverID int64) ([]*SbInbound, error) {
	rows, err := s.db.Query(`SELECT id, server_id, type, tag, listen, listen_port, tls_id, options, enabled, sort_order, upstream_inbound_id, relay_secret, egress_id, created_at, updated_at
		FROM sb_inbounds WHERE server_id=? ORDER BY sort_order, id`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SbInbound{}
	for rows.Next() {
		var n SbInbound
		var enabled int
		if err := rows.Scan(&n.ID, &n.ServerID, &n.Type, &n.Tag, &n.Listen, &n.ListenPort, &n.TlsID, &n.Options, &enabled, &n.SortOrder, &n.UpstreamInboundID, &n.RelaySecret, &n.EgressID, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		n.Enabled = enabled == 1
		out = append(out, &n)
	}
	return out, rows.Err()
}

// transportBits is the set of L4 protocols an inbound binds on its port.
type transportBits uint8

const (
	transportTCP transportBits = 1 << iota
	transportUDP
)

// inboundTransports reports which L4 protocols an inbound type binds. Sharing a
// port is only a conflict when two inbounds want the same L4 — e.g. hysteria2
// (QUIC/UDP) and vless (TCP) both on 443 is a legitimate, common pairing.
func inboundTransports(typ, options string) transportBits {
	switch typ {
	case "tuic", "hysteria2", "hysteria":
		return transportUDP // QUIC-based: UDP only
	case "vless", "vmess", "trojan", "anytls", "mixed":
		return transportTCP
	case "shadowsocks":
		// sing-box's shadowsocks inbound serves both unless network narrows it.
		var opts map[string]interface{}
		if options != "" {
			_ = json.Unmarshal([]byte(options), &opts)
		}
		var bits transportBits
		if n, _ := opts["network"].(string); n != "" {
			for _, part := range strings.Split(n, ",") {
				switch strings.TrimSpace(part) {
				case "tcp":
					bits |= transportTCP
				case "udp":
					bits |= transportUDP
				}
			}
		}
		if bits == 0 {
			bits = transportTCP | transportUDP
		}
		return bits
	default:
		// Unknown/new type: assume it may bind both, so a clash is reported
		// rather than silently allowed — a double bind fails at sing-box start,
		// which is worse than a false rejection the admin can see and reason about.
		return transportTCP | transportUDP
	}
}

// listenIsAny reports whether a listen address is a wildcard (binds every
// interface, so it overlaps any other address on the same port).
func listenIsAny(s string) bool {
	return s == "" || s == "::" || s == "::0" || s == "0.0.0.0"
}

func listenOverlaps(a, b string) bool {
	if listenIsAny(a) || listenIsAny(b) {
		return true
	}
	return a == b
}

// SbInboundPortConflict 检测入站的 listen+端口是否与同服务器的其他入站真正冲突。
// 仅当「监听地址重叠」且「L4 协议(TCP/UDP)有交集」时才算冲突 —— 同端口的
// hysteria2(UDP)+ vless(TCP)是合法搭配,不应拦截。n.ID 非 0 时排除自身。
// 返回 (conflict, existingTag, error)。
func (s *Store) SbInboundPortConflict(n *SbInbound) (bool, string, error) {
	rows, err := s.db.Query(`SELECT tag, type, listen, options FROM sb_inbounds
		WHERE server_id=? AND listen_port=? AND id!=?`, n.ServerID, n.ListenPort, n.ID)
	if err != nil {
		return false, "", err
	}
	defer rows.Close()
	newBits := inboundTransports(n.Type, n.Options)
	for rows.Next() {
		var tag, typ, listen, options string
		if err := rows.Scan(&tag, &typ, &listen, &options); err != nil {
			return false, "", err
		}
		if !listenOverlaps(listen, n.Listen) {
			continue
		}
		if inboundTransports(typ, options)&newBits == 0 {
			continue // different L4 — both can bind this port
		}
		return true, tag, nil
	}
	return false, "", rows.Err()
}

// SetUserPlan sets a user's current plan (for entitlement); planID 0 clears it.
func (s *Store) SetUserPlan(userID, planID int64) error {
	if planID == 0 {
		_, err := s.db.Exec(`UPDATE users SET current_plan_id=NULL, updated_at=? WHERE id=?`, time.Now().Unix(), userID)
		return err
	}
	_, err := s.db.Exec(`UPDATE users SET current_plan_id=?, updated_at=? WHERE id=?`, planID, time.Now().Unix(), userID)
	return err
}

// AddUsageByClientName accumulates a traffic delta from the sing-box v2ray stats
// poll. Identities are now per-bucket (plan / pool), so the delta is routed to
// the matching bucket, which also mirrors it onto the owning user (aggregate
// counters + last_online + the per-user time-series). Counters in sing-box reset
// each poll, so this is called with deltas.
func (s *Store) AddUsageByClientName(name string, up, down int64) error {
	return s.AddBucketUsage(name, up, down)
}

// ---- config assembly ----

// SettingBlockPrivateEgress is the admin toggle for the private-destination
// reject rule. Absent means enabled: it is a security default, and an upgrade
// that silently left existing installs reachable-into-LAN would defeat the
// point. Only an explicit "0" turns it off — for the rare operator who really
// does want subscribers to reach the landing machine's own network.
const SettingBlockPrivateEgress = "sb_block_private"

func (s *Store) blockPrivateEgress() bool {
	v, _ := s.GetSetting(SettingBlockPrivateEgress)
	return v != "0"
}

// BuildSingboxConfig assembles a full sing-box config from the enabled inbounds
// (each merged with its TLS/Reality block and extra options) plus the users
// entitled to each inbound tag (usersByTag, computed by the caller's
// entitlement logic). base is the log/dns/route/outbounds template JSON.
func (s *Store) BuildSingboxConfig(base, v2rayListen string, usersByTag map[string][]singbox.User) ([]byte, error) {
	inbounds, err := s.ListSbInboundsByServer(0)
	if err != nil {
		return nil, err
	}
	allInbounds, err := s.ListSbInbounds()
	if err != nil {
		return nil, err
	}
	relays, landingUsers, err := s.buildRelayWiring(inbounds, allInbounds, usersByTag)
	if err != nil {
		return nil, err
	}
	tlsCache := map[int64]*SbTls{}
	certCache := map[int64]*Cert{}
	var ibs []singbox.Inbound
	for _, ib := range inbounds {
		if !ib.Enabled {
			continue
		}
		baseMap := map[string]interface{}{
			"type":        ib.Type,
			"tag":         ib.Tag,
			"listen":      ib.Listen,
			"listen_port": ib.ListenPort,
		}
		if ib.Options != "" && ib.Options != "{}" {
			var opts map[string]interface{}
			if err := json.Unmarshal([]byte(ib.Options), &opts); err == nil {
				for k, v := range opts {
					baseMap[k] = v
				}
			}
		}
		tlsBlock, err := s.resolveTlsBlock(ib.TlsID, ib.Tag, tlsCache, certCache)
		if err != nil {
			return nil, err
		}
		if tlsBlock != nil {
			baseMap["tls"] = tlsBlock
		}
		ibs = append(ibs, singbox.Inbound{Type: ib.Type, Base: baseMap, Users: mergeRelayUser(usersByTag[ib.Tag], landingUsers, ib.Tag)})
	}
	return singbox.GenerateConfigWithOptions([]byte(base), ibs, singbox.Options{
		V2RayListen:  v2rayListen,
		Relays:       relays,
		BlockPrivate: s.blockPrivateEgress(),
	})
}

// BuildSingboxConfigForServer is like BuildSingboxConfig but filters inbounds
// to those belonging to the given server. serverID 0 means "no server" (legacy).
func (s *Store) BuildSingboxConfigForServer(serverID int64, base, v2rayListen string, usersByTag map[string][]singbox.User) ([]byte, error) {
	var inbounds []*SbInbound
	var err error
	if serverID == 0 {
		inbounds, err = s.ListSbInbounds()
	} else {
		inbounds, err = s.ListSbInboundsByServer(serverID)
	}
	if err != nil {
		return nil, err
	}
	allInbounds, err := s.ListSbInbounds()
	if err != nil {
		return nil, err
	}
	relays, landingUsers, err := s.buildRelayWiring(inbounds, allInbounds, usersByTag)
	if err != nil {
		return nil, err
	}
	tlsCache := map[int64]*SbTls{}
	certCache := map[int64]*Cert{}
	var ibs []singbox.Inbound
	for _, ib := range inbounds {
		if !ib.Enabled {
			continue
		}
		baseMap := map[string]interface{}{
			"type":        ib.Type,
			"tag":         ib.Tag,
			"listen":      ib.Listen,
			"listen_port": ib.ListenPort,
		}
		if ib.Options != "" && ib.Options != "{}" {
			var opts map[string]interface{}
			if err := json.Unmarshal([]byte(ib.Options), &opts); err == nil {
				for k, v := range opts {
					baseMap[k] = v
				}
			}
		}
		tlsBlock, err := s.resolveTlsBlock(ib.TlsID, ib.Tag, tlsCache, certCache)
		if err != nil {
			return nil, err
		}
		if tlsBlock != nil {
			baseMap["tls"] = tlsBlock
		}
		ibs = append(ibs, singbox.Inbound{Type: ib.Type, Base: baseMap, Users: mergeRelayUser(usersByTag[ib.Tag], landingUsers, ib.Tag)})
	}
	// Remote servers may not have v2ray_api compiled in; pass empty to skip.
	// v2rayListen == "" skips the experimental.v2ray_api block. Remote servers
	// used to be hardcoded to "" here, on the assumption that they might not have
	// the plugin compiled in — which meant no remote node ever exposed its stats
	// API, and so no remote traffic was ever metered. The caller now decides:
	// it probes each node's build tags (sshctl.SupportsStatsAPI) and passes that
	// node's own listen address only when the plugin is actually present.
	return singbox.GenerateConfigWithOptions([]byte(base), ibs, singbox.Options{
		V2RayListen:  v2rayListen,
		Relays:       relays,
		BlockPrivate: s.blockPrivateEgress(),
	})
}

// SelfBuiltLink is one generated share-link plus the inbound tag it came from.
// The tag is carried alongside rather than read back out of the link's remark,
// because the remark shows the node's admin-configured display name.
type SelfBuiltLink struct {
	NodeID int64  // logical node id — several nodes may share one inbound tag
	Tag    string // physical inbound tag — topology/config join key
	Link   string
}

// BuildSelfBuiltLinks generates client share-links for every enabled native
// inbound using the user's own credentials — replacing the sing-box sub fetch so
// subscriptions survive the cutover. host is the dial address advertised to
// clients (node_host_override / origin IP). Each link's remark is the node's
// display name from the 节点 page, falling back to the inbound tag when no node
// is bound to it. Each link uses the credentials of the bucket that owns the
// inbound (see UserOwnedInbounds), so a
// user with an active plan bucket gets links even if their legacy users.*
// identity is empty — e.g. an admin account, which is never provisioned a
// client_uuid. Returns nil only when no address is configured or no bucket
// owns any inbound.
func (s *Store) BuildSelfBuiltLinks(u *User, host string) []SelfBuiltLink {
	if host == "" {
		return nil // no advertised address configured
	}
	inbounds, err := s.ListSbInbounds()
	if err != nil {
		return nil
	}
	// Cache server and TLS lookups so we don't query per-inbound.
	serverCache := make(map[int64]*Server)
	tlsCache := make(map[int64]*SbTls)
	getTls := func(id int64) *SbTls {
		if t, ok := tlsCache[id]; ok {
			return t
		}
		t, _ := s.GetSbTls(id)
		tlsCache[id] = t // cache nil too (negative cache)
		return t
	}
	// Fingerprint of the certificate an inbound presents, but only when it is
	// self-signed — that is the case where the client has nothing to verify
	// against and would otherwise have to accept any certificate at all. A
	// publicly-issued cert is deliberately left unpinned: it rotates on every
	// ACME renewal, and a subscription cached by the client across a renewal
	// would then pin a certificate the server no longer presents.
	pinCache := map[int64]string{}
	certPin := func(t *SbTls, server map[string]interface{}) string {
		if t == nil || t.DecryptFailed {
			return ""
		}
		if pin, ok := pinCache[t.ID]; ok {
			return pin
		}
		pem := mapStr(server, "certificate") // legacy inline-PEM profile
		if t.CertID != 0 {
			if c, _ := s.GetCert(t.CertID); c != nil && !c.DecryptFailed {
				pem = c.CertPEM
			} else {
				pem = ""
			}
		}
		pin := ""
		if pem != "" && singbox.IsSelfSignedCert(pem) {
			pin = singbox.CertFingerprintSHA256(pem)
		}
		pinCache[t.ID] = pin
		return pin
	}
	// Each self-built node is owned by one of the user's buckets; the link uses
	// that bucket's credentials and shows its own remaining quota/expiry. A node
	// with no active owning bucket is omitted (no access).
	now := time.Now().Unix()
	owners, _ := s.UserOwnedNodes(u.ID, now)
	legacyOwners, _ := s.UserOwnedInbounds(u.ID, now)
	legacyNames, _ := s.SelfBuiltNodeNames()
	nodes, _ := s.ListNodes()
	inboundByTag := map[string]*SbInbound{}
	for _, ib := range inbounds {
		inboundByTag[ib.Tag] = ib
	}
	// Whether an inbound's bound egress drops UDP (udp_mode=block), so the link
	// can say so (LinkParams.NoUDP) and clients refuse UDP locally instead of
	// timing out against the node-side reject. Cached per egress id: several
	// inbounds routinely share one egress.
	egressNoUDP := map[int64]bool{}
	noUDP := func(egressID int64) bool {
		if egressID == 0 {
			return false
		}
		if v, ok := egressNoUDP[egressID]; ok {
			return v
		}
		v := false
		if eg, _ := s.GetSbEgress(egressID); eg != nil {
			v = eg.EffectiveUDPMode() == UDPModeBlock
		}
		egressNoUDP[egressID] = v
		return v
	}

	var out []SelfBuiltLink
	legacyDone := map[string]bool{}
	for _, n := range nodes {
		if !n.Enabled || n.Type != "self_built" || n.RouteUpstreamBroken {
			continue
		}
		ib := inboundByTag[n.InboundTag]
		if ib == nil || !ib.Enabled {
			continue
		}
		if n.RouteUpstreamInboundID != 0 {
			if !inboundRouteHealthy(inbounds, n.RouteUpstreamInboundID) {
				continue // fixed exits fail closed; never leak through the entry machine
			}
		}
		owner := owners[n.ID]
		logicalID := n.ID
		if n.RouteUpstreamInboundID == 0 {
			if legacyDone[ib.Tag] {
				continue
			}
			legacyDone[ib.Tag] = true
			owner = legacyOwners[ib.Tag]
			logicalID = 0
		}
		if owner == nil {
			continue // no active bucket grants this node
		}
		// Use the server's own host for remote nodes instead of the
		// global host which only applies to the local server.
		nodeHost := host
		if ib.ServerID != 0 {
			if sv, ok := serverCache[ib.ServerID]; ok {
				if sv != nil && sv.Host != "" {
					nodeHost = sv.Host
				}
			} else if sv, _ := s.GetServer(ib.ServerID); sv != nil {
				serverCache[ib.ServerID] = sv
				if sv.Host != "" {
					nodeHost = sv.Host
				}
			} else {
				serverCache[ib.ServerID] = nil // negative cache
			}
		}
		var server, client, opts map[string]interface{}
		pin := ""
		if ib.TlsID != 0 {
			if t := getTls(ib.TlsID); t != nil {
				_ = json.Unmarshal([]byte(t.ServerJSON), &server)
				_ = json.Unmarshal([]byte(t.ClientJSON), &client)
				pin = certPin(t, server)
			}
		}
		_ = json.Unmarshal([]byte(ib.Options), &opts)

		// Remark = the node's display name from the 节点 page; the raw inbound tag
		// is only a fallback for an inbound no node is bound to.
		remark := n.Name
		if logicalID == 0 {
			remark = legacyNames[ib.Tag]
		}
		if remark == "" {
			remark = ib.Tag
		}
		cred := singbox.User{Name: owner.ClientName, UUID: owner.ClientUUID, Password: owner.ClientSecret}
		if n.RouteUpstreamInboundID != 0 {
			cred = deriveRouteUser(cred, n.ID)
		}
		exit := ib
		if n.RouteUpstreamInboundID != 0 {
			if routed := inboundByID(inbounds, n.RouteUpstreamInboundID); routed != nil {
				exit = routed
			}
		}
		seenExit := map[int64]bool{}
		for exit != nil && exit.UpstreamInboundID != 0 && !seenExit[exit.ID] {
			seenExit[exit.ID] = true
			exit = inboundByID(inbounds, exit.UpstreamInboundID)
		}
		// 节点名必须稳定：客户端（Clash Verge/v2rayNG）按名字记住手动选中的
		// 节点，名字带动态剩余流量/天数时每次订阅刷新都会改名，手动选择随即
		// 失效、分组回退自动选择。剩余流量/到期由 Subscription-Userinfo 头承载。
		p := singbox.LinkParams{
			Type: ib.Type, Tag: remark, Host: nodeHost, Port: ib.ListenPort,
			UUID: cred.UUID, Password: cred.Password,
			NoUDP:       exit != nil && noUDP(exit.EgressID),
			TLS:         ib.TlsID != 0,
			SNI:         mapStr(server, "server_name"),
			Fingerprint: nestedStr(client, "utls", "fingerprint"),
			Insecure:    mapBool(client, "insecure"),
			PinSHA256:   pin,
			Congestion:  mapStr(opts, "congestion_control"),
			ZeroRTT:     mapBool(opts, "zero_rtt_handshake"), // tuic 0-RTT
			Method:      mapStr(opts, "method"),              // shadowsocks
			ServerKey:   mapStr(opts, "password"),            // shadowsocks-2022 server PSK
			UpMbps:      mapInt(opts, "up_mbps"),             // hysteria v1
			DownMbps:    mapInt(opts, "down_mbps"),
			TCPFastOpen: mapBool(opts, "tcp_fast_open"),
			MPTCP:       mapBool(opts, "tcp_multi_path"),
		}
		// Multiplex + Brutal (vless/vmess/trojan): both are opt-in on the client,
		// so mirror the inbound's setting onto the link or Brutal does nothing.
		if mx, ok := opts["multiplex"].(map[string]interface{}); ok && mapBool(mx, "enabled") {
			p.Mux = true
			if br, ok := mx["brutal"].(map[string]interface{}); ok && mapBool(br, "enabled") {
				// Brutal bandwidths are per-endpoint and mirror across the link: the
				// server's uplink (up_mbps = what clients download) is the client's
				// downlink, and the server's downlink is the client's uplink.
				p.BrutalUp = mapInt(br, "down_mbps")
				p.BrutalDown = mapInt(br, "up_mbps")
			}
		}
		// hysteria2 salamander obfs lives in options as {"obfs":{"type","password"}}.
		if obfs, ok := opts["obfs"].(map[string]interface{}); ok {
			p.Obfs = mapStr(obfs, "type")
			p.ObfsPassword = mapStr(obfs, "password")
		}
		// transport (ws/grpc/httpupgrade) for vless/vmess/trojan
		if tr, ok := opts["transport"].(map[string]interface{}); ok {
			p.Network = mapStr(tr, "type")
			p.Path = mapStr(tr, "path")
			p.ServiceName = mapStr(tr, "service_name")
			p.WSMaxEarlyData = mapInt(tr, "max_early_data")
			p.WSEarlyDataHeader = mapStr(tr, "early_data_header_name")
			if h := mapStr(tr, "host"); h != "" {
				p.WSHost = h
			} else if hdr, ok := tr["headers"].(map[string]interface{}); ok {
				p.WSHost = mapStr(hdr, "Host")
			}
			if p.WSHost == "" && (p.Network == "ws" || p.Network == "httpupgrade") {
				p.WSHost = p.SNI // CDN host defaults to the TLS SNI
			}
		}
		if r, ok := server["reality"].(map[string]interface{}); ok {
			p.PublicKey = nestedStr(client, "reality", "public_key")
			p.ShortID = firstShortID(r["short_id"])
			// VLESS flow: 默认 vision，但 options.flow="none" 时关闭
			if ib.Type == "vless" && mapStr(opts, "flow") != "none" {
				p.Flow = true
			}
		}
		if alpn, ok := server["alpn"].([]interface{}); ok {
			parts := make([]string, 0, len(alpn))
			for _, a := range alpn {
				if str, ok := a.(string); ok {
					parts = append(parts, str)
				}
			}
			p.ALPN = strings.Join(parts, ",")
		}
		if link := singbox.BuildShareLink(p); link != "" {
			out = append(out, SelfBuiltLink{NodeID: logicalID, Tag: ib.Tag, Link: link})
		}
	}
	return out
}

func inboundByID(inbounds []*SbInbound, id int64) *SbInbound {
	for _, ib := range inbounds {
		if ib.ID == id {
			return ib
		}
	}
	return nil
}

func inboundRouteHealthy(inbounds []*SbInbound, first int64) bool {
	seen := map[int64]bool{}
	cur := inboundByID(inbounds, first)
	for cur != nil && !seen[cur.ID] {
		if !cur.Enabled {
			return false
		}
		seen[cur.ID] = true
		if cur.UpstreamInboundID == 0 {
			return true
		}
		cur = inboundByID(inbounds, cur.UpstreamInboundID)
	}
	return false // missing hop or cycle
}

// UserProxy is one mixed (HTTP/SOCKS5) inbound's connection info for a user,
// surfaced as copyable credentials to paste into plain-proxy fields (1Panel /
// Docker / git http.proxy, ...). Mixed inbounds are deliberately excluded from
// the Clash/sing-box subscription (BuildShareLink returns "" for them), so this
// is how a user retrieves them instead.
type UserProxy struct {
	Tag       string `json:"tag"`
	BucketID  int64  `json:"bucket_id"` // owning bucket — target of a credential edit
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	TLS       bool   `json:"tls"`        // true → HTTPS proxy; false → plain HTTP/SOCKS5
	ExpiresAt int64  `json:"expires_at"` // 0 = permanent
	Expired   bool   `json:"expired"`    // credential past its expiry (won't authenticate)
	Custom    bool   `json:"custom"`     // true → a dedicated proxy account, not the node identity
	// Account is true when this node authenticates with the user's account-level
	// credential — the same login on every such node, edited in one place. False
	// means the node still carries a bucket credential of its own (the free
	// bucket, whose traffic must not be charged to a plan).
	Account bool `json:"account"`
	// MeterPlan names the份 this node's traffic is charged to, for the page to
	// state rather than leave the user guessing. Account-level traffic all lands
	// on one份 (see accountMeterBucket), which need not be the one that grants
	// the node.
	MeterPlan string `json:"meter_plan"`
	// Plan is the owning份's own credential, always reported. It authenticates
	// this node whether or not the account credential does (BuildUsersByTag emits
	// both), and a login that works but appears nowhere on the page is a login
	// nobody can use — the ones already pasted into 1Panel/Docker are exactly
	// these. It is also the only way to charge this node's proxy traffic to THIS
	// 份 rather than to whichever份 the account credential meters.
	Plan *ProxyPlanCred `json:"plan"`
}

// ProxyPlanCred is one份's own mixed-proxy credential, as the panel presents it.
// Separate from the UserProxy fields above because those carry whichever
// credential the page recommends for the node — the account-level one when it is
// usable — while this one always describes the份 that grants the node.
type ProxyPlanCred struct {
	BucketID int64 `json:"bucket_id"` // target of a credential edit
	// Name is the份's display name. Two nodes can be granted by different份, so
	// without it "套餐账号" would not say which套餐.
	Name      string `json:"name"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	ExpiresAt int64  `json:"expires_at"` // 0 = permanent
	Expired   bool   `json:"expired"`
	Custom    bool   `json:"custom"` // false → still the system-minted node identity
}

// BuildUserProxies returns the mixed-inbound proxy credentials the user is
// entitled to. It mirrors BuildSelfBuiltLinks' ownership + per-server host
// override so a node hosted on a remote server advertises that server's host.
func (s *Store) BuildUserProxies(u *User, host string) []UserProxy {
	if host == "" {
		return nil
	}
	inbounds, err := s.ListSbInbounds()
	if err != nil {
		return nil
	}
	serverCache := make(map[int64]*Server)
	now := time.Now().Unix()
	owners, _ := s.UserOwnedInbounds(u.ID, now)
	// The account-level credential, if usable, is what every non-free node shows.
	// Resolved once: the charged份 is the same for all of them.
	acct := proxyAcct{name: u.ProxyUsername, password: u.ProxyPassword, expiresAt: u.ProxyExpiresAt}
	meterPlan := ""
	if acct.active(now) {
		meterPlan = s.AccountMeterPlanName(u.ID)
	}
	var out []UserProxy
	for _, ib := range inbounds {
		if !ib.Enabled || ib.Type != "mixed" {
			continue
		}
		owner := owners[ib.Tag]
		if owner == nil {
			continue // no active bucket grants this node
		}
		nodeHost := host
		if ib.ServerID != 0 {
			if sv, ok := serverCache[ib.ServerID]; ok {
				if sv != nil && sv.Host != "" {
					nodeHost = sv.Host
				}
			} else if sv, _ := s.GetServer(ib.ServerID); sv != nil {
				serverCache[ib.ServerID] = sv
				if sv.Host != "" {
					nodeHost = sv.Host
				}
			} else {
				serverCache[ib.ServerID] = nil // negative cache
			}
		}
		plan := &ProxyPlanCred{
			BucketID:  owner.ID,
			Name:      owner.Name,
			Username:  owner.ProxyName(),
			Password:  owner.ProxySecret(),
			ExpiresAt: owner.ProxyExpiresAt,
			Expired:   owner.ProxyExpiresAt != 0 && owner.ProxyExpiresAt <= now,
			Custom:    owner.ProxyUsername != "",
		}
		p := UserProxy{
			Tag:       ib.Tag,
			BucketID:  owner.ID,
			Host:      nodeHost,
			Port:      ib.ListenPort,
			Username:  plan.Username,
			Password:  plan.Password,
			TLS:       ib.TlsID != 0,
			ExpiresAt: plan.ExpiresAt,
			Expired:   plan.Expired,
			Custom:    plan.Custom,
			MeterPlan: owner.Name,
			Plan:      plan,
		}
		// Mirror BuildUsersByTag: every node except a free-owned one also
		// authenticates with the account credential, so that is the one the ready-made
		// proxy URL carries — it survives group moves and renewals, which is the whole
		// point. p.Plan still reports the份 credential beside it: both are live in the
		// config, and hiding one would strand whoever already pasted it somewhere.
		if owner.Kind != KindFree && acct.active(now) {
			p.Username, p.Password = acct.name, acct.password
			p.ExpiresAt, p.Expired = acct.expiresAt, false
			p.Custom, p.Account = true, true
			p.MeterPlan = meterPlan
		}
		out = append(out, p)
	}
	return out
}

func mapStr(m map[string]interface{}, k string) string {
	if m == nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}
func mapInt(m map[string]interface{}, k string) int {
	if m == nil {
		return 0
	}
	if f, ok := m[k].(float64); ok {
		return int(f)
	}
	return 0
}
func mapBool(m map[string]interface{}, k string) bool {
	if m == nil {
		return false
	}
	b, _ := m[k].(bool)
	return b
}
func nestedStr(m map[string]interface{}, k1, k2 string) string {
	if m == nil {
		return ""
	}
	if inner, ok := m[k1].(map[string]interface{}); ok {
		s, _ := inner[k2].(string)
		return s
	}
	return ""
}
func firstShortID(v interface{}) string {
	arr, ok := v.([]interface{})
	if !ok {
		return ""
	}
	for _, e := range arr {
		if s, ok := e.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// BuildUsersByTag computes which identity belongs in each enabled inbound, with
// per-bucket enforcement: each self-built node is owned by exactly one of the
// user's *active* buckets (the soonest-expiring plan that covers it, else the
// traffic pool), so an exhausted/expired plan only drops its own nodes while the
// user's other plans keep working. The owning bucket's identity (its own stats
// key) goes into the inbound, giving sing-box per-bucket traffic accounting.
// Banned users are excluded; admins holding an active plan are provisioned like
// any other user (so an admin account can also be used as a subscription).
// No node groups and no free group means nobody is injected into any inbound —
// a missing free group must not become "every user owns every node".
// inboundUser identifies one emitted user within one inbound, for deduplication.
type inboundUser struct{ tag, name string }

func (s *Store) BuildUsersByTag(now int64) (map[string][]singbox.User, error) {
	seen := map[inboundUser]bool{}
	inbounds, err := s.ListSbInbounds()
	if err != nil {
		return nil, err
	}
	groupCount, _ := s.GroupCount()
	freeGroup, _ := s.GetSettingInt64("free_group_id", 0)

	// Load every bucket whose owner is an eligible (non-banned) user. Admins are
	// included: an admin who buys a plan gets a normal metered bucket and should
	// be usable as a subscription just like any other user. Only active buckets
	// (orderBuckets below) end up provisioned, so plan-less admins add nothing.
	rows, err := s.db.Query(`SELECT ` + bucketCols + bucketFrom + `
		WHERE p.user_id IN (SELECT id FROM users WHERE status!='banned')
		ORDER BY p.user_id, p.id`)
	if err != nil {
		return nil, err
	}
	byUser := map[int64][]*Bucket{}
	for rows.Next() {
		b, err := scanBucket(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		byUser[b.UserID] = append(byUser[b.UserID], b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	accounts, err := s.proxyAccounts()
	if err != nil {
		return nil, err
	}
	aliases, err := s.activeCredentialAliases(now)
	if err != nil {
		return nil, err
	}

	planGroupsCache := map[int64][]int64{}
	planGroups := func(pkgID int64) []int64 {
		if g, ok := planGroupsCache[pkgID]; ok {
			return g
		}
		g, _ := s.PlanGroupIDs(pkgID)
		planGroupsCache[pkgID] = g
		return g
	}

	ordered := map[int64][]ownedBucket{}
	for uid, bs := range byUser {
		if ord := orderBuckets(bs, now, freeGroup, planGroups); len(ord) > 0 {
			ordered[uid] = ord
		}
	}

	out := map[string][]singbox.User{}
	emit := func(ib *SbInbound, b *Bucket, routeNodeID int64) {
		addRaw := func(u singbox.User) {
			key := inboundUser{ib.Tag, u.Name}
			if seen[key] {
				return
			}
			seen[key] = true
			out[ib.Tag] = append(out[ib.Tag], u)
		}
		add := func(u singbox.User) {
			if routeNodeID > 0 {
				u = deriveRouteUser(u, routeNodeID)
			}
			addRaw(u)
		}
		if ib.Type == "mixed" {
			// Keep both account-level and per-plan proxy credentials. Logical routes
			// derive both, so auth_user can distinguish exits on the shared port while
			// traffic accounting still canonicalises the name back to its owner.
			if b.Kind != KindFree {
				if a, ok := accounts[b.UserID]; ok && a.active(now) && b.Active(now) {
					add(singbox.User{Name: a.name, Password: a.password})
				}
			}
			if b.ProxyActive(now) {
				add(singbox.User{Name: b.ProxyName(), Password: b.ProxySecret()})
			}
			return
		}
		primary := singbox.User{Name: b.ClientName, UUID: b.ClientUUID, Password: b.ClientSecret}
		if routeNodeID > 0 {
			primary = deriveRouteUser(primary, routeNodeID)
		}
		addRaw(primary)
		authKey := func(u singbox.User) string {
			switch ib.Type {
			case "vless", "vmess":
				return "uuid:" + u.UUID
			case "trojan", "shadowsocks", "anytls", "hysteria", "hysteria2":
				return "password:" + u.Password
			default: // TUIC and future two-part credentials
				return u.UUID + "\x00" + u.Password
			}
		}
		wireSeen := map[string]bool{authKey(primary): true}
		// Online-upgrade compatibility: accept credentials already imported by an
		// old client while every new subscription renders only the primary above.
		// Alias names encode the current owner, so traffic remains attributed to the
		// right套餐 even when the old credential originally came from another one.
		for _, a := range aliases[b.UserID] {
			statsBase := credentialAliasStatsName(b.ClientName, a.ID)
			var legacy singbox.User
			if routeNodeID > 0 {
				legacy = deriveLegacyRouteUser(statsBase, a.SourceName, a.ClientUUID, a.ClientSecret, routeNodeID)
			} else {
				legacy = singbox.User{Name: statsBase, UUID: a.ClientUUID, Password: a.ClientSecret}
			}
			// The raw primary may also be recorded as an alias because old logical
			// routes hashed its source name. A normal inbound needs no duplicate.
			key := authKey(legacy)
			if wireSeen[key] {
				continue
			}
			wireSeen[key] = true
			addRaw(legacy)
		}
	}

	// Legacy/default nodes inherit the physical inbound's own upstream chain and
	// keep their existing credentials byte-for-byte. Route nodes are handled in
	// the second pass with per-node derived credentials.
	for _, ib := range inbounds {
		if !ib.Enabled {
			continue
		}
		var ibGroups []int64
		if groupCount > 0 {
			ibGroups, _ = s.SelfBuiltNodeGroupIDs(ib.Tag)
		}
		for _, ord := range ordered {
			b := pickOwner(ord, ibGroups, groupCount)
			if b == nil {
				continue
			}
			emit(ib, b, 0)
		}
	}

	byTag := map[string]*SbInbound{}
	for _, ib := range inbounds {
		byTag[ib.Tag] = ib
	}
	nodes, _ := s.ListNodes()
	for _, n := range nodes {
		if !n.Enabled || n.Type != "self_built" || n.RouteUpstreamInboundID == 0 || n.RouteUpstreamBroken {
			continue
		}
		ib := byTag[n.InboundTag]
		if ib == nil || !ib.Enabled || !inboundRouteHealthy(inbounds, n.RouteUpstreamInboundID) {
			continue
		}
		for _, ord := range ordered {
			if b := pickOwner(ord, n.GroupIDs, groupCount); b != nil {
				emit(ib, b, n.ID)
			}
		}
	}
	return out, nil
}

// ownedBucket is one active bucket plus the node groups it covers.
type ownedBucket struct {
	b      *Bucket
	groups map[int64]bool
}

// orderBuckets returns a user's ACTIVE buckets in ownership-priority order:
// plans by soonest expiry (then id), then the traffic pool last. The pool always
// covers the free group (free/unmetered) and, when it has paid balance, the
// union of the user's plan groups as a fallback. planGroups should be a cached
// PlanGroupIDs lookup.
func orderBuckets(bs []*Bucket, now, freeGroup int64, planGroups func(int64) []int64) []ownedBucket {
	allPlanGroups := map[int64]bool{}
	var plans []*Bucket
	var pool, free *Bucket
	for _, b := range bs {
		// A queued plan bucket (a same-package purchase waiting its turn) is not yet
		// usable: it carries no identity into any inbound and contributes no group
		// access until advanceUserQueues promotes it to 'active'.
		if b.Status == "queued" {
			continue
		}
		switch b.Kind {
		case "pool":
			pool = b
			continue
		case KindFree:
			free = b
			continue
		}
		plans = append(plans, b)
		// A zero-limit legacy plan is not a node entitlement. In particular, an
		// active pool/grant must not revive its groups and turn "0" back into an
		// implicit unlimited plan through the fallback path below.
		if b.TrafficLimit > 0 {
			for _, g := range planGroups(b.PackageID) {
				allPlanGroups[g] = true
			}
		}
	}
	sort.Slice(plans, func(i, j int) bool {
		ei, ej := plans[i].ExpiryAt, plans[j].ExpiryAt
		if ei == 0 {
			ei = 1<<63 - 1 // never-expires sorts after dated plans
		}
		if ej == 0 {
			ej = 1<<63 - 1
		}
		if ei != ej {
			return ei < ej
		}
		return plans[i].ID < plans[j].ID
	})
	var ord []ownedBucket
	for _, b := range plans {
		if !b.Active(now) {
			continue
		}
		gs := map[int64]bool{}
		if b.PackageID <= 0 {
			// Grant bucket with no package of its own — the admin comp (package_id=0)
			// or the signup grant (WelcomePackageID). Scope it like the pool: the free
			// group plus the union of the user's plan groups, so the allowance works on
			// whatever nodes the user can already reach.
			if freeGroup > 0 {
				gs[freeGroup] = true
			}
			for g := range allPlanGroups {
				gs[g] = true
			}
		} else {
			for _, g := range planGroups(b.PackageID) {
				gs[g] = true
			}
		}
		ord = append(ord, ownedBucket{b: b, groups: gs})
	}
	// The free group is carried by the free bucket, never by the pool: they are
	// separate stats identities, so free traffic is metered on its own instead of
	// being debited from the pool's paid balance. Falling back to the pool when a
	// user has no free bucket yet would reintroduce exactly that, so an
	// un-backfilled account loses free-group access until EnsureFreeBucket runs
	// rather than silently spending its balance.
	if free != nil && freeGroup > 0 {
		ord = append(ord, ownedBucket{b: free, groups: map[int64]bool{freeGroup: true}})
	}
	if pool != nil {
		if pool.Active(now) && len(allPlanGroups) > 0 {
			gs := map[int64]bool{}
			for g := range allPlanGroups {
				gs[g] = true
			}
			ord = append(ord, ownedBucket{b: pool, groups: gs})
		}
	}
	return ord
}

// pickOwner returns the highest-priority active bucket that covers the inbound's
// node groups, or nil. No groups configured means no owner — do not hand the
// inbound to the first active bucket.
func pickOwner(ord []ownedBucket, ibGroups []int64, groupCount int) *Bucket {
	if len(ord) == 0 || groupCount == 0 {
		return nil
	}
	for _, ob := range ord {
		for _, g := range ibGroups {
			if ob.groups[g] {
				return ob.b
			}
		}
	}
	return nil
}

// userBucketOrder builds this user's priority-ordered active buckets along with
// the configured node-group count, the two inputs pickOwner needs. Split out so
// the tag-keyed and group-keyed owner lookups below can't drift apart — they
// must agree on who owns what, or a node would be listed under one plan and
// billed to another.
func (s *Store) userBucketOrder(userID, now int64) ([]ownedBucket, int, error) {
	buckets, err := s.ListBuckets(userID)
	if err != nil {
		return nil, 0, err
	}
	freeGroup, _ := s.GetSettingInt64("free_group_id", 0)
	groupCount, _ := s.GroupCount()
	planGroupsCache := map[int64][]int64{}
	ord := orderBuckets(buckets, now, freeGroup, func(pkgID int64) []int64 {
		if g, ok := planGroupsCache[pkgID]; ok {
			return g
		}
		g, _ := s.PlanGroupIDs(pkgID)
		planGroupsCache[pkgID] = g
		return g
	})
	return ord, groupCount, nil
}

// UserGroupOwners maps each node group to the bucket that grants this user
// access to it. Same assignment as UserOwnedInbounds, keyed by group instead of
// inbound tag — external nodes carry a group id but no inbound tag, so they have
// nothing to join on there. No groups configured yields an empty map: there is
// no implicit "own everything" owner.
func (s *Store) UserGroupOwners(userID, now int64) (map[int64]*Bucket, error) {
	ord, groupCount, err := s.userBucketOrder(userID, now)
	if err != nil {
		return nil, err
	}
	out := map[int64]*Bucket{}
	if groupCount == 0 {
		return out, nil
	}
	groups, err := s.ListGroups()
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if b := pickOwner(ord, []int64{g.ID}, groupCount); b != nil {
			out[g.ID] = b
		}
	}
	return out, nil
}

// UserOwnedInbounds maps each enabled self-built inbound tag to the bucket that
// currently owns it for this user (its credentials + remaining quota drive the
// subscription link), or omits it when the user has no active bucket covering
// it. Mirrors BuildUsersByTag's assignment for one user.
func (s *Store) UserOwnedInbounds(userID, now int64) (map[string]*Bucket, error) {
	ord, groupCount, err := s.userBucketOrder(userID, now)
	if err != nil {
		return nil, err
	}
	inbounds, err := s.ListSbInbounds()
	if err != nil {
		return nil, err
	}
	out := map[string]*Bucket{}
	for _, ib := range inbounds {
		if !ib.Enabled {
			continue
		}
		var ibGroups []int64
		if groupCount > 0 {
			ibGroups, _ = s.SelfBuiltNodeGroupIDs(ib.Tag)
		}
		if b := pickOwner(ord, ibGroups, groupCount); b != nil {
			out[ib.Tag] = b
		}
	}
	return out, nil
}

// UserOwnedNodes is the logical-route equivalent of UserOwnedInbounds. The
// node's own group membership decides its bucket, so two nodes sharing a
// physical inbound may belong to different plans without broadening access.
func (s *Store) UserOwnedNodes(userID, now int64) (map[int64]*Bucket, error) {
	ord, groupCount, err := s.userBucketOrder(userID, now)
	if err != nil {
		return nil, err
	}
	nodes, err := s.ListNodes()
	if err != nil {
		return nil, err
	}
	out := map[int64]*Bucket{}
	for _, n := range nodes {
		if !n.Enabled || n.Type != "self_built" || n.RouteUpstreamBroken {
			continue
		}
		if b := pickOwner(ord, n.GroupIDs, groupCount); b != nil {
			out[n.ID] = b
		}
	}
	return out, nil
}
