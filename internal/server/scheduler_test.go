package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"404-probe/internal/agent"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func TestSchedulerTickMaterializesAndRedactsConfig(t *testing.T) {
	_, store, agentID, _ := testApp(t)
	defer store.Close()
	at := time.Unix(6_000, 0)
	target := "secret-target.example"
	_, _, err := store.PutProbeSchedule(context.Background(), storage.PutScheduleParams{
		ID: "schedule", AgentID: agentID, Name: "private", ProbeType: protocol.ProbeTypeTCPConnect,
		Config:    protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: target, Port: 443}},
		TimeoutMS: 5000, IntervalSeconds: 60, Enabled: true, Now: at.UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	app, err := NewApp(store, time.Minute, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	app.now = func() time.Time { return at }
	app.runSchedulerTick()
	job, err := store.GetProbeJob(context.Background(), storage.ScheduledJobID("schedule", at.UnixMilli()))
	if err != nil || job.ScheduleID != "schedule" {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if strings.Contains(logs.String(), target) {
		t.Fatalf("logs leaked target: %s", logs.String())
	}
}

func TestScheduledTCPJobEndToEnd(t *testing.T) {
	app, store, agentID, agentToken, controlToken := newControlTestApp(t)
	defer store.Close()
	at := time.Now().Truncate(time.Millisecond)
	app.now = func() time.Time { return at }
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			acceptErr = connection.Close()
		}
		accepted <- acceptErr
	}()
	request := validControlScheduleRequest(agentID, protocol.ProbeTypeTCPConnect)
	request.Config = protocol.TCPConnectConfig{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port}
	schedulePath := controlSchedulePathPrefix + firstControlScheduleID
	if response := controlHTTPResponse(t, app, http.MethodPut, schedulePath, controlToken, "application/json", request); response.Code != http.StatusCreated {
		t.Fatalf("schedule status=%d body=%s", response.Code, response.Body.String())
	}
	app.runSchedulerTick()
	jobID := storage.ScheduledJobID(firstControlScheduleID, at.UnixMilli())
	jobPath := controlJobPathPrefix + jobID
	if response := controlHTTPResponse(t, app, http.MethodPut, jobPath, controlToken, "application/json", validControlTestRequest(agentID, protocol.ProbeTypeTCPConnect)); response.Code != http.StatusBadRequest {
		t.Fatalf("64-character one-shot PUT status=%d body=%s", response.Code, response.Body.String())
	}
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()
	runner, err := agent.New(agent.Config{
		ServerURL: httpServer.URL, AgentID: agentID, Token: agentToken,
		Interval: time.Hour, JobInterval: time.Millisecond, Timeout: 2 * time.Second,
		AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "agent.state"),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- runner.Run(ctx) }()
	var finished controlJobView
	for ctx.Err() == nil {
		response := controlHTTPResponse(t, app, http.MethodGet, jobPath, controlToken, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("job status=%d body=%s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &finished); err != nil {
			t.Fatal(err)
		}
		if finished.Status == storage.JobStatusFinished {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if finished.Status != storage.JobStatusFinished || finished.Result == nil || !finished.Result.Success {
		t.Fatalf("job=%+v context=%v", finished, ctx.Err())
	}
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	if err := <-runnerDone; err != nil {
		t.Fatal(err)
	}
	app.Shutdown()
	httpServer.Close()
	_ = listener.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerLoopRunsImmediatelyAndStops(t *testing.T) {
	_, store, agentID, _ := testApp(t)
	defer store.Close()
	at := time.Unix(7_000, 0)
	params := storage.PutScheduleParams{
		ID: "schedule", AgentID: agentID, Name: "loop", ProbeType: protocol.ProbeTypeHTTP,
		Config:    protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://example.com", Method: "GET"}},
		TimeoutMS: 5000, IntervalSeconds: 60, Enabled: true, Now: at.UnixMilli(),
	}
	if _, _, err := store.PutProbeSchedule(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(store, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	app.now = func() time.Time { return at }
	done := make(chan struct{})
	go func() { defer close(done); app.schedulerLoop(time.Hour) }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := store.GetProbeJob(context.Background(), storage.ScheduledJobID("schedule", at.UnixMilli())); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("immediate scheduler pass did not materialize job")
		}
		time.Sleep(time.Millisecond)
	}
	app.Shutdown()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}
