package enroll

import (
	"bytes"
	"strings"
	"testing"
)

func TestHashTokenUsesSaltAndVerifies(t *testing.T) {
	token := "lt_cred-a_" + strings.Repeat("a", 32)

	first := HashToken(token)
	second := HashToken(token)

	if bytes.Equal(first, second) {
		t.Fatal("expected independently salted hashes")
	}
	if !VerifyToken(token, first) {
		t.Fatal("expected correct token to verify")
	}
	if VerifyToken("lt_cred-a_"+strings.Repeat("b", 32), first) {
		t.Fatal("expected wrong token to be rejected")
	}
}

func TestCredentialIDParsesPublicTokenIdentifier(t *testing.T) {
	token := "lt_cred-a_" + strings.Repeat("a", 32)
	id, ok := CredentialID(token)
	if !ok || id != "cred-a" {
		t.Fatalf("CredentialID(%q) = %q, %v", token, id, ok)
	}

	for _, invalid := range []string{
		"",
		"cred-a_" + strings.Repeat("a", 32),
		"lt__" + strings.Repeat("a", 32),
		"lt_cred-a_",
		"lt_cred a_" + strings.Repeat("a", 32),
	} {
		if _, ok := CredentialID(invalid); ok {
			t.Fatalf("expected invalid token format rejection: %q", invalid)
		}
	}
}
