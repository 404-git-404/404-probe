package storage

import (
	"context"
	"encoding/json"
	"math"
	"math/rand"
	"testing"
	"time"
)

func p50Bucket(counts map[uint16]uint32) qualityBucketData {
	b := qualityNewBucket()
	for k, n := range counts {
		b.Histogram[k] = n
		b.Stats.ValidRTTs += int(n)
		b.RTTSum += float64(k) * QualityHistogramGridMS * float64(n)
	}
	b.Stats.Count = b.Stats.ValidRTTs
	return b
}

// The mini curve reads p50_ms from the same minute histogram and interpolation as p95_ms.
func TestQualityBucketP50UsesTheSameHistogramAsP95(t *testing.T) {
	// 51 samples at 10ms, 50 at 3000ms: the median sits in the low group, P95 in the high one.
	s := qualityBucketStats(p50Bucket(map[uint16]uint32{40: 51, 12000: 50}))
	if s.P50 == nil || s.P95 == nil || *s.P50 != 10 || *s.P95 != 3000 {
		t.Fatalf("p50=%v p95=%v", s.P50, s.P95)
	}
	// Interpolates between adjacent ranks exactly like P95: 50 at 10ms, 50 at 100ms -> rank 49.5.
	s = qualityBucketStats(p50Bucket(map[uint16]uint32{40: 50, 400: 50}))
	if *s.P50 != 55 {
		t.Fatalf("interpolated p50=%v", *s.P50)
	}
}

func TestQualityBucketP50NullWithoutValidRTT(t *testing.T) {
	b := qualityNewBucket()
	b.Stats.Count = 3
	s := qualityBucketStats(b)
	if s.P50 != nil || s.P95 != nil {
		t.Fatalf("p50=%v p95=%v", s.P50, s.P95)
	}
	raw, err := json.Marshal(QualityRollupPoint{QualityStatsV2: s})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["p50_ms"]; !ok || v != nil {
		t.Fatalf("p50_ms must be present and null: %s", raw)
	}
	if v, ok := m["p95_ms"]; !ok || v != nil {
		t.Fatalf("p95_ms must be present and null: %s", raw)
	}
}

func TestQualityBucketP50NeverExceedsP95(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 500; i++ {
		counts := map[uint16]uint32{}
		for j := rng.Intn(6) + 1; j > 0; j-- {
			counts[uint16(rng.Intn(24000))] += uint32(rng.Intn(40) + 1)
		}
		s := qualityBucketStats(p50Bucket(counts))
		if s.P50 == nil || s.P95 == nil || *s.P50 > *s.P95 || *s.P50 < 0 {
			t.Fatalf("counts=%v p50=%v p95=%v", counts, s.P50, s.P95)
		}
	}
}

func fiveMinBucket(cfg, slot int64, counts map[uint16]uint32) qualityBucketData {
	b := p50Bucket(counts)
	b.ConfigMin, b.ConfigMax, b.SlotMin, b.SlotMax = cfg, cfg, slot, slot
	return b
}

func TestFiveMinuteP50AbsorbsASingleRetransmission(t *testing.T) {
	// Two RTTs per minute, one SYN retransmission (3000ms) in the current minute: the minute's own P50 is the
	// 1575ms average, the five-minute median stays at 150ms.
	var window []qualityBucketData
	for i := 0; i < 4; i++ {
		window = append(window, fiveMinBucket(9, 7, map[uint16]uint32{600: 2}))
	}
	window = append(window, fiveMinBucket(9, 7, map[uint16]uint32{600: 1, 12000: 1}))
	own := qualityBucketStats(window[4])
	got := qualityFiveMinuteP50(window)
	if *own.P50 != 1575 || got == nil || *got != 150 {
		t.Fatalf("own=%v five=%v", *own.P50, got)
	}
}

func TestFiveMinuteP50RisesWhenRetransmissionsAreHalfOrMore(t *testing.T) {
	window := []qualityBucketData{
		fiveMinBucket(9, 7, map[uint16]uint32{600: 2}),
		fiveMinBucket(9, 7, map[uint16]uint32{600: 2}),
		fiveMinBucket(9, 7, map[uint16]uint32{600: 1, 12000: 1}),
		fiveMinBucket(9, 7, map[uint16]uint32{12000: 2}),
		fiveMinBucket(9, 7, map[uint16]uint32{12000: 2}),
	}
	// 5 x 150ms and 5 x 3000ms: rank 4.5 sits between them.
	got := qualityFiveMinuteP50(window)
	if got == nil || *got != 1575 {
		t.Fatalf("five=%v", got)
	}
}

