package server

import (
	"errors"
	"testing"
	"time"

	"404-probe/internal/storage"
)

func TestSQLiteWALHealthWarningThresholds(t *testing.T) {
	base := storage.WALHealth{LogFrames: 1000, CheckpointedFrames: 1000, RemainingFrames: 0, WALBytes: 64 << 20}
	tests := []struct {
		name    string
		health  storage.WALHealth
		err     error
		warning bool
	}{
		{name: "healthy", health: base},
		{name: "busy", health: storage.WALHealth{CheckpointBusy: 1}, warning: true},
		{name: "remaining frames threshold", health: storage.WALHealth{RemainingFrames: sqliteWALWarnFrames}, warning: true},
		{name: "below remaining frames threshold", health: storage.WALHealth{RemainingFrames: sqliteWALWarnFrames - 1}},
		{name: "WAL size threshold", health: storage.WALHealth{WALBytes: sqliteWALWarnBytes}, warning: true},
		{name: "below WAL size threshold", health: storage.WALHealth{WALBytes: sqliteWALWarnBytes - 1}},
		{name: "diagnostic error", health: base, err: errors.New("checkpoint failed"), warning: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sqliteWALHealthNeedsWarning(test.health, test.err); got != test.warning {
				t.Fatalf("warning = %t, want %t", got, test.warning)
			}
		})
	}
}

func TestSQLiteWALAlertRateLimitAndRecovery(t *testing.T) {
	base := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	var state sqliteWALAlertState
	tests := []struct {
		name    string
		after   time.Duration
		warning bool
		want    sqliteWALAlertEvent
	}{
		{name: "first warning immediately", warning: true, want: sqliteWALAlertWarn},
		{name: "persistent warning is suppressed", after: 59 * time.Minute, warning: true, want: sqliteWALAlertNone},
		{name: "one warning per hour", after: time.Hour, warning: true, want: sqliteWALAlertWarn},
		{name: "one recovery info", after: time.Hour + time.Minute, want: sqliteWALAlertRecovered},
		{name: "continued health is quiet", after: time.Hour + 2*time.Minute, want: sqliteWALAlertNone},
		{name: "new incident warns immediately", after: time.Hour + 3*time.Minute, warning: true, want: sqliteWALAlertWarn},
	}
	for _, test := range tests {
		if got := state.observe(base.Add(test.after), test.warning); got != test.want {
			t.Errorf("%s: event = %d, want %d", test.name, got, test.want)
		}
	}
}

func TestSQLiteWALAlertClockRollbackWarnsAndResetsRateLimit(t *testing.T) {
	base := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	var state sqliteWALAlertState
	if got := state.observe(base, true); got != sqliteWALAlertWarn {
		t.Fatalf("first warning = %d, want warning", got)
	}
	if got := state.observe(base.Add(-time.Minute), true); got != sqliteWALAlertWarn {
		t.Fatalf("warning after clock rollback = %d, want immediate warning", got)
	}
	if got := state.observe(base.Add(58*time.Minute), true); got != sqliteWALAlertNone {
		t.Fatalf("warning before one hour from rollback = %d, want suppressed", got)
	}
	if got := state.observe(base.Add(59*time.Minute), true); got != sqliteWALAlertWarn {
		t.Fatalf("warning one hour from rollback = %d, want warning", got)
	}
	if got := state.observe(base.Add(time.Hour), false); got != sqliteWALAlertRecovered {
		t.Fatalf("recovery after rollback = %d, want recovered", got)
	}
}
