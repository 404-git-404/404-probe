package securitycollector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func journalLine(cursor string, at time.Time, message string) string {
	encoded, _ := json.Marshal(message)
	value := journalEntry{Unit: journalUnit, Cursor: cursor, Timestamp: json.RawMessage(`"` + strconv.FormatInt(at.UnixMicro(), 10) + `"`), Message: encoded}
	data, _ := json.Marshal(value)
	return string(data)
}

func journalByteArrayLine(cursor string, at time.Time, message string) string {
	values := make([]int, len(message))
	for index := 0; index < len(message); index++ {
		values[index] = int(message[index])
	}
	encoded, _ := json.Marshal(values)
	value := journalEntry{Unit: journalUnit, Cursor: cursor, Timestamp: json.RawMessage(`"` + strconv.FormatInt(at.UnixMicro(), 10) + `"`), Message: encoded}
	data, _ := json.Marshal(value)
	return string(data)
}

func reality(ipPort string) string {
	return "INFO inbound: process connection from " + ipPort + ": REALITY: processed invalid connection"
}

func TestParseJournalRequiresRealityAndHandlesIPv6AndDuplicateCursor(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	input := strings.Join([]string{
		journalLine("c1", now.Add(-time.Minute), reality("192.0.2.1:443")),
		journalLine("c1", now.Add(-time.Minute), reality("192.0.2.1:443")),
		journalLine("c2", now.Add(-30*time.Second), "INFO process connection from 192.0.2.2:443"),
		journalLine("c3", now.Add(-time.Second), reality("[2001:db8::1]:8443")),
	}, "\n")
	parsed := parseJournal(strings.NewReader(input), now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), now)
	if parsed.batch.Status != protocol.SecurityStatusComplete || parsed.batch.TotalEvents != 2 || len(parsed.batch.Sources) != 2 || parsed.cursor != "c3" {
		t.Fatalf("parsed=%+v", parsed)
	}
	if parsed.batch.Sources[0].IP == "" || parsed.batch.Sources[1].IP == "" {
		t.Fatal("missing canonical IP")
	}
}

func TestParseJournalAcceptsJournaldByteArrayMessage(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	message := "测试 \x1b[31mERROR\x1b[0m inbound: process connection from 192.0.2.8:443: REALITY: processed invalid connection"
	parsed := parseJournal(strings.NewReader(journalByteArrayLine("c1", now.Add(-time.Second), message)), now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), now)
	if parsed.batch.Status != protocol.SecurityStatusComplete || parsed.batch.TotalEvents != 1 || len(parsed.batch.Sources) != 1 || parsed.batch.Sources[0].IP != "192.0.2.8" {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestParseJournalRejectsNullJournaldByteArrayElementsAndContinues(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, message := range []string{"[null]", "[65,null,66]"} {
		t.Run(message, func(t *testing.T) {
			invalid := `{"_SYSTEMD_UNIT":"sing-box.service","__CURSOR":"c1","__REALTIME_TIMESTAMP":"` + strconv.FormatInt(now.Add(-time.Minute).UnixMicro(), 10) + `","MESSAGE":` + message + `}`
			input := invalid + "\n" + journalLine("c2", now.Add(-time.Second), reality("192.0.2.10:443"))
			parsed := parseJournal(strings.NewReader(input), now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), now)
			if parsed.batch.Status != protocol.SecurityStatusPartial || parsed.batch.Reason != "malformed_or_untrusted_lines" || parsed.batch.TotalEvents != 1 || parsed.cursor != "c2" {
				t.Fatalf("parsed=%+v", parsed)
			}
		})
	}
}

