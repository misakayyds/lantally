CREATE TABLE IF NOT EXISTS ledger_applied (
  node_id TEXT NOT NULL,
  boot_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  PRIMARY KEY (node_id, boot_id, sequence)
);

CREATE TABLE IF NOT EXISTS ledger_totals (
  site_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  device_id TEXT NOT NULL DEFAULT '',
  class TEXT NOT NULL,
  rx INTEGER NOT NULL DEFAULT 0,
  tx INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (site_id, node_id, device_id, class)
);
