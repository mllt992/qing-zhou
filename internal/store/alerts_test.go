package store

import (
	"testing"
	"time"
)

func unreadOf(t *testing.T, st *Store, typ string) *ServerAlert {
	t.Helper()
	all, err := st.ListAlerts(true)
	if err != nil {
		t.Fatal(err)
	}
	var found *ServerAlert
	for _, a := range all {
		if a.Type != typ {
			continue
		}
		if found != nil {
			t.Fatalf("type %q has more than one unread alert", typ)
		}
		found = a
	}
	return found
}

// An ongoing condition must stay ONE line in the panel: every re-observation
// merges into the open episode instead of adding a row. The old behavior wrote a
// fresh row per hourly check, so one server offline for a week produced ~170
// identical unread alerts.
func TestInsertAlertMergesOngoingEpisode(t *testing.T) {
	st := newRefundStore(t)
	start := time.Now().Add(-5 * time.Hour).Unix()

	for i := 0; i < 5; i++ {
		if _, err := st.InsertAlert(ServerAlert{ServerID: 1, Type: "offline", Message: "离线", Ts: start + int64(i)*3600}); err != nil {
			t.Fatal(err)
		}
	}
	a := unreadOf(t, st, "offline")
	if a == nil {
		t.Fatal("no unread offline alert")
	}
	if a.Hits != 5 {
		t.Errorf("hits = %d, want 5", a.Hits)
	}
	if a.FirstTs != start {
		t.Errorf("first_ts = %d, want %d (episode start)", a.FirstTs, start)
	}
	if a.Ts != start+4*3600 {
		t.Errorf("ts = %d, want %d (last observation)", a.Ts, start+4*3600)
	}
	if n, _ := st.UnreadAlertCount(); n != 1 {
		t.Errorf("unread count = %d, want 1", n)
	}
}

// Once the condition clears, its alert leaves the panel by itself — the admin
// shouldn't have to dismiss alerts for servers that already recovered.
func TestResolveAlertClearsAndReopens(t *testing.T) {
	st := newRefundStore(t)
	now := time.Now().Unix()

	if _, err := st.InsertAlert(ServerAlert{ServerID: 1, Type: "offline", Message: "离线", Ts: now - 7200}); err != nil {
		t.Fatal(err)
	}
	if err := st.ResolveAlert(1, "offline"); err != nil {
		t.Fatal(err)
	}
	if a := unreadOf(t, st, "offline"); a != nil {
		t.Fatal("resolved alert is still unread")
	}

	// A new outage right after recovery is a new episode: it must alert at once,
	// not be swallowed by the acknowledge cooldown.
	if _, err := st.InsertAlert(ServerAlert{ServerID: 1, Type: "offline", Message: "又离线", Ts: now}); err != nil {
		t.Fatal(err)
	}
	a := unreadOf(t, st, "offline")
	if a == nil {
		t.Fatal("re-occurrence did not raise a new alert")
	}
	if a.Hits != 1 || a.FirstTs != now {
		t.Errorf("got hits=%d first_ts=%d, want a fresh episode (1, %d)", a.Hits, a.FirstTs, now)
	}
}

// Dismissing an alert whose condition never clears must buy a full day of quiet,
// then remind once — not re-appear on the next hourly check.
func TestDismissedAlertRemindsOncePerDay(t *testing.T) {
	st := newRefundStore(t)
	now := time.Now().Unix()

	if _, err := st.InsertAlert(ServerAlert{ServerID: 1, Type: "expired", Message: "已过期", Ts: now}); err != nil {
		t.Fatal(err)
	}
	a := unreadOf(t, st, "expired")
	if a == nil {
		t.Fatal("no alert to dismiss")
	}
	if err := st.MarkAlertRead(a.ID); err != nil {
		t.Fatal(err)
	}

	// Still expired an hour later: stay quiet.
	if _, err := st.InsertAlert(ServerAlert{ServerID: 1, Type: "expired", Message: "已过期", Ts: now + 3600}); err != nil {
		t.Fatal(err)
	}
	if got := unreadOf(t, st, "expired"); got != nil {
		t.Error("dismissed alert came back within the quiet window")
	}

	// A day later: one reminder.
	if _, err := st.InsertAlert(ServerAlert{ServerID: 1, Type: "expired", Message: "已过期", Ts: now + 25*3600}); err != nil {
		t.Fatal(err)
	}
	if got := unreadOf(t, st, "expired"); got == nil {
		t.Error("no reminder after the quiet window elapsed")
	}
}

