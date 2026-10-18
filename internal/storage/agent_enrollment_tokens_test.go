package storage

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"404-probe/internal/auth"
)

func newEnrollmentToken(t *testing.T, s *Store, name string, now time.Time) (id, secret string, hash []byte) {
	t.Helper()
	id, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	secret, hash, err = auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAgentEnrollmentToken(context.Background(), id, name, hash, now); err != nil {
		t.Fatal(err)
	}
	return id, secret, hash
}

func redeemEnrollment(t *testing.T, s *Store, hash []byte, name string, now time.Time) (string, error) {
	t.Helper()
	agentID, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	_, agentHash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return agentID, s.RedeemAgentEnrollmentToken(context.Background(), hash, name, agentID, agentHash, now)
}

func TestEnrollmentTokenIsSingleUseAndStoredHashed(t *testing.T) {
	s, _, _ := testStore(t, filepath.Join(t.TempDir(), "e.db"))
	defer s.Close()
	now := time.Unix(1_800_000_000, 0)
	_, secret, hash := newEnrollmentToken(t, s, "web 1", now)
	var stored []byte
	if err := s.db.QueryRow(`SELECT token_hash FROM agent_enrollment_tokens`).Scan(&stored); err != nil || !bytes.Equal(stored, hash) || bytes.Contains(stored, []byte(secret)) {
		t.Fatalf("stored=%x err=%v", stored, err)
	}
	var plaintext int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_enrollment_tokens WHERE id=? OR name=?`, secret, secret).Scan(&plaintext); err != nil || plaintext != 0 {
		t.Fatalf("secret stored in clear: %d %v", plaintext, err)
	}
	agentID, err := redeemEnrollment(t, s, hash, "web 1", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var name string
	if err := s.db.QueryRow(`SELECT name FROM agents WHERE id=?`, agentID).Scan(&name); err != nil || name != "web 1" {
		t.Fatalf("agent name=%q err=%v", name, err)
	}
	if _, err := redeemEnrollment(t, s, hash, "web 1", now.Add(2*time.Minute)); !errors.Is(err, ErrEnrollmentUsed) {
		t.Fatalf("second redeem err=%v", err)
	}
	if count := countRows(t, s, `SELECT COUNT(*) FROM agents WHERE name='web 1'`); count != 1 {
		t.Fatalf("agents created=%d", count)
	}
}

func TestEnrollmentTokenExpiresAfterThirtyMinutes(t *testing.T) {
	s, _, _ := testStore(t, filepath.Join(t.TempDir(), "e.db"))
	defer s.Close()
	now := time.Unix(1_800_000_000, 0)
	_, _, hash := newEnrollmentToken(t, s, "a", now)
	if _, err := redeemEnrollment(t, s, hash, "a", now.Add(30*time.Minute)); !errors.Is(err, ErrEnrollmentExpired) {
		t.Fatalf("at 30 minutes err=%v", err)
	}
	_, _, hash = newEnrollmentToken(t, s, "b", now)
	if _, err := redeemEnrollment(t, s, hash, "b", now.Add(30*time.Minute-time.Second)); err != nil {
		t.Fatalf("just before expiry err=%v", err)
	}
}

func TestEnrollmentTokenRevokeNameBindingAndUnknown(t *testing.T) {
	s, _, _ := testStore(t, filepath.Join(t.TempDir(), "e.db"))
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	id, _, hash := newEnrollmentToken(t, s, "bound", now)
	if _, err := redeemEnrollment(t, s, hash, "other", now); !errors.Is(err, ErrEnrollmentNameMismatch) {
		t.Fatalf("wrong name err=%v", err)
	}
	// A wrong name does not burn the token.
	if err := s.RevokeAgentEnrollmentToken(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAgentEnrollmentToken(ctx, id, now); err != nil {
		t.Fatalf("second revoke err=%v", err)
	}
	if _, err := redeemEnrollment(t, s, hash, "bound", now); !errors.Is(err, ErrEnrollmentRevoked) {
		t.Fatalf("revoked err=%v", err)
	}
	if _, err := redeemEnrollment(t, s, auth.Hash("never issued"), "bound", now); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("unknown err=%v", err)
	}
	id2, _, hash2 := newEnrollmentToken(t, s, "used", now)
	if _, err := redeemEnrollment(t, s, hash2, "used", now); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAgentEnrollmentToken(ctx, id2, now); !errors.Is(err, ErrEnrollmentUsed) {
		t.Fatalf("revoking a used token err=%v", err)
	}
	if err := s.RevokeAgentEnrollmentToken(ctx, "ffffffffffffffffffffffffffffffff", now); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("revoking an unknown token err=%v", err)
	}
}

func TestEnrollmentTokenPendingLimitAndCleanup(t *testing.T) {
	s, _, _ := testStore(t, filepath.Join(t.TempDir(), "e.db"))
	defer s.Close()
	now := time.Unix(1_800_000_000, 0)
	for i := 0; i < maxPendingEnrollmentTokens; i++ {
		newEnrollmentToken(t, s, "n", now)
	}
	id, _ := auth.NewID()
	_, hash, _ := auth.NewToken()
	if _, err := s.CreateAgentEnrollmentToken(context.Background(), id, "n", hash, now); !errors.Is(err, ErrEnrollmentLimit) {
		t.Fatalf("limit err=%v", err)
	}
	later := now.Add(EnrollmentTokenTTL + enrollmentTokenRetention + time.Hour)
	newEnrollmentToken(t, s, "n", later)
	if count := countRows(t, s, `SELECT COUNT(*) FROM agent_enrollment_tokens`); count != 1 {
		t.Fatalf("old rows kept: %d", count)
	}
}
