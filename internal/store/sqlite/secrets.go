package sqlite

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/hkdf"
)

const (
	secretKeySize     = 32
	secretNonceSize   = 12
	secretBlobVersion = 0x01
)

func isMemoryDSN(dsn string) bool {
	trimmed := strings.TrimSpace(dsn)
	return trimmed == "" || trimmed == ":memory:" || strings.HasPrefix(trimmed, "file::memory:") || strings.Contains(trimmed, "mode=memory")
}

func loadOrCreateSecretKey(dsn string) ([]byte, error) {
	if env := strings.TrimSpace(os.Getenv("LANTALLY_SECRET_KEY")); env != "" {
		kdf := hkdf.New(
			sha256.New,
			[]byte(env),
			[]byte("lantally-secret-enc-v1"),
			[]byte("aes-256-gcm"),
		)
		key := make([]byte, secretKeySize)
		if _, err := io.ReadFull(kdf, key); err != nil {
			return nil, fmt.Errorf("derive secret key: %w", err)
		}
		return key, nil
	}

	if isMemoryDSN(dsn) {
		key := make([]byte, secretKeySize)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate ephemeral secret key: %w", err)
		}
		return key, nil
	}

	keyPath := dsn + ".key"
	if key, ok, err := readSecretKeyFile(keyPath); err != nil {
		return nil, err
	} else if ok {
		return key, nil
	}

	if err := os.MkdirAll(filepath.Dir(keyPath), 0o750); err != nil {
		return nil, fmt.Errorf("create secret key dir: %w", err)
	}
	key := make([]byte, secretKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate secret key: %w", err)
	}
	f, err := os.OpenFile(keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			existing, ok, readErr := readSecretKeyFile(keyPath)
			if readErr != nil {
				return nil, readErr
			}
			if ok {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("create secret key file: %w", err)
	}
	if _, writeErr := f.Write(key); writeErr != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write secret key file: %w", writeErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		return nil, fmt.Errorf("close secret key file: %w", closeErr)
	}
	return key, nil
}

func readSecretKeyFile(path string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read secret key file: %w", err)
	}
	if len(raw) == secretKeySize {
		return raw, true, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == secretKeySize*2 {
		decoded, decErr := hex.DecodeString(trimmed)
		if decErr == nil && len(decoded) == secretKeySize {
			return decoded, true, nil
		}
	}
	return nil, false, fmt.Errorf("invalid secret key file %q: unexpected length %d", path, len(raw))
}

func encryptSecret(key []byte, plaintext string) ([]byte, error) {
	if plaintext == "" {
		return []byte{}, nil
	}
	if len(key) != secretKeySize {
		return nil, fmt.Errorf("invalid encryption key length %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, secretNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	out := make([]byte, 0, 1+secretNonceSize+len(sealed))
	out = append(out, secretBlobVersion)
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, nil
}

func decryptSecret(key []byte, blob []byte) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	if len(key) != secretKeySize {
		return "", fmt.Errorf("invalid decryption key length %d", len(key))
	}
	if blob[0] != secretBlobVersion {
		return "", fmt.Errorf("unsupported secret envelope version %d", blob[0])
	}
	if len(blob) < 1+secretNonceSize+16 {
		return "", errors.New("corrupted secret envelope")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, blob[1:1+secretNonceSize], blob[1+secretNonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("secret decryption failed: %w", err)
	}
	return string(plain), nil
}
