package store

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/sbver"
	"qingzhou/internal/singbox"
)

// This matrix exercises the planner, generated JSON and acknowledgement state
// machine only. A passing pair is not evidence of a real protocol handshake,
// packet forwarding, concurrent capacity or third-party client compatibility.
type meteringProtocolCase struct {
	name, protocol, method string
}

func meteringProtocolCases() []meteringProtocolCase {
	return []meteringProtocolCase{
		{name: "vless", protocol: "vless"},
		{name: "vmess", protocol: "vmess"},
		{name: "trojan", protocol: "trojan"},
		{name: "hysteria", protocol: "hysteria"},
		{name: "hysteria2", protocol: "hysteria2"},
		{name: "tuic", protocol: "tuic"},
		{name: "anytls", protocol: "anytls"},
		{name: "ss2022-aes128", protocol: "shadowsocks", method: "2022-blake3-aes-128-gcm"},
		{name: "ss2022-aes256", protocol: "shadowsocks", method: "2022-blake3-aes-256-gcm"},
	}
}

func (p meteringProtocolCase) options() map[string]any {
	opts := map[string]any{}
	switch p.protocol {
	case "vless":
		opts["flow"] = "none"
		opts["transport"] = map[string]any{"type": "ws", "path": "/metering-logic"}
	case "vmess":
		opts["transport"] = map[string]any{"type": "grpc", "service_name": "metering-logic"}
	case "hysteria":
		opts["up_mbps"], opts["down_mbps"] = 100, 100
		opts["obfs"] = "synthetic-logic-hysteria"
	case "hysteria2":
		opts["obfs"] = map[string]any{"type": "salamander", "password": "synthetic-logic-hysteria2"}
	case "tuic":
		opts["congestion_control"] = "cubic"
	case "shadowsocks":
		opts["method"] = p.method
		opts["password"] = singbox.DeriveSSKey("synthetic-logic-root-key", p.method)
	}
	return opts
}

func (p meteringProtocolCase) authFields() []string {
	switch p.protocol {
	case "vless":
		return []string{"uuid", "flow"}
	case "vmess":
		return []string{"uuid", "alterId"}
	case "tuic":
		return []string{"uuid", "password"}
	case "hysteria":
		return []string{"auth_str"}
	default:
		return []string{"password"}
	}
}

