package enroll

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	saltLength   = 16
	hashLength   = 32
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
)

// HashToken returns a randomly salted argon2id token hash.
func HashToken(token string) []byte {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		panic("enroll: secure random source unavailable: " + err.Error())
	}
	key := argon2.IDKey([]byte(token), salt, argonTime, argonMemory, argonThreads, hashLength)
	return append(salt, key...)
}

// VerifyToken compares a token with a stored hash in constant time.
func VerifyToken(token string, stored []byte) bool {
	if len(stored) != saltLength+hashLength {
		return false
	}
	salt := stored[:saltLength]
	want := stored[saltLength:]
	got := argon2.IDKey([]byte(token), salt, argonTime, argonMemory, argonThreads, hashLength)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// IssueToken creates a one-time bearer token of the form lt_<credential_id>_<secret>.
func IssueToken() (token, credentialID string, err error) {
	var cred [8]byte
	var secret [24]byte
	if _, err := rand.Read(cred[:]); err != nil {
		return "", "", fmt.Errorf("generate credential id: %w", err)
	}
	if _, err := rand.Read(secret[:]); err != nil {
		return "", "", fmt.Errorf("generate token secret: %w", err)
	}
	credentialID = hex.EncodeToString(cred[:])
	token = "lt_" + credentialID + "_" + hex.EncodeToString(secret[:])
	return token, credentialID, nil
}

// CredentialID extracts the non-secret lookup identifier from a bearer token.
func CredentialID(token string) (string, bool) {
	const prefix = "lt_"
	if !strings.HasPrefix(token, prefix) {
		return "", false
	}
	credentialID, secret, ok := strings.Cut(strings.TrimPrefix(token, prefix), "_")
	if !ok || credentialID == "" || len(secret) < 16 {
		return "", false
	}
	for _, c := range []byte(credentialID) {
		if (c < 'a' || c > 'z') &&
			(c < 'A' || c > 'Z') &&
			(c < '0' || c > '9') &&
			c != '-' {
			return "", false
		}
	}
	if strings.ContainsAny(secret, " \t\r\n") {
		return "", false
	}
	return credentialID, true
}
