package storage

import (
	"encoding/json"
	"math/rand"
	"testing"
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
