package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"
)

// RefundPolicy is the store-configured refund behaviour, resolved from settings.
//   Mode  = prorated | full   — prorated refunds only the unused portion; full
//           always returns the whole price.
//   Basis = min | traffic | time — how a plan's prorated fraction is derived.
//           "min" takes the smaller of the remaining-traffic and remaining-time
//           fractions (anti-abuse). Pool (traffic-package) refunds are always
//           traffic-based since the pool never expires.
//   FeePercent = handling fee deducted from the refund (0 = none).
type RefundPolicy struct {
	Mode       string  `json:"mode"`
	Basis      string  `json:"basis"`
	FeePercent float64 `json:"fee_percent"`
}

// refundPolicy loads the effective policy from settings, applying defaults for
// missing/blank keys (prorated / min / no fee).
func (s *Store) refundPolicy() RefundPolicy {
	mode, _ := s.GetSetting("refund_mode")
	if mode != "full" {
		mode = "prorated"
	}
	basis, _ := s.GetSetting("refund_basis")
	switch basis {
	case "traffic", "time", "min":
	default:
		basis = "min"
	}
	feeStr, _ := s.GetSetting("refund_fee_percent")
	fee, _ := strconv.ParseFloat(feeStr, 64)
	if fee < 0 {
		fee = 0
	} else if fee > 100 {
		fee = 100
	}
	return RefundPolicy{Mode: mode, Basis: basis, FeePercent: fee}
}

// RefundQuote is the computed proration for refunding one order. Ratios are -1
// when the dimension does not apply (e.g. TimeRatio for an unlimited-duration
// plan or a never-expiring pool).
type RefundQuote struct {
	OrderID       int64   `json:"order_id"`
	PricePoints   int64   `json:"price_points"`
	Mode          string  `json:"mode"`
	Basis         string  `json:"basis"`
	Type          string  `json:"type"`
	Name          string  `json:"name"`
	TotalTraffic  int64   `json:"total_traffic"`  // this order's contributed quota
	UsedTraffic   int64   `json:"used_traffic"`   // the bucket's consumed bytes (context)
	RefundTraffic int64   `json:"refund_traffic"` // unused quota clawed back
	TrafficRatio  float64 `json:"traffic_ratio"`  // -1 = N/A
	TimeRatio     float64 `json:"time_ratio"`     // -1 = N/A
	Ratio         float64 `json:"ratio"`          // fraction of price refunded (post-fee)
	FeePercent    float64 `json:"fee_percent"`
	RefundPoints  int64   `json:"refund_points"`
	AlreadyDone   bool    `json:"already_refunded"`
}

// orderSnapshot decodes the fields of a package snapshot the refund path needs.
type orderSnapshot struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	TrafficBytes int64  `json:"traffic_bytes"`
	DurationDays int64  `json:"duration_days"`
}

