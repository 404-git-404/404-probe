package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPlanRenewalCleanupBoundedAndConverges(t *testing.T) {
	for _, v := range []struct {
		name string
		stop int
		want int64
	}{{"converge", 2, 600}, {"cap", 0, 2000}} {
		t.Run(v.name, func(t *testing.T) {
			calls := 0
			now := time.Now()
			deleted, err := runPlanRenewalCleanup(context.Background(), now, func(ctx context.Context, stamp time.Time) (int64, bool, error) {
				calls++
				if stamp != now {
					t.Fatal("time changed")
				}
				if calls == v.stop {
					return 100, false, nil
				}
				return 500, true, nil
			})
			if err != nil || deleted != v.want || (v.stop == 0 && calls != 4) || (v.stop != 0 && calls != v.stop) {
				t.Fatalf("%d %d %v", deleted, calls, err)
			}
		})
	}
}
func TestPlanRenewalCleanupErrorAndCancellation(t *testing.T) {
	calls := 0
	want := errors.New("failure")
	_, err := runPlanRenewalCleanup(context.Background(), time.Now(), func(context.Context, time.Time) (int64, bool, error) { calls++; return 0, false, want })
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("%d %v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	_, err = runPlanRenewalCleanup(ctx, time.Now(), func(context.Context, time.Time) (int64, bool, error) { calls++; return 500, true, nil })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("%d %v", calls, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	calls = 0
	deleted, err := runPlanRenewalCleanup(ctx, time.Now(), func(context.Context, time.Time) (int64, bool, error) { calls++; cancel(); return 500, true, nil })
	if !errors.Is(err, context.Canceled) || calls != 1 || deleted != 500 {
		t.Fatalf("%d %d %v", calls, deleted, err)
	}
}
func TestPlanRenewalCleanupRealBatchesStartupTickerAndShutdown(t *testing.T) {
	a, s, db, id := renewalFileApp(t)
	ctx := context.Background()
	now := a.now()
	// 40 devices x <=51 receipts, including one live boundary survivor.
	for i := 1; i < 40; i++ {
		if err := s.AddAgent(ctx, fmt.Sprintf("%032x", i), "retention", []byte(fmt.Sprintf("retention-token-%d", i)), now); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 2002; i++ {
		agent := id
		if i%40 != 0 {
			agent = fmt.Sprintf("%032x", i%40)
		}
		expiry := now.UnixMilli()
		if i == 2001 {
			expiry++
		}
		_, err := tx.Exec(`INSERT INTO agent_plan_renewal_requests(request_id,agent_id,kind,fingerprint,field,from_date,to_date,prior_revision,result_revision,operation_id,created_at,undo_until,expires_at) VALUES(?,?,'apply',?,'renewal_date','2024-01-01','2024-02-01',?,?,?,0,0,?)`, fmt.Sprintf("%032x", i+10000), agent, fmt.Sprintf("%064x", i), fmt.Sprintf("%032x", i), fmt.Sprintf("%032x", i+1), fmt.Sprintf("%032x", i), expiry)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	a.cleanupPlanRenewalRequests()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM agent_plan_renewal_requests`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("4x500 limit %d %v", count, err)
	}
	deleted, more, err := s.CleanupPlanRenewalRequestsBatch(ctx, now)
	if err != nil || deleted != 1 || more {
		t.Fatalf("%d %v %v", deleted, more, err)
	}
	// Prove startup then the existing maintenance ticker delete expired rows;
	// no new timer is introduced by the production change.
	if _, err := db.Exec(`UPDATE agent_plan_renewal_requests SET expires_at=?`, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	a.removalReceiptCleanupInterval = 20 * time.Millisecond
	done := make(chan struct{})
	go func() { a.CleanupLoop(); close(done) }()
	waitEmpty := func() {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if err := db.QueryRow(`SELECT count(*) FROM agent_plan_renewal_requests`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count == 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("cleanup did not run")
	}
	waitEmpty()
	if _, err := db.Exec(`INSERT INTO agent_plan_renewal_requests(request_id,agent_id,kind,fingerprint,field,from_date,to_date,prior_revision,result_revision,operation_id,created_at,undo_until,expires_at) VALUES(?,?,'apply',?,'renewal_date','2024-01-01','2024-02-01',?,?,?,0,0,?)`, fmt.Sprintf("%032x", 90000), id, fmt.Sprintf("%064x", 1), fmt.Sprintf("%032x", 1), fmt.Sprintf("%032x", 2), fmt.Sprintf("%032x", 1), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	waitEmpty()
	a.Shutdown()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not stop cleanup")
	}
}

func TestPlanRenewalCleanupRealRollbackAndExpiryIndex(t *testing.T) {
	a, s, db, id := renewalFileApp(t)
	now := a.now()
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO agent_plan_renewal_requests(request_id,agent_id,kind,fingerprint,field,from_date,to_date,prior_revision,result_revision,operation_id,created_at,undo_until,expires_at) VALUES(?,?,'apply',?,'renewal_date','2024-01-01','2024-02-01',?,?,?,0,0,?)`, fmt.Sprintf("%032x", 90000), id, fmt.Sprintf("%064x", 1), fmt.Sprintf("%032x", 1), fmt.Sprintf("%032x", 2), fmt.Sprintf("%032x", 1), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT request_id FROM agent_plan_renewal_requests INDEXED BY idx_plan_renewal_expiry WHERE expires_at<=? ORDER BY expires_at,request_id LIMIT 500`, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	var detail string
	for rows.Next() {
		var a, b, c int
		var line string
		if err := rows.Scan(&a, &b, &c, &line); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		detail += line
	}
	err = rows.Err()
	rows.Close()
	if err != nil || !strings.Contains(detail, "idx_plan_renewal_expiry") || strings.Contains(detail, "TEMP B-TREE") {
		t.Fatalf("%s %v", detail, err)
	}
	if _, err := db.Exec(`CREATE TRIGGER renewal_cleanup_fault BEFORE DELETE ON agent_plan_renewal_requests BEGIN SELECT RAISE(ABORT,'cleanup failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CleanupPlanRenewalRequestsBatch(ctx, now); err == nil {
		t.Fatal("cleanup failure ignored")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM agent_plan_renewal_requests`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rollback %d %v", count, err)
	}
	if _, err := db.Exec(`DROP TRIGGER renewal_cleanup_fault`); err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := s.CleanupPlanRenewalRequestsBatch(cancelCtx, now); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	deleted, more, err := s.CleanupPlanRenewalRequestsBatch(ctx, now)
	if err != nil || deleted != 1 || more {
		t.Fatalf("after rollback %d %v %v", deleted, more, err)
	}
}
