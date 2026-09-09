package sbctl

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"qingzhou/internal/sshctl"
	"qingzhou/internal/store"
)

type fleetStore struct {
	schedFakeStore
	servers []*store.Server
}

func (s fleetStore) ListServers() ([]*store.Server, error)     { return s.servers, nil }
func (s fleetStore) GetServer(id int64) (*store.Server, error) { return s.servers[id-1], nil }

type boundedRemote struct {
	active, peak, applies, tunnels, probes atomic.Int32
	entered                                chan struct{}
	gate                                   chan struct{}
}

func (r *boundedRemote) wait(ctx context.Context) error {
	n := r.active.Add(1)
	defer r.active.Add(-1)
	for old := r.peak.Load(); n > old; old = r.peak.Load() {
		if r.peak.CompareAndSwap(old, n) {
			break
		}
	}
	r.entered <- struct{}{}
	select {
	case <-r.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *boundedRemote) ApplyConfig(ctx context.Context, _ *sshctl.ServerConfig, _ []byte) (bool, error) {
	r.applies.Add(1)
	return false, r.wait(ctx)
}
func (r *boundedRemote) DialTunnel(ctx context.Context, _ *sshctl.ServerConfig, _ string) (net.Conn, error) {
	r.tunnels.Add(1)
	if err := r.wait(ctx); err != nil {
		return nil, err
	}
	return nil, errors.New("fixture tunnel unavailable")
}
func (r *boundedRemote) SupportsStatsAPI(ctx context.Context, _ *sshctl.ServerConfig) (bool, string, error) {
	r.probes.Add(1)
	err := r.wait(ctx)
	return err == nil, "1.14.0", err
}
func (*boundedRemote) ForgetSingBoxBin(int64) {}
func (r *boundedRemote) RunCommand(ctx context.Context, _ *sshctl.ServerConfig, _ string) (string, error) {
	return "", r.wait(ctx)
}

func networkController(n int) (*Controller, *boundedRemote) {
	st := fleetStore{}
	for i := 1; i <= n; i++ {
		st.servers = append(st.servers, &store.Server{ID: int64(i), Host: "192.0.2.1", Enabled: true, V2rayListen: "127.0.0.1:18080"})
	}
	c := New(st, &countingApplier{}, nil, "{}", "")
	rm := &boundedRemote{entered: make(chan struct{}, n*4), gate: make(chan struct{})}
	c.remoteMgr = rm
	for _, sv := range st.servers {
		c.statsCap[sv.ID] = statsProbe{ok: true}
	}
	return c, rm
}
func waitRemoteEntries(t *testing.T, rm *boundedRemote, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-rm.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("SSH workers did not start")
		}
	}
}

func TestRebuildAndRemoteStatsShareSSHLimit(t *testing.T) {
	c, rm := networkController(40)
	rebuild := make(chan error, 1)
	go func() { rebuild <- c.Rebuild() }()
	waitRemoteEntries(t, rm, remoteConcurrency)
	stats := make(chan []remoteResult, 1)
	go func() { stats <- c.remoteStats(context.Background()) }()
	// The first batch holds all slots; another operation must not open more SSH.
	select {
	case <-rm.entered:
		t.Error("stats bypassed the shared SSH budget")
	case <-time.After(50 * time.Millisecond):
	}
	close(rm.gate)
	select {
	case err := <-rebuild:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rebuild did not drain")
	}
	select {
	case results := <-stats:
		if len(results) != 40 {
			t.Fatalf("results=%d", len(results))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stats did not drain")
	}
	if rm.peak.Load() > remoteConcurrency || rm.active.Load() != 0 || rm.applies.Load() != 40 || rm.tunnels.Load() != 40 || len(c.remoteSlots) != 0 {
		t.Fatalf("peak=%d active=%d applies=%d tunnels=%d slots=%d", rm.peak.Load(), rm.active.Load(), rm.applies.Load(), rm.tunnels.Load(), len(c.remoteSlots))
	}
}

func TestRemoteStatsCancelsActiveAndQueuedWork(t *testing.T) {
	c, rm := networkController(100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan []remoteResult, 1)
	go func() { done <- c.remoteStats(ctx) }()
	waitRemoteEntries(t, rm, remoteConcurrency)
	cancel()
	select {
	case results := <-done:
		if len(results) == 0 {
			t.Fatal("cancellation reported success")
		}
		for _, r := range results {
			if r.err == nil {
				t.Fatal("unexpected successful fixture poll")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled stats retained SSH work")
	}
	if rm.tunnels.Load() != remoteConcurrency || rm.active.Load() != 0 || len(c.remoteSlots) != 0 {
		t.Fatalf("tunnels=%d active=%d slots=%d", rm.tunnels.Load(), rm.active.Load(), len(c.remoteSlots))
	}
}

func TestRemoteCapabilityProbeInheritsCancellation(t *testing.T) {
	c, rm := networkController(1)
	delete(c.statsCap, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan []remoteResult, 1)
	go func() { done <- c.remoteStats(ctx) }()
	waitRemoteEntries(t, rm, 1)
	cancel()
	select {
	case results := <-done:
		if len(results) != 1 || !errors.Is(results[0].err, context.Canceled) {
			t.Fatalf("results=%+v", results)
		}
	case <-time.After(time.Second):
		t.Fatal("capability probe ignored cancellation")
	}
	if _, ok := c.statsCap[1]; ok {
		t.Fatal("request cancellation poisoned capability cache")
	}
	if rm.tunnels.Load() != 0 || rm.active.Load() != 0 {
		t.Fatal("SSH work continued after cancellation")
	}
}
