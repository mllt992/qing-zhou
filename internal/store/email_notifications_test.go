package store

import "testing"

func TestNotifyChannelsPreserveLegacyClaims(t *testing.T) {
	st := newRefundStore(t)
	uid := mkUser(t, st, "channels")
	first, err := st.ClaimNotify(uid, "expiry_soon", "b1")
	if err != nil || !first {
		t.Fatal(err)
	}
	tg, err := st.ClaimNotifyChannel(uid, "telegram", "expiry_soon", "b1")
	if err != nil || tg {
		t.Fatal("legacy TG claim lost")
	}
	mail, err := st.ClaimNotifyChannel(uid, "email", "expiry_soon", "b1")
	if err != nil || !mail {
		t.Fatal("mail blocked by TG claim")
	}
	st.ClearNotifyChannel(uid, "email", "expiry_soon", "b1")
	tg, _ = st.ClaimNotifyChannel(uid, "telegram", "expiry_soon", "b1")
	if tg {
		t.Fatal("email recovery cleared TG claim")
	}
}
