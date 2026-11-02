package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Reproduces the review's worst case using the actual sample SQL and the same
// existing index. "Cold" means no application sample cache, not cold OS pages.
func TestDashboardSlotFailureHistoryPerformance(t *testing.T) {
	if os.Getenv("V11_SLOT_SCAN_PERF") != "1" {
		t.Skip("opt-in eight-hour failure-history measurement")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range []string{`CREATE TABLE quality_facts(id INTEGER PRIMARY KEY AUTOINCREMENT,target_id TEXT,sample_time INTEGER,seq INTEGER,eligible INTEGER,rtt REAL)`, `CREATE INDEX quality_fact_neighbor ON quality_facts(target_id,sample_time,seq,id)`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Unix(1800000000, 0)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO quality_facts(target_id,sample_time,seq,eligible,rtt) VALUES(?,?,?,0,NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	values := []string{}
	args := []any{}
	for target := 0; target < 600; target++ {
		id := fmt.Sprintf("target-%03d", target)
		values = append(values, "(?)")
		args = append(args, id)
		for sample := 1; sample <= 960; sample++ {
			at := now.Add(-8*time.Hour + time.Duration(sample)*30*time.Second)
			if _, err := stmt.Exec(id, at.UnixMilli(), sample); err != nil {
				t.Fatal(err)
			}
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	current := fmt.Sprintf(dashboardSlotSampleSQL, strings.Join(values, ","))
	previous := strings.Replace(current, "fact.sample_time>=? AND ", "", 1)
	beforeArgs := append(append([]any{}, args...), now.UnixMilli())
	afterArgs := append(append([]any{}, args...), now.Add(-time.Hour).UnixMilli(), now.UnixMilli())
	measure := func(q string, params []any) float64 {
		t.Helper()
		start := time.Now()
		rows, err := db.Query(q, params...)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for rows.Next() {
			var id string
			var ms sql.NullFloat64
			var at sql.NullInt64
			if err := rows.Scan(&id, &ms, &at); err != nil {
				t.Fatal(err)
			}
			if ms.Valid || at.Valid {
				t.Fatal("failure history produced a valid sample")
			}
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil || count != 600 {
			t.Fatal(count, err)
		}
		return float64(time.Since(start)) / float64(time.Millisecond)
	}
	before, after := []float64{}, []float64{}
	for i := 0; i < 7; i++ {
		before = append(before, measure(previous, beforeArgs))
		after = append(after, measure(current, afterArgs))
	}
	median := func(v []float64) float64 { v = append([]float64{}, v...); sort.Float64s(v); return v[len(v)/2] }
	result, _ := json.Marshal(map[string]any{"targets": 600, "rows": 576000, "history_hours": 8, "interval_seconds": 30, "before_ms": before, "after_ms": after, "before_median_ms": median(before), "after_median_ms": median(after)})
	t.Log("V11_COLD_SCAN " + string(result))
}
