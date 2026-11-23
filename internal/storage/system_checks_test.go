package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"404-probe/internal/auth"
)

func TestRegisterServerLocalAgentPreservesIdentityAndSessionFence(t *testing.T) {
	ctx := context.Background()
	s, _, _ := testStore(t, ":memory:")
	defer s.Close()
	now := time.Unix(1800000000, 0)

	register := func(id string, at time.Time) (string, string, uint64) {
		t.Helper()
		token, hash, err := auth.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		actual, epoch, err := s.RegisterServerLocalAgent(ctx, id, "Server", hash, at)
		if err != nil {
			t.Fatal(err)
		}
		return actual, token, epoch
	}
	id, old, epoch := register("local-first", now)
	if id != "local-first" || epoch != 0 {
		t.Fatalf("first registration: %q epoch %d", id, epoch)
	}
	first := validReport(id, 5, "old-session", "same-boot", 1, 100, 200)
	if _, ok, why, err := s.ProcessReport(ctx, id, first, now); err != nil || !ok {
		t.Fatalf("first report: ok=%v reason=%q err=%v", ok, why, err)
	}

	rotated, token, floor := register("local-reinstall", now.Add(time.Minute))
	if rotated != id || floor != 5 {
		t.Fatalf("unexpected reinstall: %q epoch %d", rotated, floor)
	}
	if _, err := s.Authenticate(ctx, old); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old credential: %v", err)
	}
	if got, err := s.Authenticate(ctx, token); err != nil || got != id {
		t.Fatalf("new credential: id=%q err=%v", got, err)
	}
	if _, ok, why, err := s.ProcessReport(ctx, id, first, now.Add(time.Minute)); err != nil || ok || why == "" {
		t.Fatalf("old session fence: ok=%v reason=%q err=%v", ok, why, err)
	}
	fresh := validReport(id, floor+1, "new-session", "same-boot", 1, 130, 240)
	if state, ok, why, err := s.ProcessReport(ctx, id, fresh, now.Add(61*time.Second)); err != nil || !ok || state.RXTotal != 30 {
		t.Fatalf("first reinstall report: ok=%v reason=%q received total=%d err=%v", ok, why, state.RXTotal, err)
	}
	agents, err := s.ListAgents(ctx)
	if err != nil || len(agents) != 2 {
		t.Fatalf("expected original fixture and one Server: %+v (%v)", agents, err)
	}
	if changed, err := s.RevokeAgent(ctx, id, now.Add(2*time.Minute)); err != nil || !changed {
		t.Fatalf("revocation: %v (%v)", changed, err)
	}
	if _, _, err := s.RegisterServerLocalAgent(ctx, "local-revoked-retry", "Server", auth.Hash("synthetic"), now.Add(3*time.Minute)); !errors.Is(err, ErrAgentRevoked) {
		t.Fatalf("revoked identity: %v", err)
	}
}
