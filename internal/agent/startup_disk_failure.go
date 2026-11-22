package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

const startupDiskFailureInterval = time.Minute

// ReportStartupFailure is called by the command before exiting on a startup
// error. Waiting before exit bounds requests across supervisor restarts even
// when the filesystem cannot store a retry marker.
func ReportStartupFailure(ctx context.Context, config Config, logger *slog.Logger, startupErr error) {
	reportStartupFailure(ctx, config, logger, startupErr, startupDiskFailureInterval)
}

func reportStartupFailure(ctx context.Context, config Config, logger *slog.Logger, startupErr error, interval time.Duration) {
	var diskErr *epochWriteError
	if !errors.As(startupErr, &diskErr) || ctx.Err() != nil {
		return
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	if err := sendStartupDiskFailure(ctx, config); err != nil && ctx.Err() == nil {
		logger.Warn("send startup disk failure", "error", err)
	}
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func sendStartupDiskFailure(ctx context.Context, config Config) error {
	endpoint := strings.TrimRight(config.ServerURL, "/") + protocol.StartupDiskFailurePath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+config.Token)
	client := &http.Client{
		Timeout: min(config.Timeout, 8*time.Second),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := doAgentServerRequest(client, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("server returned %s", resp.Status)
	}
	return nil
}
