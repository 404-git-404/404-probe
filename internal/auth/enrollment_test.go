package auth

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestEncodeEnrollmentUsesExistingCredentialFormat(t *testing.T) {
	agentID := strings.Repeat("a", 32)
	token := strings.Repeat("A", 43)
	value, err := EncodeEnrollment(agentID, token)
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.TrimPrefix(value, EnrollmentPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || string(decoded) != agentID+":"+token {
		t.Fatalf("enrollment=%q decoded=%q err=%v", value, decoded, err)
	}
}

func TestEncodeEnrollmentRejectsInvalidCredentials(t *testing.T) {
	validID := strings.Repeat("a", 32)
	validToken := strings.Repeat("A", 43)
	for _, test := range []struct {
		id    string
		token string
	}{
		{"", validToken},
		{strings.Repeat("A", 32), validToken},
		{validID, "short"},
		{validID, strings.Repeat("+", 43)},
	} {
		if value, err := EncodeEnrollment(test.id, test.token); err == nil || value != "" {
			t.Fatalf("id=%q token=%q value=%q err=%v", test.id, test.token, value, err)
		}
	}
}
