// Test-only bridge: compare actual frontend anchors with the unchanged Server.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
	_ "time/tzdata"

	"404-probe/internal/storage"
)

type sample struct {
	Day      int    `json:"day"`
	Now      string `json:"now"`
	Timezone string `json:"timezone"`
	Anchor   string `json:"anchor"`
}

type result struct {
	sample
	Start  string   `json:"start"`
	End    string   `json:"end"`
	Starts []string `json:"starts"`
}

func compare(samples []sample) ([]result, error) {
	var results []result
	for _, s := range samples {
		now, err := time.Parse(time.RFC3339, s.Now)
		if err != nil {
			return nil, err
		}
		location, err := time.LoadLocation(s.Timezone)
		if err != nil {
			return nil, err
		}
		plan := storage.AgentPlan{TrafficMode: "sum", CycleKind: "monthly", CycleCount: 1, CycleAnchor: s.Anchor, Timezone: s.Timezone}
		r := result{sample: s}
		for i := 0; i < 13; i++ {
			start, end, active, err := storage.PlanCycleBounds(plan, now)
			if err != nil || !active || start == nil || end == nil || *start > now.UnixMilli() || *end <= now.UnixMilli() {
				return nil, fmt.Errorf("inactive/invalid bounds for %+v at %s: %v", s, now, err)
			}
			local := time.UnixMilli(*start).In(location)
			last := time.Date(local.Year(), local.Month()+1, 0, 0, 0, 0, 0, location).Day()
			want := min(s.Day, last)
			if local.Day() != want {
				return nil, fmt.Errorf("reset day drift for %+v: %s, want day %d", s, local, want)
			}
			r.Starts = append(r.Starts, local.Format("2006-01-02"))
			if i == 0 {
				r.Start, r.End = local.Format("2006-01-02"), time.UnixMilli(*end).In(location).Format("2006-01-02")
			}
			now = time.UnixMilli(*end)
		}
		results = append(results, r)
	}
	return results, nil
}

func main() {
	var samples []sample
	err := json.NewDecoder(os.Stdin).Decode(&samples)
	var results []result
	if err == nil {
		results, err = compare(samples)
	}
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(results)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
