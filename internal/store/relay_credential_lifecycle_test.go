package store

import (
	"qingzhou/internal/singbox"
	"strings"
	"testing"
	"time"
)

func activateMeteringFixture(t *testing.T) (*Store, int64, int64, int64, map[string][]singbox.User) {
	t.Helper()
	st, a, b, _, bi, users := meteringRelayFixture(t)
	if err := st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
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
	if err = st.RecordRelayConfigApplied(a, entry); err != nil {
		t.Fatal(err)
	}
	return st, a, b, bi, users
}
func recordDrainedPolls(t *testing.T, st *Store, serverID int64) {
	t.Helper()
	for i := int64(1); i <= 2; i++ {
		p := NewTrafficPoll(serverID, map[string]UsageDelta{})
		p.ObservedAt = time.Now().Unix() + i
		if _, err := st.RecordTrafficPoll(p); err != nil {
			t.Fatal(err)
		}
	}
}
func TestLegacyRelayRetirementDrainAndSafeRestore(t *testing.T) {
	st, _, b, bi, users := activateMeteringFixture(t)
	v := RelayCredentialView{Kind: "legacy", ServerID: b, InboundID: bi}
	if err := st.ChangeRelayCredential(1, v, "retire"); err == nil {
		t.Fatal("retired before counter drain")
	}
	recordDrainedPolls(t, st, b)
	if err := st.ChangeRelayCredential(1, v, "retire"); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfigureTrafficMetering(false, false); err == nil {
		t.Fatal("disabled while rollback credential unavailable")
	}
	raw, err := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "relay_"+itoa(bi)) {
		t.Fatal("retired legacy credential still rendered")
	}
	if err = st.RecordRelayConfigApplied(b, raw); err != nil {
		t.Fatal(err)
	}
	state, _ := st.LegacyRelayCompatibility(b, bi)
	if state != "retired" {
		t.Fatalf("phase %s", state)
	}
	if err = st.ChangeRelayCredential(1, v, "restore"); err != nil {
		t.Fatal(err)
	}
	if err = st.ConfigureTrafficMetering(false, false); err == nil {
		t.Fatal("disabled before restore applied")
	}
	restored, err := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restored), "relay_"+itoa(bi)) {
		t.Fatal("legacy restore missing")
	}
	if err = st.RecordRelayConfigApplied(b, restored); err != nil {
		t.Fatal(err)
	}
	if err = st.ConfigureTrafficMetering(false, false); err != nil {
		t.Fatal(err)
	}
	var events int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM relay_credential_audit`).Scan(&events); err != nil || events != 2 {
		t.Fatalf("audit %d %v", events, err)
	}
}
func TestRetireOneGenerationKeepsHistoricalAttribution(t *testing.T) {
	st, a, b, bi, users := activateMeteringFixture(t)
	old, _ := st.RelayMeteringLinks()
	oldName := old[0].IdentityName
	target, _ := st.GetSbInbound(bi)
	target.ListenPort++
	if _, err := st.SaveSbInbound(target); err != nil {
		t.Fatal(err)
	}
	if err := st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	landing, _ := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err := st.RecordRelayConfigApplied(b, landing); err != nil {
		t.Fatal(err)
	}
	entry, err := st.BuildSingboxConfigForServer(a, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RecordRelayConfigApplied(a, entry); err != nil {
		t.Fatal(err)
	}
	recordDrainedPolls(t, st, b)
	v := RelayCredentialView{Kind: "generation", LinkID: old[0].ID, ServerID: b, InboundID: bi, Generation: 1}
	if err = st.ChangeRelayCredential(1, v, "retire"); err != nil {
		t.Fatal(err)
	}
	raw, err := st.BuildSingboxConfigForServer(b, singbox.DefaultBaseConfig, "127.0.0.1:18080", users)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), oldName) {
		t.Fatal("retired generation still accepted")
	}
	if _, err = st.RecordTrafficPoll(NewTrafficPoll(b, map[string]UsageDelta{oldName: {Down: 20}})); err != nil {
		t.Fatal(err)
	}
	report, err := st.ServerServiceTraffic(b, 0)
	if err != nil || report.Sources[0].Kind != "relay_link" {
		t.Fatal("retirement erased old mapping")
	}
	v.Generation = 2
	if err = st.ChangeRelayCredential(1, v, "retire"); err == nil {
		t.Fatal("current generation could be retired")
	}
}
