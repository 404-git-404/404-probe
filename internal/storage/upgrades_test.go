package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUpgradeOperationLifecycleAndSingleActiveInvariant(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(1000, 0)
	operation := UpgradeOperation{OperationID: "0123456789abcdef0123456789abcdef", AgentID: agentID, FromVersion: "v0.8.0", TargetVersion: "v0.8.1", CreatedAt: at.UnixMilli()}
	created, err := store.CreateUpgrade(ctx, operation)
	if err != nil || created.Status != UpgradeRequested {
		t.Fatalf("create=%+v err=%v", created, err)
	}
	conflict := operation
	conflict.OperationID = "abcdef0123456789abcdef0123456789"
	if _, err := store.CreateUpgrade(ctx, conflict); !errors.Is(err, ErrUpgradeConflict) {
		t.Fatalf("duplicate active error=%v", err)
	}
	claimed, err := store.ClaimUpgrade(ctx, agentID, at.Add(time.Second))
	if err != nil || claimed == nil || claimed.Status != UpgradeClaimed || claimed.StartedAt == nil {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if _, err := store.UpdateUpgradeStatus(ctx, agentID, operation.OperationID, UpgradeInstalling, "", "", at.Add(2*time.Second)); !errors.Is(err, ErrUpgradeTransition) {
		t.Fatalf("skipped stage error=%v", err)
	}
	for index, status := range []UpgradeStatus{UpgradeDownloading, UpgradeVerifying, UpgradeStaging, UpgradeInstalling, UpgradeRestarting, UpgradeHealthCheck, UpgradeSucceeded} {
		updated, err := store.UpdateUpgradeStatus(ctx, agentID, operation.OperationID, status, "", "", at.Add(time.Duration(index+2)*time.Second))
		if err != nil || updated.Status != status {
			t.Fatalf("status=%s operation=%+v err=%v", status, updated, err)
		}
	}
	finished, err := store.GetUpgrade(ctx, operation.OperationID)
	if err != nil || finished.FinishedAt == nil || finished.Status != UpgradeSucceeded {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	if _, err := store.CreateUpgrade(ctx, conflict); err != nil {
		t.Fatalf("new operation after terminal state: %v", err)
	}
}

func TestUpgradeFailureOwnershipAndMessagePersistence(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(2000, 0)
	operation := UpgradeOperation{OperationID: "0123456789abcdef0123456789abcdef", AgentID: agentID, FromVersion: "v0.8.0", TargetVersion: "v0.8.1", CreatedAt: at.UnixMilli()}
	if _, err := store.CreateUpgrade(ctx, operation); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimUpgrade(ctx, agentID, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateUpgradeStatus(ctx, "other-agent", operation.OperationID, UpgradeFailed, "download_failed", "network unavailable", at.Add(2*time.Second)); !errors.Is(err, ErrUpgradeNotFound) {
		t.Fatalf("cross-agent update error=%v", err)
	}
	failed, err := store.UpdateUpgradeStatus(ctx, agentID, operation.OperationID, UpgradeFailed, "download_failed", "network unavailable", at.Add(3*time.Second))
	if err != nil || failed.FailureCode != "download_failed" || failed.FailureMessage != "network unavailable" || failed.FinishedAt == nil {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
}
