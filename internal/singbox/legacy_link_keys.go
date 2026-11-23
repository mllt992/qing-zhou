package singbox

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
)

// LegacyShareLinkForNodeKey reconstructs the pre-WS-fidelity serializer only
// for a trusted store-supplied parameter snapshot. Never publish this lossy
// link or accept legacy-key claims from external URI fields. The store supplies
// the old TLS/Host/ALPN choices, which cannot be inferred from a new share URI.
func LegacyShareLinkForNodeKey(p LinkParams) string {
	p.WSHeaders = nil
	p.TLSDisabled = false
	// The certificate-pinning extensions postdate every legacy serializer.
	// Hysteria links have always carried the standard pinSHA256.
	p.PinCert = ""
	p.ECHConfig = ""
	if p.Type != "hysteria" && p.Type != "hysteria2" {
		p.PinSHA256 = ""
	}
	if p.Type == "vless" || p.Type == "trojan" {
		p.ALPN = ""
	}
	if p.Type == "vmess" {
		p.WSMaxEarlyData = 0
		p.WSEarlyDataHeader = ""
	}
	link := BuildShareLink(p)
	if p.Type == "vmess" && link != "" {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
		if err != nil {
			return ""
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			return ""
		}
		delete(object, "insecure") // old serializer emitted only allowInsecure
		raw, err = json.Marshal(object)
		if err != nil {
			return ""
		}
		return "vmess://" + base64.StdEncoding.EncodeToString(raw)
	}
	if p.Type == "vless" || p.Type == "trojan" {
		scheme := p.Type + "://"
		tail := strings.TrimPrefix(link, scheme)
		if _, rest, ok := strings.Cut(tail, "@"); ok {
			credential := p.UUID
			if p.Type == "trojan" {
				credential = p.Password
			}
			return scheme + url.QueryEscape(credential) + "@" + rest
		}
	}
	return link
}
