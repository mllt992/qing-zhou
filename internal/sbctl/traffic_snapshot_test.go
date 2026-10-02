package sbctl

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"qingzhou/internal/sbstats"
	"qingzhou/internal/singbox"
	"qingzhou/internal/sshctl"
	"qingzhou/internal/store"
)

const epochOne = "11111111-1111-1111-1111-111111111111:11111111111111111111111111111111"
const epochTwo = "11111111-1111-1111-1111-111111111111:22222222222222222222222222222222"

type epochRemote struct {
	boundedRemote
	epochs []string
	at     int
}

func (r *epochRemote) RunCommand(context.Context, *sshctl.ServerConfig, string) (string, error) {
	if len(r.epochs) == 0 {
		return "", nil
	}
	i := r.at
	r.at++
	if i >= len(r.epochs) {
		i = len(r.epochs) - 1
	}
	return r.epochs[i], nil
}

type snapshotFixture struct {
	responses []int64
	resets    []bool
	fail      bool
}

func (f *snapshotFixture) QueryTraffic(_ context.Context, reset bool) (map[string]*sbstats.Traffic, error) {
	f.resets = append(f.resets, reset)
	if f.fail {
		return nil, errors.New("uncertain response")
	}
	v := f.responses[0]
	f.responses = f.responses[1:]
	return map[string]*sbstats.Traffic{"qz_snapshot": {Down: v}}, nil
}
func snapshotController(t *testing.T) (*Controller, *store.Store, int64, *store.Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err = st.Migrate(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(store.NewUser{Username: "snapshot", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.EnsurePoolBucket(uid, "qz_snapshot", "uuid", "test-secret"); err != nil {
		t.Fatal(err)
	}
	if err = st.SetSetting(cumulativeMeteringSetting, "true"); err != nil {
		t.Fatal(err)
	}
	c := New(st, &countingApplier{}, nil, singbox.DefaultBaseConfig, "")
	c.remoteMgr = &epochRemote{epochs: []string{epochOne}}
	return c, st, uid, &store.Server{ID: 7, SystemdUnit: "sing-box"}
}
func snapshotUsed(t *testing.T, st *store.Store, uid int64) int64 {
	t.Helper()
	var n int64
	if err := st.DB().QueryRow(`SELECT used_down FROM users WHERE id=?`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestSnapshotControlledBoundaryAndRestart(t *testing.T) {
	c, st, uid, sv := snapshotController(t)
	f := &snapshotFixture{responses: []int64{100, 5, 15, 7}}
	for i := 0; i < 2; i++ {
		if _, err := c.collectTrafficSnapshot(context.Background(), sv.ID, sv, f); err != nil {
			t.Fatal(err)
		}
	}
	if n := snapshotUsed(t, st, uid); n != 115 {
		t.Fatalf("boundary charged %d", n)
	}
	c.remoteMgr = &epochRemote{epochs: []string{epochTwo}}
	if _, err := c.collectTrafficSnapshot(context.Background(), sv.ID, sv, f); err != nil {
		t.Fatal(err)
	}
	if n := snapshotUsed(t, st, uid); n != 122 {
		t.Fatalf("restart charged %d", n)
	}
	if len(f.resets) != 4 || !f.resets[0] || f.resets[1] || f.resets[2] || f.resets[3] {
		t.Fatalf("destructive reads after handover %+v", f.resets)
	}
	q, _, err := st.TrafficCollectionState(sv.ID)
	if err != nil || q.Mode != "cumulative" {
		t.Fatalf("mode %+v %v", q, err)
	}
	if !c.cumulativeNodeRequiresCollector(sv.ID) {
		t.Fatal("old reader could resume after opt-out")
	}
}
func TestSnapshotDoesNotResetWhenActiveEpochUnavailable(t *testing.T) {
	c, st, _, sv := snapshotController(t)
	f := &snapshotFixture{responses: []int64{1, 0}}
	if _, err := c.collectTrafficSnapshot(context.Background(), sv.ID, sv, f); err != nil {
		t.Fatal(err)
	}
	c.remoteMgr = &epochRemote{}
	if _, err := c.collectTrafficSnapshot(context.Background(), sv.ID, sv, f); err == nil {
		t.Fatal("missing epoch accepted")
	}
	if len(f.resets) != 2 {
		t.Fatal("queried/reset unverifiable active counter")
	}
	q, _, _ := st.TrafficCollectionState(sv.ID)
	if q.Mode != "cumulative" {
		t.Fatal("collector downgraded")
	}
}
func TestSnapshotUnsupportedNodeKeepsLegacyMetering(t *testing.T) {
	c, st, uid, sv := snapshotController(t)
	c.remoteMgr = &epochRemote{}
	f := &snapshotFixture{responses: []int64{50}}
	if _, err := c.collectTrafficSnapshot(context.Background(), sv.ID, sv, f); err != nil {
		t.Fatal(err)
	}
	if snapshotUsed(t, st, uid) != 50 {
		t.Fatal("unsupported node stopped metering")
	}
	q, _, _ := st.TrafficCollectionState(sv.ID)
	if q.Mode != "reset" || q.Status != "legacy" {
		t.Fatalf("quality %+v", q)
	}
}
func TestSnapshotCollectorLeaseAndUncertainTransition(t *testing.T) {
	c, st, _, sv := snapshotController(t)
	f := &snapshotFixture{fail: true}
	acquired, err := st.AcquireTrafficLease(sv.ID, "other-process")
	if err != nil || !acquired {
		t.Fatal(err)
	}
	if _, err = c.collectTrafficSnapshot(context.Background(), sv.ID, sv, f); err == nil {
		t.Fatal("collector ignored lease")
	}
	if len(f.resets) != 0 {
		t.Fatal("foreign lease still reset counters")
	}
	st.ReleaseTrafficLease(sv.ID, "other-process")
	if _, err = c.collectTrafficSnapshot(context.Background(), sv.ID, sv, f); err == nil {
		t.Fatal("uncertain reset accepted")
	}
	r, err := st.ServerServiceTraffic(sv.ID, 0)
	if err != nil || r.Quality.Gaps != 1 {
		t.Fatalf("uncertain boundary disappeared %+v %v", r, err)
	}
}

func TestSnapshotPendingFinalResetCannotRepeatDestructiveRead(t *testing.T) {
	c, st, uid, sv := snapshotController(t)
	if _, err := st.DB().Exec(`CREATE TRIGGER fail_boundary_usage BEFORE UPDATE OF used_up ON user_plans BEGIN SELECT RAISE(FAIL,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	f := &snapshotFixture{responses: []int64{100, 5, 15}}
	if _, err := c.collectTrafficSnapshot(context.Background(), sv.ID, sv, f); err == nil {
		t.Fatal("expected final reset processing failure")
	}
	q, _, err := st.TrafficCollectionState(sv.ID)
	if err != nil || q.Mode != "cumulative" || q.Status != "pending" {
		t.Fatalf("durable boundary missing %+v %v", q, err)
	}
	if _, err := st.DB().Exec(`DROP TRIGGER fail_boundary_usage`); err != nil {
		t.Fatal(err)
	}
	// A later collector can run before pending quota writes recover. It must use
	// cumulative mode and eventually retain both sides of the boundary exactly once.
	if _, err := c.collectTrafficSnapshot(context.Background(), sv.ID, sv, f); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RetryPendingTrafficPolls(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.collectTrafficSnapshot(context.Background(), sv.ID, sv, f); err != nil {
		t.Fatal(err)
	}
	if snapshotUsed(t, st, uid) != 115 {
		t.Fatalf("boundary usage = %d", snapshotUsed(t, st, uid))
	}
	if len(f.resets) != 3 || !f.resets[0] || f.resets[1] || f.resets[2] {
		t.Fatalf("boundary reset repeated %+v", f.resets)
	}
	q, epoch, err := st.TrafficCollectionState(sv.ID)
	if err != nil || q.Mode != "cumulative" || epoch != epochOne {
		t.Fatalf("pending reset downgraded cursor %+v %s %v", q, epoch, err)
	}
}

type gatedLegacySnapshot struct {
	entered chan struct{}
	release chan struct{}
}

func (f *gatedLegacySnapshot) QueryTraffic(ctx context.Context, reset bool) (map[string]*sbstats.Traffic, error) {
	if !reset {
		return nil, errors.New("expected initial legacy reset")
	}
	close(f.entered)
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return map[string]*sbstats.Traffic{"qz_snapshot": {Down: 100}}, nil
}
func TestSnapshotLegacyReaderSharesHandoverLease(t *testing.T) {
	c, st, uid, sv := snapshotController(t)
	if err := st.SetSetting(cumulativeMeteringSetting, "false"); err != nil {
		t.Fatal(err)
	}
	first := &gatedLegacySnapshot{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := c.collectTrafficSnapshot(context.Background(), sv.ID, sv, first); done <- err }()
	<-first.entered
	if err := st.SetSetting(cumulativeMeteringSetting, "true"); err != nil {
		t.Fatal(err)
	}
	// A second panel/controller must not cross the pending old reader.
	other := New(st, &countingApplier{}, nil, singbox.DefaultBaseConfig, "")
	other.remoteMgr = &epochRemote{epochs: []string{epochOne}}
	next := &snapshotFixture{responses: []int64{10, 5, 15}}
	if _, err := other.collectTrafficSnapshot(context.Background(), sv.ID, sv, next); err == nil {
		t.Fatal("handover crossed active legacy reset")
	}
	if len(next.resets) != 0 {
		t.Fatal("second panel touched counters despite lease")
	}
	close(first.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := other.collectTrafficSnapshot(context.Background(), sv.ID, sv, next); err != nil {
		t.Fatal(err)
	}
	if snapshotUsed(t, st, uid) != 115 {
		t.Fatalf("transition used %d", snapshotUsed(t, st, uid))
	}
	// Even a stale disabled switch cannot choose a reset reader once the mode is durable.
	if err := st.SetSetting(cumulativeMeteringSetting, "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.collectTrafficSnapshot(context.Background(), sv.ID, sv, next); err != nil {
		t.Fatal(err)
	}
	if snapshotUsed(t, st, uid) != 125 {
		t.Fatalf("stale setting charged %d", snapshotUsed(t, st, uid))
	}
	if len(next.resets) != 3 || !next.resets[0] || next.resets[1] || next.resets[2] {
		t.Fatalf("late reset after handover %+v", next.resets)
	}
}
