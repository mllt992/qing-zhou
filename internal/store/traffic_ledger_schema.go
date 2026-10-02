package store

const trafficLedgerSchema = `
CREATE TABLE traffic_collection_leases (server_id INTEGER PRIMARY KEY,owner TEXT NOT NULL,expires_at INTEGER NOT NULL);
CREATE TABLE traffic_collection_sequence (id INTEGER PRIMARY KEY CHECK(id=1), value INTEGER NOT NULL);
INSERT INTO traffic_collection_sequence VALUES(1,0);
CREATE TABLE traffic_polls (
 id TEXT PRIMARY KEY, sequence INTEGER NOT NULL UNIQUE, server_id INTEGER NOT NULL, observed_at INTEGER NOT NULL,
 mode TEXT NOT NULL, epoch TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL,
 digest TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending', created_at INTEGER NOT NULL,
 error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_traffic_polls_pending ON traffic_polls(state,created_at);
CREATE TABLE traffic_poll_bindings (
 poll_id TEXT NOT NULL, counter_name TEXT NOT NULL, source_kind TEXT NOT NULL,
 link_id INTEGER NOT NULL DEFAULT 0, user_id INTEGER NOT NULL DEFAULT 0,
 bucket_id INTEGER NOT NULL DEFAULT 0, package_id INTEGER NOT NULL DEFAULT 0, bucket_kind TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(poll_id,counter_name)
);
CREATE TABLE traffic_observations (
 id INTEGER PRIMARY KEY AUTOINCREMENT, poll_id TEXT NOT NULL, counter_name TEXT NOT NULL,
 server_id INTEGER NOT NULL, ts INTEGER NOT NULL, source_kind TEXT NOT NULL,
 link_id INTEGER NOT NULL DEFAULT 0, user_id INTEGER NOT NULL DEFAULT 0,
 up INTEGER NOT NULL CHECK(up>=0), down INTEGER NOT NULL CHECK(down>=0),
 quality TEXT NOT NULL DEFAULT 'observed', billable INTEGER NOT NULL DEFAULT 0,
 UNIQUE(poll_id,counter_name)
);
CREATE INDEX idx_traffic_observations_server_ts ON traffic_observations(server_id,ts);
CREATE TABLE machine_traffic_daily (
 day TEXT NOT NULL, server_id INTEGER NOT NULL, source_kind TEXT NOT NULL,
 link_id INTEGER NOT NULL DEFAULT 0, user_id INTEGER NOT NULL DEFAULT 0,
 up INTEGER NOT NULL DEFAULT 0 CHECK(typeof(up)='integer' AND up>=0), down INTEGER NOT NULL DEFAULT 0 CHECK(typeof(down)='integer' AND down>=0),
 PRIMARY KEY(day,server_id,source_kind,link_id,user_id)
);
CREATE TABLE traffic_counter_cursors (
 server_id INTEGER NOT NULL, counter_name TEXT NOT NULL, epoch TEXT NOT NULL,
 up INTEGER NOT NULL, down INTEGER NOT NULL, observed_at INTEGER NOT NULL, sequence INTEGER NOT NULL,
 PRIMARY KEY(server_id,counter_name,epoch)
);
CREATE TABLE traffic_metering_state (
 server_id INTEGER PRIMARY KEY, mode TEXT NOT NULL DEFAULT 'reset', epoch TEXT NOT NULL DEFAULT '',
 last_sequence INTEGER NOT NULL DEFAULT 0, last_success INTEGER NOT NULL DEFAULT 0, last_attempt INTEGER NOT NULL DEFAULT 0,
 failures INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'unknown', error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE traffic_metering_gaps (
 id INTEGER PRIMARY KEY AUTOINCREMENT, server_id INTEGER NOT NULL, ts INTEGER NOT NULL,
 reason TEXT NOT NULL, poll_id TEXT NOT NULL, UNIQUE(server_id,poll_id,reason)
);
CREATE INDEX idx_traffic_metering_gaps_server_ts ON traffic_metering_gaps(server_id,ts);
CREATE TABLE relay_metering_links (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 source_server_id INTEGER NOT NULL, source_inbound_id INTEGER NOT NULL,
 route_node_id INTEGER NOT NULL DEFAULT 0, target_server_id INTEGER NOT NULL,
 target_inbound_id INTEGER NOT NULL, identity_name TEXT NOT NULL UNIQUE,
 credential TEXT NOT NULL, generation INTEGER NOT NULL DEFAULT 1,
 state TEXT NOT NULL DEFAULT 'prepared', created_at INTEGER NOT NULL,
 accepted_at INTEGER NOT NULL DEFAULT 0, activated_at INTEGER NOT NULL DEFAULT 0,
 source_name TEXT NOT NULL, target_name TEXT NOT NULL, spec_hash TEXT NOT NULL,
 UNIQUE(source_server_id,source_inbound_id,route_node_id,target_server_id,target_inbound_id)
);
CREATE TABLE relay_metering_generations (
 link_id INTEGER NOT NULL, generation INTEGER NOT NULL, identity_name TEXT NOT NULL UNIQUE,
 credential TEXT NOT NULL, retired_at INTEGER NOT NULL DEFAULT 0, accepted_at INTEGER NOT NULL DEFAULT 0, retirement_applied_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(link_id,generation)
);
CREATE TABLE relay_legacy_compatibility (
 server_id INTEGER NOT NULL,inbound_id INTEGER NOT NULL,state TEXT NOT NULL,updated_at INTEGER NOT NULL,
 PRIMARY KEY(server_id,inbound_id)
);
CREATE TABLE relay_credential_audit (
 id INTEGER PRIMARY KEY AUTOINCREMENT,actor_id INTEGER NOT NULL,server_id INTEGER NOT NULL,inbound_id INTEGER NOT NULL,
 link_id INTEGER NOT NULL,generation INTEGER NOT NULL,action TEXT NOT NULL,ts INTEGER NOT NULL
);
CREATE INDEX idx_relay_metering_target ON relay_metering_links(target_server_id);
CREATE TABLE relay_metering_applies (
 server_id INTEGER PRIMARY KEY, config_hash TEXT NOT NULL, applied_at INTEGER NOT NULL
);
-- Preserve historical users without inventing a route or replaying any debit.
INSERT INTO traffic_observations(poll_id,counter_name,server_id,ts,source_kind,user_id,up,down,quality,billable)
 SELECT 'legacy:'||rowid,'legacy-user:'||user_id,server_id,ts,'historical_user',user_id,
 MAX(0,up),MAX(0,down),'legacy_user_only',0 FROM server_user_traffic_samples;
INSERT INTO machine_traffic_daily(day,server_id,source_kind,user_id,up,down)
 SELECT strftime('%Y-%m-%d',ts,'unixepoch'),server_id,'historical_user',user_id,SUM(up),SUM(down)
 FROM traffic_observations GROUP BY 1,2,4;
`
