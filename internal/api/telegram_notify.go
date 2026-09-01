package api

import (
	"context"
	"fmt"
	"log"
	"time"

	"qingzhou/internal/store"
	"qingzhou/internal/telegram"
)

const (
	notifyKindExpirySoon = "expiry_soon"
	notifyKindExpired    = "expired"
	notifyKindTrafficLow = "traffic_low"
	notifyKindTrafficOut = "traffic_exhausted"

	notifyTrafficSubject = "account"
)

func (a *API) telegramNotifyLoop(ctx context.Context) {
	// A short first delay so boot isn't a burst of getMe + notify scans
	// against a token the admin is still pasting in.
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sweepTelegramNotifies()
			t.Reset(5 * time.Minute)
		}
	}
}

func (a *API) notifyExpiryDays() int64 {
	n, _ := a.st.GetSettingInt64("notify_expiry_days", 3)
	if n < 1 {
		return 1
	}
	if n > 30 {
		return 30
	}
	return n
}

func (a *API) notifyTrafficPercent() int64 {
	n, _ := a.st.GetSettingInt64("notify_traffic_percent", 20)
	if n < 1 {
		return 1
	}
	if n > 50 {
		return 50
	}
	return n
}

func (a *API) sweepTelegramNotifies() {
	if !a.telegramConfigured() {
		return
	}
	binds, err := a.st.ListTelegramBinds()
	if err != nil || len(binds) == 0 {
		return
	}
	now := time.Now().Unix()
	days := a.notifyExpiryDays()
	pct := a.notifyTrafficPercent()
	names, _ := a.st.PackageNames()
	panel := a.siteBase()
	site := a.siteName()

	for _, b := range binds {
		u, err := a.st.UserByID(b.UserID)
		if err != nil || u == nil || u.Status == "banned" {
			continue
		}
		_ = a.advanceQueueOnRead(u.ID)
		buckets, err := a.st.ListBuckets(u.ID)
		if err != nil {
			log.Printf("telegram notify: list buckets user %d: %v", u.ID, err)
			continue
		}
		if b.NotifyExpiry {
			a.notifyExpiry(b, u.Username, buckets, names, now, days, panel, site)
		}
		// Traffic evaluation also maintains recovery state. While muted it only
		// clears stale low/out claims; it never creates a claim or sends.
		a.notifyTraffic(b, u.Username, buckets, pct, panel, site, b.NotifyTraffic)
	}
}

func (a *API) notifyExpiry(b *store.TelegramBind, username string, buckets []*store.Bucket, names map[int64]string, now, days int64, panel, site string) {
	horizon := now + days*86400
	for _, bk := range buckets {
		if bk.Kind == store.KindFree || bk.Status == "queued" {
			continue
		}
		if bk.TrafficLimit <= 0 {
			continue
		}
		if bk.ExpiryAt <= 0 {
			continue
		}
		name := bucketDisplayName(bk, names)
		switch {
		case bk.ExpiryAt <= now:
			// Only the first day after expiry, so turning the bot on does not
			// dump a backlog of long-dead套餐 at everyone.
			if now-bk.ExpiryAt > 86400 {
				continue
			}
			subject := fmt.Sprintf("b%d:%d", bk.ID, bk.ExpiryAt)
			ok, err := a.st.ClaimNotify(b.UserID, notifyKindExpired, subject)
			if err != nil || !ok {
				continue
			}
			m := a.tgBaseVars(username)
			m["plan"] = telegram.Escape(name)
			m["expiry"] = fmtUnix(bk.ExpiryAt)
			m["footer"] = tgFooter(panel, site, "打开面板续费")
			if err := a.tgSend(b.ChatID, applyTpl(a.tgTpl("expired"), m)); err != nil {
				_ = a.st.ClearNotify(b.UserID, notifyKindExpired, subject)
			}
		case bk.ExpiryAt <= horizon && bk.NotExpired(now):
			subject := fmt.Sprintf("b%d:%d", bk.ID, bk.ExpiryAt)
			ok, err := a.st.ClaimNotify(b.UserID, notifyKindExpirySoon, subject)
			if err != nil || !ok {
				continue
			}
			left := (bk.ExpiryAt - now + 3599) / 3600
			var leftText string
			if left >= 48 {
				leftText = fmt.Sprintf("%d 天", (bk.ExpiryAt-now+86399)/86400)
			} else {
				leftText = fmt.Sprintf("%d 小时", left)
			}
			m := a.tgBaseVars(username)
			m["plan"] = telegram.Escape(name)
			m["expiry"] = fmtUnix(bk.ExpiryAt)
			m["left"] = leftText
			m["footer"] = tgFooter(panel, site, "打开面板续费")
			if err := a.tgSend(b.ChatID, applyTpl(a.tgTpl("expiry_soon"), m)); err != nil {
				_ = a.st.ClearNotify(b.UserID, notifyKindExpirySoon, subject)
			}
		}
	}
}

func (a *API) notifyTraffic(b *store.TelegramBind, username string, buckets []*store.Bucket, pct int64, panel, site string, send bool) {
	tr := dashboardTraffic(buckets)
	// An empty roll-up is "no quota", not "0% left".
	if tr.Total <= 0 {
		_ = a.st.ClearNotify(b.UserID, notifyKindTrafficLow, notifyTrafficSubject)
		_ = a.st.ClearNotify(b.UserID, notifyKindTrafficOut, notifyTrafficSubject)
		return
	}
	remainPct := tr.Remaining * 100 / tr.Total
	if tr.Remaining <= 0 {
		_ = a.st.ClearNotify(b.UserID, notifyKindTrafficLow, notifyTrafficSubject)
		if !send {
			return
		}
		ok, err := a.st.ClaimNotify(b.UserID, notifyKindTrafficOut, notifyTrafficSubject)
		if err != nil || !ok {
			return
		}
		m := a.trafficVars(username, buckets)
		m["footer"] = tgFooter(panel, site, "打开面板查看")
		if err := a.tgSend(b.ChatID, applyTpl(a.tgTpl("traffic_out"), m)); err != nil {
			_ = a.st.ClearNotify(b.UserID, notifyKindTrafficOut, notifyTrafficSubject)
		}
		return
	}
	_ = a.st.ClearNotify(b.UserID, notifyKindTrafficOut, notifyTrafficSubject)
	if remainPct > pct {
		_ = a.st.ClearNotify(b.UserID, notifyKindTrafficLow, notifyTrafficSubject)
		return
	}
	if !send {
		return
	}
	ok, err := a.st.ClaimNotify(b.UserID, notifyKindTrafficLow, notifyTrafficSubject)
	if err != nil || !ok {
		return
	}
	m := a.trafficVars(username, buckets)
	m["footer"] = tgFooter(panel, site, "打开面板查看")
	if err := a.tgSend(b.ChatID, applyTpl(a.tgTpl("traffic_low"), m)); err != nil {
		_ = a.st.ClearNotify(b.UserID, notifyKindTrafficLow, notifyTrafficSubject)
	}
}

func bucketDisplayName(b *store.Bucket, names map[int64]string) string {
	if b.PackageID > 0 {
		if live, ok := names[b.PackageID]; ok && live != "" {
			return live
		}
	}
	if b.Name != "" {
		return b.Name
	}
	return "套餐"
}
