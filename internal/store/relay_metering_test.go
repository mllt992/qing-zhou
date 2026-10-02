package store

import (
	"encoding/json"
	"strings"
	"testing"

	"qingzhou/internal/singbox"
)

func meteringRelayFixture(t *testing.T) (*Store, int64, int64, int64, int64, map[string][]singbox.User) {
	t.Helper()
	st := newRefundStore(t)
	st.SetSecretKey([]byte("test-encryption-key"))
	a, _ := st.CreateServer(Server{Name: "DMIT", Host: "192.0.2.10", Enabled: true})
	b, _ := st.CreateServer(Server{Name: "Verizon", Host: "192.0.2.20", Enabled: true})
	bi, err := st.SaveSbInbound(&SbInbound{ServerID: b, Type: "trojan", Tag: "landing", ListenPort: 8443, Options: `{}`, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ai, err := st.SaveSbInbound(&SbInbound{ServerID: a, Type: "vless", Tag: "entry", ListenPort: 443, Options: `{}`, Enabled: true, UpstreamInboundID: bi})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetSetting(RelayMeteringSetting, "true"); err != nil {
		t.Fatal(err)
	}
	return st, a, b, ai, bi, map[string][]singbox.User{"entry": {{Name: "qz_test", UUID: "11111111-1111-1111-1111-111111111111"}}}
}
func TestMeteringRelayWaitsForDownstreamAndRetainsCompatibility(t *testing.T) {
	st, a, b, _, bi, users := meteringRelayFixture(t)
	if err := st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	links, err := st.RelayMeteringLinks()
	if err != nil || len(links) != 1 {
		t.Fatalf("links %+v %v", links, err)
	}
	link := links[0]
	if !strings.HasPrefix(link.Credential, encPrefix) {
		t.Fatal("credential not encrypted")
	}
	if _, err = st.BuildSingboxConfigForServer(a, singbox.DefaultBaseConfig, "127.0.0.1:18080", users); err == nil {
		t.Fatal("entry switched before landing acceptance")
	}
	landing, err := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(landing), link.IdentityName) || !strings.Contains(string(landing), "relay_"+itoa(bi)) {
		t.Fatal("new/old identities not both accepted")
	}
	if err = st.RecordRelayConfigApplied(b, landing); err != nil {
		t.Fatal(err)
	}
	entry, err := st.BuildSingboxConfigForServer(a, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entry), link.outboundTag()) {
		t.Fatal("entry did not use distinct link outbound")
	}
	u, err := st.meteringRelayUser(link)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entry), u.Password) || !strings.Contains(string(landing), u.Password) {
		t.Fatal("wire credentials do not match")
	}
	if err = st.RecordRelayConfigApplied(a, entry); err != nil {
		t.Fatal(err)
	}
	links, _ = st.RelayMeteringLinks()
	if links[0].State != "active" {
		t.Fatalf("state %s", links[0].State)
	}
	before, _ := st.RelayMeteringProgress()
	if err = st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	after, _ := st.RelayMeteringProgress()
	if before != after {
		t.Fatal("no-op planner changed readiness")
	}
	if err = st.SetSetting(RelayMeteringSetting, "false"); err != nil {
		t.Fatal(err)
	}
	legacy, err := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(legacy), link.IdentityName) {
		t.Fatal("opt-out revoked credentials before upstream switched")
	}
	view, _ := json.Marshal(link)
	if strings.Contains(string(view), u.Password) || strings.Contains(string(view), link.Credential) || strings.Contains(string(view), link.IdentityName) {
		t.Fatal("report leaked credential/identity")
	}
}
func TestMeteringRelayDistinctEdgesSameLanding(t *testing.T) {
	st, a, b, _, bi, users := meteringRelayFixture(t)
	_, err := st.SaveSbInbound(&SbInbound{ServerID: a, Type: "vless", Tag: "entry-two", ListenPort: 1443, Options: `{}`, Enabled: true, UpstreamInboundID: bi})
	if err != nil {
		t.Fatal(err)
	}
	users["entry-two"] = users["entry"]
	if err = st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	links, _ := st.RelayMeteringLinks()
	if len(links) != 2 || links[0].IdentityName == links[1].IdentityName {
		t.Fatal("edges shared identities")
	}
	landing, err := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RecordRelayConfigApplied(b, landing); err != nil {
		t.Fatal(err)
	}
	entry, err := st.BuildSingboxConfigForServer(a, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range links {
		if !strings.Contains(string(entry), link.outboundTag()) || !strings.Contains(string(landing), link.IdentityName) {
			t.Fatal("edge missing from generated configs")
		}
	}
	p := NewTrafficPoll(b, map[string]UsageDelta{links[0].IdentityName: {Up: 1, Down: 2}, links[1].IdentityName: {Up: 5, Down: 7}})
	if n, err := st.RecordTrafficPoll(p); err != nil || n != 0 {
		t.Fatalf("relay debited user: %d %v", n, err)
	}
	report, err := st.ServerServiceTraffic(b, 0)
	if err != nil || report.Total != 15 || len(report.Sources) != 2 || report.BillableTotal != 0 {
		t.Fatalf("report %+v %v", report, err)
	}
}
func TestMeteringRelayCapabilityAndSpecGuard(t *testing.T) {
	st, a, b, _, bi, users := meteringRelayFixture(t)
	if err := st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	noStats, err := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "", users)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RecordRelayConfigApplied(b, noStats); err != nil {
		t.Fatal(err)
	}
	if _, err = st.BuildSingboxConfigForServer(a, singbox.DefaultBaseConfig, "", users); err == nil {
		t.Fatal("unmetered landing was accepted")
	}
	landing, _ := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err = st.RecordRelayConfigApplied(b, landing); err != nil {
		t.Fatal(err)
	}
	ib, _ := st.GetSbInbound(bi)
	ib.ListenPort++
	if _, err = st.SaveSbInbound(ib); err != nil {
		t.Fatal(err)
	}
	if err = st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	if _, err = st.BuildSingboxConfigForServer(a, singbox.DefaultBaseConfig, "", users); err == nil {
		t.Fatal("entry dialed changed target before target apply")
	}
}
func TestMeteringRelayRejectsUnsupportedAndCycles(t *testing.T) {
	for _, protocol := range []string{"mixed", "shadowsocks"} {
		t.Run(protocol, func(t *testing.T) {
			st, _, _, _, bi, _ := meteringRelayFixture(t)
			ib, _ := st.GetSbInbound(bi)
			ib.Type = protocol
			ib.Options = `{"method":"2022-blake3-chacha20-poly1305"}`
			if _, err := st.SaveSbInbound(ib); err != nil {
				t.Fatal(err)
			}
			if st.PrepareRelayMetering() == nil {
				t.Fatal("unsupported relay accepted")
			}
		})
	}
	st, _, _, ai, bi, _ := meteringRelayFixture(t)
	ib, _ := st.GetSbInbound(bi)
	ib.UpstreamInboundID = ai
	if _, err := st.SaveSbInbound(ib); err != nil {
		t.Fatal(err)
	}
	if st.PrepareRelayMetering() == nil {
		t.Fatal("cyclic topology accepted")
	}
}

