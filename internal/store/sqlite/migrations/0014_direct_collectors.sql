CREATE TABLE IF NOT EXISTS direct_collectors (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  site_id TEXT NOT NULL,
  node_id TEXT NOT NULL UNIQUE,
  url TEXT NOT NULL,
  secret_enc BLOB NOT NULL DEFAULT X'',
  enabled INTEGER NOT NULL DEFAULT 1,
  collect_domains INTEGER NOT NULL DEFAULT 0,
  poll_interval_ms INTEGER NOT NULL DEFAULT 1000,
  last_seen_at TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY (node_id) REFERENCES nodes(id)
);

CREATE INDEX IF NOT EXISTS direct_collectors_enabled_idx
ON direct_collectors(enabled, id);
