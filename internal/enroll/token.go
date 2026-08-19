package enroll

import (
	"crypto/rand"
	"crypto/subtle"

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