func saveMeteringProtocolInbound(t *testing.T, st *Store, inbound *SbInbound, p meteringProtocolCase) int64 {
	t.Helper()
	var err error
	inbound.Type = p.protocol
	inbound.TlsID = 0
	if p.protocol != "shadowsocks" && p.protocol != "mixed" {
		serverTLS, clientTLS, _ := relayFixtureTLS(t)
		inbound.TlsID, err = st.SaveSbTls(&SbTls{ServerID: inbound.ServerID, Name: "protocol-logic-tls", Mode: "tls", ServerJSON: serverTLS, ClientJSON: clientTLS})
		if err != nil {
			t.Fatal(err)
		}
	}
	opts, err := json.Marshal(p.options())
	if err != nil {
		t.Fatal(err)
	}
	inbound.Options = string(opts)
	id, err := st.SaveSbInbound(inbound)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newProtocolMeteringFixture(t *testing.T, protocols ...meteringProtocolCase) userMeteringFixture {
	t.Helper()
	f := userMeteringFixture{st: newRefundStore(t), servers: make([]int64, len(protocols)), inbounds: make([]int64, len(protocols)), tags: make([]string, len(protocols))}
	f.st.SetSecretKey([]byte("protocol-logic-state-fixture"))
	for hop := range protocols {
		id, err := f.st.CreateServer(Server{Name: fmt.Sprintf("protocol-hop-%d", hop), Host: fmt.Sprintf("192.0.2.%d", hop+20), Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		f.servers[hop], f.tags[hop] = id, fmt.Sprintf("protocol-stage-%d", hop)
	}
	for hop := len(protocols) - 1; hop >= 0; hop-- {
		var upstream int64
		if hop+1 < len(protocols) {
			upstream = f.inbounds[hop+1]
		}
		f.inbounds[hop] = saveMeteringProtocolInbound(t, f.st, &SbInbound{ServerID: f.servers[hop], Tag: f.tags[hop], Listen: "127.0.0.1", ListenPort: 2443 + hop, Enabled: true, UpstreamInboundID: upstream}, protocols[hop])
	}
	f.pkg = mkPlan(t, f.st, "protocol-logic-plan", 10, 10, 30)
	bindPlanToInbound(t, f.st, f.pkg.ID, f.tags[0])
	for owner := 0; owner < 2; owner++ {
		id := mkUser(t, f.st, fmt.Sprintf("protocol-owner-%d", owner))
		buy(t, f.st, id, f.pkg)
		f.owners = append(f.owners, id)
	}
	// This planner-only fixture models a reviewed installed capability for
	// activation preflight. It is not a real handshake or live-process proof.
	for _, id := range f.servers {
		if err := f.st.SetNodeSingbox(id, sbver.Parse("sing-box version "+sbver.TrojanHandshakeFixVersion+"\nTags: with_v2ray_api")); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.st.ConfigureTrafficMetering(true, false, true); err != nil {
		t.Fatal(err)
	}
	if err := f.st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	return f
}

func protocolConfigObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func protocolObjectByTag(t *testing.T, cfg map[string]any, field, tag string) map[string]any {
	t.Helper()
	for _, item := range cfg[field].([]any) {
		obj := item.(map[string]any)
		if obj["tag"] == tag {
			return obj
		}
	}
	t.Fatalf("missing %s object %q", field, tag)
	return nil
}

func protocolUserByName(t *testing.T, inbound map[string]any, name string) map[string]any {
	t.Helper()
	for _, item := range inbound["users"].([]any) {
		user := item.(map[string]any)
		if user["name"] == name || user["username"] == name {
			return user
		}
	}
	t.Fatalf("missing authenticated user %q", name)
	return nil
}

func protocolCurrentUsers(t *testing.T, f userMeteringFixture) []*RelayMeteringUser {
	t.Helper()
	all, err := f.st.RelayMeteringUsers()
	if err != nil {
		t.Fatal(err)
	}
	links, err := f.st.RelayMeteringLinks()
	if err != nil {
		t.Fatal(err)
	}
	generation := map[int64]int{}
	for _, link := range links {
		generation[link.ID] = link.Generation
	}
	var current []*RelayMeteringUser
	for _, user := range all {
		if user.Enabled && user.Generation == generation[user.LinkID] {
			current = append(current, user)
		}
	}
	return current
}

func protocolAssertState(t *testing.T, f userMeteringFixture, want string) []*RelayMeteringUser {
	t.Helper()
	rows := protocolCurrentUsers(t, f)
	if len(rows) != len(f.owners)*(len(f.servers)-1) {
		t.Fatalf("current owner/hop rows=%d", len(rows))
	}
	for _, row := range rows {
		if row.State != want {
			t.Fatalf("owner=%d generation=%d state=%s, want %s", row.UserID, row.Generation, row.State, want)
		}
	}
	return rows
}

func TestRelayMeteringProtocolOrderedMatrix(t *testing.T) {
	for _, source := range meteringProtocolCases() {
		for _, target := range meteringProtocolCases() {
			t.Run(source.name+"_to_"+target.name, func(t *testing.T) {
				f := newProtocolMeteringFixture(t, source, target)
				protocolAssertState(t, f, "prepared")
				if _, err := f.config(t, 0); err == nil {
					t.Fatal("source compiled before either user's target acceptance")
				}
				down := f.apply(t, 1)
				protocolAssertState(t, f, "accepted")
				up := f.apply(t, 0)
				rows := protocolAssertState(t, f, "active")
				upCfg, downCfg := protocolConfigObject(t, up), protocolConfigObject(t, down)
				if got := len(downCfg["inbounds"].([]any)); got != 1 {
					t.Fatalf("per-user metering created %d listeners, want one shared listener", got)
				}
				landing := protocolObjectByTag(t, downCfg, "inbounds", f.tags[1])
				if landing["type"] != target.protocol {
					t.Fatal("target protocol was substituted")
				}
				if protocolObjectByTag(t, upCfg, "inbounds", f.tags[0])["type"] != source.protocol {
					t.Fatal("source protocol was substituted")
				}
				seenIdentity, seenSecret := map[string]bool{}, map[string]bool{}
				for _, row := range rows {
					if row.SourceNames == "[]" || !strings.HasPrefix(row.Credential, encPrefix) {
						t.Fatal("missing owner mapping or unencrypted relay credential")
					}
					credential, err := f.st.relayMeteringUserCredential(row)
					if err != nil {
						t.Fatal(err)
					}
					if seenIdentity[row.IdentityName] || seenSecret[credential.Password] {
						t.Fatal("two owners share a relay identity or credential")
					}
					seenIdentity[row.IdentityName], seenSecret[credential.Password] = true, true
					user := protocolUserByName(t, landing, row.IdentityName)
					out := protocolObjectByTag(t, upCfg, "outbounds", row.outboundTag())
					if out["type"] != target.protocol {
						t.Fatal("managed outbound changed protocol")
					}
					switch target.protocol {
					case "vless", "vmess":
						if user["uuid"] != credential.UUID || out["uuid"] != credential.UUID {
							t.Fatal("UUID not mapped end to end")
						}
					case "tuic":
						if user["uuid"] != credential.UUID || out["uuid"] != credential.UUID || user["password"] != credential.Password || out["password"] != credential.Password {
							t.Fatal("TUIC UUID/password pair not mapped end to end")
						}
					case "hysteria":
						if user["auth_str"] != credential.Password || out["auth_str"] != credential.Password {
							t.Fatal("Hysteria auth_str not mapped end to end")
						}
					case "shadowsocks":
						key := singbox.DeriveSSKey(credential.Password, target.method)
						root := target.options()["password"].(string)
						if user["password"] != key || out["password"] != root+":"+key || out["method"] != target.method {
							t.Fatal("SS2022 method/root/user key not mapped end to end")
						}
					default:
						if user["password"] != credential.Password || out["password"] != credential.Password {
							t.Fatal("password not mapped end to end")
						}
					}
				}
				stamp, err := f.st.RelayMeteringProgress()
				if err != nil {
					t.Fatal(err)
				}
				if err = f.st.PrepareRelayMetering(); err != nil {
					t.Fatal(err)
				}
				after := protocolAssertState(t, f, "active")
				if !reflect.DeepEqual(rows, after) {
					t.Fatal("no-op planner changed an owner, identity, credential or acknowledgement")
				}
				if now, err := f.st.RelayMeteringProgress(); err != nil || now != stamp {
					t.Fatalf("no-op readiness changed: %v", err)
				}
				for hop, original := range [][]byte{up, down} {
					raw, err := f.config(t, hop)
					if err != nil || !bytes.Equal(raw, original) {
						t.Fatalf("no-op JSON changed on hop %d: %v", hop, err)
					}
				}
			})
		}
	}
}

func TestRelayMeteringProtocolAcknowledgementFailsClosed(t *testing.T) {
	for _, protocol := range meteringProtocolCases() {
		t.Run(protocol.name, func(t *testing.T) {
			f := newProtocolMeteringFixture(t, protocol, protocol)
			down := f.apply(t, 1)
			up := f.apply(t, 0)
			rows := protocolAssertState(t, f, "active")
			subject := rows[0]
			var sourceNames []string
			if err := json.Unmarshal([]byte(subject.SourceNames), &sourceNames); err != nil || len(sourceNames) == 0 {
				t.Fatalf("source identity missing: %v", err)
			}
			type corruption struct {
				name string
				hop  int
				edit func(map[string]any)
			}
			var cases []corruption
			for _, hop := range []int{0, 1} {
				label := []string{"source", "target"}[hop]
				cases = append(cases, corruption{label + "-duplicate-inbound-tag", hop, func(cfg map[string]any) {
					inbound := protocolObjectByTag(t, cfg, "inbounds", f.tags[hop])
					cfg["inbounds"] = append(cfg["inbounds"].([]any), inbound)
				}})
				shadows := []string{"duplicate-stat-name", "duplicate-wire-key"}
				switch protocol.protocol {
				case "vless", "vmess", "tuic":
					shadows = append(shadows, "uppercase-uuid", "compact-uuid")
				case "hysteria":
					shadows = append(shadows, "base64-auth")
				case "shadowsocks":
					shadows = append(shadows, "raw-base64-key")
				}
				for _, shadow := range shadows {
					cases = append(cases, corruption{label + "-" + shadow, hop, func(cfg map[string]any) {
						name := subject.IdentityName
						if hop == 0 {
							name = sourceNames[0]
						}
						inbound := protocolObjectByTag(t, cfg, "inbounds", f.tags[hop])
						original := protocolUserByName(t, inbound, name)
						duplicate := map[string]any{}
						for key, value := range original {
							duplicate[key] = value
						}
						if shadow != "duplicate-stat-name" {
							duplicate["name"] = "synthetic-shadow-owner"
						}
						switch shadow {
						case "uppercase-uuid":
							duplicate["uuid"] = strings.ToUpper(original["uuid"].(string))
						case "compact-uuid":
							duplicate["uuid"] = strings.ReplaceAll(original["uuid"].(string), "-", "")
						case "base64-auth":
							duplicate["auth"] = base64.StdEncoding.EncodeToString([]byte(original["auth_str"].(string)))
							delete(duplicate, "auth_str")
						case "raw-base64-key":
							duplicate["password"] = strings.TrimRight(original["password"].(string), "=")
						}
						if protocol.protocol == "tuic" && shadow != "duplicate-stat-name" {
							// TUIC indexes by UUID before checking the password.
							duplicate["password"] = "different-password-same-uuid"
						}
						inbound["users"] = append(inbound["users"].([]any), duplicate)
					}})
				}
				for _, field := range protocol.authFields() {
					cases = append(cases, corruption{label + "-auth-" + field, hop, func(cfg map[string]any) {
						name := subject.IdentityName
						if hop == 0 {
							name = sourceNames[0]
						}
						user := protocolUserByName(t, protocolObjectByTag(t, cfg, "inbounds", f.tags[hop]), name)
						if field == "alterId" {
							user[field] = 7
						} else {
							user[field] = "wrong-protocol-authentication-value"
						}
					}})
				}
				for _, field := range []string{"tls", "transport"} {
					cases = append(cases, corruption{label + "-" + field, hop, func(cfg map[string]any) {
						inbound := protocolObjectByTag(t, cfg, "inbounds", f.tags[hop])
						if field == "tls" {
							inbound[field] = map[string]any{"enabled": true, "server_name": "wrong.example.test"}
						} else {
							inbound[field] = map[string]any{"type": "ws", "path": "/wrong-path"}
						}
					}})
				}
				if protocol.protocol == "shadowsocks" {
					for _, field := range []string{"method", "password"} {
						cases = append(cases, corruption{label + "-ss-" + field, hop, func(cfg map[string]any) {
							inbound := protocolObjectByTag(t, cfg, "inbounds", f.tags[hop])
							if field == "method" {
								inbound[field] = "2022-blake3-chacha20-poly1305"
							} else {
								inbound[field] = singbox.DeriveSSKey("wrong-root-psk", protocol.method)
							}
						}})
					}
				}
			}
			cases = append(cases, corruption{"duplicate-outbound-tag", 0, func(cfg map[string]any) {
				out := protocolObjectByTag(t, cfg, "outbounds", subject.outboundTag())
				cfg["outbounds"] = append(cfg["outbounds"].([]any), out)
			}})
			for _, field := range []string{"detour", "tls", "transport", "server", "server_port"} {
				cases = append(cases, corruption{"outbound-" + field, 0, func(cfg map[string]any) {
					out := protocolObjectByTag(t, cfg, "outbounds", subject.outboundTag())
					switch field {
					case "tls":
						out[field] = map[string]any{"enabled": true, "insecure": true}
					case "transport":
						out[field] = map[string]any{"type": "ws", "path": "/wrong-egress"}
					case "server_port":
						out[field] = 6553
					default:
						out[field] = "unapproved-egress"
					}
				}})
			}
			for _, field := range protocol.authFields() {
				if field == "alterId" { // outbound VMess uses alter_id
					field = "alter_id"
				}
				cases = append(cases, corruption{"outbound-auth-" + field, 0, func(cfg map[string]any) {
					out := protocolObjectByTag(t, cfg, "outbounds", subject.outboundTag())
					if field == "alter_id" {
						out[field] = 7
					} else {
						out[field] = "wrong-managed-outbound-auth"
					}
				}})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					original := [][]byte{up, down}[tc.hop]
					cfg := protocolConfigObject(t, original)
					tc.edit(cfg)
					bad, err := json.Marshal(cfg)
					if err != nil {
						t.Fatal(err)
					}
					if err = f.st.RecordRelayConfigApplied(f.servers[tc.hop], bad); err != nil {
						t.Fatal(err)
					}
					for _, row := range protocolCurrentUsers(t, f) {
						if row.ID == subject.ID && row.State == "active" {
							t.Fatal("incorrect applied config retained active acknowledgement")
						}
					}
					if tc.hop == 1 {
						if _, err = f.config(t, 0); err == nil {
							t.Fatal("source trusted an incorrect target listener or credential")
						}
					}
					if err = f.st.RecordRelayConfigApplied(f.servers[tc.hop], original); err != nil {
						t.Fatal(err)
					}
					if err = f.st.RecordRelayConfigApplied(f.servers[0], up); err != nil {
						t.Fatal(err)
					}
					protocolAssertState(t, f, "active")
				})
			}
		})
	}
}

func TestRelayMeteringProtocolGenerationChangesRetainOwners(t *testing.T) {
	protocols := meteringProtocolCases()
	for index, initial := range protocols {
		// All nine starting protocols change to a different protocol or method;
		// the last two transitions explicitly cover AES-128 -> AES-256 and back.
		next := protocols[(index+1)%len(protocols)]
		if initial.name == "ss2022-aes256" {
			next = protocols[len(protocols)-2]
		}
		t.Run(initial.name+"_to_"+next.name, func(t *testing.T) {
			f := newProtocolMeteringFixture(t, initial, initial)
			oldLanding := f.apply(t, 1)
			oldSource := f.apply(t, 0)
			old := protocolAssertState(t, f, "active")
			target, err := f.st.GetSbInbound(f.inbounds[1])
			if err != nil {
				t.Fatal(err)
			}
			saveMeteringProtocolInbound(t, f.st, target, next)
			if err = f.st.PrepareRelayMetering(); err != nil {
				t.Fatal(err)
			}
			current := protocolAssertState(t, f, "prepared")
			for _, row := range current {
				if row.Generation != old[0].Generation+1 {
					t.Fatal("protocol/method change did not create the next generation")
				}
				for _, prior := range old {
					if row.IdentityName == prior.IdentityName || row.Credential == prior.Credential {
						t.Fatal("new target spec reused a historical identity or credential")
					}
				}
			}
			if err = f.st.RecordRelayConfigApplied(f.servers[1], oldLanding); err != nil {
				t.Fatal(err)
			}
			protocolAssertState(t, f, "prepared")
			if _, err = f.config(t, 0); err == nil {
				t.Fatal("old target apply endorsed the new generation")
			}
			f.apply(t, 1)
			if err = f.st.RecordRelayConfigApplied(f.servers[0], oldSource); err != nil {
				t.Fatal(err)
			}
			protocolAssertState(t, f, "accepted")
			f.apply(t, 0)
			protocolAssertState(t, f, "active")
			all, err := f.st.RelayMeteringUsers()
			if err != nil || len(all) != 4 {
				t.Fatalf("historical rows lost: %d %v", len(all), err)
			}
			for _, prior := range old {
				found := false
				for _, row := range all {
					if row.ID == prior.ID {
						found = true
						if row.UserID != prior.UserID || row.LinkID != prior.LinkID || row.Generation != prior.Generation || row.IdentityName != prior.IdentityName || row.Credential != prior.Credential {
							t.Fatal("old generation was rebound to a new owner/link/credential")
						}
					}
				}
				if !found {
					t.Fatal("historical identity row deleted")
				}
			}
			// Reopen the isolated store to ensure history is not an in-memory map.
			path := f.st.path
			if err = f.st.Close(); err != nil {
				t.Fatal(err)
			}
			f.st, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { f.st.Close() })
			f.st.SetSecretKey([]byte("protocol-logic-state-fixture"))
			if err = f.st.Migrate(); err != nil {
				t.Fatal(err)
			}
			if err = f.st.PrepareRelayMetering(); err != nil {
				t.Fatal(err)
			}
			protocolAssertState(t, f, "active")
			traffic := map[string]UsageDelta{}
			for i, row := range old {
				traffic[row.IdentityName] = UsageDelta{Up: int64(10 + i), Down: int64(100 + i)}
			}
			poll := NewTrafficPoll(f.servers[1], traffic)
			if _, err = f.st.RecordTrafficPoll(poll); err != nil {
				t.Fatal(err)
			}
			if _, err = f.st.RecordTrafficPoll(poll); err != nil {
				t.Fatal(err)
			}
			report, err := f.st.ServerServiceTraffic(f.servers[1], 0)
			if err != nil || report.Total != 222 || report.BillableTotal != 0 || len(report.Users) != 2 {
				t.Fatalf("late historical generation attribution/replay: total=%d billable=%d users=%d err=%v", report.Total, report.BillableTotal, len(report.Users), err)
			}
			for i, row := range old {
				found := false
				for _, user := range report.Users {
					if user.UserID == row.UserID {
						found = user.RelayTotal == int64(110+2*i) && user.BillableTotal == 0
					}
				}
				if !found || ledgerUsed(t, f.st, row.UserID) != 0 {
					t.Fatal("late old-generation bytes lost owner attribution or charged the package")
				}
			}
		})
	}
}

func TestRelayMeteringProtocolSourceCredentialChangeRequiresFreshApply(t *testing.T) {
	for _, protocol := range meteringProtocolCases() {
		t.Run(protocol.name, func(t *testing.T) {
			f := newProtocolMeteringFixture(t, protocol, protocol)
			f.apply(t, 1)
			oldSource := f.apply(t, 0)
			prior := protocolAssertState(t, f, "active")
			// Retain the same authenticated user names while changing the actual
			// source credential. A names-only proof must not endorse this apply.
			if _, err := f.st.db.Exec(`UPDATE users SET client_uuid=?,client_secret=? WHERE id=?`, "99999999-9999-4999-8999-999999999999", "synthetic-rotated-original-secret", f.owners[0]); err != nil {
				t.Fatal(err)
			}
			if err := f.st.PrepareRelayMetering(); err != nil {
				t.Fatal(err)
			}
			if err := f.st.RecordRelayConfigApplied(f.servers[0], oldSource); err != nil {
				t.Fatal(err)
			}
			for _, row := range protocolCurrentUsers(t, f) {
				if row.UserID == f.owners[0] && row.State == "active" {
					t.Fatal("old source authentication object endorsed a changed credential")
				}
			}
			f.apply(t, 0)
			after := protocolAssertState(t, f, "active")
			for i, row := range after {
				if row.IdentityName != prior[i].IdentityName || row.Credential != prior[i].Credential || row.Generation != prior[i].Generation {
					t.Fatal("original client rotation rekeyed stable relay identities")
				}
			}
			users, err := f.st.BuildUsersByTag(time.Now().Unix())
			if err != nil || len(users[f.tags[0]]) != 2 {
				t.Fatalf("client identities changed unexpectedly: %v", err)
			}
		})
	}
}

func TestRelayMeteringProtocolAuthMigrationUpgradeAndRetry(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("transaction-failure-%t", failFirst), func(t *testing.T) {
			protocol := meteringProtocolCase{name: "vless", protocol: "vless"}
			f := newProtocolMeteringFixture(t, protocol, protocol)
			f.apply(t, 1)
			source := f.apply(t, 0)
			before := protocolAssertState(t, f, "active")
			// Reconstruct the version-7 schema in this isolated fixture only. It
			// contains genuine prepared/applied identities, rather than fabricated
			// credentials that could not have passed the previous planner.
			const version = "000008_relay_protocol_auth_verification"
			if _, err := f.st.db.Exec(`DROP TABLE node_source_key_aliases;
 DROP TABLE relay_user_retirements;
 ALTER TABLE relay_credential_audit DROP COLUMN user_id;
 DROP INDEX idx_traffic_polls_server_state_time;
 DROP INDEX idx_traffic_observations_identity_positive;
 ALTER TABLE relay_metering_users DROP COLUMN source_auth_hashes;
 DELETE FROM schema_migrations WHERE version>=?`, version); err != nil {
				t.Fatal(err)
			}
			chain := f.st.migrations()
			if err := f.st.runMigrations(chain[:7]); err != nil {
				t.Fatalf("reconstructed fixture is not a valid version-7 database: %v", err)
			}
			if failFirst {
				if _, err := f.st.db.Exec(`CREATE TRIGGER reject_auth_migration BEFORE UPDATE OF state ON relay_metering_users WHEN NEW.state='accepted' BEGIN SELECT RAISE(ABORT,'synthetic auth migration failure'); END`); err != nil {
					t.Fatal(err)
				}
				if err := f.st.Migrate(); err == nil || !strings.Contains(err.Error(), version) {
					t.Fatalf("expected contextual migration failure: %v", err)
				}
				if n := scalar(t, f.st, `SELECT COUNT(*) FROM pragma_table_info('relay_metering_users') WHERE name='source_auth_hashes'`); n != 0 {
					t.Fatal("failed migration leaked its new column")
				}
				if n := scalar(t, f.st, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version); n != 0 {
					t.Fatal("failed migration committed its version marker")
				}
				if n := scalar(t, f.st, `SELECT COUNT(*) FROM relay_metering_users WHERE state='active'`); n != 2 {
					t.Fatal("failed migration partially downgraded existing readiness")
				}
				if _, err := f.st.db.Exec(`DROP TRIGGER reject_auth_migration`); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.st.Migrate(); err != nil {
				t.Fatal(err)
			}
			protocolAssertState(t, f, "accepted")
			if n := scalar(t, f.st, `SELECT COUNT(*) FROM relay_metering_users WHERE source_auth_hashes='{}' AND activated_at=0`); n != 2 {
				t.Fatal("upgrade invented source-auth proof for historical rows")
			}
			if err := f.st.RecordRelayConfigApplied(f.servers[0], source); err != nil {
				t.Fatal(err)
			}
			protocolAssertState(t, f, "accepted")
			if err := f.st.PrepareRelayMetering(); err != nil {
				t.Fatal(err)
			}
			if n := scalar(t, f.st, `SELECT COUNT(*) FROM relay_metering_users WHERE source_auth_hashes<>'{}'`); n != 2 {
				t.Fatal("planner failed to capture source authentication after upgrade")
			}
			for i, row := range protocolCurrentUsers(t, f) {
				if row.ID != before[i].ID || row.UserID != before[i].UserID || row.IdentityName != before[i].IdentityName || row.Credential != before[i].Credential || row.Generation != before[i].Generation {
					t.Fatal("migration/preparation changed a historical owner or relay credential")
				}
			}
			if err := f.st.RecordRelayConfigApplied(f.servers[0], source); err != nil {
				t.Fatal(err)
			}
			after := protocolAssertState(t, f, "active")
			if err := f.st.Migrate(); err != nil {
				t.Fatal(err)
			}
			if got := protocolAssertState(t, f, "active"); !reflect.DeepEqual(got, after) {
				t.Fatal("repeated migration changed an acknowledged identity")
			}
			if n := scalar(t, f.st, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version); n != 1 {
				t.Fatal("migration marker not recorded exactly once")
			}
		})
	}
}

