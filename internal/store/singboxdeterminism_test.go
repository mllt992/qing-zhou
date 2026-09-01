package store

import (
	"testing"
	"time"

	"qingzhou/internal/singbox"
)

// TestBuildSingboxConfigIsByteStable is the invariant the whole "don't restart a
// node that already has this config" mechanism rests on.
//
// sbctl's periodic pass rebuilds the config every interval (a minute by default)
// and pushes it to every node; both the local and the SSH path suppress the push
// by comparing the bytes they just generated against what is already installed.
// If generation is not byte-stable — one map iterated without sorting is enough,
// and Go randomises that per run — the comparison never matches, and every pass
// rewrites an identical config and restarts sing-box under the live connections.
// Users see a disconnect every interval, with nothing in the logs but a
// successful deploy.
//
// The fixture is deliberately plural: several users, several buckets each, and
// inbounds of more than one type, because a single user cannot expose an
// ordering bug.
func TestBuildSingboxConfigIsByteStable(t *testing.T) {
	st := openMigrated(t)
	now := time.Now().Unix()

	plan, err := st.CreatePackage(Package{Type: "plan", Name: "月付", TrafficBytes: 1 << 30, DurationDays: 30, Stock: -1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := st.GetPackage(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob", "carol", "dave", "erin", "frank"} {
		uid, err := st.CreateUser(NewUser{Username: name, PasswordHash: "x"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AssignPackage(uid, pkg, 0, func(*User, bool) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	for _, ib := range []*SbInbound{
		{Type: "vless", Tag: "vless-in", Listen: "::", ListenPort: 443, Options: "{}", Enabled: true},
		{Type: "vmess", Tag: "vmess-in", Listen: "::", ListenPort: 8443, Options: "{}", Enabled: true},
		{Type: "mixed", Tag: "mixed-in", Listen: "::", ListenPort: 7890, Options: "{}", Enabled: true},
	} {
		if _, err := st.SaveSbInbound(ib); err != nil {
			t.Fatal(err)
		}
	}

	// Rebuild does exactly this pair, so the entitlement map is rebuilt too —
	// an ordering bug can hide in either half.
	build := func() string {
		byTag, err := st.BuildUsersByTag(now)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := st.BuildSingboxConfig(singbox.DefaultBaseConfig, "127.0.0.1:18080", byTag)
		if err != nil {
			t.Fatal(err)
		}
		return string(cfg)
	}

	// Enough passes that a randomised map order is overwhelmingly likely to show
	// up: with six users, two consecutive builds agreeing by luck is rare, and
	// thirty agreeing by luck is not a thing that happens.
	want := build()
	for i := 1; i < 30; i++ {
		if got := build(); got != want {
			t.Fatalf("config changed between identical rebuilds (pass %d); "+
				"every sync pass would restart sing-box on every node\n--- pass 0 ---\n%s\n--- pass %d ---\n%s",
				i, want, i, got)
		}
	}
}
