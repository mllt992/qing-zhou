package subconv

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"qingzhou/internal/singbox"
)

func realmLink() string {
	return singbox.BuildShareLink(singbox.LinkParams{
		Type: "hysteria2", Tag: "realm-node", Host: "203.0.113.10", Port: 443,
		Password: "secret", TLS: true, SNI: "hy2.example",
		HopInterval: "30s", HopIntervalMax: "60s",
		Obfs: "salamander", ObfsPassword: "example",
		Realm: &singbox.RealmParams{
			ServerURL:   "https://realm.example.com",
			Token:       "example",
			RealmID:     "home",
			STUNServers: []string{"stun.example.com:3478", "1.1.1.1:3478"},
			IPVersion:   4,
		},
	})
}

func plainHy2Link() string {
	return singbox.BuildShareLink(singbox.LinkParams{
		Type: "hysteria2", Tag: "plain-hy2", Host: "203.0.113.11", Port: 8443,
		Password: "secret", TLS: true, SNI: "plain.example",
		HopInterval: "30s",
	})
}

func TestHysteria2RealmSingboxOmitsDirectAddress(t *testing.T) {
	link := realmLink()
	if !strings.Contains(link, "qz-realm=1") || !strings.Contains(link, "realm-id=home") {
		t.Fatalf("carrier link missing realm fields: %s", link)
	}
	ob := renderedSbOutbound(t, link, "hysteria2")
	for _, key := range []string{"server", "server_port", "server_ports", "hop_interval", "hop_interval_max"} {
		if v, ok := ob[key]; ok {
			t.Errorf("%s conflicts with realm, got %v", key, v)
		}
	}
	realm, _ := ob["realm"].(map[string]any)
	if realm["server_url"] != "https://realm.example.com" || realm["realm_id"] != "home" || realm["token"] != "example" {
		t.Fatalf("realm = %#v", realm)
	}
	stuns, _ := realm["stun_servers"].([]any)
	if len(stuns) != 2 || stuns[0] != "stun.example.com:3478" || stuns[1] != "1.1.1.1:3478" {
		t.Fatalf("stun_servers = %#v", realm["stun_servers"])
	}
	if realm["ip_version"] != float64(4) {
		t.Errorf("ip_version = %v, want 4", realm["ip_version"])
	}
	if ob["password"] != "secret" {
		t.Errorf("password = %v", ob["password"])
	}
	if ob["tag"] != "realm-node" {
		t.Errorf("tag = %v", ob["tag"])
	}
	obfs, _ := ob["obfs"].(map[string]any)
	if obfs["type"] != "salamander" || obfs["password"] != "example" {
		t.Errorf("obfs = %#v", obfs)
	}

	out, err := Singbox(ParseLinks([]string{link}), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "realm.example.com") {
		t.Errorf("sing-box config did not keep the realm server on the direct DNS path:\n%s", out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc["inbounds"])
	if strings.Contains(string(raw), "203.0.113.10") {
		t.Errorf("realm node IP was excluded from TUN as if the client dialled it: %s", raw)
	}
	if !strings.Contains(string(raw), "1.1.1.1") {
		t.Errorf("realm STUN address was not excluded from TUN: %s", raw)
	}
}

func TestHysteria2WithoutRealmUnchanged(t *testing.T) {
	link := plainHy2Link()
	if strings.Contains(link, "qz-realm") || strings.Contains(link, "realm-id") {
		t.Fatalf("plain hysteria2 link gained realm params: %s", link)
	}
	ob := renderedSbOutbound(t, link, "hysteria2")
	if _, ok := ob["realm"]; ok {
		t.Fatalf("plain outbound has realm: %#v", ob["realm"])
	}
	if ob["server"] != "203.0.113.11" || ob["server_port"] != float64(8443) {
		t.Fatalf("server = %v:%v", ob["server"], ob["server_port"])
	}
	if ob["hop_interval"] != "30s" {
		t.Fatalf("hop_interval = %v", ob["hop_interval"])
	}
}

func TestHysteria2RealmSkippedByClash(t *testing.T) {
	realm, plain := realmLink(), plainHy2Link()
	out, err := Clash(ParseLinks([]string{realm, plain}), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "realm-node") || strings.Contains(out, "realm.example.com") {
		t.Fatalf("clash emitted the realm node:\n%s", out)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	list, _ := doc["proxies"].([]any)
	if len(list) != 1 {
		t.Fatalf("clash proxies = %d, want only the plain node", len(list))
	}
	m, _ := list[0].(map[string]any)
	if m["name"] != "plain-hy2" || m["type"] != "hysteria2" || m["server"] != "203.0.113.11" {
		t.Fatalf("kept proxy = %#v", m)
	}
}

func TestHysteria2RealmShareLinkSkipped(t *testing.T) {
	realm, plain := realmLink(), plainHy2Link()
	body, ctype, err := Render(FormatBase64, []string{realm, plain}, nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ctype != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %s", ctype)
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "qz-realm") || strings.Contains(text, "realm-node") || strings.Contains(text, "realm-server-url") {
		t.Fatalf("share-link list included the realm node:\n%s", text)
	}
	if !strings.Contains(text, plain) {
		t.Fatalf("plain hysteria2 link was dropped:\n%s", text)
	}
	if surge := Surge(ParseLinks([]string{realm, plain}), ""); strings.Contains(surge, "realm-node") || strings.Contains(surge, "realm.example.com") {
		t.Fatalf("surge emitted the realm node:\n%s", surge)
	}
}
