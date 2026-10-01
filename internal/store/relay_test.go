package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"qingzhou/internal/singbox"
)

// TestRelayChaining exercises the full relay wiring: a relay inbound on server 1
// pointing at a landing inbound on server 2 must (a) get an upstream outbound +
// route rule in server 1's config, and (b) inject a matching relay user into the
// landing inbound in server 2's config.
func TestRelayChaining(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}

	relaySrv, err := st.CreateServer(Server{Name: "relay", Host: "1.2.3.4", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	landingSrv, err := st.CreateServer(Server{Name: "landing", Host: "5.6.7.8", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// Landing inbound (trojan) on the landing server — exits to internet.
	landingID, err := st.SaveSbInbound(&SbInbound{
		ServerID: landingSrv, Type: "trojan", Tag: "L-trojan", ListenPort: 8443,
		Options: `{}`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Relay inbound (vless) on server 1 → forwards to the landing inbound.
	relayID, err := st.SaveSbInbound(&SbInbound{
		ServerID: relaySrv, Type: "vless", Tag: "R-vless", ListenPort: 443,
		Options: `{}`, Enabled: true, UpstreamInboundID: landingID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = relayID

	users := map[string][]singbox.User{
		"R-vless":  {{Name: "u1", UUID: "11111111-1111-1111-1111-111111111111"}},
		"L-trojan": {{Name: "u1", Password: "pw"}},
	}

	// --- Server 1 (relay): must carry the upstream outbound + route rule. ---
	relayCfg, err := st.BuildSingboxConfigForServer(relaySrv, singbox.DefaultBaseConfig, "", users)
	if err != nil {
		t.Fatal(err)
	}
	var rc map[string]any
	if err := json.Unmarshal(relayCfg, &rc); err != nil {
		t.Fatal(err)
	}
	wantTag := "relay-to-" + itoa(landingID)
	if !hasOutbound(rc, wantTag) {
		t.Errorf("relay server config missing upstream outbound %q:\n%s", wantTag, relayCfg)
	}
	if !hasRouteToOutbound(rc, wantTag, "R-vless") {
		t.Errorf("relay server config missing route rule R-vless -> %q:\n%s", wantTag, relayCfg)
	}

	// --- Server 2 (landing): must inject the relay user into the landing inbound. ---
	landingCfg, err := st.BuildSingboxConfigForServer(landingSrv, singbox.DefaultBaseConfig, "", users)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(landingCfg), "relay_"+itoa(landingID)) {
		t.Errorf("landing config missing injected relay user relay_%d:\n%s", landingID, landingCfg)
	}

	// The relay credential must be identical on both ends.
	ib, _ := st.GetSbInbound(landingID)
	if ib.RelaySecret == "" {
		t.Fatal("landing inbound relay_secret was not persisted")
	}
	// Landing is trojan, so its injected user (and the relay outbound) authenticate
	// with the derived password — the same value must appear on both ends.
	_, wantPassword := relayCred(ib.RelaySecret)
	if !strings.Contains(string(landingCfg), wantPassword) {
		t.Errorf("landing relay user password missing (want %s):\n%s", wantPassword, landingCfg)
	}
	if !strings.Contains(string(relayCfg), wantPassword) {
		t.Errorf("relay outbound password mismatch (want %s):\n%s", wantPassword, relayCfg)
	}
}

// TestRelayMultiHopChain exercises a full multi-hop chain with an egress tail:
// A (server 1) → B (server 2) → C (server 3) → proxy egress. Per-server wiring
// must compose: each hop routes its inbound into the next hop's outbound, and
// each landing injects the relay credential of whoever dials it.
func TestRelayMultiHopChain(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}

	srv1, _ := st.CreateServer(Server{Name: "s1", Host: "1.1.1.1", Enabled: true})
	srv2, _ := st.CreateServer(Server{Name: "s2", Host: "2.2.2.2", Enabled: true})
	srv3, _ := st.CreateServer(Server{Name: "s3", Host: "3.3.3.3", Enabled: true})

	egID, err := st.SaveSbEgress(&SbEgress{Name: "static-ip", Type: "socks", Host: "9.9.9.9", Port: 1080})
	if err != nil {
		t.Fatal(err)
	}
	// C: terminal landing on server 3, exits through the purchased proxy.
	cID, err := st.SaveSbInbound(&SbInbound{
		ServerID: srv3, Type: "vless", Tag: "C-vless", ListenPort: 443,
		Options: `{}`, Enabled: true, EgressID: egID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// B: middle hop on server 2 — landing for A, relay toward C.
	bID, err := st.SaveSbInbound(&SbInbound{
		ServerID: srv2, Type: "trojan", Tag: "B-trojan", ListenPort: 8443,
		Options: `{}`, Enabled: true, UpstreamInboundID: cID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A: entry on server 1.
	if _, err := st.SaveSbInbound(&SbInbound{
		ServerID: srv1, Type: "vless", Tag: "A-vless", ListenPort: 443,
		Options: `{}`, Enabled: true, UpstreamInboundID: bID,
	}); err != nil {
		t.Fatal(err)
	}

	users := map[string][]singbox.User{
		"A-vless": {{Name: "u1", UUID: "11111111-1111-1111-1111-111111111111"}},
	}
	cfgFor := func(sid int64) map[string]any {
		raw, err := st.BuildSingboxConfigForServer(sid, singbox.DefaultBaseConfig, "", users)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	// Server 1: A routes into relay-to-B.
	c1 := cfgFor(srv1)
	if !hasOutbound(c1, "relay-to-"+itoa(bID)) || !hasRouteToOutbound(c1, "relay-to-"+itoa(bID), "A-vless") {
		t.Errorf("server1 missing A→B wiring: %v", c1["route"])
	}

	// Server 2: B carries A's relay user AND routes into relay-to-C.
	c2 := cfgFor(srv2)
	b2, _ := json.Marshal(c2)
	if !strings.Contains(string(b2), "relay_"+itoa(bID)) {
		t.Errorf("server2 missing injected relay user relay_%d", bID)
	}
	if !hasOutbound(c2, "relay-to-"+itoa(cID)) || !hasRouteToOutbound(c2, "relay-to-"+itoa(cID), "B-trojan") {
		t.Errorf("server2 missing B→C wiring: %v", c2["route"])
	}

	// Server 3: C carries B's relay user AND routes into the egress outbound.
	c3 := cfgFor(srv3)
	b3, _ := json.Marshal(c3)
	if !strings.Contains(string(b3), "relay_"+itoa(cID)) {
		t.Errorf("server3 missing injected relay user relay_%d", cID)
	}
	if !hasOutbound(c3, "egress-"+itoa(egID)) || !hasRouteToOutbound(c3, "egress-"+itoa(egID), "C-vless") {
		t.Errorf("server3 missing C→egress wiring: %v", c3["route"])
	}
}

// TestDeleteLandingUnchainsRelays covers the 链路拓扑 bug: deleting a landing
// inbound must clear the upstream link on every inbound that relayed to it,
// instead of leaving a dangling id that the topology keeps drawing as a
// 「落地已失效」hop to an inbound that no longer exists.
func TestDeleteLandingUnchainsRelays(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}

	relaySrv, _ := st.CreateServer(Server{Name: "relay", Host: "1.2.3.4", Enabled: true})
	landingSrv, _ := st.CreateServer(Server{Name: "landing", Host: "5.6.7.8", Enabled: true})

	landingID, err := st.SaveSbInbound(&SbInbound{
		ServerID: landingSrv, Type: "trojan", Tag: "L-trojan", ListenPort: 8443,
		Options: `{}`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	relayID, err := st.SaveSbInbound(&SbInbound{
		ServerID: relaySrv, Type: "vless", Tag: "R-vless", ListenPort: 443,
		Options: `{}`, Enabled: true, UpstreamInboundID: landingID,
	})
	if err != nil {
		t.Fatal(err)
	}

	touched, err := st.DeleteSbInbound(landingID)
	if err != nil {
		t.Fatal(err)
	}
	if len(touched) != 1 || touched[0] != relaySrv {
		t.Errorf("un-chained servers = %v, want [%d] so the relay machine rebuilds", touched, relaySrv)
	}

	relay, err := st.GetSbInbound(relayID)
	if err != nil {
		t.Fatal(err)
	}
	if relay == nil {
		t.Fatal("relay inbound was deleted along with its landing")
	}
	if relay.UpstreamInboundID != 0 {
		t.Errorf("relay still points at deleted landing %d — 链路拓扑 will keep showing it", relay.UpstreamInboundID)
	}
	// Un-chaining is not a neutral edit: this relay now exits from its own
	// machine's IP instead of the landing's. Clearing the link without recording
	// that would make the change invisible.
	if !relay.UpstreamBroken {
		t.Error("relay was silently downgraded to a direct exit with nothing marking it")
	}
}

// The flag has to be dismissible or it stops being read — but only by something
// that actually resolves it. Enable/disable goes through the same SaveSbInbound,
// and an unrelated toggle must not clear a warning that traffic is leaving from
// the wrong machine.
func TestUpstreamBrokenClearsOnlyOnResolution(t *testing.T) {
	newBrokenRelay := func(t *testing.T) (*Store, int64) {
		t.Helper()
		st, err := Open(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		if err := st.Migrate(); err != nil {
			t.Fatal(err)
		}
		landingID, err := st.SaveSbInbound(&SbInbound{
			ServerID: 0, Type: "trojan", Tag: "L-trojan", ListenPort: 8443, Options: `{}`, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		relayID, err := st.SaveSbInbound(&SbInbound{
			ServerID: 0, Type: "vless", Tag: "R-vless", ListenPort: 443,
			Options: `{}`, Enabled: true, UpstreamInboundID: landingID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.DeleteSbInbound(landingID); err != nil {
			t.Fatal(err)
		}
		if ib, _ := st.GetSbInbound(relayID); ib == nil || !ib.UpstreamBroken {
			t.Fatal("precondition: delete did not set upstream_broken")
		}
		return st, relayID
	}

	t.Run("toggling enabled preserves it", func(t *testing.T) {
		st, relayID := newBrokenRelay(t)
		relay, _ := st.GetSbInbound(relayID)
		relay.Enabled = false
		if _, err := st.SaveSbInbound(relay); err != nil {
			t.Fatal(err)
		}
		after, _ := st.GetSbInbound(relayID)
		if !after.UpstreamBroken {
			t.Error("disabling the inbound dismissed the downgrade warning — an unrelated action must not")
		}
	})

	t.Run("re-pointing at a new landing clears it", func(t *testing.T) {
		st, relayID := newBrokenRelay(t)
		newLanding, err := st.SaveSbInbound(&SbInbound{
			ServerID: 0, Type: "trojan", Tag: "L2-trojan", ListenPort: 9443, Options: `{}`, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		relay, _ := st.GetSbInbound(relayID)
		relay.UpstreamInboundID = newLanding
		if _, err := st.SaveSbInbound(relay); err != nil {
			t.Fatal(err)
		}
		after, _ := st.GetSbInbound(relayID)
		if after.UpstreamBroken {
			t.Error("the chain was repaired but the warning stuck around")
		}
	})

	t.Run("acknowledging clears it", func(t *testing.T) {
		st, relayID := newBrokenRelay(t)
		if err := st.AckUpstreamBroken(relayID); err != nil {
			t.Fatal(err)
		}
		after, _ := st.GetSbInbound(relayID)
		if after.UpstreamBroken {
			t.Error("acknowledging left the warning in place — it would never go away")
		}
		if after.UpstreamInboundID != 0 {
			t.Error("acknowledging changed the chain; it must only clear the warning")
		}
	})
}

// TestMigrateClearsDanglingUpstream covers DBs that already accumulated dangling
// relay links from deletions made before DeleteSbInbound un-chained referrers.
func TestMigrateClearsDanglingUpstream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	relayID, err := st.SaveSbInbound(&SbInbound{
		ServerID: 0, Type: "vless", Tag: "R-vless", ListenPort: 443,
		Options: `{}`, Enabled: true, UpstreamInboundID: 4242, // landing long gone
	})
	if err != nil {
		t.Fatal(err)
	}
	rewindVersionedBaseline(t, st)
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	relay, _ := st.GetSbInbound(relayID)
	if relay == nil || relay.UpstreamInboundID != 0 {
		t.Errorf("migrate left a dangling upstream: %+v", relay)
	}
	// Upgrading must not be what makes an already-broken chain invisible.
	if relay != nil && !relay.UpstreamBroken {
		t.Error("migrate cleared the dangling link without flagging the downgrade")
	}
}

func itoa(n int64) string {
	return strings.TrimSpace(string(jsonNumber(n)))
}
func jsonNumber(n int64) []byte { b, _ := json.Marshal(n); return b }

func hasOutbound(cfg map[string]any, tag string) bool {
	obs, _ := cfg["outbounds"].([]any)
	for _, o := range obs {
		if m, ok := o.(map[string]any); ok && m["tag"] == tag {
			return true
		}
	}
	return false
}

func hasRouteToOutbound(cfg map[string]any, outboundTag, inboundTag string) bool {
	route, _ := cfg["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok || m["outbound"] != outboundTag {
			continue
		}
		ins, _ := m["inbound"].([]any)
		for _, in := range ins {
			if in == inboundTag {
				return true
			}
		}
	}
	return false
}
