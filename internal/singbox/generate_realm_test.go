package singbox

import (
	"encoding/json"
	"testing"
)

// Realm is a server inbound field and must survive config assembly. Hop hints
// are client-only and stay stripped, including when realm is also set.
func TestGenerateKeepsHysteria2Realm(t *testing.T) {
	ib := Inbound{
		Type: "hysteria2",
		Base: map[string]interface{}{
			"type": "hysteria2", "tag": "hy2", "listen": "::", "listen_port": 443,
			"hop_interval":          "30s",
			"hop_interval_max":      "60s",
			"disable_chrome_parrot": true,
			"realm": map[string]interface{}{
				"server_url":   "https://realm.example.com",
				"token":        "example",
				"realm_id":     "home",
				"stun_servers": []interface{}{"stun.example.com:3478"},
				"ip_version":   float64(4),
			},
		},
		Users: []User{{Name: "u", Password: "pw"}},
	}
	raw, err := GenerateConfig(json.RawMessage(DefaultBaseConfig), []Inbound{ib}, "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Inbounds []map[string]interface{} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Inbounds) != 1 {
		t.Fatalf("inbounds = %d", len(doc.Inbounds))
	}
	got := doc.Inbounds[0]
	for _, key := range []string{"hop_interval", "hop_interval_max", "disable_chrome_parrot"} {
		if _, ok := got[key]; ok {
			t.Errorf("server config still has client-only %s", key)
		}
	}
	realm, _ := got["realm"].(map[string]interface{})
	if realm["server_url"] != "https://realm.example.com" || realm["realm_id"] != "home" || realm["token"] != "example" {
		t.Fatalf("realm = %#v", realm)
	}
	if realm["ip_version"] != float64(4) {
		t.Errorf("ip_version = %v", realm["ip_version"])
	}
}
