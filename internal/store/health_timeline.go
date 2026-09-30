package store

import (
	"strings"
)

// Health timeline is the one picture on the admin overview: one strip per
// machine, one shared time axis. Color is whether the probe was reporting.
// Traffic is only a shade on top of a reporting cell.
//
// The probe payload has no latency and no packet loss (see sysmetrics.Metrics).
// A timeout report therefore cannot be painted from "suddenly slower"; the
// fault color is silence. Idle — samples arrived, byte delta about zero — stays
// on the reporting color with no shade. A gap is not a zero-traffic bucket.

const (
	// HealthOnline means at least one stored sample landed in the bucket.
	HealthOnline = "online"
	// HealthGap means the bucket sits in a silence longer than the probe's
	// online window. It must not be drawn as zero traffic.
	HealthGap = "gap"
	// HealthBridge means no sample fell in this bucket, but the hole is shorter
	// than the silence window, so the probe is not considered down. Traffic for
	// the bucket is unknown, not zero.
	HealthBridge = "bridge"

	// HealthSignalProbePresence is the only health signal this timeline has.
	// Latency and loss are not in the stored probe payload.
	HealthSignalProbePresence = "probe_presence"
)

// HealthMachine is one row the overview wants a strip for. The store does not
// decide which servers are in scope; the admin handler does.
type HealthMachine struct {
	ID    int64
	Name  string
	Local bool
}

// HealthCell is one bucket on a strip. Bytes is the NIC counter delta (rx+tx)
// and is meaningful only when Known is true. A gap or a sample whose counters
// could not be differenced keeps Known false so the UI cannot paint it idle.
type HealthCell struct {
	Ts    int64  `json:"ts"`
	State string `json:"state"`
	Bytes int64  `json:"bytes,omitempty"`
	Known bool   `json:"known,omitempty"`
}

// HealthStrip is one machine. Cells is empty (and Empty is true) when the
// window has neither samples nor a silence we can see from a prior sample.
// That is "no samples yet", not a fabricated outage.
type HealthStrip struct {
	ServerID    int64        `json:"server_id"`
	Name        string       `json:"name"`
	Local       bool         `json:"local"`
	Empty       bool         `json:"empty"`
	SampleCount int          `json:"sample_count"`
	MaxBytes    int64        `json:"max_bytes"`
	Cells       []HealthCell `json:"cells"`
}

// HealthTimeline is the overview payload. Every machine shares From/To/BucketSec.
type HealthTimeline struct {
	From             int64         `json:"from"`
	To               int64         `json:"to"`
	BucketSec        int64         `json:"bucket_sec"`
	GapAfterSec      int64         `json:"gap_after_sec"`
	HealthSignal     string        `json:"health_signal"`
	LatencyAvailable bool          `json:"latency_available"`
	LossAvailable    bool          `json:"loss_available"`
	Machines         []HealthStrip `json:"machines"`
}

// BuildHealthCells lays TrafficStatus onto a uniform bucket grid. Buckets with
// a sample are online (idle only when the counter delta is known and zero).
// Buckets inside a silence gap are gap, with no byte value. Buckets in a short
// hole are bridge. No samples and no gaps yields nil — not a row of zeros.
func BuildHealthCells(st TrafficStatus) []HealthCell {
	if st.SampleCount == 0 && len(st.Gaps) == 0 {
		return nil
	}
	bucket := st.BucketSec
	if bucket <= 0 {
		bucket = 60
	}
	points := make(map[int64]TrafficStatusPoint, len(st.Points))
	for _, p := range st.Points {
		points[p.Ts] = p
	}
	start := st.From - (st.From % bucket)
	cells := make([]HealthCell, 0, (st.To-start)/bucket+1)
	for ts := start; ts < st.To; ts += bucket {
		end := ts + bucket
		if end <= st.From {
			continue
		}
		if p, ok := points[ts]; ok {
			cell := HealthCell{Ts: ts, State: HealthOnline}
			if p.TotalsValid {
				cell.Known = true
				cell.Bytes = p.RxBytes + p.TxBytes
			}
			cells = append(cells, cell)
			continue
		}
		if cellOverlapsGap(ts, end, st.Gaps) {
			cells = append(cells, HealthCell{Ts: ts, State: HealthGap})
			continue
		}
		cells = append(cells, HealthCell{Ts: ts, State: HealthBridge})
	}
	return cells
}

