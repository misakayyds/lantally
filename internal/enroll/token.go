package enroll

import (
	"crypto/rand"
	"crypto/subtle"
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