func TestParseJournalRejectsInvalidJournaldByteArrayAndContinues(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	invalid := `{"_SYSTEMD_UNIT":"sing-box.service","__CURSOR":"c1","__REALTIME_TIMESTAMP":"` + strconv.FormatInt(now.Add(-time.Minute).UnixMicro(), 10) + `","MESSAGE":[256]}`
	input := invalid + "\n" + journalLine("c2", now.Add(-time.Second), reality("192.0.2.9:443"))
	parsed := parseJournal(strings.NewReader(input), now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), now)
	if parsed.batch.Status != protocol.SecurityStatusPartial || parsed.batch.Reason != "malformed_or_untrusted_lines" || parsed.batch.TotalEvents != 1 || parsed.cursor != "c2" {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestParseJournalContinuesAfterMalformedAndMissingCursor(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	input := strings.Join([]string{
		journalLine("c1", now.Add(-time.Minute), reality("192.0.2.1:443")),
		`{"broken":`,
		journalLine("", now.Add(-40*time.Second), reality("192.0.2.3:443")),
		journalLine("c4", now.Add(-time.Second), reality("192.0.2.4:443")),
	}, "\n")
	parsed := parseJournal(strings.NewReader(input), now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), now)
	if parsed.batch.Status != protocol.SecurityStatusPartial || parsed.batch.Reason != "malformed_or_untrusted_lines" || parsed.cursor != "c4" || parsed.batch.TotalEvents != 2 {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestClassificationThresholds(t *testing.T) {
	persistent := &sourceAccumulator{count: 2573, first: 0, last: int64(649 * time.Minute / time.Millisecond), buckets: make(map[int64]int), maxBucket: 97}
	for index := 0; index < 130; index++ {
		persistent.buckets[int64(index)] = 1
	}
	classes := classify(persistent)
	if len(classes) != 1 || classes[0] != protocol.SecurityPersistent {
		t.Fatalf("fixture classes=%v", classes)
	}
	high := &sourceAccumulator{count: 1000, first: 0, last: int64(2 * time.Hour / time.Millisecond), buckets: map[int64]int{0: 100}, maxBucket: 100}
	for index := 1; index < 6; index++ {
		high.buckets[int64(index)] = 1
	}
	classes = classify(high)
	if len(classes) != 2 || classes[0] != protocol.SecurityPersistent || classes[1] != protocol.SecurityHighVolume {
		t.Fatalf("synthetic classes=%v", classes)
	}
}

func TestMissingMessageAndInvalidRealityArePartial(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	missing := journalEntry{Unit: journalUnit, Cursor: "c1", Timestamp: json.RawMessage(`"` + strconv.FormatInt(now.Add(-time.Second).UnixMicro(), 10) + `"`)}
	missingJSON, _ := json.Marshal(missing)
	invalid := journalLine("c2", now, "process connection from bad:0: REALITY: processed invalid connection")
	parsed := parseJournal(strings.NewReader(string(missingJSON)+"\n"+invalid), now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), now)
	if parsed.batch.Status != protocol.SecurityStatusPartial || parsed.batch.Reason != "missing_message" || parsed.batch.TotalEvents != 0 || parsed.cursor != "c2" {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestHighVolumeBoundary(t *testing.T) {
	for _, test := range []struct {
		count int
		high  bool
	}{{999, false}, {1000, true}} {
		item := &sourceAccumulator{count: test.count, first: 0, last: int64(time.Minute / time.Millisecond), buckets: map[int64]int{0: test.count}, maxBucket: test.count}
		classes := classify(item)
		got := false
		for _, class := range classes {
			got = got || class == protocol.SecurityHighVolume
		}
		if got != test.high {
			t.Fatalf("count=%d classes=%v", test.count, classes)
		}
	}
}

func TestPendingRecoveryKeepsStableBatchAndCheckpoint(t *testing.T) {
	root := t.TempDir()
	if err := ensureDirectories(root); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	batch := emptyBatch(now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), now.UnixMilli(), protocol.SecurityStatusComplete, "")
	pending := pendingTransaction{Batch: batch, Next: checkpoint{Cursor: "cursor-next", LastWindowEnd: now.UnixMilli()}}
	data, _ := json.Marshal(pending)
	if err := atomicWrite(filepath.Join(root, "private", "pending.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := commitPending(root, pending); err != nil {
		t.Fatal(err)
	}
	exported, err := os.ReadFile(filepath.Join(root, "export", "current.json"))
	if err != nil || !bytes.Contains(exported, []byte(batch.BatchID)) {
		t.Fatalf("export=%s err=%v", exported, err)
	}
	state, err := readCheckpoint(filepath.Join(root, "private", "checkpoint.json"))
	if err != nil || state.Cursor != "cursor-next" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if _, err := os.Stat(filepath.Join(root, "private", "pending.json")); !os.IsNotExist(err) {
		t.Fatalf("pending remains: %v", err)
	}
}

func TestPruneBatchesRetainsThirtyAndRemovesOnlyLegalOrphanMirrors(t *testing.T) {
	root := t.TempDir()
	if err := ensureDirectories(root); err != nil {
		t.Fatal(err)
	}
	outboxDir := filepath.Join(root, "private", "outbox")
	exportDir := filepath.Join(root, "export")
	names := make([]string, 34)
	for index := range names {
		name := testBatchFilename(int64(index+1), index+1)
		names[index] = name
		writeTestFile(t, filepath.Join(outboxDir, name), []byte(name))
		writeTestFile(t, filepath.Join(exportDir, name), []byte(name))
	}

	legalOrphan := testBatchFilename(8_000_000_000_000, 800)
	writeTestFile(t, filepath.Join(exportDir, legalOrphan), []byte("orphan"))
	current := []byte("current mirror stays untouched")
	writeTestFile(t, filepath.Join(exportDir, "current.json"), current)
	unknown := filepath.Join(exportDir, "not-a-batch.json")
	writeTestFile(t, unknown, []byte("unknown"))
	invalidHex := filepath.Join(exportDir, fmt.Sprintf("%013d-%s.json", 8_000_000_000_001, strings.Repeat("A", 32)))
	writeTestFile(t, invalidHex, []byte("uppercase ID"))
	temporary := filepath.Join(exportDir, testBatchFilename(8_000_000_000_002, 802)+".tmp")
	writeTestFile(t, temporary, []byte("temporary"))
	directoryName := testBatchFilename(8_000_000_000_003, 803)
	directory := filepath.Join(exportDir, directoryName)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(directory, "keep"), []byte("directory entry"))
	outboxNoise := filepath.Join(outboxDir, "outbox-not-a-batch.json")
	writeTestFile(t, outboxNoise, []byte("unknown outbox data"))

	if err := pruneBatches(outboxDir, exportDir); err != nil {
		t.Fatal(err)
	}
	gotBatches := regularBatchNames(t, outboxDir)
	if len(gotBatches) != maxOutboxBatches {
		t.Fatalf("retained outbox batches=%d want %d (%v)", len(gotBatches), maxOutboxBatches, gotBatches)
	}
	for _, name := range names[:4] {
		assertMissingPath(t, filepath.Join(outboxDir, name))
		assertMissingPath(t, filepath.Join(exportDir, name))
	}
	for _, name := range names[4:] {
		assertRegularPath(t, filepath.Join(outboxDir, name))
		assertRegularPath(t, filepath.Join(exportDir, name))
	}
	assertMissingPath(t, filepath.Join(exportDir, legalOrphan))
	for _, path := range []string{unknown, invalidHex, temporary, outboxNoise} {
		assertRegularPath(t, path)
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		t.Fatalf("legal-looking directory was not preserved: info=%v err=%v", info, err)
	}
	assertRegularPath(t, filepath.Join(directory, "keep"))
	gotCurrent, err := os.ReadFile(filepath.Join(exportDir, "current.json"))
	if err != nil || !bytes.Equal(gotCurrent, current) {
		t.Fatalf("current mirror=%q err=%v", gotCurrent, err)
	}
}

func TestPruneExportFailurePreservesPendingAndOutboxThenRecovers(t *testing.T) {
	root, pending, names := setupPendingPruneFixture(t)
	outboxDir := filepath.Join(root, "private", "outbox")
	exportDir := filepath.Join(root, "export")
	blockedMirror := filepath.Join(exportDir, names[0])
	if err := os.Remove(blockedMirror); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blockedMirror, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(blockedMirror, "obstruction"), []byte("keep"))

	if err := commitPending(root, pending); err == nil {
		t.Fatal("non-regular matching export entry did not stop pruning")
	}
	assertRegularPath(t, filepath.Join(outboxDir, names[0]))
	assertRegularPath(t, filepath.Join(root, "private", "pending.json"))
	if info, err := os.Stat(blockedMirror); err != nil || !info.IsDir() {
		t.Fatalf("blocking export entry changed: info=%v err=%v", info, err)
	}

	if err := os.Remove(filepath.Join(blockedMirror, "obstruction")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blockedMirror); err != nil {
		t.Fatal(err)
	}
	recovered, exists, err := readPending(filepath.Join(root, "private", "pending.json"), time.Now())
	if err != nil || !exists {
		t.Fatalf("pending exists=%v err=%v", exists, err)
	}
	if err := commitPending(root, recovered); err != nil {
		t.Fatalf("retry commitPending: %v", err)
	}
	assertMissingPath(t, filepath.Join(outboxDir, names[0]))
	assertMissingPath(t, blockedMirror)
	assertMissingPath(t, filepath.Join(root, "private", "pending.json"))
	if got := regularBatchNames(t, outboxDir); len(got) != maxOutboxBatches {
		t.Fatalf("retry retained %d outbox batches want %d", len(got), maxOutboxBatches)
	}
}

func TestPruneOutboxDeleteFailureLeavesPendingAndRetryConverges(t *testing.T) {
	root, pending, names := setupPendingPruneFixture(t)
	outboxDir := filepath.Join(root, "private", "outbox")
	exportDir := filepath.Join(root, "export")
	failedRemoval := filepath.Join(outboxDir, names[0])
	injectedErr := errors.New("injected outbox removal failure")
	injected := false
	remove := func(path string) error {
		if path == failedRemoval {
			injected = true
			return injectedErr
		}
		return os.Remove(path)
	}
	if err := commitPendingWithRemove(root, pending, remove); !errors.Is(err, injectedErr) {
		t.Fatalf("commit error=%v want injected outbox removal failure", err)
	}
	if !injected {
		t.Fatal("outbox failure injection was not reached")
	}
	assertMissingPath(t, filepath.Join(exportDir, names[0]))
	assertRegularPath(t, failedRemoval)
	assertRegularPath(t, filepath.Join(root, "private", "pending.json"))

	recovered, exists, err := readPending(filepath.Join(root, "private", "pending.json"), time.Now())
	if err != nil || !exists {
		t.Fatalf("pending exists=%v err=%v", exists, err)
	}
	if err := commitPending(root, recovered); err != nil {
		t.Fatalf("retry commitPending: %v", err)
	}
	assertMissingPath(t, failedRemoval)
	assertMissingPath(t, filepath.Join(root, "private", "pending.json"))
	assertRegularPath(t, filepath.Join(exportDir, "current.json"))
	if got := regularBatchNames(t, outboxDir); len(got) != maxOutboxBatches {
		t.Fatalf("retry retained %d outbox batches want %d", len(got), maxOutboxBatches)
	}
}

func TestPruneOrphanExportFailureLeavesPendingForRetry(t *testing.T) {
	root, pending, _ := setupPendingPruneFixture(t)
	exportDir := filepath.Join(root, "export")
	orphan := testBatchFilename(9_000_000_000_000, 900)
	orphanPath := filepath.Join(exportDir, orphan)
	writeTestFile(t, orphanPath, []byte("orphan"))
	injectedErr := errors.New("injected orphan removal failure")
	injected := false
	remove := func(path string) error {
		if path == orphanPath {
			injected = true
			return injectedErr
		}
		return os.Remove(path)
	}
	if err := commitPendingWithRemove(root, pending, remove); !errors.Is(err, injectedErr) {
		t.Fatalf("commit error=%v want injected orphan removal failure", err)
	}
	if !injected {
		t.Fatal("orphan failure injection was not reached")
	}
	assertRegularPath(t, orphanPath)
	assertRegularPath(t, filepath.Join(root, "private", "pending.json"))

	recovered, exists, err := readPending(filepath.Join(root, "private", "pending.json"), time.Now())
	if err != nil || !exists {
		t.Fatalf("pending exists=%v err=%v", exists, err)
	}
	if err := commitPending(root, recovered); err != nil {
		t.Fatalf("retry commitPending: %v", err)
	}
	assertMissingPath(t, orphanPath)
	assertMissingPath(t, filepath.Join(root, "private", "pending.json"))
	if got := regularBatchNames(t, filepath.Join(root, "private", "outbox")); len(got) != maxOutboxBatches {
		t.Fatalf("retry retained %d outbox batches want %d", len(got), maxOutboxBatches)
	}
}

func setupPendingPruneFixture(t *testing.T) (string, pendingTransaction, []string) {
	t.Helper()
	root := t.TempDir()
	if err := ensureDirectories(root); err != nil {
		t.Fatal(err)
	}
	collectedAt := time.Now().UnixMilli()
	outboxDir := filepath.Join(root, "private", "outbox")
	exportDir := filepath.Join(root, "export")
	names := make([]string, 0, maxOutboxBatches+1)
	for index := 0; index < maxOutboxBatches; index++ {
		name := testBatchFilename(collectedAt-int64(maxOutboxBatches-index), index+1)
		names = append(names, name)
		writeTestFile(t, filepath.Join(outboxDir, name), []byte(name))
		writeTestFile(t, filepath.Join(exportDir, name), []byte(name))
	}
	batch := emptyBatch(collectedAt-int64(time.Hour/time.Millisecond), collectedAt, collectedAt, protocol.SecurityStatusComplete, "")
	pending := pendingTransaction{Batch: batch, Next: checkpoint{Cursor: "cursor-next", LastWindowEnd: collectedAt}}
	name := fmt.Sprintf("%013d-%s.json", pending.Batch.CollectedAt, pending.Batch.BatchID)
	names = append(names, name)
	data, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(root, "private", "pending.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return root, pending, names
}

func testBatchFilename(collectedAt int64, id int) string {
	return fmt.Sprintf("%013d-%032x.json", collectedAt, id)
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func assertMissingPath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %s exists or could not be inspected: %v", path, err)
	}
}

func assertRegularPath(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("path %s is not a regular file: info=%v err=%v", path, info, err)
	}
}

