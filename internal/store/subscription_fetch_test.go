package store

import (
	"sync"
	"testing"
)

func TestRecordSubscriptionFetchThrottlesAndKeepsAccountTimestamp(t *testing.T) {
	st := openMigrated(t)
	uid, err := st.CreateUser(NewUser{Username: "fetch-user", PasswordHash: "x", SubToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE users SET updated_at=1234 WHERE id=?`, uid); err != nil {
		t.Fatal(err)
	}
	before, _ := st.UserByID(uid)

	wrote, err := st.RecordSubscriptionFetch(uid, 10_000, "clash", "mihomo")
	if err != nil || !wrote {
		t.Fatalf("first record = %v, %v; want write", wrote, err)
	}
	u, _ := st.UserByID(uid)
	if u.SubLastFetchedAt != 10_000 || u.SubLastFormat != "clash" || u.SubLastClient != "mihomo" {
		t.Fatalf("stored telemetry = %d/%q/%q", u.SubLastFetchedAt, u.SubLastFormat, u.SubLastClient)
	}
	if u.UpdatedAt != before.UpdatedAt {
		t.Fatalf("observational fetch changed users.updated_at: %d -> %d", before.UpdatedAt, u.UpdatedAt)
	}

	wrote, err = st.RecordSubscriptionFetch(uid, 10_100, "singbox", "sing-box")
	if err != nil || wrote {
		t.Fatalf("within-window record = %v, %v; want throttled", wrote, err)
	}
	u, _ = st.UserByID(uid)
	if u.SubLastFetchedAt != 10_000 || u.SubLastFormat != "clash" {
		t.Fatalf("throttled record changed row: %+v", u)
	}

	wrote, err = st.RecordSubscriptionFetch(uid, 13_600, "singbox", "sing-box")
	if err != nil || !wrote {
		t.Fatalf("next-window record = %v, %v; want write", wrote, err)
	}
}

func TestMigrateAddsSubscriptionFetchColumnsToOldUsersTable(t *testing.T) {
	st := openMigrated(t)
	for _, column := range []string{"sub_last_fetched_at", "sub_last_format", "sub_last_client"} {
		if _, err := st.db.Exec(`ALTER TABLE users DROP COLUMN ` + column); err != nil {
			t.Fatalf("drop %s: %v", column, err)
		}
	}
	rewindVersionedBaseline(t, st)
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(NewUser{Username: "old-user", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.UserByID(uid)
	if err != nil || u.SubLastFetchedAt != 0 || u.SubLastFormat != "" || u.SubLastClient != "" {
		t.Fatalf("migrated defaults: user=%+v err=%v", u, err)
	}
}

func TestRecordSubscriptionFetchConcurrentRefreshesWriteOnce(t *testing.T) {
	st := openMigrated(t)
	uid, err := st.CreateUser(NewUser{Username: "fetch-race", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 12
	start := make(chan struct{})
	results := make(chan bool, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			wrote, err := st.RecordSubscriptionFetch(uid, 20_000, "base64", "unknown")
			results <- wrote
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	writes := 0
	for wrote := range results {
		if wrote {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("concurrent writes=%d want 1", writes)
	}
}