// Distinct conditions and distinct servers stay distinct lines — merging is per
// (server, type), not global.
func TestInsertAlertMergesPerServerAndType(t *testing.T) {
	st := newRefundStore(t)
	now := time.Now().Unix()
	for _, a := range []ServerAlert{
		{ServerID: 1, Type: "offline", Message: "a", Ts: now},
		{ServerID: 1, Type: "expired", Message: "b", Ts: now},
		{ServerID: 2, Type: "offline", Message: "c", Ts: now},
		{ServerID: 2, Type: "offline", Message: "c", Ts: now + 60},
	} {
		if _, err := st.InsertAlert(a); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := st.UnreadAlertCount(); n != 3 {
		t.Errorf("unread count = %d, want 3", n)
	}
}

func TestMarkAllAlertsRead(t *testing.T) {
	st := newRefundStore(t)
	now := time.Now().Unix()
	for i := int64(1); i <= 4; i++ {
		if _, err := st.InsertAlert(ServerAlert{ServerID: i, Type: "offline", Message: "x", Ts: now}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.MarkAllAlertsRead()
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("marked %d, want 4", n)
	}
	if c, _ := st.UnreadAlertCount(); c != 0 {
		t.Errorf("unread count = %d, want 0", c)
	}
}

func TestRestartCircuitLatchSurvivesAcknowledgementUntilResolved(t *testing.T) {
	st := newRefundStore(t)
	created, err := st.InsertAlert(ServerAlert{ServerID: 7, Type: AlertRestartLoop, Message: "已熔断"})
	if err != nil || !created {
		t.Fatalf("insert circuit alert: created=%v err=%v", created, err)
	}
	open, err := st.IsAlertOpen(7, AlertRestartLoop)
	if err != nil || !open {
		t.Fatalf("new circuit is not open: open=%v err=%v", open, err)
	}
	a := unreadOf(t, st, AlertRestartLoop)
	if a == nil {
		t.Fatal("missing circuit alert")
	}
	if err := st.MarkAlertRead(a.ID); err != nil {
		t.Fatal(err)
	}
	if open, _ := st.IsAlertOpen(7, AlertRestartLoop); !open {
		t.Fatal("acknowledging the Telegram/panel alert silently closed the circuit")
	}
	if err := st.ResolveAlert(7, AlertRestartLoop); err != nil {
		t.Fatal(err)
	}
	if open, _ := st.IsAlertOpen(7, AlertRestartLoop); open {
		t.Fatal("successful recovery did not close the circuit")
	}
}

// Upgrading a panel that already accumulated one row per observation must fold
// the backlog: newest row per (server, type) survives, carrying the group's
// start time and observation count.
func TestMigrateCollapsesLegacyAlertBacklog(t *testing.T) {
	st := newRefundStore(t)
	// Legacy rows: first_ts=0 is what the pre-episode schema left behind.
	for i := 0; i < 6; i++ {
		if _, err := st.db.Exec(`INSERT INTO server_alerts (server_id, type, message, ts, first_ts, hits, read, resolved)
			VALUES (1,'offline','离线',?,0,1,0,0)`, 1000+int64(i)*3600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.db.Exec(`INSERT INTO server_alerts (server_id, type, message, ts, first_ts, hits, read, resolved)
		VALUES (2,'expired','已过期',5000,0,1,0,0)`); err != nil {
		t.Fatal(err)
	}

	rewindVersionedBaseline(t, st)
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}

	if n, _ := st.UnreadAlertCount(); n != 2 {
		t.Fatalf("unread count = %d, want 2 (one per server+type)", n)
	}
	a := unreadOf(t, st, "offline")
	if a == nil {
		t.Fatal("offline episode vanished")
	}
	if a.Ts != 1000+5*3600 {
		t.Errorf("survivor ts = %d, want the newest row %d", a.Ts, 1000+5*3600)
	}
	if a.FirstTs != 1000 {
		t.Errorf("first_ts = %d, want 1000 (oldest row in the group)", a.FirstTs)
	}
	if a.Hits != 6 {
		t.Errorf("hits = %d, want 6 (folded rows)", a.Hits)
	}
}
