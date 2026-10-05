package store

import (
	"time"

	"qingzhou/internal/sbver"
)

// LocalNodeID is the server_id standing for the panel's own machine, which runs
// sing-box but has no servers row. The same convention the rest of the code
// already uses for "local" (BuildSingboxConfigForServer, ScheduleRebuildServer).
const LocalNodeID int64 = 0

// LocalNodeName is what that machine is called wherever it is listed next to
// real servers — the node version list, the monitor dashboard, its alerts.
// One constant so the machine does not end up with a different name per page.
const LocalNodeName = "面板本机"

// NodeSingbox is what the panel last observed about one node's sing-box binary.
type NodeSingbox struct {
	ServerID    int64  `json:"server_id"`
	Version     string `json:"version"`
	HasV2RayAPI bool   `json:"has_v2ray_api"`
	// Derived from the preserved version marker, never from version ordering.
	HasVisionFramingFix       bool   `json:"has_vision_framing_fix"`
	HasTransportReadBufferFix bool   `json:"has_transport_read_buffer_fix"`
	HasTrojanHandshakeFix     bool   `json:"has_trojan_handshake_fix"`
	Raw                       string `json:"raw"`
	CheckedAt                 int64  `json:"checked_at"`
	// Error is why the last probe failed. A failed probe never clears the
	// previously observed version: "the node is unreachable right now" and "the
	// node has no sing-box" are different answers, and overwriting the former
	// with a blank would present the wrong one.
	Error string `json:"error"`
}

// maxRawLen bounds what a node can write into the panel's database through the
// version string. The real first line is ~30 characters.
const maxRawLen = 200

// SetNodeSingbox records a successful probe.
func (s *Store) SetNodeSingbox(serverID int64, info sbver.Info) error {
	raw := info.Raw
	if len(raw) > maxRawLen {
		raw = raw[:maxRawLen]
	}
	_, err := s.db.Exec(`INSERT INTO node_singbox (server_id, version, v2ray_api, raw, checked_at, error)
		VALUES (?,?,?,?,?,'')
		ON CONFLICT(server_id) DO UPDATE SET
		  version=excluded.version, v2ray_api=excluded.v2ray_api,
		  raw=excluded.raw, checked_at=excluded.checked_at, error=''`,
		serverID, info.Version, b2i(info.HasV2RayAPI), raw, time.Now().Unix())
	return err
}

// SetNodeSingboxError records a failed probe, keeping whatever version was last
// observed so the panel can still say "was 1.13.18, unreachable since".
func (s *Store) SetNodeSingboxError(serverID int64, msg string) error {
	if len(msg) > maxRawLen {
		msg = msg[:maxRawLen]
	}
	_, err := s.db.Exec(`INSERT INTO node_singbox (server_id, checked_at, error)
		VALUES (?,?,?)
		ON CONFLICT(server_id) DO UPDATE SET checked_at=excluded.checked_at, error=excluded.error`,
		serverID, time.Now().Unix(), msg)
	return err
}

// NodeSingboxAll returns every observation, keyed by server id.
func (s *Store) NodeSingboxAll() (map[int64]*NodeSingbox, error) {
	rows, err := s.db.Query(`SELECT server_id, version, v2ray_api, raw, checked_at, error FROM node_singbox`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*NodeSingbox{}
	for rows.Next() {
		var n NodeSingbox
		var api int
		if err := rows.Scan(&n.ServerID, &n.Version, &api, &n.Raw, &n.CheckedAt, &n.Error); err != nil {
			return nil, err
		}
		n.HasV2RayAPI = api != 0
		n.HasVisionFramingFix = sbver.HasVisionFramingFix(n.Version)
		n.HasTransportReadBufferFix = sbver.HasTransportReadBufferFix(n.Version)
		n.HasTrojanHandshakeFix = sbver.HasTrojanHandshakeFix(n.Version)
		out[n.ServerID] = &n
	}
	return out, rows.Err()
}

// DeleteNodeSingbox drops a node's observation, called when its server is
// removed so the row does not outlive the thing it describes.
func (s *Store) DeleteNodeSingbox(serverID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM node_singbox WHERE server_id=?`, serverID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM node_vision_runtime WHERE server_id=?`, serverID); err != nil {
		return err
	}
	return tx.Commit()
}
