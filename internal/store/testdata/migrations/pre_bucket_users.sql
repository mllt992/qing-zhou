-- Pre-bucket schema with legacy sui_* names and no proxy/exemption columns.
CREATE TABLE IF NOT EXISTS users (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  username        TEXT    NOT NULL UNIQUE,
  email           TEXT    UNIQUE,
  password_hash   TEXT    NOT NULL,
  role            TEXT    NOT NULL DEFAULT 'user',
  status          TEXT    NOT NULL DEFAULT 'active',
  email_verified  INTEGER NOT NULL DEFAULT 0,
  points          INTEGER NOT NULL DEFAULT 0,
  sui_client_id     INTEGER,
  sui_client_name   TEXT,
  sui_client_uuid   TEXT,
  sui_client_secret TEXT,
  sub_token       TEXT    UNIQUE,
  current_plan_id INTEGER,
  traffic_limit   INTEGER NOT NULL DEFAULT 0,
  device_limit    INTEGER NOT NULL DEFAULT 3, -- unused; see device_addons below
  used_up         INTEGER NOT NULL DEFAULT 0,
  used_down       INTEGER NOT NULL DEFAULT 0,
  expiry_at       INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
INSERT INTO users(id,username,password_hash,sui_client_id,sui_client_name,sui_client_uuid,sui_client_secret,sub_token,traffic_limit,used_up,used_down,created_at,updated_at)
 VALUES(1,'legacy-provisioned','fixture-hash',42,'qz_legacy','fixture-uuid','fixture-secret','fixture-sub',1000,12,34,1,1);
INSERT INTO users(id,username,password_hash,sub_token,created_at,updated_at)
 VALUES(2,'never-provisioned','fixture-hash','fixture-sub-new',1,1);
