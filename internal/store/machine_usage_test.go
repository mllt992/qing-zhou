package store

import (
	"testing"
	"time"
)

func TestMachineUsageIsolationBoundariesAndRanking(t *testing.T) {
	st := newRefundStore(t)
	a := mkUser(t, st, "a")
	b := mkUser(t, st, "b")
	now := time.Now().Unix()
	for _, r := range [][5]int64{{1, a, now, 10, 20}, {1, b, now, 50, 10}, {2, a, now, 999, 999}, {1, a, now - 100, 500, 500}, {1, a, now + 1, 1000, 1000}} {
		if _, err := st.db.Exec(`INSERT INTO server_user_traffic_samples(server_id,user_id,ts,up,down) VALUES(?,?,?,?,?)`, r[0], r[1], r[2], r[3], r[4]); err != nil {
			t.Fatal(err)
		}
	}
	r, err := st.MachineUsage(1, now, now+1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Up != 60 || r.Down != 30 || r.UserCount != 2 || len(r.Users) != 2 || r.Users[0].UserID != b || len(r.Days) != 1 {
		t.Fatalf("bad scoped report %#v", r)
	}
	if r.CoverageStart != now || r.CoverageEnd != now {
		t.Fatal("coverage mismatch")
	}
	empty, err := st.MachineUsage(3, now, now+1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if empty.CoverageStart != 0 || len(empty.Users) != 0 || empty.Users == nil || empty.Days == nil {
		t.Fatal("empty coverage must stay explicit")
	}
	page, err := st.MachineUsage(1, now, now+1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Users) != 0 || page.UserCount != 2 || page.Up != 60 {
		t.Fatal("pagination changed totals")
	}
}
