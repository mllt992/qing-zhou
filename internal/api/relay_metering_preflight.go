package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"qingzhou/internal/sbver"
	"qingzhou/internal/store"
)

// This endpoint only reads persisted topology and capability observations. It
// never creates identities, changes switches, probes SSH or restarts a core.
func (a *API) handleRelayMeteringPreflight(w http.ResponseWriter, r *http.Request) {
	values := make([]bool, 3)
	for i, key := range []string{"enabled", "cumulative_enabled", "per_user_enabled"} {
		value, err := strconv.ParseBool(r.URL.Query().Get(key))
		if err != nil {
			fail(w, http.StatusBadRequest, "预检参数无效："+key)
			return
		}
		values[i] = value
	}
	failures := []string{}
	if a.sbctl == nil {
		failures = append(failures, "sing-box 控制器未启用，无法下发配置")
	}
	if err := a.st.PreflightTrafficMetering(values[0], values[1], values[2]); err != nil {
		failures = append(failures, err.Error())
	}
	nodes, err := a.relayMeteringPreflightNodes(values[0], values[2])
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取受影响节点失败："+err.Error())
		return
	}
	ok(w, J{
		"valid": len(failures) == 0, "errors": failures, "nodes": nodes,
		"min_supported": sbver.MinSupported, "vision_fixed_version": sbver.VisionFramingFixVersion,
		"transport_fixed_version": sbver.TransportReadBufferFixVersion,
		"trojan_fixed_version":    sbver.TrojanHandshakeFixVersion,
		"scope_note":              "保存后会检查并重新下发本机及已启用的服务器；配置未改变且服务正常的节点不会因此重启。预检只读取已有记录，不能代替节点实际下发和运行确认。",
	})
}

type relayMeteringPreflightNode struct {
	ServerID                  int64    `json:"server_id"`
	Name                      string   `json:"name"`
	MeteringParticipant       bool     `json:"metering_participant"`
	Version                   string   `json:"version"`
	HasV2RayAPI               bool     `json:"has_v2ray_api"`
	HasVisionFramingFix       bool     `json:"has_vision_framing_fix"`
	VisionRequired            bool     `json:"vision_required"`
	TransportRequired         bool     `json:"transport_required"`
	HasTransportReadBufferFix bool     `json:"has_transport_read_buffer_fix"`
	TrojanRequired            bool     `json:"trojan_required"`
	HasTrojanHandshakeFix     bool     `json:"has_trojan_handshake_fix"`
	CheckedAt                 int64    `json:"checked_at"`
	Error                     string   `json:"error,omitempty"`
	RequiresReinstall         bool     `json:"requires_reinstall"`
	RequiresCheck             bool     `json:"requires_check"`
	Reasons                   []string `json:"reasons"`
}

