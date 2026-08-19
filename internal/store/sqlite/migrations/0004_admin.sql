CREATE TABLE IF NOT EXISTS admin_credentials (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  password_hash BLOB NOT NULL,
  created_at TEXT NOT NULL
);
