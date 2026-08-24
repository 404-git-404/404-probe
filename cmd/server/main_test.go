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
