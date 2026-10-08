package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestAgentCountryCodeLookupIsOneShotIdempotentAndPreservesLastSuccess(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	at := time.Now()
	report := validReport(agentID, 1, "session-1", "boot", 1, 10, 20)
	report.Management = &protocol.AgentManagementCapabilities{CountryCodeLookup: true}
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, report, at); err != nil || !accepted {
		t.Fatalf("capability report accepted=%t err=%v", accepted, err)
	}
	first, err := store.CreateAgentCountryCodeLookup(ctx, agentID, at.Add(time.Second))
	if err != nil || first.Status != AgentCountryCodeLookupRequested {
		t.Fatalf("first operation=%+v err=%v", first, err)
	}
	if _, err := store.CreateAgentCountryCodeLookup(ctx, agentID, at.Add(2*time.Second)); !errors.Is(err, ErrAgentCountryLookupPending) {
		t.Fatalf("duplicate pending trigger error=%v", err)
	}
	delivery, err := store.ClaimAgentCountryCodeLookup(ctx, agentID, 1, report.SessionID, at.Add(3*time.Second))
	if err != nil || delivery == nil || delivery.OperationID != first.OperationID || delivery.Status != AgentCountryCodeLookupDelivered {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
	secondDelivery, err := store.ClaimAgentCountryCodeLookup(ctx, agentID, 1, report.SessionID, at.Add(4*time.Second))
	if err != nil || secondDelivery != nil {
		t.Fatalf("one-shot redelivery=%+v err=%v", secondDelivery, err)
	}
	result, err := store.CompleteAgentCountryCodeLookup(ctx, agentID, first.OperationID, 1, report.SessionID, "US", "", at.Add(5*time.Second))
	if err != nil || !result.Accepted || result.Duplicate || result.Status != AgentCountryCodeLookupSucceeded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = store.CompleteAgentCountryCodeLookup(ctx, agentID, first.OperationID, 1, report.SessionID, "US", "", at.Add(6*time.Second))
	if err != nil || !result.Accepted || !result.Duplicate {
		t.Fatalf("duplicate result=%+v err=%v", result, err)
	}
	if _, err := store.CompleteAgentCountryCodeLookup(ctx, agentID, first.OperationID, 1, report.SessionID, "JP", "", at.Add(7*time.Second)); !errors.Is(err, ErrAgentCountryLookupTransition) {
		t.Fatalf("conflicting duplicate result error=%v", err)
	}
	current, exists, err := store.GetAgentCountryCodeLookup(ctx, agentID)
	if err != nil || !exists || current.Status != AgentCountryCodeLookupSucceeded || current.LastCode != "US" {
		t.Fatalf("stored success=%+v exists=%t err=%v", current, exists, err)
	}

	failedRequest, err := store.CreateAgentCountryCodeLookup(ctx, agentID, at.Add(8*time.Second))
	if err != nil || failedRequest.LastCode != "US" {
		t.Fatalf("second trigger=%+v err=%v", failedRequest, err)
	}
	if _, err := store.ClaimAgentCountryCodeLookup(ctx, agentID, 1, report.SessionID, at.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	result, err = store.CompleteAgentCountryCodeLookup(ctx, agentID, failedRequest.OperationID, 1, report.SessionID, "", protocol.CountryCodeLookupFailure, at.Add(10*time.Second))
	if err != nil || result.Status != AgentCountryCodeLookupFailed {
		t.Fatalf("failed lookup result=%+v err=%v", result, err)
	}
	current, exists, err = store.GetAgentCountryCodeLookup(ctx, agentID)
	if err != nil || !exists || current.Status != AgentCountryCodeLookupFailed || current.LastCode != "US" || current.ResultCode != "" {
		t.Fatalf("failed lookup replaced last success: %+v exists=%t err=%v", current, exists, err)
	}
}

func TestAgentCountryCodeLookupRequiresCurrentCapabilityAndFencesOldSession(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	at := time.Now()
	legacy := validReport(agentID, 1, "session-old", "boot", 1, 10, 20)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, legacy, at); err != nil || !accepted {
		t.Fatalf("legacy report accepted=%t err=%v", accepted, err)
	}
	if _, err := store.CreateAgentCountryCodeLookup(ctx, agentID, at.Add(time.Second)); !errors.Is(err, ErrAgentCountryLookupUnsupported) {
		t.Fatalf("legacy trigger error=%v", err)
	}
	capable := validReport(agentID, 1, "session-old", "boot", 2, 11, 21)
	capable.Management = &protocol.AgentManagementCapabilities{CountryCodeLookup: true}
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, capable, at.Add(2*time.Second)); err != nil || !accepted {
		t.Fatalf("capable report accepted=%t err=%v", accepted, err)
	}
	operation, err := store.CreateAgentCountryCodeLookup(ctx, agentID, at.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimAgentCountryCodeLookup(ctx, agentID, 1, capable.SessionID, at.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	newSession := validReport(agentID, 2, "session-new", "boot", 1, 12, 22)
	newSession.Management = &protocol.AgentManagementCapabilities{CountryCodeLookup: true}
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, newSession, at.Add(5*time.Second)); err != nil || !accepted {
		t.Fatalf("new session report accepted=%t err=%v", accepted, err)
	}
	if _, err := store.CompleteAgentCountryCodeLookup(ctx, agentID, operation.OperationID, 1, capable.SessionID, "US", "", at.Add(6*time.Second)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old session result error=%v", err)
	}
}

func TestAgentCountryCodeLookupPendingCanBeManuallySupersededAfterTimeout(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	at := time.Now()
	report := validReport(agentID, 1, "session", "boot", 1, 10, 20)
	report.Management = &protocol.AgentManagementCapabilities{CountryCodeLookup: true}
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, report, at); err != nil || !accepted {
		t.Fatalf("capability report accepted=%t err=%v", accepted, err)
	}
	first, err := store.CreateAgentCountryCodeLookup(ctx, agentID, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateAgentCountryCodeLookup(ctx, agentID, at.Add(time.Second+AgentCountryCodeLookupPendingTTL))
	if err != nil || second.OperationID == first.OperationID {
		t.Fatalf("timed-out one-shot was not superseded: first=%+v second=%+v err=%v", first, second, err)
	}
	if _, exists, err := store.GetAgentCountryCodeLookup(ctx, agentID); err != nil || !exists {
		t.Fatalf("superseding request not persisted: exists=%t err=%v", exists, err)
	}
}
