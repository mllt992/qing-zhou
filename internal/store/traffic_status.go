package store

import (
	"database/sql"
	"errors"
	"sort"
)

// Traffic status is the whole-machine picture an admin needs when a user
// reports a timeout at a known time: NIC byte deltas, the live connection
// count, and stretches where the probe stopped reporting.
//
// It is not the cycle-quota projection in server_traffic_analysis.go, and it
// does not read sing-box user stats. Every number comes from rows the probe
// (or the panel's own sampler) already stored.

const (
	// TrafficStatusMaxSpanSec is the widest window one query will answer.
	// Retention below is longer so a report from the last few days is still
	// inside the table; thirty days is the widest look-back the page offers.
	TrafficStatusMaxSpanSec int64 = 30 * 24 * 3600
	// TrafficStatusRetentionSec matches PruneMetrics(35) in the monitor
	// maintenance loop. Rows older than that are gone, so a query must not
	// pretend they can be read.
	TrafficStatusRetentionSec int64 = 35 * 24 * 3600
)

// ErrTrafficStatusRange is returned when the requested window is inverted,
// wider than TrafficStatusMaxSpanSec, or entirely outside retention.
var ErrTrafficStatusRange = errors.New("invalid traffic status range")

// TrafficStatusSample is one stored snapshot. Rates (bytes/s) are deliberately
// absent: a delayed report makes them unsafe to integrate. Deltas come from
// the cumulative NIC counters.
type TrafficStatusSample struct {
	Ts             int64
	NetRxTotal     int64
	NetTxTotal     int64
	NetTotalsValid bool
	Uptime         int64
	TCPConnections int
}

// TrafficStatusPoint is one display bucket. Rx/Tx are summed counter deltas
// of the samples that landed in the bucket, not a fabricated per-minute rate.
// TotalsValid is false when the bucket has observations but none of them could
// be turned into a counter delta — the UI must not draw those zeros as idle.
type TrafficStatusPoint struct {
	Ts          int64 `json:"ts"`
	RxBytes     int64 `json:"rx_bytes"`
	TxBytes     int64 `json:"tx_bytes"`
	Connections int   `json:"connections"`
	Samples     int   `json:"samples"`
	TotalsValid bool  `json:"totals_valid"`
}

// TrafficStatusGap is a stretch longer than the probe's silence threshold
// with no stored sample. From/To are the actual silent interval and may start
// slightly before the query when the last report was just outside it.
type TrafficStatusGap struct {
	From    int64 `json:"from"`
	To      int64 `json:"to"`
	Seconds int64 `json:"seconds"`
}

// TrafficStatus is the query result. Points and Gaps are empty slices (never
// null) so an empty window encodes as [] and the UI can say "no data" without
// inventing a chart.
type TrafficStatus struct {
	ServerID        int64                `json:"server_id"`
	From            int64                `json:"from"`
	To              int64                `json:"to"`
	BucketSec       int64                `json:"bucket_sec"`
	GapAfterSec     int64                `json:"gap_after_sec"`
	SampleCount     int                  `json:"sample_count"`
	DeltaSamples    int                  `json:"delta_samples"`
	TotalsSamples   int                  `json:"totals_samples"`
	RxBytes         int64                `json:"rx_bytes"`
	TxBytes         int64                `json:"tx_bytes"`
	PeakConnections int                  `json:"peak_connections"`
	Points          []TrafficStatusPoint `json:"points"`
	Gaps            []TrafficStatusGap   `json:"gaps"`
}

// NormalizeTrafficStatusRange clamps a requested window onto what the table
// can still answer. now is the caller's clock so tests stay deterministic.
func NormalizeTrafficStatusRange(from, to, now int64) (int64, int64, error) {
	if from <= 0 || to <= 0 || now <= 0 || from >= to {
		return 0, 0, ErrTrafficStatusRange
	}
	if to-from > TrafficStatusMaxSpanSec {
		return 0, 0, ErrTrafficStatusRange
	}
	oldest := now - TrafficStatusRetentionSec
	if to <= oldest {
		return 0, 0, ErrTrafficStatusRange
	}
	if from < oldest {
		from = oldest
	}
	if to > now {
		to = now
	}
	if from >= to {
		return 0, 0, ErrTrafficStatusRange
	}
	return from, to, nil
}

// TrafficStatusBucketSec picks a display bucket. A short investigation stays
// at one minute — the same cadence the samples are stored at. Longer windows
// widen the bucket so a month does not become tens of thousands of points.
func TrafficStatusBucketSec(span int64) int64 {
	switch {
	case span <= 48*3600:
		return 60
	case span <= 7*24*3600:
		return 300
	default:
		return 900
	}
}

// counterDelta is the same rule InsertMetrics uses at ingestion. A counter
// that went backwards is only trusted after a reboot (uptime decreased);
// otherwise the interval is dropped rather than counted as a full new total.
func counterDelta(prev, cur int64, rebooted bool) (int64, bool) {
	if cur >= prev {
		return cur - prev, true
	}
	if rebooted {
		return cur, true
	}
	return 0, false
}

