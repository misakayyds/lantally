CREATE TABLE IF NOT EXISTS ledger_samples (
  sampled_at INTEGER NOT NULL,
  site_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  device_id TEXT NOT NULL DEFAULT '',
  class TEXT NOT NULL,
  rx INTEGER NOT NULL DEFAULT 0,
  tx INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS ledger_samples_lookup_idx
ON ledger_samples(sampled_at, class, device_id, node_id);
