package compliance

import (
	"context"
	"net"
	"testing"
)

func rules(fs []Finding) map[string]string {
	m := map[string]string{}
	for _, f := range fs {
		m[f.Rule] = f.Severity
	}
	return m
}

func TestIsPublicBind(t *testing.T) {
	for a, want := range map[string]bool{"": true, "::": true, "0.0.0.0": true, "*": true, "[::]": true,
		"127.0.0.1": false, "::1": false, "10.0.0.2": false, "192.168.1.1": false, "fd00::1": false,
		"203.0.113.9": true, "0.0.0.0%eth0": true, "localhost": false} {
		if got := IsPublicBind(a); got != want {
			t.Errorf("%q: got %v want %v", a, got, want)
		}
	}
}

func TestOpenAndAuthedProxies(t *testing.T) {
	cfg := `{"inbounds":[
	 {"type":"socks","tag":"s-open","listen":"::","listen_port":1080},
	 {"type":"mixed","tag":"m-auth","listen":"0.0.0.0","listen_port":2080,"users":[{"username":"a","password":"b"}]},
	 {"type":"http","tag":"h-local","listen":"127.0.0.1","listen_port":3128},
	 {"type":"http","tag":"h-lim","listen":"::","listen_port":3129,"users":[{"username":"a"}]},
	 {"type":"http","tag":"h-web","listen":"::","listen_port":8080,"users":[{"username":"a"}]}],
	 "route":{"rules":[{"inbound":["h-lim"],"source_ip_cidr":["198.51.100.0/24"],"outbound":"direct"}]}}`
	fs, owned, err := AnalyzeConfig([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	byTarget := map[string][]Finding{}
	for _, f := range fs {
		byTarget[f.Target] = append(byTarget[f.Target], f)
	}
	if r := rules(byTarget["s-open"]); r["proxy_open"] != High {
		t.Errorf("open socks: %v", r)
	}
	if r := rules(byTarget["m-auth"]); r["proxy_no_ip_limit"] != Medium {
		t.Errorf("authed mixed: %v", r)
	}
	if len(byTarget["h-local"]) != 0 {
		t.Errorf("loopback proxy flagged: %v", byTarget["h-local"])
	}
	if r := rules(byTarget["h-lim"]); r["proxy_plaintext"] != Low || len(r) != 1 {
		t.Errorf("restricted http: %v", r)
	}
	if r := rules(byTarget["h-web"]); r["web_port_proxy"] != High {
		t.Errorf("proxy on 8080: %v", r)
	}
	if owned[1080] != "s-open" || owned[3128] != "" {
		t.Errorf("owned=%v", owned)
	}
}

func TestWebPortInbounds(t *testing.T) {
	cfg := `{"inbounds":[
	 {"type":"trojan","tag":"tj","listen":"::","listen_port":443,"tls":{"enabled":true},"fallback":{"server":"127.0.0.1","server_port":8000}},
	 {"type":"vless","tag":"ws80","listen":"::","listen_port":80,"transport":{"type":"ws","path":"/x"}},
	 {"type":"vless","tag":"reality","listen":"::","listen_port":8443,"tls":{"enabled":true}},
	 {"type":"vless","tag":"ws-hi","listen":"::","listen_port":20000,"transport":{"type":"ws"}}],
	 "experimental":{"v2ray_api":{"listen":"0.0.0.0:18080"},"clash_api":{"external_controller":"127.0.0.1:9090"}}}`
	fs, _, err := AnalyzeConfig([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	r := rules(fs)
	if r["web_port_fallback"] != Medium || r["web_port_plain"] != Medium || r["stats_api_public"] != High {
		t.Errorf("rules=%v", r)
	}
	if _, ok := r["clash_api_public"]; ok {
		t.Error("loopback clash api flagged")
	}
	for _, f := range fs {
		if f.Target == "reality" || f.Target == "ws-hi" {
			t.Errorf("should not flag %s: %+v", f.Target, f)
		}
		if f.Title == "" || f.Detail == "" || f.Fix == "" {
			t.Errorf("finding missing text: %+v", f)
		}
	}
}

func TestPanel(t *testing.T) {
	if r := rules(AnalyzePanel("0.0.0.0:443")); r["panel_web_port"] != Medium {
		t.Errorf("%v", r)
	}
	if r := rules(AnalyzePanel("0.0.0.0:8081")); r["panel_public"] != Info {
		t.Errorf("%v", r)
	}
	if fs := AnalyzePanel("127.0.0.1:8080"); len(fs) != 0 {
		t.Errorf("%v", fs)
	}
}

func TestListeners(t *testing.T) {
	ss := `tcp   LISTEN 0      511          0.0.0.0:80        0.0.0.0:*    users:(("nginx",pid=12,fd=6))
tcp   LISTEN 0      4096               *:443             *:*    users:(("sing-box",pid=9,fd=3))
tcp   LISTEN 0      4096       127.0.0.1:8080      0.0.0.0:*
tcp   LISTEN 0      4096            [::]:8443          [::]:*
udp   UNCONN 0      0            0.0.0.0:443       0.0.0.0:*
WEB 80 200 0
WEB 443 0 0
WEB 8080 404 0
WEB 8443 400 200`
	ls := ParseListeners(ss)
	if len(ls) != 5 || ls[0].Process != "nginx" || ls[0].HTTP != 200 || ls[3].Addr != "[::]" {
		t.Fatalf("parsed %+v", ls)
	}
	fs := AnalyzeListeners(ls, map[int]string{443: "tj"}, 0)
	got := map[int]string{}
	for _, f := range fs {
		got[f.Port] = f.Severity
	}
	if got[80] != High || got[8443] != Medium || len(got) != 2 {
		t.Errorf("findings=%v", got)
	}
	// netstat fallback
	ns := "tcp        0      0 0.0.0.0:8080            0.0.0.0:*               LISTEN      77/python3\n"
	if l := ParseListeners(ns); len(l) != 1 || l[0].Port != 8080 || l[0].Process != "python3" {
		t.Errorf("netstat parsed %+v", l)
	}
	// panel's own port on the panel host is reported by AnalyzePanel instead
	if fs := AnalyzeListeners(ParseListeners(ns), nil, 8080); len(fs) != 0 {
		t.Errorf("panel port double-reported: %v", fs)
	}
}

func TestSourceFamilyEgressDoesNotBypassProxyRestriction(t *testing.T) {
	// FollowSourceFamilyEgress emits source_ip_cidr:["0.0.0.0/0","::ffff:0:0/96"]
	// for both resolve and direct-v4pref routing. That wildcard must NEVER fool
	// restricted() into thinking a public mixed proxy has an IP allow-list.
	cfg := `{
	  "inbounds": [
	    {"type":"mixed","tag":"m-open","listen":"::","listen_port":9582},
	    {"type":"mixed","tag":"m-auth","listen":"::","listen_port":9583,"users":[{"username":"a","password":"b"}]}
	  ],
	  "route": {"rules": [
	    {"inbound":["m-open","m-auth"],"source_ip_cidr":["0.0.0.0/0","::ffff:0:0/96"],"action":"resolve","strategy":"prefer_ipv4"},
	    {"inbound":["m-open","m-auth"],"source_ip_cidr":["0.0.0.0/0","::ffff:0:0/96"],"outbound":"direct-v4pref"}
	  ]}
	}`
	fs, _, err := AnalyzeConfig([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	byTarget := map[string]map[string]string{}
	for _, f := range fs {
		if byTarget[f.Target] == nil {
			byTarget[f.Target] = map[string]string{}
		}
		byTarget[f.Target][f.Rule] = f.Severity
	}
	if byTarget["m-open"]["proxy_open"] != High {
		t.Fatalf("open mixed with wildcard source_ip_cidr must still be proxy_open High: %+v", byTarget["m-open"])
	}
	if byTarget["m-auth"]["proxy_no_ip_limit"] != Medium {
		t.Fatalf("authed mixed with wildcard source_ip_cidr must still be proxy_no_ip_limit Medium: %+v", byTarget["m-auth"])
	}
}

func TestRealityCDNAndFallbackCompliance(t *testing.T) {
	cfg := `{
	  "inbounds": [
	    {
	      "type":"vless","tag":"r-cf","listen":"::","listen_port":7056,
	      "tls":{"enabled":true,"server_name":"www.cloudflare.com","reality":{"enabled":true,"handshake":{"server":"www.cloudflare.com","server_port":443},"short_id":[""]}}
	    },
	    {
	      "type":"vless","tag":"r-ak","listen":"::","listen_port":7057,
	      "tls":{"enabled":true,"server_name":"www.icloud.com","reality":{"enabled":true,"handshake":{"server":"www.icloud.com","server_port":443},"short_id":["aabbccdd"]}}
	    },
	    {
	      "type":"vless","tag":"r-custom","listen":"::","listen_port":7058,
	      "tls":{"enabled":true,"server_name":"blog.example.org","reality":{"enabled":true,"handshake":{"server":"blog.example.org","server_port":443},"short_id":["aabbccdd"]}}
	    },
	    {
	      "type":"hysteria2","tag":"hy-ext","listen":"::","listen_port":10954,
	      "masquerade":"https://www.cloudflare.com"
	    },
	    {
	      "type":"trojan","tag":"tj-ext","listen":"::","listen_port":8888,
	      "fallback":{"server":"www.cloudflare.com","server_port":443}
	    }
	  ]
	}`
	fs, _, err := AnalyzeConfig([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	byTarget := map[string]map[string]string{}
	for _, f := range fs {
		if byTarget[f.Target] == nil {
			byTarget[f.Target] = map[string]string{}
		}
		byTarget[f.Target][f.Rule] = f.Severity
	}
	if byTarget["r-cf"]["reality_cdn_fallback"] != High || byTarget["r-cf"]["reality_empty_short_id"] != Medium {
		t.Fatalf("r-cf findings wrong: %+v", byTarget["r-cf"])
	}
	if byTarget["r-ak"]["reality_cdn_fallback"] != Medium {
		t.Fatalf("r-ak findings wrong: %+v", byTarget["r-ak"])
	}
	if len(byTarget["r-custom"]) != 0 {
		t.Fatalf("r-custom should not trigger static CDN rule: %+v", byTarget["r-custom"])
	}
	if byTarget["hy-ext"]["hysteria2_external_masquerade"] != High {
		t.Fatalf("hy-ext findings wrong: %+v", byTarget["hy-ext"])
	}
	if byTarget["tj-ext"]["fallback_external"] != High {
		t.Fatalf("tj-ext findings wrong: %+v", byTarget["tj-ext"])
	}

	// Live DNS check catches a custom domain proxied behind Cloudflare edge IPs.
	dnsFindings := AnalyzeRealityDNS(context.Background(), []byte(cfg), func(_ context.Context, host string) ([]net.IP, error) {
		if host == "blog.example.org" {
			return []net.IP{net.ParseIP("104.21.10.20")}, nil
		}
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	})
	if len(dnsFindings) != 1 || dnsFindings[0].Target != "r-custom" || dnsFindings[0].Rule != "reality_cloudflare_ip" || dnsFindings[0].Severity != High {
		t.Fatalf("AnalyzeRealityDNS got %+v", dnsFindings)
	}
}
