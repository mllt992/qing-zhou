package store

import (
	"errors"
	"testing"
)

func TestNormalizeTrafficStatusRangeBounds(t *testing.T) {
	const now int64 = 1_800_000_000
	oldest := now - TrafficStatusRetentionSec

	if _, _, err := NormalizeTrafficStatusRange(100, 50, now); !errors.Is(err, ErrTrafficStatusRange) {
		t.Fatalf("inverted range err = %v", err)
	}
	if _, _, err := NormalizeTrafficStatusRange(100, 100, now); !errors.Is(err, ErrTrafficStatusRange) {
		t.Fatalf("empty range err = %v", err)
	}
	if _, _, err := NormalizeTrafficStatusRange(now, now+TrafficStatusMaxSpanSec+1, now+TrafficStatusMaxSpanSec+1); !errors.Is(err, ErrTrafficStatusRange) {
		t.Fatalf("span over 30d err = %v", err)
	}
	// Entirely older than the metrics retention job.
	if _, _, err := NormalizeTrafficStatusRange(oldest-10*86400, oldest, now); !errors.Is(err, ErrTrafficStatusRange) {
		t.Fatalf("pruned range err = %v", err)
	}
	// Future-only window collapses onto now and is rejected.
	if _, _, err := NormalizeTrafficStatusRange(now+60, now+3600, now); !errors.Is(err, ErrTrafficStatusRange) {
		t.Fatalf("future range err = %v", err)
	}

	from, to, err := NormalizeTrafficStatusRange(oldest-3600, oldest+3600, now)
	if err != nil {
		t.Fatal(err)
	}
	if from != oldest || to != oldest+3600 {
		t.Fatalf("clamped from/to = %d/%d, want %d/%d", from, to, oldest, oldest+3600)
	}

	from, to, err = NormalizeTrafficStatusRange(now-60, now+3600, now)
	if err != nil {
		t.Fatal(err)
	}
	if from != now-60 || to != now {
		t.Fatalf("future end not clamped: %d..%d", from, to)
	}

	// Exactly 30 days is allowed; one second more is not.
	if _, _, err := NormalizeTrafficStatusRange(now-TrafficStatusMaxSpanSec, now, now); err != nil {
		t.Fatalf("30d window: %v", err)
	}
}

func TestTrafficStatusBucketSec(t *testing.T) {
	if got := TrafficStatusBucketSec(48 * 3600); got != 60 {
		t.Fatalf("48h bucket = %d, want 60", got)
	}
	if got := TrafficStatusBucketSec(48*3600 + 1); got != 300 {
		t.Fatalf("just over 48h bucket = %d, want 300", got)
	}
	if got := TrafficStatusBucketSec(7 * 24 * 3600); got != 300 {
		t.Fatalf("7d bucket = %d, want 300", got)
	}
	if got := TrafficStatusBucketSec(7*24*3600 + 1); got != 900 {
		t.Fatalf("over 7d bucket = %d, want 900", got)
	}
}

