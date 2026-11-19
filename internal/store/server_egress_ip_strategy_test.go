package store

import (
	"encoding/json"
	"strings"
	"testing"

	"qingzhou/internal/singbox"
)

func TestServerEgressIPStrategyMigrationDefaultEmpty(t *testing.T) {
	st := newRefundStore(t)
	if n := scalar(t, st, `SELECT COUNT(*) FROM pragma_table_info('servers') WHERE name='egress_ip_strategy'`); n != 1 {
		t.Fatal("egress_ip_strategy column missing on fresh database")
	}
	if n := scalar(t, st, `SELECT COUNT(*) FROM schema_migrations WHERE version='000014_server_egress_ip_strategy'`); n != 1 {
		t.Fatalf("migration marker missing: %d", n)
	}
	sid, err := st.CreateServer(Server{Name: "s1", Host: "203.0.113.8", Port: 22, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	sv, err := st.GetServer(sid)
	if err != nil || sv == nil {
		t.Fatalf("get: %v %+v", err, sv)
	}
	if sv.EgressIPStrategy != "" {
		t.Fatalf("default must be empty, got %q", sv.EgressIPStrategy)
	}
}

func TestServerEgressIPStrategyMigrationUpgradesExistingDatabase(t *testing.T) {
	st := openUnmigrated(t)
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	sid, err := st.CreateServer(Server{Name: "old", Host: "203.0.113.9", Port: 22, Enabled: true, EgressIPStrategy: "prefer_ipv4"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM schema_migrations WHERE version='000014_server_egress_ip_strategy'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`ALTER TABLE servers DROP COLUMN egress_ip_strategy`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if n := scalar(t, st, `SELECT COUNT(*) FROM pragma_table_info('servers') WHERE name='egress_ip_strategy'`); n != 1 {
		t.Fatal("column missing after upgrade")
	}
	sv, err := st.GetServer(sid)
	if err != nil || sv == nil {
		t.Fatalf("get after upgrade: %v %+v", err, sv)
	}
	if sv.EgressIPStrategy != "" {
		t.Fatalf("upgraded row must keep current behaviour (empty), got %q", sv.EgressIPStrategy)
	}
	// Idempotent when the column already exists.
	if _, err := st.db.Exec(`DELETE FROM schema_migrations WHERE version='000014_server_egress_ip_strategy'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("re-run with column present: %v", err)
	}
}

func TestServerEgressIPStrategyRoundTripAndRejectsInvalid(t *testing.T) {
	st := newRefundStore(t)
	if _, err := st.CreateServer(Server{Name: "bad", Host: "203.0.113.10", EgressIPStrategy: "ipv4"}); err == nil {
		t.Fatal("invalid strategy accepted on create")
	}
	sid, err := st.CreateServer(Server{Name: "ok", Host: "203.0.113.10", Port: 22, Enabled: true, EgressIPStrategy: " ipv6_only "})
	if err != nil {
		t.Fatal(err)
	}
	sv, err := st.GetServer(sid)
	if err != nil || sv == nil || sv.EgressIPStrategy != "ipv6_only" {
		t.Fatalf("trimmed persist: %+v %v", sv, err)
	}
	sv.EgressIPStrategy = "prefer_ipv6"
	if err := st.UpdateServer(*sv); err != nil {
		t.Fatal(err)
	}
	sv, _ = st.GetServer(sid)
	if sv.EgressIPStrategy != "prefer_ipv6" {
		t.Fatalf("update: %q", sv.EgressIPStrategy)
	}
	sv.EgressIPStrategy = "bogus"
	if err := st.UpdateServer(*sv); err == nil {
		t.Fatal("invalid strategy accepted on update")
	}
}

func TestBuildSingboxConfigForServerAppliesEgressIPStrategy(t *testing.T) {
	st := newRefundStore(t)
	sid, err := st.CreateServer(Server{Name: "s1", Host: "203.0.113.11", Port: 22, Enabled: true, EgressIPStrategy: "prefer_ipv4"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSbInbound(&SbInbound{ServerID: sid, Type: "mixed", Tag: "mix", Listen: "127.0.0.1", ListenPort: 17890, Options: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	users := map[string][]singbox.User{"mix": {{Name: "tester", Password: "testpass"}}}
	raw, err := st.BuildSingboxConfigForServer(sid, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	var direct map[string]interface{}
	for _, it := range cfg["outbounds"].([]interface{}) {
		m := it.(map[string]interface{})
		if m["type"] == "direct" && m["tag"] == "direct" {
			direct = m
			break
		}
	}
	if direct == nil {
		t.Fatal("direct outbound missing")
	}
	res, _ := direct["domain_resolver"].(map[string]interface{})
	if res["strategy"] != "prefer_ipv4" || res["server"] != "local" {
		t.Fatalf("domain_resolver: %v", res)
	}
	if _, ok := direct["domain_strategy"]; ok || strings.Contains(string(raw), `"domain_strategy"`) {
		t.Fatalf("legacy domain_strategy present: %s", raw)
	}

	// Changing nothing on a default server must keep current config shape.
	sid2, err := st.CreateServer(Server{Name: "s2", Host: "203.0.113.12", Port: 22, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSbInbound(&SbInbound{ServerID: sid2, Type: "mixed", Tag: "mix2", Listen: "127.0.0.1", ListenPort: 17891, Options: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	raw2, err := st.BuildSingboxConfigForServer(sid2, singbox.DefaultBaseConfig, "127.0.0.1:18080", map[string][]singbox.User{"mix2": {{Name: "tester", Password: "testpass"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw2), `"domain_resolver"`) {
		t.Fatalf("empty strategy must not set domain_resolver: %s", raw2)
	}
}
