CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  site_id TEXT NOT NULL,
  canonical_id TEXT,
  created_at TEXT NOT NULL,
  FOREIGN KEY (canonical_id) REFERENCES devices(id)
);

CREATE INDEX IF NOT EXISTS devices_site_id_idx
ON devices(site_id);

CREATE TABLE IF NOT EXISTS identity_evidence (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_id TEXT NOT NULL,
  device_id TEXT NOT NULL,
  evidence_rank INTEGER NOT NULL,
  evidence_value TEXT NOT NULL,
  node_id TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  FOREIGN KEY (device_id) REFERENCES devices(id)
);

CREATE INDEX IF NOT EXISTS identity_evidence_lookup_idx
ON identity_evidence(site_id, evidence_rank, evidence_value);

CREATE UNIQUE INDEX IF NOT EXISTS identity_evidence_device_uq
ON identity_evidence(site_id, device_id, evidence_rank, evidence_value);

CREATE TABLE IF NOT EXISTS device_pins (
  site_id TEXT NOT NULL,
  pin_id TEXT NOT NULL,
  device_id TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  PRIMARY KEY (site_id, pin_id),
  FOREIGN KEY (device_id) REFERENCES devices(id)
);

CREATE TABLE IF NOT EXISTS identity_merges (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  winner_device_id TEXT NOT NULL,
  loser_device_id TEXT NOT NULL,
  evidence_rank INTEGER NOT NULL,
  evidence_value TEXT NOT NULL,
  merged_at TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 1,
  FOREIGN KEY (winner_device_id) REFERENCES devices(id),
  FOREIGN KEY (loser_device_id) REFERENCES devices(id)
);

CREATE INDEX IF NOT EXISTS identity_merges_active_idx
ON identity_merges(winner_device_id, loser_device_id, active);
