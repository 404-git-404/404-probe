package server

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"404-probe/internal/storage"
)

func scoreNumber(value float64) *float64 { return &value }

func TestDeviceNetworkScoreComponents(t *testing.T) {
	day := (24 * time.Hour).Milliseconds()
	for _, tc := range []struct {
		name string
		line storage.DeviceScoreLine
		want [5]float64
	}{
		{"best", storage.DeviceScoreLine{FailurePercent: scoreNumber(0), P50: scoreNumber(50), P95: scoreNumber(60), Jitter: scoreNumber(5), Evening: scoreNumber(55)}, [5]float64{100, 100, 100, 100, 100}},
		{"midpoint", storage.DeviceScoreLine{FailurePercent: scoreNumber(5), P50: scoreNumber(325), P95: scoreNumber(601.25), Jitter: scoreNumber(27.5), Evening: scoreNumber(503.75), AlertMS: day / 2}, [5]float64{50, 50, 50, 50, 50}},
		{"worst", storage.DeviceScoreLine{FailurePercent: scoreNumber(10), P50: scoreNumber(600), P95: scoreNumber(1500), Jitter: scoreNumber(50), Evening: scoreNumber(1200), AlertMS: day}, [5]float64{}},
		{"all attempts failed", storage.DeviceScoreLine{FailurePercent: scoreNumber(100)}, [5]float64{0, 0, 0, 0, 100}},
		{"quantized zero", storage.DeviceScoreLine{FailurePercent: scoreNumber(0), P50: scoreNumber(0), P95: scoreNumber(0), Jitter: scoreNumber(0), Evening: scoreNumber(0)}, [5]float64{100, 100, 100, 100, 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i, value := range scoreNetworkLine(tc.line) {
				if math.Abs(value-tc.want[i]) > 1e-9 {
					t.Fatalf("%s got %v, want %v", networkScoreParts[i].Key, value, tc.want[i])
				}
			}
		})
	}
}

func TestDeviceNetworkScoreUsesOneHourAndAveragesLines(t *testing.T) {
	best := storage.DeviceScoreLine{CoveredMS: time.Hour.Milliseconds(), FailurePercent: scoreNumber(0), P50: scoreNumber(50), P95: scoreNumber(60), Jitter: scoreNumber(5), Evening: scoreNumber(50)}
	worst := storage.DeviceScoreLine{CoveredMS: (61 * time.Minute).Milliseconds(), FailurePercent: scoreNumber(10), P50: scoreNumber(600), P95: scoreNumber(1500), Jitter: scoreNumber(50), Evening: scoreNumber(1200), AlertMS: (24 * time.Hour).Milliseconds()}
	short := best
	short.CoveredMS = (59 * time.Minute).Milliseconds()
	if got := scoreMachineNetwork([]storage.DeviceScoreLine{short}, false); got.Score != nil || got.Lines[0].Reason != "line_coverage" {
		t.Fatal(got)
	}
	if got := scoreMachineNetwork([]storage.DeviceScoreLine{short, best, worst}, false); got.Score == nil || *got.Score != 50 || got.Grade != "C" || *got.Parts[1].Score != 50 ||
		got.Lines[1].Score == nil || got.Lines[2].Score == nil {
		t.Fatal(got)
	}
}

func TestDeviceNetworkScoreExcludesMissingEveningPerLine(t *testing.T) {
	best := storage.DeviceScoreLine{CoveredMS: time.Hour.Milliseconds(), FailurePercent: scoreNumber(0),
		P50: scoreNumber(50), P95: scoreNumber(60), Jitter: scoreNumber(5)}
	without := scoreMachineNetwork([]storage.DeviceScoreLine{best}, false)
	if without.Score == nil || *without.Score != 100 || without.Lines[0].Score == nil || *without.Lines[0].Score != 100 {
		t.Fatal(without)
	}
	if part := without.Parts[networkEveningPart]; part.Score != nil || part.EffectiveWeight != 0 || part.Reason != "no_evening_data" {
		t.Fatalf("missing evening: %+v", part)
	}
	bestWithSlowEvening := best
	bestWithSlowEvening.Evening = scoreNumber(100)
	mixed := scoreMachineNetwork([]storage.DeviceScoreLine{best, bestWithSlowEvening}, false)
	if mixed.Score == nil || *mixed.Score != 92.5 || mixed.Lines[1].Score == nil || *mixed.Lines[1].Score != 85 {
		t.Fatal(mixed)
	}
	if part := mixed.Parts[networkEveningPart]; part.Score == nil || *part.Score != 0 || part.EffectiveWeight != 7.5 || part.Reason != "" {
		t.Fatalf("mixed evening: %+v", part)
	}
	var explained float64
	for _, part := range mixed.Parts {
		explained += part.EffectiveWeight * *part.Score / 100
	}
	if math.Abs(explained-*mixed.Score) > 1e-9 {
		t.Fatal(explained, *mixed.Score)
	}
}

func TestDeviceScoreGradeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		value float64
		grade string
	}{{0, "D"}, {49.999, "D"}, {50, "C"}, {64.999, "C"}, {65, "B"}, {74.999, "B"}, {75, "A"}, {84.999, "A"}, {85, "S"}, {89.999, "S"}, {90, "SS"}, {94.999, "SS"}, {95, "SSS"}, {100, "SSS"}} {
		if got := deviceScoreGrade(tc.value); got != tc.grade {
			t.Errorf("grade at %v = %s, want %s", tc.value, got, tc.grade)
		}
	}
}

func TestDeviceOverallScoreRenormalizesAndHandlesNewAndPaused(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	agent := storage.Agent{CreatedAt: now.Add(-time.Minute).UnixMilli()}
	uptime := storage.PresenceHistory{OnlinePercent: scoreNumber(99)}
	resources := &storage.DeviceResourceAverage{CPU: 20, RAM: 40, Disk: 80}
	lines := []storage.DeviceScoreLine{{CoveredMS: time.Hour.Milliseconds(), FailurePercent: scoreNumber(0),
		P50: scoreNumber(50), P95: scoreNumber(60), Jitter: scoreNumber(5), Evening: scoreNumber(50)}}
	for _, tc := range []struct {
		name    string
		inputs  storage.DeviceScoreInputs
		score   float64
		weights [3]float64
	}{
		{"all parts", storage.DeviceScoreInputs{Lines: lines, Presence: uptime, Resources: resources}, 86, [3]float64{50, 30, 20}},
		{"no card lines", storage.DeviceScoreInputs{Presence: uptime, Resources: resources}, 72, [3]float64{0, 60, 40}},
		{"only resources", storage.DeviceScoreInputs{Resources: resources}, 60, [3]float64{0, 0, 100}},
		{"only uptime", storage.DeviceScoreInputs{Presence: uptime}, 80, [3]float64{0, 100, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scoreOverall(agent, tc.inputs)
			if got.Overall.Score == nil || *got.Overall.Score != tc.score || len(got.Overall.Parts) != len(tc.weights) {
				t.Fatalf("overall = %+v, want score %v", got.Overall, tc.score)
			}
			for i, part := range got.Overall.Parts {
				if part.EffectiveWeight != tc.weights[i] || part.Weight != [...]int{50, 30, 20}[i] {
					t.Errorf("part %q = %+v", part.Key, part)
				}
			}
		})
	}
	if got := scoreOverall(agent, storage.DeviceScoreInputs{}); got.Overall.Score != nil || got.Overall.Reason != "no_history" {
		t.Fatalf("empty inputs = %+v", got)
	}
	agent.DisabledAt = &agent.CreatedAt
	if got := scoreOverall(agent, storage.DeviceScoreInputs{Resources: resources}); got.Overall.Score != nil || got.Overall.Reason != "paused" || got.Network.Reason != "paused" {
		t.Fatal(got)
	}
}