func cellOverlapsGap(ts, end int64, gaps []TrafficStatusGap) bool {
	for _, g := range gaps {
		if g.From < end && g.To > ts {
			return true
		}
	}
	return false
}

// QueryHealthTimeline reads stored server_metrics for the given machines and
// builds one strip each. It does not sample hosts, capture packets, or read
// anything but rows already written by the probe (or the panel's own sampler).
func (s *Store) QueryHealthTimeline(machines []HealthMachine, from, to, now, gapAfterSec int64) (HealthTimeline, error) {
	from, to, err := NormalizeTrafficStatusRange(from, to, now)
	if err != nil {
		return HealthTimeline{}, err
	}
	if gapAfterSec <= 0 {
		gapAfterSec = 150
	}
	bucket := TrafficStatusBucketSec(to - from)
	out := HealthTimeline{
		From: from, To: to, BucketSec: bucket, GapAfterSec: gapAfterSec,
		HealthSignal:     HealthSignalProbePresence,
		LatencyAvailable: false,
		LossAvailable:    false,
		Machines:         []HealthStrip{},
	}
	if len(machines) == 0 {
		return out, nil
	}
	ids := make([]int64, len(machines))
	for i, m := range machines {
		ids[i] = m.ID
	}
	byServer, err := s.healthSamples(ids, from, to)
	if err != nil {
		return HealthTimeline{}, err
	}
	out.Machines = make([]HealthStrip, 0, len(machines))
	for _, m := range machines {
		agg := AggregateTrafficStatus(byServer[m.ID], from, to, bucket, gapAfterSec)
		cells := BuildHealthCells(agg)
		strip := HealthStrip{
			ServerID: m.ID, Name: m.Name, Local: m.Local,
			SampleCount: agg.SampleCount,
			Cells:       []HealthCell{},
		}
		if len(cells) == 0 {
			strip.Empty = true
		} else {
			strip.Cells = cells
			for _, c := range cells {
				if c.Known && c.Bytes > strip.MaxBytes {
					strip.MaxBytes = c.Bytes
				}
			}
		}
		out.Machines = append(out.Machines, strip)
	}
	return out, nil
}

func (s *Store) healthSamples(ids []int64, from, to int64) (map[int64][]TrafficStatusSample, error) {
	out := make(map[int64][]TrafficStatusSample, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.Repeat("?,", len(ids))
	ph = ph[:len(ph)-1]
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	inRange := append(append([]any{}, args...), from, to)
	rows, err := s.db.Query(`SELECT server_id, ts, net_rx_total, net_tx_total, net_totals_valid, uptime, tcp_connections
		FROM server_metrics
		WHERE server_id IN (`+ph+`) AND ts>=? AND ts<=?
		ORDER BY server_id, ts`, inRange...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var sm TrafficStatusSample
		if err := rows.Scan(&id, &sm.Ts, &sm.NetRxTotal, &sm.NetTxTotal, &sm.NetTotalsValid, &sm.Uptime, &sm.TCPConnections); err != nil {
			return nil, err
		}
		out[id] = append(out[id], sm)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// One baseline per machine, strictly before the window, so the first
	// in-range counter delta is real and a silence that started earlier is visible.
	baseArgs := append(append([]any{}, args...), from)
	bases, err := s.db.Query(`SELECT m.server_id, m.ts, m.net_rx_total, m.net_tx_total, m.net_totals_valid, m.uptime, m.tcp_connections
		FROM server_metrics m
		JOIN (
			SELECT server_id, MAX(ts) AS ts FROM server_metrics
			WHERE server_id IN (`+ph+`) AND ts<?
			GROUP BY server_id
		) b ON m.server_id=b.server_id AND m.ts=b.ts`, baseArgs...)
	if err != nil {
		return nil, err
	}
	defer bases.Close()
	for bases.Next() {
		var id int64
		var sm TrafficStatusSample
		if err := bases.Scan(&id, &sm.Ts, &sm.NetRxTotal, &sm.NetTxTotal, &sm.NetTotalsValid, &sm.Uptime, &sm.TCPConnections); err != nil {
			return nil, err
		}
		out[id] = append(out[id], sm)
	}
	if err := bases.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
