package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func TestLoadControlTokenHash(t *testing.T) {
	token, wantHash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "\n", "\r\n"} {
		t.Run(strings.ReplaceAll(suffix, "\n", "LF"), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control.token")
			if err := os.WriteFile(path, []byte(token+suffix), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := loadControlTokenHash(path)
			if err != nil || !bytes.Equal(got, wantHash) {
				t.Fatalf("hash=%x err=%v", got, err)
			}
		})
	}
}

func TestRunProbeCommandCreatesTypedOneShotJobs(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		probeType protocol.ProbeType
		config    protocol.ProbeConfig
	}{
		{
			name: "icmp", arguments: []string{"--type", "icmp_ping", "--target", "1.1.1.1", "--count", "3"},
			probeType: protocol.ProbeTypeICMPPing, config: protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: "1.1.1.1", Count: 3}},
		},
		{
			name: "tcp", arguments: []string{"--type", "tcp_connect", "--host", "example.com", "--port", "443"},
			probeType: protocol.ProbeTypeTCPConnect, config: protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "example.com", Port: 443}},
		},
		{
			name: "http", arguments: []string{"--type", "http", "--url", "https://example.com/health", "--method", "HEAD", "--expected-status", "204"},
			probeType: protocol.ProbeTypeHTTP, config: protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://example.com/health", Method: "HEAD", ExpectedStatus: intPointer(204)}},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "probe.db")
			store, err := storage.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			agentID, _ := auth.NewID()
			_, hash, _ := auth.NewToken()
			if err := store.AddAgent(ctx, agentID, "test", hash, time.Unix(100, 0)); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			jobID := strings.Repeat(string("abc"[index]), 32)
			arguments := append([]string{"run", "--db", path, "--agent-id", agentID, "--timeout", "2500ms", "--expires-in", "2m"}, test.arguments...)
			var output bytes.Buffer
			at := time.Unix(9_000, 0)
			if err := runProbeCommand(arguments, func() time.Time { return at }, func() (string, error) { return jobID, nil }, &output); err != nil {
				t.Fatal(err)
			}
			if output.String() != "Job ID: "+jobID+"\n" {
				t.Fatalf("output=%q", output.String())
			}
			store, err = storage.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			job, err := store.GetProbeJob(ctx, jobID)
			if err != nil {
				t.Fatal(err)
			}
			if job.AgentID != agentID || job.ProbeType != test.probeType || !reflect.DeepEqual(job.Config, test.config) ||
				job.TimeoutMS != 2500 || job.CreatedAt != at.UnixMilli() || job.NotBefore != at.UnixMilli() ||
				job.ExpiresAt != at.Add(2*time.Minute).UnixMilli() || job.Status != storage.JobStatusQueued || job.Attempt != 0 || job.ScheduleID != "" {
				t.Fatalf("job=%+v", job)
			}
		})
	}
}

func TestRunProbeCommandRejectsInvalidInputsBeforeOpeningDatabase(t *testing.T) {
	tests := [][]string{
		{"run", "--agent-id", "bad", "--type", "tcp_connect", "--host", "example.com", "--port", "443"},
		{"run", "--type", "tcp_connect", "--host", "example.com", "--port", "443", "--url", "https://example.com"},
		{"run", "--type", "icmp_ping", "--target", "1.1.1.1", "--count", "0"},
		{"run", "--type", "http", "--url", "ftp://example.com"},
		{"run", "--type", "tcp_connect", "--host", "example.com", "--port", "443", "--timeout", "100.5ms"},
		{"run", "--type", "tcp_connect", "--host", "example.com", "--port", "443", "--expires-in", "59s"},
		{"run", "--type", "shell"},
		{"list"},
	}
	for _, arguments := range tests {
		if arguments[0] == "run" && !strings.Contains(strings.Join(arguments, " "), "--agent-id") {
			arguments = append(arguments, "--agent-id", strings.Repeat("a", 32))
		}
		called := false
		err := runProbeCommand(arguments, func() time.Time { return time.Unix(9_000, 0) }, func() (string, error) {
			called = true
			return strings.Repeat("a", 32), nil
		}, &bytes.Buffer{})
		if err == nil || called {
			t.Fatalf("arguments=%v called=%t error=%v", arguments, called, err)
		}
	}
}

