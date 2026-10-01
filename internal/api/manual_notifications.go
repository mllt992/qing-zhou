package api

import (
	"encoding/json"
	"errors"
	"html"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/store"
	"qingzhou/internal/telegram"
)

// GET /api/admin/manual-notifications/users?q= returns active, non-admin users
// eligible for a manual broadcast and whether Telegram / email can reach each one.
func (a *API) handleAdminManualNotificationUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.st.ListUsers(strings.TrimSpace(r.URL.Query().Get("q")), 1000)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取用户失败")
		return
	}
	binds, err := a.st.ListTelegramBinds()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取 Telegram 绑定失败")
		return
	}
	bound := map[int64]bool{}
	for _, bind := range binds {
		if bind != nil && bind.ChatID != 0 {
			bound[bind.UserID] = true
		}
	}
	out := []J{}
	for _, user := range users {
		if user.Role == "admin" || user.Status != "active" {
			continue
		}
		email := ""
		if user.Email.Valid {
			email = user.Email.String
		}
		out = append(out, J{
			"id": user.ID, "username": user.Username,
			"email": email, "telegram_bound": bound[user.ID],
			"email_bound": email != "",
		})
	}
	ok(w, out)
}

func (a *API) handleAdminManualNotificationVars(w http.ResponseWriter, r *http.Request) {
	ok(w, manualNotifyVarViews())
}

func manualNotifyVarViews() []J {
	out := make([]J, 0, len(manualNotifyVarSpecs))
	for _, v := range manualNotifyVarSpecs {
		out = append(out, J{"key": v.Key, "desc": v.Desc})
	}
	return out
}

