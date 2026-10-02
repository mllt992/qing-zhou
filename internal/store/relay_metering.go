package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"qingzhou/internal/singbox"
)

const RelayMeteringSetting = "relay_link_metering"

// RelayMeteringLink intentionally excludes the encrypted credential from JSON.
// The numeric IDs describe the immediate hop, never a guessed end user.
type RelayMeteringLink struct {
	ID              int64  `json:"id"`
	SourceServerID  int64  `json:"source_server_id"`
	SourceInboundID int64  `json:"source_inbound_id"`
	RouteNodeID     int64  `json:"route_node_id"`
	TargetServerID  int64  `json:"target_server_id"`
	TargetInboundID int64  `json:"target_inbound_id"`
	IdentityName    string `json:"-"`
	Credential      string `json:"-"`
	Generation      int    `json:"generation"`
	State           string `json:"state"`
	CreatedAt       int64  `json:"created_at"`
	AcceptedAt      int64  `json:"accepted_at"`
	ActivatedAt     int64  `json:"activated_at"`
	SourceName      string `json:"source_name"`
	TargetName      string `json:"target_name"`
	SpecHash        string `json:"-"`
}

const relayMeteringCols = `id,source_server_id,source_inbound_id,route_node_id,target_server_id,target_inbound_id,identity_name,credential,generation,state,created_at,accepted_at,activated_at,source_name,target_name,spec_hash`