func TestMeteringRelayStaleApplyCannotAcceptEditedTarget(t *testing.T) {
	st, a, b, _, bi, users := meteringRelayFixture(t)
	if err := st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	old, err := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := st.RelayMeteringLinks()
	oldIdentity := before[0].IdentityName
	target, _ := st.GetSbInbound(bi)
	target.ListenPort++
	if _, err = st.SaveSbInbound(target); err != nil {
		t.Fatal(err)
	}
	// The asynchronous apply finishes after the edit, but before a planner pass.
	if err = st.RecordRelayConfigApplied(b, old); err != nil {
		t.Fatal(err)
	}
	links, _ := st.RelayMeteringLinks()
	if links[0].State != "prepared" {
		t.Fatal("stale apply accepted edited DB target")
	}
	if err = st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	links, _ = st.RelayMeteringLinks()
	if links[0].Generation != 2 || links[0].IdentityName == oldIdentity {
		t.Fatal("changed spec reused identity generation")
	}
	if err = st.RecordRelayConfigApplied(b, old); err != nil {
		t.Fatal(err)
	}
	if _, err = st.BuildSingboxConfigForServer(a, singbox.DefaultBaseConfig, "127.0.0.1:18080", users); err == nil {
		t.Fatal("old apply confirmed new generation")
	}
	current, err := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), oldIdentity) {
		t.Fatal("old generation removed prematurely")
	}
	if err = st.RecordRelayConfigApplied(b, current); err != nil {
		t.Fatal(err)
	}
	if _, err = st.RecordTrafficPoll(NewTrafficPoll(b, map[string]UsageDelta{oldIdentity: {Down: 50}})); err != nil {
		t.Fatal(err)
	}
	report, err := st.ServerServiceTraffic(b, 0)
	if err != nil || report.Total != 50 || len(report.Sources) != 1 || report.Sources[0].Kind != "relay_link" {
		t.Fatalf("late old generation lost: %+v %v", report, err)
	}
}
func TestMeteringRelayMovedMachineGetsNewHistoricalEdge(t *testing.T) {
	st, _, b, _, bi, users := meteringRelayFixture(t)
	if err := st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	old, _ := st.RelayMeteringLinks()
	c, _ := st.CreateServer(Server{Name: "new-landing", Host: "192.0.2.30", Enabled: true})
	target, _ := st.GetSbInbound(bi)
	target.ServerID = c
	if _, err := st.SaveSbInbound(target); err != nil {
		t.Fatal(err)
	}
	if err := st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	links, _ := st.RelayMeteringLinks()
	if len(links) != 2 || links[0].TargetServerID != b || links[1].TargetServerID != c || links[0].ID == links[1].ID {
		t.Fatalf("machine ownership overwritten %+v", links)
	}
	raw, err := st.BuildSingboxConfigForServer(c, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), old[0].IdentityName) {
		t.Fatal("old-machine credential was granted to moved machine")
	}
}
func TestMeteringRelayAckChecksInboundAndCredential(t *testing.T) {
	st, _, b, _, _, users := meteringRelayFixture(t)
	if err := st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	raw, err := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"tag", "password"} {
		var cfg map[string]interface{}
		if err = json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		ib := cfg["inbounds"].([]interface{})[0].(map[string]interface{})
		if change == "tag" {
			ib["tag"] = "wrong-inbound"
		} else {
			for _, u := range ib["users"].([]interface{}) {
				u.(map[string]interface{})["password"] = "wrong-password"
			}
		}
		bad, _ := json.Marshal(cfg)
		if err = st.RecordRelayConfigApplied(b, bad); err != nil {
			t.Fatal(err)
		}
		links, _ := st.RelayMeteringLinks()
		if links[0].State != "prepared" {
			t.Fatalf("accepted wrong %s", change)
		}
	}
}
func TestMeteringRelayRejectsUnmeteredEntryAndOverridingRule(t *testing.T) {
	st, a, b, _, _, users := meteringRelayFixture(t)
	if err := st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	landing, _ := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err := st.RecordRelayConfigApplied(b, landing); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BuildSingboxConfigForServer(a, singbox.DefaultBaseConfig, "", users); err == nil {
		t.Fatal("unmetered entry switched")
	}
	base := `{"outbounds":[{"type":"direct","tag":"direct"}],"route":{"rules":[{"outbound":"direct"}],"final":"direct"}}`
	if _, err := st.BuildSingboxConfigForServer(a, base, "127.0.0.1:18080", users); err == nil {
		t.Fatal("earlier custom rule silently bypassed metering route")
	}
}
