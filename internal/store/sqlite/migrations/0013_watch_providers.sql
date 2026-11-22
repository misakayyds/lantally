CREATE TABLE IF NOT EXISTS watch_items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  matchers_json TEXT NOT NULL DEFAULT '{}',
  override_factor REAL DEFAULT NULL,
  quota_value REAL NOT NULL DEFAULT 0,
  quota_bytes INTEGER NOT NULL DEFAULT 0,
  quota_unit TEXT NOT NULL DEFAULT 'GB',
  reset_type TEXT NOT NULL DEFAULT 'none',
  reset_day INTEGER NOT NULL DEFAULT 1,
  expires_at TEXT NOT NULL DEFAULT '',
  pinned INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS watch_items_pinned_idx
ON watch_items(pinned DESC, id ASC);

CREATE TABLE IF NOT EXISTS provider_subscriptions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  source_type TEXT NOT NULL DEFAULT 'manual',
  node_id TEXT NOT NULL DEFAULT '',
  provider_name TEXT NOT NULL DEFAULT '',
  vehicle_type TEXT NOT NULL DEFAULT 'Manual',
  upload_bytes INTEGER NOT NULL DEFAULT 0,
  download_bytes INTEGER NOT NULL DEFAULT 0,
  total_bytes INTEGER NOT NULL DEFAULT 0,
  quota_unit TEXT NOT NULL DEFAULT 'GB',
  expire_at INTEGER NOT NULL DEFAULT 0,
  reset_day INTEGER NOT NULL DEFAULT 1,
  outbound_pattern TEXT NOT NULL DEFAULT '',
  linked_nodes_json TEXT NOT NULL DEFAULT '[]',
  proxies_json TEXT NOT NULL DEFAULT '[]',
  nodes_locked INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS provider_subscriptions_mihomo_uq
ON provider_subscriptions(source_type, provider_name)
WHERE source_type = 'mihomo';

CREATE INDEX IF NOT EXISTS provider_subscriptions_enabled_idx
ON provider_subscriptions(enabled, source_type, id);
