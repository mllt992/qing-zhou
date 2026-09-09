package api

import (
	"context"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPingWorkersBoundConcurrencyAndDrainOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slots := make(chan struct{}, 8)
	started := make(chan struct{}, 64)
	var calls, active, peak atomic.Int32
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	before := runtime.NumGoroutine()
	var wg sync.WaitGroup
	for j := 0; j < 2; j++ {
		out := make([]pingResult, 10000)
		for i := range out {
			out[i] = pingResult{Server: "example.com", Port: 443}
		}
		wg.Add(1)
		go func() { defer wg.Done(); pingNodes(ctx, out, slots, dial) }()
	}
	for i := 0; i < cap(slots); i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	// A per-node goroutine fan-out would add ~20,000 here; allow runtime noise.
	if delta := runtime.NumGoroutine() - before; delta > 100 {
		t.Errorf("unbounded worker goroutines: %d", delta)
	}
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("workers leaked after cancellation")
	}
	if calls.Load() != 8 || peak.Load() > 8 || active.Load() != 0 || len(slots) != 0 {
		t.Fatalf("calls=%d peak=%d active=%d slots=%d", calls.Load(), peak.Load(), active.Load(), len(slots))
	}
}

func TestPingHonorsAlreadyCanceledContextAndSkipsUDP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	dial := func(context.Context, string, string) (net.Conn, error) {
		calls++
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
	out := []pingResult{{Server: "tcp.example", Port: 443}, {Server: "udp.example", Port: 443, UDP: true}, {Port: 443}}
	pingNodes(ctx, out, make(chan struct{}, 1), dial)
	if calls != 0 {
		t.Fatal("dialed canceled request")
	}
	pingNodes(context.Background(), out, make(chan struct{}, 1), dial)
	if calls != 1 || !out[0].OK || out[1].OK || out[2].OK {
		t.Fatalf("calls=%d results=%+v", calls, out)
	}
}