func scanRelayMetering(row scanner) (*RelayMeteringLink, error) {
	r := new(RelayMeteringLink)
	err := row.Scan(&r.ID, &r.SourceServerID, &r.SourceInboundID, &r.RouteNodeID, &r.TargetServerID, &r.TargetInboundID, &r.IdentityName, &r.Credential, &r.Generation, &r.State, &r.CreatedAt, &r.AcceptedAt, &r.ActivatedAt, &r.SourceName, &r.TargetName, &r.SpecHash)
	return r, err
}
func (s *Store) RelayMeteringEnabled() bool {
	value, err := s.GetSetting(RelayMeteringSetting)
	return err == nil && value == "true"
}
func (s *Store) RelayMeteringLinks() ([]*RelayMeteringLink, error) {
	return relayMeteringLinksWith(s.db)
}
func relayMeteringLinksWith(db txLike) ([]*RelayMeteringLink, error) {
	rows, err := db.Query(`SELECT ` + relayMeteringCols + ` FROM relay_metering_links ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*RelayMeteringLink{}
	for rows.Next() {
		r, e := scanRelayMetering(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) RelayMeteringProgress() (string, error) {
	var stamp string
	err := s.db.QueryRow(`SELECT COALESCE(group_concat(id||':'||state||':'||spec_hash,'|'),'') FROM (SELECT id,state,spec_hash FROM relay_metering_links ORDER BY id)`).Scan(&stamp)
	return stamp, err
}

// PrepareRelayMetering registers an immutable credential before any compiler
// runs. New links remain blocked at entry until the downstream running config
// has acknowledged the same generation. It never performs network actions.
func (s *Store) PrepareRelayMetering() error {
	if !s.RelayMeteringEnabled() {
		return nil
	}
	if len(s.secretKey) == 0 {
		return fmt.Errorf("链路计量需要配置加密密钥，不能明文保存中转凭据")
	}
	inbounds, err := s.ListSbInbounds()
	if err != nil {
		return err
	}
	nodes, err := s.ListNodes()
	if err != nil {
		return err
	}
	byID := map[int64]*SbInbound{}
	byTag := map[string]*SbInbound{}
	for _, ib := range inbounds {
		byID[ib.ID] = ib
		byTag[ib.Tag] = ib
	}
	type edge struct {
		from  *SbInbound
		to    *SbInbound
		route int64
	}
	edges := []edge{}
	for _, ib := range inbounds {
		if ib.Enabled && ib.UpstreamInboundID != 0 {
			edges = append(edges, edge{ib, byID[ib.UpstreamInboundID], 0})
		}
	}
	for _, n := range nodes {
		if n.Enabled && n.Type == "self_built" && n.RouteUpstreamInboundID != 0 && !n.RouteUpstreamBroken {
			if ib := byTag[n.InboundTag]; ib != nil && ib.Enabled {
				edges = append(edges, edge{ib, byID[n.RouteUpstreamInboundID], n.ID})
			}
		}
	}
	// The rollout assumes a DAG. Same-machine edges cannot be independently
	// staged safely with this systemd config-restart backend.
	graph := map[int64][]int64{}
	for _, e := range edges {
		if e.to == nil || !e.to.Enabled {
			return fmt.Errorf("中转入口 %d 的落地不存在或已禁用", e.from.ID)
		}
		if e.from.ServerID == e.to.ServerID {
			return fmt.Errorf("链路计量暂不支持同机多跳（入站 %d）", e.from.ID)
		}
		if err := supportedMeteringLanding(e.to); err != nil {
			return err
		}
		graph[e.from.ServerID] = append(graph[e.from.ServerID], e.to.ServerID)
	}
	colors := map[int64]int{}
	var visit func(int64) bool
	visit = func(id int64) bool {
		if colors[id] == 1 {
			return false
		}
		if colors[id] == 2 {
			return true
		}
		colors[id] = 1
		for _, to := range graph[id] {
			if !visit(to) {
				return false
			}
		}
		colors[id] = 2
		return true
	}
	for id := range graph {
		if !visit(id) {
			return fmt.Errorf("中转链路存在环路，不能启用分阶段计量")
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from.ID != edges[j].from.ID {
			return edges[i].from.ID < edges[j].from.ID
		}
		return edges[i].route < edges[j].route
	})
	for _, e := range edges {
		// The target's protocol/port/TLS changes invalidate readiness; source
		// compiler then holds its previous config until the new target is applied.

		specHash, targetServer, _, er := s.relayTargetSpec(e.to)
		if er != nil {
			return er
		}
		sourceName, targetName := e.from.Tag, e.to.Tag
		if targetServer != nil {
			targetName = targetServer.Name + " / " + e.to.Tag
		}
		if e.from.ServerID != 0 {
			if source, err := s.GetServer(e.from.ServerID); err == nil && source != nil {
				sourceName = source.Name + " / " + e.from.Tag
			}
		} else {
			sourceName = "面板本机 / " + e.from.Tag
		}
		tx, er := s.db.Begin()
		if er != nil {
			return er
		}
		var enabled string
		if er = tx.QueryRow(`SELECT value FROM settings WHERE key=?`, RelayMeteringSetting).Scan(&enabled); er != nil || enabled != "true" {
			tx.Rollback()
			return er
		}
		existing, er := scanRelayMetering(tx.QueryRow(`SELECT `+relayMeteringCols+` FROM relay_metering_links WHERE source_server_id=? AND source_inbound_id=? AND route_node_id=? AND target_server_id=? AND target_inbound_id=?`, e.from.ServerID, e.from.ID, e.route, e.to.ServerID, e.to.ID))

		if er == nil && existing.SpecHash != specHash {
			name, encrypted, credentialErr := s.newRelayMeteringCredential(tx)
			er = credentialErr
			if er == nil {
				generation := existing.Generation + 1
				_, er = tx.Exec(`INSERT INTO relay_metering_generations(link_id,generation,identity_name,credential) VALUES(?,?,?,?)`, existing.ID, generation, name, encrypted)
				if er == nil {
					_, er = tx.Exec(`UPDATE relay_metering_links SET spec_hash=?,generation=?,identity_name=?,credential=?,state='prepared',accepted_at=0,activated_at=0 WHERE id=?`, specHash, generation, name, encrypted, existing.ID)
				}
			}
		} else if errors.Is(er, sql.ErrNoRows) {
			name, encrypted, credentialErr := s.newRelayMeteringCredential(tx)
			er = credentialErr
			if er == nil {
				var result sql.Result
				result, er = tx.Exec(`INSERT INTO relay_metering_links(source_server_id,source_inbound_id,route_node_id,target_server_id,target_inbound_id,identity_name,credential,created_at,source_name,target_name,spec_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, e.from.ServerID, e.from.ID, e.route, e.to.ServerID, e.to.ID, name, encrypted, time.Now().Unix(), sourceName, targetName, specHash)
				if er == nil {
					id, _ := result.LastInsertId()
					_, er = tx.Exec(`INSERT INTO relay_metering_generations(link_id,generation,identity_name,credential) VALUES(?,1,?,?)`, id, name, encrypted)
				}
			}
		}
		if er != nil {
			tx.Rollback()
			return er
		}
		if er = tx.Commit(); er != nil {
			return er
		}
	}
	return nil
}
func supportedMeteringLanding(ib *SbInbound) error {
	switch ib.Type {
	case "vless", "vmess", "trojan", "tuic", "hysteria2", "anytls", "hysteria":
		return nil
	case "shadowsocks":
		var opts map[string]any
		if err := json.Unmarshal([]byte(ib.Options), &opts); err != nil {
			return err
		}
		if method, _ := opts["method"].(string); method == "2022-blake3-aes-128-gcm" || method == "2022-blake3-aes-256-gcm" {
			return nil
		}
	}
	return fmt.Errorf("入站 %s 的协议 %s 尚不支持链路计量；保持旧配置", ib.Tag, ib.Type)
}
func (r *RelayMeteringLink) outboundTag() string {
	return fmt.Sprintf("relay-link-%d-g%d", r.ID, r.Generation)
}
func (s *Store) meteringRelayUser(r *RelayMeteringLink) (singbox.User, error) {
	secret, ok := s.decryptOK(r.Credential)
	if !ok || secret == "" || strings.HasPrefix(secret, encPrefix) {
		return singbox.User{}, fmt.Errorf("链路 %d 凭据解密失败", r.ID)
	}
	id, pw := relayCred(secret)
	return singbox.User{Name: r.IdentityName, UUID: id, Password: pw}, nil
}
func (s *Store) meteredRelayOutbound(from *SbInbound, routeID int64, landing *SbInbound, serverCache map[int64]*Server, tlsCache map[int64]*SbTls) (map[string]interface{}, error) {
	link, err := scanRelayMetering(s.db.QueryRow(`SELECT `+relayMeteringCols+` FROM relay_metering_links WHERE source_server_id=? AND source_inbound_id=? AND route_node_id=? AND target_server_id=? AND target_inbound_id=?`, from.ServerID, from.ID, routeID, landing.ServerID, landing.ID))
	if err != nil {
		return nil, fmt.Errorf("链路未完成注册，保持旧配置")
	}
	if link.State != "accepted" && link.State != "active" {
		return nil, fmt.Errorf("链路 %d 等待落地先接受新身份，入口保持旧配置", link.ID)
	}
	currentSpec, targetServer, targetTLS, err := s.relayTargetSpec(landing)
	if err != nil || currentSpec != link.SpecHash {
		return nil, fmt.Errorf("链路 %d 的目标配置已变化，等待落地重新确认", link.ID)
	}
	if targetServer != nil {
		serverCache[landing.ServerID] = targetServer
	}
	if targetTLS != nil {
		tlsCache[landing.TlsID] = targetTLS
	}
	user, err := s.meteringRelayUser(link)
	if err != nil {
		return nil, err
	}
	return s.relayOutboundWithIdentity(landing, serverCache, tlsCache, &user, link.outboundTag())
}

