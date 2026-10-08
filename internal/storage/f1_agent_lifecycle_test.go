package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestF1ActiveAgentGuardPreservesLifecycleAndUnderlyingErrors(t *testing.T) {
	for _, state := range []string{"active", "missing", "disabled", "revoked", "canceled"} {
		t.Run(state, func(t *testing.T) {
			store, id, _ := testStore(t, ":memory:")
			defer store.Close()
			ctx := context.Background()
			var want error
			switch state {
			case "missing":
				id, want = "missing", ErrUnauthorized
			case "disabled":
				if _, err := store.DisableAgent(ctx, id, time.Now()); err != nil {
					t.Fatal(err)
				}
				want = ErrAgentDisabled
			case "revoked":
				if _, err := store.RevokeAgent(ctx, id, time.Now()); err != nil {
					t.Fatal(err)
				}
				want = ErrAgentRevoked
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if state == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			err = requireActiveAgentTx(ctx, tx, id)
			// Exact identity distinguishes the wrapped lifecycle sentinels from
			// their Unauthorized base; errors.Is remains backward compatible.
			if err != want {
				t.Fatalf("got=%v want exact=%v", err, want)
			}
			if (state == "disabled" || state == "revoked") && !errors.Is(err, ErrUnauthorized) {
				t.Fatal("Unauthorized compatibility lost")
			}
			if state == "canceled" && errors.Is(err, ErrUnauthorized) {
				t.Fatal("underlying error misclassified as authentication")
			}
		})
	}
}