// POST /api/admin/manual-notifications
func (a *API) handleAdminCreateManualNotification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title      string  `json:"title"`
		Content    string  `json:"content"`
		TargetType string  `json:"target_type"`
		Channel    string  `json:"channel"`
		UserIDs    []int64 `json:"user_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Content = strings.TrimSpace(req.Content)
	if req.Title == "" {
		fail(w, http.StatusBadRequest, "标题不能为空")
		return
	}
	channel, err := store.NormalizeManualNotifyChannel(req.Channel)
	if err != nil {
		fail(w, http.StatusBadRequest, "通知渠道无效")
		return
	}
	needTelegram := channel == store.ManualNotifyTelegram || channel == store.ManualNotifyBoth
	needEmail := channel == store.ManualNotifyEmail || channel == store.ManualNotifyBoth
	if needTelegram && !a.telegramConfigured() {
		fail(w, http.StatusServiceUnavailable, "尚未配置 Telegram Bot")
		return
	}
	if needEmail && !a.mailerConfigured() {
		fail(w, http.StatusServiceUnavailable, "尚未配置邮件服务")
		return
	}
	if needEmail && !a.businessEmailConfigured() {
		fail(w, http.StatusServiceUnavailable, "业务提醒邮件尚未开启，请到系统设置 → 邮件服务开启")
		return
	}
	// Telegram messages are limited to 4096 characters. Leave space for the
	// emoji, HTML title wrapper, separators, and escaped entities.
	if len([]rune(req.Title)) > 100 || len([]rune(req.Content)) > 3000 {
		fail(w, http.StatusBadRequest, "标题最多 100 字，内容最多 3000 字")
		return
	}
	uid, _ := r.Context().Value(ctxUserID).(int64)
	notification, err := a.st.CreateManualNotificationWithChannel(req.Title, req.Content, req.TargetType, channel, req.UserIDs, uid)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	go a.deliverManualNotification(notification.ID)
	ok(w, notification)
}

func (a *API) deliverManualNotification(notificationID int64) {
	notification, err := a.st.ManualNotificationByID(notificationID)
	if err != nil || notification == nil {
		log.Printf("manual notification %d: load: %v", notificationID, err)
		return
	}
	for {
		recipient, err := a.st.ClaimManualNotificationRecipient(notificationID)
		if err != nil {
			log.Printf("manual notification %d: claim: %v", notificationID, err)
			return
		}
		if recipient == nil {
			return
		}

		status, reason := a.deliverManualNotificationRecipient(notification, recipient)
		if err := a.st.SetManualNotificationRecipientChannelResult(notificationID, recipient.UserID, recipient.Channel, status, reason); err != nil {
			log.Printf("manual notification %d: persist user %d %s: %v", notificationID, recipient.UserID, recipient.Channel, err)
		}
	}
}

func (a *API) deliverManualNotificationRecipient(notification *store.ManualNotification, recipient *store.ManualNotificationRecipient) (string, string) {
	user, err := a.st.UserByID(recipient.UserID)
	if err != nil {
		return "failed", truncateManualNotificationError(err.Error())
	}
	title, content := renderManualNotificationText(notification.Title, notification.Content, a.manualNotifyVars(user, recipient))
	switch recipient.Channel {
	case store.ManualNotifyEmail:
		subject, htmlBody := renderManualEmailNotification(a.siteName(), title, content)
		return a.deliverManualEmail(recipient, user, subject, htmlBody)
	default:
		return a.deliverManualTelegram(recipient, renderManualTelegramNotification(title, content))
	}
}

func (a *API) deliverManualTelegram(recipient *store.ManualNotificationRecipient, message string) (string, string) {
	bind, err := a.st.TelegramBindByUser(recipient.UserID)
	if err != nil {
		return "failed", truncateManualNotificationError(err.Error())
	}
	if bind == nil {
		return "skipped", "发送前已解绑 Telegram"
	}
	if bind.ChatID != recipient.ChatID {
		return "skipped", "Telegram 绑定已变更"
	}
	if err := a.tgSend(recipient.ChatID, message); err != nil {
		return "failed", truncateManualNotificationError(err.Error())
	}
	return "sent", ""
}

func (a *API) deliverManualEmail(recipient *store.ManualNotificationRecipient, user *store.User, subject, htmlBody string) (string, string) {
	if !a.businessEmailConfigured() {
		return "skipped", "业务提醒邮件已关闭"
	}
	enabled, err := a.st.BusinessEmailEnabled(recipient.UserID)
	if err != nil {
		return "failed", "读取业务提醒偏好失败"
	}
	if !enabled {
		return "skipped", "用户已关闭业务提醒邮件"
	}
	if user == nil || !user.Email.Valid || strings.TrimSpace(user.Email.String) == "" {
		return "skipped", "发送前已解绑邮箱"
	}
	live := strings.TrimSpace(user.Email.String)
	if !strings.EqualFold(live, recipient.Email) {
		return "skipped", "邮箱已变更"
	}
	if !validEmail(live) {
		return "skipped", "邮箱格式无效"
	}
	if err := a.sendManualEmail([]string{live}, subject, htmlBody); err != nil {
		return "failed", truncateManualNotificationError(err.Error())
	}
	return "sent", ""
}

func (a *API) sendManualEmail(to []string, subject, htmlBody string) error {
	if a.mailSendFn != nil {
		return a.mailSendFn(to, subject, htmlBody)
	}
	m := a.currentMailer()
	if m == nil {
		return errors.New("邮件服务未配置")
	}
	return m.Send(to, subject, htmlBody)
}

// StartManualNotifications resumes unfinished broadcasts at process startup.
// Pending rows are safe to resume because they were never claimed; interrupted
// sending rows are marked unknown instead of retried to avoid a possible
// duplicate Telegram / email. This is independent of Telegram being configured,
// because a restart may still have pending email deliveries.
func (a *API) StartManualNotifications() {
	go a.resumeManualNotifications()
}

func (a *API) resumeManualNotifications() {
	if err := a.st.FailInterruptedManualNotifications(); err != nil {
		log.Printf("manual notifications: recover interrupted: %v", err)
		return
	}
	ids, err := a.st.ListPendingManualNotificationIDs()
	if err != nil {
		log.Printf("manual notifications: list pending: %v", err)
		return
	}
	for _, id := range ids {
		a.deliverManualNotification(id)
	}
}

type manualNotifyVar struct {
	Key  string
	Desc string
}

// manualNotifyVarSpecs is the documented {{name}} set for admin broadcasts.
// Values are filled per recipient at send time. Subscription URLs are
// intentionally absent: a mass-mail is not a place to reprint a credential.
var manualNotifyVarSpecs = []manualNotifyVar{
	{"username", "用户名"},
	{"email", "账号邮箱；没有则为空"},
	{"site", "站点名称"},
	{"panel", "面板访问地址"},
	{"plan", "当前生效套餐名；没有则为 —"},
	{"expiry", "最近到期时间；不过期则为「不过期」"},
	{"used", "已用流量"},
	{"total", "当前可用流量额度；没有额度时为 —"},
	{"remaining", "剩余流量"},
	{"remain_pct", "剩余百分比数字，不含 %"},
	{"points", "当前积分"},
}

func renderManualNotificationText(title, content string, vars map[string]string) (string, string) {
	return applyTpl(strings.TrimSpace(title), vars), applyTpl(strings.TrimSpace(content), vars)
}

func (a *API) manualNotifyVars(user *store.User, recipient *store.ManualNotificationRecipient) map[string]string {
	username, email := "", ""
	if recipient != nil {
		username = recipient.Username
		email = strings.TrimSpace(recipient.Email)
	}
	if user != nil {
		username = user.Username
		if user.Email.Valid {
			email = strings.TrimSpace(user.Email.String)
		} else {
			email = ""
		}
	}
	site := a.siteName()
	if strings.TrimSpace(site) == "" {
		site = "轻舟"
	}
	m := map[string]string{
		"username":   username,
		"email":      email,
		"site":       site,
		"panel":      a.siteBase(),
		"plan":       "—",
		"expiry":     "—",
		"used":       fmtBytes(0),
		"total":      "—",
		"remaining":  "—",
		"remain_pct": "",
		"points":     "0",
	}
	if user == nil {
		return m
	}
	m["points"] = strconv.FormatInt(user.Points, 10)
	buckets, _ := a.st.ListBuckets(user.ID)
	tr := dashboardTraffic(buckets)
	switch {
	case tr.Total <= 0:
		m["used"] = fmtBytes(0)
	default:
		m["used"] = fmtBytes(tr.Used)
		m["total"] = fmtBytes(tr.Total)
		m["remaining"] = fmtBytes(tr.Remaining)
		m["remain_pct"] = strconv.FormatInt(tr.Remaining*100/tr.Total, 10)
	}
	names, _ := a.st.PackageNames()
	var active []string
	var nextExpiry int64
	neverExpires := false
	for _, p := range buildPlanViews(buckets, names) {
		if p.Status != "active" {
			continue
		}
		if p.Name != "" {
			active = append(active, p.Name)
		}
		if p.ExpiryAt <= 0 {
			neverExpires = true
			continue
		}
		if nextExpiry == 0 || p.ExpiryAt < nextExpiry {
			nextExpiry = p.ExpiryAt
		}
	}
	if len(active) > 0 {
		m["plan"] = strings.Join(active, "、")
	}
	switch {
	case nextExpiry > 0:
		m["expiry"] = fmtUnix(nextExpiry)
	case neverExpires || len(active) > 0:
		m["expiry"] = "不过期"
	}
	return m
}

func renderManualTelegramNotification(title, content string) string {
	var b strings.Builder
	b.WriteString("🔔 <b>")
	b.WriteString(telegram.Escape(strings.TrimSpace(title)))
	b.WriteString("</b>")
	if content = strings.TrimSpace(content); content != "" {
		b.WriteString("\n\n")
		b.WriteString(telegram.Escape(content))
	}
	return b.String()
}

func renderManualEmailNotification(siteName, title, content string) (string, string) {
	siteName = strings.TrimSpace(siteName)
	if siteName == "" {
		siteName = "轻舟"
	}
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)
	subject := title + " - " + siteName
	var b strings.Builder
	b.WriteString(`<div style="font-family:system-ui,sans-serif;max-width:560px;margin:0 auto">`)
	b.WriteString("<h2>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</h2>")
	if content != "" {
		b.WriteString(`<p style="white-space:pre-wrap;line-height:1.7">`)
		b.WriteString(html.EscapeString(content))
		b.WriteString("</p>")
	}
	b.WriteString(`<p style="color:#6b7280;font-size:13px">这是来自「`)
	b.WriteString(html.EscapeString(siteName))
	b.WriteString(`」的通知邮件。</p></div>`)
	return subject, b.String()
}

func truncateManualNotificationError(s string) string {
	const max = 300
	r := []rune(strings.TrimSpace(s))
	if len(r) > max {
		r = r[:max]
	}
	return string(r)
}

// GET /api/admin/manual-notifications
func (a *API) handleAdminListManualNotifications(w http.ResponseWriter, r *http.Request) {
	list, err := a.st.ListManualNotifications(100)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取发送历史失败")
		return
	}
	ok(w, list)
}

// GET /api/admin/manual-notifications/{id}
func (a *API) handleAdminManualNotificationDetail(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if id <= 0 {
		fail(w, http.StatusBadRequest, "无效的通知 ID")
		return
	}
	notification, err := a.st.ManualNotificationByID(id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取发送历史失败")
		return
	}
	if notification == nil {
		fail(w, http.StatusNotFound, "通知不存在")
		return
	}
	recipients, err := a.st.ListManualNotificationRecipients(id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取发送明细失败")
		return
	}
	ok(w, J{"notification": notification, "recipients": recipients})
}
