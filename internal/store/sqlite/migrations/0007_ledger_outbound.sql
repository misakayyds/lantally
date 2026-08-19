CREATE TABLE IF NOT EXISTS ledger_totals_outbound (
  site_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  device_id TEXT NOT NULL DEFAULT '',
  class TEXT NOT NULL,
  outbound TEXT NOT NULL DEFAULT '',
  rx INTEGER NOT NULL DEFAULT 0,
  tx INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (site_id, node_id, device_id, class, outbound)
);

INSERT INTO ledger_totals_outbound (site_id, node_id, device_id, class, outbound, rx, tx)
SELECT site_id, node_id, device_id, class, '', rx, tx FROM ledger_totals;

DROP TABLE ledger_totals;
ALTER TABLE ledger_totals_outbound RENAME TO ledger_totals;

ALTER TABLE ledger_samples ADD COLUMN outbound TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS ledger_samples_lookup_idx;
CREATE INDEX IF NOT EXISTS ledger_samples_lookup_idx
ON ledger_samples(sampled_at, class, device_id, node_id, outbound);
