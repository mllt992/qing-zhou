package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"qingzhou/internal/sbver"
	"qingzhou/internal/singbox"
)

// Activation needs a recent explicit probe. This is only an operator preflight;
// the controller independently probes the installed and running cores before
// applying P1 Vision configurations, so persisted observations cannot authorize
// a downgraded process. A manual "重新检测" refreshes this observation.
const relayVisionProbeMaxAge = 15 * time.Minute

func relayInboundUsesVision(ib *SbInbound) bool {
	if ib == nil || ib.Type != "vless" {
		return false
	}
	opts := map[string]interface{}{}
	_ = json.Unmarshal([]byte(ib.Options), &opts)
	if opts == nil {
		opts = map[string]interface{}{}
	}
	if ib.TlsID != 0 {
		opts["tls"] = true
	}
	return singbox.VLESSUserFlow(opts) == "xtls-rprx-vision"
}

// Read the same enabled physical/logical routes as P1's activation preflight.
// Both the Vision listener and its managed client execute the affected framing
// code. A Vision customer-facing entry also needs the fix even when its next
// managed hop is plaintext. This is per machine, not per user or credential.
func relayVisionTopologyWith(db txLike) (map[int64]bool, map[int64][]int64, error) {
	return relayCapabilityTopologyWith(db, relayInboundUsesVision, "Vision")
}

func relayCapabilityTopologyWith(db txLike, uses func(*SbInbound) bool, capability string) (map[int64]bool, map[int64][]int64, error) {
	return relayCapabilityTopologyModeWith(db, uses, capability, true)
}

