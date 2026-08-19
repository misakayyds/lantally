-- R3: outbound multipliers. Device display_name is added in applyMigrations
-- when the column is missing (SQLite cannot ALTER IF NOT EXISTS).
-- Multipliers apply to future ingest only; history is not rewritten.

CREATE TABLE IF NOT EXISTS outbound_multipliers (
    name TEXT PRIMARY KEY,
    factor REAL NOT NULL,
    updated_at TEXT NOT NULL
);
