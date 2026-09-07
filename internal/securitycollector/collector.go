package securitycollector

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

const (
	StateRoot        = "/var/lib/404-probe-security"
	journalctlPath   = "/usr/bin/journalctl"
	journalUnit      = "sing-box.service"
	maxLines         = 100000
	maxBytes         = 64 << 20
	maxLineBytes     = 128 << 10
	maxTrackedIPs    = 4096
	maxOutboxBatches = 30
	commandTimeout   = 90 * time.Second
)

var errCursorInvalid = errors.New("journal cursor is not an exact retained entry")

type checkpoint struct {
	Cursor        string `json:"cursor"`
	LastWindowEnd int64  `json:"last_window_end"`
}

type pendingTransaction struct {
	Batch protocol.SecurityBatch `json:"batch"`
	Next  checkpoint             `json:"next_checkpoint"`
}

type journalEntry struct {
	Unit      string          `json:"_SYSTEMD_UNIT"`
	Cursor    string          `json:"__CURSOR"`
	Timestamp json.RawMessage `json:"__REALTIME_TIMESTAMP"`
	Message   *string         `json:"MESSAGE"`
}

type sourceAccumulator struct {
	count, maxBucket int
	first, last      int64
	buckets          map[int64]int
}

type parseResult struct {
	batch        protocol.SecurityBatch
	cursor       string
	processedEnd int64
}