// relayCapabilityTopologyModeWith: callerNeedsFix=false is for server-only
// fixes (Trojan #87), where the relay caller only runs the protocol client.
func relayCapabilityTopologyModeWith(db txLike, uses func(*SbInbound) bool, capability string, callerNeedsFix bool) (map[int64]bool, map[int64][]int64, error) {
	rows, err := db.Query(`SELECT i.id,i.server_id,i.type,i.tag,i.tls_id,i.options,i.upstream_inbound_id FROM sb_inbounds i WHERE i.enabled=1 AND i.upstream_inbound_id<>0
 UNION ALL SELECT i.id,i.server_id,i.type,i.tag,i.tls_id,i.options,n.route_upstream_inbound_id FROM nodes n JOIN sb_inbounds i ON i.tag=n.inbound_tag WHERE n.enabled=1 AND n.type='self_built' AND n.route_upstream_broken=0 AND n.route_upstream_inbound_id<>0 AND i.enabled=1`)
	if err != nil {
		return nil, nil, err
	}
	type route struct {
		from SbInbound
		toID int64
	}
	var routes []route
	for rows.Next() {
		var edge route
		if err = rows.Scan(&edge.from.ID, &edge.from.ServerID, &edge.from.Type, &edge.from.Tag, &edge.from.TlsID, &edge.from.Options, &edge.toID); err != nil {
			rows.Close()
			return nil, nil, err
		}
		routes = append(routes, edge)
	}
	if err = rows.Close(); err != nil {
		return nil, nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	required := map[int64]bool{}
	connected := map[int64][]int64{}
	for _, edge := range routes {
		target, err := meteringInboundWith(db, edge.toID)
		if err != nil {
			return nil, nil, err
		}
		if target == nil || !target.Enabled {
			return nil, nil, fmt.Errorf("逐用户机器观测无法确认 %s 能力：入口 %s 的落地不存在或已禁用", capability, edge.from.Tag)
		}
		connected[edge.from.ServerID] = append(connected[edge.from.ServerID], target.ServerID)
		connected[target.ServerID] = append(connected[target.ServerID], edge.from.ServerID)
		if uses(&edge.from) {
			required[edge.from.ServerID] = true
		}
		if uses(target) {
			required[target.ServerID] = true
			if callerNeedsFix {
				required[edge.from.ServerID] = true
			}
		}
	}
	return required, connected, nil
}

// RelayUserVisionServerIDs lists just the nodes requiring the reviewed framing
// fix for an enabled P1 topology. Node 0 is a real participant, not a sentinel
// to drop. Ordinary P0, plaintext and WS routes need no Vision capability.
func (s *Store) RelayUserVisionServerIDs() ([]int64, error) {
	if !s.RelayUserMeteringEnabled() {
		return nil, nil
	}
	required, _, err := relayVisionTopologyWith(s.db)
	if err != nil {
		return nil, err
	}
	return orderedVisionServerIDs(required), nil
}

// RelayUserVisionDependencies scopes runtime blocking to the machines sharing a
// path with an unverified Vision processor. An independent plaintext/WS path
// remains deployable even if another component fails its fresh runtime probe.
func (s *Store) RelayUserVisionDependencies() (map[int64][]int64, error) {
	if !s.RelayUserMeteringEnabled() {
		return nil, nil
	}
	required, connected, err := relayVisionTopologyWith(s.db)
	if err != nil {
		return nil, err
	}
	out := map[int64][]int64{}
	seen := map[int64]bool{}
	for start := range connected {
		if seen[start] {
			continue
		}
		component := []int64{}
		needed := map[int64]bool{}
		pending := []int64{start}
		for len(pending) > 0 {
			id := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if seen[id] {
				continue
			}
			seen[id] = true
			component = append(component, id)
			if required[id] {
				needed[id] = true
			}
			pending = append(pending, connected[id]...)
		}
		if len(needed) > 0 {
			ids := orderedVisionServerIDs(needed)
			for _, id := range component {
				out[id] = ids
			}
		}
	}
	return out, nil
}

func orderedVisionServerIDs(required map[int64]bool) []int64 {
	ids := make([]int64, 0, len(required))
	for id := range required {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func nodeVisionCapabilityReadyWith(db txLike, serverID, now int64) (bool, error) {
	var version, probeErr string
	var checkedAt int64
	var stats bool
	err := db.QueryRow(`SELECT version,v2ray_api,checked_at,error FROM node_singbox WHERE server_id=?`, serverID).Scan(&version, &stats, &checkedAt, &probeErr)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sbver.HasVisionFramingFix(version) && stats && probeErr == "" && checkedAt > 0 && checkedAt >= now-int64(relayVisionProbeMaxAge/time.Second) && checkedAt <= now+60, nil
}

func validateRelayVisionCapabilitiesWith(db txLike, now int64) error {
	required, _, err := relayVisionTopologyWith(db)
	if err != nil {
		return err
	}
	for _, id := range orderedVisionServerIDs(required) {
		ready, err := nodeVisionCapabilityReadyWith(db, id, now)
		if err != nil {
			return err
		}
		if !ready {
			name := fmt.Sprintf("服务器 #%d", id)
			if id == LocalNodeID {
				name = LocalNodeName
			}
			return fmt.Errorf("逐用户机器观测无法启用：%s 的 Vision 分片修复能力未确认；请先安装并运行轻舟修复内核 %s（含 with_v2ray_api），再到服务器页重新检测。官方 1.14.2、未知/过期或检测失败的结果不能启用 P1 Vision", name, sbver.VisionFramingFixVersion)
		}
	}
	return validateRelayTransportCapabilitiesWith(db, now)
}

// An old active receipt cannot keep a path's attribution readiness green after
// any of its Vision processors is known to be old, unreachable or stale. Check
// only this connected route component: unrelated plain/WS routes stay usable.
func relayVisionAttributionReadyWith(db txLike, serverID, now int64) (bool, error) {
	var enabled int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key IN ('relay_link_metering','relay_user_metering') AND value='true'`).Scan(&enabled); err != nil {
		return false, err
	}
	if enabled != 2 {
		return true, nil
	}
	required, connected, err := relayVisionTopologyWith(db)
	if err != nil {
		return false, err
	}
	seen := map[int64]bool{}
	pending := []int64{serverID}
	for len(pending) > 0 {
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		if required[id] {
			ready, err := nodeVisionRuntimeReadyWith(db, id, now)
			if err != nil || !ready {
				return false, err
			}
		}
		pending = append(pending, connected[id]...)
	}
	return true, nil
}
