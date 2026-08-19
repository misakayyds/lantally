package sqlite

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

var ErrAdminUnauthorized = errors.New("admin unauthorized")

func (s *Store) HasAdminCredential() (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM admin_credentials WHERE id = 1`).Scan(&count)
	return count == 1, err
}

func (s *Store) CreateAdminCredential(password string) error {
	hash, err := hashAdminPassword(password)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO admin_credentials (id, password_hash, created_at) VALUES (1, ?, ?)`,
		hash,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) AuthenticateAdmin(password string) error {
	var stored []byte
	err := s.db.QueryRow(`SELECT password_hash FROM admin_credentials WHERE id = 1`).Scan(&stored)
	if err != nil {
		return ErrAdminUnauthorized
	}
	if !verifyAdminPassword(password, stored) {
		return ErrAdminUnauthorized
	}
	return nil
}

func (s *Store) UpdateAdminPassword(current, next string) error {
	if err := s.AuthenticateAdmin(current); err != nil {
		return err
	}
	next = strings.TrimSpace(next)
	if len(next) < 8 {
		return fmt.Errorf("password too short")
	}
	hash, err := hashAdminPassword(next)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE admin_credentials SET password_hash = ? WHERE id = 1`, hash)
	return err
}

func hashAdminPassword(password string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return append([]byte("argon2id"), append(salt, hash...)...), nil
}

func verifyAdminPassword(password string, stored []byte) bool {
	if len(stored) < 17 || string(stored[:8]) != "argon2id" {
		return false
	}
	salt := stored[8:24]
	expected := stored[24:]
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func GenerateAdminPassword() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return strings.TrimRight(encoded[:24], "-_"), nil
}

func (s *Store) BackupBeforeMigration(label string) (string, error) {
	if strings.TrimSpace(label) == "" {
		return "", fmt.Errorf("backup label is required")
	}
	return s.Backup()
}