func TestFiveMinuteP50DoesNotCrossRevisionBoundaries(t *testing.T) {
	old := fiveMinBucket(9, 6, map[uint16]uint32{12000: 4}) // earlier slot revision, high RTTs
	oldCfg := fiveMinBucket(8, 7, map[uint16]uint32{12000: 4})
	cur := fiveMinBucket(9, 7, map[uint16]uint32{600: 4})
	got := qualityFiveMinuteP50([]qualityBucketData{old, oldCfg, cur})
	if got == nil || *got != 150 {
		t.Fatalf("revision boundary merged: %v", got)
	}
	mixed := fiveMinBucket(9, 7, map[uint16]uint32{600: 4})
	mixed.SlotMax = 8
	if got = qualityFiveMinuteP50([]qualityBucketData{cur, mixed}); got != nil {
		t.Fatalf("mixed current minute must be null: %v", *got)
	}
	if got = qualityFiveMinuteP50([]qualityBucketData{mixed, cur}); got == nil || *got != 150 {
		t.Fatalf("a mixed earlier minute must be left out: %v", got)
	}
}

func TestFiveMinuteP50IsNullForGapsAndTooFewSamples(t *testing.T) {
	data := fiveMinBucket(9, 7, map[uint16]uint32{600: 2})
	empty := qualityNewBucket()
	if got := qualityFiveMinuteP50([]qualityBucketData{data, data, empty}); got != nil {
		t.Fatalf("current minute without RTT must stay a gap: %v", *got)
	}
	if got := qualityFiveMinuteP50([]qualityBucketData{empty, empty, fiveMinBucket(9, 7, map[uint16]uint32{600: 2})}); got != nil {
		t.Fatalf("two RTTs are too few: %v", *got)
	}
	if got := qualityFiveMinuteP50([]qualityBucketData{empty, data, fiveMinBucket(9, 7, map[uint16]uint32{600: 1})}); got == nil || *got != 150 {
		t.Fatalf("three RTTs are enough: %v", got)
	}
}

func TestFiveMinuteP50ThroughTheHistoryEndpoint(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	target := cfg.Targets[0]
	m0 := now.UnixMilli() / 60000 * 60000
	seq := int64(0)
	for j := int64(5); j >= 1; j-- {
		start := time.UnixMilli(m0 - j*60000)
		second := 150.0
		if j == 1 {
			second = 3000
		}
		seq++
		qualitySave(t, s, id, now, qualitySample(target, cfg.Revision, seq, start.Add(10*time.Second), 150, "success"))
		seq++
		qualitySave(t, s, id, now, qualitySample(target, cfg.Revision, seq, start.Add(40*time.Second), second, "success"))
	}
	h, err := s.QualityHistoryRollup(context.Background(), id, target.ID, "1day", now)
	if err != nil {
		t.Fatal(err)
	}
	byStart := map[int64]QualityRollupPoint{}
	for _, g := range h.Groups {
		for _, p := range g.Points {
			if p.Count > 0 {
				byStart[p.Start] = p
			}
		}
	}
	last, ok := byStart[m0-60000]
	if !ok || last.P50 == nil || last.P95 == nil || last.P50FiveMin == nil {
		t.Fatalf("point %+v", last)
	}
	if math.Abs(*last.P50-1575) > .25 || math.Abs(*last.P50FiveMin-150) > .25 {
		t.Fatalf("p50=%v p50_5m=%v", *last.P50, *last.P50FiveMin)
	}
	if *last.P50FiveMin > *last.P95 {
		t.Fatalf("p50_5m above p95")
	}
	for _, g := range h.Groups {
		for _, p := range g.Points {
			if p.Count == 0 && p.P50FiveMin != nil {
				t.Fatalf("empty minute %d has p50_5m %v", p.Start, *p.P50FiveMin)
			}
		}
	}
	// Other resolutions do not carry the rolling median.
	week, err := s.QualityHistoryRollup(context.Background(), id, target.ID, "1week", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range week.Groups {
		for _, p := range g.Points {
			if p.P50FiveMin != nil {
				t.Fatal("1week point has p50_5m_ms")
			}
		}
	}
}
