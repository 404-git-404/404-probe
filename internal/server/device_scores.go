package server

import (
	"context"
	"errors"
	"math"
	"math/big"
	"net/http"
	"sort"
	"time"

	"404-probe/internal/storage"
)

const deviceScoreInterval = 10 * time.Minute

type scorePart struct {
	Key             string   `json:"key"`
	Score           *float64 `json:"score"`
	Weight          int      `json:"weight"`
	EffectiveWeight float64  `json:"effective_weight"`
	Reason          string   `json:"reason,omitempty"`
}

type scoreSummary struct {
	Score  *float64    `json:"score"`
	Grade  string      `json:"grade,omitempty"`
	Parts  []scorePart `json:"parts"`
	Reason string      `json:"reason,omitempty"`
}

type scoreLine struct {
	storage.NetworkLine
	Score     *float64 `json:"score"`
	CoveredMS int64    `json:"covered_ms"`
	Reason    string   `json:"reason,omitempty"`
}

type deviceNetworkScore struct {
	scoreSummary
	Lines []scoreLine `json:"lines,omitempty"`
}

type deviceScore struct {
	Network       deviceNetworkScore `json:"network"`
	Overall       scoreSummary       `json:"overall"`
	OnlinePercent *float64           `json:"online_percent,omitempty"`
	OnlineKnownMS int64              `json:"online_known_ms,omitempty"`
	ValueRank     int                `json:"value_rank"`
	ValueCount    int                `json:"value_count"`
	ValueScore    *float64           `json:"value_score"`
	ValueReason   string             `json:"value_reason,omitempty"`
	HighValue     bool               `json:"high_value"`
}

type deviceScoreSnapshot struct {
	ComputedAt int64                  `json:"computed_at"`
	Items      map[string]deviceScore `json:"items"`
}

var networkScoreParts = [...]scorePart{
	{Key: "availability", Weight: 30},
	{Key: "latency", Weight: 25},
	{Key: "stability", Weight: 20},
	{Key: "evening", Weight: 15},
	{Key: "alerts", Weight: 10},
}

func deviceScoreGrade(value float64) string {
	switch {
	case value >= 95:
		return "SSS"
	case value >= 90:
		return "SS"
	case value >= 85:
		return "S"
	case value >= 75:
		return "A"
	case value >= 65:
		return "B"
	case value >= 50:
		return "C"
	default:
		return "D"
	}
}

func scoreDescending(value, best, worst float64) float64 {
	return 100 * max(0, min(1, (worst-value)/(worst-best)))
}

func summarizeScore(parts []scorePart, reason string) scoreSummary {
	out := scoreSummary{Parts: parts, Reason: reason}
	if reason != "" {
		return out
	}
	var weighted float64
	var weight int
	for _, part := range parts {
		if part.Score != nil {
			weighted += float64(part.Weight) * *part.Score
			weight += part.Weight
		}
	}
	if weight == 0 {
		out.Reason = "no_history"
		return out
	}
	for i := range parts {
		if parts[i].Score != nil {
			parts[i].EffectiveWeight = 100 * float64(parts[i].Weight) / float64(weight)
		}
	}
	value := min(100, weighted/float64(weight))
	out.Score, out.Grade = &value, deviceScoreGrade(value)
	return out
}

func scoreLatencyRatio(value, reference float64) float64 {
	if reference > 0 {
		return value / reference
	}
	if value == 0 {
		return 1
	}
	return math.Inf(1)
}

func scoreNetworkLine(line storage.DeviceScoreLine) [len(networkScoreParts)]float64 {
	scores := [len(networkScoreParts)]float64{}
	scores[0] = scoreDescending(*line.FailurePercent, 0, 10)
	if line.P50 != nil {
		scores[1] = scoreDescending(*line.P50, 50, 600)
		if line.P95 != nil {
			spread := scoreLatencyRatio(*line.P95, *line.P50) - 1
			scores[2] = 0.7 * scoreDescending(spread, 0.2, 1.5)
		}
		if line.Evening != nil {
			scores[3] = scoreDescending(scoreLatencyRatio(*line.Evening, *line.P50), 1.1, 2.0)
		}
	}
	if line.Jitter != nil {
		scores[2] += 0.3 * scoreDescending(*line.Jitter, 5, 50)
	}
	scores[4] = scoreDescending(float64(line.AlertMS), 0, float64((24 * time.Hour).Milliseconds()))
	return scores
}

func scoreMachineNetwork(lines []storage.DeviceScoreLine, paused bool) deviceNetworkScore {
	parts := append([]scorePart(nil), networkScoreParts[:]...)
	var out deviceNetworkScore
	var sums [len(networkScoreParts)]float64
	count := 0
	reason := ""
	for _, line := range lines {
		item := scoreLine{NetworkLine: line.NetworkLine, CoveredMS: line.CoveredMS}
		switch {
		case line.CoveredMS < storage.DeviceScoreMinLineCoverage.Milliseconds():
			item.Reason = "line_coverage"
		case line.FailurePercent == nil:
			item.Reason = "no_probe_results"
		default:
			scores := scoreNetworkLine(line)
			total := 0.0
			for i, score := range scores {
				sums[i] += score
				total += score * float64(parts[i].Weight) / 100
			}
			item.Score = &total
			count++
		}
		out.Lines = append(out.Lines, item)
	}
	switch {
	case paused:
		reason = "paused"
	case count == 0 && len(lines) == 0:
		reason = "no_card_lines"
	case count == 0:
		reason = "network_coverage"
	}
	for i := range parts {
		if reason == "" {
			value := sums[i] / float64(count)
			parts[i].Score = &value
		} else {
			parts[i].Reason = reason
		}
	}
	out.scoreSummary = summarizeScore(parts, reason)
	return out
}

