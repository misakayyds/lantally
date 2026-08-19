package sqlite

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Backup copies the SQLite database to lantally.db.bak-<unix> in the same directory.
func (s *Store) Backup() (string, error) {
	path := s.dbPath()
	dir := filepath.Dir(path)
	name := fmt.Sprintf("lantally.db.bak-%d", time.Now().UTC().Unix())
	destination := filepath.Join(dir, name)

	source, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer source.Close()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	target, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		return "", err
	}
	if err := target.Close(); err != nil {
		return "", err
	}
	return destination, nil
}

func (s *Store) dbPath() string {
	// Store keeps db handle only; path is tracked for backup helpers.
	if s.databasePath != "" {
		return s.databasePath
	}
	return "lantally.db"
}
