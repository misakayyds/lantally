PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS nodes (
  id TEXT PRIMARY KEY,
  site_id TEXT NOT NULL,
  credential_id TEXT NOT NULL UNIQUE,
  token_hash BLOB NOT NULL,
  revoked INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS ingest_batches (
  node_id TEXT NOT NULL,
  boot_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  received_at TEXT NOT NULL,
  payload_sha256 TEXT NOT NULL,
  PRIMARY KEY (node_id, boot_id, sequence),
  FOREIGN KEY (node_id) REFERENCES nodes(id)
);
