package updater

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

var (
	errDownloadDeadline = errors.New("shared download deadline exceeded")
	errDownloadHeaders  = errors.New("connection or response headers timed out")
	errDownloadStalled  = errors.New("download stalled")
)

// The header timer covers DNS, connection, TLS, and any redirects before
// the final response headers. The request context stays alive for the body.
func (e *Engine) openDownload(request *http.Request) (*http.Response, context.Context, context.CancelCauseFunc, error) {
	ctx, cancel := context.WithCancelCause(request.Context())
	var timer *time.Timer
	if e.config.DownloadHeaderTimeout > 0 {
		timer = time.AfterFunc(e.config.DownloadHeaderTimeout, func() { cancel(errDownloadHeaders) })
	}
	response, err := e.config.HTTPClient.Do(request.WithContext(ctx))
	if timer != nil && !timer.Stop() {
		cancel(errDownloadHeaders)
	}
	if cause := context.Cause(ctx); cause != nil {
		if err == nil {
			_ = response.Body.Close()
		}
		err = cause
	}
	if err != nil {
		cancel(nil)
		return nil, nil, nil, err
	}
	return response, ctx, cancel, nil
}

type downloadProgress struct {
	reader io.Reader
	bytes  atomic.Int64
}

func (p *downloadProgress) Read(buffer []byte) (int, error) {
	n, err := p.reader.Read(buffer)
	p.bytes.Add(int64(n))
	return n, err
}

func (e *Engine) readDownloadBody(ctx context.Context, cancel context.CancelCauseFunc, body io.Reader, limit int64) ([]byte, error) {
	reader := io.LimitReader(body, limit)
	window := e.config.DownloadStallWindow
	if window <= 0 {
		return io.ReadAll(reader)
	}
	progress := &downloadProgress{reader: reader}
	done, stopped := make(chan struct{}), make(chan struct{})
	started := time.Now()
	go func() {
		defer close(stopped)
		watchDownloadSpeed(ctx, cancel, done, progress, started, window, e.config.DownloadMinBytesPerSecond)
	}()
	data, err := io.ReadAll(progress)
	close(done)
	<-stopped
	if cause := context.Cause(ctx); cause != nil {
		return data, cause
	}
	return data, err
}

func watchDownloadSpeed(ctx context.Context, cancel context.CancelCauseFunc, done <-chan struct{}, progress *downloadProgress, started time.Time, window time.Duration, minSpeed int64) {
	interval := max(time.Nanosecond, min(time.Second, window/10))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	type sample struct {
		at    time.Time
		bytes int64
	}
	samples := []sample{{at: started}}
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			total := progress.bytes.Load()
			// Keep the last sample at or before the start of the window.
			cutoff := now.Add(-window)
			for len(samples) > 1 && !samples[1].at.After(cutoff) {
				samples = samples[1:]
			}
			oldest := samples[0]
			if !oldest.at.After(cutoff) && float64(total-oldest.bytes) < float64(minSpeed)*now.Sub(oldest.at).Seconds() {
				select {
				case <-done:
				default:
					cancel(errDownloadStalled)
				}
				return
			}
			samples = append(samples, sample{at: now, bytes: total})
		}
	}
}
