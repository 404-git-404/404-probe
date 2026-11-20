package auth

import (
	"crypto/subtle"
	"fmt"
	"unicode"
	"unicode/utf8"
)

const WebPasswordMinRunes = 12
const WebPasswordMaxBytes = 1024

type WebPasswordError struct {
	Code       string
	Characters int
}

func (e *WebPasswordError) Error() string {
	switch e.Code {
	case "too_short":
		return fmt.Sprintf("password must be at least %d characters", WebPasswordMinRunes)
	case "too_long":
		return fmt.Sprintf("password must be at most %d bytes", WebPasswordMaxBytes)
	case "mismatch":
		return "passwords do not match"
	case "same_as_current":
		return "new password must differ from the current password"
	default:
		return "password must be valid UTF-8 without control characters"
	}
}

// ValidateNewWebPassword applies to newly set passwords. current is nil when
// there is no previously verified password, such as initial installation.
func ValidateNewWebPassword(newPassword, confirmation, current []byte) *WebPasswordError {
	if !utf8.Valid(newPassword) {
		return &WebPasswordError{Code: "invalid_chars"}
	}
	for _, r := range string(newPassword) {
		if unicode.IsControl(r) {
			return &WebPasswordError{Code: "invalid_chars"}
		}
	}
	if subtle.ConstantTimeCompare(newPassword, confirmation) != 1 {
		return &WebPasswordError{Code: "mismatch"}
	}
	if len(newPassword) > WebPasswordMaxBytes {
		return &WebPasswordError{Code: "too_long"}
	}
	if count := utf8.RuneCount(newPassword); count < WebPasswordMinRunes {
		return &WebPasswordError{Code: "too_short", Characters: count}
	}
	if current != nil && subtle.ConstantTimeCompare(newPassword, current) == 1 {
		return &WebPasswordError{Code: "same_as_current"}
	}
	return nil
}
