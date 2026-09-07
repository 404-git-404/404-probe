package securitycollector

import (
	"bytes"
	"encoding/json"
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
