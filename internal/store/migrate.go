package store

import (
	"log"
	"strings"
	"time"
)

// schema is applied idempotently on every boot. Tables for later phases
// (packages, orders, nodes, groups, ...) are added in their own phases.
const schema = `
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- Versioned, one-shot data migrations. Fresh installs run the same migration
-- chain against an empty DB and record completion; upgrades transform the rows
-- already present. Runtime business code never branches on installation age.
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    TEXT PRIMARY KEY,
  applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  username        TEXT    NOT NULL UNIQUE,
  email           TEXT    UNIQUE,
  password_hash   TEXT    NOT NULL,
  role            TEXT    NOT NULL DEFAULT 'user',
  status          TEXT    NOT NULL DEFAULT 'active',
  email_verified  INTEGER NOT NULL DEFAULT 0,
  -- One-time compatibility bit for accounts already admitted before the email
  -- gate became enforceable (legacy paid/provisioned/invite/admin accounts).
  email_gate_exempt INTEGER NOT NULL DEFAULT 0,
  points          INTEGER NOT NULL DEFAULT 0,
  client_id     INTEGER,
  client_name   TEXT,
  client_uuid   TEXT,
  client_secret TEXT,
  sub_token       TEXT    UNIQUE,
  current_plan_id INTEGER,
  traffic_limit   INTEGER NOT NULL DEFAULT 0,
  device_limit    INTEGER NOT NULL DEFAULT 3, -- unused; see device_addons below
  used_up         INTEGER NOT NULL DEFAULT 0,
  used_down       INTEGER NOT NULL DEFAULT 0,
  expiry_at       INTEGER NOT NULL DEFAULT 0,
  -- When the user last rotated their node credentials. Backs the cooldown on
  -- that action: unlike swapping the subscription address (panel-only), it has
  -- to reach every node's config, so it is deliberately rate-limited. 0 = never.
  creds_reset_at  INTEGER NOT NULL DEFAULT 0,
  -- Account-level mixed (HTTP/SOCKS5) proxy credential: ONE login that works on
  -- every node the user is entitled to, and never changes when a node moves
  -- between groups or a plan is renewed. Minted per user at provision time;
  -- editable by the user. proxy_expires_at 0 = permanent. See proxyaccount.go.
  proxy_username  TEXT    NOT NULL DEFAULT '',
  proxy_password  TEXT    NOT NULL DEFAULT '',
  proxy_expires_at INTEGER NOT NULL DEFAULT 0,
  -- Free-form admin note about this account (who it belongs to, why it was
  -- comped, ...). Panel-side only: never rendered into sing-box config, never
  -- shown to the user themselves.
  remark          TEXT    NOT NULL DEFAULT '',
  -- Coarse operational telemetry for subscription support. Client is a bounded
  -- category, never the raw User-Agent; timestamps are throttled by the writer.
  sub_last_fetched_at INTEGER NOT NULL DEFAULT 0,
  sub_last_format  TEXT    NOT NULL DEFAULT '',
  sub_last_client  TEXT    NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS packages (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  type          TEXT    NOT NULL,            -- traffic | plan
  name          TEXT    NOT NULL,
  queue_key     TEXT    NOT NULL DEFAULT '', -- blank = this package's own renewal line
  description   TEXT    NOT NULL DEFAULT '',
  highlights    TEXT    NOT NULL DEFAULT '',   -- JSON array of selling-point bullets
  price_points  INTEGER NOT NULL DEFAULT 0,
  traffic_bytes INTEGER NOT NULL DEFAULT 0,
  device_add    INTEGER NOT NULL DEFAULT 0, -- unused; see device_addons below
  duration_days INTEGER NOT NULL DEFAULT 0,
  duration_options TEXT NOT NULL DEFAULT '', -- JSON array of selectable durations; '' = single duration
  stock         INTEGER NOT NULL DEFAULT -1, -- -1 = unlimited
  enabled       INTEGER NOT NULL DEFAULT 1,
  sort_order    INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS orders (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id          INTEGER NOT NULL,
  package_id       INTEGER NOT NULL,
  package_snapshot TEXT    NOT NULL DEFAULT '',
  price_points     INTEGER NOT NULL,
  status           TEXT    NOT NULL,
  created_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS point_transactions (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id       INTEGER NOT NULL,
  amount        INTEGER NOT NULL,            -- + credit, - debit
  type          TEXT    NOT NULL,            -- admin_recharge | purchase | signup_bonus | refund | adjust
  balance_after INTEGER NOT NULL,
  ref_id        INTEGER NOT NULL DEFAULT 0,  -- order id when type=purchase
  note          TEXT    NOT NULL DEFAULT '',
  operator_id   INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL
);

-- Legacy scaffolding for a per-account device cap that was never wired up: no
-- code reads this table, users.device_limit, or packages.device_add, and the
-- package API has rejected type='device' since the first release, so no row can
-- exist here outside a hand-edited DB. Kept (not dropped) because dropping a
-- column/table on SQLite means a table rebuild inside Migrate, and a mistake
-- there is a boot loop on a deployment whose only update path is the panel it
-- just killed — see the comment on the indexes const. Cost of keeping it: zero rows.
CREATE TABLE IF NOT EXISTS device_addons (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL,
  slots        INTEGER NOT NULL,
  order_id     INTEGER NOT NULL DEFAULT 0,
  purchased_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS email_tokens (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL,
  token      TEXT    NOT NULL UNIQUE,
  purpose    TEXT    NOT NULL,            -- verify | reset
  expires_at INTEGER NOT NULL,
  used       INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS nodes (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  type            TEXT    NOT NULL,          -- self_built | external
  name            TEXT    NOT NULL,
  protocol        TEXT    NOT NULL DEFAULT '',
  inbound_tag TEXT    NOT NULL DEFAULT '', -- self_built: matches sing-box inbound tag
  route_upstream_inbound_id INTEGER NOT NULL DEFAULT 0, -- self_built logical route override; 0 = inherit inbound chain
  route_upstream_broken INTEGER NOT NULL DEFAULT 0, -- selected landing was deleted; route stays fail-closed until acknowledged
  share_link      TEXT    NOT NULL DEFAULT '', -- external: raw share URI
  source_id       INTEGER NOT NULL DEFAULT 0,
  enabled         INTEGER NOT NULL DEFAULT 1,
  sort_order      INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS node_groups (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT    NOT NULL,
  description TEXT    NOT NULL DEFAULT '',
  is_ai       INTEGER NOT NULL DEFAULT 0,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS node_group_members (
  node_id  INTEGER NOT NULL,
  group_id INTEGER NOT NULL,
  PRIMARY KEY (node_id, group_id)
);

CREATE TABLE IF NOT EXISTS plan_groups (
  package_id INTEGER NOT NULL,
  group_id   INTEGER NOT NULL,
  PRIMARY KEY (package_id, group_id)
);

-- User groups gate WHO MAY BUY a package. Do not confuse with node_groups,
-- which gate WHICH NODES a bought package grants (users → packages here vs
-- packages → nodes there); the two are independent axes.
CREATE TABLE IF NOT EXISTS user_groups (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT    NOT NULL,
  description TEXT    NOT NULL DEFAULT '',
  sort_order  INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS user_group_members (
  user_id  INTEGER NOT NULL,
  group_id INTEGER NOT NULL,
  PRIMARY KEY (user_id, group_id)
);

-- package_user_groups restricts a package to the listed user groups. NO ROWS
-- for a package means "public": anyone may buy it. That keeps every package
-- that existed before this feature buyable after the upgrade.
CREATE TABLE IF NOT EXISTS package_user_groups (
  package_id INTEGER NOT NULL,
  group_id   INTEGER NOT NULL,
  PRIMARY KEY (package_id, group_id)
);

-- Registration codes may auto-join their redeemer into user groups.
CREATE TABLE IF NOT EXISTS reg_code_user_groups (
  code_id  INTEGER NOT NULL,
  group_id INTEGER NOT NULL,
  PRIMARY KEY (code_id, group_id)
);

CREATE TABLE IF NOT EXISTS reg_codes (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  code       TEXT    NOT NULL UNIQUE,
  max_uses   INTEGER NOT NULL DEFAULT 1,  -- 0 = unlimited
  used       INTEGER NOT NULL DEFAULT 0,
  enabled    INTEGER NOT NULL DEFAULT 1,
  note       TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);

-- who consumed a reg code (username/email snapshotted so it survives user delete)
CREATE TABLE IF NOT EXISTS reg_code_uses (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  code_id  INTEGER NOT NULL,
  user_id  INTEGER NOT NULL DEFAULT 0,
  username TEXT    NOT NULL DEFAULT '',
  email    TEXT    NOT NULL DEFAULT '',
  used_at  INTEGER NOT NULL
);

-- per-user node blocklist: a node_key present here is hidden from that user's
-- subscription output (only affects the owning user). node_key = subconv.NodeKey.
CREATE TABLE IF NOT EXISTS user_disabled_nodes (
  user_id  INTEGER NOT NULL,
  node_key TEXT    NOT NULL,
  PRIMARY KEY (user_id, node_key)
);

CREATE TABLE IF NOT EXISTS sessions (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL,
  jti        TEXT    NOT NULL UNIQUE,
  ip         TEXT    NOT NULL DEFAULT '',
  user_agent TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS announcements (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  title      TEXT    NOT NULL,
  content    TEXT    NOT NULL DEFAULT '',
  pinned     INTEGER NOT NULL DEFAULT 0,
  enabled    INTEGER NOT NULL DEFAULT 1,
  start_at   INTEGER NOT NULL DEFAULT 0,
  end_at     INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS announcement_reads (
  user_id         INTEGER NOT NULL,
  announcement_id INTEGER NOT NULL,
  read_at         INTEGER NOT NULL,
  PRIMARY KEY (user_id, announcement_id)
);

CREATE TABLE IF NOT EXISTS node_sources (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT    NOT NULL,
  url          TEXT    NOT NULL,
  -- Unused: the fetcher never reads it. subconv.ParseList sniffs the payload
  -- itself (base64 list / plain links / clash YAML), which is what a real
  -- airport subscription needs anyway — the same URL can switch format on the
  -- provider's side. Kept as an inert column for the reason device_addons is.
  type         TEXT    NOT NULL DEFAULT 'base64',
  enabled      INTEGER NOT NULL DEFAULT 1,
  last_fetched INTEGER NOT NULL DEFAULT 0,
  last_count   INTEGER NOT NULL DEFAULT 0,
  last_error   TEXT    NOT NULL DEFAULT '',
  group_ids    TEXT    NOT NULL DEFAULT '',   -- JSON array of group ids applied to imported nodes
  created_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS help_docs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  title      TEXT    NOT NULL,
  content    TEXT    NOT NULL DEFAULT '',   -- markdown
  sort_order INTEGER NOT NULL DEFAULT 0,
  published  INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- ===== Native sing-box management (B2: 轻舟 replaces sing-box) =====
-- TLS / Reality profiles attached to inbounds. server_json holds the sing-box
-- inbound "tls" block (with the Reality private_key) and is stored ENCRYPTED.
-- client_json holds the client-side params (sni/pbk/sid/alpn/fp) used to build
-- share links.
CREATE TABLE IF NOT EXISTS sb_tls (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT    NOT NULL,
  mode        TEXT    NOT NULL DEFAULT 'reality', -- reality | tls
  server_json TEXT    NOT NULL DEFAULT '',        -- encrypted sing-box tls block
  client_json TEXT    NOT NULL DEFAULT '',        -- client params for links
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

-- Managed certificates: a first-class, reusable resource. Unlike a PEM inlined
-- into one sb_tls row, one certificate is referenced by many sb_tls (mode=tls)
-- profiles via sb_tls.cert_id, so a renewal updates a SINGLE row and every
-- inbound that references it picks up the new cert on the next config build.
-- Issued on the PANEL HOST (DNS-01 needs no access to the node), so remote
-- nodes get real certs + auto-renew too. cert_pem/key_pem are stored ENCRYPTED.
CREATE TABLE IF NOT EXISTS certificates (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT    NOT NULL,
  domain        TEXT    NOT NULL DEFAULT '',        -- primary domain/SAN; used as SNI + for renewal
  source        TEXT    NOT NULL DEFAULT 'acme',     -- acme | paste | selfsigned
  acme_method   TEXT    NOT NULL DEFAULT '',         -- dns-cf | http-01 | webroot
  cert_pem      TEXT    NOT NULL DEFAULT '',         -- encrypted fullchain PEM
  key_pem       TEXT    NOT NULL DEFAULT '',         -- encrypted private key PEM
  not_after     INTEGER NOT NULL DEFAULT 0,          -- expiry (unix), parsed from cert_pem
  auto_renew    INTEGER NOT NULL DEFAULT 1,
  last_renew_at INTEGER NOT NULL DEFAULT 0,
  last_error    TEXT    NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

-- sing-box server inbounds owned by 轻舟. options holds extra inbound fields
-- (transport, congestion_control, masquerade, ...) as JSON. A self_built node's
-- inbound_tag links to sb_inbounds.tag, so grouping/subscription keep working.
CREATE TABLE IF NOT EXISTS sb_inbounds (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  type        TEXT    NOT NULL,                   -- vless | hysteria2 | tuic | trojan | vmess
  tag         TEXT    NOT NULL UNIQUE,
  listen      TEXT    NOT NULL DEFAULT '::',
  listen_port INTEGER NOT NULL,
  tls_id      INTEGER NOT NULL DEFAULT 0,         -- -> sb_tls.id (0 = none)
  options     TEXT    NOT NULL DEFAULT '{}',      -- extra inbound fields (JSON)
  enabled     INTEGER NOT NULL DEFAULT 1,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

-- Third-party proxy egresses (e.g. a purchased static-IP SOCKS5/HTTP proxy).
-- An inbound with egress_id != 0 routes its traffic out through this proxy
-- instead of exiting directly. password is encrypted at rest.
-- tls_* describe the hop TO the proxy ("HTTPS proxy"): there we are the TLS
-- CLIENT, so tls_cert_id names a trust anchor to verify the proxy against —
-- never a certificate we present. sing-box has no tls option on its socks
-- outbound, so tls_enabled is only valid with type='http'.
CREATE TABLE IF NOT EXISTS sb_egresses (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT    NOT NULL,
  type        TEXT    NOT NULL DEFAULT 'socks',     -- socks | http
  host        TEXT    NOT NULL,
  port        INTEGER NOT NULL,
  username    TEXT    NOT NULL DEFAULT '',
  password    TEXT    NOT NULL DEFAULT '',
  tls_enabled  INTEGER NOT NULL DEFAULT 0,
  sni          TEXT    NOT NULL DEFAULT '',
  tls_cert_id  INTEGER NOT NULL DEFAULT 0,          -- -> certificates.id (0 = system roots)
  tls_insecure INTEGER NOT NULL DEFAULT 0,
  udp_mode     TEXT    NOT NULL DEFAULT '',         -- '' = by type | passthrough | block
  connect_timeout_ms INTEGER NOT NULL DEFAULT 0,    -- 0 = built-in default
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

-- A "bucket" = an independently-metered unit a user holds: either a purchased
-- subscription plan or the shared traffic-package pool. Each bucket has its own
-- internal client_name so sing-box's per-identity stats give per-bucket usage;
-- UUID/secret authenticate the user and are stable across buckets. Replaces the
-- single users.current_plan_id/traffic_limit/expiry_at model so a user can hold
-- several plans that expire and run out independently.
--   kind='plan': package_id → plan_groups; has expiry. kind='pool': package_id=0,
--   no expiry, covers the union of the user's plan groups + free group, drained
--   last. traffic_limit 0 = no quota for every bucket kind.
CREATE TABLE IF NOT EXISTS user_plans (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id        INTEGER NOT NULL,
  kind           TEXT    NOT NULL DEFAULT 'plan',  -- plan | pool
  package_id     INTEGER NOT NULL DEFAULT 0,
  queue_key      TEXT    NOT NULL DEFAULT '', -- effective renewal-line snapshot
  name           TEXT    NOT NULL DEFAULT '',       -- snapshot, survives pkg delete
  client_name    TEXT    NOT NULL,                  -- sing-box stats identity (unique)
  client_uuid    TEXT    NOT NULL DEFAULT '',
  client_secret  TEXT    NOT NULL DEFAULT '',
  traffic_limit  INTEGER NOT NULL DEFAULT 0,
  used_up        INTEGER NOT NULL DEFAULT 0,
  used_down      INTEGER NOT NULL DEFAULT 0,
  expiry_at      INTEGER NOT NULL DEFAULT 0,        -- 0 = never
  last_online_at INTEGER NOT NULL DEFAULT 0,
  order_id       INTEGER NOT NULL DEFAULT 0,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);

-- One internal subscription/metering line per (user, package), NOT per purchase.
--
-- Each purchase gets its own user_plans row because metering is per份: every月
-- needs its own quota and counters. client_name and optional mixed-proxy fields
-- stay on the line so usage follows the active份. client_uuid/client_secret are
-- retained as migration source columns; users.client_* is runtime authority.
--
-- Splitting the line from the份 means a handoff is just two status changes.
CREATE TABLE IF NOT EXISTS plan_identities (
  user_id          INTEGER NOT NULL,
  package_id       INTEGER NOT NULL,
  client_name      TEXT    NOT NULL,
  client_uuid      TEXT    NOT NULL,
  client_secret    TEXT    NOT NULL,
  proxy_username   TEXT    NOT NULL DEFAULT '',
  proxy_password   TEXT    NOT NULL DEFAULT '',
  proxy_expires_at INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  PRIMARY KEY (user_id, package_id)
);

-- Protocol credentials accepted temporarily after a credential/model upgrade.
-- A fresh installation has no rows. They are deliberately separate from the
-- user's primary users.client_* pair, so compatibility expires cleanly instead
-- of becoming a permanent second identity model. source_name is needed to
-- reproduce credentials of pre-upgrade logical routes, whose old derivation
-- included the historical stats name.
CREATE TABLE IF NOT EXISTS user_credential_aliases (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id       INTEGER NOT NULL,
  source_name   TEXT    NOT NULL,
  client_uuid   TEXT    NOT NULL DEFAULT '',
  client_secret TEXT    NOT NULL DEFAULT '',
  valid_until   INTEGER NOT NULL,
  created_at    INTEGER NOT NULL,
  UNIQUE(user_id, source_name, client_uuid, client_secret)
);

-- Per-user traffic time-series, one row per stats poll that saw traffic. Feeds
-- the daily charts in the native sing-box era (sing-box kept its own stat table);
-- pruned to a rolling window. up/down are per-poll deltas, not cumulative.
CREATE TABLE IF NOT EXISTS traffic_samples (
  user_id INTEGER NOT NULL,
  ts      INTEGER NOT NULL,
  up      INTEGER NOT NULL DEFAULT 0,
  down    INTEGER NOT NULL DEFAULT 0
);

-- Per-server attribution of sing-box user traffic. The legacy traffic_samples
-- table intentionally remains the site-wide source of truth for billing; this
-- companion table only preserves where each successful stats poll came from so
-- operators can explain a device's physical traffic and estimate capacity.
-- It shares traffic_samples' rolling retention window.
CREATE TABLE IF NOT EXISTS server_user_traffic_samples (
  server_id INTEGER NOT NULL,
  user_id   INTEGER NOT NULL,
  ts        INTEGER NOT NULL,
  up        INTEGER NOT NULL DEFAULT 0,
  down      INTEGER NOT NULL DEFAULT 0
);

-- Per-day, per-bucket traffic rollup. Exists because traffic_samples answers
-- neither question the usage report asks: it carries no bucket, so "which
-- package did this traffic belong to" is unanswerable, and it is pruned to 35
-- days, so any longer range comes back empty.
--
-- Kept forever, unlike the samples. One row per user per bucket per active day
-- is tiny (1000 users × 2 buckets × 365 days ≈ 730k rows, tens of MB) and
-- append-mostly, so there is no bloat argument for dropping history that an
-- admin reconciling a year-old order would want.
--
-- day is the LOCAL calendar date (YYYY-MM-DD), matching what the existing
-- strftime(...,'localtime') daily queries produce, so both views agree on where
-- a day starts.
--
-- package_id is denormalised rather than joined through bucket_id: buckets are
-- deleted by mergeDuplicatePlanBuckets, and history whose grouping key vanishes
-- when an unrelated maintenance job runs is not history. 0 = the shared pool,
-- -1 = traffic recorded before this table existed (see backfillTrafficDaily) —
-- real bytes, but no attributable package, and the UI says so rather than
-- silently folding them into a package that did not earn them.
CREATE TABLE IF NOT EXISTS traffic_daily (
  day        TEXT    NOT NULL,
  user_id    INTEGER NOT NULL,
  bucket_id  INTEGER NOT NULL DEFAULT 0,
  package_id INTEGER NOT NULL DEFAULT 0,
  up         INTEGER NOT NULL DEFAULT 0,
  down       INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, user_id, bucket_id)
);

CREATE TABLE IF NOT EXISTS servers (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  name            TEXT    NOT NULL,
  host            TEXT    NOT NULL,
  port            INTEGER NOT NULL DEFAULT 22,
  ssh_user        TEXT    NOT NULL DEFAULT 'root',
  ssh_key         TEXT    NOT NULL DEFAULT '',
  ssh_key_pass    TEXT    NOT NULL DEFAULT '',
  config_path     TEXT    NOT NULL DEFAULT '/etc/sing-box/config.json',
  systemd_unit    TEXT    NOT NULL DEFAULT 'sing-box',
  v2ray_listen    TEXT    NOT NULL DEFAULT '127.0.0.1:18080',
  sing_box_bin    TEXT    NOT NULL DEFAULT '',
  enabled         INTEGER NOT NULL DEFAULT 1,
  status          TEXT    NOT NULL DEFAULT 'unknown',
  last_seen       INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  use_sudo        INTEGER NOT NULL DEFAULT 0,
  sudo_password   TEXT    NOT NULL DEFAULT '',
  ssh_key_path    TEXT    NOT NULL DEFAULT '',
  sort_order      INTEGER NOT NULL DEFAULT 0
);

-- ===== Monitor probe (轻舟探针) =====
-- Per-server system metrics time-series, one row per agent report.
-- Pruned to a rolling 35-day window (enough for a complete calendar month).
CREATE TABLE IF NOT EXISTS server_metrics (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  server_id       INTEGER NOT NULL,
  ts              INTEGER NOT NULL,
  probe_version   TEXT    NOT NULL DEFAULT '',
  cpu_percent     REAL    NOT NULL DEFAULT 0,
  mem_used        INTEGER NOT NULL DEFAULT 0,
  mem_total       INTEGER NOT NULL DEFAULT 0,
  swap_used       INTEGER NOT NULL DEFAULT 0,
  swap_total      INTEGER NOT NULL DEFAULT 0,
  disk_used       INTEGER NOT NULL DEFAULT 0,
  disk_total      INTEGER NOT NULL DEFAULT 0,
  net_rx          INTEGER NOT NULL DEFAULT 0,
  net_tx          INTEGER NOT NULL DEFAULT 0,
  -- Raw /proc/net/dev counters. Unlike net_rx/net_tx (instantaneous B/s),
  -- successive totals can be differenced into accurate interval usage.
  net_rx_total    INTEGER NOT NULL DEFAULT 0,
  net_tx_total    INTEGER NOT NULL DEFAULT 0,
  net_totals_valid INTEGER NOT NULL DEFAULT 0,
  net_rx_bytes    INTEGER NOT NULL DEFAULT 0,
  net_tx_bytes    INTEGER NOT NULL DEFAULT 0,
  load1           REAL    NOT NULL DEFAULT 0,
  load5           REAL    NOT NULL DEFAULT 0,
  load15          REAL    NOT NULL DEFAULT 0,
  tcp_connections INTEGER NOT NULL DEFAULT 0,
  process_count   INTEGER NOT NULL DEFAULT 0,
  uptime          INTEGER NOT NULL DEFAULT 0,
  hostname        TEXT    NOT NULL DEFAULT '',
  platform        TEXT    NOT NULL DEFAULT '',
  kernel          TEXT    NOT NULL DEFAULT '',
  arch            TEXT    NOT NULL DEFAULT ''
);

-- Manual provider-usage calibration for the current device billing cycle.
-- offset_bytes corrects the probe total under accounting_mode to the provider's
-- displayed total at calibrated_at. Cycle or mode mismatches make it inert, so
-- an old calibration can never leak into another billing convention/month.
CREATE TABLE IF NOT EXISTS server_traffic_calibrations (
  server_id     INTEGER PRIMARY KEY,
  cycle_start   INTEGER NOT NULL,
  accounting_mode TEXT NOT NULL DEFAULT 'sum',
  offset_bytes  INTEGER NOT NULL,
  calibrated_at INTEGER NOT NULL
);

-- What sing-box each node is actually running. A separate table rather than
-- extra columns on the servers table, for two reasons: the panel's own machine
-- runs sing-box too but has no servers row (it is server_id 0 everywhere else
-- in the code), and this is observed state that a failed probe must be able to
-- leave stale without touching the server's configuration.
CREATE TABLE IF NOT EXISTS node_singbox (
  server_id  INTEGER PRIMARY KEY,          -- 0 = 面板本机
  version    TEXT    NOT NULL DEFAULT '',  -- 解析出的版本号，如 1.13.18；空=测不出
  v2ray_api  INTEGER NOT NULL DEFAULT 0,   -- 是否带 with_v2ray_api（没有则流量统计失效）
  raw        TEXT    NOT NULL DEFAULT '',  -- sing-box version 的首行原文，解析失败时给人看
  checked_at INTEGER NOT NULL DEFAULT 0,
  error      TEXT    NOT NULL DEFAULT ''   -- 探测失败的原因；成功时清空
);

-- Server alerts: offline, high_cpu, high_mem, disk_full, expiring, expired.
-- One row per *episode*, not per observation: a condition that keeps holding
-- merges into its open row (ts/count refreshed) rather than adding a new one.
CREATE TABLE IF NOT EXISTS server_alerts (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  server_id INTEGER NOT NULL,
  type      TEXT    NOT NULL,
  message   TEXT    NOT NULL,
  ts        INTEGER NOT NULL,
  first_ts  INTEGER NOT NULL DEFAULT 0,
  hits      INTEGER NOT NULL DEFAULT 1,
  read      INTEGER NOT NULL DEFAULT 0,
  resolved  INTEGER NOT NULL DEFAULT 0
);

-- Durable delivery cursors for per-device Telegram alerts. cycle_key changes
-- when an expiry timestamp changes or a new traffic billing cycle starts.
CREATE TABLE IF NOT EXISTS device_notify_state (
  server_id     INTEGER NOT NULL,
  kind          TEXT    NOT NULL,
  cycle_key     TEXT    NOT NULL,
  sent_count    INTEGER NOT NULL DEFAULT 0,
  last_sent_day TEXT    NOT NULL DEFAULT '',
  updated_at    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (server_id, kind, cycle_key)
);

-- One Telegram account per panel user. telegram_id is unique so a single chat
-- cannot be bound to two accounts (and then pull the other user's subscription).
CREATE TABLE IF NOT EXISTS telegram_binds (
  user_id        INTEGER PRIMARY KEY,
  telegram_id    INTEGER NOT NULL UNIQUE,
  chat_id        INTEGER NOT NULL,
  username       TEXT    NOT NULL DEFAULT '',
  first_name     TEXT    NOT NULL DEFAULT '',
  notify_expiry  INTEGER NOT NULL DEFAULT 1,
  notify_traffic INTEGER NOT NULL DEFAULT 1,
  -- Operations alerts (node restart loops). Off by default and set by an admin
  -- only: this is not something a user may subscribe themselves to, and the
  -- message names nodes and failure counts.
  notify_ops     INTEGER NOT NULL DEFAULT 0,
  bound_at       INTEGER NOT NULL,
  last_chat_at   INTEGER NOT NULL DEFAULT 0
);

-- Short-lived one-time tokens for the panel → t.me/bot?start=TOKEN bind flow.
CREATE TABLE IF NOT EXISTS telegram_bind_tokens (
  token      TEXT    PRIMARY KEY,
  user_id    INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used       INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

-- Dedup log for user-facing notifications (Telegram today, email later).
-- (user, kind, subject) is claimed once; ClearNotify deletes the row so the
-- same condition can fire again after it recovers (traffic topped up, etc.).
CREATE TABLE IF NOT EXISTS user_notify_log (
  user_id  INTEGER NOT NULL,
  kind     TEXT    NOT NULL,
  subject  TEXT    NOT NULL,
  sent_at  INTEGER NOT NULL,
  PRIMARY KEY (user_id, kind, subject)
);

-- Admin-created Telegram broadcasts. The recipient table is a snapshot: it
-- preserves who was selected and why delivery did/did not happen even if the
-- user later binds Telegram, changes their name, or is deleted.
CREATE TABLE IF NOT EXISTS manual_notifications (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  title        TEXT    NOT NULL,
  content      TEXT    NOT NULL DEFAULT '',
  target_type  TEXT    NOT NULL, -- all | selected
  created_by   INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS manual_notification_recipients (
  notification_id INTEGER NOT NULL,
  user_id         INTEGER NOT NULL,
  username        TEXT    NOT NULL DEFAULT '',
  chat_id         INTEGER NOT NULL DEFAULT 0,
  status          TEXT    NOT NULL DEFAULT 'pending', -- pending | sending | sent | failed | skipped
  error           TEXT    NOT NULL DEFAULT '',
  sent_at         INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (notification_id, user_id)
);
`

