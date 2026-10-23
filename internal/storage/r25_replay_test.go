package storage

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// The R25 test-bed replay (design 1.5.4): the collector of the test Server wrote every raw sample to raw.csv, the
// lane map to lanes.csv, known gaps to gaps.csv, and the injection script wrote events.log (ground truth). This
// harness aggregates the samples into 1-minute buckets, builds the minute states with the very functions the
// Server uses, runs the one red rule over every minute and prints, per lane and per check interval (every
// sample = 30 s, every 2nd = 60 s, every 10th = 300 s), what turned red and when. It reads files only.
//
//	R25_REPLAY_DIR=<dir with the four files> go test ./internal/storage -run R25Replay -v

type replayLane struct {
	target, slot, family, proto string
	agent                       int
	createdAt                   int64
}

type replaySample struct {
	at              int64
	outcome         string
	success         bool
	latency         float64
	hasLatency      bool
	probeComplete   bool
	connectComplete bool
	dns             bool
	config, slot    int64
	intervalMS      int
}

type replayEvent struct {
	name       string
	begin, end int64
}

func readCSVRows(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, err
	}
	var rows []map[string]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		row := map[string]string{}
		for i, h := range header {
			if i < len(rec) {
				row[h] = rec[i]
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func atoi64(s string) int64 { v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return v }

func readReplayEvents(path string) ([]replayEvent, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	begin := map[string]int64{}
	var out []replayEvent
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		at := atoi64(f[0])
		switch f[1] {
		case "BEGIN":
			begin[f[2]] = at
		case "END":
			if b, ok := begin[f[2]]; ok {
				out = append(out, replayEvent{name: f[2], begin: b, end: at})
			}
		}
	}
	return out, nil
}

// replayBuckets groups the samples of one lane into 1-minute buckets exactly as the ingest aggregates them.
func replayBuckets(samples []replaySample, interval int) map[int64]qualityBucketData {
	out := map[int64]qualityBucketData{}
	for _, s := range samples {
		m := floorDiv64(s.at, qualityMinuteMS)
		b, ok := out[m]
		if !ok {
			b = qualityNewBucket()
		}
		b.Stats.Count++
		if s.dns {
			b.Stats.Categories["dns_error"]++
		}
		if s.connectComplete {
			b.Stats.ConnectAttempts++
			if s.success {
				b.Stats.ConnectSuccesses++
			}
		}
		if s.success && s.probeComplete && s.hasLatency {
			b.Stats.ValidRTTs++
			b.Histogram[uint16(math.Round(s.latency/QualityHistogramGridMS))]++
		}
		if b.ConfigMin == 0 || s.config < b.ConfigMin {
			b.ConfigMin = s.config
		}
		b.ConfigMax = max(b.ConfigMax, s.config)
		if b.SlotMin == 0 || s.slot < b.SlotMin {
			b.SlotMin = s.slot
		}
		b.SlotMax = max(b.SlotMax, s.slot)
		b.IntervalMax = interval
		out[m] = b
	}
	return out
}

type replayResult struct {
	lane    replayLane
	stride  int
	minutes []int64 // minute indexes in the timeline order
	red     map[int64]bool
	first   int64
	tl      qualityTimeline
}

func replayLaneRun(l replayLane, samples []replaySample, gaps []protocol.QualityGap, thresholdMS, stride int) replayResult {
	picked := make([]replaySample, 0, len(samples)/stride+1)
	for i, s := range samples {
		if i%stride == 0 {
			picked = append(picked, s)
		}
	}
	interval := 30000 * stride
	slot := int64(0)
	for _, s := range picked {
		slot = max(slot, s.slot)
	}
	buckets := replayBuckets(picked, interval)
	if len(picked) == 0 {
		return replayResult{lane: l, stride: stride}
	}
	first := floorDiv64(picked[0].at, qualityMinuteMS) - 2
	last := floorDiv64(picked[len(picked)-1].at, qualityMinuteMS) + 3
	frame := qualityLaneFrame{SlotRevision: slot, IntervalStart: picked[0].at - qualityMinuteMS, HasInterval: true, ThresholdMS: thresholdMS, TCP: true}
	tl := qualityBuildTimeline(frame, first, last, buckets, gaps)
	res := replayResult{lane: l, stride: stride, red: map[int64]bool{}, first: first, tl: tl}
	for m := first; m < last; m++ {
		if tl.at(m).Kind != minuteUnusable && tl.redAt(m, interval) {
			res.red[m] = true
		}
		res.minutes = append(res.minutes, m)
	}
	return res
}

func r25Replay(dir string, thresholdMS int, strides []int) (string, error) {
	laneRows, err := readCSVRows(filepath.Join(dir, "lanes.csv"))
	if err != nil {
		return "", err
	}
	lanes := map[string]replayLane{}
	for _, r := range laneRows {
		lanes[r["target_id"]] = replayLane{target: r["target_id"], slot: r["slot"], family: r["family"], proto: r["protocol"], agent: int(atoi64(r["agent_no"])), createdAt: atoi64(r["created_at"])}
	}
	rawRows, err := readCSVRows(filepath.Join(dir, "raw.csv"))
	if err != nil {
		return "", err
	}
	by := map[string][]replaySample{}
	for _, r := range rawRows {
		l, ok := lanes[r["target_id"]]
		if !ok || l.proto != "tcp" {
			continue
		}
		s := replaySample{at: atoi64(r["sample_time"]), outcome: r["outcome"], success: r["outcome"] == "success",
			probeComplete: r["probe_complete"] == "1" || r["probe_complete"] == "true", connectComplete: r["connect_complete"] == "1" || r["connect_complete"] == "true",
			dns: r["outcome"] == "dns_error", config: atoi64(r["config_revision"]), slot: atoi64(r["slot_revision"]), intervalMS: int(atoi64(r["interval_ms"]))}
		if v := strings.TrimSpace(r["latency"]); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err == nil {
				s.latency, s.hasLatency = f, true
			}
		}
		by[r["target_id"]] = append(by[r["target_id"]], s)
	}
	var gapsByAgent = map[int][]protocol.QualityGap{}
	// gaps.csv has no header line: (agent_no, start, end, reason) per row.
	if raw, err := os.ReadFile(filepath.Join(dir, "gaps.csv")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Split(strings.TrimSpace(line), ",")
			if len(f) < 4 || strings.HasPrefix(f[3], "storage_pruned:") {
				continue
			}
			agent := int(atoi64(f[0]))
			gapsByAgent[agent] = append(gapsByAgent[agent], protocol.QualityGap{Start: atoi64(f[1]), End: atoi64(f[2]), Reason: f[3]})
		}
	}
	events, err := readReplayEvents(filepath.Join(dir, "events.log"))
	if err != nil {
		return "", err
	}
	var targets []string
	for id := range by {
		targets = append(targets, id)
	}
	sort.Slice(targets, func(i, j int) bool {
		a, b := lanes[targets[i]], lanes[targets[j]]
		return fmt.Sprint(a.agent, a.family, a.slot) < fmt.Sprint(b.agent, b.family, b.slot)
	})
	var sb strings.Builder
	fmt.Fprintf(&sb, "threshold %d ms; events: %d\n", thresholdMS, len(events))
	for _, stride := range strides {
		fmt.Fprintf(&sb, "\n=== check interval %d s (every %d. sample) ===\n", 30*stride, stride)
		for _, target := range targets {
			l := lanes[target]
			samples := by[target]
			sort.Slice(samples, func(i, j int) bool { return samples[i].at < samples[j].at })
			res := replayLaneRun(l, samples, gapsByAgent[l.agent], thresholdMS, stride)
			if len(res.minutes) == 0 {
				continue
			}
			redMinutes := 0
			for range res.red {
				redMinutes++
			}
			fmt.Fprintf(&sb, "agent#%d %s/%s samples=%d minutes=%d red minutes=%d\n", l.agent, l.slot, l.family, len(samples), len(res.minutes), redMinutes)
			// Which events hit this lane: the samples inside the event are mostly bad.
			covered := map[int64]bool{}
			for _, e := range events {
				begin, end := floorDiv64(e.begin, qualityMinuteMS), ceilDiv64(e.end, qualityMinuteMS)
				bad, total := 0, 0
				for _, s := range samples {
					if s.at >= e.begin && s.at <= e.end {
						total++
						if !s.success || (s.hasLatency && s.latency > float64(thresholdMS)) {
							bad++
						}
					}
				}
				if total == 0 || bad == 0 {
					continue
				}
				first := int64(-1)
				// A red may last as long as the look-back window after the event (design 1.5.3), not longer.
				after := int64(qualityJudgeParamsFor(float64(stride)/2).lookN + 5)
				for m := begin; m <= end+after; m++ {
					if res.red[m] {
						first = m
						break
					}
				}
				for m := begin - 2; m <= end+after; m++ {
					covered[m] = true
				}
				if first < 0 {
					fmt.Fprintf(&sb, "  %-4s bad %d/%d samples  -> never red\n", e.name, bad, total)
				} else {
					fmt.Fprintf(&sb, "  %-4s bad %d/%d samples  -> first red at +%d min (event %.1f min long)\n", e.name, bad, total, first-begin, float64(e.end-e.begin)/60000)
				}
			}
			var outside []int64
			for m := range res.red {
				if !covered[m] {
					outside = append(outside, m)
				}
			}
			sort.Slice(outside, func(i, j int) bool { return outside[i] < outside[j] })
			if len(outside) > 0 {
				start := outside[0]
				prev := outside[0]
				flush := func(a, b int64) {
					fmt.Fprintf(&sb, "  red outside every event: %s .. %s (%d min)\n", time.UnixMilli(a*qualityMinuteMS).UTC().Format("01-02 15:04"), time.UnixMilli(b*qualityMinuteMS).UTC().Format("01-02 15:04"), b-a+1)
				}
				for _, m := range outside[1:] {
					if m != prev+1 {
						flush(start, prev)
						start = m
					}
					prev = m
				}
				flush(start, prev)
			}
		}
	}
	return sb.String(), nil
}