type deviceValueScore struct {
	ratio       *big.Rat
	rank, count int
	score       *float64
	reason      string
}

func scoreDeviceValues(data storage.DashboardData, scores map[string]deviceScore) map[string]deviceValueScore {
	result := make(map[string]deviceValueScore, len(scores))
	eligible := make([]string, 0, len(scores))
	for _, price := range estimateExpenses(data, nil).Devices {
		device, found := scores[price.ID]
		if !found {
			continue
		}
		item := deviceValueScore{reason: price.Exclusion}
		switch {
		case item.reason != "":
		case price.monthlyUSDExact.Sign() <= 0:
			item.reason = "invalid_price"
		case device.Overall.Score == nil:
			item.reason = device.Overall.Reason
		default:
			item.ratio = new(big.Rat).Quo(new(big.Rat).SetFloat64(*device.Overall.Score), price.monthlyUSDExact)
			eligible = append(eligible, price.ID)
		}
		result[price.ID] = item
	}
	if len(eligible) < 2 {
		for _, id := range eligible {
			result[id] = deviceValueScore{reason: "fewer_than_two"}
		}
		return result
	}
	sort.Slice(eligible, func(i, j int) bool {
		return result[eligible[i]].ratio.Cmp(result[eligible[j]].ratio) > 0
	})
	lastRank := len(eligible) - 1
	worst := result[eligible[lastRank]].ratio
	for lastRank > 0 && result[eligible[lastRank-1]].ratio.Cmp(worst) == 0 {
		lastRank--
	}
	rank := 0
	for i, id := range eligible {
		item := result[id]
		if i > 0 && item.ratio.Cmp(result[eligible[i-1]].ratio) != 0 {
			rank = i
		}
		value := 50.0
		if lastRank != 0 {
			value = 100 * float64(lastRank-rank) / float64(lastRank)
		}
		item.rank, item.count, item.score = rank+1, len(eligible), &value
		result[id] = item
	}
	return result
}

func (d *deviceScore) setValueRanking(value deviceValueScore) {
	d.ValueRank, d.ValueCount, d.ValueScore, d.ValueReason = value.rank, value.count, value.score, value.reason
	d.HighValue = value.score != nil && *value.score >= 80 && d.Overall.Score != nil && *d.Overall.Score >= 65
}

func scoreOverall(agent storage.Agent, inputs storage.DeviceScoreInputs) deviceScore {
	paused := agent.DisabledAt != nil
	out := deviceScore{Network: scoreMachineNetwork(inputs.Lines, paused)}
	parts := []scorePart{
		{Key: "network", Weight: 50, Score: out.Network.Score, Reason: out.Network.Reason},
		{Key: "uptime", Weight: 30, Reason: "no_presence"},
		{Key: "resources", Weight: 20, Reason: "no_resources"},
	}
	reason := ""
	switch {
	case paused:
		reason = "paused"
	default:
		out.OnlinePercent = inputs.Presence.OnlinePercent
		out.OnlineKnownMS = inputs.Presence.OnlineMS + inputs.Presence.OfflineMS
		if out.OnlinePercent != nil {
			uptime := scoreDescending(100-*out.OnlinePercent, 0, 5)
			parts[1].Score, parts[1].Reason = &uptime, ""
		}
		if r := inputs.Resources; r != nil {
			headroom := max(0, min(100, 100-(0.4*r.CPU+0.4*r.RAM+0.2*r.Disk)))
			parts[2].Score, parts[2].Reason = &headroom, ""
		}
	}
	if reason != "" {
		for i := range parts {
			parts[i].Score, parts[i].Reason = nil, reason
		}
	}
	out.Overall = summarizeScore(parts, reason)
	return out
}

func (a *App) calculateDeviceScores(ctx context.Context, now time.Time) (*deviceScoreSnapshot, error) {
	data, err := a.store.ReadDashboardData(ctx, now, a.offlineTimeout, false)
	if err != nil {
		return nil, err
	}
	snapshot := &deviceScoreSnapshot{ComputedAt: now.UnixMilli(), Items: make(map[string]deviceScore, len(data.Records))}
	for _, record := range data.Records {
		agent := record.Snapshot.Agent
		if agent.Revoked {
			continue
		}
		var inputs storage.DeviceScoreInputs
		if agent.DisabledAt == nil {
			inputs, err = a.store.ReadDeviceScoreInputs(ctx, agent.ID, now)
			if errors.Is(err, storage.ErrAgentNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
		}
		snapshot.Items[agent.ID] = scoreOverall(agent, inputs)
	}
	for id, value := range scoreDeviceValues(data, snapshot.Items) {
		device := snapshot.Items[id]
		device.setValueRanking(value)
		snapshot.Items[id] = device
	}
	return snapshot, nil
}

func (a *App) maintainDeviceScores() {
	ticker := time.NewTicker(deviceScoreInterval)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(a.shutdown, 2*time.Minute)
		snapshot, err := a.calculateDeviceScores(ctx, a.now())
		cancel()
		if err == nil {
			a.deviceScores.Store(snapshot)
		} else if a.shutdown.Err() == nil {
			a.logger.Warn("device score refresh failed", "error", err)
		}
		select {
		case <-a.shutdown.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) handleDeviceScores(w http.ResponseWriter, r *http.Request) {
	if r.URL.EscapedPath() != "/api/v1/web/scores" || r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "scores do not accept query parameters")
		return
	}
	snapshot := a.deviceScores.Load()
	if snapshot == nil {
		snapshot = &deviceScoreSnapshot{Items: map[string]deviceScore{}}
	}
	writeJSON(w, http.StatusOK, struct {
		*deviceScoreSnapshot
		ServerNow int64 `json:"server_now"`
	}{snapshot, a.now().UnixMilli()})
}
