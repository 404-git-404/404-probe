package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

type sseTestWriter struct {
	header http.Header

	mu                 sync.Mutex
	deadlines          []time.Time
	writeAttempts      [][]byte
	writes             [][]byte
	flushes            int
	setDeadlineCalls   int
	writeCalls         int
	writeErrAt         int
	writeErr           error
	shortWriteAt       int
	flushErrAt         int
	flushErr           error
	setDeadlineErrAt   int
	setDeadlineErr     error
	blockWriteAt       int
	blockedWrite       chan struct{}
	blockedWriteOnce   sync.Once
	releaseBlocked     chan struct{}
	releaseBlockedOnce sync.Once
	flushNotifications chan struct{}
}

func newSSETestWriter() *sseTestWriter {
	return &sseTestWriter{
		header:             make(http.Header),
		blockedWrite:       make(chan struct{}),
		releaseBlocked:     make(chan struct{}),
		flushNotifications: make(chan struct{}, 8),
	}
}

func (w *sseTestWriter) Header() http.Header { return w.header }

func (w *sseTestWriter) WriteHeader(int) {}

func (w *sseTestWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.setDeadlineCalls++
	w.deadlines = append(w.deadlines, deadline)
	if w.setDeadlineCalls == w.setDeadlineErrAt {
		return w.setDeadlineErr
	}
	return nil
}

func (w *sseTestWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writeCalls++
	call := w.writeCalls
	copyOfFrame := append([]byte(nil), p...)
	w.writeAttempts = append(w.writeAttempts, copyOfFrame)
	if call == w.blockWriteAt {
		w.blockedWriteOnce.Do(func() { close(w.blockedWrite) })
		w.mu.Unlock()
		<-w.releaseBlocked
		return 0, os.ErrDeadlineExceeded
	}
	if call == w.writeErrAt {
		err := w.writeErr
		w.mu.Unlock()
		return 0, err
	}
	if call == w.shortWriteAt {
		w.writes = append(w.writes, copyOfFrame)
		w.mu.Unlock()
		return len(p) - 1, nil
	}
	w.writes = append(w.writes, copyOfFrame)
	w.mu.Unlock()
	return len(p), nil
}

func (w *sseTestWriter) FlushError() error {
	w.mu.Lock()
	w.flushes++
	call := w.flushes
	if call == w.flushErrAt {
		err := w.flushErr
		w.mu.Unlock()
		return err
	}
	w.mu.Unlock()
	w.flushNotifications <- struct{}{}
	return nil
}

func (w *sseTestWriter) waitForFlush(t *testing.T) {
	t.Helper()
	select {
	case <-w.flushNotifications:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SSE flush")
	}
}

func (w *sseTestWriter) releaseWrite() {
	w.releaseBlockedOnce.Do(func() { close(w.releaseBlocked) })
}

func (w *sseTestWriter) snapshot() ([][]byte, []time.Time, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	writes := make([][]byte, len(w.writes))
	for i := range w.writes {
		writes[i] = append([]byte(nil), w.writes[i]...)
	}
	return writes, append([]time.Time(nil), w.deadlines...), w.flushes
}

func startSSETestHandler(t *testing.T, app *App, writer *sseTestWriter, ctx context.Context) <-chan struct{} {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "https://probe.test/api/v1/web/events", nil)
	addTestWebSession(t, app, request)
	if ctx != nil {
		request = request.WithContext(ctx)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.Handler().ServeHTTP(writer, request)
	}()
	return done
}

func waitSSEHandler(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SSE handler to exit")
	}
}

func TestSSEFrameWriteFailuresReleaseSubscription(t *testing.T) {
	tests := []struct {
		name   string
		config func(*sseTestWriter)
	}{
		{name: "set deadline", config: func(w *sseTestWriter) { w.setDeadlineErrAt, w.setDeadlineErr = 1, errors.New("deadline unsupported") }},
		{name: "write", config: func(w *sseTestWriter) { w.writeErrAt, w.writeErr = 1, errors.New("write failed") }},
		{name: "short write", config: func(w *sseTestWriter) { w.shortWriteAt = 1 }},
		{name: "flush", config: func(w *sseTestWriter) { w.flushErrAt, w.flushErr = 1, errors.New("flush failed") }},
		{name: "clear deadline", config: func(w *sseTestWriter) { w.setDeadlineErrAt, w.setDeadlineErr = 2, errors.New("clear deadline failed") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, store, _, _ := testApp(t)
			defer app.Shutdown()
			defer store.Close()
			writer := newSSETestWriter()
			tt.config(writer)
			done := startSSETestHandler(t, app, writer, nil)
			waitSSEHandler(t, done)
			waitForSubscribers(t, app.hub, 0)
		})
	}
}

