package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func agentSecurityBatch(now time.Time) protocol.SecurityBatch {
	start := now.Add(-time.Hour).UnixMilli()
	end := now.Add(-time.Minute).UnixMilli()
	return protocol.SecurityBatch{BatchID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", WindowStart: start, WindowEnd: end, CollectedAt: now.UnixMilli(), Status: protocol.SecurityStatusComplete,
		TotalEvents: 1, TrackedSources: 1, ScannedLines: 1, ScannedBytes: 256,
		Sources: []protocol.SecuritySource{{IP: "2001:db8::1", Count: 1, FirstSeen: end, LastSeen: end, DurationMS: 0, Classifications: []protocol.SecurityClassification{protocol.SecurityObserved}}}}
}

func TestSecurityUploaderResubmitsNewestAcknowledgedBatchForNewSession(t *testing.T) {
	var requests atomic.Int32
	var got protocol.SecuritySubmission
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(protocol.SecurityResponse{Accepted: true})
	}))
	defer server.Close()
	directory := t.TempDir()
	export := filepath.Join(directory, "export")
	if err := os.Mkdir(export, 0750); err != nil {
		t.Fatal(err)
	}
	batch := agentSecurityBatch(time.Now().Truncate(time.Second))
	data, _ := json.Marshal(batch)
	if err := os.WriteFile(filepath.Join(export, "0000000000001-"+batch.BatchID+".json"), data, 0640); err != nil {
		t.Fatal(err)
	}
	ackPath := filepath.Join(directory, "acks.json")
	ackData, _ := json.Marshal(securityAcknowledgements{BatchIDs: []string{batch.BatchID}, LastCollectedAt: batch.CollectedAt})
	if err := os.WriteFile(ackPath, ackData, 0600); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{config: Config{ServerURL: server.URL, Token: "token", SecurityExportDir: export, SecurityAckPath: ackPath}, client: server.Client(), epoch: 2, sessionID: "new-session"}
	runner.uploadSecurity(context.Background())
	if requests.Load() != 1 || got.AgentEpoch != 2 || got.SessionID != "new-session" || got.Batch == nil || got.Batch.BatchID != batch.BatchID {
		t.Fatalf("requests=%d submission=%+v", requests.Load(), got)
	}
}

func TestSecurityWorkerDoesNotUploadWhenCurrentCapabilityIsFalse(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	ready := make(chan struct{})
	close(ready)
	runner := &Runner{config: Config{ServerURL: server.URL, Token: "token", SecurityExportDir: t.TempDir(), SecurityAckPath: filepath.Join(t.TempDir(), "acks"), SecurityInterval: 5 * time.Millisecond}, client: server.Client(), securityReady: ready}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runner.runSecurityWorker(ctx)
	if requests.Load() != 0 {
		t.Fatalf("uploads without current capability=%d", requests.Load())
	}
}

func TestSecurityUploaderReportsPrunedCoverageWithoutMutatingBatch(t *testing.T) {
	var got protocol.SecuritySubmission
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(protocol.SecurityResponse{Accepted: true})
	}))
	defer server.Close()
	directory := t.TempDir()
	export := filepath.Join(directory, "export")
	if err := os.Mkdir(export, 0750); err != nil {
		t.Fatal(err)
	}
	batch := agentSecurityBatch(time.Now().Truncate(time.Second))
	data, _ := json.Marshal(batch)
	if err := os.WriteFile(filepath.Join(export, "0000000000001-"+batch.BatchID+".json"), data, 0640); err != nil {
		t.Fatal(err)
	}
	previous := batch.CollectedAt - int64((40*24*time.Hour)/time.Millisecond)
	ackPath := filepath.Join(directory, "acks.json")
	ackData, _ := json.Marshal(securityAcknowledgements{LastCollectedAt: previous})
	if err := os.WriteFile(ackPath, ackData, 0600); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{config: Config{ServerURL: server.URL, Token: "token", SecurityExportDir: export, SecurityAckPath: ackPath}, client: server.Client(), epoch: 2, sessionID: "session"}
	runner.uploadSecurity(context.Background())
	if got.Batch == nil || got.Batch.BatchID != batch.BatchID || got.Batch.Status != protocol.SecurityStatusComplete {
		t.Fatalf("collector batch was mutated: %+v", got.Batch)
	}
	if got.Delivery == nil || !got.Delivery.OutboxGap || got.Delivery.PreviousCollectedAt != previous {
		t.Fatalf("delivery coverage=%+v", got.Delivery)
	}
}

func TestSecurityUploaderDoesNotReportGapForDelayedCatchupWindow(t *testing.T) {
	var got protocol.SecuritySubmission
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(protocol.SecurityResponse{Accepted: true})
	}))
	defer server.Close()
	directory := t.TempDir()
	export := filepath.Join(directory, "export")
	if err := os.Mkdir(export, 0750); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	previous := now.Add(-48 * time.Hour).UnixMilli()
	batch := agentSecurityBatch(now)
	batch.WindowStart = previous - int64(time.Minute/time.Millisecond)
	batch.Sources[0].FirstSeen = batch.WindowStart
	batch.Sources[0].DurationMS = batch.Sources[0].LastSeen - batch.Sources[0].FirstSeen
	data, _ := json.Marshal(batch)
	if err := os.WriteFile(filepath.Join(export, "0000000000001-"+batch.BatchID+".json"), data, 0640); err != nil {
		t.Fatal(err)
	}
	ackPath := filepath.Join(directory, "acks.json")
	ackData, _ := json.Marshal(securityAcknowledgements{LastCollectedAt: previous})
	if err := os.WriteFile(ackPath, ackData, 0600); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{config: Config{ServerURL: server.URL, Token: "token", SecurityExportDir: export, SecurityAckPath: ackPath}, client: server.Client(), epoch: 2, sessionID: "session"}
	runner.uploadSecurity(context.Background())
	if got.Batch == nil || got.Delivery != nil {
		t.Fatalf("delayed catchup incorrectly reported a gap: %+v", got)
	}
}

func TestReadSecurityBatchRejectsTrailingData(t *testing.T) {
	batch := agentSecurityBatch(time.Now().Truncate(time.Second))
	data, _ := json.Marshal(batch)
	path := filepath.Join(t.TempDir(), "batch.json")
	data = append(data, []byte(" unexpected trailing data")...)
	if err := os.WriteFile(path, data, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecurityBatch(path); err == nil {
		t.Fatal("accepted trailing security export data")
	}
}