func TestRunProbeCommandEnforcesCapacityAndRevocation(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(10_000, 0)
	arguments := func(path, agentID string) []string {
		return []string{"run", "--db", path, "--agent-id", agentID, "--type", "tcp_connect", "--host", "example.com", "--port", "443"}
	}

	t.Run("capacity", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "capacity.db")
		store, err := storage.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		agentID, _ := auth.NewID()
		_, hash, _ := auth.NewToken()
		if err := store.AddAgent(ctx, agentID, "test", hash, at); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < storage.MaxOutstandingJobsPerAgent; index++ {
			if err := store.CreateOneShotJob(ctx, storage.CreateOneShotJobParams{
				ID: fmt.Sprintf("%032x", index), AgentID: agentID, ProbeType: protocol.ProbeTypeTCPConnect,
				Config:    protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "example.com", Port: 443}},
				TimeoutMS: 5000, CreatedAt: at.UnixMilli(), NotBefore: at.UnixMilli(), ExpiresAt: at.Add(10 * time.Minute).UnixMilli(),
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		jobID := strings.Repeat("f", 32)
		var output bytes.Buffer
		err = runProbeCommand(arguments(path, agentID), func() time.Time { return at }, func() (string, error) { return jobID, nil }, &output)
		if !errors.Is(err, storage.ErrOutstandingJobsFull) || output.Len() != 0 {
			t.Fatalf("error=%v output=%q", err, output.String())
		}
		store, err = storage.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err := store.GetProbeJob(ctx, jobID); !errors.Is(err, storage.ErrJobNotFound) {
			t.Fatalf("rejected job error=%v", err)
		}
	})

	t.Run("revoked agent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "revoked.db")
		store, err := storage.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		agentID, _ := auth.NewID()
		_, hash, _ := auth.NewToken()
		if err := store.AddAgent(ctx, agentID, "test", hash, at); err != nil {
			t.Fatal(err)
		}
		if changed, err := store.RevokeAgent(ctx, agentID, at.Add(time.Second)); err != nil || !changed {
			t.Fatalf("changed=%t error=%v", changed, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		err = runProbeCommand(arguments(path, agentID), func() time.Time { return at.Add(2 * time.Second) }, func() (string, error) { return strings.Repeat("f", 32), nil }, &output)
		if !errors.Is(err, storage.ErrAgentNotFound) || output.Len() != 0 {
			t.Fatalf("error=%v output=%q", err, output.String())
		}
	})
}

func TestRunScheduleCommandListsStableRedactedRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schedules.db")
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	agentID, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(12_000, 0)
	if err := store.AddAgent(ctx, agentID, "test", hash, at); err != nil {
		t.Fatal(err)
	}
	for _, value := range []struct {
		id, name string
		enabled  bool
	}{{strings.Repeat("b", 32), "beta", false}, {strings.Repeat("a", 32), "alpha", true}} {
		_, _, err := store.PutProbeSchedule(ctx, storage.PutScheduleParams{
			ID: value.id, AgentID: agentID, Name: value.name, ProbeType: protocol.ProbeTypeHTTP,
			Config:    protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://secret.example/path", Method: "GET"}},
			TimeoutMS: 5000, IntervalSeconds: 60, Enabled: value.enabled, Now: at.UnixMilli(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runScheduleCommand([]string{"list", "--db", path, "--agent-id", agentID}, time.Now, auth.NewID, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Contains(text, "secret.example") || strings.Index(text, strings.Repeat("a", 32)) > strings.Index(text, strings.Repeat("b", 32)) {
		t.Fatalf("output=%q", text)
	}
	for _, expected := range []string{"ID", "NAME", "TYPE", "ENABLED", "INTERVAL", "NEXT RUN", "alpha", "beta", "http", "60s"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in %q", expected, text)
		}
	}
}

func TestRunScheduleCommandRejectsInvalidInvocation(t *testing.T) {
	for _, arguments := range [][]string{{}, {"get"}, {"list"}, {"list", "--agent-id", "bad"}, {"list", "--agent-id", strings.Repeat("a", 32), "extra"}} {
		if err := runScheduleCommand(arguments, time.Now, auth.NewID, &bytes.Buffer{}); err == nil {
			t.Fatalf("arguments=%v accepted", arguments)
		}
	}
}

func TestRunScheduleCommandAddToggleDeleteLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schedule-lifecycle.db")
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := auth.NewID()
	_, hash, _ := auth.NewToken()
	createdAt := time.Unix(13_000, 0)
	if err := store.AddAgent(ctx, agentID, "test", hash, createdAt); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	scheduleID := strings.Repeat("d", 32)
	var output bytes.Buffer
	add := []string{"add", "--db", path, "--agent-id", agentID, "--name", "edge tcp", "--type", "tcp_connect", "--host", "example.com", "--port", "443", "--timeout", "2500ms", "--interval", "2m"}
	if err := runScheduleCommand(add, func() time.Time { return createdAt }, func() (string, error) { return scheduleID, nil }, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Schedule ID: "+scheduleID+"\n" {
		t.Fatalf("add output=%q", output.String())
	}
	store, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.GetProbeSchedule(ctx, scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if record.AgentID != agentID || record.Name != "edge tcp" || record.ProbeType != protocol.ProbeTypeTCPConnect ||
		record.Config.TCPConnect == nil || record.Config.TCPConnect.Host != "example.com" || record.Config.TCPConnect.Port != 443 ||
		record.TimeoutMS != 2500 || record.IntervalSeconds != 120 || !record.Enabled || record.NextRunAt != createdAt.UnixMilli() {
		t.Fatalf("created=%+v", record)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	output.Reset()
	disabledAt := createdAt.Add(10 * time.Second)
	if err := runScheduleCommand([]string{"disable", scheduleID, "--db", path}, func() time.Time { return disabledAt }, auth.NewID, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Disabled schedule "+scheduleID+"\n" {
		t.Fatalf("disable output=%q", output.String())
	}
	store, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := store.GetProbeSchedule(ctx, scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled || disabled.NextRunAt != record.NextRunAt || disabled.UpdatedAt != disabledAt.UnixMilli() {
		t.Fatalf("disabled=%+v", disabled)
	}
	if err := runScheduleCommand([]string{"disable", scheduleID, "--db", path}, func() time.Time { return disabledAt.Add(time.Second) }, auth.NewID, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.GetProbeSchedule(ctx, scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed, disabled) {
		t.Fatalf("disable replay=%+v want=%+v", replayed, disabled)
	}

	enabledAt := createdAt.Add(30 * time.Second)
	if err := runScheduleCommand([]string{"enable", scheduleID, "--db", path}, func() time.Time { return enabledAt }, auth.NewID, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := store.GetProbeSchedule(ctx, scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.NextRunAt != enabledAt.UnixMilli() || enabled.UpdatedAt != enabledAt.UnixMilli() {
		t.Fatalf("enabled=%+v", enabled)
	}
	output.Reset()
	if err := runScheduleCommand([]string{"delete", scheduleID, "--db", path}, time.Now, auth.NewID, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Deleted schedule "+scheduleID+"\n" {
		t.Fatalf("delete output=%q", output.String())
	}
	store, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.GetProbeSchedule(ctx, scheduleID); !errors.Is(err, storage.ErrScheduleNotFound) {
		t.Fatalf("deleted error=%v", err)
	}
}

func TestRunScheduleCommandAddCreatesICMPAndHTTPSchedules(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schedule-types.db")
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	agentID, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(14_000, 0)
	if err := store.AddAgent(ctx, agentID, "test", hash, at); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		id        string
		arguments []string
		check     func(storage.ProbeScheduleRecord) bool
	}{
		{
			id: strings.Repeat("e", 32),
			arguments: []string{"add", "--db", path, "--agent-id", agentID, "--name", "edge ping", "--type", "icmp_ping",
				"--target", "1.1.1.1", "--count", "3", "--interval", "30s", "--enabled=false"},
			check: func(record storage.ProbeScheduleRecord) bool {
				return record.ProbeType == protocol.ProbeTypeICMPPing && record.Config.ICMPPing != nil &&
					record.Config.ICMPPing.Target == "1.1.1.1" && record.Config.ICMPPing.Count == 3 &&
					!record.Enabled && record.IntervalSeconds == 30
			},
		},
		{
			id: strings.Repeat("f", 32),
			arguments: []string{"add", "--db", path, "--agent-id", agentID, "--name", "edge http", "--type", "http",
				"--url", "https://example.com/health", "--method", "HEAD", "--expected-status", "204"},
			check: func(record storage.ProbeScheduleRecord) bool {
				return record.ProbeType == protocol.ProbeTypeHTTP && record.Config.HTTP != nil &&
					record.Config.HTTP.URL == "https://example.com/health" && record.Config.HTTP.Method == "HEAD" &&
					record.Config.HTTP.ExpectedStatus != nil && *record.Config.HTTP.ExpectedStatus == 204
			},
		},
	}
	for _, test := range tests {
		if err := runScheduleCommand(test.arguments, func() time.Time { return at }, func() (string, error) { return test.id, nil }, &bytes.Buffer{}); err != nil {
			t.Fatalf("id=%s: %v", test.id, err)
		}
		store, err = storage.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		record, err := store.GetProbeSchedule(ctx, test.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if !test.check(record) {
			t.Fatalf("id=%s record=%+v", test.id, record)
		}
	}
}

func TestRunScheduleMutationRejectsInvalidTimeBeforeOpeningDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist.db")
	err := runScheduleCommand([]string{"enable", strings.Repeat("a", 32), "--db", path}, func() time.Time {
		return time.UnixMilli(0)
	}, auth.NewID, &bytes.Buffer{})
	if err == nil {
		t.Fatal("invalid time accepted")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("database side effect: %v", statErr)
	}
}

func TestRunScheduleCommandRejectsTypeMismatchBeforeIDGeneration(t *testing.T) {
	called := false
	err := runScheduleCommand([]string{
		"add", "--agent-id", strings.Repeat("a", 32), "--name", "bad", "--type", "tcp_connect",
		"--host", "example.com", "--port", "443", "--url", "https://example.com",
	}, time.Now, func() (string, error) {
		called = true
		return strings.Repeat("b", 32), nil
	}, &bytes.Buffer{})
	if err == nil || called {
		t.Fatalf("called=%t err=%v", called, err)
	}
}

func intPointer(value int) *int { return &value }

func TestLoadControlTokenHashRejectsInvalidFiles(t *testing.T) {
	token, _, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		content string
	}{
		{name: "empty"},
		{name: "leading whitespace", content: " " + token},
		{name: "trailing whitespace", content: token + " "},
		{name: "multiple lines", content: token + "\n\n"},
		{name: "embedded newline", content: token[:20] + "\n" + token[20:]},
		{name: "padded base64", content: token + "="},
		{name: "invalid base64", content: strings.Repeat("!", 43)},
		{name: "wrong decoded length", content: strings.Repeat("a", 42)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control.token")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadControlTokenHash(path); err == nil || strings.Contains(err.Error(), token) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	large := filepath.Join(t.TempDir(), "large.token")
	if err := os.WriteFile(large, bytes.Repeat([]byte("a"), 1025), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadControlTokenHash(large); err == nil {
		t.Fatal("oversized token file accepted")
	}
	if _, err := loadControlTokenHash(t.TempDir()); err == nil {
		t.Fatal("directory accepted as token file")
	}
}

func TestLoadControlTokenHashRejectsBroadUnixPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	token, _, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "control.token")
	if err := os.WriteFile(path, []byte(token), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadControlTokenHash(path); err == nil {
		t.Fatal("broad token file permissions accepted")
	}
}
