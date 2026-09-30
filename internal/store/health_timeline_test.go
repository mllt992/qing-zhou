package store

import (
	"testing"
	"time"
)

func cellAt(cells []HealthCell, ts int64) (HealthCell, bool) {
	for _, c := range cells {
		if c.Ts == ts {
			return c, true
		}
	}
	return HealthCell{}, false
}

func TestBuildHealthCellsGapIdleAndTraffic(t *testing.T) {
	const (
		from     int64 = 10_080 // divisible by 60
		bucket   int64 = 60
		gapAfter int64 = 150
	)

	// Idle: the probe keeps reporting and the NIC counters do not move.
	// That is healthy, not a fault, and not a gap.
	idle := AggregateTrafficStatus([]TrafficStatusSample{
		{Ts: from - 60, NetRxTotal: 5000, NetTxTotal: 8000, NetTotalsValid: true, Uptime: 1000, TCPConnections: 2},
		{Ts: from, NetRxTotal: 5000, NetTxTotal: 8000, NetTotalsValid: true, Uptime: 1060, TCPConnections: 2},
		{Ts: from + 60, NetRxTotal: 5000, NetTxTotal: 8000, NetTotalsValid: true, Uptime: 1120, TCPConnections: 1},
		{Ts: from + 120, NetRxTotal: 5000, NetTxTotal: 8000, NetTotalsValid: true, Uptime: 1180, TCPConnections: 1},
	}, from, from+180, bucket, gapAfter)
	idleCells := BuildHealthCells(idle)
	if len(idle.Gaps) != 0 {
		t.Fatalf("idle painted a gap: %+v", idle.Gaps)
	}
	for _, ts := range []int64{from, from + 60, from + 120} {
		c, ok := cellAt(idleCells, ts)
		if !ok {
			t.Fatalf("idle missing bucket %d in %+v", ts, idleCells)
		}
		if c.State != HealthOnline || !c.Known || c.Bytes != 0 {
			t.Fatalf("idle bucket %d = %+v, want online known 0 bytes", ts, c)
		}
	}

	// High traffic stays the same health state. Shade is the byte count,
	// not a second fault color.
	busy := AggregateTrafficStatus([]TrafficStatusSample{
		{Ts: from - 60, NetRxTotal: 1000, NetTxTotal: 1000, NetTotalsValid: true, Uptime: 1000},
		{Ts: from, NetRxTotal: 1000 + 8<<20, NetTxTotal: 1000 + 2<<20, NetTotalsValid: true, Uptime: 1060},
		{Ts: from + 60, NetRxTotal: 1000 + 20<<20, NetTxTotal: 1000 + 6<<20, NetTotalsValid: true, Uptime: 1120},
	}, from, from+120, bucket, gapAfter)
	busyCells := BuildHealthCells(busy)
	if len(busy.Gaps) != 0 {
		t.Fatalf("busy painted a gap: %+v", busy.Gaps)
	}
	var peak int64
	for _, c := range busyCells {
		if c.State != HealthOnline {
			t.Fatalf("busy cell changed health: %+v", c)
		}
		if c.Bytes > peak {
			peak = c.Bytes
		}
	}
	if peak < 8<<20 {
		t.Fatalf("busy peak bytes = %d, want a multi-megabyte shade", peak)
	}
	first, ok := cellAt(busyCells, from)
	if !ok || !first.Known || first.Bytes == 0 {
		t.Fatalf("busy first cell = %+v", first)
	}

	// A ten-minute hole is silence. Those buckets are gap, with no byte
	// value, even though the counters jump across the hole afterwards.
	const hole = int64(600)
	gapped := AggregateTrafficStatus([]TrafficStatusSample{
		{Ts: from - 60, NetRxTotal: 1000, NetTxTotal: 1000, NetTotalsValid: true, Uptime: 1000},
		{Ts: from, NetRxTotal: 1500, NetTxTotal: 1200, NetTotalsValid: true, Uptime: 1060},
		{Ts: from + 60, NetRxTotal: 1800, NetTxTotal: 1400, NetTotalsValid: true, Uptime: 1120},
		// Probe went quiet, then came back with whatever accumulated.
		{Ts: from + 60 + hole, NetRxTotal: 9000, NetTxTotal: 4000, NetTotalsValid: true, Uptime: 1120 + hole},
		{Ts: from + 120 + hole, NetRxTotal: 9500, NetTxTotal: 4200, NetTotalsValid: true, Uptime: 1180 + hole},
	}, from, from+180+hole, bucket, gapAfter)
	if len(gapped.Gaps) != 1 || gapped.Gaps[0].Seconds != hole {
		t.Fatalf("gap = %+v, want one %ds hole", gapped.Gaps, hole)
	}
	gapCells := BuildHealthCells(gapped)
	silent := 0
	for ts := from + 120; ts < from+60+hole; ts += bucket {
		c, ok := cellAt(gapCells, ts)
		if !ok {
			t.Fatalf("missing silent bucket %d", ts)
		}
		if c.State != HealthGap || c.Known || c.Bytes != 0 {
			t.Fatalf("silent bucket %d = %+v, want gap without a zero-traffic reading", ts, c)
		}
		silent++
	}
	if silent < 5 {
		t.Fatalf("only %d silent buckets", silent)
	}
	// The reporting edges stay online. The byte jump across the hole is a
	// delta on the return bucket, not a reason to call the hole "idle".
	edge, ok := cellAt(gapCells, from+60)
	if !ok || edge.State != HealthOnline || !edge.Known || edge.Bytes == 0 {
		t.Fatalf("pre-gap edge = %+v", edge)
	}
	back, ok := cellAt(gapCells, from+60+hole)
	if !ok || back.State != HealthOnline || !back.Known || back.Bytes == 0 {
		t.Fatalf("return bucket = %+v", back)
	}
}