func TestAggregateTrafficStatusCounterDeltasAndGaps(t *testing.T) {
	const (
		from     int64 = 1_000
		to       int64 = 1_600
		gapAfter int64 = 150
	)
	samples := []TrafficStatusSample{
		// Baseline just before the window. Its own delta is not in range;
		// the next sample's delta is.
		{Ts: from - 10, NetRxTotal: 1000, NetTxTotal: 5000, NetTotalsValid: true, Uptime: 1000, TCPConnections: 3},
		// +300 rx, +100 tx over 70s. Not a gap.
		{Ts: from + 60, NetRxTotal: 1300, NetTxTotal: 5100, NetTotalsValid: true, Uptime: 1070, TCPConnections: 8},
		// Two 30s reports in the same minute. Connections keep the later value.
		{Ts: from + 90, NetRxTotal: 1350, NetTxTotal: 5200, NetTotalsValid: true, Uptime: 1100, TCPConnections: 9},
		{Ts: from + 120, NetRxTotal: 1400, NetTxTotal: 5300, NetTotalsValid: true, Uptime: 1130, TCPConnections: 4},
		// Silence, then a reboot. Bytes since boot count; the hole is a gap.
		{Ts: from + 400, NetRxTotal: 50, NetTxTotal: 80, NetTotalsValid: true, Uptime: 20, TCPConnections: 1},
		// NIC counter reset without a reboot. That interval is not traffic.
		{Ts: from + 460, NetRxTotal: 10, NetTxTotal: 20, NetTotalsValid: true, Uptime: 80, TCPConnections: 2},
	}

	got := AggregateTrafficStatus(samples, from, to, 60, gapAfter)

	// Deltas: 300+100, 50+100, 50+100, reboot 50+80, reset ignored.
	if got.RxBytes != 300+50+50+50 || got.TxBytes != 100+100+100+80 {
		t.Fatalf("bytes rx/tx = %d/%d, want %d/%d", got.RxBytes, got.TxBytes, 300+50+50+50, 100+100+100+80)
	}
	if got.DeltaSamples != 4 { // four trusted intervals; the reset is not one
		t.Fatalf("delta samples = %d, want 4", got.DeltaSamples)
	}
	if got.SampleCount != 5 {
		t.Fatalf("sample count = %d, want 5", got.SampleCount)
	}
	if got.PeakConnections != 9 {
		t.Fatalf("peak connections = %d, want 9", got.PeakConnections)
	}

	// Bucket at from+60 holds the +70s sample (60) and both +90/+120 samples
	// (minute of 1000+60 = 1060; 1090 and 1120 floor to 1060?
	// from=1000, +60=1060, +90=1090, +120=1120.
	// 1060%60=40, floor -> 1020? Wait, bucket is unix-aligned, not window-aligned.
	// 1060 / 60 = 17.666 -> 1020. 1090 -> 1080. 1120 -> 1080.
	// So first in-range sample is its own bucket, the next two share one.
	var minute TrafficStatusPoint
	found := false
	for _, p := range got.Points {
		if p.Ts == 1080 {
			minute = p
			found = true
		}
	}
	if !found {
		t.Fatalf("missing bucket 1080: %+v", got.Points)
	}
	if minute.Samples != 2 || minute.Connections != 4 || minute.RxBytes != 100 || minute.TxBytes != 200 {
		t.Fatalf("shared minute = %+v, want 2 samples, conn 4, rx 100, tx 200", minute)
	}
	if !minute.TotalsValid {
		t.Fatal("bucket with real counter deltas must be marked valid")
	}

	// Gap between +120 (ts 1120) and +400 (ts 1400): 280s > 150.
	// Trailing from 1460 to 1600 is 140s, under the threshold.
	if len(got.Gaps) != 1 {
		t.Fatalf("gaps = %+v, want one silence", got.Gaps)
	}
	if got.Gaps[0].From != from+120 || got.Gaps[0].To != from+400 || got.Gaps[0].Seconds != 280 {
		t.Fatalf("gap = %+v, want 1120..1400 (280s)", got.Gaps[0])
	}

	// A rate-only row must not become traffic, and must not open a fake chart
	// value. Connections still count.
	legacy := AggregateTrafficStatus([]TrafficStatusSample{
		{Ts: from + 10, TCPConnections: 6},
		{Ts: from + 70, TCPConnections: 7},
	}, from, to, 60, gapAfter)
	if legacy.RxBytes != 0 || legacy.TxBytes != 0 || legacy.DeltaSamples != 0 {
		t.Fatalf("legacy rates counted as bytes: %+v", legacy)
	}
	if legacy.SampleCount != 2 || legacy.PeakConnections != 7 {
		t.Fatalf("legacy connections lost: %+v", legacy)
	}
	for _, p := range legacy.Points {
		if p.TotalsValid {
			t.Fatalf("legacy bucket looks like measured traffic: %+v", p)
		}
	}
}

