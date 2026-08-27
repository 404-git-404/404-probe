package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashRoundTripAndCanonicalValidation(t *testing.T) {
	password := []byte("correct horse battery staple")
	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if err := ParsePasswordHash(encoded); err != nil {
		t.Fatalf("parse generated hash: %v", err)
	}
	if !VerifyPassword(encoded, password) || VerifyPassword(encoded, []byte("wrong")) {
		t.Fatal("password verification mismatch")
	}
	for _, invalid := range []string{
		"", " " + encoded, encoded + "\n", strings.Replace(encoded, "m=19456", "m=1", 1),
		strings.Replace(encoded, "$argon2id$", "$argon2i$", 1), encoded + "=",
	} {
		if err := ParsePasswordHash(invalid); err == nil || VerifyPassword(invalid, password) {
			t.Fatalf("accepted invalid hash %q", invalid)
		}
	}
}

func TestPasswordHashRejectsInvalidPasswordLengths(t *testing.T) {
	if _, err := HashPassword(nil); err == nil {
		t.Fatal("empty password accepted")
	}
	if _, err := HashPassword(make([]byte, 1025)); err == nil {
		t.Fatal("oversized password accepted")
	}
}
