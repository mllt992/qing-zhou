package store

import (
	"testing"
	"time"
)

func TestEmailChangeRecoveryHandoff(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "rita")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.AdminSetUserEmail(uid, "old@example.test"))
	must(st.CreateSession(uid, "current", "127.0.0.1", "test"))
	u, err := st.UserByID(uid)
	must(err)
	issue := func(address, code string, want bool) {
		t.Helper()
		issued, err := st.IssueEmailToken(uid, address, code, "reset", time.Hour)
		must(err)
		if issued != want {
			t.Fatalf("issue to %s = %t, want %t", address, issued, want)
		}
	}
	must(st.RequestEmailChange(u, "current", "first@example.test", "first"))
	must(st.RequestEmailChange(u, "current", "new@example.test", "second"))
	issue("new@example.test", "premature", false)
	issue("old@example.test", "old-reset", true)
	if _, ok, err := st.ConfirmEmail("first", "rebind"); err != nil || ok {
		t.Fatalf("superseded link %t %v", ok, err)
	}
	if _, ok, err := st.ConfirmEmail("second", "rebind"); err != nil || !ok {
		t.Fatalf("confirmation %t %v", ok, err)
	}
	if !st.EmailAlreadyConfirmed("second") {
		t.Fatal("confirmation was not idempotent")
	}
	if ok, err := st.ResetPassword("old-reset", "changed"); err != nil || ok {
		t.Fatalf("old link %t %v", ok, err)
	}
	issue("old@example.test", "late-old", false)
	issue("new@example.test", "new-reset", true)
	if ok, err := st.ResetPassword("new-reset", "changed"); err != nil || !ok {
		t.Fatalf("new link %t %v", ok, err)
	}
	if st.SessionValid("current") {
		t.Fatal("reset left the session active")
	}
}

func TestAdminRebindCancelsPendingLinks(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "user")
	if err := st.AdminSetUserEmail(uid, "before@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(uid, "jti", "127.0.0.1", "ua"); err != nil {
		t.Fatal(err)
	}
	u, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RequestEmailChange(u, "jti", "pending@example.test", "pending"); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.IssueEmailToken(uid, u.Email.String, "reset", "reset", time.Hour); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := st.AdminSetUserEmail(uid, "admin@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ConfirmEmail("pending", "rebind"); err != nil || ok {
		t.Fatal(ok, err)
	}
	if ok, err := st.ResetTokenValid("reset"); err != nil || ok {
		t.Fatal(ok, err)
	}
}

// Existing provisioned accounts must gain a free bucket on upgrade — the pool no
// longer carries the free group, so without the backfill they lose free-node
// access. Accounts that were never provisioned must be left alone: inventing an
// identity for one would insert a user into the sing-box config who was never
// meant to be there.
func TestBackfillFreeBuckets(t *testing.T) {
	st := newRefundStore(t)
	provisioned := mkUser(t, st, "olduser")
	if err := st.EnsurePoolBucket(provisioned, "qz_olduser", "uuid", "secret"); err != nil {
		t.Fatal(err)
	}
	unprovisioned := mkUser(t, st, "pending")

	if err := st.backfillFreeBuckets(); err != nil {
		t.Fatal(err)
	}
	if bucketOfKind(t, st, provisioned, KindFree) == nil {
		t.Error("provisioned account did not get a free bucket — it just lost free-node access")
	}
	if bucketOfKind(t, st, unprovisioned, KindFree) != nil {
		t.Error("unprovisioned account should not be given an identity by the backfill")
	}

	// Idempotent: it runs on every start.
	if err := st.backfillFreeBuckets(); err != nil {
		t.Fatal(err)
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM user_plans WHERE user_id=? AND kind=?`, provisioned, KindFree).Scan(&n)
	if n != 1 {
		t.Errorf("got %d free buckets after two runs, want 1", n)
	}
}

func TestEmailChangeAddressClaimAtConfirmation(t *testing.T) {
	st := newRefundStore(t)
	for _, name := range []string{"rita", "kai"} {
		uid := mkUser(t, st, name)
		if err := st.CreateSession(uid, name, "local", name); err != nil {
			t.Fatal(err)
		}
		u, err := st.UserByID(uid)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.RequestEmailChange(u, name, "shared@example.test", name); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := st.ConfirmEmail("kai", "rebind"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, ok, err := st.ConfirmEmail("rita", "rebind"); err != nil || ok {
		t.Fatal(ok, err)
	}
}
