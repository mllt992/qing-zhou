package singbox

import (
	"encoding/json"
	"testing"
)

func TestGenerateDropsOrphanRelaysForUserlessMixed(t *testing.T) {
	raw, err := GenerateConfigWithOptions([]byte(DefaultBaseConfig), []Inbound{
		{
			Type: "mixed",
			Base: map[string]interface{}{
				"type":        "mixed",
				"tag":         "empty-mixed",
				"listen":      "::",
				"listen_port": 9582,
			},
			Users: nil, // dropped to prevent open proxy
		},
		{
			Type: "vless",
			Base: map[string]interface{}{
				"type":        "vless",
				"tag":         "vless-ok",
				"listen":      "::",
				"listen_port": 7056,
			},
			Users: []User{{Name: "u1", UUID: "11111111-1111-1111-1111-111111111111"}},
		},
	}, Options{
		Relays: []Relay{
			{
				Outbound:    map[string]interface{}{"type": "vless", "tag": "relay-ghost", "server": "1.2.3.4", "uuid": "secret"},
				InboundTags: []string{"empty-mixed"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
		Route     struct {
			Rules []map[string]interface{} `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, ob := range cfg.Outbounds {
		if ob["tag"] == "relay-ghost" {
			t.Fatalf("orphan relay outbound must not be emitted when its mixed inbound was dropped")
		}
	}
	if len(cfg.Route.Rules) != 0 {
		t.Fatalf("orphan relay route rule must not be emitted: %+v", cfg.Route.Rules)
	}
}

func TestGenerateMixedTLSDefaultsALPNToHTTP11(t *testing.T) {
	origTLS := map[string]interface{}{"enabled": true, "server_name": "taxi.mllt.vip"}
	raw, err := GenerateConfigWithOptions([]byte(DefaultBaseConfig), []Inbound{
		{
			Type: "mixed",
			Base: map[string]interface{}{
				"type":        "mixed",
				"tag":         "https-px",
				"listen":      "::",
				"listen_port": 7648,
				"tls":         origTLS,
			},
			Users: []User{{Name: "px_u1", Password: "secret"}},
		},
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, mutated := origTLS["alpn"]; mutated {
		t.Fatal("GenerateConfigWithOptions must not mutate caller's TLS map")
	}
	var cfg struct {
		Inbounds []struct {
			TLS struct {
				ALPN []string `json:"alpn"`
			} `json:"tls"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inbounds) != 1 || len(cfg.Inbounds[0].TLS.ALPN) != 1 || cfg.Inbounds[0].TLS.ALPN[0] != "http/1.1" {
		t.Fatalf("expected alpn=[http/1.1], got %+v", cfg.Inbounds)
	}
}