func regularBatchNames(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0)
	for _, entry := range entries {
		if !isBatchFilename(entry.Name()) {
			continue
		}
		regular, err := isRegularFile(entry)
		if err != nil {
			t.Fatal(err)
		}
		if regular {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestConvertedOverlappingExportFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "reality-overlapping-export.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	zone := time.FixedZone("fixture", -7*60*60)
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, zone)
	end := time.Date(2026, 9, 5, 10, 50, 0, 0, zone)
	parsed := parseJournal(bytes.NewReader(data), start.UnixMilli(), end.UnixMilli(), end)
	if parsed.batch.Status != protocol.SecurityStatusComplete || parsed.batch.TotalEvents != 2586 || parsed.batch.TrackedSources != 2 || len(parsed.batch.Sources) != 2 {
		t.Fatalf("batch status=%s events=%d tracked=%d sources=%d reason=%s", parsed.batch.Status, parsed.batch.TotalEvents, parsed.batch.TrackedSources, len(parsed.batch.Sources), parsed.batch.Reason)
	}
	main := parsed.batch.Sources[0]
	if main.IP != "64.83.31.13" || main.Count != 2573 || len(main.Classifications) != 1 || main.Classifications[0] != protocol.SecurityPersistent {
		t.Fatalf("main source=%+v", main)
	}
}
