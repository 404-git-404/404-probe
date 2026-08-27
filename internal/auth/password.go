package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	passwordHashMemory      = 19 * 1024
	passwordHashIterations  = 2
	passwordHashParallelism = 1
	passwordHashSaltBytes   = 16
	passwordHashBytes       = 32
)

// HashPassword creates the canonical Argon2id PHC string accepted by
// ParsePasswordHash and VerifyPassword.
func HashPassword(password []byte) (string, error) {
	if len(password) == 0 || len(password) > 1024 {
		return "", errors.New("password must be between 1 and 1024 bytes")
	}
	salt := make([]byte, passwordHashSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey(password, salt, passwordHashIterations, passwordHashMemory, passwordHashParallelism, passwordHashBytes)
	return encodePasswordHash(salt, hash), nil
}

// ParsePasswordHash validates the exact password-hash format supported by the
// Web authentication boundary. Restricting parameters also prevents a trusted
// configuration mistake from turning login into an unbounded resource cost.
func ParsePasswordHash(encoded string) error {
	_, _, err := decodePasswordHash(encoded)
	return err
}

// VerifyPassword performs one Argon2id calculation for every well-formed
// configured hash. Callers must rate-limit attempts before invoking it.
func VerifyPassword(encoded string, password []byte) bool {
	if len(password) == 0 || len(password) > 1024 {
		return false
	}
	salt, expected, err := decodePasswordHash(encoded)
	if err != nil {
		return false
	}
	candidate := argon2.IDKey(password, salt, passwordHashIterations, passwordHashMemory, passwordHashParallelism, passwordHashBytes)
	return subtle.ConstantTimeCompare(candidate, expected) == 1
}

func encodePasswordHash(salt, hash []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		passwordHashMemory, passwordHashIterations, passwordHashParallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))
}

func decodePasswordHash(encoded string) ([]byte, []byte, error) {
	if len(encoded) == 0 || len(encoded) > 1024 || strings.TrimSpace(encoded) != encoded {
		return nil, nil, errors.New("invalid password hash")
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) ||
		parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", passwordHashMemory, passwordHashIterations, passwordHashParallelism) {
		return nil, nil, errors.New("invalid password hash")
	}
	salt, err := decodeCanonicalPasswordBase64(parts[4], passwordHashSaltBytes)
	if err != nil {
		return nil, nil, errors.New("invalid password hash")
	}
	hash, err := decodeCanonicalPasswordBase64(parts[5], passwordHashBytes)
	if err != nil {
		return nil, nil, errors.New("invalid password hash")
	}
	return salt, hash, nil
}

func decodeCanonicalPasswordBase64(value string, size int) ([]byte, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(decoded) != size || base64.RawStdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("invalid base64")
	}
	return decoded, nil
}
