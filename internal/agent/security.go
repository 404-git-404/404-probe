package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

type securityAcknowledgements struct {
	BatchIDs        []string `json:"batch_ids"`
	LastCollectedAt int64    `json:"last_collected_at,omitempty"`
}

func (r *Runner) runSecurityWorker(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-r.securityReady:
	}
	ticker := time.NewTicker(r.config.SecurityInterval)
	defer ticker.Stop()
	for {
		if r.securitySupported.Load() {
			r.uploadSecurity(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) uploadSecurity(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if r.deferredUnsupported {
		if err := r.postSecurity(ctx, protocol.SecuritySubmission{ProtocolVersion: protocol.SecurityProtocolVersion, AgentEpoch: r.epoch, SessionID: r.sessionID, Status: protocol.SecurityStatusUnavailable, Reason: "platform_unsupported"}); err != nil && ctx.Err() == nil {
			r.logger.Warn("publish platform security status failed", "error", err)
		}
		return
	}
	files, err := os.ReadDir(r.config.SecurityExportDir)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		r.postSecurity(ctx, protocol.SecuritySubmission{ProtocolVersion: protocol.SecurityProtocolVersion, AgentEpoch: r.epoch, SessionID: r.sessionID, Status: protocol.SecurityStatusUnavailable, Reason: "setup_required"})
		return
	}
	if err != nil {
		r.logger.Warn("read security export", "error", err)
		r.postSecurity(ctx, protocol.SecuritySubmission{ProtocolVersion: protocol.SecurityProtocolVersion, AgentEpoch: r.epoch, SessionID: r.sessionID, Status: protocol.SecurityStatusFailed, Reason: "export_read_failed"})
		return
	}
	names := make([]string, 0, len(files))
	for _, file := range files {
		if !file.IsDir() && file.Name() != "current.json" && strings.HasSuffix(file.Name(), ".json") {
			names = append(names, file.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		r.postSecurity(ctx, protocol.SecuritySubmission{ProtocolVersion: protocol.SecurityProtocolVersion, AgentEpoch: r.epoch, SessionID: r.sessionID, Status: protocol.SecurityStatusNoData, Reason: "awaiting_first_collection"})
		return
	}
	ackState := r.readSecurityAcknowledgements()
	acks := make(map[string]bool, len(ackState.BatchIDs))
	for _, id := range ackState.BatchIDs {
		acks[id] = true
	}
	batches := make([]protocol.SecurityBatch, len(names))
	firstPending := -1
	for index, name := range names {
		batch, err := readSecurityBatch(filepath.Join(r.config.SecurityExportDir, name))
		if err != nil {
			r.logger.Warn("ignore invalid security export", "file", name, "error", err)
			r.postSecurity(ctx, protocol.SecuritySubmission{ProtocolVersion: protocol.SecurityProtocolVersion, AgentEpoch: r.epoch, SessionID: r.sessionID, Status: protocol.SecurityStatusFailed, Reason: "invalid_export"})
			return
		}
		batches[index] = batch
		if firstPending < 0 && !acks[batch.BatchID] {
			firstPending = index
		}
	}
	for index := range names {
		if ctx.Err() != nil {
			return
		}
		batch := batches[index]
		// Always re-submit the newest durable aggregate. This registers the
		// capability and current pointer for a new Agent epoch even when the
		// same batch was acknowledged by the previous process.
		if acks[batch.BatchID] && index != len(names)-1 {
			continue
		}
		submission := protocol.SecuritySubmission{ProtocolVersion: protocol.SecurityProtocolVersion, AgentEpoch: r.epoch, SessionID: r.sessionID, Status: batch.Status, Reason: batch.Reason, Batch: &batch}
		// A delayed collector can legitimately catch up a long interval in one
		// batch. Report a delivery gap only when the earliest retained batch
		// starts after the last acknowledged coverage point.
		if index == firstPending && ackState.LastCollectedAt > 0 && batch.WindowStart > ackState.LastCollectedAt {
			submission.Delivery = &protocol.SecurityDelivery{OutboxGap: true, PreviousCollectedAt: ackState.LastCollectedAt}
		}
		if err := r.postSecurity(ctx, submission); err != nil {
			if ctx.Err() == nil {
				r.logger.Warn("security upload failed; durable export retained", "batch_id", batch.BatchID, "error", err)
			}
			return
		}
		acks[batch.BatchID] = true
		if batch.CollectedAt > ackState.LastCollectedAt {
			ackState.LastCollectedAt = batch.CollectedAt
		}
		if err := r.writeSecurityAcknowledgements(acks, ackState.LastCollectedAt); err != nil {
			r.logger.Warn("record security acknowledgement", "error", err)
			return
		}
	}
}

func readSecurityBatch(path string) (protocol.SecurityBatch, error) {
	file, err := os.Open(path)
	if err != nil {
		return protocol.SecurityBatch{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, protocol.MaxSecurityBodyBytes+1))
	if err != nil || len(data) > protocol.MaxSecurityBodyBytes {
		return protocol.SecurityBatch{}, errors.New("security export exceeds limit")
	}
	var batch protocol.SecurityBatch
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&batch); err != nil {
		return protocol.SecurityBatch{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return protocol.SecurityBatch{}, errors.New("invalid trailing security export data")
	}
	if err := batch.Validate(); err != nil {
		return protocol.SecurityBatch{}, err
	}
	return batch, nil
}

func (r *Runner) postSecurity(ctx context.Context, submission protocol.SecuritySubmission) error {
	body, err := json.Marshal(submission)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(r.config.ServerURL, "/") + "/api/v1/agent/security"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+r.config.Token)
	response, err := doAgentServerRequest(r.client, request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(limited) > 4096 {
		return errors.New("invalid security response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("server returned %s", response.Status)
	}
	var acknowledgement protocol.SecurityResponse
	if json.Unmarshal(limited, &acknowledgement) != nil || !acknowledgement.Accepted {
		return errors.New("security submission was not acknowledged")
	}
	return nil
}

func (r *Runner) readSecurityAcknowledgements() securityAcknowledgements {
	result := securityAcknowledgements{}
	file, err := os.Open(r.config.SecurityAckPath)
	if err != nil {
		return result
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || json.Unmarshal(data, &result) != nil || result.LastCollectedAt < 0 || len(result.BatchIDs) > 90 {
		return securityAcknowledgements{}
	}
	return result
}

func (r *Runner) writeSecurityAcknowledgements(acks map[string]bool, lastCollectedAt int64) error {
	files, _ := os.ReadDir(r.config.SecurityExportDir)
	ids := make([]string, 0, len(files))
	for _, file := range files {
		name := file.Name()
		if file.IsDir() || name == "current.json" || !strings.HasSuffix(name, ".json") {
			continue
		}
		batch, err := readSecurityBatch(filepath.Join(r.config.SecurityExportDir, name))
		if err == nil && acks[batch.BatchID] {
			ids = append(ids, batch.BatchID)
		}
	}
	sort.Strings(ids)
	data, _ := json.Marshal(securityAcknowledgements{BatchIDs: ids, LastCollectedAt: lastCollectedAt})
	directory := filepath.Dir(r.config.SecurityAckPath)
	temporary, err := os.CreateTemp(directory, ".security-acks-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, r.config.SecurityAckPath)
}