func (a *API) relayMeteringPreflightNodes(links, perUser bool) ([]relayMeteringPreflightNode, error) {
	servers, err := a.st.ListServers()
	if err != nil {
		return nil, err
	}
	observed, err := a.st.NodeSingboxAll()
	if err != nil {
		return nil, err
	}
	inbounds, err := a.st.ListSbInbounds()
	if err != nil {
		return nil, err
	}
	routes, err := a.st.ListNodes()
	if err != nil {
		return nil, err
	}
	byID, byTag := map[int64]*store.SbInbound{}, map[string]*store.SbInbound{}
	for _, ib := range inbounds {
		byID[ib.ID], byTag[ib.Tag] = ib, ib
	}
	participants := map[int64]bool{}
	add := func(from *store.SbInbound, targetID int64) {
		if from == nil || !from.Enabled || targetID == 0 {
			return
		}
		participants[from.ServerID] = true
		if to := byID[targetID]; to != nil {
			participants[to.ServerID] = true
		}
	}
	for _, ib := range inbounds {
		add(ib, ib.UpstreamInboundID)
	}
	for _, n := range routes {
		if n.Enabled && n.Type == "self_built" && !n.RouteUpstreamBroken {
			add(byTag[n.InboundTag], n.RouteUpstreamInboundID)
		}
	}
	requirements := map[int64]store.RelayCoreRequirements{}
	requirementsError := ""
	if links && perUser {
		preview, previewErr := a.st.PreviewRelayUserCoreRequirements()
		// Preserve the affected machine list when a broken route prevents safe
		// requirement inference. Never guess that such a node needs no fix.
		if previewErr == nil {
			requirements = preview
		} else {
			requirementsError = previewErr.Error()
		}
	}
	names := map[int64]string{store.LocalNodeID: store.LocalNodeName}
	for _, sv := range servers {
		if sv.Enabled {
			names[sv.ID] = sv.Name
		}
	}
	ids := make([]int64, 0, len(names))
	for id := range names {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	nodes := make([]relayMeteringPreflightNode, 0, len(ids))
	now := time.Now().Unix()
	for _, id := range ids {
		node := relayMeteringPreflightNode{ServerID: id, Name: names[id], MeteringParticipant: participants[id], VisionRequired: requirements[id].VisionFraming, TransportRequired: requirements[id].TransportReadBuffer, TrojanRequired: requirements[id].TrojanHandshake, Reasons: []string{}}
		observation := observed[id]
		if observation == nil || observation.CheckedAt == 0 {
			node.RequiresCheck = true
			node.Reasons = append(node.Reasons, "尚无内核检测记录，请到服务器页重新检测")
		} else {
			node.Version, node.HasV2RayAPI, node.HasVisionFramingFix, node.CheckedAt, node.Error = observation.Version, observation.HasV2RayAPI, observation.HasVisionFramingFix, observation.CheckedAt, observation.Error
			node.HasTransportReadBufferFix = observation.HasTransportReadBufferFix
			node.HasTrojanHandshakeFix = observation.HasTrojanHandshakeFix
			if observation.Version == "" {
				node.RequiresCheck = true
				node.Reasons = append(node.Reasons, "未能确认内核版本，请到服务器页重新检测")
			}
			if observation.Error != "" {
				node.RequiresCheck = true
				node.Reasons = append(node.Reasons, "最近检测失败："+observation.Error)
			}
			if observation.Error == "" && observation.Version != "" {
				if (sbver.Info{Version: observation.Version}).TooOld() {
					node.RequiresReinstall = true
					node.Reasons = append(node.Reasons, "当前内核低于最低支持版本 "+sbver.MinSupported)
				}
				if links && participants[id] && !observation.HasV2RayAPI {
					node.RequiresReinstall = true
					node.Reasons = append(node.Reasons, "内核缺少 with_v2ray_api，无法逐用户统计；请重装带统计能力的内核")
				}
				if node.VisionRequired && !observation.HasVisionFramingFix {
					node.RequiresReinstall = true
					node.Reasons = append(node.Reasons, fmt.Sprintf("该路径需要 Vision 分片修复，请安装 %s 并运行后重新检测", sbver.VisionFramingFixVersion))
				}
				if node.TransportRequired && !observation.HasTransportReadBufferFix {
					node.RequiresReinstall = true
					node.Reasons = append(node.Reasons, fmt.Sprintf("该路径需要 WebSocket/HTTPUpgrade 缓冲修复，请安装 %s 并运行后重新检测；旧 Vision 专用修复内核不包含这项修复", sbver.TransportReadBufferFixVersion))
				}
				if node.TrojanRequired && !observation.HasTrojanHandshakeFix {
					node.RequiresReinstall = true
					node.Reasons = append(node.Reasons, fmt.Sprintf("该路径含 Trojan，需要 Trojan 分段握手修复（项目维护补丁），请安装 %s 并运行后重新检测；旧 Vision/WebSocket 修复内核不包含这项修复", sbver.TrojanHandshakeFixVersion))
				}
			}
			if (node.VisionRequired || node.TransportRequired || node.TrojanRequired) && (observation.CheckedAt < now-15*60 || observation.CheckedAt > now+60) {
				node.RequiresCheck = true
				node.Reasons = append(node.Reasons, requirements[id].Label()+" 能力记录已过期，请到服务器页重新检测")
			}
		}
		if requirementsError != "" {
			node.RequiresCheck = true
			node.Reasons = append(node.Reasons, "当前拓扑无法确认所需的内核修复能力："+requirementsError)
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}
