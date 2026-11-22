package sqlite

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Backup writes a consistent SQLite snapshot to lantally.db.bak-<unix> in the same directory.
func (s *Store) Backup() (string, error) {
	path := s.dbPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	base := fmt.Sprintf("lantally.db.bak-%d", time.Now().UTC().Unix())
	destination := filepath.Join(dir, base)
	if _, err := os.Stat(destination); err == nil {
		destination = filepath.Join(dir, fmt.Sprintf("%s-%d", base, time.Now().UTC().UnixNano()))
	}
	if err := s.BackupTo(destination); err != nil {
		return "", err
	}
	return destination, nil
}

// BackupTo writes a consistent SQLite snapshot to destination using VACUUM INTO.
func (s *Store) BackupTo(destination string) error {
	if s == nil || s.db == nil {
		return errors.New("database is closed")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return err
	}
	quoted := strings.ReplaceAll(destination, "'", "''")
	_, err := s.db.Exec(fmt.Sprintf("VACUUM INTO '%s'", quoted))
	return err
}

func (s *Store) dbPath() string {
	// Store keeps db handle only; path is tracked for backup helpers.
	if s.databasePath != "" {
		return s.databasePath
	}
	return "lantally.db"
}