func TestRelayMeteringProtocolMixedDifferentUsersMaySharePassword(t *testing.T) {
	f := newProtocolMeteringFixture(t,
		meteringProtocolCase{name: "mixed", protocol: "mixed"},
		meteringProtocolCase{name: "trojan", protocol: "trojan"})
	// Mixed authenticates on username+password. Equal passwords across distinct
	// usernames must not be mistaken for the UUID/key shadowing of other types.
	for _, table := range []string{"users", "user_plans", "plan_identities"} {
		if _, err := f.st.db.Exec(`UPDATE ` + table + ` SET proxy_password='synthetic-same-mixed-password'`); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.st.PrepareRelayMetering(); err != nil {
		t.Fatal(err)
	}
	f.apply(t, 1)
	raw := f.apply(t, 0)
	protocolAssertState(t, f, "active")
	cfg := protocolConfigObject(t, raw)
	inbound := protocolObjectByTag(t, cfg, "inbounds", f.tags[0])
	users := inbound["users"].([]any)
	if len(users) < 2 {
		t.Fatal("mixed fixture did not exercise distinct usernames")
	}
	names := map[string]bool{}
	for _, item := range users {
		user := item.(map[string]any)
		name, _ := user["username"].(string)
		if name == "" || names[name] || user["password"] != "synthetic-same-mixed-password" {
			t.Fatal("mixed fixture lacks unique usernames with the same password")
		}
		names[name] = true
	}
	duplicate := map[string]any{}
	for key, value := range users[0].(map[string]any) {
		duplicate[key] = value
	}
	duplicate["password"] = "synthetic-conflicting-password"
	inbound["users"] = append(users, duplicate)
	bad, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.RecordRelayConfigApplied(f.servers[0], bad); err != nil {
		t.Fatal(err)
	}
	protocolAssertState(t, f, "accepted")
	if err = f.st.RecordRelayConfigApplied(f.servers[0], raw); err != nil {
		t.Fatal(err)
	}
	protocolAssertState(t, f, "active")
}