func TestR25ReplayOfTheTestBedData(t *testing.T) {
	dir := os.Getenv("R25_REPLAY_DIR")
	if dir == "" {
		t.Skip("set R25_REPLAY_DIR to a directory with raw.csv, lanes.csv, gaps.csv and events.log")
	}
	threshold := 500
	if v := os.Getenv("R25_THRESHOLD_MS"); v != "" {
		threshold = int(atoi64(v))
	}
	report, err := r25Replay(dir, threshold, []int{1, 2, 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + report)
	if out := os.Getenv("R25_REPLAY_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(report), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The harness itself, on a small synthetic data set in the collector's format: a calm lane with one spike (E1) and
// a dead stretch (E5) must come out as "never red" and "red", and an event window must not hide a red minute that
// lies outside it.
func TestR25ReplayHarnessOnSyntheticFiles(t *testing.T) {
	dir := t.TempDir()
	const t0 = int64(1_790_000_000_000) / 60000 * 60000
	var raw strings.Builder
	raw.WriteString("id,target_id,sample_time,received_at,duration_ms,outcome,success,latency,config_revision,slot_revision,dns_outcome,probe_complete,connect_complete,connect_ms,error_stage,error_code,interval_ms\n")
	id := 0
	add := func(at int64, outcome string, ms float64) {
		id++
		success, latency, connect := 0, "", 1
		if outcome == "success" {
			success, latency = 1, fmt.Sprint(ms)
		}
		fmt.Fprintf(&raw, "%d,tgt1,%d,%d,20,%s,%d,%s,3,2,success,1,%d,,,,30000\n", id, at, at+500, outcome, success, latency, connect)
	}
	for i := int64(0); i < 240; i++ { // two hours of 30 s samples
		at := t0 + i*30000
		switch {
		case i == 100: // E1: one slow sample
			add(at, "success", 1500)
		case i >= 160 && i < 168: // E5: four dead minutes
			add(at, "timeout", 0)
		default:
			add(at, "success", 50)
		}
	}
	files := map[string]string{
		"raw.csv":    raw.String(),
		"lanes.csv":  "target_id,slot,family,protocol,agent_no,created_at,retired_at\ntgt1,telecom,ipv4,tcp,0,0,\n",
		"gaps.csv":   "",
		"events.log": fmt.Sprintf("%d BEGIN E1 netem[x]\n%d END E1\n%d BEGIN E5 netem[x]\n%d END E5\n", t0+100*30000, t0+100*30000+25000, t0+160*30000, t0+168*30000),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := r25Replay(dir, 500, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "E1") || !strings.Contains(report, "never red") || !strings.Contains(report, "E5") || !strings.Contains(report, "first red at +") {
		t.Fatalf("report:\n%s", report)
	}
	if strings.Contains(report, "red outside every event") {
		t.Fatalf("a red minute outside the events:\n%s", report)
	}
}
