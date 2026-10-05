package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Use panel IDs in diagnostics, never client credentials or derived wire keys.
type relayFixtureCounterID struct {
	Hop   int
	Owner int64
}

type relayFixtureCounterTarget struct {
	Before  UsageDelta
	Minimum UsageDelta // completed application payload, not a substituted raw count
	Frozen  bool
}

type relayFixtureCounterSample map[relayFixtureCounterID]UsageDelta

type relayFixtureCounterSettler struct {
	targets     map[relayFixtureCounterID]relayFixtureCounterTarget
	first, last relayFixtureCounterSample
	stableSince time.Time
	deadline    time.Time
	stableFor   time.Duration
	samples     int
	// http2LostWrites, when set, returns how many download writes the hop's
	// own core log reported as "http2: stream closed" (v2ray HTTP/2 transport
	// only). See relayHTTP2LostWriteBound. Nil means no allowance at all.
	http2LostWrites func(hop int) int
	// slack records the per-hop Down shortfall accepted under the bound, for
	// diagnostics. It is reset on every sample.
	slack map[int]relayHTTP2Slack
}

type relayHTTP2Slack struct {
	Shortfall  int64
	LostWrites int
	Bound      int64
}

// relayHTTP2LostWriteBound is the largest payload one failed download write can
// carry: sing's copy loop counts a buffer only after WriteBuffer succeeds, and
// its largest pooled buffer is buf.MaxPooledBufferSize (1<<16 + 1<<13).
//
// golang.org/x/net/http2 (v0.57.0, server.go writeDataFromHandler) returns
// errStreamClosed when the peer's RST_STREAM is processed by the serve loop
// before the wroteFrame result of a DATA frame that was already written. The
// client therefore has the bytes while sing-box counts zero for that write.
// This is a pre-existing, conservative (under-charge) metering limit of the
// v2ray HTTP/2 transport for every protocol (vless/vmess/trojan alike), not
// part of the #87 Trojan handshake patch. The allowance is accepted only when
// the same hop logged that exact error, is bounded per logged failure, applies
// to Down only, and never relaxes frozen/cross-user/regression checks.
const relayHTTP2LostWriteBound = int64(1<<16 + 1<<13)

// relayHTTP2SlackStableFor is the longer quiet period required before an
// allowance is accepted, so ordinary read-side visibility lag is not mistaken
// for a lost write.
const relayHTTP2SlackStableFor = 500 * time.Millisecond

func cloneRelayCounterSample(sample relayFixtureCounterSample) relayFixtureCounterSample {
	copy := make(relayFixtureCounterSample, len(sample))
	for key, value := range sample {
		copy[key] = value
	}
	return copy
}

// Bytes can reach the receiving application before the sending core returns
// from WriteBuffer and executes its atomic stats increment. Observe only waits
// for that read-side visibility boundary. It never generates or retries traffic,
// adjusts a counter, or accepts an unrelated owner's intermediate movement.
func (s *relayFixtureCounterSettler) observe(now time.Time, sample relayFixtureCounterSample) (bool, error) {
	s.samples++
	if s.first == nil {
		s.first = cloneRelayCounterSample(sample)
	}
	previous := s.last
	s.last = cloneRelayCounterSample(sample)
	ready, changed := true, previous == nil
	s.slack = nil
	downShort := map[int]int64{}
	for key, target := range s.targets {
		got, ok := sample[key]
		if !ok {
			return false, fmt.Errorf("missing counter hop=%d owner=%d", key.Hop, key.Owner)
		}
		if target.Frozen && got != target.Before {
			return false, fmt.Errorf("unrelated counter moved hop=%d owner=%d before=%+v got=%+v", key.Hop, key.Owner, target.Before, got)
		}
		before := target.Before
		if previous != nil {
			before = previous[key]
		}
		if got.Up < before.Up || got.Down < before.Down {
			return false, fmt.Errorf("counter regressed hop=%d owner=%d before=%+v got=%+v", key.Hop, key.Owner, before, got)
		}
		if got.Up < target.Minimum.Up {
			ready = false
		}
		if got.Down < target.Minimum.Down {
			downShort[key.Hop] += target.Minimum.Down - got.Down
		}
		if previous != nil && got != previous[key] {
			changed = true
		}
	}
	for hop, short := range downShort {
		lost := 0
		if s.http2LostWrites != nil {
			lost = s.http2LostWrites(hop)
		}
		bound := int64(lost) * relayHTTP2LostWriteBound
		if lost <= 0 || short > bound {
			ready = false
			continue
		}
		if s.slack == nil {
			s.slack = map[int]relayHTTP2Slack{}
		}
		s.slack[hop] = relayHTTP2Slack{Shortfall: short, LostWrites: lost, Bound: bound}
	}
	if !now.Before(s.deadline) {
		return false, fmt.Errorf("completed-payload counters did not become stable before deadline (down_shortfall_by_hop=%v accepted_http2_slack=%+v)", downShort, s.slack)
	}
	if !ready {
		s.stableSince = time.Time{}
		return false, nil
	}
	if changed || s.stableSince.IsZero() {
		s.stableSince = now
		return false, nil
	}
	stableFor := s.stableFor
	if len(s.slack) > 0 && stableFor < relayHTTP2SlackStableFor {
		stableFor = relayHTTP2SlackStableFor
	}
	return now.Sub(s.stableSince) >= stableFor, nil
}

