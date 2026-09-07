package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func validSecuritySubmission(now time.Time) SecuritySubmission {
	start := now.Add(-time.Hour).UnixMilli()
	end := now.Add(-time.Minute).UnixMilli()
	return SecuritySubmission{ProtocolVersion: SecurityProtocolVersion, AgentEpoch: 2, SessionID: "session", Status: SecurityStatusComplete,
		Batch: &SecurityBatch{BatchID: "0123456789abcdef0123456789abcdef", WindowStart: start, WindowEnd: end, CollectedAt: now.UnixMilli(),
			Status: SecurityStatusComplete, TotalEvents: 5, TrackedSources: 1, ScannedLines: 6, ScannedBytes: 900,
			Sources: []SecuritySource{{IP: "192.0.2.1", Count: 5, FirstSeen: start, LastSeen: end, DurationMS: end - start, Classifications: []SecurityClassification{SecurityRepeated}}}}}
}

func TestSecuritySubmissionStrictDecodeAndBounds(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	valid := validSecuritySubmission(now)
	data, _ := json.Marshal(valid)
	if _, err := DecodeSecuritySubmission(data); err != nil {
		t.Fatalf("decode valid: %v", err)
	}
	if err := valid.ValidateAt(now); err != nil {
		t.Fatalf("valid submission rejected: %v", err)
	}
	for _, suffix := range []string{" {}", " trailing"} {
		if _, err := DecodeSecuritySubmission(append(data, suffix...)); err == nil {
			t.Fatalf("accepted trailing data %q", suffix)
		}
	}
	invalid := valid
	invalid.Batch = cloneSecurityBatch(valid.Batch)
	invalid.Batch.CollectedAt = now.Add(25 * time.Hour).UnixMilli()
	if invalid.ValidateAt(now) == nil {
		t.Fatal("accepted future collection")
	}
	invalid = valid
	invalid.Batch = cloneSecurityBatch(valid.Batch)
	invalid.Batch.Reason = "free form"
	if invalid.ValidateAt(now) == nil {
		t.Fatal("accepted untyped reason")
	}
}

func TestSecurityDeliveryCoverageIsIndependentAndValidated(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	valid := validSecuritySubmission(now)
	valid.Delivery = &SecurityDelivery{OutboxGap: true, PreviousCollectedAt: now.Add(-48 * time.Hour).UnixMilli()}
	if err := valid.ValidateAt(now); err != nil {
		t.Fatalf("valid delivery coverage rejected: %v", err)
	}
	withoutBatch := valid
	withoutBatch.Batch = nil
	withoutBatch.Status = SecurityStatusFailed
	withoutBatch.Reason = "outbox_gap"
	if withoutBatch.ValidateAt(now) == nil {
		t.Fatal("accepted delivery coverage without immutable batch")
	}
	invalid := valid
	invalid.Delivery = &SecurityDelivery{OutboxGap: false, PreviousCollectedAt: now.Add(-48 * time.Hour).UnixMilli()}
	if invalid.ValidateAt(now) == nil {
		t.Fatal("accepted delivery metadata without a gap")
	}
}

func TestSecurityBatchRejectsDuplicateNonCanonicalAndInconsistentSources(t *testing.T) {
	now := time.Now()
	valid := validSecuritySubmission(now)
	for name, mutate := range map[string]func(*SecurityBatch){
		"duplicate": func(batch *SecurityBatch) {
			batch.Sources = append(batch.Sources, batch.Sources[0])
			batch.TrackedSources = 2
		},
		"noncanonical":            func(batch *SecurityBatch) { batch.Sources[0].IP = "192.0.2.01" },
		"count":                   func(batch *SecurityBatch) { batch.Sources[0].Count = batch.TotalEvents + 1 },
		"complete drop":           func(batch *SecurityBatch) { batch.DroppedEvents = 1 },
		"complete missing source": func(batch *SecurityBatch) { batch.TotalEvents++ },
	} {
		t.Run(name, func(t *testing.T) {
			batch := cloneSecurityBatch(valid.Batch)
			mutate(batch)
			if batch.ValidateAt(now) == nil {
				t.Fatal("invalid batch accepted")
			}
		})
	}
}

func cloneSecurityBatch(batch *SecurityBatch) *SecurityBatch {
	copyValue := *batch
	copyValue.Sources = append([]SecuritySource(nil), batch.Sources...)
	for index := range copyValue.Sources {
		copyValue.Sources[index].Classifications = append([]SecurityClassification(nil), batch.Sources[index].Classifications...)
	}
	return &copyValue
}
