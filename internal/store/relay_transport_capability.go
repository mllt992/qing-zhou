package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"qingzhou/internal/sbver"
)

// RelayCoreRequirements describes reviewed core fixes required at one machine.
// It is a capability requirement, never a persisted claim of running readiness.
type RelayCoreRequirements struct {
	VisionFraming       bool `json:"vision_framing"`
	TransportReadBuffer bool `json:"transport_read_buffer"`
	// TrojanHandshake: any Trojan endpoint on a P1 path (#87 maintenance patch).
	TrojanHandshake bool `json:"trojan_handshake"`
}

func (r RelayCoreRequirements) Label() string {
	var parts []string
	if r.VisionFraming || !r.TransportReadBuffer && !r.TrojanHandshake {
		parts = append(parts, "Vision")
	}
	if r.TransportReadBuffer {
		parts = append(parts, "WebSocket/HTTPUpgrade")
	}
	if r.TrojanHandshake {
		parts = append(parts, "Trojan 分段握手")
	}
	return strings.Join(parts, " 与 ")
}
func (r RelayCoreRequirements) FixedVersion() string {
	if r.TrojanHandshake {
		return sbver.TrojanHandshakeFixVersion
	}
	if r.TransportReadBuffer {
		return sbver.TransportReadBufferFixVersion
	}
	return sbver.VisionFramingFixVersion
}
func (r RelayCoreRequirements) SupportedBy(info sbver.Info) bool {
	return info.HasV2RayAPI && (!r.VisionFraming || sbver.HasVisionFramingFix(info.Version)) &&
		(!r.TransportReadBuffer || sbver.HasTransportReadBufferFix(info.Version)) &&
		(!r.TrojanHandshake || sbver.HasTrojanHandshakeFix(info.Version))
}

// Every Trojan inbound reads a request header from a stream, so every
// transport (TCP, TLS, WS, HTTPUpgrade, HTTP/2, gRPC, QUIC) needs the fix.
func relayInboundUsesTrojan(ib *SbInbound) bool {
	return ib != nil && ib.Type == "trojan"
}

func relayTrojanTopologyWith(db txLike) (map[int64]bool, map[int64][]int64, error) {
	return relayCapabilityTopologyModeWith(db, relayInboundUsesTrojan, "Trojan", false)
}

func relayInboundUsesBufferedTransport(ib *SbInbound) bool {
	if ib == nil {
		return false
	}
	switch ib.Type {
	case "vless", "vmess", "trojan":
	default:
		return false
	}
	var options struct {
		Transport struct {
			Type string `json:"type"`
		} `json:"transport"`
	}
	if json.Unmarshal([]byte(ib.Options), &options) != nil {
		return false
	}
	return options.Transport.Type == "ws" || options.Transport.Type == "httpupgrade"
}

func relayTransportTopologyWith(db txLike) (map[int64]bool, map[int64][]int64, error) {
	return relayCapabilityTopologyWith(db, relayInboundUsesBufferedTransport, "WebSocket/HTTPUpgrade")
}

func relayCoreTopologyWith(db txLike) (map[int64]RelayCoreRequirements, map[int64][]int64, error) {
	vision, connected, err := relayCapabilityTopologyWith(db, relayInboundUsesVision, "中转内核")
	if err != nil {
		return nil, nil, err
	}
	transport, _, err := relayTransportTopologyWith(db)
	if err != nil {
		return nil, nil, err
	}
	trojanRequired, _, err := relayTrojanTopologyWith(db)
	if err != nil {
		return nil, nil, err
	}
	required := map[int64]RelayCoreRequirements{}
	for id := range vision {
		required[id] = RelayCoreRequirements{VisionFraming: true}
	}
	for id := range transport {
		r := required[id]
		r.TransportReadBuffer = true
		required[id] = r
	}
	for id := range trojanRequired {
		r := required[id]
		r.TrojanHandshake = true
		required[id] = r
	}
	dependencies := map[int64][]int64{}
	seen := map[int64]bool{}
	for start := range connected {
		if seen[start] {
			continue
		}
		var component []int64
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
			if _, ok := required[id]; ok {
				needed[id] = true
			}
			pending = append(pending, connected[id]...)
		}
		if len(needed) > 0 {
			ids := orderedVisionServerIDs(needed)
			for _, id := range component {
				dependencies[id] = ids
			}
		}
	}
	return required, dependencies, nil
}

// RelayUserCoreTopology is the active P1 contract consumed by the controller.
// Every component depends only on the processors that actually need a fix;
// unrelated components remain deployable when a fresh probe fails.
func (s *Store) RelayUserCoreTopology() (map[int64]RelayCoreRequirements, map[int64][]int64, error) {
	if !s.RelayUserMeteringEnabled() {
		return nil, nil, nil
	}
	return relayCoreTopologyWith(s.db)
}

