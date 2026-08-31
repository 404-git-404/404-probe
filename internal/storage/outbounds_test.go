package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestOutboundSnapshotLatestSuccessAndUnavailableState(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	first := protocol.OutboundSnapshot{Available: true, Selectors: []protocol.OutboundSelector{{Name: "select", Current: "a", Choices: []string{"a", "b"}}}}
	if err := store.SaveOutboundSnapshot(ctx, agentID, first, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOutboundSnapshot(ctx, agentID, protocol.OutboundSnapshot{Available: false}, time.Unix(210, 0)); err != nil {
		t.Fatal(err)
	}
	got, configured, err := store.GetOutboundSnapshot(ctx, agentID)
	if err != nil || !configured || got.Available || got.CheckedAt != time.Unix(210, 0).UnixMilli() || got.UpdatedAt == nil || *got.UpdatedAt != time.Unix(200, 0).UnixMilli() || len(got.Selectors) != 1 {
		t.Fatalf("snapshot=%+v configured=%t err=%v", got, configured, err)
	}
	replacement := protocol.OutboundSnapshot{Available: true, Selectors: []protocol.OutboundSelector{{Name: "other", Current: "x", Choices: []string{"x"}}}}
	if err := store.SaveOutboundSnapshot(ctx, agentID, replacement, time.Unix(220, 0)); err != nil {
		t.Fatal(err)
	}
	got, _, _ = store.GetOutboundSnapshot(ctx, agentID)
	if len(got.Selectors) != 1 || got.Selectors[0].Name != "other" {
		t.Fatalf("snapshot was not replaced: %+v", got)
	}
}

func TestOutboundSnapshotHonorsAgentState(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	value := protocol.OutboundSnapshot{Available: false}
	if err := store.SaveOutboundSnapshot(ctx, "missing", value, time.Now()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing error=%v", err)
	}
	if changed, err := store.DisableAgent(ctx, agentID, time.Now()); err != nil || !changed {
		t.Fatalf("disable=%t err=%v", changed, err)
	}
	if err := store.SaveOutboundSnapshot(ctx, agentID, value, time.Now()); !errors.Is(err, ErrAgentDisabled) {
		t.Fatalf("disabled error=%v", err)
	}
}
