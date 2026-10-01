package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"qingzhou/internal/store"
)

const businessEmailSetting = "business_email_enabled"

func (a *API) businessEmailConfigured() bool {
	enabled, err := a.st.GetSettingBool(businessEmailSetting)
	return err == nil && enabled && a.mailerConfigured()
}

func (a *API) handleEmailNotifyPrefs(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(ctxUserID).(int64)
	if r.Method == http.MethodPut {
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		d.DisallowUnknownFields()
		if d.Decode(&req) != nil || req.Enabled == nil {
			fail(w, 400, "请提供业务提醒邮件开关")
			return
		}
		if err := a.st.SetBusinessEmailEnabled(uid, *req.Enabled); err != nil {
			fail(w, 500, "保存提醒设置失败")
			return
		}
	}
	enabled, err := a.st.BusinessEmailEnabled(uid)
	if err != nil {
		fail(w, 500, "读取提醒设置失败")
		return
	}
	ok(w, J{"enabled": enabled, "available": a.businessEmailConfigured()})
}

// This sweep runs independently of Telegram configuration. No SMTP/global switch
// means no claims and no network traffic. Muted recipients still clear traffic
// recovery state so re-enabling later cannot inherit a stale low-quota claim.
func (a *API) sweepEmailNotifies() {
	if !a.businessEmailConfigured() {
		return
	}
	names, err := a.st.PackageNames()
	if err != nil {
		return
	}
	now := time.Now().Unix()
	days, pct := a.notifyExpiryDays(), a.notifyTrafficPercent()
	var after int64
	for {
		ids, err := a.st.BusinessEmailUsersAfter(after, 100)
		if err != nil {
			log.Printf("email reminders: list users: %v", err)
			return
		}
		if len(ids) == 0 {
			return
		}
		for _, uid := range ids {
			after = uid
			if !a.businessEmailConfigured() {
				return
			}
			u, err := a.st.UserByID(uid)
			if err != nil || u == nil {
				continue
			}
			enabled, err := a.st.BusinessEmailEnabled(uid)
			if err != nil {
				continue
			}
			_ = a.advanceQueueOnRead(uid)
			buckets, err := a.st.ListBuckets(uid)
			if err != nil {
				continue
			}
			if enabled {
				a.notifyEmailExpiry(u, buckets, names, now, days)
			}
			a.notifyEmailTraffic(u, buckets, pct, enabled)
		}
	}
}

func (a *API) notifyEmailExpiry(u *store.User, buckets []*store.Bucket, names map[int64]string, now, days int64) {
	for _, b := range buckets {
		if b.Kind == store.KindFree || b.Status == "queued" || b.TrafficLimit <= 0 || b.ExpiryAt <= 0 {
			continue
		}
		kind, title, content := "", "", ""
		switch {
		case b.ExpiryAt <= now && now-b.ExpiryAt <= 86400:
			kind, title = notifyKindExpired, "套餐已到期"
			content = "你的套餐「{{plan}}」已于 {{expiry}} 到期，请打开面板查看。"
		case b.ExpiryAt > now && b.ExpiryAt <= now+days*86400 && b.NotExpired(now):
			kind, title = notifyKindExpirySoon, "套餐即将到期"
			content = "你的套餐「{{plan}}」将于 {{expiry}} 到期，请及时查看或续费。"
		default:
			continue
		}
		vars := a.manualNotifyVars(u, nil)
		vars["plan"], vars["expiry"] = bucketDisplayName(b, names), fmtUnix(b.ExpiryAt)
		a.sendBusinessReminder(u, kind, fmt.Sprintf("b%d:%d", b.ID, b.ExpiryAt), title, content, vars)
	}
}

func (a *API) notifyEmailTraffic(u *store.User, buckets []*store.Bucket, pct int64, send bool) {
	tr := dashboardTraffic(buckets)
	clear := func(kind string) { _ = a.st.ClearNotifyChannel(u.ID, "email", kind, notifyTrafficSubject) }
	if tr.Total <= 0 {
		clear(notifyKindTrafficLow)
		clear(notifyKindTrafficOut)
		return
	}
	kind, title, content := notifyKindTrafficLow, "流量不足提醒", "你的剩余流量为 {{remaining}}（{{remain_pct}}%），请打开面板查看。"
	if tr.Remaining <= 0 {
		clear(notifyKindTrafficLow)
		kind, title, content = notifyKindTrafficOut, "流量已用尽", "你的可用流量已用尽，请打开面板查看或补充流量。"
	} else {
		clear(notifyKindTrafficOut)
		if tr.Remaining*100/tr.Total > pct {
			clear(notifyKindTrafficLow)
			return
		}
	}
	if send {
		a.sendBusinessReminder(u, kind, notifyTrafficSubject, title, content, a.manualNotifyVars(u, nil))
	}
}

func (a *API) sendBusinessReminder(u *store.User, kind, subject, title, content string, vars map[string]string) {
	// Recheck at delivery so a preference/global switch changed during a large
	// sweep is honored. Never route system verify/reset mail through this method.
	if !a.businessEmailConfigured() {
		return
	}
	enabled, err := a.st.BusinessEmailEnabled(u.ID)
	if err != nil || !enabled {
		return
	}
	live, err := a.st.UserByID(u.ID)
	if err != nil || live == nil || live.Status != "active" || !live.EmailVerified || !live.Email.Valid ||
		!validEmail(live.Email.String) || !strings.EqualFold(live.Email.String, u.Email.String) {
		return
	}
	claimed, err := a.st.ClaimNotifyChannel(u.ID, "email", kind, subject)
	if err != nil || !claimed {
		return
	}
	content += "\n\n面板：{{panel}}\n可在账户设置关闭业务提醒邮件；邮箱验证、找回密码和安全邮件不受此开关影响。"
	title, content = renderManualNotificationText(title, content, vars)
	mailSubject, body := renderManualEmailNotification(a.siteName(), title, content)
	if err := a.sendManualEmail([]string{live.Email.String}, mailSubject, body); err != nil {
		_ = a.st.ClearNotifyChannel(u.ID, "email", kind, subject)
		log.Printf("email reminder: user %d delivery failed", u.ID)
	}
}