func TestAggregateTrafficStatusNoFakePoints(t *testing.T) {
	empty := AggregateTrafficStatus(nil, 100, 500, 60, 150)
	if empty.SampleCount != 0 || len(empty.Points) != 0 || len(empty.Gaps) != 0 {
		t.Fatalf("empty window invented data: %+v", empty)
	}
	// Points are non-nil so JSON is [] not null.
	if empty.Points == nil || empty.Gaps == nil {
		t.Fatal("nil slices")
	}

	// A previous report and then nothing in the window is silence, not a
	// zero-traffic series.
	silent := AggregateTrafficStatus([]TrafficStatusSample{
		{Ts: 50, NetRxTotal: 10, NetTxTotal: 10, NetTotalsValid: true, Uptime: 10, TCPConnections: 1},
	}, 100, 500, 60, 150)
	if silent.SampleCount != 0 || len(silent.Points) != 0 {
		t.Fatalf("silence drew points: %+v", silent)
	}
	if len(silent.Gaps) != 1 || silent.Gaps[0].From != 100 || silent.Gaps[0].To != 500 {
		t.Fatalf("silence gap = %+v", silent.Gaps)
	}

	// Samples outside the window do not leak in. The one before it is only a
	// baseline: 1000 -> 1250 inside the window, the 5000 after `to` is ignored.
	bounded := AggregateTrafficStatus([]TrafficStatusSample{
		{Ts: 10, NetRxTotal: 1000, NetTxTotal: 0, NetTotalsValid: true, Uptime: 10},
		{Ts: 200, NetRxTotal: 1250, NetTxTotal: 40, NetTotalsValid: true, Uptime: 200},
		{Ts: 900, NetRxTotal: 99999, NetTxTotal: 99999, NetTotalsValid: true, Uptime: 900},
	}, 100, 400, 60, 150)
	if bounded.SampleCount != 1 || bounded.RxBytes != 250 || bounded.TxBytes != 40 {
		t.Fatalf("bounds leaked: %+v", bounded)
	}
}

func TestQueryTrafficStatusUsesCountersNotStoredDeltas(t *testing.T) {
	st := newRefundStore(t)
	put := func(serverID, ts, rx, tx, uptime int64, conns int) {
		t.Helper()
		if err := st.InsertMetrics(serverID, ServerMetrics{
			Ts: ts, NetRxTotal: rx, NetTxTotal: tx, NetTotalsValid: true,
			Uptime: uptime, TCPConnections: conns,
			// Hostile or stale derived fields must not affect the query.
			NetRxBytes: 999999, NetTxBytes: 888888,
			NetRx: 12345, NetTx: 54321,
		}); err != nil {
			t.Fatal(err)
		}
	}
	const now int64 = 1_800_000_000
	put(1, now-500, 1000, 2000, 100, 2)
	put(1, now-440, 1500, 2200, 160, 5)
	put(1, now-100, 1700, 2600, 500, 9)
	// Another machine's counters must not mix in.
	put(2, now-440, 9000, 9000, 160, 40)
	if _, err := st.DB().Exec(`UPDATE server_metrics SET net_rx_bytes=999999, net_tx_bytes=888888`); err != nil {
		t.Fatal(err)
	}

	got, err := st.QueryTrafficStatus(1, now-500, now-1, now, 150)
	if err != nil {
		t.Fatal(err)
	}
	// 500+200 rx, 200+400 tx. The stored 999999 columns are ignored.
	if got.ServerID != 1 || got.RxBytes != 700 || got.TxBytes != 600 {
		t.Fatalf("query = %+v, want rx 700 tx 600", got)
	}
	if got.PeakConnections != 9 || got.SampleCount != 3 {
		t.Fatalf("samples/peak = %d/%d", got.SampleCount, got.PeakConnections)
	}
	if len(got.Gaps) != 1 || got.Gaps[0].Seconds != 340 {
		t.Fatalf("gap = %+v, want 340s between 1060 and 1400", got.Gaps)
	}

	// Outside retention, and wider than 30 days.
	if _, err := st.QueryTrafficStatus(1, now-40*86400, now-36*86400, now, 150); !errors.Is(err, ErrTrafficStatusRange) {
		t.Fatalf("pruned query err = %v", err)
	}
	if _, err := st.QueryTrafficStatus(1, now-TrafficStatusMaxSpanSec-5, now, now, 150); !errors.Is(err, ErrTrafficStatusRange) {
		t.Fatalf("wide query err = %v", err)
	}
}
