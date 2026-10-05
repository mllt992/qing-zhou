package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ServiceTraffic is one non-overlapping observation surface: inbound user
// counters. NIC and outbound counters are deliberately not added to this total.
type ServiceTraffic struct {
	Up                           int64                  `json:"up"`
	Down                         int64                  `json:"down"`
	Total                        int64                  `json:"total"`
	BillableTotal                int64                  `json:"billable_total"`
	CoverageStart                int64                  `json:"coverage_start"`
	CoverageEnd                  int64                  `json:"coverage_end"`
	NewCoverageStart             int64                  `json:"new_coverage_start"`
	UserCoverageComplete         bool                   `json:"user_coverage_complete"`
	ObservedUserCoverageComplete bool                   `json:"observed_user_coverage_complete"`
	AttributionReady             bool                   `json:"attribution_ready"`
	CoverageReasons              []string               `json:"coverage_reasons"`
	UnallocatedTotal             int64                  `json:"unallocated_total"`
	Users                        []ServiceTrafficUser   `json:"users"`
	HasRelay                     bool                   `json:"has_relay"`
	Sources                      []ServiceTrafficSource `json:"sources"`
	OutboundLinks                []ServiceTrafficSource `json:"outbound_links"`
	Quality                      TrafficMeteringQuality `json:"quality"`
	Surface                      string                 `json:"surface"`
}

// ServiceTrafficUser combines only independently observed direct and relay
// identities for the same owner on this machine. Historical and shared counters
// remain in Sources; they are never allocated by share or copied from an entry.
type ServiceTrafficUser struct {
	UserID        int64  `json:"user_id"`
	Name          string `json:"name"`
	Up            int64  `json:"up"`
	Down          int64  `json:"down"`
	Total         int64  `json:"total"`
	DirectTotal   int64  `json:"direct_total"`
	RelayTotal    int64  `json:"relay_total"`
	BillableTotal int64  `json:"billable_total"`
}
type ServiceTrafficSource struct {
	Kind          string `json:"kind"`
	LinkID        int64  `json:"link_id"`
	UserID        int64  `json:"user_id"`
	Name          string `json:"name"`
	Up            int64  `json:"up"`
	Down          int64  `json:"down"`
	Total         int64  `json:"total"`
	BillableTotal int64  `json:"billable_total"`
}
type TrafficMeteringQuality struct {
	Mode           string `json:"mode"`
	Status         string `json:"status"`
	LastSuccess    int64  `json:"last_success"`
	LastAttempt    int64  `json:"last_attempt"`
	Failures       int    `json:"failures"`
	PendingPolls   int    `json:"pending_polls"`
	Gaps           int    `json:"gaps"`
	HistoryLimited bool   `json:"history_limited"`
	Error          string `json:"error"`
}

