package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"404-probe/internal/agent"
	"404-probe/internal/buildinfo"
	"404-probe/internal/geoip"
	"404-probe/internal/platformsupport"
	"404-probe/internal/securitycollector"
	"404-probe/internal/updater"
)

func main() {
	if err := checkPlatformCommand(os.Args[1:], platformsupport.Host()); err != nil {
		slog.Error("unsupported Agent command", "error", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "country-code" {
		if err := runCountryCodeLookup(os.Args[2:], os.Stdout); err != nil {
			slog.Error("country lookup failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "selector-order" {
		if err := runSelectorOrder(os.Args[2:]); err != nil {
			slog.Error("selector order extraction failed", "error", err)
			os.Exit(1)
		}
		return
	}
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
		if len(os.Args) == 3 && (os.Args[2] == "migrate-legacy" || os.Args[2] == "migrate-beta") {
			if err := updater.ServeLocalMigration(ctx); err != nil {
				slog.Error("local Beta migration stopped", "error", err)
				os.Exit(1)
			}
			return
		}
		if len(os.Args) == 3 && os.Args[2] == "removal-worker" {
			if err := updater.ServeAgentRemovalWorker(ctx); err != nil {
				slog.Error("Agent removal worker stopped", "error", err)
				os.Exit(1)
			}
			return
		}
		if len(os.Args) == 3 && os.Args[2] == "removal-finalize" {
			if err := updater.ServeAgentRemovalFinalizer(ctx); err != nil {
				slog.Error("Agent removal finalizer stopped", "error", err)
				os.Exit(1)
			}
			return
		}
		if len(os.Args) != 2 {
			slog.Error("updater accepts no arguments except fixed removal modes")
			os.Exit(2)
		}
		if err := updater.Serve(ctx); err != nil {
			slog.Error("Agent updater stopped", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "security-collect" {
		if len(os.Args) != 2 {
			slog.Error("security collector accepts no arguments")
			os.Exit(2)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := securitycollector.Run(ctx, securitycollector.StateRoot, time.Now()); err != nil {
			slog.Error("security collector stopped", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func runCountryCodeLookup(arguments []string, output io.Writer) error {
	if len(arguments) != 1 || arguments[0] != "lookup" {
		return errors.New("usage: 404-probe-agent country-code lookup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), geoip.DefaultTimeout)
	defer cancel()
	code, err := geoip.LookupCountryCode(ctx, geoip.Client(geoip.DefaultTimeout), geoip.Endpoint)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, code)
	return err
}

func runSelectorOrder(arguments []string) error {
	if len(arguments) == 0 || arguments[0] != "extract" {
		return errors.New("usage: 404-probe-agent selector-order extract --config <path> --output <path>")
	}
	flags := flag.NewFlagSet("selector-order", flag.ContinueOnError)
	config := flags.String("config", "", "absolute sing-box JSON config path")
	output := flags.String("output", "", "absolute secret-free metadata output path")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *config == "" || *output == "" {
		return errors.New("usage: 404-probe-agent selector-order extract --config <path> --output <path>")
	}
	return agent.ExtractSelectorOrder(*config, *output)
}

func checkPlatformCommand(args []string, policy platformsupport.Policy) error {
	if len(args) == 0 {
		return nil
	}
	return policy.CheckCommand(args[0])
}

func run() error {
	remoteRemovalEnabled, err := remoteRemovalOptInFromEnv()
	if err != nil {
		return err
	}
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
	clashAPI := flag.String("sing-box-clash-api", clashAPIFromEnv(), "local sing-box Clash API loopback origin")
	selectorOrder := flag.String("selector-order", env("PROBE_404_SELECTOR_ORDER", agent.DefaultSelectorOrderPath), "local secret-free selector order metadata")
	outboundInterval := flag.Duration("outbound-interval", time.Minute, "sing-box outbound discovery interval")
	updaterSocket := flag.String("updater-socket", env("PROBE_404_UPDATER_SOCKET", updater.DefaultSocket), "restricted Agent updater socket")
	securityExport := flag.String("security-export", env("PROBE_404_SECURITY_EXPORT", agent.DefaultSecurityExportDir), "read-only local security aggregate export")
	securityAcks := flag.String("security-acks", env("PROBE_404_SECURITY_ACKS", agent.DefaultSecurityAckPath), "security upload acknowledgement state")
	countryCode := flag.String("country-code", env("PROBE_404_COUNTRY_CODE", ""), "persisted install-time Agent egress country code")
	enableRemoteRemoval := flag.Bool("enable-remote-removal", remoteRemovalEnabled, "enable remote Agent removal only after isolated acceptance")
	flag.Parse()
	_, legacyClashSecret := os.LookupEnv("PROBE_404_SING_BOX_CLASH_SECRET")
	runner, err := agent.New(agent.Config{ServerURL: *server, AgentID: *id, Token: *token, Interval: *interval, JobInterval: *jobInterval, DisabledInterval: *disabledInterval, Timeout: *timeout, AllowInsecureHTTP: *insecure, StatePath: *state, NetworkIncludes: split(*include), NetworkExcludes: split(*exclude), ClashAPIURL: *clashAPI, SelectorOrderPath: *selectorOrder, LegacyClashSecretConfigured: legacyClashSecret, OutboundInterval: *outboundInterval, AgentVersion: buildinfo.Current().Version, UpdaterSocket: *updaterSocket, SecurityExportDir: *securityExport, SecurityAckPath: *securityAcks, SecurityInterval: 5 * time.Minute, CountryCode: *countryCode, EnableRemoteRemoval: *enableRemoteRemoval}, slog.Default())
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runner.Run(ctx)
}

func remoteRemovalOptInFromEnv() (bool, error) {
	value, ok := os.LookupEnv("PROBE_404_ENABLE_REMOTE_REMOVAL")
	return parseRemoteRemovalOptIn(value, ok)
}

func parseRemoteRemovalOptIn(value string, present bool) (bool, error) {
	if !present {
		return false, nil
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("PROBE_404_ENABLE_REMOTE_REMOVAL must be true or false")
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func clashAPIFromEnv() string {
	if value, present := os.LookupEnv("PROBE_404_SING_BOX_CLASH_API"); present {
		return value
	}
	return agent.DefaultClashAPIURL
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
