package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/store"
)

func TestDeliverManualNotificationRecordsSentFailedAndSkipped(t *testing.T) {
	a, st, _ := newTelegramAPI(t)
	sentUser, _ := st.CreateUser(store.NewUser{Username: "manual-sent", PasswordHash: "x"})
	failedUser, _ := st.CreateUser(store.NewUser{Username: "manual-failed", PasswordHash: "x"})
	unboundUser, _ := st.CreateUser(store.NewUser{Username: "manual-unbound", PasswordHash: "x"})
	if err := st.BindTelegram(sentUser, 301, 3001, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.BindTelegram(failedUser, 302, 3002, "", ""); err != nil {
		t.Fatal(err)
	}
	n, err := st.CreateManualNotification("通知 <标题>", "正文 & 内容", "selected", []int64{sentUser, failedUser, unboundUser}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var messages []string
	a.tgSendFn = func(chatID int64, html string) error {
		messages = append(messages, html)
		if chatID == 3002 {
			return errors.New("blocked by user")
		}
		return nil
	}
	a.deliverManualNotification(n.ID)

	got, err := st.ManualNotificationByID(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sent != 1 || got.Failed != 1 || got.Skipped != 1 || got.Pending != 0 {
		t.Fatalf("counts = %+v", got)
	}
	if len(messages) != 2 || !strings.Contains(messages[0], "&lt;标题&gt;") || !strings.Contains(messages[0], "&amp;") {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestDeliverManualEmailNotificationRecordsSentFailedAndSkipped(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting(businessEmailSetting, "1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("smtp_host", "smtp.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("site_name", "轻舟测试"); err != nil {
		t.Fatal(err)
	}
	sentUser, _ := st.CreateUser(store.NewUser{Username: "mail-sent", Email: "sent@example.com", PasswordHash: "x"})
	failedUser, _ := st.CreateUser(store.NewUser{Username: "mail-failed", Email: "failed@example.com", PasswordHash: "x"})
	unboundUser, _ := st.CreateUser(store.NewUser{Username: "mail-unbound", PasswordHash: "x"})
	n, err := st.CreateManualNotificationWithChannel("通知 <标题>", "正文 & 内容", "selected", store.ManualNotifyEmail, []int64{sentUser, failedUser, unboundUser}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	a.mailSendFn = func(to []string, subject, htmlBody string) error {
		sent = append(sent, strings.Join(to, ","))
		if to[0] == "failed@example.com" {
			return errors.New("smtp rejected")
		}
		if !strings.Contains(subject, "通知 <标题>") || !strings.Contains(htmlBody, "&lt;标题&gt;") || !strings.Contains(htmlBody, "&amp;") {
			t.Fatalf("email payload subject=%q html=%q", subject, htmlBody)
		}
		return nil
	}
	a.deliverManualNotification(n.ID)

	got, err := st.ManualNotificationByID(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sent != 1 || got.Failed != 1 || got.Skipped != 1 || got.Pending != 0 {
		t.Fatalf("counts = %+v", got)
	}
	if len(sent) != 2 {
		t.Fatalf("sent addresses = %#v", sent)
	}
}

func TestCreateManualNotificationRequiresConfiguredChannel(t *testing.T) {
	a, st, _ := newTelegramAPI(t)
	uid, _ := st.CreateUser(store.NewUser{Username: "need-mail", Email: "need@example.com", PasswordHash: "x"})
	body := `{"title":"t","content":"c","target_type":"selected","channel":"email","user_ids":[` + itoa(uid) + `]}`
	w := httptest.NewRecorder()
	a.handleAdminCreateManualNotification(w, httptest.NewRequest(http.MethodPost, "/api/admin/manual-notifications", strings.NewReader(body)))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "尚未配置邮件服务") {
		t.Fatalf("email without SMTP: status=%d body=%s", w.Code, w.Body.String())
	}

	if err := st.SetSetting(businessEmailSetting, "1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("smtp_host", "smtp.example.com"); err != nil {
		t.Fatal(err)
	}
	a.mailSendFn = func([]string, string, string) error { return nil }
	w = httptest.NewRecorder()
	a.handleAdminCreateManualNotification(w, httptest.NewRequest(http.MethodPost, "/api/admin/manual-notifications", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("email with SMTP: status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data store.ManualNotification `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Channel != store.ManualNotifyEmail || envelope.Data.Total != 1 {
		t.Fatalf("created = %+v", envelope.Data)
	}
}

func TestCreateManualNotificationRejectsInvalidChannel(t *testing.T) {
	a, _ := newUserEditAPI(t)
	w := httptest.NewRecorder()
	a.handleAdminCreateManualNotification(w, httptest.NewRequest(http.MethodPost, "/api/admin/manual-notifications",
		strings.NewReader(`{"title":"t","content":"c","target_type":"all","channel":"sms"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDeliverManualEmailNotificationSubstitutesRecipientVars(t *testing.T) {
	a, st := newUserEditAPI(t)
	if err := st.SetSetting(businessEmailSetting, "1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("smtp_host", "smtp.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("site_name", "轻舟测试"); err != nil {
		t.Fatal(err)
	}
	alice, _ := st.CreateUser(store.NewUser{Username: "alice", Email: "alice@example.com", PasswordHash: "x", Points: 12})
	bob, _ := st.CreateUser(store.NewUser{Username: "bob", Email: "bob@example.com", PasswordHash: "x"})
	expiry := time.Now().Add(48 * time.Hour).Unix()
	insertPlan(t, st, alice, "月付 100G", 100<<30, 20<<30, expiry)

	n, err := st.CreateManualNotificationWithChannel(
		"你好 {{username}}",
		"套餐 {{plan}} 剩余 {{remaining}}，积分 {{points}}。未知 {{missing}}。",
		"selected", store.ManualNotifyEmail, []int64{alice, bob}, 1)
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string]string{}
	a.mailSendFn = func(to []string, subject, htmlBody string) error {
		bodies[to[0]] = subject + "\n" + htmlBody
		return nil
	}
	a.deliverManualNotification(n.ID)
	aliceBody := bodies["alice@example.com"]
	if aliceBody == "" || !strings.Contains(aliceBody, "你好 alice") || !strings.Contains(aliceBody, "月付 100G") || !strings.Contains(aliceBody, "积分 12") {
		t.Fatalf("alice body = %s", aliceBody)
	}
	if strings.Contains(aliceBody, "{{username}}") || strings.Contains(aliceBody, "{{plan}}") {
		t.Fatalf("alice placeholders survived: %s", aliceBody)
	}
	if !strings.Contains(aliceBody, "{{missing}}") {
		t.Fatalf("unknown placeholder should remain: %s", aliceBody)
	}
	bobBody := bodies["bob@example.com"]
	if !strings.Contains(bobBody, "你好 bob") || !strings.Contains(bobBody, "套餐 —") || !strings.Contains(bobBody, "积分 0") {
		t.Fatalf("bob body = %s", bobBody)
	}
}

func TestDeliverManualTelegramNotificationSubstitutesRecipientVars(t *testing.T) {
	a, st, _ := newTelegramAPI(t)
	uid, _ := st.CreateUser(store.NewUser{Username: "carol", PasswordHash: "x"})
	if err := st.BindTelegram(uid, 401, 4001, "", ""); err != nil {
		t.Fatal(err)
	}
	n, err := st.CreateManualNotification("给 {{username}}", "站点 {{site}}", "selected", []int64{uid}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var html string
	a.tgSendFn = func(chatID int64, body string) error {
		html = body
		return nil
	}
	a.deliverManualNotification(n.ID)
	if !strings.Contains(html, "给 carol") || !strings.Contains(html, "站点 轻舟") {
		t.Fatalf("telegram body = %q", html)
	}
	if strings.Contains(html, "{{username}}") || strings.Contains(html, "{{site}}") {
		t.Fatalf("placeholders survived: %q", html)
	}
}

func TestManualNotifyVarsOmitSubscriptionURL(t *testing.T) {
	for _, v := range manualNotifyVarSpecs {
		if strings.Contains(v.Key, "url") || strings.Contains(v.Key, "sub") {
			t.Fatalf("broadcast vars must not include credentials: %+v", v)
		}
	}
}

func TestHandleAdminManualNotificationVars(t *testing.T) {
	a, _ := newUserEditAPI(t)
	w := httptest.NewRecorder()
	a.handleAdminManualNotificationVars(w, httptest.NewRequest(http.MethodGet, "/api/admin/manual-notifications/vars", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"username"`) || !strings.Contains(w.Body.String(), `"remaining"`) {
		t.Fatalf("vars = %s", w.Body.String())
	}
}

func TestRenderManualEmailNotificationEscapesHTML(t *testing.T) {
	subject, body := renderManualEmailNotification("轻舟", `通知 <标题>`, "正文 & 内容")
	if !strings.Contains(subject, "通知 <标题>") {
		t.Fatalf("subject = %q", subject)
	}
	if strings.Contains(body, "<标题>") || strings.Contains(body, "正文 & 内容") {
		t.Fatalf("unescaped html: %s", body)
	}
	if !strings.Contains(body, "&lt;标题&gt;") || !strings.Contains(body, "&amp;") {
		t.Fatalf("escaped html missing: %s", body)
	}
}
