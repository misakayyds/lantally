-- R4: alerts, billing checkpoints, and node last-seen for silence detection.

CREATE TABLE IF NOT EXISTS alerts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    fingerprint TEXT NOT NULL,
    kind TEXT NOT NULL,
    site_id TEXT NOT NULL DEFAULT '',
    node_id TEXT NOT NULL DEFAULT '',
    device_id TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    acknowledged_at TEXT,
    resolved_at TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS alerts_unresolved_fingerprint
ON alerts(fingerprint) WHERE resolved_at IS NULL;

CREATE TABLE IF NOT EXISTS billing_checkpoints (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    reset_day INTEGER NOT NULL,
    provider_bytes INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);