func TestSSEEventWriteFailureReleasesSubscription(t *testing.T) {
	app, store, _, _ := testApp(t)
	defer app.Shutdown()
	defer store.Close()
	writer := newSSETestWriter()
	writer.writeErrAt, writer.writeErr = 2, errors.New("event write failed")
	done := startSSETestHandler(t, app, writer, nil)
	writer.waitForFlush(t)
	app.hub.publish([]byte(`{"test":true}`))
	waitSSEHandler(t, done)
	waitForSubscribers(t, app.hub, 0)
}

func TestSSEInitialAndEventFramesUseBoundedDeadlines(t *testing.T) {
	app, store, _, _ := testApp(t)
	defer app.Shutdown()
	defer store.Close()
	fixedNow := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	app.now = func() time.Time { return fixedNow }
	writer := newSSETestWriter()
	done := startSSETestHandler(t, app, writer, nil)
	writer.waitForFlush(t)
	app.hub.publish([]byte(`{"id":"agent-1"}`))
	writer.waitForFlush(t)
	app.Shutdown()
	waitSSEHandler(t, done)
	waitForSubscribers(t, app.hub, 0)

	writes, deadlines, flushes := writer.snapshot()
	if len(writes) != 2 || string(writes[0]) != "retry: 3000\n\n" || string(writes[1]) != "event: agent\ndata: {\"id\":\"agent-1\"}\n\n" {
		t.Fatalf("SSE frames=%q", writes)
	}
	if flushes != 2 || len(deadlines) != 4 {
		t.Fatalf("flushes=%d deadlines=%v", flushes, deadlines)
	}
	wantDeadline := fixedNow.Add(sseFrameWriteTimeout)
	if !deadlines[0].Equal(wantDeadline) || !deadlines[1].IsZero() || !deadlines[2].Equal(wantDeadline) || !deadlines[3].IsZero() {
		t.Fatalf("per-frame deadlines=%v want [%v zero %v zero]", deadlines, wantDeadline, wantDeadline)
	}
}

func TestSSEBlockedWriteDeadlineAndShutdownExitHandler(t *testing.T) {
	app, store, _, _ := testApp(t)
	defer app.Shutdown()
	defer store.Close()
	fixedNow := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	app.now = func() time.Time { return fixedNow }
	writer := newSSETestWriter()
	writer.blockWriteAt = 2
	defer writer.releaseWrite()
	done := startSSETestHandler(t, app, writer, nil)
	writer.waitForFlush(t)
	app.hub.publish([]byte(`{"id":"agent-1"}`))
	select {
	case <-writer.blockedWrite:
	case <-time.After(time.Second):
		t.Fatal("event write did not reach controlled blocking point")
	}
	_, deadlines, _ := writer.snapshot()
	if len(deadlines) != 3 || !deadlines[2].Equal(fixedNow.Add(sseFrameWriteTimeout)) {
		t.Fatalf("blocked write deadline=%v", deadlines)
	}
	app.Shutdown()
	writer.releaseWrite() // The fake writer simulates the socket returning at its installed write deadline.
	waitSSEHandler(t, done)
	waitForSubscribers(t, app.hub, 0)
}

func TestSSERequestDisconnectReleasesSubscription(t *testing.T) {
	app, store, _, _ := testApp(t)
	defer app.Shutdown()
	defer store.Close()
	writer := newSSETestWriter()
	ctx, cancel := context.WithCancel(context.Background())
	done := startSSETestHandler(t, app, writer, ctx)
	writer.waitForFlush(t)
	cancel()
	waitSSEHandler(t, done)
	waitForSubscribers(t, app.hub, 0)
}
