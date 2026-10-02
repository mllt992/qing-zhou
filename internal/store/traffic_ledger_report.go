package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ServiceTraffic is one non-overlapping observation surface: inbound user
// counters. NIC and outbound counters are deliberately not added to this total.
type ServiceTraffic struct {
	Up                   int64                  `json:"up"`
	Down                 int64                  `json:"down"`
	Total                int64                  `json:"total"`
	BillableTotal        int64                  `json:"billable_total"`
	CoverageStart        int64                  `json:"coverage_start"`
	CoverageEnd          int64                  `json:"coverage_end"`
	NewCoverageStart     int64                  `json:"new_coverage_start"`
	UserCoverageComplete bool                   `json:"user_coverage_complete"`
	HasRelay             bool                   `json:"has_relay"`
	Sources              []ServiceTrafficSource `json:"sources"`
	OutboundLinks        []ServiceTrafficSource `json:"outbound_links"`
	Quality              TrafficMeteringQuality `json:"quality"`
	Surface              string                 `json:"surface"`
}
type ServiceTrafficSource struct {
	Kind   string `json:"kind"`
	LinkID int64  `json:"link_id"`
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
	Up     int64  `json:"up"`
	Down   int64  `json:"down"`
	Total  int64  `json:"total"`
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
	out := ServiceTraffic{Sources: []ServiceTrafficSource{}, Surface: "inbound_user", Quality: TrafficMeteringQuality{Mode: "reset", Status: "unknown", HistoryLimited: true}}
	// Old binary rollbacks require a compatible snapshot because migrations are
	// versioned; all rows here therefore share the new ledger's ingestion rules.
	err := s.db.QueryRow(`SELECT COALESCE(SUM(up),0),COALESCE(SUM(down),0),COALESCE(SUM(CASE WHEN billable=1 THEN up+down ELSE 0 END),0),COALESCE(MIN(ts),0),COALESCE(MAX(ts),0),COALESCE(MIN(CASE WHEN source_kind<>'historical_user' THEN ts END),0) FROM traffic_observations WHERE server_id=? AND ts>=? AND source_kind<>'diagnostic_outbound'`, serverID, since).Scan(&out.Up, &out.Down, &out.BillableTotal, &out.CoverageStart, &out.CoverageEnd, &out.NewCoverageStart)
	if err != nil {
		return out, err
	}
	out.Total = out.Up + out.Down
	rows, err := s.db.Query(`SELECT t.source_kind,t.link_id,t.user_id,COALESCE(NULLIF(u.username,''),'已删除用户 #'||t.user_id),COALESCE(l.source_name,''),COALESCE(l.target_name,''),SUM(t.up),SUM(t.down)
 FROM traffic_observations t LEFT JOIN users u ON u.id=t.user_id LEFT JOIN relay_metering_links l ON l.id=t.link_id
 WHERE t.server_id=? AND t.ts>=? AND t.source_kind<>'diagnostic_outbound' GROUP BY t.source_kind,t.link_id,t.user_id HAVING SUM(t.up+t.down)>0 ORDER BY SUM(t.up+t.down) DESC,t.source_kind,t.link_id,t.user_id`, serverID, since)
	if err != nil {
		return out, err
	}
	complete := true
	for rows.Next() {
		var src ServiceTrafficSource
		var user, from, to string
		if err = rows.Scan(&src.Kind, &src.LinkID, &src.UserID, &user, &from, &to, &src.Up, &src.Down); err != nil {
			rows.Close()
			return out, err
		}
		src.Total = src.Up + src.Down
		switch src.Kind {
		case "direct_user":
			src.Name = user
		case "historical_user":
			src.Name = user + "（旧用户记录）"
			complete = false
		case "relay_link":
			src.Name = from + " → " + to
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
		out.Sources = append(out.Sources, src)
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	err = s.db.QueryRow(`SELECT mode,status,last_success,last_attempt,failures,error FROM traffic_metering_state WHERE server_id=?`, serverID).Scan(&out.Quality.Mode, &out.Quality.Status, &out.Quality.LastSuccess, &out.Quality.LastAttempt, &out.Quality.Failures, &out.Quality.Error)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM traffic_polls WHERE server_id=? AND state='pending'`, serverID).Scan(&out.Quality.PendingPolls); err != nil {
		return out, err
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM traffic_metering_gaps WHERE server_id=? AND ts>=?`, serverID, since).Scan(&out.Quality.Gaps); err != nil {
		return out, err
	}
	var targeted int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM sb_inbounds i WHERE i.server_id=? AND (EXISTS(SELECT 1 FROM sb_inbounds r WHERE r.enabled=1 AND r.upstream_inbound_id=i.id) OR EXISTS(SELECT 1 FROM nodes n WHERE n.enabled=1 AND n.route_upstream_inbound_id=i.id))`, serverID).Scan(&targeted); err != nil {
		return out, err
	}
	out.HasRelay = out.HasRelay || targeted > 0
	out.UserCoverageComplete = complete && !out.HasRelay && out.Quality.Status == "ok" && out.Quality.PendingPolls == 0 && out.Quality.Gaps == 0 && out.CoverageStart > 0
	out.OutboundLinks = []ServiceTrafficSource{}
	outgoing, err := s.db.Query(`SELECT t.link_id,COALESCE(l.source_name,'未注册出口'),COALESCE(l.target_name,''),SUM(t.up),SUM(t.down) FROM traffic_observations t LEFT JOIN relay_metering_links l ON l.id=t.link_id WHERE t.server_id=? AND t.ts>=? AND t.source_kind='diagnostic_outbound' GROUP BY t.link_id ORDER BY SUM(t.up+t.down) DESC`, serverID, since)
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
