package api

import (
	"context"
	"log"
	"sync"
	"time"
)

// StartQueueAdvance periodically promotes queued plan buckets whose active head
// has been exhausted or has expired, so the next purchase in that renewal line
// takes over. Purchases advance their own user synchronously (in the buy
// transaction);
// this ticker covers the later exhaustion/expiry transitions, which have no
// synchronous trigger. A promotion changes which sing-box identity is live, so a
// config rebuild is scheduled whenever anything advanced. Mirrors the
// StartCertRenew ticker pattern.
//
// It is a backstop, not the only path: advanceQueueOnRead promotes a user's份 the
// moment their dashboard or subscription is fetched, so nobody waits out an
// interval — or stays stranded if this loop is unhealthy.
//
// The first sweep runs immediately rather than one interval in, so accounts that
// are ALREADY stuck (queued份 behind a套餐 that expired while the old code was
// running) are activated at startup instead of after the panel has been up for
// two minutes. That makes the upgrade itself the repair.
//
// wg (may be nil) lets shutdown wait for an in-flight sweep. A sweep holds write
// transactions, and the startup one runs at exactly the moment a restarting panel
// is most likely to be told to stop again — without this the deferred store Close
// can fire mid-transaction and log a "database is closed" failure for promotions
// that were half done. Same treatment the sing-box controller loop already gets.
func (a *API) StartQueueAdvance(ctx context.Context, interval time.Duration, wg *sync.WaitGroup) {
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		a.sweepQueues()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.sweepQueues()
			}
		}
	}()
}

// sweepQueues first hands off already-purchased queued segments, then renews
// expired lines whose user has enabled automatic renewal. Both paths can change
// entitlements, so publish one deduplicated sing-box rebuild per user.
//
// A partial sweep still counts: AdvanceAllQueues promotes everyone it can and
// reports the failures rather than stopping at the first, so the error is logged
// but the config is still pushed for whoever DID activate.
func (a *API) sweepQueues() {
	promoted, err := a.st.AdvanceAllQueues()
	if err != nil {
		log.Printf("queue advance: %v", err)
	}
	renewed, renewErr := a.st.AutoRenewDuePlans()
	if renewErr != nil {
		log.Printf("automatic renewal: %v", renewErr)
	}
	if len(promoted) > 0 {
		log.Printf("queue advance: promoted next plan for %d user(s)", len(promoted))
	}
	if len(renewed) > 0 {
		log.Printf("automatic renewal: renewed due plan for %d user(s)", len(renewed))
	}
	// Monthly reset (#96): refill every period that ended; users who were paused
	// for running out come back, so their config is rebuilt below.
	revived, resetErr := a.st.ResetDuePeriods()
	if resetErr != nil {
		log.Printf("traffic period reset: %v", resetErr)
	}
	if len(revived) > 0 {
		log.Printf("traffic period reset: restored service for %d user(s)", len(revived))
	}
	changed := append(promoted, renewed...)
	changed = append(changed, revived...)
	if len(changed) > 0 {
		seen := make(map[int64]struct{}, len(changed))
		unique := make([]int64, 0, len(changed))
		for _, userID := range changed {
			if _, ok := seen[userID]; ok {
				continue
			}
			seen[userID] = struct{}{}
			unique = append(unique, userID)
		}
		a.onQueuePromoted(unique...)
	}
}
