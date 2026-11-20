package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewWebPasswordValidatesUnicodeAndControls(t *testing.T) {
	const pass = " a synthetic 密码 with spaces "
	for _, tc := range []struct {
		name, code string
		value      []byte
	}{
		{"valid", "", []byte(pass)},
		{"valid replacement character", "", []byte(pass + "\uFFFD")},
		{"malformed UTF-8", "invalid_chars", append([]byte(pass), 0xff)},
		{"C0 control", "invalid_chars", append([]byte(pass), 0)},
		{"C1 control", "invalid_chars", []byte(pass + "\u0085")},
		{"exact byte limit", "", bytes.Repeat([]byte("ab"), 512)},
		{"above byte limit", "too_long", bytes.Repeat([]byte("a"), 1025)},
		{"short code point count", "too_short", []byte(strings.Repeat("密", 11))},
		{"11 ASCII", "too_short", []byte(strings.Repeat("a", 11))},
		{"12 ASCII", "", []byte(strings.Repeat("a", 12))},
		{"12 Unicode", "", []byte(strings.Repeat("密", 12))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := ValidateNewWebPassword(tc.value, tc.value, nil)
			var code string
			if result != nil {
				code = result.Code
			}
			if code != tc.code {
				t.Fatalf("got code %q, want %q", code, tc.code)
			}
		})
	}
}
