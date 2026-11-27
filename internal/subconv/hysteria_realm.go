package subconv

import (
	"net"
	"net/url"
	"strings"
)

// viaRealm reports whether this hysteria2 node is reached through Hysteria Realm
// rather than a direct server address. Clash, Surge and share-link lists cannot
// express that, so they drop the node. sing-box subscriptions emit the realm
// block and omit the direct-dial fields.
func (p *Proxy) viaRealm() bool {
	if p == nil || p.Protocol != "hysteria2" || p.Params == nil {
		return false
	}
	return p.Params.Get("realm-server-url") != "" || p.Params.Get("realm-id") != ""
}

// hysteriaRealmBlock copies the client-facing realm fields. A token already set
// on the link is the one clients use; the server's port-mapping block is not
// part of the share link and must not be invented here.
func (p *Proxy) hysteriaRealmBlock() map[string]any {
	if !p.viaRealm() {
		return nil
	}
	realm := map[string]any{}
	if v := p.Params.Get("realm-server-url"); v != "" {
		realm["server_url"] = v
	}
	if v := p.Params.Get("realm-id"); v != "" {
		realm["realm_id"] = v
	}
	if v := p.Params.Get("realm-token"); v != "" {
		realm["token"] = v
	}
	if stuns := nonemptyParams(p.Params["realm-stun"]); len(stuns) > 0 {
		realm["stun_servers"] = stuns
	}
	switch p.Params.Get("realm-ip-version") {
	case "4":
		realm["ip_version"] = 4
	case "6":
		realm["ip_version"] = 6
	}
	return realm
}

func nonemptyParams(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// realmDirectHosts are the names a realm client dials (realm server and STUN).
// They must stay out of fake-ip, the same way a normal node's server does.
func (p *Proxy) realmDirectHosts() []string {
	if !p.viaRealm() {
		return nil
	}
	var hosts []string
	if raw := p.Params.Get("realm-server-url"); raw != "" {
		if u, err := url.Parse(raw); err == nil {
			if host := u.Hostname(); host != "" {
				hosts = append(hosts, host)
			}
		}
	}
	for _, stun := range p.Params["realm-stun"] {
		host := strings.TrimSpace(stun)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// shareLinkViaRealm reports whether a raw share URI is a realm carrier. Those
// URIs are not a standard hysteria2 address and must not be handed to clients
// that import a link list.
func shareLinkViaRealm(link string) bool {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return false
	}
	q := u.Query()
	return q.Get("realm-server-url") != "" || q.Get("realm-id") != ""
}

// withoutRealmShareLinks drops realm carriers. When none are present the
// original slice is returned so a normal subscription stays byte-for-byte.
func withoutRealmShareLinks(links []string) []string {
	out := make([]string, 0, len(links))
	dropped := false
	for _, link := range links {
		if shareLinkViaRealm(link) {
			dropped = true
			continue
		}
		out = append(out, link)
	}
	if !dropped {
		return links
	}
	return out
}