// Run performs one fixed-purpose, local-only journal audit. The binary accepts
// no remote selectors, network credentials, unit names, commands, or paths.
func Run(ctx context.Context, root string, now time.Time) error {
	if root == "" {
		root = StateRoot
	}
	if err := ensureDirectories(root); err != nil {
		return err
	}
	lock, err := acquireCollectorLock(filepath.Join(root, "private", "collector.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	pendingPath := filepath.Join(root, "private", "pending.json")
	if pending, exists, err := readPending(pendingPath, now); err != nil {
		return err
	} else if exists {
		return commitPending(root, pending)
	}
	state, err := readCheckpoint(filepath.Join(root, "private", "checkpoint.json"))
	if err != nil {
		return err
	}
	start := now.Add(-24 * time.Hour).UnixMilli()
	if state.LastWindowEnd > 0 {
		start = state.LastWindowEnd
	}
	data, overflow, reason, err := readJournal(ctx, state.Cursor, now)
	partialReason := ""
	if errors.Is(err, errCursorInvalid) && state.Cursor != "" {
		data, overflow, reason, err = readJournal(ctx, "", now)
		partialReason = "cursor_invalid_recent_24h"
		start = now.Add(-24 * time.Hour).UnixMilli()
	}
	if err != nil {
		batch := emptyBatch(start, now.UnixMilli(), now.UnixMilli(), protocol.SecurityStatusFailed, reason)
		return stageAndCommit(root, pendingTransaction{Batch: batch, Next: state})
	}
	if len(bytes.TrimSpace(data)) == 0 && state.Cursor == "" {
		batch := emptyBatch(start, now.UnixMilli(), now.UnixMilli(), protocol.SecurityStatusFailed, "no_journal_entries")
		return stageAndCommit(root, pendingTransaction{Batch: batch, Next: state})
	}
	parsed := parseJournal(bytes.NewReader(data), start, now.UnixMilli(), now)
	if overflow {
		markPartial(&parsed.batch, "input_limit")
	}
	if partialReason != "" {
		markPartial(&parsed.batch, partialReason)
	}
	parsed.batch.BatchID = batchID(parsed.batch)
	next := state
	if parsed.cursor != "" {
		next.Cursor = parsed.cursor
	}
	if parsed.processedEnd > 0 {
		next.LastWindowEnd = parsed.processedEnd
	} else if state.Cursor != "" && parsed.batch.Status == protocol.SecurityStatusComplete {
		next.LastWindowEnd = now.UnixMilli()
	}
	return stageAndCommit(root, pendingTransaction{Batch: parsed.batch, Next: next})
}

func ensureDirectories(root string) error {
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{filepath.Join(root, "private"), 0700}, {filepath.Join(root, "private", "outbox"), 0700}, {filepath.Join(root, "export"), 0750}} {
		if err := os.MkdirAll(item.path, item.mode); err != nil {
			return err
		}
		if err := os.Chmod(item.path, item.mode); err != nil {
			return err
		}
	}
	return nil
}

func readJournal(parent context.Context, cursor string, now time.Time) ([]byte, bool, string, error) {
	args := []string{"--output=json", "--quiet", "--no-pager", "--until=@" + strconv.FormatInt(now.Unix(), 10), "_SYSTEMD_UNIT=" + journalUnit}
	if cursor == "" {
		args = append(args, "--since=-24h")
	} else {
		args = append(args, "--cursor="+cursor)
	}
	data, overflow, reason, err := executeJournal(parent, args)
	if err != nil || cursor == "" {
		return data, overflow, reason, err
	}
	line, rest, ok := splitFirstLine(data)
	var anchor journalEntry
	if !ok || json.Unmarshal(line, &anchor) != nil || anchor.Cursor != cursor || anchor.Unit != journalUnit {
		return nil, false, "cursor_invalid_recent_24h", errCursorInvalid
	}
	return rest, overflow, "", nil
}

func splitFirstLine(data []byte) ([]byte, []byte, bool) {
	index := bytes.IndexByte(data, '\n')
	if index < 0 {
		line := bytes.TrimSpace(data)
		return line, nil, len(line) != 0
	}
	return bytes.TrimSpace(data[:index]), data[index+1:], true
}

func executeJournal(parent context.Context, args []string) ([]byte, bool, string, error) {
	ctx, cancel := context.WithTimeout(parent, commandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, journalctlPath, args...)
	stdout := &boundedBuffer{limit: maxBytes}
	stderr := &boundedBuffer{limit: 8 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return stdout.Bytes(), stdout.overflow, "journal_timeout", ctx.Err()
	}
	if err != nil {
		var execError *exec.Error
		if errors.As(err, &execError) || errors.Is(err, os.ErrNotExist) {
			return stdout.Bytes(), stdout.overflow, "journalctl_unavailable", err
		}
		return stdout.Bytes(), stdout.overflow, "journal_read_failed", err
	}
	return stdout.Bytes(), stdout.overflow, "", nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.overflow = true
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return original, nil
}

func parseJournal(reader io.Reader, requestedStart, requestedEnd int64, collectedAt time.Time) parseResult {
	batch := emptyBatch(requestedStart, requestedEnd, collectedAt.UnixMilli(), protocol.SecurityStatusComplete, "")
	tracked := make(map[string]*sourceAccumulator)
	seenCursors := make(map[string]bool)
	buffered := bufio.NewReaderSize(reader, maxLineBytes+1)
	var bytesRead int64
	lastCursor := ""
	processedEnd := int64(0)
	for {
		line, consumed, tooLong, readErr := readBoundedLine(buffered)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			markPartial(&batch, "malformed_or_untrusted_lines")
			break
		}
		if len(line) == 0 && consumed == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		if tooLong {
			batch.ScannedLines++
			if consumed > maxLineBytes {
				consumed = maxLineBytes
			}
			bytesRead += consumed
			markPartial(&batch, "line_size_limit")
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		lineBytes := int64(len(line) + 1)
		if batch.ScannedLines >= maxLines || bytesRead+lineBytes > maxBytes {
			markPartial(&batch, "input_limit")
			break
		}
		batch.ScannedLines++
		bytesRead += lineBytes
		var entry journalEntry
		if json.Unmarshal(line, &entry) != nil || entry.Unit != journalUnit {
			markPartial(&batch, "malformed_or_untrusted_lines")
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		stamp, err := journalTimestampMS(entry.Timestamp)
		if err != nil || stamp <= 0 || stamp > collectedAt.UnixMilli() {
			markPartial(&batch, "malformed_or_untrusted_lines")
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		if entry.Cursor == "" {
			markPartial(&batch, "missing_cursor")
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		if seenCursors[entry.Cursor] {
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		seenCursors[entry.Cursor] = true
		lastCursor = entry.Cursor
		if stamp > processedEnd {
			processedEnd = stamp
		}
		if stamp < batch.WindowStart {
			batch.WindowStart = stamp
		}
		if stamp > batch.WindowEnd {
			batch.WindowEnd = stamp
		}
		if entry.Message == nil {
			markPartial(&batch, "missing_message")
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		ip, reality, valid := sourceIP(*entry.Message)
		if !reality {
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		if !valid {
			markPartial(&batch, "invalid_reality_entry")
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		batch.TotalEvents++
		item := tracked[ip]
		if item == nil {
			if len(tracked) >= maxTrackedIPs {
				batch.DroppedEvents++
				continue
			}
			item = &sourceAccumulator{first: stamp, last: stamp, buckets: make(map[int64]int)}
			tracked[ip] = item
		}
		item.count++
		if stamp < item.first {
			item.first = stamp
		}
		if stamp > item.last {
			item.last = stamp
		}
		bucket := (stamp / (5 * 60 * 1000)) * (5 * 60 * 1000)
		item.buckets[bucket]++
		if item.buckets[bucket] > item.maxBucket {
			item.maxBucket = item.buckets[bucket]
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	batch.ScannedBytes = bytesRead
	batch.TrackedSources = len(tracked)
	if batch.DroppedEvents > 0 {
		markPartial(&batch, "source_limit")
	}
	all := make([]protocol.SecuritySource, 0, len(tracked))
	for ip, item := range tracked {
		all = append(all, protocol.SecuritySource{IP: ip, Count: item.count, FirstSeen: item.first, LastSeen: item.last,
			DurationMS: item.last - item.first, Classifications: classify(item)})
	}
	protocol.SortSecuritySources(all)
	if len(all) > protocol.MaxSecuritySources {
		batch.DroppedSources = len(all) - protocol.MaxSecuritySources
		all = all[:protocol.MaxSecuritySources]
		markPartial(&batch, "source_output_limit")
	}
	batch.Sources = all
	batch.BatchID = batchID(batch)
	return parseResult{batch: batch, cursor: lastCursor, processedEnd: processedEnd}
}

func readBoundedLine(reader *bufio.Reader) ([]byte, int64, bool, error) {
	line, err := reader.ReadSlice('\n')
	consumed := int64(len(line))
	if !errors.Is(err, bufio.ErrBufferFull) {
		return bytes.TrimSuffix(line, []byte{'\n'}), consumed, false, err
	}
	for errors.Is(err, bufio.ErrBufferFull) {
		line, err = reader.ReadSlice('\n')
		consumed += int64(len(line))
	}
	return nil, consumed, true, err
}

func journalTimestampMS(raw json.RawMessage) (int64, error) {
	var text string
	if len(raw) == 0 {
		return 0, errors.New("missing timestamp")
	}
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, err
		}
	} else {
		text = string(raw)
	}
	microseconds, err := strconv.ParseInt(text, 10, 64)
	if err != nil || microseconds <= 0 {
		return 0, errors.New("invalid timestamp")
	}
	return microseconds / 1000, nil
}

func sourceIP(message string) (string, bool, bool) {
	const realityMarker = "REALITY: processed invalid connection"
	realityAt := strings.Index(message, realityMarker)
	if realityAt < 0 {
		return "", false, true
	}
	const connectionMarker = "process connection from "
	connectionAt := strings.LastIndex(message[:realityAt], connectionMarker)
	if connectionAt < 0 {
		return "", true, false
	}
	value := strings.TrimSpace(message[connectionAt+len(connectionMarker) : realityAt])
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return "", true, false
	}
	endpoint := strings.TrimRight(fields[0], ",;")
	if strings.HasSuffix(endpoint, ":") {
		endpoint = endpoint[:len(endpoint)-1]
	}
	host, portText, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", true, false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", true, false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return "", true, false
	}
	return ip.String(), true, true
}

func classify(item *sourceAccumulator) []protocol.SecurityClassification {
	result := make([]protocol.SecurityClassification, 0, 2)
	if item.count >= 20 && item.last-item.first >= int64(time.Hour/time.Millisecond) && len(item.buckets) >= 6 {
		result = append(result, protocol.SecurityPersistent)
	}
	if item.count >= 1000 && item.maxBucket >= 100 {
		result = append(result, protocol.SecurityHighVolume)
	}
	if len(result) == 0 {
		if item.count >= 5 {
			result = append(result, protocol.SecurityRepeated)
		} else {
			result = append(result, protocol.SecurityObserved)
		}
	}
	return result
}

func emptyBatch(start, end, collected int64, status protocol.SecurityStatus, reason string) protocol.SecurityBatch {
	batch := protocol.SecurityBatch{WindowStart: start, WindowEnd: end, CollectedAt: collected, Status: status, Reason: reason, Sources: []protocol.SecuritySource{}}
	batch.BatchID = batchID(batch)
	return batch
}

func markPartial(batch *protocol.SecurityBatch, reason string) {
	if batch.Status != protocol.SecurityStatusFailed {
		batch.Status = protocol.SecurityStatusPartial
	}
	if batch.Reason == "" {
		batch.Reason = reason
	}
}

func batchID(batch protocol.SecurityBatch) string {
	batch.BatchID = ""
	encoded, _ := json.Marshal(batch)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:16])
}

func stageAndCommit(root string, pending pendingTransaction) error {
	data, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(root, "private", "pending.json"), data, 0600); err != nil {
		return err
	}
	return commitPending(root, pending)
}

func commitPending(root string, pending pendingTransaction) error {
	batchData, err := json.Marshal(pending.Batch)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%013d-%s.json", pending.Batch.CollectedAt, pending.Batch.BatchID)
	outboxDir := filepath.Join(root, "private", "outbox")
	exportDir := filepath.Join(root, "export")
	if err := atomicWrite(filepath.Join(outboxDir, name), batchData, 0600); err != nil {
		return err
	}
	checkpointData, _ := json.Marshal(pending.Next)
	if err := atomicWrite(filepath.Join(root, "private", "checkpoint.json"), checkpointData, 0600); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(exportDir, name), batchData, 0640); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(exportDir, "current.json"), batchData, 0640); err != nil {
		return err
	}
	if err := pruneBatches(outboxDir, exportDir); err != nil {
		return err
	}
	return os.Remove(filepath.Join(root, "private", "pending.json"))
}

func readPending(path string, now time.Time) (pendingTransaction, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return pendingTransaction{}, false, nil
	}
	if err != nil {
		return pendingTransaction{}, false, err
	}
	var pending pendingTransaction
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pending); err != nil || pending.Batch.ValidateAt(now) != nil || len(pending.Next.Cursor) > 4096 || pending.Next.LastWindowEnd < 0 {
		return pendingTransaction{}, false, errors.New("invalid pending security transaction")
	}
	return pending, true, nil
}

func readCheckpoint(path string) (checkpoint, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return checkpoint{}, nil
	}
	if err != nil {
		return checkpoint{}, err
	}
	var state checkpoint
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || len(state.Cursor) > 4096 || state.LastWindowEnd < 0 {
		return checkpoint{}, errors.New("invalid security checkpoint")
	}
	return state, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".security-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(mode); err == nil {
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
	if err := replaceFile(temporaryName, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func pruneBatches(outboxDir, exportDir string) error {
	entries, err := os.ReadDir(outboxDir)
	if err != nil {
		return err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	for len(files) > maxOutboxBatches {
		name := files[0]
		if err := os.Remove(filepath.Join(outboxDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_ = os.Remove(filepath.Join(exportDir, name))
		files = files[1:]
	}
	return nil
}
