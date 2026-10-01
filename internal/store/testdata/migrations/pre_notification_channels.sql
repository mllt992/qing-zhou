-- Telegram-only broadcast schema before channel became part of the recipient PK.
CREATE TABLE manual_notifications (
 id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, content TEXT NOT NULL DEFAULT '',
 target_type TEXT NOT NULL, created_by INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL
);
CREATE TABLE manual_notification_recipients (
 notification_id INTEGER NOT NULL, user_id INTEGER NOT NULL, username TEXT NOT NULL DEFAULT '',
 chat_id INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'pending',
 error TEXT NOT NULL DEFAULT '', sent_at INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(notification_id,user_id)
);
INSERT INTO manual_notifications VALUES(7,'Historical notice','keep this content','selected',1,1);
INSERT INTO manual_notification_recipients VALUES(7,42,'legacy',123,'sent','',11);
