CREATE TABLE IF NOT EXISTS dimension_samples (
  sampled_at INTEGER NOT NULL,
  site_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  dim_type TEXT NOT NULL,
  dim_key TEXT NOT NULL,
  rx INTEGER NOT NULL DEFAULT 0,
  tx INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS dimension_samples_lookup_idx
ON dimension_samples(sampled_at, dim_type, node_id, site_id, dim_key);

CREATE INDEX IF NOT EXISTS dimension_samples_type_key_idx
ON dimension_samples(dim_type, dim_key, sampled_at);

CREATE TABLE IF NOT EXISTS dimension_daily (
  day INTEGER NOT NULL,
  site_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  dim_type TEXT NOT NULL,
  dim_key TEXT NOT NULL,
  rx INTEGER NOT NULL DEFAULT 0,
  tx INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, site_id, node_id, dim_type, dim_key)
);

CREATE INDEX IF NOT EXISTS dimension_daily_lookup_idx
ON dimension_daily(day, dim_type, node_id, site_id, dim_key);

CREATE TABLE IF NOT EXISTS multiplier_rules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL DEFAULT '',
  match_type TEXT NOT NULL,
  pattern TEXT NOT NULL DEFAULT '',
  factor REAL NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS multiplier_rules_order_idx
ON multiplier_rules(enabled, priority DESC, id ASC);

CREATE UNIQUE INDEX IF NOT EXISTS multiplier_rules_exact_pattern_uq
ON multiplier_rules(match_type, pattern)
WHERE match_type = 'exact';

CREATE TABLE IF NOT EXISTS proxy_node_catalog (
  node_id TEXT NOT NULL,
  name TEXT NOT NULL,
  provider_name TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY (node_id, name)
);

CREATE INDEX IF NOT EXISTS proxy_node_catalog_name_idx
ON proxy_node_catalog(name);