func TestBuildHealthCellsShortHoleIsNotSilence(t *testing.T) {
	const (
		from     int64 = 10_080
		bucket   int64 = 60
		gapAfter int64 = 150
	)
	// 120s between reports is inside the online window (silence starts at
	// 150s) and skips a whole minute bucket. That minute is a bridge: the
	// probe is not down, and the bucket must not be drawn as zero traffic.
	st := AggregateTrafficStatus([]TrafficStatusSample{
		{Ts: from, NetRxTotal: 100, NetTxTotal: 100, NetTotalsValid: true, Uptime: 1000},
		{Ts: from + 120, NetRxTotal: 140, NetTxTotal: 110, NetTotalsValid: true, Uptime: 1120},
	}, from, from+180, bucket, gapAfter)
	if len(st.Gaps) != 0 {
		t.Fatalf("short hole became a gap: %+v", st.Gaps)
	}
	cells := BuildHealthCells(st)
	bridge, ok := cellAt(cells, from+60)
	if !ok || bridge.State != HealthBridge || bridge.Known || bridge.Bytes != 0 {
		t.Fatalf("bridge cell = %+v in %+v", bridge, cells)
	}
	back, ok := cellAt(cells, from+120)
	if !ok || back.State != HealthOnline || !back.Known || back.Bytes == 0 {
		t.Fatalf("sample after the short hole = %+v", back)
	}
}

func TestBuildHealthCellsNoSamplesIsEmpty(t *testing.T) {
	st := AggregateTrafficStatus(nil, 10_080, 10_080+3600, 60, 150)
	if cells := BuildHealthCells(st); cells != nil {
		t.Fatalf("empty window drew %d cells", len(cells))
	}
}

func TestQueryHealthTimelineSeparatesMachines(t *testing.T) {
	st := newRefundStore(t)
	now := time.Now().Unix()
	to := now
	from := to - 780
	bucket := TrafficStatusBucketSec(to - from)
	if bucket != 60 {
		t.Fatalf("13min bucket = %d, want 60", bucket)
	}

	put := func(id, ts, rx, tx, up int64) {
		t.Helper()
		if err := st.InsertMetrics(id, ServerMetrics{
			Ts: ts, NetRxTotal: rx, NetTxTotal: tx, NetTotalsValid: true, Uptime: up,
			TCPConnections: 4,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Idle and busy both report on every bucket, so neither has a silence.
	for ts, i := from, int64(0); ts <= to; ts, i = ts+bucket, i+1 {
		put(1, ts, 10_000, 20_000, 5_000+i*bucket)
		put(2, ts, i*30<<20, i*5<<20, 8_000+i*bucket)
	}
	// Ten quiet minutes in the middle. Edges keep reporting.
	put(3, from, 1000, 1000, 1000)
	put(3, from+bucket, 1600, 1300, 1000+bucket)
	put(3, to-2*bucket, 8000, 3000, 1000+(to-2*bucket-from))
	put(3, to-bucket, 8600, 3200, 1000+(to-bucket-from))
	put(3, to, 9000, 3400, 1000+(to-from))

	got, err := st.QueryHealthTimeline([]HealthMachine{
		{ID: 1, Name: "空闲"},
		{ID: 2, Name: "繁忙"},
		{ID: 3, Name: "中断"},
		{ID: 4, Name: "未采样"},
	}, from, to, now, 150)
	if err != nil {
		t.Fatal(err)
	}
	if got.HealthSignal != HealthSignalProbePresence || got.LatencyAvailable || got.LossAvailable {
		t.Fatalf("signal = %+v", got)
	}
	if len(got.Machines) != 4 {
		t.Fatalf("machines = %d", len(got.Machines))
	}

	idle := got.Machines[0]
	if idle.Empty || idle.MaxBytes != 0 {
		t.Fatalf("idle strip max=%d empty=%v", idle.MaxBytes, idle.Empty)
	}
	idleKnown := false
	for _, c := range idle.Cells {
		if c.State == HealthGap {
			t.Fatalf("idle has a gap cell %+v", c)
		}
		if c.Known && c.Bytes != 0 {
			t.Fatalf("idle has traffic %+v", c)
		}
		if c.State == HealthOnline && c.Known && c.Bytes == 0 {
			idleKnown = true
		}
	}
	if !idleKnown {
		t.Fatalf("idle never showed a known-zero reporting bucket")
	}

	busy := got.Machines[1]
	if busy.Empty || busy.MaxBytes < 20<<20 {
		t.Fatalf("busy shade = %d, empty=%v", busy.MaxBytes, busy.Empty)
	}
	for _, c := range busy.Cells {
		if c.State == HealthGap {
			t.Fatalf("high traffic painted as silence: %+v", c)
		}
		if c.State == HealthOnline && c.Known && c.Bytes == 0 && c.Ts != from {
			// The first bucket has no prior counter, so it is unknown rather
			// than a real zero. Any later known zero would mean the shade failed.
			t.Fatalf("busy bucket looks idle: %+v", c)
		}
	}

	gapped := got.Machines[2]
	gaps := 0
	for _, c := range gapped.Cells {
		if c.State != HealthGap {
			continue
		}
		gaps++
		if c.Known || c.Bytes != 0 {
			t.Fatalf("gap cell carries traffic %+v", c)
		}
	}
	if gaps < 5 {
		t.Fatalf("gapped strip only has %d gap cells", gaps)
	}

	if !got.Machines[3].Empty || len(got.Machines[3].Cells) != 0 {
		t.Fatalf("unsampled machine empty=%v cells=%d", got.Machines[3].Empty, len(got.Machines[3].Cells))
	}
}
