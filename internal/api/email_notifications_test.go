package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qingzhou/internal/store"
)

func emailReminderFixture(t *testing.T) (*API, *store.Store, int64, *[]string) {
	t.Helper()
	a, st := newUserEditAPI(t)
	uid, err := st.CreateUser(store.NewUser{Username: "mail-reminder", Email: "reminder@example.com", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetEmailVerified(uid); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"smtp_host": "smtp.example.com", businessEmailSetting: "1", "public_base": "https://panel.example"} {
		if err := st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	inbox := &[]string{}
	a.mailSendFn = func(to []string, subject, body string) error { *inbox = append(*inbox, subject+"\n"+body); return nil }
	return a, st, uid, inbox
}

func TestEmailRemindersIndependentChannelsAndDedup(t *testing.T) {
	a, st, uid, inbox := emailReminderFixture(t)
	st.SetSetting("telegram_bot_token", "TEST:token")
	st.BindTelegram(uid, 12, 12, "", "")
	tg := 0
	a.tgSendFn = func(int64, string) error { tg++; return nil }
	insertPlan(t, st, uid, "<月付>", 100<<30, 90<<30, time.Now().Unix()+2*86400)
	a.sweepTelegramNotifies()
	a.sweepEmailNotifies()
	a.sweepEmailNotifies()
	a.sweepTelegramNotifies()
	if tg != 2 || len(*inbox) != 2 {
		t.Fatalf("tg=%d mail=%d, want expiry+traffic once per channel", tg, len(*inbox))
	}
	if !strings.Contains((*inbox)[0], "&lt;月付&gt;") || strings.Contains((*inbox)[0], "<月付>") {
		t.Fatalf("unsafe template: %s", (*inbox)[0])
	}
}

func TestEmailRemindersGatesAreQuiet(t *testing.T) {
	for _, gate := range []string{"default-off", "smtp-missing", "muted", "unverified", "banned", "missing-email"} {
		t.Run(gate, func(t *testing.T) {
			a, st, uid, inbox := emailReminderFixture(t)
			insertPlan(t, st, uid, "月付", 100<<30, 90<<30, time.Now().Unix()+86400)
			switch gate {
			case "default-off":
				st.DB().Exec(`DELETE FROM settings WHERE key=?`, businessEmailSetting)
			case "smtp-missing":
				st.SetSetting("smtp_host", "")
			case "muted":
				st.SetBusinessEmailEnabled(uid, false)
			case "unverified":
				st.DB().Exec(`UPDATE users SET email_verified=0 WHERE id=?`, uid)
			case "banned":
				st.DB().Exec(`UPDATE users SET status='banned' WHERE id=?`, uid)
			case "missing-email":
				st.DB().Exec(`UPDATE users SET email=NULL WHERE id=?`, uid)
			}
			a.sweepEmailNotifies()
			if len(*inbox) != 0 {
				t.Fatal("muted/unconfigured recipient received mail")
			}
			var claims int
			st.DB().QueryRow(`SELECT COUNT(*) FROM user_notify_log`).Scan(&claims)
			if claims != 0 {
				t.Fatal("quiet skip claimed notification")
			}
		})
	}
}

func TestEmailReminderFailureRetriesAndMutedRecoveryRearms(t *testing.T) {
	a, st, uid, inbox := emailReminderFixture(t)
	insertPlan(t, st, uid, "流量", 100<<30, 90<<30, time.Now().Unix()+30*86400)
	send := a.mailSendFn
	a.mailSendFn = func([]string, string, string) error { return errors.New("smtp down") }
	a.sweepEmailNotifies()
	a.mailSendFn = send
	a.sweepEmailNotifies()
	a.sweepEmailNotifies()
	if len(*inbox) != 1 {
		t.Fatalf("retry/dedup mails=%d", len(*inbox))
	}
	st.SetBusinessEmailEnabled(uid, false)
	st.DB().Exec(`UPDATE user_plans SET used_up=? WHERE user_id=?`, 10<<30, uid)
	a.sweepEmailNotifies()
	st.DB().Exec(`UPDATE user_plans SET used_up=? WHERE user_id=?`, 95<<30, uid)
	a.sweepEmailNotifies()
	st.SetBusinessEmailEnabled(uid, true)
	a.sweepEmailNotifies()
	if len(*inbox) != 2 {
		t.Fatalf("recovered/rearmed mails=%d", len(*inbox))
	}
}

func TestEmailReminderPrefsOnlyChangeCurrentUser(t *testing.T) {
	a, st, uid, _ := emailReminderFixture(t)
	other, _ := st.CreateUser(store.NewUser{Username: "other", PasswordHash: "x"})
	request := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.handleEmailNotifyPrefs(w, asUser(uid, httptest.NewRequest(http.MethodPut, "/api/user/notifications/email", strings.NewReader(body))))
		return w
	}
	if w := request(`{"enabled":false,"user_id":` + itoa(other) + `}`); w.Code != 400 {
		t.Fatal("cross-user body accepted")
	}
	if w := request(`{"enabled":false}`); w.Code != 200 {
		t.Fatalf("prefs: %d %s", w.Code, w.Body.String())
	}
	enabled, _ := st.BusinessEmailEnabled(uid)
	otherEnabled, _ := st.BusinessEmailEnabled(other)
	if enabled || !otherEnabled {
		t.Fatal("preference isolation failed")
	}
}

func TestManualBusinessEmailHonorsGlobalAndUserSwitches(t *testing.T) {
	a, st, uid, inbox := emailReminderFixture(t)
	for _, gate := range []string{"global", "user"} {
		st.SetSetting(businessEmailSetting, "1")
		st.SetBusinessEmailEnabled(uid, true)
		n, err := st.CreateManualNotificationWithChannel("业务通知", "到期提醒", "selected", store.ManualNotifyEmail, []int64{uid}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if gate == "global" {
			st.SetSetting(businessEmailSetting, "0")
		} else {
			st.SetBusinessEmailEnabled(uid, false)
		}
		a.deliverManualNotification(n.ID)
		got, _ := st.ManualNotificationByID(n.ID)
		if got.Skipped != 1 || len(*inbox) != 0 {
			t.Fatalf("manual bypassed %s switch: %+v", gate, got)
		}
	}
}

func TestBusinessEmailDisabledDoesNotBlockPasswordReset(t *testing.T) {
	a, st, uid, _ := emailReminderFixture(t)
	// A closed local port fails promptly; creating the reset token/response is
	// independent of the business switch and still invokes the system-mail path.
	st.SetSetting("smtp_host", "127.0.0.1")
	st.SetSetting("smtp_port", "1")
	st.SetSetting(businessEmailSetting, "0")
	st.SetBusinessEmailEnabled(uid, false)
	w := httptest.NewRecorder()
	a.handleForgot(w, httptest.NewRequest(http.MethodPost, "/api/auth/forgot", strings.NewReader(`{"email":"reminder@example.com"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("system reset blocked: %d %s", w.Code, w.Body.String())
	}
	var tokens int
	st.DB().QueryRow(`SELECT COUNT(*) FROM email_tokens WHERE user_id=? AND purpose='reset'`, uid).Scan(&tokens)
	if tokens != 1 {
		t.Fatal("system reset did not create its email token")
	}
}