func (s *Store) ServerServiceTraffic(serverID, since int64) (ServiceTraffic, error) {
	out := ServiceTraffic{Sources: []ServiceTrafficSource{}, Users: []ServiceTrafficUser{}, CoverageReasons: []string{}, Surface: "inbound_user", Quality: TrafficMeteringQuality{Mode: "reset", Status: "unknown", HistoryLimited: true}}
	// Use one snapshot so concurrent ingestion cannot make Users, Sources and
	// machine totals disagree within a response. modernc ReadOnly bypasses the
	// DSN _txlock=immediate, so this WAL snapshot never reserves the writer lock.
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	// Old binary rollbacks require a compatible snapshot because migrations are
	// versioned; all rows here therefore share the new ledger's ingestion rules.
	err = tx.QueryRow(`SELECT COALESCE(SUM(up),0),COALESCE(SUM(down),0),COALESCE(SUM(CASE WHEN billable=1 AND source_kind='direct_user' THEN up+down ELSE 0 END),0),COALESCE(MIN(ts),0),COALESCE(MAX(ts),0),COALESCE(MIN(CASE WHEN source_kind<>'historical_user' THEN ts END),0) FROM traffic_observations WHERE server_id=? AND ts>=? AND source_kind<>'diagnostic_outbound'`, serverID, since).Scan(&out.Up, &out.Down, &out.BillableTotal, &out.CoverageStart, &out.CoverageEnd, &out.NewCoverageStart)
	if err != nil {
		return out, err
	}
	out.Total = out.Up + out.Down
	rows, err := tx.Query(`SELECT t.source_kind,t.link_id,t.user_id,COALESCE(NULLIF(u.username,''),'已删除用户 #'||t.user_id),COALESCE(l.source_name,''),COALESCE(l.target_name,''),SUM(t.up),SUM(t.down),SUM(CASE WHEN t.source_kind='direct_user' AND t.billable=1 THEN t.up+t.down ELSE 0 END)
 FROM traffic_observations t LEFT JOIN users u ON u.id=t.user_id LEFT JOIN relay_metering_links l ON l.id=t.link_id
 WHERE t.server_id=? AND t.ts>=? AND t.source_kind<>'diagnostic_outbound' GROUP BY t.source_kind,t.link_id,t.user_id HAVING SUM(t.up+t.down)>0 ORDER BY SUM(t.up+t.down) DESC,t.source_kind,t.link_id,t.user_id`, serverID, since)
	if err != nil {
		return out, err
	}
	complete := true
	users := map[int64]*ServiceTrafficUser{}
	for rows.Next() {
		var src ServiceTrafficSource
		var user, from, to string
		if err = rows.Scan(&src.Kind, &src.LinkID, &src.UserID, &user, &from, &to, &src.Up, &src.Down, &src.BillableTotal); err != nil {
			rows.Close()
			return out, err
		}
		src.Total = src.Up + src.Down
		switch src.Kind {
		case "direct_user", "relay_user":
			src.Name = user
			if src.Kind == "relay_user" {
				out.HasRelay = true
			}
		case "historical_user":
			src.Name = user + "（旧用户记录）"
			complete = false
		case "relay_link":
			src.Name = from + " → " + to
			if from == "" && to == "" {
				src.Name = fmt.Sprintf("共享中转链路 #%d", src.LinkID)
			}
			complete = false
			out.HasRelay = true
		case "legacy_shared_relay":
			src.Name = "旧共享中转（未细分入口）"
			complete = false
			out.HasRelay = true
		case "ambiguous_identity":
			src.Name = "身份名称冲突（未扣费）"
			complete = false
		default:
			src.Name = "未知代理身份（未扣费）"
			complete = false
		}
		if (src.Kind == "direct_user" || src.Kind == "relay_user") && src.UserID > 0 {
			u := users[src.UserID]
			if u == nil {
				u = &ServiceTrafficUser{UserID: src.UserID, Name: user}
				users[src.UserID] = u
			}
			u.Up += src.Up
			u.Down += src.Down
			u.Total += src.Total
			u.BillableTotal += src.BillableTotal
			if src.Kind == "direct_user" {
				u.DirectTotal += src.Total
			} else {
				u.RelayTotal += src.Total
			}
		} else {
			out.UnallocatedTotal += src.Total
			complete = false
		}
		out.Sources = append(out.Sources, src)
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	for _, u := range users {
		out.Users = append(out.Users, *u)
	}
	sort.Slice(out.Users, func(i, j int) bool {
		if out.Users[i].Total != out.Users[j].Total {
			return out.Users[i].Total > out.Users[j].Total
		}
		return out.Users[i].UserID < out.Users[j].UserID
	})
	err = tx.QueryRow(`SELECT mode,status,last_success,last_attempt,failures,error FROM traffic_metering_state WHERE server_id=?`, serverID).Scan(&out.Quality.Mode, &out.Quality.Status, &out.Quality.LastSuccess, &out.Quality.LastAttempt, &out.Quality.Failures, &out.Quality.Error)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM traffic_polls WHERE server_id=? AND state='pending'`, serverID).Scan(&out.Quality.PendingPolls); err != nil {
		return out, err
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM traffic_metering_gaps WHERE server_id=? AND ts>=?`, serverID, since).Scan(&out.Quality.Gaps); err != nil {
		return out, err
	}
	if !complete {
		out.CoverageReasons = append(out.CoverageReasons, "unallocated_observations")
	}
	if out.Quality.Status != "ok" {
		out.CoverageReasons = append(out.CoverageReasons, "collection_unhealthy")
	}
	if out.Quality.PendingPolls > 0 {
		out.CoverageReasons = append(out.CoverageReasons, "pending_polls")
	}
	if out.Quality.Gaps > 0 {
		out.CoverageReasons = append(out.CoverageReasons, "metering_gaps")
	}
	if out.CoverageStart == 0 {
		out.CoverageReasons = append(out.CoverageReasons, "no_observations")
	}
	out.ObservedUserCoverageComplete = len(out.CoverageReasons) == 0
	ready, hasIncomingRelay, reasons, err := serviceTrafficAttributionReady(tx, serverID)
	if err != nil {
		return out, err
	}
	out.AttributionReady = ready
	out.HasRelay = out.HasRelay || hasIncomingRelay
	out.CoverageReasons = append(out.CoverageReasons, reasons...)
	out.UserCoverageComplete = out.ObservedUserCoverageComplete && out.AttributionReady
	out.OutboundLinks = []ServiceTrafficSource{}
	outgoing, err := tx.Query(`SELECT t.link_id,COALESCE(l.source_name,'未注册出口'),COALESCE(l.target_name,''),SUM(t.up),SUM(t.down) FROM traffic_observations t LEFT JOIN relay_metering_links l ON l.id=t.link_id WHERE t.server_id=? AND t.ts>=? AND t.source_kind='diagnostic_outbound' GROUP BY t.link_id ORDER BY SUM(t.up+t.down) DESC`, serverID, since)
	if err != nil {
		return out, err
	}
	for outgoing.Next() {
		row := ServiceTrafficSource{Kind: "diagnostic_outbound"}
		var from, to string
		if err = outgoing.Scan(&row.LinkID, &from, &to, &row.Up, &row.Down); err != nil {
			outgoing.Close()
			return out, err
		}
		row.Name = from
		if to != "" {
			row.Name += " → " + to
		}
		row.Total = row.Up + row.Down
		out.OutboundLinks = append(out.OutboundLinks, row)
	}
	if err = outgoing.Close(); err != nil {
		return out, err
	}
	if err = outgoing.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// Attribution readiness describes the deployed paths, independently of whether
// shared credentials actually carried traffic in this observation window. An
// accepted compatibility credential is not evidence of lost bytes, but it is
// insufficient evidence to enable a per-user capacity estimate.
func serviceTrafficAttributionReady(db txLike, serverID int64) (bool, bool, []string, error) {
	reasons := []string{}
	if ready, err := relayVisionAttributionReadyWith(db, serverID, time.Now().Unix()); err != nil {
		return false, false, reasons, err
	} else if !ready {
		reasons = append(reasons, "vision_core_unverified")
	}
	if ready, err := relayTransportAttributionReadyWith(db, serverID, time.Now().Unix()); err != nil {
		return false, false, reasons, err
	} else if !ready {
		reasons = append(reasons, "transport_core_unverified")
	}
	if ready, err := relayTrojanAttributionReadyWith(db, serverID, time.Now().Unix()); err != nil {
		return false, false, reasons, err
	} else if !ready {
		reasons = append(reasons, "trojan_core_unverified")
	}
	// Include both explicit inbound relays and enabled logical node routes.
	// A disabled or broken logical node must not manufacture an active path.
	const incomingRoutes = `WITH incoming AS (
 SELECT r.server_id AS source_server_id,r.id AS source_inbound_id,0 AS route_node_id,i.id AS target_inbound_id
 FROM sb_inbounds i JOIN sb_inbounds r ON r.upstream_inbound_id=i.id
 WHERE i.server_id=? AND i.enabled=1 AND r.enabled=1
 UNION
 SELECT r.server_id,r.id,n.id,i.id
 FROM sb_inbounds i JOIN nodes n ON n.route_upstream_inbound_id=i.id JOIN sb_inbounds r ON r.tag=n.inbound_tag
 WHERE i.server_id=? AND i.enabled=1 AND n.enabled=1 AND n.type='self_built' AND n.route_upstream_broken=0 AND r.enabled=1
 ) `
	var incoming, notReady int
	err := db.QueryRow(incomingRoutes+`SELECT COUNT(*),COALESCE(SUM(CASE WHEN l.id IS NULL OR l.state<>'active' THEN 1 ELSE 0 END),0)
 FROM incoming r LEFT JOIN relay_metering_links l ON l.source_server_id=r.source_server_id AND l.source_inbound_id=r.source_inbound_id AND l.route_node_id=r.route_node_id AND l.target_server_id=? AND l.target_inbound_id=r.target_inbound_id`, serverID, serverID, serverID).Scan(&incoming, &notReady)
	if err != nil {
		return false, false, reasons, err
	}
	if incoming > 0 {
		var enabled int
		if err = db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key IN ('relay_link_metering','relay_user_metering') AND value='true'`).Scan(&enabled); err != nil {
			return false, true, reasons, err
		}
		if enabled != 2 {
			reasons = append(reasons, "per_user_metering_disabled")
		} else {
			var usersNotReady int
			if err = db.QueryRow(incomingRoutes+`SELECT COUNT(*) FROM incoming r JOIN relay_metering_links l ON l.source_server_id=r.source_server_id AND l.source_inbound_id=r.source_inbound_id AND l.route_node_id=r.route_node_id AND l.target_server_id=? AND l.target_inbound_id=r.target_inbound_id
 WHERE NOT EXISTS(SELECT 1 FROM relay_metering_users u WHERE u.link_id=l.id AND u.enabled=1 AND u.state='active' AND u.generation=l.generation)
 OR EXISTS(SELECT 1 FROM relay_metering_users u WHERE u.link_id=l.id AND u.enabled=1 AND u.generation=l.generation AND u.state<>'active')`, serverID, serverID, serverID).Scan(&usersNotReady); err != nil {
				return false, true, reasons, err
			}
			if usersNotReady > 0 {
				reasons = append(reasons, "relay_users_not_active")
			}
		}
		if notReady > 0 {
			reasons = append(reasons, "relay_route_not_ready")
		}
	}
	var legacy, generations int
	if err = db.QueryRow(`SELECT COUNT(*) FROM sb_inbounds i LEFT JOIN relay_legacy_compatibility c ON c.server_id=i.server_id AND c.inbound_id=i.id
 WHERE i.server_id=? AND i.enabled=1 AND i.relay_secret<>'' AND COALESCE(c.state,'active')<>'retired'`, serverID).Scan(&legacy); err != nil {
		return false, incoming > 0, reasons, err
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM relay_metering_generations g JOIN relay_metering_links l ON l.id=g.link_id JOIN sb_inbounds i ON i.id=l.target_inbound_id AND i.server_id=l.target_server_id
 WHERE l.target_server_id=? AND i.enabled=1 AND (g.retired_at=0 OR g.retirement_applied_at=0)`, serverID).Scan(&generations); err != nil {
		return false, incoming > 0, reasons, err
	}
	if legacy > 0 || generations > 0 {
		reasons = append(reasons, "shared_compatibility_active")
	}
	return len(reasons) == 0, incoming > 0 || legacy > 0 || generations > 0, reasons, nil
}

// Retain daily sums after raw inspection data expires. Pending polls and their
// processed markers are never pruned: replaying a partial poll must not debit a
// successful identity twice. No raw counter payload ever contains credentials.
func (s *Store) PruneTrafficLedger(now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	threshold := now.AddDate(0, 0, -90).Unix()
	if _, err = tx.Exec(`DELETE FROM traffic_observations WHERE ts<? AND NOT EXISTS(SELECT 1 FROM traffic_polls p WHERE p.id=traffic_observations.poll_id AND p.state='pending')`, threshold); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM traffic_poll_bindings WHERE poll_id IN (SELECT id FROM traffic_polls WHERE state='done' AND observed_at<?)`, threshold); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE traffic_polls SET payload='' WHERE state='done' AND observed_at<?`, threshold); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM traffic_metering_gaps WHERE ts<?`, now.AddDate(-2, 0, 0).Unix()); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM machine_traffic_daily WHERE day<?`, now.AddDate(-2, 0, 0).UTC().Format("2006-01-02")); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) TrafficCollectionState(serverID int64) (TrafficMeteringQuality, string, error) {
	q := TrafficMeteringQuality{Mode: "reset", Status: "unknown"}
	var epoch string
	err := s.db.QueryRow(`SELECT mode,epoch,status,last_success,last_attempt,failures,error FROM traffic_metering_state WHERE server_id=?`, serverID).Scan(&q.Mode, &epoch, &q.Status, &q.LastSuccess, &q.LastAttempt, &q.Failures, &q.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return q, "", nil
	}
	if err != nil {
		return q, "", fmt.Errorf("read traffic collection state: %w", err)
	}
	return q, epoch, nil
}
