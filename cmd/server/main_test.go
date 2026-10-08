package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"404-probe/internal/releasemetadata"
	"404-probe/internal/storage"
)

func TestVerifyReleaseAssetStrictlyBindsMetadataManifestAndBytes(t *testing.T) {
	directory := t.TempDir()
	assetPath := filepath.Join(directory, "404-probe-server-linux-amd64")
	assetBytes := []byte("candidate")
	if err := os.WriteFile(assetPath, assetBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	digestBytes := sha256.Sum256(assetBytes)
	digest := hex.EncodeToString(digestBytes[:])
	commit := strings.Repeat("a", 40)
	document := releasemetadata.Document{SchemaVersion: releasemetadata.SchemaVersion, Version: "v0.9.1", Commit: commit, Assets: []releasemetadata.Asset{{
		Name: filepath.Base(assetPath), GOOS: "linux", GOARCH: "amd64", SHA256: digest,
	}}}
	metadata, err := releasemetadata.Encode(document)
	if err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(directory, "RELEASE-METADATA.json")
	checksumsPath := filepath.Join(directory, "SHA256SUMS")
	if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checksumsPath, []byte(digest+"  "+filepath.Base(assetPath)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := verifyReleaseAsset("v0.9.1", metadataPath, checksumsPath, assetPath, "linux", "amd64")
	if err != nil || result.Commit != commit || result.SHA256 != digest {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	wrongBinding := bytes.Replace(metadata, []byte(digest), []byte(strings.Repeat("b", 64)), 1)
	if err := os.WriteFile(metadataPath, wrongBinding, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyReleaseAsset("v0.9.1", metadataPath, checksumsPath, assetPath, "linux", "amd64"); err == nil {
		t.Fatal("metadata checksum bound to different bytes was accepted")
	}
	duplicate := bytes.Replace(metadata, []byte(`"version": "v0.9.1"`), []byte(`"version": "v0.9.1", "version": "v0.9.1"`), 1)
	if err := os.WriteFile(metadataPath, duplicate, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyReleaseAsset("v0.9.1", metadataPath, checksumsPath, assetPath, "linux", "amd64"); err == nil {
		t.Fatal("duplicate metadata key was accepted")
	}
}

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

type fakeHTTPShutdownController struct {
	shutdownErr   error
	closeErr      error
	shutdownCalls int
	closeCalls    int
	deadline      time.Time
}

func (f *fakeHTTPShutdownController) Shutdown(ctx context.Context) error {
	f.shutdownCalls++
	f.deadline, _ = ctx.Deadline()
	return f.shutdownErr
}

func (f *fakeHTTPShutdownController) Close() error {
	f.closeCalls++
	return f.closeErr
}

func TestShutdownHTTPServerForceClosesAfterGracefulShutdownError(t *testing.T) {
	graceful := &fakeHTTPShutdownController{}
	if err := shutdownHTTPServer(graceful, 10*time.Second); err != nil {
		t.Fatalf("graceful shutdown error=%v", err)
	}
	if graceful.shutdownCalls != 1 || graceful.closeCalls != 0 || graceful.deadline.IsZero() || time.Until(graceful.deadline) <= 0 {
		t.Fatalf("graceful shutdown calls=%d close calls=%d deadline=%v", graceful.shutdownCalls, graceful.closeCalls, graceful.deadline)
	}

	shutdownErr := context.DeadlineExceeded
	closeErr := errors.New("forced close failed")
	forced := &fakeHTTPShutdownController{shutdownErr: shutdownErr, closeErr: closeErr}
	err := shutdownHTTPServer(forced, 10*time.Second)
	if !errors.Is(err, shutdownErr) || !errors.Is(err, closeErr) {
		t.Fatalf("shutdown error=%v, want both graceful and forced-close errors", err)
	}
	if forced.shutdownCalls != 1 || forced.closeCalls != 1 {
		t.Fatalf("failed shutdown calls=%d close calls=%d", forced.shutdownCalls, forced.closeCalls)
	}
}

func TestWebDomainCommandLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "domains.db")
	if err := webDomainCommand([]string{"list", "--db", path, "--json"}); err != nil {
		t.Fatal(err)
	}
	if err := webDomainCommand([]string{"add", "--db", path, "--json", "navolyn.com"}); err != nil {
		t.Fatal(err)
	}
	if err := webDomainCommand([]string{"add", "--db", path, "--json", "example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := webDomainCommand([]string{"remove", "--db", path, "--json", "example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := webDomainCommand([]string{"remove", "--db", path, "--json", "navolyn.com"}); !errors.Is(err, storage.ErrLastWebDomainSuffix) {
		t.Fatalf("last remove err=%v", err)
	}
	if err := webDomainCommand([]string{"disable", "--db", path, "--json"}); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy, err := store.GetWebDomainPolicy(context.Background())
	if err != nil || policy.Mode != storage.WebDomainModeExact || len(policy.Suffixes) != 0 {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
}

func TestLoadWebPasswordHash(t *testing.T) {
	encoded, err := auth.HashPassword([]byte("test-password"))
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "\n", "\r\n"} {
		path := filepath.Join(t.TempDir(), "web-password.hash")
		if err := os.WriteFile(path, []byte(encoded+suffix), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := loadWebPasswordHash(path)
		if err != nil || got != encoded {
			t.Fatalf("hash=%q err=%v", got, err)
		}
	}
	for _, content := range []string{"", "not-a-hash", encoded + "\n\n", strings.Repeat("x", 1025)} {
		path := filepath.Join(t.TempDir(), "invalid-web-password.hash")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := loadWebPasswordHash(path); err == nil || (content != "" && strings.Contains(err.Error(), content)) || got != "" {
			t.Fatalf("content length=%d hash=%q err=%v", len(content), got, err)
		}
	}
}

func TestLoadWebPasswordHashRejectsBroadUnixPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions are not available")
	}
	encoded, err := auth.HashPassword([]byte("test-password"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "web-password.hash")
	if err := os.WriteFile(path, []byte(encoded), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := loadWebPasswordHash(path); err == nil || got != "" || strings.Contains(err.Error(), encoded) {
		t.Fatalf("hash=%q err=%v", got, err)
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

func TestRunProbeCommandGetShowsLeasedAndFinishedSnapshotsWithoutFencingSecrets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "probe-get.db")
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
	at := time.Unix(15_000, 0)
	if err := store.AddAgent(ctx, agentID, "test", hash, at); err != nil {
		t.Fatal(err)
	}
	jobID := strings.Repeat("b", 64)
	if err := store.CreateOneShotJob(ctx, storage.CreateOneShotJobParams{
		ID: jobID, AgentID: agentID, ProbeType: protocol.ProbeTypeTCPConnect,
		Config:    protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "example.com", Port: 443}},
		TimeoutMS: 2500, CreatedAt: at.UnixMilli(), NotBefore: at.UnixMilli(), ExpiresAt: at.Add(10 * time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimJob(ctx, agentID, protocol.ClaimRequest{
		ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: 7, SessionID: "session-secret",
		SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypeTCPConnect},
	}, at, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v error=%v", claim, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runProbeCommand([]string{"get", jobID, "--db", path}, func() time.Time { return at.Add(time.Second) }, auth.NewID, &output); err != nil {
		t.Fatal(err)
	}
	leased := output.String()
	for _, want := range []string{"STATUS", "leased", "ATTEMPT", "1", "LEASED AT", "LEASE UNTIL"} {
		if !strings.Contains(leased, want) {
			t.Fatalf("leased output missing %q: %q", want, leased)
		}
	}
	for _, secret := range []string{claim.LeaseToken, "session-secret"} {
		if strings.Contains(leased, secret) {
			t.Fatalf("leased output exposed secret %q: %q", secret, leased)
		}
	}

	store, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	result := protocol.JobResult{
		ProtocolVersion: protocol.JobProtocolVersion, LeaseToken: claim.LeaseToken, Attempt: claim.Attempt,
		AgentEpoch: 7, SessionID: "session-secret", StartedAt: 1_000, FinishedAt: 1_025, DurationMS: 25,
		Success: false, ResolvedIP: "192.0.2.1", ErrorCategory: "connection_refused", ErrorMessage: "sensitive endpoint detail",
		Result: protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{ConnectMS: 20}},
	}
	if _, err := store.SubmitJobResult(ctx, agentID, jobID, result, at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	output.Reset()
	if err := runProbeCommand([]string{"get", jobID, "--db", path}, func() time.Time { return at.Add(3 * time.Second) }, auth.NewID, &output); err != nil {
		t.Fatal(err)
	}
	finished := output.String()
	for _, want := range []string{"STATUS", "finished", "SUCCESS", "false", "RESOLVED IP", "192.0.2.1", "ERROR CATEGORY", "connection_refused", "CONNECT", "20ms"} {
		if !strings.Contains(finished, want) {
			t.Fatalf("finished output missing %q: %q", want, finished)
		}
	}
	for _, secret := range []string{claim.LeaseToken, "session-secret", "sensitive endpoint detail"} {
		if strings.Contains(finished, secret) {
			t.Fatalf("finished output exposed secret %q: %q", secret, finished)
		}
	}
}

func TestRunProbeCommandGetNormalizesExpiry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "probe-expiry.db")
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
	at := time.Unix(16_000, 0)
	if err := store.AddAgent(ctx, agentID, "test", hash, at); err != nil {
		t.Fatal(err)
	}
	jobID := strings.Repeat("c", 32)
	expiresAt := at.Add(time.Minute)
	if err := store.CreateOneShotJob(ctx, storage.CreateOneShotJobParams{
		ID: jobID, AgentID: agentID, ProbeType: protocol.ProbeTypeHTTP,
		Config:    protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://example.com/health", Method: "GET"}},
		TimeoutMS: 5000, CreatedAt: at.UnixMilli(), NotBefore: at.UnixMilli(), ExpiresAt: expiresAt.UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runProbeCommand([]string{"get", jobID, "--db", path}, func() time.Time { return expiresAt }, auth.NewID, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "expired") {
		t.Fatalf("output=%q", output.String())
	}
	store, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.GetProbeJob(ctx, jobID)
	if err != nil || job.Status != storage.JobStatusExpired {
		t.Fatalf("job=%+v error=%v", job, err)
	}
}

func TestRunProbeCommandGetRejectsInvalidInputBeforeOpeningDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist.db")
	tests := [][]string{
		{"get"},
		{"get", "bad", "--db", path},
		{"get", strings.Repeat("a", 32), "extra", "--db", path},
	}
	for _, arguments := range tests {
		if err := runProbeCommand(arguments, time.Now, auth.NewID, &bytes.Buffer{}); err == nil {
			t.Fatalf("arguments=%v accepted", arguments)
		}
	}
	err := runProbeCommand([]string{"get", strings.Repeat("a", 32), "--db", path}, func() time.Time {
		return time.UnixMilli(0)
	}, auth.NewID, &bytes.Buffer{})
	if err == nil {
		t.Fatal("invalid time accepted")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("database side effect: %v", statErr)
	}
}

func TestWriteProbeSnapshotRejectsCorruptState(t *testing.T) {
	tests := []struct {
		name   string
		job    storage.ProbeJobRecord
		result *storage.ProbeResultRecord
	}{
		{name: "unknown status", job: storage.ProbeJobRecord{Status: storage.JobStatus("broken")}},
		{name: "invalid lease", job: storage.ProbeJobRecord{Status: storage.JobStatusLeased, LeasedAt: 2, LeaseUntil: 2}},
		{name: "missing finished result", job: storage.ProbeJobRecord{Status: storage.JobStatusFinished, FinishedAt: 1}},
		{name: "result on queued", job: storage.ProbeJobRecord{Status: storage.JobStatusQueued}, result: &storage.ProbeResultRecord{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := writeProbeSnapshot(&output, test.job, test.result)
			if !errors.Is(err, storage.ErrCorruptProbeData) || output.Len() != 0 {
				t.Fatalf("error=%v output=%q", err, output.String())
			}
		})
	}
}

func TestRunProbeCommandListShowsStableRedactedSummaries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "probe-list.db")
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
	at := time.Unix(17_000, 0)
	if err := store.AddAgent(ctx, agentID, "test", hash, at); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{strings.Repeat("a", 32), strings.Repeat("b", 32)} {
		if err := store.CreateOneShotJob(ctx, storage.CreateOneShotJobParams{
			ID: id, AgentID: agentID, ProbeType: protocol.ProbeTypeTCPConnect,
			Config:    protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "secret.example", Port: 443}},
			TimeoutMS: 5000, CreatedAt: at.UnixMilli(), NotBefore: at.UnixMilli(), ExpiresAt: at.Add(10 * time.Minute).UnixMilli(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	claim, err := store.ClaimJob(ctx, agentID, protocol.ClaimRequest{
		ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: 9, SessionID: "private-session",
		SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypeTCPConnect},
	}, at, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v error=%v", claim, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runProbeCommand([]string{"list", "--db", path, "--agent-id", agentID}, func() time.Time { return at.Add(time.Second) }, auth.NewID, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "ID") || !strings.Contains(text, "STATUS") || !strings.Contains(text, "leased") || !strings.Contains(text, "queued") {
		t.Fatalf("output=%q", text)
	}
	bID := strings.Repeat("b", 32)
	aID := strings.Repeat("a", 32)
	if strings.Index(text, bID) >= strings.Index(text, aID) {
		t.Fatalf("unstable order: %q", text)
	}
	for _, secret := range []string{"secret.example", claim.LeaseToken, "private-session", agentID} {
		if strings.Contains(text, secret) {
			t.Fatalf("output exposed %q: %q", secret, text)
		}
	}

	output.Reset()
	if err := runProbeCommand([]string{"list", "--db", path, "--agent-id", agentID, "--limit", "1"}, func() time.Time { return at.Add(time.Second) }, auth.NewID, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), bID) || strings.Contains(output.String(), aID) {
		t.Fatalf("limited output=%q", output.String())
	}
}

func TestRunProbeCommandListRejectsInvalidInputBeforeOpeningDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist.db")
	tests := [][]string{
		{"list", "--db", path},
		{"list", "--db", path, "--agent-id", "bad"},
		{"list", "--db", path, "--agent-id", strings.Repeat("a", 32), "--limit", "0"},
		{"list", "--db", path, "--agent-id", strings.Repeat("a", 32), "extra"},
	}
	for _, arguments := range tests {
		if err := runProbeCommand(arguments, time.Now, auth.NewID, &bytes.Buffer{}); err == nil {
			t.Fatalf("arguments=%v accepted", arguments)
		}
	}
	err := runProbeCommand([]string{"list", "--db", path, "--agent-id", strings.Repeat("a", 32)}, func() time.Time {
		return time.UnixMilli(0)
	}, auth.NewID, &bytes.Buffer{})
	if err == nil {
		t.Fatal("invalid time accepted")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("database side effect: %v", statErr)
	}
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
