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

func TestDeviceNetworkScoreUsesHalfWindowAndAveragesLines(t *testing.T) {
	half := deviceScoreWindow.Milliseconds() / 2
	best := storage.DeviceScoreLine{CoveredMS: half, FailurePercent: scoreNumber(0), P50: scoreNumber(50), P95: scoreNumber(60), Jitter: scoreNumber(5), Evening: scoreNumber(50)}
	worst := storage.DeviceScoreLine{CoveredMS: half + 1, FailurePercent: scoreNumber(10), P50: scoreNumber(600), P95: scoreNumber(1500), Jitter: scoreNumber(50), Evening: scoreNumber(1200), AlertMS: (24 * time.Hour).Milliseconds()}
	short := best
	short.CoveredMS = half - 1
	if got := scoreMachineNetwork([]storage.DeviceScoreLine{short}, false); got.Score != nil || got.Lines[0].Reason != "line_coverage" {
		t.Fatal(got)
	}
	if got := scoreMachineNetwork([]storage.DeviceScoreLine{short, best, worst}, false); got.Score == nil || *got.Score != 50 || got.Grade != "C" || *got.Parts[1].Score != 50 {
		t.Fatal(got)
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
	agent := storage.Agent{CreatedAt: now.Add(-8 * 24 * time.Hour).UnixMilli()}
	for _, value := range []float64{0, 50, 100} {
		inputs := storage.DeviceScoreInputs{
			Presence:  storage.PresenceHistory{OnlinePercent: scoreNumber(95 + value/20)},
			Resources: &storage.DeviceResourceAverage{CPU: 100 - value, RAM: 100 - value, Disk: 100 - value},
		}
		got := scoreOverall(agent, inputs, deviceValueScore{reason: "once"}, now)
		if got.Overall.Score == nil || *got.Overall.Score != value || got.Overall.Parts[1].EffectiveWeight != 62.5 || got.Overall.Parts[3].EffectiveWeight != 37.5 {
			t.Fatalf("value %v, got %+v", value, got.Overall)
		}
		if got.Overall.Parts[2].Reason != "once" || got.Overall.Parts[0].Reason != "no_card_lines" {
			t.Fatal(got.Overall.Parts)
		}
	}
	inputs := storage.DeviceScoreInputs{Resources: &storage.DeviceResourceAverage{CPU: 20, RAM: 40, Disk: 80}}
	if got := scoreOverall(agent, inputs, deviceValueScore{}, now); *got.Overall.Score != 60 {
		t.Fatal(got)
	}
	agent.CreatedAt = now.Add(-time.Hour).UnixMilli()
	if got := scoreOverall(agent, inputs, deviceValueScore{}, now); got.Overall.Score != nil || got.Overall.Reason != "new_machine" {
		t.Fatal(got)
	}
	agent.DisabledAt = &agent.CreatedAt
	if got := scoreOverall(agent, inputs, deviceValueScore{}, now); got.Overall.Score != nil || got.Overall.Reason != "paused" || got.Network.Reason != "paused" {
		t.Fatal(got)
	}
}

func TestDeviceValueScoreUsesOwnerRatesAndRecurringPlans(t *testing.T) {
	data := expenseFixture([3]string{"84", "yearly", "CNY"}, [3]string{"2", "monthly", "USD"},
		[3]string{"4", "monthly", "USD"}, [3]string{"50", "once", "USD"})
	quota := uint64(12e12)
	for i := range data.Records {
		data.Records[i].Snapshot.State = &storage.State{CPUCores: 1, RAMTotal: 1 << 30, DiskTotal: 20 << 30}
		data.Records[i].Plan.QuotaBytes = &quota
		data.Records[i].Plan.CycleKind = "yearly"
	}
	got := scoreDeviceValues(data)
	for i, want := range []float64{100, 50, 0} {
		score := got[data.Records[i].Snapshot.Agent.ID]
		if score.score == nil || *score.score != want || score.rank != i+1 || score.count != 3 {
			t.Fatalf("rank %d: %+v", i, score)
		}
	}
	if once := got[data.Records[3].Snapshot.Agent.ID]; once.score != nil || once.reason != "once" {
		t.Fatal(once)
	}
	if q := monthlyTrafficTB(data.Records[0].Plan); q != 1 {
		t.Fatalf("monthly quota = %v", q)
	}
	if q := monthlyTrafficTB(&storage.AgentPlan{TrafficMode: "sum"}); q != 10 {
		t.Fatalf("unlimited quota = %v", q)
	}
	data.Records = data.Records[:1]
	if only := scoreDeviceValues(data)[data.Records[0].Snapshot.Agent.ID]; only.score != nil || only.reason != "fewer_than_two" {
		t.Fatal(only)
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
	if !ok || got.ComputedAt != now.UnixMilli() || device.Overall.Score != nil || device.Network.Score != nil || len(device.Overall.Parts) != 4 || len(device.Network.Parts) != 5 {
		t.Fatalf("unexpected snapshot %+v", got)
	}
}
