CREATE TABLE IF NOT EXISTS ledger_daily (
  day INTEGER NOT NULL,
  site_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  device_id TEXT NOT NULL DEFAULT '',
  class TEXT NOT NULL,
  outbound TEXT NOT NULL DEFAULT '',
  rx INTEGER NOT NULL DEFAULT 0,
  tx INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, site_id, node_id, device_id, class, outbound)
);

CREATE INDEX IF NOT EXISTS ledger_daily_lookup_idx
ON ledger_daily(day, class, device_id, node_id, outbound);
