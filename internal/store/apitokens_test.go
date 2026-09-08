package store

import (
	"testing"
	"time"
)

func TestAPIToken_CreateListRevokeLookup(t *testing.T) {
	st := openMigrated(t)
	plain, tok, err := st.CreateAPIToken("bot", []string{"stats:read", "users:read"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if plain == "" || tok.ID == 0 || tok.Prefix == "" {
		t.Fatalf("bad create: plain=%q tok=%+v", plain, tok)
	}
	if got, err := st.LookupAPIToken(plain); err != nil || got == nil || got.ID != tok.ID {
		t.Fatalf("lookup want id=%d got=%v err=%v", tok.ID, got, err)
	}
	list, err := st.ListAPITokens()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}
	// ListAPITokens does not select token_hash; TokenHash stays empty in the view.
	if list[0].TokenHash != "" {
		t.Fatal("list view must not carry token_hash")
	}
	if err := st.RevokeAPIToken(tok.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := st.LookupAPIToken(plain); err != nil || got != nil {
		t.Fatalf("revoked token must not authenticate, got=%v err=%v", got, err)
	}
}

func TestAPIToken_ExpiryAndUnknownScope(t *testing.T) {
	st := openMigrated(t)
	if _, _, err := st.CreateAPIToken("x", []string{"admin:all"}, 1, 0); err == nil {
		t.Fatal("unknown scope must fail")
	}
	exp := time.Now().Add(-time.Hour).Unix()
	plain, _, err := st.CreateAPIToken("old", []string{"nodes:read"}, 1, exp)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.LookupAPIToken(plain); err != nil || got != nil {
		t.Fatalf("expired must not authenticate, got=%v err=%v", got, err)
	}
	if got, err := st.LookupAPIToken("qz_at_deadbeef"); err != nil || got != nil {
		t.Fatalf("unknown must be nil,nil got=%v err=%v", got, err)
	}
}
