package auth

import (
	"encoding/base64"
	"errors"
	"regexp"
)

const EnrollmentPrefix = "404p1_"

var (
	agentIDPattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	agentTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// EncodeEnrollment packages the existing Agent ID and credential for the
// Linux installer. It does not create a separate or single-use credential.
func EncodeEnrollment(agentID, token string) (string, error) {
	if !agentIDPattern.MatchString(agentID) || !agentTokenPattern.MatchString(token) {
		return "", errors.New("invalid agent credential")
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(agentID + ":" + token))
	return EnrollmentPrefix + payload, nil
}
