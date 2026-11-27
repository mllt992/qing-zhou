package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"qingzhou/internal/sbver"
	"qingzhou/internal/store"
)

// guardHysteriaRealm rejects a hysteria2 realm block the target node cannot
// run, and drops an empty realm object so it is not emitted into the config.
// A non-empty realm on any other protocol is rejected.
func (a *API) guardHysteriaRealm(n *store.SbInbound) string {
	if n == nil || strings.TrimSpace(n.Options) == "" {
		return ""
	}
	var opts map[string]interface{}
	if err := json.Unmarshal([]byte(n.Options), &opts); err != nil {
		return ""
	}
	raw, exists := opts["realm"]
	if !exists || raw == nil {
		return ""
	}
	realm, ok := raw.(map[string]interface{})
	if !ok {
		return "realm 必须是对象"
	}
	serverURL := strings.TrimSpace(jsonString(realm["server_url"]))
	realmID := strings.TrimSpace(jsonString(realm["realm_id"]))
	token := strings.TrimSpace(jsonString(realm["token"]))
	stuns := jsonStringList(realm["stun_servers"])
	if serverURL == "" && realmID == "" && token == "" && len(stuns) == 0 {
		delete(opts, "realm")
		if b, err := json.Marshal(opts); err == nil {
			n.Options = string(b)
		}
		return ""
	}
	if n.Type != "hysteria2" {
		return "Hysteria Realm 仅适用于 Hysteria2 入站"
	}
	if serverURL == "" || realmID == "" {
		return "启用 Realm 时必须填写服务地址和 Realm ID"
	}
	version := a.nodeSingboxVersion(n.ServerID)
	if version == "" {
		return "目标节点的 sing-box 版本未知，不能启用 Hysteria Realm。请先在「服务器」页检测内核（需要 ≥ 1.14）"
	}
	if sbver.Compare(version, sbver.HysteriaRealmMinVersion) < 0 {
		return fmt.Sprintf("目标节点 sing-box %s 低于 1.14，不能启用 Hysteria Realm", version)
	}
	return ""
}

func (a *API) nodeSingboxVersion(serverID int64) string {
	all, err := a.st.NodeSingboxAll()
	if err != nil {
		return ""
	}
	if n := all[serverID]; n != nil {
		return strings.TrimSpace(n.Version)
	}
	return ""
}

func jsonString(v interface{}) string {
	s, _ := v.(string)
	return s
}

func jsonStringList(v interface{}) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []interface{}:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}
