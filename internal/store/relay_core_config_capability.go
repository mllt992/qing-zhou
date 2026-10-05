package store

import (
	"encoding/json"
	"regexp"
	"strings"
)

var relayUserOutboundTag = regexp.MustCompile(`^relay-link-[1-9][0-9]*-u[1-9][0-9]*-g[1-9][0-9]*$`)

type relayCoreConfigEndpoint struct {
	Type      string `json:"type"`
	Tag       string `json:"tag"`
	Flow      string `json:"flow"`
	Transport struct {
		Type string `json:"type"`
	} `json:"transport"`
	Users []struct {
		Name string `json:"name"`
		Flow string `json:"flow"`
	} `json:"users"`
}

func addEndpointCoreRequirements(required *RelayCoreRequirements, endpoint relayCoreConfigEndpoint) {
	if endpoint.Type == "vless" {
		if endpoint.Flow == "xtls-rprx-vision" {
			required.VisionFraming = true
		}
		for _, u := range endpoint.Users {
			if u.Flow == "xtls-rprx-vision" {
				required.VisionFraming = true
			}
		}
	}
	switch endpoint.Type {
	case "vless", "vmess", "trojan":
		if endpoint.Transport.Type == "ws" || endpoint.Transport.Type == "httpupgrade" {
			required.TransportReadBuffer = true
		}
	}

}

// RelayCoreRequirementsForConfig derives the capability from the exact bytes
// about to be applied, never from a newer feature switch or inbound options.
// Per-user outbound tags identify P1 callers; immutable historical per-user
// identities identify P1 receivers, including staged and compatibility users.
// Pure P0 shared identities cannot acquire a new transport gate accidentally.
func (s *Store) RelayCoreRequirementsForConfig(serverID int64, raw []byte) (RelayCoreRequirements, error) {
	var cfg struct {
		Inbounds  []relayCoreConfigEndpoint `json:"inbounds"`
		Outbounds []relayCoreConfigEndpoint `json:"outbounds"`
		Route     struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	required := RelayCoreRequirements{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return required, err
	}
	outboundP1 := map[string]bool{}
	for _, endpoint := range cfg.Outbounds {
		if relayUserOutboundTag.MatchString(endpoint.Tag) {
			outboundP1[endpoint.Tag] = true
			addEndpointCoreRequirements(&required, endpoint)
		}
	}
	inboundP1 := map[string]bool{}
	wildcard := false
	var inspectRules func([]map[string]any)
	inspectRules = func(rules []map[string]any) {
		for _, rule := range rules {
			if tag, _ := rule["outbound"].(string); outboundP1[tag] {
				switch value := rule["inbound"].(type) {
				case string:
					inboundP1[value] = true
				case []any:
					if len(value) == 0 {
						wildcard = true
					}
					for _, v := range value {
						if tag, ok := v.(string); ok {
							inboundP1[tag] = true
						}
					}
				default:
					wildcard = true
				}
			}
			if nested, ok := rule["rules"].([]any); ok {
				for _, value := range nested {
					if child, ok := value.(map[string]any); ok {
						inspectRules([]map[string]any{child})
					}
				}
			}
		}
	}
	inspectRules(cfg.Route.Rules)
	needReceivers := false
	for _, endpoint := range cfg.Inbounds {
		for _, u := range endpoint.Users {
			if strings.HasPrefix(u.Name, "qzr_l_") {
				needReceivers = true
			}
		}
	}
	receiverNames := map[string]bool{}
	if needReceivers {
		rows, err := s.db.Query(`SELECT u.identity_name FROM relay_metering_users u JOIN relay_metering_links l ON l.id=u.link_id WHERE l.target_server_id=?`, serverID)
		if err != nil {
			return required, err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return required, err
			}
			receiverNames[name] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return required, err
		}
		if err := rows.Close(); err != nil {
			return required, err
		}
	}
	for _, endpoint := range cfg.Inbounds {
		involved := wildcard || inboundP1[endpoint.Tag]
		for _, u := range endpoint.Users {
			involved = involved || receiverNames[u.Name]
		}
		if involved {
			addEndpointCoreRequirements(&required, endpoint)
			// The #87 patch is in the Trojan server; a Trojan client outbound does not need it.
			if endpoint.Type == "trojan" {
				required.TrojanHandshake = true
			}
		}
	}
	return required, nil
}