// indexes is the index DDL, kept OUT of schema above and applied AFTER the
// additive ALTER TABLE block in Migrate — never before it.
//
// An index on a column that a later ALTER adds is the shape that took the panel
// down in v0.2.48: on an existing DB `CREATE TABLE IF NOT EXISTS users` is a
// no-op, so the column the new index named did not exist yet, the whole schema
// Exec failed on it, Migrate returned that error, and main log.Fatal'd — a boot
// loop on a deployment whose only update path is the panel it had just killed.
// Ordering the phases this way makes that shape unrepresentable: by the time
// anything here runs, every column the migration knows about is on its table.
const indexes = `
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_proxy_username ON users(proxy_username) WHERE proxy_username <> '';
CREATE INDEX IF NOT EXISTS idx_orders_user ON orders(user_id);
CREATE INDEX IF NOT EXISTS idx_ptx_user ON point_transactions(user_id);
CREATE INDEX IF NOT EXISTS idx_device_addons_user ON device_addons(user_id, expires_at);
CREATE INDEX IF NOT EXISTS idx_email_tokens_token ON email_tokens(token);
CREATE INDEX IF NOT EXISTS idx_nodes_enabled ON nodes(enabled);
CREATE INDEX IF NOT EXISTS idx_ngm_group ON node_group_members(group_id);
CREATE INDEX IF NOT EXISTS idx_ngm_node  ON node_group_members(node_id);
CREATE INDEX IF NOT EXISTS idx_plan_groups_pkg ON plan_groups(package_id);
CREATE INDEX IF NOT EXISTS idx_ugm_group ON user_group_members(group_id);
CREATE INDEX IF NOT EXISTS idx_ugm_user  ON user_group_members(user_id);
CREATE INDEX IF NOT EXISTS idx_pug_pkg   ON package_user_groups(package_id);
CREATE INDEX IF NOT EXISTS idx_pug_group ON package_user_groups(group_id);
CREATE INDEX IF NOT EXISTS idx_rcug_code ON reg_code_user_groups(code_id);
CREATE INDEX IF NOT EXISTS idx_rcu_code ON reg_code_uses(code_id);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_jti  ON sessions(jti);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_plans_client ON user_plans(client_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_plan_identities_client ON plan_identities(client_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_plan_identities_proxy ON plan_identities(proxy_username) WHERE proxy_username <> '';
CREATE INDEX IF NOT EXISTS idx_user_plans_user ON user_plans(user_id);
CREATE INDEX IF NOT EXISTS idx_user_plans_queue ON user_plans(user_id, queue_key, status, id);
CREATE INDEX IF NOT EXISTS idx_credential_aliases_user ON user_credential_aliases(user_id, valid_until);
CREATE INDEX IF NOT EXISTS idx_traffic_samples_user_ts ON traffic_samples(user_id, ts);
CREATE INDEX IF NOT EXISTS idx_traffic_samples_ts ON traffic_samples(ts);
CREATE INDEX IF NOT EXISTS idx_server_user_traffic_server_ts ON server_user_traffic_samples(server_id, ts);
CREATE INDEX IF NOT EXISTS idx_server_user_traffic_server_user_ts ON server_user_traffic_samples(server_id, user_id, ts);
CREATE INDEX IF NOT EXISTS idx_traffic_daily_user_day ON traffic_daily(user_id, day);
CREATE INDEX IF NOT EXISTS idx_traffic_daily_day ON traffic_daily(day);
CREATE INDEX IF NOT EXISTS idx_metrics_server_ts ON server_metrics(server_id, ts);
-- Standalone ts index: the composite (server_id, ts) can't serve queries that
-- filter on ts alone — the hourly PruneMetrics (WHERE ts<?) and the unauthenticated
-- heatmap/sparkline endpoints (ListAllMetricsSince, WHERE ts>=?) would otherwise
-- full-scan this, the fastest-growing table.
CREATE INDEX IF NOT EXISTS idx_metrics_ts ON server_metrics(ts);
CREATE INDEX IF NOT EXISTS idx_alerts_server ON server_alerts(server_id, ts);
CREATE INDEX IF NOT EXISTS idx_alerts_open ON server_alerts(server_id, type, read);
CREATE INDEX IF NOT EXISTS idx_tg_bind_tokens_user ON telegram_bind_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_tg_bind_tokens_exp ON telegram_bind_tokens(expires_at);
CREATE INDEX IF NOT EXISTS idx_notify_log_user ON user_notify_log(user_id);
CREATE INDEX IF NOT EXISTS idx_manual_notifications_created ON manual_notifications(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_manual_notify_recipients_status ON manual_notification_recipients(notification_id, status);
`

