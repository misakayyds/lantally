package enroll

import (
	"bytes"
	"testing"
)

func TestHashTokenUsesSaltAndVerifies(t *testing.T) {
	const token = "synthetic-node-token"

	first := HashToken(token)
	second := HashToken(token)

	if bytes.Equal(first, second) {
		t.Fatal("expected independently salted hashes")
	}
	if !VerifyToken(token, first) {
		t.Fatal("expected correct token to verify")
	}
	if VerifyToken("wrong-token", first) {
		t.Fatal("expected wrong token to be rejected")
	}
}