func TestRelayCounterSnapshotWaitsForPayloadAndStability(t *testing.T) {
	start := time.Unix(100, 0)
	a, b := relayFixtureCounterID{0, 1}, relayFixtureCounterID{0, 2}
	oldA := UsageDelta{Up: 477, Down: 65675}
	s := relayFixtureCounterSettler{
		targets: map[relayFixtureCounterID]relayFixtureCounterTarget{
			a: {Before: oldA, Minimum: oldA, Frozen: true},
			b: {Minimum: UsageDelta{Up: 512, Down: 131072}},
		},
		deadline: start.Add(2 * time.Second), stableFor: 50 * time.Millisecond,
	}
	for _, step := range []struct {
		after time.Duration
		down  int64
		ready bool
	}{
		{0, 118442, false},
		{10 * time.Millisecond, 131211, false},
		{40 * time.Millisecond, 131211, false},
		{50 * time.Millisecond, 131212, false}, // trailing bytes reset stability
		{99 * time.Millisecond, 131212, false},
		{100 * time.Millisecond, 131212, true},
	} {
		got, err := s.observe(start.Add(step.after), relayFixtureCounterSample{a: oldA, b: {Up: 734, Down: step.down}})
		if err != nil || got != step.ready {
			t.Fatalf("after=%s settled=%t want=%t err=%v", step.after, got, step.ready, err)
		}
	}
	if s.first[b].Down != 118442 || s.last[b].Down != 131212 {
		t.Fatal("diagnostics lost the first or last actual counter sample")
	}
}

func TestRelayCounterSnapshotNeverHidesCrossUserMovement(t *testing.T) {
	start := time.Unix(100, 0)
	a, b := relayFixtureCounterID{0, 1}, relayFixtureCounterID{0, 2}
	before := UsageDelta{Up: 10, Down: 100}
	s := relayFixtureCounterSettler{
		targets: map[relayFixtureCounterID]relayFixtureCounterTarget{
			a: {Before: before, Minimum: before, Frozen: true},
			b: {Minimum: UsageDelta{Up: 20, Down: 200}},
		},
		deadline: start.Add(time.Second), stableFor: 50 * time.Millisecond,
	}
	// B is still below its floor. Waiting for B must not postpone or hide A's
	// movement, even if a hypothetical later sample returned to the baseline.
	_, err := s.observe(start, relayFixtureCounterSample{a: {Up: 10, Down: 101}, b: {Up: 1, Down: 1}})
	if err == nil || !strings.Contains(err.Error(), "unrelated counter moved") {
		t.Fatalf("cross-user movement was not rejected immediately: %v", err)
	}
}