// AggregateTrafficStatus turns stored snapshots into minute-or-wider buckets
// and silence gaps. samples may include one or more rows from before from;
// only the latest of those is the baseline for the first delta. Rows after to
// are ignored. The function does not read rates and does not fill empty
// buckets with zeros.
func AggregateTrafficStatus(samples []TrafficStatusSample, from, to, bucketSec, gapAfter int64) TrafficStatus {
	if bucketSec <= 0 {
		bucketSec = 60
	}
	if gapAfter <= 0 {
		gapAfter = 150
	}
	out := TrafficStatus{
		From: from, To: to, BucketSec: bucketSec, GapAfterSec: gapAfter,
		Points: []TrafficStatusPoint{},
		Gaps:   []TrafficStatusGap{},
	}
	if len(samples) == 0 || to <= from {
		return out
	}
	sorted := append([]TrafficStatusSample(nil), samples...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Ts < sorted[j].Ts })

	var baseline *TrafficStatusSample
	inRange := make([]TrafficStatusSample, 0, len(sorted))
	for i := range sorted {
		s := sorted[i]
		if s.Ts < from {
			c := s
			baseline = &c
			continue
		}
		if s.Ts > to {
			continue
		}
		inRange = append(inRange, s)
	}

	addGap := func(start, end int64) {
		if end <= start || end-start <= gapAfter {
			return
		}
		// Keep the part that overlaps the asked window. Seconds stays the
		// full silence so a gap that began just before `from` is not reported
		// as shorter than it was.
		full := end - start
		if start < from {
			start = from
		}
		if end > to {
			end = to
		}
		if end <= start {
			return
		}
		out.Gaps = append(out.Gaps, TrafficStatusGap{From: start, To: end, Seconds: full})
	}

	if len(inRange) == 0 {
		if baseline != nil {
			addGap(baseline.Ts, to)
		}
		return out
	}

	out.SampleCount = len(inRange)
	var prev *TrafficStatusSample
	if baseline != nil {
		prev = baseline
		addGap(baseline.Ts, inRange[0].Ts)
	} else {
		addGap(from, inRange[0].Ts)
	}

	type acc struct {
		point TrafficStatusPoint
	}
	buckets := map[int64]*acc{}
	order := make([]int64, 0)

	for i := range inRange {
		cur := inRange[i]
		if cur.TCPConnections > out.PeakConnections {
			out.PeakConnections = cur.TCPConnections
		}
		if cur.NetTotalsValid {
			out.TotalsSamples++
		}
		var rx, tx int64
		rxOK, txOK := false, false
		if prev != nil && prev.NetTotalsValid && cur.NetTotalsValid {
			rebooted := cur.Uptime < prev.Uptime
			rx, rxOK = counterDelta(prev.NetRxTotal, cur.NetRxTotal, rebooted)
			tx, txOK = counterDelta(prev.NetTxTotal, cur.NetTxTotal, rebooted)
		}
		if rxOK || txOK {
			out.DeltaSamples++
			if rxOK {
				out.RxBytes += rx
			}
			if txOK {
				out.TxBytes += tx
			}
		}
		bkt := cur.Ts - (cur.Ts % bucketSec)
		a := buckets[bkt]
		if a == nil {
			a = &acc{point: TrafficStatusPoint{Ts: bkt}}
			buckets[bkt] = a
			order = append(order, bkt)
		}
		a.point.Samples++
		a.point.Connections = cur.TCPConnections
		if rxOK {
			a.point.RxBytes += rx
			a.point.TotalsValid = true
		}
		if txOK {
			a.point.TxBytes += tx
			a.point.TotalsValid = true
		}
		c := cur
		prev = &c
		if i+1 < len(inRange) {
			addGap(cur.Ts, inRange[i+1].Ts)
		}
	}
	addGap(inRange[len(inRange)-1].Ts, to)

	out.Points = make([]TrafficStatusPoint, 0, len(order))
	for _, ts := range order {
		out.Points = append(out.Points, buckets[ts].point)
	}
	return out
}

// QueryTrafficStatus reads stored metrics for one server and aggregates them.
// It does not sample the host and does not call sing-box. gapAfterSec is how
// long a missing report must last before it counts as silence; the API passes
// the live online window so a deliberately slow probe is not painted as down
// between its own ticks.
func (s *Store) QueryTrafficStatus(serverID, from, to, now, gapAfterSec int64) (TrafficStatus, error) {
	from, to, err := NormalizeTrafficStatusRange(from, to, now)
	if err != nil {
		return TrafficStatus{}, err
	}
	rows, err := s.db.Query(`SELECT ts, net_rx_total, net_tx_total, net_totals_valid, uptime, tcp_connections
		FROM server_metrics
		WHERE server_id=? AND ts>=? AND ts<=?
		ORDER BY ts`, serverID, from, to)
	if err != nil {
		return TrafficStatus{}, err
	}
	defer rows.Close()

	samples := make([]TrafficStatusSample, 0)
	for rows.Next() {
		var sm TrafficStatusSample
		if err := rows.Scan(&sm.Ts, &sm.NetRxTotal, &sm.NetTxTotal, &sm.NetTotalsValid, &sm.Uptime, &sm.TCPConnections); err != nil {
			return TrafficStatus{}, err
		}
		samples = append(samples, sm)
	}
	if err := rows.Err(); err != nil {
		return TrafficStatus{}, err
	}

	var base TrafficStatusSample
	err = s.db.QueryRow(`SELECT ts, net_rx_total, net_tx_total, net_totals_valid, uptime, tcp_connections
		FROM server_metrics
		WHERE server_id=? AND ts<?
		ORDER BY ts DESC LIMIT 1`, serverID, from).Scan(
		&base.Ts, &base.NetRxTotal, &base.NetTxTotal, &base.NetTotalsValid, &base.Uptime, &base.TCPConnections)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TrafficStatus{}, err
	}
	if err == nil {
		samples = append(samples, base)
	}

	out := AggregateTrafficStatus(samples, from, to, TrafficStatusBucketSec(to-from), gapAfterSec)
	out.ServerID = serverID
	out.From = from
	out.To = to
	return out, nil
}