func TestDeviceValueScoreUsesOwnerRatesAndRecurringPlans(t *testing.T) {
	data := expenseFixture([3]string{"84", "yearly", "CNY"}, [3]string{"2", "monthly", "USD"},
		[3]string{"2", "monthly", "USD"}, [3]string{"4", "monthly", "USD"},
		[3]string{"4", "monthly", "USD"}, [3]string{"50", "once", "USD"},
		[3]string{"0", "monthly", "USD"}, [3]string{"2", "monthly", "JPY"},
		[3]string{"", "", ""}, [3]string{"1", "monthly", "USD"})
	id := func(i int) string { return data.Records[i].Snapshot.Agent.ID }
	scores := make(map[string]deviceScore)
	for i, overall := range []float64{65, 80, 80, 80, 80, 100, 100, 100, 100} {
		scores[id(i)] = deviceScore{Overall: scoreSummary{Score: scoreNumber(overall)}}
	}
	scores[id(9)] = deviceScore{Overall: scoreSummary{Reason: "no_history"}}
	got := scoreDeviceValues(data, scores)
	for i, want := range []struct {
		rank  int
		score float64
	}{{1, 100}, {2, 100.0 * 2 / 3}, {2, 100.0 * 2 / 3}, {4, 0}, {4, 0}} {
		value := got[id(i)]
		if value.score == nil || *value.score != want.score || value.rank != want.rank || value.count != 5 {
			t.Errorf("device %d: %+v, want rank %d with score %v", i, value, want.rank, want.score)
		}
	}
	for i, reason := range []string{"once", "invalid_price", "missing_rate", "missing_price", "no_history"} {
		if value := got[id(i+5)]; value.score != nil || value.rank != 0 || value.reason != reason {
			t.Errorf("device %d: %+v, want %q", i+5, value, reason)
		}
	}
	allTied := data
	allTied.Records = data.Records[1:3]
	for _, value := range scoreDeviceValues(allTied, scores) {
		if value.rank != 1 || value.count != 2 || value.score == nil || *value.score != 50 {
			t.Errorf("all tied: %+v, want shared first rank and the existing midpoint score", value)
		}
	}
	data.Records = data.Records[:1]
	if only := scoreDeviceValues(data, scores)[id(0)]; only.score != nil || only.reason != "fewer_than_two" {
		t.Fatal(only)
	}
}

func TestDeviceValueHighValueBoundaries(t *testing.T) {
	ranked := expenseFixture([3]string{"1", "monthly", "USD"}, [3]string{"2", "monthly", "USD"}, [3]string{"3", "monthly", "USD"},
		[3]string{"4", "monthly", "USD"}, [3]string{"5", "monthly", "USD"}, [3]string{"6", "monthly", "USD"})
	scores := make(map[string]deviceScore)
	for _, record := range ranked.Records {
		scores[record.Snapshot.Agent.ID] = deviceScore{Overall: scoreSummary{Score: scoreNumber(65)}}
	}
	value := scoreDeviceValues(ranked, scores)[ranked.Records[1].Snapshot.Agent.ID]
	if value.score == nil || *value.score != 80 {
		t.Fatalf("second of six = %+v, want 80", value)
	}
	for _, tc := range []struct {
		overall, value *float64
		want           bool
	}{
		{scoreNumber(65), value.score, true},
		{scoreNumber(64.999), value.score, false},
		{scoreNumber(65), scoreNumber(79.999), false},
		{scoreNumber(100), nil, false},
		{nil, value.score, false},
	} {
		device := deviceScore{Overall: scoreSummary{Score: tc.overall}}
		device.setValueRanking(deviceValueScore{score: tc.value})
		if device.HighValue != tc.want {
			t.Errorf("high_value = %t, want %t for %+v", device.HighValue, tc.want, tc)
		}
	}
}

func TestDeviceScoreAPIUsesAuthenticatedCache(t *testing.T) {
	a, store, id, _ := testApp(t)
	defer store.Close()
	defer a.Shutdown()
	now := a.now()
	snapshot, err := a.calculateDeviceScores(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	a.deviceScores.Store(snapshot)
	if res := organizationHTTP(t, a, "GET", "/api/v1/web/scores", "", false, false); res.Code != 401 {
		t.Fatalf("unauthenticated request = %d", res.Code)
	}
	res := organizationHTTP(t, a, "GET", "/api/v1/web/scores", "", true, false)
	var got deviceScoreSnapshot
	if err := json.Unmarshal(res.Body.Bytes(), &got); res.Code != 200 || err != nil {
		t.Fatalf("response %d %s: %v", res.Code, res.Body, err)
	}
	device, ok := got.Items[id]
	if !ok || got.ComputedAt != now.UnixMilli() || device.Network.Score != nil || len(device.Overall.Parts) != 3 || len(device.Network.Parts) != 5 ||
		device.ValueScore != nil || device.HighValue || device.ValueReason != "missing_price" {
		t.Fatalf("unexpected snapshot %+v", got)
	}
}