// RecordRelayConfigApplied is called only after verified config application.
// Readiness is persisted, so panel restarts cannot falsely skip the safe order.
// Old shared credentials remain accepted for compatibility until explicitly
// retired in a future operation; this method never revokes them implicitly.

func (s *Store) RecordRelayConfigApplied(serverID int64, raw []byte) error {
	if !s.RelayMeteringEnabled() {
		return nil
	}
	var cfg struct {
		Inbounds []struct {
			Type  string                   `json:"type"`
			Tag   string                   `json:"tag"`
			Port  int                      `json:"listen_port"`
			Users []map[string]interface{} `json:"users"`
		} `json:"inbounds"`
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
		Experimental struct {
			V2Ray struct {
				Stats struct {
					Users []string `json:"users"`
				} `json:"stats"`
			} `json:"v2ray_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	observed := map[string]bool{}
	outbounds := map[string]bool{}
	for _, n := range cfg.Experimental.V2Ray.Stats.Users {
		observed[n] = true
	}
	for _, ob := range cfg.Outbounds {
		outbounds[ob.Tag] = true
	}
	// Acquire the writer lock BEFORE reading the current definitions, so an
	// administrator edit cannot race the check-and-acknowledge transaction.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	links, err := relayMeteringLinksWith(tx)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, link := range links {
		target, err := meteringInboundWith(tx, link.TargetInboundID)
		if err != nil {
			return err
		}
		if target == nil || target.ServerID != link.TargetServerID {
			continue
		}
		spec, _, _, err := s.relayTargetSpecWith(tx, target)
		if err != nil || spec != link.SpecHash {
			continue
		}
		if link.TargetServerID == serverID && observed[link.IdentityName] {
			user, err := s.meteringRelayUser(link)
			if err != nil {
				return err
			}
			matched := false
			for _, ib := range cfg.Inbounds {
				if ib.Tag != target.Tag || ib.Type != target.Type || ib.Port != target.ListenPort {
					continue
				}
				for _, actual := range ib.Users {
					if meteringUserMatches(target, user, actual) {
						matched = true
					}
				}
			}
			if matched {
				if _, err = tx.Exec(`UPDATE relay_metering_links SET state='accepted',accepted_at=? WHERE id=? AND state='prepared' AND generation=? AND spec_hash=?`, now, link.ID, link.Generation, link.SpecHash); err != nil {
					return err
				}
			}
		}
		if link.SourceServerID == serverID && outbounds[link.outboundTag()] {
			if _, err = tx.Exec(`UPDATE relay_metering_links SET state='active',activated_at=? WHERE id=? AND state='accepted' AND generation=? AND spec_hash=?`, now, link.ID, link.Generation, link.SpecHash); err != nil {
				return err
			}
		}
	}
	if err = s.acknowledgeRelayCredentials(tx, serverID, raw); err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	if _, err = tx.Exec(`INSERT INTO relay_metering_applies(server_id,config_hash,applied_at) VALUES(?,?,?) ON CONFLICT(server_id) DO UPDATE SET config_hash=excluded.config_hash,applied_at=excluded.applied_at`, serverID, hex.EncodeToString(hash[:]), now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) newRelayMeteringCredential(tx *sql.Tx) (string, string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", "", err
	}
	encrypted := s.encrypt(hex.EncodeToString(secret))
	if !strings.HasPrefix(encrypted, encPrefix) {
		return "", "", fmt.Errorf("中转凭据加密失败")
	}
	name := "qzr_l_" + hex.EncodeToString(secret[:12])
	var collision int
	err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM users WHERE proxy_username=? OR client_name=?)+(SELECT COUNT(*) FROM user_plans WHERE proxy_username=? OR client_name=?)+(SELECT COUNT(*) FROM plan_identities WHERE proxy_username=? OR client_name=?)`, name, name, name, name, name, name).Scan(&collision)
	if err != nil {
		return "", "", err
	}
	if collision > 0 {
		return "", "", fmt.Errorf("中转统计身份与已有账号冲突")
	}
	return name, encrypted, nil
}
func (s *Store) relayTargetSpec(ib *SbInbound) (string, *Server, *SbTls, error) {
	return s.relayTargetSpecWith(s.db, ib)
}
func (s *Store) relayTargetSpecWith(db txLike, ib *SbInbound) (string, *Server, *SbTls, error) {
	var sv *Server
	var cert *SbTls
	var err error
	host := "127.0.0.1"
	if ib.ServerID != 0 {
		sv = &Server{ID: ib.ServerID}
		err = db.QueryRow(`SELECT name,host,enabled FROM servers WHERE id=?`, ib.ServerID).Scan(&sv.Name, &sv.Host, &sv.Enabled)
		if err != nil || sv == nil || !sv.Enabled {
			return "", nil, nil, fmt.Errorf("落地服务器不存在或已禁用")
		}
		host = sv.Host
	}
	tlsServer, tlsClient := "", ""
	var certID int64
	if ib.TlsID != 0 {
		cert = &SbTls{ID: ib.TlsID}
		err = db.QueryRow(`SELECT server_json,client_json,cert_id FROM sb_tls WHERE id=?`, ib.TlsID).Scan(&cert.ServerJSON, &cert.ClientJSON, &cert.CertID)
		var ok bool
		cert.ServerJSON, ok = s.decryptOK(cert.ServerJSON)
		cert.DecryptFailed = !ok
		if err != nil || cert == nil || cert.DecryptFailed {
			return "", nil, nil, fmt.Errorf("落地TLS不可用")
		}
		tlsServer, tlsClient, certID = cert.ServerJSON, cert.ClientJSON, cert.CertID
	}
	spec, _ := json.Marshal(struct {
		Type, Tag, Listen, Options, Host, TLSServer, TLSClient string
		Port                                                   int
		CertID                                                 int64
	}{ib.Type, ib.Tag, ib.Listen, ib.Options, host, tlsServer, tlsClient, ib.ListenPort, certID})
	h := sha256.Sum256(spec)
	return hex.EncodeToString(h[:]), sv, cert, nil
}
func meteringUserMatches(ib *SbInbound, u singbox.User, actual map[string]interface{}) bool {
	if actual["name"] != u.Name {
		return false
	}
	switch ib.Type {
	case "vless", "vmess":
		return actual["uuid"] == u.UUID
	case "tuic":
		return actual["uuid"] == u.UUID && actual["password"] == u.Password
	case "hysteria":
		return actual["auth_str"] == u.Password
	case "shadowsocks":
		var opts map[string]interface{}
		_ = json.Unmarshal([]byte(ib.Options), &opts)
		method, _ := opts["method"].(string)
		return actual["password"] == singbox.DeriveSSKey(u.Password, method)
	default:
		return actual["password"] == u.Password
	}
}

// Every historic generation stays observable and accepted during compatibility
// mode. Its immutable link retains the original machine ownership on moves.
func (s *Store) relayMeteringAcceptedGenerations() ([]*RelayMeteringLink, error) {
	rows, err := s.db.Query(`SELECT l.id,l.source_server_id,l.source_inbound_id,l.route_node_id,l.target_server_id,l.target_inbound_id,g.identity_name,g.credential,g.generation,l.state,l.created_at,l.accepted_at,l.activated_at,l.source_name,l.target_name,l.spec_hash FROM relay_metering_links l JOIN relay_metering_generations g ON g.link_id=l.id WHERE g.retired_at=0 AND (g.accepted_at>0 OR ?=1) ORDER BY l.id,g.generation`, s.RelayMeteringEnabled())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RelayMeteringLink
	for rows.Next() {
		r, err := scanRelayMetering(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ConfigureTrafficMetering keeps both feature switches atomic. Existing
// cumulative nodes never re-enter a reset reader through a settings toggle.
func (s *Store) ConfigureTrafficMetering(links, cumulative bool) error {
	if links && len(s.secretKey) == 0 {
		return fmt.Errorf("链路计量需要配置加密密钥")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var started int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM traffic_metering_state WHERE mode='cumulative'`).Scan(&started); err != nil {
		return err
	}
	if started > 0 && !cumulative {
		return fmt.Errorf("已有节点进入累计采集，不能直接切回清零；请保留累计模式，避免重复扣费")
	}
	if !links {
		var incompatible int
		err = tx.QueryRow(`SELECT COUNT(*) FROM relay_legacy_compatibility c JOIN sb_inbounds i ON i.id=c.inbound_id AND i.server_id=c.server_id WHERE c.state<>'active' AND i.enabled=1 AND (EXISTS(SELECT 1 FROM sb_inbounds r WHERE r.enabled=1 AND r.upstream_inbound_id=i.id) OR EXISTS(SELECT 1 FROM nodes n WHERE n.enabled=1 AND n.route_upstream_inbound_id=i.id))`).Scan(&incompatible)
		if err != nil {
			return err
		}
		if incompatible > 0 {
			return fmt.Errorf("请先恢复并确认落地的旧共享凭据，再关闭链路计量")
		}
	}

	boolString := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}
	for k, v := range map[string]bool{RelayMeteringSetting: links, "traffic_cumulative_metering": cumulative} {
		if _, err = tx.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, boolString(v)); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.invalidateSettingsCache()
	return nil
}

func (s *Store) validateMeteringRoutes(base, listen string, relays []singbox.Relay) error {
	if !s.RelayMeteringEnabled() {
		return nil
	}
	tags := map[string]bool{}
	for _, r := range relays {
		tag, _ := r.Outbound["tag"].(string)
		if strings.HasPrefix(tag, "relay-link-") {
			for _, ib := range r.InboundTags {
				tags[ib] = true
			}
		}
	}
	if len(tags) == 0 {
		return nil
	}
	if listen == "" {
		return fmt.Errorf("中转入口的用户统计不可用，不能切换独立链路身份")
	}
	var cfg struct {
		Route struct {
			Rules []map[string]interface{} `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(base), &cfg); err != nil {
		return err
	}
	for _, rule := range cfg.Route.Rules {
		_, routes := rule["outbound"]
		action, _ := rule["action"].(string)
		if !routes && action != "route" {
			continue
		}
		if inbound, ok := rule["inbound"].([]interface{}); ok && len(inbound) > 0 {
			overlaps := false
			for _, v := range inbound {
				if name, ok := v.(string); ok && tags[name] {
					overlaps = true
				}
			}
			if !overlaps {
				continue
			}
		}
		return fmt.Errorf("已有自定义出口规则优先于中转计量路由，请先明确规则优先级；保持旧配置")
	}
	return nil
}

func meteringInboundWith(db txLike, id int64) (*SbInbound, error) {
	ib := new(SbInbound)
	err := db.QueryRow(`SELECT id,server_id,type,tag,listen,listen_port,tls_id,options,enabled,relay_secret FROM sb_inbounds WHERE id=?`, id).Scan(&ib.ID, &ib.ServerID, &ib.Type, &ib.Tag, &ib.Listen, &ib.ListenPort, &ib.TlsID, &ib.Options, &ib.Enabled, &ib.RelaySecret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ib, err
}