// Migrate brings the schema up to date in three ordered phases: tables, then
// additive columns, then indexes. The order is load-bearing — see the comment on
// indexes for the outage that fixed it in place — and nothing that references a
// column may run before the phase that adds it.
//
// migrateEmailGateExempt is separate because its backfill is security-sensitive:
// it must execute iff this invocation actually adds the column. The existence
// check and ALTER share one IMMEDIATE transaction, so another migrator cannot
// race between them; rollback removes both the column and classifications if the
// backfill fails.
func (s *Store) migrateEmailGateExempt() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('users') WHERE name='email_gate_exempt'`).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return tx.Commit()
	}
	if _, err := tx.Exec(`ALTER TABLE users ADD COLUMN email_gate_exempt INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE users SET email_gate_exempt=1 WHERE
		role='admin' OR client_id IS NOT NULL
		OR EXISTS (SELECT 1 FROM reg_code_uses r WHERE r.user_id=users.id)
		OR EXISTS (SELECT 1 FROM user_plans p
		           WHERE p.user_id=users.id AND p.kind='plan' AND p.package_id>=0)
		OR (current_plan_id IS NOT NULL AND current_plan_id>0)`); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateServerUseSudo adds servers.use_sudo and, in the same transaction that
// creates it, turns it on for every row whose SSH user is not root.
//
// It cannot live in the best-effort additive list below, for the same reason
// migrateEmailGateExempt cannot: those statements run on every boot, so the
// backfill would re-enable sudo on a row an admin had deliberately turned it off
// for, once per restart, forever.
//
// Backfilling to 1 rather than 0 is safe because a non-root row is already
// broken today: every deploy on it dies on mkdir /etc/sing-box or on systemctl.
// Turning sudo on can only move such a row from "fails" to "works".
func (s *Store) migrateServerUseSudo() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('servers') WHERE name='use_sudo'`).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return tx.Commit()
	}
	if _, err := tx.Exec(`ALTER TABLE servers ADD COLUMN use_sudo INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE servers SET use_sudo=1 WHERE ssh_user <> 'root' AND ssh_user <> ''`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// This migration carries a security-sensitive data backfill, so it cannot live
	// in the best-effort additive list below: those statements run on every boot.
	// Only the transaction that actually adds the column may classify existing
	// accounts; later purchases/provisioning must never be reclassified on restart.
	if err := s.migrateEmailGateExempt(); err != nil {
		return err
	}
	if err := s.migrateServerUseSudo(); err != nil {
		return err
	}
	// Additive column migrations for DBs created before these columns existed.
	// Errors (e.g. "duplicate column name") are expected on up-to-date DBs.
	for _, stmt := range []string{
		// Ops-alert recipient flag. Default 0, so upgrading never starts sending
		// node failure details to anyone who was merely bound for expiry notices.
		`ALTER TABLE telegram_binds ADD COLUMN notify_ops INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE announcements ADD COLUMN start_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE announcements ADD COLUMN end_at INTEGER NOT NULL DEFAULT 0`,
		// AI groups feed the subscription's guarded AI route. Existing groups stay
		// ordinary, so upgrading cannot reroute any user's traffic by surprise.
		`ALTER TABLE node_groups ADD COLUMN is_ai INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE node_sources ADD COLUMN group_ids TEXT NOT NULL DEFAULT ''`,
		// Shop selling-point bullets, stored as a JSON array of strings.
		`ALTER TABLE packages ADD COLUMN highlights TEXT NOT NULL DEFAULT ''`,
		// Renewal grouping: blank preserves the historical one-package-per-line
		// behaviour; user_plans snapshots the effective key when granted.
		`ALTER TABLE packages ADD COLUMN queue_key TEXT NOT NULL DEFAULT ''`,
		// Selectable durations (30/90/365天…), JSON array of {days,price_points,traffic_bytes}.
		// '' keeps the package single-duration, priced by its own columns — which is
		// exactly what every pre-existing row is.
		`ALTER TABLE packages ADD COLUMN duration_options TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sb_inbounds ADD COLUMN server_id INTEGER NOT NULL DEFAULT 0`,
		// Relay chaining: an inbound with upstream_inbound_id != 0 forwards its
		// traffic to that landing inbound instead of exiting directly. relay_secret
		// is a landing inbound's own auth secret (lazily generated) from which the
		// relay credential is derived.
		`ALTER TABLE sb_inbounds ADD COLUMN upstream_inbound_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sb_inbounds ADD COLUMN relay_secret TEXT NOT NULL DEFAULT ''`,
		// Third-party proxy egress: an inbound with egress_id != 0 exits through
		// that sb_egresses row (static-IP SOCKS5/HTTP proxy) instead of directly.
		`ALTER TABLE sb_inbounds ADD COLUMN egress_id INTEGER NOT NULL DEFAULT 0`,
		// Set when DeleteSbInbound un-chains this relay because its landing was
		// deleted, so 链路拓扑 can keep showing that the exit silently moved to this
		// machine. Cleared by any save of the inbound. See SbInbound.UpstreamBroken.
		`ALTER TABLE sb_inbounds ADD COLUMN upstream_broken INTEGER NOT NULL DEFAULT 0`,
		// A self-built node is the user-facing logical route. Several nodes may now
		// share one physical inbound and select different landing inbounds; 0 keeps
		// the legacy behaviour of inheriting the physical inbound's own chain.
		`ALTER TABLE nodes ADD COLUMN route_upstream_inbound_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE nodes ADD COLUMN route_upstream_broken INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE servers ADD COLUMN ssh_password TEXT NOT NULL DEFAULT ''`,
		// sudo password for accounts without NOPASSWD (encrypted at rest, like the
		// SSH password beside it), and the name of a key file in the panel's key
		// directory for rows that do not want the PEM pasted into the browser.
		// use_sudo is NOT here — it carries a one-time backfill, see
		// migrateServerUseSudo.
		`ALTER TABLE servers ADD COLUMN sudo_password TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE servers ADD COLUMN ssh_key_path TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE servers ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sb_tls ADD COLUMN server_id INTEGER NOT NULL DEFAULT 0`,
		// Certificate center: a mode=tls profile references a managed certificate
		// by id instead of inlining its PEM, so one cert serves many inbounds and a
		// renewal touches a single row. 0 = legacy inline PEM (backfilled below).
		`ALTER TABLE sb_tls ADD COLUMN cert_id INTEGER NOT NULL DEFAULT 0`,
		// Display order of the TLS list (sb_inbounds has had one since creation).
		// All existing rows default to 0 and tie-break by id, so an un-reordered
		// list keeps its historical order.
		`ALTER TABLE sb_tls ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN last_online_at INTEGER NOT NULL DEFAULT 0`,
		// Last successful subscription response. Store only a bounded client class,
		// never the raw User-Agent/IP; RecordSubscriptionFetch throttles writes.
		`ALTER TABLE users ADD COLUMN sub_last_fetched_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN sub_last_format TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN sub_last_client TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN creds_reset_at INTEGER NOT NULL DEFAULT 0`,
		// Admin-only note on an account. Panel-side metadata; nothing downstream
		// (sing-box config, subscriptions, the user's own pages) reads it.
		`ALTER TABLE users ADD COLUMN remark TEXT NOT NULL DEFAULT ''`,
		// Rename legacy columns to neutral names on DBs created before the
		// rename. Errors ("no such column") are expected on fresh/up-to-date DBs
		// where CREATE TABLE already used the new names.
		`ALTER TABLE users RENAME COLUMN sui_client_id TO client_id`,
		`ALTER TABLE users RENAME COLUMN sui_client_name TO client_name`,
		`ALTER TABLE users RENAME COLUMN sui_client_uuid TO client_uuid`,
		`ALTER TABLE users RENAME COLUMN sui_client_secret TO client_secret`,
		`ALTER TABLE nodes RENAME COLUMN sui_inbound_tag TO inbound_tag`,
		`DROP INDEX IF EXISTS idx_users_sui_client_name`,
		// Hot-path indexes. client_name backs AddUsageByClientName's
		// per-user UPDATE on every stats poll and UsersWithClient; source_id
		// backs ReplaceSourceNodes' delete-by-source; server_id backs the
		// per-server inbound filter.
		`CREATE INDEX IF NOT EXISTS idx_users_client_name ON users(client_name)`,
		`CREATE INDEX IF NOT EXISTS idx_nodes_source ON nodes(source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sb_inbounds_server ON sb_inbounds(server_id)`,
		// Monitor probe: extend servers table with probe/asset fields.
		`ALTER TABLE servers ADD COLUMN probe_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE servers ADD COLUMN probe_token TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE servers ADD COLUMN expiry_date INTEGER NOT NULL DEFAULT 0`,
		// Per-device expiry and provider-traffic notification policies.
		// notify_enabled defaults off: upgrades remain silent until an admin opts in.
		`ALTER TABLE servers ADD COLUMN expiry_notify_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE servers ADD COLUMN expiry_notify_days INTEGER NOT NULL DEFAULT 3`,
		`ALTER TABLE servers ADD COLUMN expiry_notify_mode TEXT NOT NULL DEFAULT 'count'`,
		`ALTER TABLE servers ADD COLUMN expiry_notify_count INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE servers ADD COLUMN traffic_limit_bytes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE servers ADD COLUMN traffic_reset_day INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE servers ADD COLUMN traffic_reset_minute INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE servers ADD COLUMN traffic_alert_percent INTEGER NOT NULL DEFAULT 80`,
		`ALTER TABLE servers ADD COLUMN provider TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE servers ADD COLUMN location TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE servers ADD COLUMN spec TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE servers ADD COLUMN price REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE servers ADD COLUMN notes TEXT NOT NULL DEFAULT ''`,
		// Pinned SSH host key (authorized_keys line) for TOFU verification, so the
		// panel doesn't blindly trust any host key on connect (MITM → root RCE).
		`ALTER TABLE servers ADD COLUMN host_key TEXT NOT NULL DEFAULT ''`,
		// Lookup index for the probe token: the token itself is now encrypted at
		// rest, so the report endpoint matches on this SHA-256 hash instead.
		`ALTER TABLE servers ADD COLUMN probe_token_hash TEXT NOT NULL DEFAULT ''`,
		// Whether this machine appears on the unauthenticated status page.
		// Defaults to 1 because that is what every probe-enabled server did
		// before the flag existed — an upgrade must not quietly empty someone's
		// public page. The panel's own machine is the opposite case and is not a
		// row here: it defaults to hidden, via the monitor_local_public setting.
		`ALTER TABLE servers ADD COLUMN public_visible INTEGER NOT NULL DEFAULT 1`,
		// Public status-page asset details are opt-out and independent of whether
		// the machine itself is listed. Existing installations therefore gain the
		// requested visible-by-default behaviour without exposing hidden servers.
		`ALTER TABLE servers ADD COLUMN public_show_traffic INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE servers ADD COLUMN public_show_price INTEGER NOT NULL DEFAULT 1`,
		// Providers do not all bill physical traffic the same way. Preserve the
		// historical IN+OUT total until an admin selects another convention.
		`ALTER TABLE servers ADD COLUMN traffic_accounting_mode TEXT NOT NULL DEFAULT 'sum'`,
		`ALTER TABLE server_traffic_calibrations ADD COLUMN accounting_mode TEXT NOT NULL DEFAULT 'sum'`,
		// Per-machine traffic accounting. Old probe rows remain explicitly invalid
		// rather than pretending that their missing cumulative counters were zero.
		`ALTER TABLE server_metrics ADD COLUMN net_rx_total INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE server_metrics ADD COLUMN net_tx_total INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE server_metrics ADD COLUMN net_totals_valid INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE server_metrics ADD COLUMN net_rx_bytes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE server_metrics ADD COLUMN net_tx_bytes INTEGER NOT NULL DEFAULT 0`,
		// Version of the reporting qingzhou-probe binary. Blank identifies probes
		// from before version reporting existed and lets the UI request an upgrade.
		`ALTER TABLE server_metrics ADD COLUMN probe_version TEXT NOT NULL DEFAULT ''`,
		// Prorated refunds: record how much was actually refunded on each order so
		// admin reporting and idempotent re-reads reflect the real (possibly partial)
		// amount instead of the original price. refund_ratio is the applied fraction
		// (0..1); refunded_traffic is the unused quota clawed back (audit).
		`ALTER TABLE orders ADD COLUMN refunded_points INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE orders ADD COLUMN refunded_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE orders ADD COLUMN refund_ratio REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE orders ADD COLUMN refunded_traffic INTEGER NOT NULL DEFAULT 0`,
		// Per-bucket custom credentials for mixed (HTTP/SOCKS5) proxy inbounds: a
		// user-chosen username/password (proxy-only account, unrelated to login) that
		// replaces client_name/client_secret ONLY for mixed inbounds, with its own
		// expiry (0 = permanent). Empty proxy_username → fall back to client_name, so
		// existing buckets keep working. proxy_username is an additional sing-box
		// stats identity for the bucket, so AddBucketUsage matches it too.
		// Purchase idempotency: a client key (per purchase intent) so a network retry
		// after a lost response returns the same order instead of double-charging.
		// The partial unique index only constrains non-empty keys, so legacy/keyless
		// orders (and admin comps) are unaffected.
		`ALTER TABLE orders ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_idem ON orders(user_id, idempotency_key) WHERE idempotency_key <> ''`,
		`ALTER TABLE user_plans ADD COLUMN proxy_username TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE user_plans ADD COLUMN proxy_password TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE user_plans ADD COLUMN proxy_expires_at INTEGER NOT NULL DEFAULT 0`,
		// Multi-plan queue: buying the same package again no longer stacks into one
		// bucket. Each purchase is its own bucket; among same-package plan buckets
		// only the head is 'active', the rest are 'queued' and activate (their
		// duration_days starting to count) when the head is exhausted or expires.
		// Existing rows default to 'active' so their behavior is unchanged.
		`ALTER TABLE user_plans ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`,
		`ALTER TABLE user_plans ADD COLUMN duration_days INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE user_plans ADD COLUMN queue_key TEXT NOT NULL DEFAULT ''`,
		// A proxy_username must be globally unique (it becomes a stats identity);
		// partial index so the many empty defaults don't collide.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_user_plans_proxy_username ON user_plans(proxy_username) WHERE proxy_username <> ''`,
		// Account-level mixed-proxy credential: one HTTP/SOCKS5 login per USER,
		// valid on every node they can reach. The per-bucket credential above ties
		// the login to whichever套餐 happens to own a node, so moving a node between
		// groups (or renewing) handed the user a different username/password and
		// broke whatever they had pasted into 1Panel/Docker/git. This one never
		// changes on its own. The per-bucket credential stays valid alongside it so
		// already-copied logins keep working. Same identity namespace as
		// user_plans.proxy_username — uniqueness across all three tables is enforced
		// in Go (proxyNameTaken), the index below is the last line of defence.
		`ALTER TABLE users ADD COLUMN proxy_username TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN proxy_password TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN proxy_expires_at INTEGER NOT NULL DEFAULT 0`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_proxy_username ON users(proxy_username) WHERE proxy_username <> ''`,
		// TLS to the proxy egress itself ("HTTPS proxy"). We are the client on
		// that hop, so tls_cert_id is a TRUST ANCHOR (verify the proxy against
		// this managed cert) — the panel never presents a certificate here, and
		// the anchor's private key half is unused. 0 = verify against system
		// roots, which is what a commercial proxy with a public cert needs.
		// Defaults keep every existing egress plaintext, exactly as before.
		`ALTER TABLE sb_egresses ADD COLUMN tls_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sb_egresses ADD COLUMN sni TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sb_egresses ADD COLUMN tls_cert_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sb_egresses ADD COLUMN tls_insecure INTEGER NOT NULL DEFAULT 0`,
		// How UDP behaves on the hop through this proxy, and how long to wait for
		// the TCP connect to it. Both default to '' / 0, which mean "decide by
		// type" and "use the built-in default" respectively — see
		// SbEgress.EffectiveUDPMode / EffectiveConnectTimeoutMS. Storing the
		// sentinel instead of backfilling a concrete value keeps an existing row
		// tracking the panel's default as it improves, rather than freezing
		// whatever the default happened to be on the upgrade day.
		`ALTER TABLE sb_egresses ADD COLUMN udp_mode TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sb_egresses ADD COLUMN connect_timeout_ms INTEGER NOT NULL DEFAULT 0`,
		// Alerts became episode-based (see server_alerts above). Legacy DBs hold one
		// row per hourly observation, so a single server that stayed offline for a
		// week shows ~170 identical unread lines. Fold each unread (server, type)
		// group into its newest row and mark the rest read. The first statement is
		// the one-time part (it only matches the legacy first_ts=0 rows); the
		// collapse is a permanent no-op once InsertAlert keeps the invariant.
		`ALTER TABLE server_alerts ADD COLUMN first_ts INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE server_alerts ADD COLUMN hits INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE server_alerts ADD COLUMN resolved INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_open ON server_alerts(server_id, type, read)`,
		`UPDATE server_alerts SET
		   first_ts=(SELECT MIN(b.ts) FROM server_alerts b WHERE b.server_id=server_alerts.server_id AND b.type=server_alerts.type AND b.read=0),
		   hits=(SELECT COUNT(*) FROM server_alerts b WHERE b.server_id=server_alerts.server_id AND b.type=server_alerts.type AND b.read=0)
		 WHERE read=0 AND first_ts=0`,
		`UPDATE server_alerts SET read=1 WHERE read=0 AND id NOT IN (SELECT MAX(id) FROM server_alerts WHERE read=0 GROUP BY server_id, type)`,
		`UPDATE server_alerts SET first_ts=ts WHERE first_ts=0`,
		// Drop relay links left dangling by inbound deletions that predate
		// DeleteSbInbound un-chaining its referrers. The generated config already
		// ignores a dangling upstream, but 链路拓扑 kept drawing the deleted inbound
		// as「落地已失效」. Permanent no-op once the invariant is maintained.
		//
		// upstream_broken=1 rather than a silent clear: these relays are exiting
		// from their own machine instead of the landing the admin configured, and
		// upgrading must not be what makes that stop being visible.
		`UPDATE sb_inbounds SET upstream_inbound_id=0, upstream_broken=1
		 WHERE upstream_inbound_id<>0
		   AND upstream_inbound_id NOT IN (SELECT id FROM sb_inbounds)`,
		`UPDATE nodes SET route_upstream_inbound_id=0, route_upstream_broken=1
		 WHERE route_upstream_inbound_id<>0
		   AND route_upstream_inbound_id NOT IN (SELECT id FROM sb_inbounds)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			// Benign on an up-to-date DB: the column already exists (ADD COLUMN) or
			// was already renamed / never existed (RENAME COLUMN). Anything else — a
			// disk error, or a typo'd statement that never lands — must be surfaced,
			// not swallowed, or a required column shows up much later as a confusing
			// scan failure far from the cause.
			msg := err.Error()
			if strings.Contains(msg, "duplicate column name") ||
				strings.Contains(msg, "no such column") ||
				strings.Contains(msg, "no such table") {
				continue
			}
			log.Printf("migrate: statement failed (continuing): %q: %v", stmt, err)
		}
	}
	// Old rows predate queue_key. Their historical behaviour was one independent
	// line per package, represented by the same derived default new writes use.
	if _, err := s.db.Exec(`UPDATE user_plans SET queue_key='pkg:'||package_id
		WHERE kind='plan' AND package_id>0 AND queue_key=''`); err != nil {
		return err
	}
	// Indexes last, now that every column they can name exists. Statement by
	// statement, and a failure is logged rather than returned: an index is a
	// lookup aid (uniqueness here is enforced in Go first — see proxyNameTaken),
	// and refusing to boot over one strands a panel whose only way to receive the
	// fix is the panel itself. A missing index is slow; a boot loop is offline.
	for _, stmt := range strings.Split(indexes, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := s.db.Exec(stmt); err != nil {
			log.Printf("migrate: index failed (continuing): %q: %v", strings.TrimSpace(stmt), err)
		}
	}
	// Backfill probe_token_hash for existing (plaintext) tokens so hash-based
	// lookup keeps working after the upgrade. Idempotent (skips rows already set).
	if err := s.backfillProbeTokenHash(); err != nil {
		return err
	}
	// Seed the bucket model from legacy single-plan columns (idempotent).
	if err := s.backfillUserPlans(); err != nil {
		return err
	}
	// Preserve legacy zero-config paid service without preserving its unsafe rule
	// ("no groups" meant every bucket — including a plan-less one — got every
	// inbound). On the first upgrade only, materialise that implicit relationship
	// as an ordinary node group bound to the packages historical users hold.
	if err := s.migrateZeroConfigEntitlements(); err != nil {
		return err
	}
	// Mark the已用完份 of existing queue chains as retired BEFORE the merge below,
	// so a progressed queue is never mistaken for legacy duplicates (idempotent).
	if err := s.backfillRetiredBuckets(); err != nil {
		return err
	}
	// Lift each subscription line's credentials out of its buckets and into
	// plan_identities, taking them from the份 in service so nobody's client is
	// disconnected by the upgrade (idempotent).
	if err := s.backfillPlanIdentities(); err != nil {
		return err
	}
	// Move protocol authentication to the user's stable credential. On an
	// upgraded DB this captures every credential clients may already hold as a
	// time-limited alias; on a fresh DB there are no rows to capture and only the
	// migration marker is written.
	if err := s.migrateStableProtocolCredentials(); err != nil {
		return err
	}
	// Collapse duplicate plan buckets left by pre-renewal repurchases (idempotent).
	if err := s.mergeDuplicatePlanBuckets(); err != nil {
		return err
	}
	// Remove the synthetic zero-byte welcome rows created by the old 0=unlimited
	// interpretation and rebuild users.* from real finite buckets. One-shot: a
	// restart must not rewrite live aggregates unnecessarily.
	if err := s.migrateFiniteTrafficAggregates(); err != nil {
		return err
	}
	// Give every existing provisioned user a free bucket (idempotent). This is
	// required, not cosmetic: the pool no longer covers the free group, so an
	// account without a free bucket would lose free-node access entirely.
	if err := s.backfillFreeBuckets(); err != nil {
		return err
	}
	// Mint the account-level HTTP/SOCKS5 credential for existing users (idempotent).
	if err := s.backfillProxyAccounts(); err != nil {
		return err
	}
	// Seed traffic_daily from the samples still on disk (idempotent).
	if err := s.backfillTrafficDaily(); err != nil {
		return err
	}
	// Extract inline-PEM sb_tls (mode=tls) profiles into managed certificates
	// rows and repoint them via cert_id (idempotent).
	return s.backfillCerts()
}

