package config

import "testing"

// The listen default decides whether a panel installed with no QZ_LISTEN is
// reachable at all. It is pinned here because it is a deliberate choice rather
// than an incidental value: loopback made "装好了却打不开" the standard first
// experience, and flipping it back would do so again silently.
//
// install.sh keeps the other half of the bargain — on upgrade it writes the old
// loopback value into a config that never set one, so this default only ever
// applies to a fresh install.
func TestListenDefaultIsReachable(t *testing.T) {
	t.Setenv("QZ_LISTEN", "")
	if got := Load().ListenAddr; got != "0.0.0.0:8081" {
		t.Fatalf("default listen = %q, want 0.0.0.0:8081", got)
	}
}

func TestListenEnvWins(t *testing.T) {
	t.Setenv("QZ_LISTEN", "127.0.0.1:9999")
	if got := Load().ListenAddr; got != "127.0.0.1:9999" {
		t.Fatalf("listen = %q, want the value from the environment", got)
	}
}

func TestConfiguredSecretKey(t *testing.T) {
	for _, key := range []string{LegacyComposeKey, "  " + LegacyComposeKey + " ", "   "} {
		t.Setenv("QZ_SECRET_KEY", key)
		if _, err := ConfiguredSecretKey(); err == nil {
			t.Fatal("accepted an unsafe configured key")
		}
	}
	for _, key := range []string{"", "independent-existing-key"} {
		t.Setenv("QZ_SECRET_KEY", key)
		if got, err := ConfiguredSecretKey(); err != nil || got != key {
			t.Fatal("unexpected change to existing configuration")
		}
	}
}
