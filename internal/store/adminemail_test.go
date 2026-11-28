package store

import (
	"testing"
	"time"
)

// An admin rebinding an address is asserting it, not asking the user to prove
// it — the user usually cannot receive mail at either address, which is the
// whole reason the admin is doing this. So it lands verified, like the address
// on an admin-created account.
func TestAdminSetUserEmail_LandsVerified(t *testing.T) {
	st := openMigrated(t)
	uid, err := st.CreateUser(NewUser{Username: "u1", Email: "typo@exmaple.com", PasswordHash: "h"})
	if err != nil {
		t.Fatal(err)
	}

	if err := st.AdminSetUserEmail(uid, "fixed@example.com"); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByID(uid)
	if !u.Email.Valid || u.Email.String != "fixed@example.com" {
		t.Fatalf("email = %v, want fixed@example.com", u.Email)
	}
	if !u.EmailVerified {
		t.Error("an admin-set address should not leave the user staring at a 未验证 prompt for an address they never chose")
	}
}

// Unbinding: NULL, and unverified — there is no address left to have verified.
func TestAdminSetUserEmail_EmptyUnbinds(t *testing.T) {
	st := openMigrated(t)
	uid, _ := st.CreateUser(NewUser{Username: "u1", Email: "a@example.com", PasswordHash: "h"})
	if err := st.SetEmailVerified(uid); err != nil {
		t.Fatal(err)
	}

	if err := st.AdminSetUserEmail(uid, ""); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByID(uid)
	if u.Email.Valid {
		t.Errorf("email = %q, want NULL after unbinding", u.Email.String)
	}
	if u.EmailVerified {
		t.Error("still email_verified with no address bound")
	}
}

// 管理员完成换绑后，寄往原地址的验证链接失效。
func TestAdminSetUserEmail_DropsOutstandingVerifyTokens(t *testing.T) {
	st := openMigrated(t)
	uid, _ := st.CreateUser(NewUser{Username: "u1", Email: "old@example.com", PasswordHash: "h"})
	if ok, err := st.IssueEmailToken(uid, "old@example.com", "tok-abc", "verify", time.Hour); err != nil || !ok {
		t.Fatal(err)
	}

	if err := st.AdminSetUserEmail(uid, "new@example.com"); err != nil {
		t.Fatal(err)
	}

	_, okTok, err := st.ConfirmEmail("tok-abc", "verify")
	if err != nil {
		t.Fatal(err)
	}
	if okTok {
		t.Error("a verify token minted for the old address still redeems after the admin rebound it")
	}
}