func TestRelayCounterSnapshotRejectsPermanentUndercountAndRegression(t *testing.T) {
	start := time.Unix(100, 0)
	key := relayFixtureCounterID{1, 2}
	for _, regression := range []bool{false, true} {
		s := relayFixtureCounterSettler{
			targets:  map[relayFixtureCounterID]relayFixtureCounterTarget{key: {Minimum: UsageDelta{Up: 20, Down: 200}}},
			deadline: start.Add(time.Second), stableFor: 50 * time.Millisecond,
		}
		if ready, err := s.observe(start, relayFixtureCounterSample{key: {Up: 20, Down: 150}}); ready || err != nil {
			t.Fatalf("undercount prematurely accepted: ready=%t err=%v", ready, err)
		}
		now, down, want := start.Add(time.Second), int64(150), "before deadline"
		if regression {
			now, down, want = start.Add(time.Millisecond), 149, "counter regressed"
		}
		if _, err := s.observe(now, relayFixtureCounterSample{key: {Up: 20, Down: down}}); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("regression=%t error=%v, want %q", regression, err, want)
		}
	}
}

func TestRelayCounterSnapshotHTTP2LostWriteAllowanceIsBounded(t *testing.T) {
	start := time.Unix(100, 0)
	a, b := relayFixtureCounterID{0, 1}, relayFixtureCounterID{1, 2}
	minB := UsageDelta{Up: 20, Down: 1000000}
	newSettler := func(lost map[int]int) *relayFixtureCounterSettler {
		s := &relayFixtureCounterSettler{
			targets: map[relayFixtureCounterID]relayFixtureCounterTarget{
				a: {Before: UsageDelta{Up: 1, Down: 2}, Minimum: UsageDelta{Up: 1, Down: 2}, Frozen: true},
				b: {Minimum: minB},
			},
			deadline: start.Add(2 * time.Second), stableFor: 50 * time.Millisecond,
		}
		if lost != nil {
			s.http2LostWrites = func(hop int) int { return lost[hop] }
		}
		return s
	}
	run := func(s *relayFixtureCounterSettler, sample relayFixtureCounterSample, until time.Duration) (bool, error) {
		for at := time.Duration(0); at <= until; at += 10 * time.Millisecond {
			ready, err := s.observe(start.Add(at), sample)
			if err != nil || ready {
				return ready, err
			}
		}
		return false, nil
	}
	frozen := UsageDelta{Up: 1, Down: 2}
	short := relayFixtureCounterSample{a: frozen, b: {Up: 20, Down: minB.Down - 2*relayHTTP2LostWriteBound}}
	// No logged HTTP/2 lost write on that hop: undercount is still fatal.
	for _, lost := range []map[int]int{nil, {1: 0}, {0: 5}} {
		if ready, err := run(newSettler(lost), short, 2*time.Second); ready || err == nil || !strings.Contains(err.Error(), "before deadline") {
			t.Fatalf("lost=%v undercount accepted: ready=%t err=%v", lost, ready, err)
		}
	}
	// One logged failure cannot explain two buffers of missing bytes.
	if ready, err := run(newSettler(map[int]int{1: 1}), short, 2*time.Second); ready || err == nil {
		t.Fatalf("shortfall beyond bound accepted: ready=%t err=%v", ready, err)
	}
	// Within bound, accepted only after the longer quiet period.
	s := newSettler(map[int]int{1: 2})
	if ready, err := run(s, short, relayHTTP2SlackStableFor-20*time.Millisecond); ready || err != nil {
		t.Fatalf("allowance accepted before long stability: ready=%t err=%v", ready, err)
	}
	if ready, err := run(s, short, relayHTTP2SlackStableFor+20*time.Millisecond); !ready || err != nil || s.slack[1].LostWrites != 2 {
		t.Fatalf("bounded HTTP/2 allowance rejected: ready=%t err=%v slack=%+v", ready, err, s.slack)
	}
	// Up is never relaxed.
	upShort := relayFixtureCounterSample{a: frozen, b: {Up: 19, Down: minB.Down}}
	if ready, err := run(newSettler(map[int]int{1: 9}), upShort, 2*time.Second); ready || err == nil {
		t.Fatalf("upload undercount accepted: ready=%t err=%v", ready, err)
	}
	// Cross-user movement is still rejected immediately despite an allowance.
	moved := relayFixtureCounterSample{a: {Up: 1, Down: 3}, b: {Up: 20, Down: minB.Down}}
	if _, err := newSettler(map[int]int{0: 9, 1: 9}).observe(start, moved); err == nil || !strings.Contains(err.Error(), "unrelated counter moved") {
		t.Fatalf("cross-user movement hidden by allowance: %v", err)
	}
}
