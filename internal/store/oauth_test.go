package store

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOAuthStateAtomicConsumption(t *testing.T) {
	st := openMigrated(t)
	st.SetSecretKey([]byte("test-key"))
	ctx := context.Background()
	v := OAuthState{StateHash: "state", BrowserHash: "browser", Nonce: "nonce", Verifier: "secret-verifier", ConfigHash: "config", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	if err := st.CreateOAuthState(ctx, v); err != nil {
		t.Fatal(err)
	}
	var encrypted string
	st.db.QueryRow(`SELECT verifier FROM oauth_states`).Scan(&encrypted)
	if !strings.HasPrefix(encrypted, encPrefix) {
		t.Fatal("verifier not encrypted")
	}
	if _, err := st.ConsumeOAuthState(ctx, "state", "wrong-browser"); err == nil {
		t.Fatal("browser mismatch accepted")
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := st.ConsumeOAuthState(ctx, "state", "browser")
			if err == nil {
				if got.Verifier != "secret-verifier" {
					t.Error("bad verifier")
				}
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("consumed %d times", success.Load())
	}
	v.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	st.CreateOAuthState(ctx, v)
	if _, err := st.ConsumeOAuthState(ctx, "state", "browser"); err == nil {
		t.Fatal("expired state accepted")
	}
}

func TestOAuthIdentityTransactionAndDeletion(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	nu := &NewUser{Username: "oauth_alice", Email: "alice@example.com", PasswordHash: "!oauth2", SubToken: "sub"}
	st.db.Exec(`CREATE TRIGGER fail_oauth BEFORE INSERT ON oauth_identities BEGIN SELECT RAISE(ABORT,'injected'); END`)
	if _, _, err := st.OAuthLoginUser(ctx, "https://issuer", "alice", nu, true, "uuid", "secret"); err == nil {
		t.Fatal("expected failure")
	}
	user, err := st.UserByUsername(nu.Username)
	if err != nil || user != nil {
		t.Fatal("orphan account after transaction failure")
	}
	st.db.Exec(`DROP TRIGGER fail_oauth`)
	id, needs, err := st.OAuthLoginUser(ctx, "https://issuer", "alice", nu, true, "uuid", "secret")
	if err != nil || !needs {
		t.Fatalf("create: %v", err)
	}
	again, needs, err := st.OAuthLoginUser(ctx, "https://issuer", "alice", nil, false, "changed", "changed")
	if err != nil || again != id || !needs {
		t.Fatal("incomplete provisioning could not resume")
	}
	if err = st.MarkOAuthProvisioned(ctx, "https://issuer", "alice"); err != nil {
		t.Fatal(err)
	}
	_, needs, err = st.OAuthLoginUser(ctx, "https://issuer", "alice", nil, false, "changed", "changed")
	if err != nil || needs {
		t.Fatal("provisioning was repeated")
	}
	if err = st.BindOAuthIdentity(ctx, id, "https://issuer", "different"); err == nil {
		t.Fatal("overwrote binding")
	}
	st.db.Exec(`DELETE FROM users WHERE id=?`, id)
	var count int
	st.db.QueryRow(`SELECT COUNT(*) FROM oauth_identities`).Scan(&count)
	if count != 0 {
		t.Fatal("deleted user retained binding")
	}
}
