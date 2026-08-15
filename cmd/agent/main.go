package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"404-probe/internal/agent"
)

func main() {
	if err := run(); err != nil {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	server := flag.String("server", env("PROBE_404_SERVER", ""), "server base URL")
	id := flag.String("agent-id", env("PROBE_404_AGENT_ID", ""), "agent ID created by the server")
	token := flag.String("token", env("PROBE_404_TOKEN", ""), "per-agent token (prefer environment variable)")
	interval := flag.Duration("interval", 10*time.Second, "report interval")
	timeout := flag.Duration("timeout", 8*time.Second, "HTTP request timeout")
	insecure := flag.Bool("allow-insecure-http", false, "allow plain HTTP for localhost/development")
	state := flag.String("state", env("PROBE_404_STATE", "404-probe-agent.state"), "persistent agent epoch state file")
	include := flag.String("network-include", "", "comma-separated interface glob patterns")
	exclude := flag.String("network-exclude", "", "comma-separated additional interface glob patterns")
	flag.Parse()
	runner, err := agent.New(agent.Config{ServerURL: *server, AgentID: *id, Token: *token, Interval: *interval, Timeout: *timeout, AllowInsecureHTTP: *insecure, StatePath: *state, NetworkIncludes: split(*include), NetworkExcludes: split(*exclude)}, slog.Default())
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runner.Run(ctx)
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func split(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}