// PreviewRelayUserCoreRequirements is read-only and evaluates the proposed P1
// topology before its switches are enabled. It does not probe or mint identities.
func (s *Store) PreviewRelayUserCoreRequirements() (map[int64]RelayCoreRequirements, error) {
	required, _, err := relayCoreTopologyWith(s.db)
	return required, err
}

func nodeTransportCapabilityReadyWith(db txLike, serverID, now int64) (bool, error) {
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
	return sbver.HasTransportReadBufferFix(version) && stats && probeErr == "" && checkedAt > 0 && checkedAt >= now-int64(relayVisionProbeMaxAge/time.Second) && checkedAt <= now+60, nil
}

func validateRelayTransportCapabilitiesWith(db txLike, now int64) error {
	required, _, err := relayTransportTopologyWith(db)
	if err != nil {
		return err
	}
	for _, id := range orderedVisionServerIDs(required) {
		ready, err := nodeTransportCapabilityReadyWith(db, id, now)
		if err != nil {
			return err
		}
		if !ready {
			name := fmt.Sprintf("服务器 #%d", id)
			if id == LocalNodeID {
				name = LocalNodeName
			}
			return fmt.Errorf("逐用户机器观测无法启用：%s 的 WebSocket/HTTPUpgrade 缓冲修复能力未确认；请先安装并运行轻舟修复内核 %s（含 with_v2ray_api），再重新检测。旧 Vision 专用修复、官方版本、未知/过期或检测失败的结果不能启用此 P1 路径", name, sbver.TransportReadBufferFixVersion)
		}
	}
	return validateRelayTrojanCapabilitiesWith(db, now)
}

func nodeCoreMarkerReadyWith(db txLike, serverID, now int64, accepts func(string) bool) (bool, error) {
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
	return accepts(version) && stats && probeErr == "" && checkedAt > 0 && checkedAt >= now-int64(relayVisionProbeMaxAge/time.Second) && checkedAt <= now+60, nil
}

// validateRelayTrojanCapabilitiesWith requires the exact #87 marker on every
// machine whose P1 path terminates a Trojan inbound. Vision/transport-only
// markers, versions with suffixes or higher semver never satisfy it.
func validateRelayTrojanCapabilitiesWith(db txLike, now int64) error {
	required, _, err := relayTrojanTopologyWith(db)
	if err != nil {
		return err
	}
	for _, id := range orderedVisionServerIDs(required) {
		ready, err := nodeCoreMarkerReadyWith(db, id, now, sbver.HasTrojanHandshakeFix)
		if err != nil {
			return err
		}
		if !ready {
			name := fmt.Sprintf("服务器 #%d", id)
			if id == LocalNodeID {
				name = LocalNodeName
			}
			return fmt.Errorf("逐用户机器观测无法启用：%s 的 Trojan 分段握手修复能力未确认；请先安装并运行轻舟修复内核 %s（含 with_v2ray_api），再重新检测。旧 Vision/WebSocket 修复标记、官方版本、未知/过期或检测失败的结果不能启用此 P1 路径", name, sbver.TrojanHandshakeFixVersion)
		}
	}
	return nil
}

// relayTrojanAttributionReadyWith mirrors the transport check for Trojan paths.
func relayTrojanAttributionReadyWith(db txLike, serverID, now int64) (bool, error) {
	var enabled int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key IN ('relay_link_metering','relay_user_metering') AND value='true'`).Scan(&enabled); err != nil {
		return false, err
	}
	if enabled != 2 {
		return true, nil
	}
	required, connected, err := relayTrojanTopologyWith(db)
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
			ready, err := nodeRelayCoreRuntimeReadyWith(db, id, now, sbver.HasTrojanHandshakeFix)
			if err != nil || !ready {
				return false, err
			}
		}
		pending = append(pending, connected[id]...)
	}
	return true, nil
}

func relayTransportAttributionReadyWith(db txLike, serverID, now int64) (bool, error) {
	var enabled int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key IN ('relay_link_metering','relay_user_metering') AND value='true'`).Scan(&enabled); err != nil {
		return false, err
	}
	if enabled != 2 {
		return true, nil
	}
	required, connected, err := relayTransportTopologyWith(db)
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
			ready, err := nodeRelayCoreRuntimeReadyWith(db, id, now, sbver.HasTransportReadBufferFix)
			if err != nil || !ready {
				return false, err
			}
		}
		pending = append(pending, connected[id]...)
	}
	return true, nil
}
