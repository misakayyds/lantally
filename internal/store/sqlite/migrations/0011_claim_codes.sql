CREATE TABLE IF NOT EXISTS claim_codes (
    code TEXT PRIMARY KEY,
    site_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    local_mode INTEGER NOT NULL DEFAULT 0,
    expires_at TEXT NOT NULL,
    redeemed_at TEXT
);

CREATE INDEX IF NOT EXISTS claim_codes_node_idx
ON claim_codes(site_id, node_id);
