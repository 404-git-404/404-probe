package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"404-probe/internal/agent"
	"404-probe/internal/buildinfo"
	"404-probe/internal/updater"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		info := buildinfo.Current()
		if len(os.Args) == 3 && os.Args[2] == "--json" {
			if err := json.NewEncoder(os.Stdout).Encode(info); err != nil {
				slog.Error("write Agent version", "error", err)
				os.Exit(1)
			}
			return
		}
		fmt.Println(info.Version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "updater" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := updater.Serve(ctx); err != nil {
			slog.Error("Agent updater stopped", "error", err)
			os.Exit(1)
		}
		return
	}
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
	jobInterval := flag.Duration("job-interval", agent.DefaultJobInterval, "job claim interval")
	disabledInterval := flag.Duration("disabled-interval", agent.DefaultDisabledInterval, "server recheck interval while disabled")
	timeout := flag.Duration("timeout", 8*time.Second, "HTTP request timeout")
	insecure := flag.Bool("allow-insecure-http", false, "allow plain HTTP for localhost/development")
	state := flag.String("state", env("PROBE_404_STATE", "404-probe-agent.state"), "persistent agent epoch state file")
	include := flag.String("network-include", "", "comma-separated interface glob patterns")
	exclude := flag.String("network-exclude", "", "comma-separated additional interface glob patterns")
	clashAPI := flag.String("sing-box-clash-api", env("PROBE_404_SING_BOX_CLASH_API", agent.DefaultClashAPIURL), "local sing-box Clash API loopback origin")
	outboundInterval := flag.Duration("outbound-interval", time.Minute, "sing-box outbound discovery interval")
	updaterSocket := flag.String("updater-socket", env("PROBE_404_UPDATER_SOCKET", "/run/404-probe/agent-updater.sock"), "restricted Agent updater socket")
	flag.Parse()
	_, legacyClashSecret := os.LookupEnv("PROBE_404_SING_BOX_CLASH_SECRET")
	runner, err := agent.New(agent.Config{ServerURL: *server, AgentID: *id, Token: *token, Interval: *interval, JobInterval: *jobInterval, DisabledInterval: *disabledInterval, Timeout: *timeout, AllowInsecureHTTP: *insecure, StatePath: *state, NetworkIncludes: split(*include), NetworkExcludes: split(*exclude), ClashAPIURL: *clashAPI, LegacyClashSecretConfigured: legacyClashSecret, OutboundInterval: *outboundInterval, AgentVersion: buildinfo.Current().Version, UpdaterSocket: *updaterSocket}, slog.Default())
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