// migrateZeroConfigEntitlements converts the old implicit zero-config grant into
// explicit data. It is intentionally one-shot: after the marker exists, a new
// package with no groups must remain entitled to nothing until the admin binds
// it, rather than silently inheriting every node.
func (s *Store) migrateZeroConfigEntitlements() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var done int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM settings WHERE key='migrated_zero_config_v1'`).Scan(&done); err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	var groups int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM node_groups`).Scan(&groups); err != nil {
		return err
	}
	var paidPackages int
	if err := tx.QueryRow(`SELECT COUNT(DISTINCT package_id) FROM user_plans
		WHERE kind='plan' AND package_id>0`).Scan(&paidPackages); err != nil {
		return err
	}
	if groups == 0 && paidPackages > 0 {
		now := time.Now().Unix()
		res, err := tx.Exec(`INSERT INTO node_groups(name,description,sort_order,created_at)
			VALUES('历史全节点','由升级迁移：保留原 zero-config 付费套餐的全节点访问',0,?)`, now)
		if err != nil {
			return err
		}
		gid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		// The legacy fallback iterated enabled sing-box inbounds directly; many
		// zero-config installs therefore have no corresponding nodes row. Materialise
		// those inbounds first. External nodes were never part of that fallback and
		// must not be swept into this compatibility grant.
		if _, err := tx.Exec(`INSERT INTO nodes
			(type,name,protocol,inbound_tag,share_link,source_id,enabled,sort_order,created_at)
			SELECT 'self_built', tag, type, tag, '', 0, 1, sort_order, ?
			  FROM sb_inbounds i
			 WHERE i.enabled=1
			   AND NOT EXISTS (SELECT 1 FROM nodes n
			                    WHERE n.type='self_built' AND n.inbound_tag=i.tag)`, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO node_group_members(node_id,group_id)
			SELECT id, ? FROM nodes WHERE type='self_built' AND enabled=1`, gid); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO plan_groups(package_id,group_id)
			SELECT DISTINCT package_id, ? FROM user_plans
			 WHERE kind='plan' AND package_id>0`, gid); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES('migrated_zero_config_v1','1')`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateSettingsCache()
	return nil
}

// backfillTrafficDaily seeds the rollup from the traffic_samples still on disk
// (up to 35 days) so the usage report is not blank on the day it ships.
//
// Those samples predate per-bucket recording, so the bytes are real but their
// package is not knowable — they land under package_id -1, which the report
// labels as unattributed instead of assigning it to a package that may not have
// carried it.
//
// Idempotent by construction, and safe to run against a DB that has already
// been recording properly: it only writes days that have NO row at all for that
// user, so a day already attributed per bucket is never touched, and re-running
// cannot double-count.
func (s *Store) backfillTrafficDaily() error {
	_, err := s.db.Exec(`
		INSERT INTO traffic_daily (day, user_id, bucket_id, package_id, up, down)
		SELECT strftime('%Y-%m-%d', ts, 'unixepoch', 'localtime') AS d,
		       user_id, 0, -1, COALESCE(SUM(up),0), COALESCE(SUM(down),0)
		  FROM traffic_samples
		 GROUP BY d, user_id
		HAVING NOT EXISTS (
		         SELECT 1 FROM traffic_daily t
		          WHERE t.day = d AND t.user_id = traffic_samples.user_id)`)
	return err
}

// backfillProxyAccounts mints the account-level HTTP/SOCKS5 credential for users
// provisioned before it existed. Same scope rule as backfillFreeBuckets: only
// users who already have a bucket, because an account credential is an identity
// in the sing-box config and an unprovisioned account has no business being
// there — it gets one at provision time instead.
//
// Nothing breaks for these users on the upgrade: the per-bucket credential they
// may already have pasted somewhere stays in the config and stays valid (see
// BuildUsersByTag). The new credential is simply the one the panel shows from
// now on.
func (s *Store) backfillProxyAccounts() error {
	ids, err := queryInts(s.db, `SELECT u.id FROM users u
		WHERE u.proxy_username = ''
		  AND EXISTS (SELECT 1 FROM user_plans p WHERE p.user_id = u.id)`)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.EnsureProxyAccount(id); err != nil {
			return err
		}
	}
	if len(ids) > 0 {
		log.Printf("migrate: minted %d account-level proxy credentials", len(ids))
	}
	return nil
}

// backfillFreeBuckets creates the free-group bucket for users provisioned before
// free traffic was split off the pool. Only users who already have a bucket are
// touched — an unprovisioned account gets its free bucket at provision time, and
// synthesising an identity for one here would put a user in the sing-box config
// who was never meant to be there.
func (s *Store) backfillFreeBuckets() error {
	rows, err := s.db.Query(`SELECT u.id, u.username FROM users u
		WHERE EXISTS (SELECT 1 FROM user_plans p WHERE p.user_id = u.id)
		  AND NOT EXISTS (SELECT 1 FROM user_plans p WHERE p.user_id = u.id AND p.kind = ?)`, KindFree)
	if err != nil {
		return err
	}
	type row struct {
		id   int64
		name string
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range todo {
		if err := s.EnsureFreeBucket(r.id, r.name); err != nil {
			return err
		}
	}
	if len(todo) > 0 {
		log.Printf("migrate: created %d free-traffic buckets", len(todo))
	}
	return nil
}

// backfillProbeTokenHash computes probe_token_hash from the stored token for any
// probe-enabled server missing it (legacy rows whose token predates encryption).
func (s *Store) backfillProbeTokenHash() error {
	rows, err := s.db.Query(`SELECT id, probe_token FROM servers WHERE probe_token != '' AND probe_token_hash = ''`)
	if err != nil {
		return err
	}
	type row struct {
		id  int64
		tok string
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.tok); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range todo {
		if _, err := s.db.Exec(`UPDATE servers SET probe_token_hash=? WHERE id=?`, hashProbeToken(r.tok), r.id); err != nil {
			return err
		}
	}
	return nil
}
