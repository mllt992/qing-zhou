package store

import (
	"path/filepath"
	"testing"
)

func newManualNotificationStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "manual-notifications.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestCreateManualNotificationSnapshotsAllActiveUsers(t *testing.T) {
	st := newManualNotificationStore(t)
	bound, _ := st.CreateUser(NewUser{Username: "bound", PasswordHash: "x"})
	unbound, _ := st.CreateUser(NewUser{Username: "unbound", PasswordHash: "x"})
	disabled, _ := st.CreateUser(NewUser{Username: "disabled", PasswordHash: "x"})
	_, _ = st.DB().Exec(`UPDATE users SET status='banned' WHERE id=?`, disabled)
	if err := st.BindTelegram(bound, 11, 111, "bound", ""); err != nil {
		t.Fatal(err)
	}

	n, err := st.CreateManualNotification("标题", "正文", "all", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n.Channel != ManualNotifyTelegram {
		t.Fatalf("channel = %q", n.Channel)
	}
	if n.Total != 2 || n.Pending != 1 || n.Skipped != 1 {
		t.Fatalf("counts = total %d pending %d skipped %d", n.Total, n.Pending, n.Skipped)
	}
	recipients, err := st.ListManualNotificationRecipients(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 2 {
		t.Fatalf("recipients = %d", len(recipients))
	}
	for _, recipient := range recipients {
		if recipient.Channel != ManualNotifyTelegram {
			t.Fatalf("legacy telegram row channel = %+v", recipient)
		}
		if recipient.UserID == unbound && (recipient.Status != "skipped" || recipient.Error != "未绑定 Telegram") {
			t.Fatalf("unbound = %+v", recipient)
		}
	}
}

func TestCreateManualNotificationEmailSnapshotsBoundAddresses(t *testing.T) {
	st := newManualNotificationStore(t)
	mailed, _ := st.CreateUser(NewUser{Username: "mailed", Email: "user@example.com", PasswordHash: "x"})
	blank, _ := st.CreateUser(NewUser{Username: "blank", PasswordHash: "x"})
	_, _ = mailed, blank

	n, err := st.CreateManualNotificationWithChannel("标题", "正文", "all", ManualNotifyEmail, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n.Channel != ManualNotifyEmail || n.Total != 2 || n.Pending != 1 || n.Skipped != 1 {
		t.Fatalf("counts = %+v", n)
	}
	recipients, err := st.ListManualNotificationRecipients(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 2 {
		t.Fatalf("recipients = %d", len(recipients))
	}
	for _, recipient := range recipients {
		if recipient.Channel != ManualNotifyEmail {
			t.Fatalf("channel = %+v", recipient)
		}
		if recipient.UserID == mailed && (recipient.Status != "pending" || recipient.Email != "user@example.com") {
			t.Fatalf("mailed = %+v", recipient)
		}
		if recipient.UserID == blank && (recipient.Status != "skipped" || recipient.Error != "未绑定邮箱") {
			t.Fatalf("blank = %+v", recipient)
		}
	}
}

func TestCreateManualNotificationBothWritesOneRowPerChannel(t *testing.T) {
	st := newManualNotificationStore(t)
	uid, _ := st.CreateUser(NewUser{Username: "both", Email: "both@example.com", PasswordHash: "x"})
	if err := st.BindTelegram(uid, 44, 444, "", ""); err != nil {
		t.Fatal(err)
	}
	n, err := st.CreateManualNotificationWithChannel("标题", "正文", "selected", ManualNotifyBoth, []int64{uid}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n.Total != 2 || n.Pending != 2 {
		t.Fatalf("counts = %+v", n)
	}
	recipients, _ := st.ListManualNotificationRecipients(n.ID)
	if len(recipients) != 2 {
		t.Fatalf("recipients = %+v", recipients)
	}
	seen := map[string]bool{}
	for _, recipient := range recipients {
		seen[recipient.Channel] = true
		if recipient.Status != "pending" {
			t.Fatalf("recipient = %+v", recipient)
		}
	}
	if !seen[ManualNotifyTelegram] || !seen[ManualNotifyEmail] {
		t.Fatalf("channels = %+v", recipients)
	}
}

func TestManualNotificationRecoveryDoesNotRetrySending(t *testing.T) {
	st := newManualNotificationStore(t)
	uid, _ := st.CreateUser(NewUser{Username: "interrupted", PasswordHash: "x"})
	if err := st.BindTelegram(uid, 33, 333, "", ""); err != nil {
		t.Fatal(err)
	}
	n, err := st.CreateManualNotification("标题", "正文", "selected", []int64{uid}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := st.ClaimManualNotificationRecipient(n.ID); err != nil || claimed == nil {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := st.FailInterruptedManualNotifications(); err != nil {
		t.Fatal(err)
	}
	recipients, _ := st.ListManualNotificationRecipients(n.ID)
	if len(recipients) != 1 || recipients[0].Status != "failed" || recipients[0].Error == "" {
		t.Fatalf("recovered recipient = %+v", recipients)
	}
	ids, err := st.ListPendingManualNotificationIDs()
	if err != nil || len(ids) != 0 {
		t.Fatalf("pending ids = %v, %v", ids, err)
	}
}

func TestManualNotificationHistoryKeepsDeliveryResults(t *testing.T) {
	st := newManualNotificationStore(t)
	uid, _ := st.CreateUser(NewUser{Username: "target", PasswordHash: "x"})
	if err := st.BindTelegram(uid, 22, 222, "target", ""); err != nil {
		t.Fatal(err)
	}
	n, err := st.CreateManualNotification("标题", "正文", "selected", []int64{uid}, 1)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimManualNotificationRecipient(n.ID)
	if err != nil || claimed == nil || claimed.UserID != uid {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := st.SetManualNotificationRecipientResult(n.ID, uid, "sent", ""); err != nil {
		t.Fatal(err)
	}
	got, err := st.ManualNotificationByID(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sent != 1 || got.Pending != 0 || got.Failed != 0 {
		t.Fatalf("history = %+v", got)
	}
	recipients, _ := st.ListManualNotificationRecipients(n.ID)
	if len(recipients) != 1 || recipients[0].Status != "sent" || recipients[0].SentAt == 0 {
		t.Fatalf("recipient = %+v", recipients)
	}
}

func TestMigrateManualNotificationChannelsRebuildsLegacyPrimaryKey(t *testing.T) {
	st := openMigrated(t)
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS manual_notification_recipients`,
		`DROP TABLE IF EXISTS manual_notifications`,
		`CREATE TABLE manual_notifications (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '',
			target_type TEXT NOT NULL,
			created_by INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE manual_notification_recipients (
			notification_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			username TEXT NOT NULL DEFAULT '',
			chat_id INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending',
			error TEXT NOT NULL DEFAULT '',
			sent_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (notification_id, user_id)
		)`,
		`INSERT INTO manual_notifications (id,title,content,target_type,created_by,created_at) VALUES (7,'旧通知','正文','selected',1,1)`,
		`INSERT INTO manual_notification_recipients (notification_id,user_id,username,chat_id,status,error,sent_at) VALUES (7,9,'old',123,'sent','',11)`,
	} {
		if _, err := st.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	rewindVersionedBaseline(t, st)
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate legacy manual notifications: %v", err)
	}
	got, err := st.ManualNotificationByID(7)
	if err != nil || got == nil {
		t.Fatalf("legacy notification lost: %v %v", got, err)
	}
	if got.Channel != ManualNotifyTelegram || got.Sent != 1 {
		t.Fatalf("legacy notification = %+v", got)
	}
	recipients, err := st.ListManualNotificationRecipients(7)
	if err != nil || len(recipients) != 1 {
		t.Fatalf("legacy recipients = %+v %v", recipients, err)
	}
	if recipients[0].Channel != ManualNotifyTelegram || recipients[0].UserID != 9 || recipients[0].Status != "sent" {
		t.Fatalf("legacy recipient = %+v", recipients[0])
	}

	uid, _ := st.CreateUser(NewUser{Username: "after-upgrade", Email: "after@example.com", PasswordHash: "x"})
	n, err := st.CreateManualNotificationWithChannel("新通知", "", "selected", ManualNotifyBoth, []int64{uid}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n.Total != 2 {
		t.Fatalf("post-upgrade both-channel total = %d", n.Total)
	}
}

func TestClaimManualNotificationRecipientSeparatesChannels(t *testing.T) {
	st := newManualNotificationStore(t)
	uid, _ := st.CreateUser(NewUser{Username: "split", Email: "split@example.com", PasswordHash: "x"})
	if err := st.BindTelegram(uid, 55, 555, "", ""); err != nil {
		t.Fatal(err)
	}
	n, err := st.CreateManualNotificationWithChannel("标题", "", "selected", ManualNotifyBoth, []int64{uid}, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.ClaimManualNotificationRecipient(n.ID)
	if err != nil || first == nil {
		t.Fatalf("first claim = %+v, %v", first, err)
	}
	second, err := st.ClaimManualNotificationRecipient(n.ID)
	if err != nil || second == nil {
		t.Fatalf("second claim = %+v, %v", second, err)
	}
	if first.Channel == second.Channel {
		t.Fatalf("claimed the same channel twice: %+v %+v", first, second)
	}
	if err := st.SetManualNotificationRecipientChannelResult(n.ID, uid, first.Channel, "sent", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetManualNotificationRecipientChannelResult(n.ID, uid, second.Channel, "failed", "smtp down"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.ManualNotificationByID(n.ID)
	if got.Sent != 1 || got.Failed != 1 || got.Pending != 0 {
		t.Fatalf("history = %+v", got)
	}
}
