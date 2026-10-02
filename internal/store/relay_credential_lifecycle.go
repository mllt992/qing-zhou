package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"qingzhou/internal/singbox"
)

type RelayCredentialView struct {
	Kind       string `json:"kind"`
	LinkID     int64  `json:"link_id"`
	ServerID   int64  `json:"server_id"`
	InboundID  int64  `json:"inbound_id"`
	Generation int    `json:"generation"`
	State      string `json:"state"`
	Name       string `json:"name"`
	CanRetire  bool   `json:"can_retire"`
	Reason     string `json:"reason"`
}

func (s *Store) LegacyRelayCompatibility(serverID, inboundID int64) (string, error) {
	return legacyCompatibilityWith(s.db, serverID, inboundID)
}
func legacyCompatibilityWith(db txLike, serverID, inboundID int64) (string, error) {
	var state string
	err := db.QueryRow(`SELECT state FROM relay_legacy_compatibility WHERE server_id=? AND inbound_id=?`, serverID, inboundID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "active", nil
	}
	return state, err
}
func (s *Store) RelayCredentialViews() ([]RelayCredentialView, error) {
	rows, err := s.db.Query(`SELECT l.id,l.target_server_id,l.target_inbound_id,g.generation,l.generation,g.retired_at,g.accepted_at,g.retirement_applied_at,l.source_name,l.target_name FROM relay_metering_links l JOIN relay_metering_generations g ON g.link_id=l.id ORDER BY l.id,g.generation`)
	if err != nil {
		return nil, err
	}
	out := []RelayCredentialView{}
	for rows.Next() {
		var v RelayCredentialView
		var current int
		var retired, accepted, applied int64
		var from, to string
		if err = rows.Scan(&v.LinkID, &v.ServerID, &v.InboundID, &v.Generation, &current, &retired, &accepted, &applied, &from, &to); err != nil {
			rows.Close()
			return nil, err
		}
		v.Kind = "generation"
		v.Name = fmt.Sprintf("%s → %s 第%d代", from, to, v.Generation)
		switch {
		case retired > 0 && applied > 0:
			v.State = "retired"
		case retired > 0:
			v.State = "retiring"
		case v.Generation == current:
			v.State = "current"
		case accepted == 0:
			v.State = "restoring"
		default:
			v.State = "active"
		}
		out = append(out, v)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(`SELECT DISTINCT i.id,i.server_id,i.tag FROM sb_inbounds i JOIN relay_metering_links l ON l.target_inbound_id=i.id AND l.target_server_id=i.server_id WHERE i.relay_secret<>'' ORDER BY i.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		v := RelayCredentialView{Kind: "legacy", Name: "旧共享中转"}
		var tag string
		if err = rows.Scan(&v.InboundID, &v.ServerID, &tag); err != nil {
			rows.Close()
			return nil, err
		}
		v.Name = "旧共享中转 / " + tag
		out = append(out, v)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		v := &out[i]
		if v.Kind == "legacy" {
			v.State, err = s.LegacyRelayCompatibility(v.ServerID, v.InboundID)
			if err != nil {
				return nil, err
			}
		}
		if v.State == "active" {
			if err = s.canRetireRelayCredential(s.db, *v); err == nil {
				v.CanRetire = true
			} else {
				v.Reason = err.Error()
			}
		}
	}
	return out, nil
}

// Retirement verifies every managed dependent and two complete collection
// passes after the last old-identity traffic. The administrator must separately
// confirm that any manually configured consumers have also migrated.
func (s *Store) canRetireRelayCredential(db txLike, v RelayCredentialView) error {
	var boundary int64
	var name string
	if v.Kind == "generation" {
		var current int
		var state string
		err := db.QueryRow(`SELECT l.generation,l.state,l.activated_at,g.identity_name FROM relay_metering_links l JOIN relay_metering_generations g ON g.link_id=l.id WHERE l.id=? AND g.generation=? AND l.target_server_id=? AND l.target_inbound_id=?`, v.LinkID, v.Generation, v.ServerID, v.InboundID).Scan(&current, &state, &boundary, &name)
		if err != nil {
			return err
		}
		if v.Generation >= current || state != "active" {
			return fmt.Errorf("新一代入口尚未完成切换，不能停用当前凭据")
		}
	} else if v.Kind == "legacy" {
		name = fmt.Sprintf("relay_%d", v.InboundID)
		rows, err := db.Query(`SELECT r.server_id,r.id,0 FROM sb_inbounds r WHERE r.enabled=1 AND r.upstream_inbound_id=? UNION SELECT i.server_id,i.id,n.id FROM nodes n JOIN sb_inbounds i ON i.tag=n.inbound_tag WHERE n.type='self_built' AND n.enabled=1 AND i.enabled=1 AND n.route_upstream_broken=0 AND n.route_upstream_inbound_id=?`, v.InboundID, v.InboundID)
		if err != nil {
			return err
		}
		var deps [][3]int64
		for rows.Next() {
			var e [3]int64
			if err = rows.Scan(&e[0], &e[1], &e[2]); err != nil {
				rows.Close()
				return err
			}
			deps = append(deps, e)
		}
		if err = rows.Close(); err != nil {
			return err
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(deps) == 0 {
			return fmt.Errorf("没有可验证的受管入口，请先核查线路")
		}
		for _, e := range deps {
			var state string
			var at int64
			err = db.QueryRow(`SELECT state,activated_at FROM relay_metering_links WHERE source_server_id=? AND source_inbound_id=? AND route_node_id=? AND target_server_id=? AND target_inbound_id=?`, e[0], e[1], e[2], v.ServerID, v.InboundID).Scan(&state, &at)
			if err != nil || state != "active" {
				return fmt.Errorf("仍有受管入口未切换，保留旧共享凭据")
			}
			if at > boundary {
				boundary = at
			}
		}
	} else {
		return fmt.Errorf("无效凭据类型")
	}
	var lastPositive int64
	var pending, successful int
	if err := db.QueryRow(`SELECT COALESCE(MAX(ts),0) FROM traffic_observations WHERE server_id=? AND counter_name=? AND up+down>0`, v.ServerID, name).Scan(&lastPositive); err != nil {
		return err
	}
	if lastPositive > boundary {
		boundary = lastPositive
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM traffic_polls WHERE server_id=? AND state='pending'`, v.ServerID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return fmt.Errorf("落地还有未入库批次，先完成计数排空")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM traffic_polls WHERE server_id=? AND state='done' AND observed_at>?`, v.ServerID, boundary).Scan(&successful); err != nil {
		return err
	}
	if successful < 2 {
		return fmt.Errorf("等待旧身份最后流量后的两次成功采集，再停用凭据")
	}
	return nil
}
func (s *Store) ChangeRelayCredential(actorID int64, v RelayCredentialView, action string) error {
	if actorID <= 0 {
		return fmt.Errorf("需要管理员并保持链路计量启用")
	}
	if action != "retire" && action != "restore" {
		return fmt.Errorf("无效凭据操作")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var enabled string
	if err = tx.QueryRow(`SELECT value FROM settings WHERE key=?`, RelayMeteringSetting).Scan(&enabled); err != nil || enabled != "true" {
		return fmt.Errorf("需要保持链路计量启用，不能与关闭操作交错")
	}
	if v.Kind == "legacy" && (v.LinkID != 0 || v.Generation != 0) {
		return fmt.Errorf("无效旧共享凭据标识")
	}
	target, err := meteringInboundWith(tx, v.InboundID)
	if err != nil || target == nil || target.ServerID != v.ServerID {
		return fmt.Errorf("目标入站不存在或已移动")
	}
	if action == "retire" {
		if err = s.canRetireRelayCredential(tx, v); err != nil {
			return err
		}
	}
	now := time.Now().Unix()
	if v.Kind == "legacy" {
		state := "retiring"
		if action == "restore" {
			state = "restoring"
		}
		_, err = tx.Exec(`INSERT INTO relay_legacy_compatibility(server_id,inbound_id,state,updated_at) VALUES(?,?,?,?) ON CONFLICT(server_id,inbound_id) DO UPDATE SET state=excluded.state,updated_at=excluded.updated_at`, v.ServerID, v.InboundID, state, now)
	} else if v.Kind == "generation" {
		retired := now
		if action == "restore" {
			retired = 0
		}
		var result sql.Result
		result, err = tx.Exec(`UPDATE relay_metering_generations SET retired_at=?,accepted_at=0,retirement_applied_at=0 WHERE link_id=? AND generation=? AND EXISTS(SELECT 1 FROM relay_metering_links l WHERE l.id=link_id AND l.target_server_id=? AND l.target_inbound_id=? AND l.generation>?)`, retired, v.LinkID, v.Generation, v.ServerID, v.InboundID, v.Generation)
		if err == nil {
			n, _ := result.RowsAffected()
			if n != 1 {
				return fmt.Errorf("只能变更本链路已被替代的旧代凭据")
			}
		}
	} else {
		return fmt.Errorf("无效凭据类型")
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO relay_credential_audit(actor_id,server_id,inbound_id,link_id,generation,action,ts) VALUES(?,?,?,?,?,?,?)`, actorID, v.ServerID, v.InboundID, v.LinkID, v.Generation, action, now); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) acknowledgeRelayCredentials(tx *sql.Tx, serverID int64, raw []byte) error {
	var cfg struct {
		Inbounds []struct {
			Tag   string                   `json:"tag"`
			Type  string                   `json:"type"`
			Port  int                      `json:"listen_port"`
			Users []map[string]interface{} `json:"users"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	present := map[string]bool{}
	for _, ib := range cfg.Inbounds {
		for _, u := range ib.Users {
			if name, ok := u["name"].(string); ok {
				present[name] = true
			}
		}
	}
	now := time.Now().Unix()
	rows, err := tx.Query(`SELECT g.link_id,g.generation,g.identity_name,g.retired_at FROM relay_metering_generations g JOIN relay_metering_links l ON l.id=g.link_id WHERE l.target_server_id=?`, serverID)
	if err != nil {
		return err
	}
	type generation struct {
		id      int64
		gen     int
		name    string
		retired int64
	}
	var gens []generation
	for rows.Next() {
		var g generation
		if err = rows.Scan(&g.id, &g.gen, &g.name, &g.retired); err != nil {
			rows.Close()
			return err
		}
		gens = append(gens, g)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, g := range gens {
		if g.retired > 0 && !present[g.name] {
			_, err = tx.Exec(`UPDATE relay_metering_generations SET retirement_applied_at=? WHERE link_id=? AND generation=? AND retired_at>0`, now, g.id, g.gen)
		} else if g.retired == 0 && present[g.name] {
			_, err = tx.Exec(`UPDATE relay_metering_generations SET accepted_at=? WHERE link_id=? AND generation=? AND retired_at=0`, now, g.id, g.gen)
		}
		if err != nil {
			return err
		}
	}
	rows, err = tx.Query(`SELECT inbound_id,state FROM relay_legacy_compatibility WHERE server_id=? AND state IN ('retiring','restoring')`, serverID)
	if err != nil {
		return err
	}
	type compat struct {
		id    int64
		state string
	}
	var pending []compat
	for rows.Next() {
		var c compat
		if err = rows.Scan(&c.id, &c.state); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, c)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, c := range pending {
		name := fmt.Sprintf("relay_%d", c.id)
		state := ""
		if c.state == "retiring" && !present[name] {
			state = "retired"
		}
		if c.state == "restoring" {
			target, e := meteringInboundWith(tx, c.id)
			if e != nil {
				return e
			}
			if target == nil || target.ServerID != serverID {
				continue
			}
			id, pw := relayCred(target.RelaySecret)
			user := singbox.User{Name: name, UUID: id, Password: pw}
			for _, ib := range cfg.Inbounds {
				if ib.Tag != target.Tag || ib.Type != target.Type || ib.Port != target.ListenPort {
					continue
				}
				for _, u := range ib.Users {
					if meteringUserMatches(target, user, u) {
						state = "active"
					}
				}
			}
		}
		if state != "" {
			if _, err = tx.Exec(`UPDATE relay_legacy_compatibility SET state=?,updated_at=? WHERE server_id=? AND inbound_id=? AND state=?`, state, now, serverID, c.id, c.state); err != nil {
				return err
			}
		}
	}
	return nil
}