func minI64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// computeRefundQuote derives the prorated refund for one order from the CURRENT
// bucket state (read via q, which may be a *sql.Tx so it sees uncommitted
// writes). The entitlement reversal is unchanged elsewhere; this only decides
// how many points come back. It reuses the same "unused = min(order_quota,
// bucket_limit - bucket_used)" clamp the reversal applies, so points and
// entitlement stay consistent, and it composes correctly across stacked
// renewals (each refund shrinks the bucket, so the next one sees less unused).
func computeRefundQuote(q txLike, orderID, userID, packageID int64, snap orderSnapshot, price int64, pol RefundPolicy, now int64) (*RefundQuote, error) {
	quote := &RefundQuote{
		OrderID: 0, PricePoints: price, Mode: pol.Mode, Basis: pol.Basis,
		Type: snap.Type, Name: snap.Name, TotalTraffic: snap.TrafficBytes,
		TrafficRatio: -1, TimeRatio: -1, FeePercent: pol.FeePercent,
	}

	if pol.Mode == "full" {
		quote.Ratio = applyFee(1, pol.FeePercent)
		quote.RefundTraffic = snap.TrafficBytes
		quote.RefundPoints = roundRefund(price, quote.Ratio)
		return quote, nil
	}

	// Read the live bucket this order fed (its own plan bucket by order_id, or the
	// shared pool). A missing bucket (fully consumed & expired → deleted) means
	// nothing is left to refund.
	var limit, used, expiry, periodDays int64
	var found bool
	if snap.Type == "plan" {
		// Queue model: one purchase = one bucket, so prefer this order's own bucket.
		var status string
		oerr := q.QueryRow(`SELECT traffic_limit, used_up+used_down, expiry_at, status, period_days FROM user_plans
			WHERE kind='plan' AND user_id=? AND order_id=? ORDER BY id LIMIT 1`, userID, orderID).
			Scan(&limit, &used, &expiry, &status, &periodDays)
		switch {
		case oerr == nil && status == "queued":
			// Never activated, nothing consumed → fully refundable (fee still applies).
			quote.RefundTraffic = snap.TrafficBytes
			quote.Ratio = applyFee(1, pol.FeePercent)
			quote.RefundPoints = roundRefund(price, quote.Ratio)
			return quote, nil
		case oerr == nil && (periodDays > 0 || !(status == "active" && snap.TrafficBytes > 0 && limit > snap.TrafficBytes)):
			// This order's own (single-order) active bucket: use its live values.
			found = true
		default:
			// No order bucket, or a legacy merged bucket (limit > this order's quota):
			// fall back to the package's head bucket for a clamped, compatible quote.
			if oerr != nil && !errors.Is(oerr, sql.ErrNoRows) {
				return nil, oerr
			}
			perr := q.QueryRow(`SELECT traffic_limit, used_up+used_down, expiry_at FROM user_plans
				WHERE user_id=? AND kind='plan' AND package_id=? ORDER BY id LIMIT 1`, userID, packageID).
				Scan(&limit, &used, &expiry)
			if perr == nil {
				found = true
			} else if !errors.Is(perr, sql.ErrNoRows) {
				return nil, perr
			}
		}
	} else { // traffic → pool (never expires)
		err := q.QueryRow(`SELECT traffic_limit, used_up+used_down FROM user_plans
			WHERE user_id=? AND kind='pool' ORDER BY id LIMIT 1`, userID).Scan(&limit, &used)
		if err == nil {
			found = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	quote.UsedTraffic = used

	// Remaining-traffic fraction: the unused part of THIS order's quota still
	// sitting in the bucket, over the order's full quota.
	// A monthly-reset份 refills every period, so its unused traffic is not a
	// meaningful share of the order: refund it by remaining time only (#96).
	if periodDays > 0 {
		quote.RefundTraffic = 0
		quote.TrafficRatio = -1
	} else if snap.TrafficBytes > 0 {
		remain := int64(0)
		if found {
			remain = limit - used
			if remain < 0 {
				remain = 0
			}
		}
		quote.RefundTraffic = minI64(snap.TrafficBytes, remain)
		quote.TrafficRatio = float64(quote.RefundTraffic) / float64(snap.TrafficBytes)
	}

	// Remaining-time fraction (plans only): the unelapsed part of THIS order's
	// duration still on the bucket's expiry, over the order's full duration.
	if snap.Type == "plan" && snap.DurationDays > 0 {
		orderSec := snap.DurationDays * 86400
		remainSec := int64(0)
		if found {
			remainSec = expiry - now
			if remainSec < 0 {
				remainSec = 0
			}
		}
		quote.TimeRatio = float64(minI64(orderSec, remainSec)) / float64(orderSec)
	}

	quote.Ratio = applyFee(combineRatios(pol.Basis, quote.TrafficRatio, quote.TimeRatio), pol.FeePercent)
	quote.RefundPoints = roundRefund(price, quote.Ratio)
	return quote, nil
}

// combineRatios reduces the (possibly N/A, sentinel -1) traffic/time fractions
// to a single refund fraction per the configured basis. A basis whose primary
// dimension is N/A falls back to the other dimension; if both are N/A (e.g. an
// unlimited-in-both plan) it returns 1 (nothing to prorate against → full).
func combineRatios(basis string, traffic, time float64) float64 {
	switch basis {
	case "traffic":
		if traffic >= 0 {
			return traffic
		}
		if time >= 0 {
			return time
		}
	case "time":
		if time >= 0 {
			return time
		}
		if traffic >= 0 {
			return traffic
		}
	default: // "min"
		switch {
		case traffic >= 0 && time >= 0:
			return math.Min(traffic, time)
		case traffic >= 0:
			return traffic
		case time >= 0:
			return time
		}
	}
	return 1
}

func applyFee(ratio, feePercent float64) float64 {
	r := ratio * (1 - feePercent/100)
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

// roundRefund converts a fraction of the price to whole points, never exceeding
// the original price nor going negative.
func roundRefund(price int64, ratio float64) int64 {
	pts := int64(math.Round(float64(price) * ratio))
	if pts < 0 {
		return 0
	}
	if pts > price {
		return price
	}
	return pts
}

// RefundPreview computes (read-only) what refunding this order would return
// under the current policy, so the admin can confirm before acting. modeOverride
// may be "" (use policy), "prorated", or "full".
func (s *Store) RefundPreview(orderID int64, modeOverride string) (*RefundQuote, error) {
	var (
		userID, pkgID, price int64
		snapStr, status      string
	)
	err := s.db.QueryRow(`SELECT user_id, package_id, package_snapshot, price_points, status
		FROM orders WHERE id=?`, orderID).Scan(&userID, &pkgID, &snapStr, &price, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	var snap orderSnapshot
	_ = json.Unmarshal([]byte(snapStr), &snap)

	pol := s.refundPolicy()
	if modeOverride == "full" || modeOverride == "prorated" {
		pol.Mode = modeOverride
	}
	q, err := computeRefundQuote(s.db, orderID, userID, pkgID, snap, price, pol, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	q.OrderID = orderID
	q.AlreadyDone = status == "refunded"
	return q, nil
}
