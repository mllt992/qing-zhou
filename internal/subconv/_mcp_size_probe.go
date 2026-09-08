package subconv

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// SIZE_TEST_FILE_DELETE_ME - first 5k of singbox for MCP payload size probe
// Singbox renders a sing-box JSON config: a "proxy" selector over the parsed
// outbounds, a direct outbound, default tun+mixed inbounds, and the anti-leak
// dns/route template merged in. Best-effort for the official sing-box client.
func Singbox(proxies []*Proxy, template string) (string, error) {
	return singboxWithProfile(proxies, template, ProfileLegacy)
}

// SingboxWithProfile renders an explicitly selected routing profile while the
// old Singbox entry point remains the untouched legacy path.
func SingboxWithProfile(proxies []*Proxy, template string, profile RoutingProfile) (string, error) {
	return singboxWithProfile(proxies, template, profile)
}

func singboxWithProfile(proxies []*Proxy, template string, profile RoutingProfile) (string, error) {
	if strings.TrimSpace(template) == "" {
		template = DefaultSingboxTemplate
	}
	doc := map[string]any{}
	_ = json.Unmarshal([]byte(template), &doc)
	modernizeSingboxDNS(doc)
	customTags := singboxTemplateSelectorTags(doc["outbounds"])
	type conv struct {
		o map[string]any
		p *Proxy
	}
	var cs []conv
	for _, p := range proxies {
		if o := singboxOutbound(p); o != nil {
			cs = append(cs, conv{o: o, p: p})
		}
	}
	kept := make([]*Proxy, len(cs))
	for i, c := range cs {
		kept[i] = c.p
	}
	dedupeNamesWithReserved(kept, customTags)
	outs := make([]map[string]any, len(cs))
	for i, c := range cs {
		c.o["tag"] = c.p.Name
		outs[i] = c.o
	}
	sg := buildStrategyGroups(kept)
	sel := []string{}
	if len(sg.all) > 0 {
		sel = append(sel, tagFixed)
	}
	if len(sg.all) > 1 {
		sel = append(sel, tagFallback)
	}
	sel = append(sel, "direct")
	generatedTags := map[string]bool{
		tagProxy: true, tagFixed: true, tagFallback: true,
		"direct": true, allPlaceholder: true,
	}
	if len(sg.ai) > 0 {
		generatedTags[tagAI] = true
	}
	for _, n := range sg.all {
		generatedTags[n] = true
	}
	customSelectors := mergeSingboxSelectors(doc["outbounds"], sg.all, generatedTags)
	all := []map[string]any{{"type": "selector", "tag": tagProxy, "outbounds": sel}}
	all = append(all, customSelectors...)
	if len(sg.all) > 0 {
		all = append(all, map[string]any{"type": "selector", "tag": tagFixed, "outbounds": sg.all})
	}
	if len(sg.all) > 1 {
		all = append(all, map[string]any{"type": "selector", "tag": tagFallback, "outbounds": sg.all})
	}
	if len(sg.ai) > 0 {
		ai := map[string]any{"type": "selector", "tag": tagAI, "outbounds": sg.ai}
		if len(sg.ai) > 1 {
			ai = map[string]any{
				"type": "urltest", "tag": tagAI, "outbounds": sg.ai,
				"url": "https://www.gstatic.com/generate_204", "interval": "3m", "tolerance": 50,
			}
		}
		all = append(all, ai)
	}
	all = append(all, outs...)
	all = append(all, map[string]any{"type": "direct", "tag": "direct"})
	doc["outbounds"] = all
	if profile != ProfileLegacy {
		applySingboxRoutingProfile(doc, profile)
	}
	if len(sg.ai) > 0 {
		injectSingboxAIRoute(doc)
	}
	if dns, ok := doc["dns"].(map[string]any); ok {
		stripEmptyDirectDetour(doc, mapSlice(dns["servers"]))
	}
	if _, ok := doc["inbounds"]; !ok {
		doc["inbounds"] = []map[string]any{
			{"type": "tun", "tag": "tun-in",
				"address":    []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
				"auto_route": true, "strict_route": true, "stack": "gvisor",
				"route_exclude_address": []string{"fe80::/10", "ff00::/8"},
			},
			{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080},
		}
	}
	injectSingboxTunExclude(doc, proxies)
	injectSingboxDomains(doc, proxies)
	b, err := json.MarshalIndent(doc, "", "  ")
	return string(b), err
}

// END SIZE TEST
