package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	appserver "404-probe/internal/server"
	"404-probe/internal/storage"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("server command failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "agent":
		return agentCommand(args[1:])
	case "probe":
		return probeCommand(args[1:])
	case "schedule":
		return scheduleCommand(args[1:])
	case "help", "-h", "--help":
		return usageError()
	default:
		return fmt.Errorf("unknown command %q\n%w", args[0], usageError())
	}
}

func usageError() error {
	return errors.New("usage: 404-probe-server serve [flags] | agent add <name> [--db path] | agent list [--db path] | agent revoke <id> [--db path] | probe <run|get> [flags] | schedule <add|list|enable|disable|delete> [flags]")
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := flags.String("listen", ":8080", "listen address")
	dbPath := flags.String("db", "404-probe.db", "SQLite database path")
	offline := flags.Duration("offline-timeout", 30*time.Second, "offline threshold")
	controlTokenFile := flags.String("control-token-file", "", "path to the control API token file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var appOptions []appserver.Option
	if *controlTokenFile != "" {
		hash, err := loadControlTokenHash(*controlTokenFile)
		if err != nil {
			return err
		}
		appOptions = append(appOptions, appserver.WithControlTokenHash(hash))
	}
	store, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	app, err := appserver.NewApp(store, *offline, slog.Default(), appOptions...)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: *listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	cleanupDone := make(chan struct{})
	schedulerDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		app.CleanupLoop()
	}()
	go func() {
		defer close(schedulerDone)
		app.SchedulerLoop()
	}()
	defer func() {
		app.Shutdown()
		<-cleanupDone
		<-schedulerDone
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		slog.Info("server listening", "address", *listen, "database", *dbPath)
		errCh <- httpServer.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		app.Shutdown()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func agentCommand(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	sub := args[0]
	flags := flag.NewFlagSet("agent "+sub, flag.ContinueOnError)
	dbPath := flags.String("db", "404-probe.db", "SQLite database path")
	positional := args[1:]
	var value string
	if len(positional) > 0 && !startsFlag(positional[0]) {
		value = positional[0]
		positional = positional[1:]
	}
	if err := flags.Parse(positional); err != nil {
		return err
	}
	store, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx := context.Background()
	switch sub {
	case "add":
		if value == "" {
			return errors.New("agent name is required")
		}
		id, err := auth.NewID()
		if err != nil {
			return err
		}
		plain, hash, err := auth.NewToken()
		if err != nil {
			return err
		}
		if err = store.AddAgent(ctx, id, value, hash, time.Now()); err != nil {
			return err
		}
		fmt.Printf("Agent ID: %s\nToken: %s\n\nSave this token now; the server stores only its SHA-256 hash.\n", id, plain)
		return nil
	case "list":
		agents, err := store.ListAgents(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tREVOKED\tCREATED")
		for _, a := range agents {
			fmt.Fprintf(w, "%s\t%s\t%t\t%s\n", a.ID, a.Name, a.Revoked, time.UnixMilli(a.CreatedAt).Format(time.RFC3339))
		}
		return w.Flush()
	case "revoke":
		if value == "" {
			return errors.New("agent ID is required")
		}
		changed, err := store.RevokeAgent(ctx, value, time.Now())
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("agent not found or already revoked")
		}
		fmt.Printf("Revoked agent %s\n", value)
		return nil
	default:
		return usageError()
	}
}

func probeCommand(args []string) error {
	return runProbeCommand(args, time.Now, auth.NewID, os.Stdout)
}

func runProbeCommand(args []string, now func() time.Time, newID func() (string, error), output io.Writer) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "run":
		return runProbeCreate(args[1:], now, newID, output)
	case "get":
		return runProbeGet(args[1:], now, output)
	default:
		return usageError()
	}
}

func runProbeCreate(args []string, now func() time.Time, newID func() (string, error), output io.Writer) error {
	flags := flag.NewFlagSet("probe run", flag.ContinueOnError)
	dbPath := flags.String("db", "404-probe.db", "SQLite database path")
	agentID := flags.String("agent-id", "", "target agent ID")
	probeTypeValue := flags.String("type", "", "probe type")
	timeout := flags.Duration("timeout", 5*time.Second, "probe timeout")
	expiresIn := flags.Duration("expires-in", 5*time.Minute, "job lifetime")
	target := flags.String("target", "", "ICMP target")
	count := flags.Int("count", 4, "ICMP packet count")
	host := flags.String("host", "", "TCP host")
	port := flags.Int("port", 0, "TCP port")
	urlValue := flags.String("url", "", "HTTP URL")
	method := flags.String("method", "GET", "HTTP method")
	expectedStatus := flags.Int("expected-status", 0, "optional expected HTTP status")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("probe run does not accept positional arguments")
	}
	probeType := protocol.ProbeType(*probeTypeValue)
	if err := probeType.Validate(); err != nil {
		return err
	}
	if !validCLIHexID(*agentID) {
		return errors.New("agent-id must be 32 lowercase hexadecimal characters")
	}
	setFlags := make(map[string]bool)
	flags.Visit(func(value *flag.Flag) { setFlags[value.Name] = true })
	allowed := map[string]bool{"db": true, "agent-id": true, "type": true, "timeout": true, "expires-in": true}
	var config protocol.ProbeConfig
	switch probeType {
	case protocol.ProbeTypeICMPPing:
		allowed["target"], allowed["count"] = true, true
		config.ICMPPing = &protocol.ICMPPingConfig{Target: *target, Count: *count}
	case protocol.ProbeTypeTCPConnect:
		allowed["host"], allowed["port"] = true, true
		config.TCPConnect = &protocol.TCPConnectConfig{Host: *host, Port: *port}
	case protocol.ProbeTypeHTTP:
		allowed["url"], allowed["method"], allowed["expected-status"] = true, true, true
		var expected *int
		if setFlags["expected-status"] {
			expected = expectedStatus
		}
		config.HTTP = &protocol.HTTPConfig{URL: *urlValue, Method: *method, ExpectedStatus: expected}
	}
	for name := range setFlags {
		if !allowed[name] {
			return fmt.Errorf("flag --%s is not valid for probe type %s", name, probeType)
		}
	}
	if err := config.Validate(probeType); err != nil {
		return err
	}
	timeoutMillis := timeout.Milliseconds()
	lifetimeMillis := expiresIn.Milliseconds()
	if *timeout <= 0 || time.Duration(timeoutMillis)*time.Millisecond != *timeout ||
		timeoutMillis < protocol.MinProbeTimeoutMS || timeoutMillis > protocol.MaxProbeTimeoutMS {
		return fmt.Errorf("timeout must be between %dms and %dms in whole milliseconds", protocol.MinProbeTimeoutMS, protocol.MaxProbeTimeoutMS)
	}
	if *expiresIn < time.Minute || *expiresIn > 24*time.Hour || time.Duration(lifetimeMillis)*time.Millisecond != *expiresIn {
		return errors.New("expires-in must be between 1m and 24h in whole milliseconds")
	}
	at := now()
	nowMillis := at.UnixMilli()
	if nowMillis <= 0 || lifetimeMillis > math.MaxInt64-nowMillis {
		return errors.New("job time is outside the supported range")
	}
	jobID, err := newID()
	if err != nil {
		return err
	}
	store, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	_, _, err = store.CreateOneShotJobIdempotent(context.Background(), storage.CreateOneShotJobParams{
		ID: jobID, AgentID: *agentID, ProbeType: probeType, Config: config, TimeoutMS: int(timeoutMillis),
		CreatedAt: nowMillis, NotBefore: nowMillis, ExpiresAt: nowMillis + lifetimeMillis,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Job ID: %s\n", jobID)
	return err
}

func runProbeGet(args []string, now func() time.Time, output io.Writer) error {
	if len(args) == 0 || startsFlag(args[0]) || !validCLIJobID(args[0]) {
		return errors.New("job ID must be 32 or 64 lowercase hexadecimal characters")
	}
	jobID := args[0]
	flags := flag.NewFlagSet("probe get", flag.ContinueOnError)
	dbPath := flags.String("db", "404-probe.db", "SQLite database path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("probe get accepts exactly one job ID")
	}
	at := now()
	if at.UnixMilli() <= 0 {
		return errors.New("snapshot time is outside the supported range")
	}
	store, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	job, result, err := store.GetProbeJobSnapshot(context.Background(), jobID, at)
	if err != nil {
		return err
	}
	return writeProbeSnapshot(output, job, result)
}

func writeProbeSnapshot(output io.Writer, job storage.ProbeJobRecord, result *storage.ProbeResultRecord) error {
	switch job.Status {
	case storage.JobStatusQueued, storage.JobStatusExpired:
	case storage.JobStatusLeased:
		if job.LeasedAt <= 0 || job.LeaseUntil <= job.LeasedAt {
			return fmt.Errorf("%w: leased job has invalid lease timestamps", storage.ErrCorruptProbeData)
		}
	case storage.JobStatusFinished:
		if job.FinishedAt <= 0 || result == nil || result.ReceivedAt != job.FinishedAt {
			return fmt.Errorf("%w: finished job is missing result data", storage.ErrCorruptProbeData)
		}
	default:
		return fmt.Errorf("%w: invalid job status", storage.ErrCorruptProbeData)
	}
	if job.Status != storage.JobStatusFinished && result != nil {
		return fmt.Errorf("%w: unfinished job has result data", storage.ErrCorruptProbeData)
	}
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	write := func(label, value string) error {
		_, err := fmt.Fprintf(w, "%s\t%s\n", label, value)
		return err
	}
	fields := [][2]string{
		{"JOB ID", job.ID},
		{"AGENT ID", job.AgentID},
		{"TYPE", string(job.ProbeType)},
		{"STATUS", string(job.Status)},
		{"ATTEMPT", fmt.Sprint(job.Attempt)},
		{"TIMEOUT", fmt.Sprintf("%dms", job.TimeoutMS)},
		{"CREATED", formatCLIUnixMilli(job.CreatedAt)},
		{"NOT BEFORE", formatCLIUnixMilli(job.NotBefore)},
		{"EXPIRES", formatCLIUnixMilli(job.ExpiresAt)},
	}
	if job.ScheduleID != "" {
		fields = append(fields, [2]string{"SCHEDULE ID", job.ScheduleID}, [2]string{"SCHEDULED FOR", formatCLIUnixMilli(job.ScheduledFor)})
	}
	if job.Status == storage.JobStatusLeased {
		fields = append(fields, [2]string{"LEASED AT", formatCLIUnixMilli(job.LeasedAt)}, [2]string{"LEASE UNTIL", formatCLIUnixMilli(job.LeaseUntil)})
	}
	if job.Status == storage.JobStatusFinished && result != nil {
		value := result.Result
		fields = append(fields,
			[2]string{"FINISHED", formatCLIUnixMilli(job.FinishedAt)},
			[2]string{"RESULT RECEIVED", formatCLIUnixMilli(result.ReceivedAt)},
			[2]string{"RESULT STARTED", formatCLIUnixMilli(value.StartedAt)},
			[2]string{"RESULT FINISHED", formatCLIUnixMilli(value.FinishedAt)},
			[2]string{"DURATION", fmt.Sprintf("%gms", value.DurationMS)},
			[2]string{"SUCCESS", fmt.Sprint(value.Success)},
		)
		if value.ResolvedIP != "" {
			fields = append(fields, [2]string{"RESOLVED IP", value.ResolvedIP})
		}
		if value.ErrorCategory != "" {
			fields = append(fields, [2]string{"ERROR CATEGORY", value.ErrorCategory})
		}
		switch job.ProbeType {
		case protocol.ProbeTypeICMPPing:
			measurement := value.Result.ICMPPing
			fields = append(fields,
				[2]string{"PACKETS", fmt.Sprintf("%d/%d", measurement.Received, measurement.Sent)},
				[2]string{"PACKET LOSS", fmt.Sprintf("%g%%", measurement.PacketLossPercent)},
				[2]string{"LATENCY AVG", fmt.Sprintf("%gms", measurement.LatencyAvgMS)},
			)
		case protocol.ProbeTypeTCPConnect:
			fields = append(fields, [2]string{"CONNECT", fmt.Sprintf("%gms", value.Result.TCPConnect.ConnectMS)})
		case protocol.ProbeTypeHTTP:
			fields = append(fields,
				[2]string{"HTTP STATUS", fmt.Sprint(value.Result.HTTP.StatusCode)},
				[2]string{"HTTP TOTAL", fmt.Sprintf("%gms", value.Result.HTTP.TotalMS)},
			)
		}
	}
	for _, field := range fields {
		if err := write(field[0], field[1]); err != nil {
			return err
		}
	}
	return w.Flush()
}

func formatCLIUnixMilli(value int64) string {
	return time.UnixMilli(value).UTC().Format(time.RFC3339Nano)
}

func validCLIJobID(value string) bool {
	return (len(value) == 32 || len(value) == 64) && validCLILowerHex(value)
}

func validCLIHexID(value string) bool {
	return len(value) == 32 && validCLILowerHex(value)
}

func validCLILowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func scheduleCommand(args []string) error {
	return runScheduleCommand(args, time.Now, auth.NewID, os.Stdout)
}

func runScheduleCommand(args []string, now func() time.Time, newID func() (string, error), output io.Writer) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "add":
		return runScheduleAdd(args[1:], now, newID, output)
	case "list":
		return runScheduleList(args[1:], output)
	case "enable", "disable", "delete":
		return runScheduleMutation(args[0], args[1:], now, output)
	default:
		return usageError()
	}
}

func runScheduleList(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("schedule list", flag.ContinueOnError)
	dbPath := flags.String("db", "404-probe.db", "SQLite database path")
	agentID := flags.String("agent-id", "", "Agent ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("schedule list does not accept positional arguments")
	}
	if !validCLIHexID(*agentID) {
		return errors.New("agent-id must be 32 lowercase hexadecimal characters")
	}
	store, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	records, err := store.ListProbeSchedules(context.Background(), *agentID)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tTYPE\tENABLED\tINTERVAL\tNEXT RUN"); err != nil {
		return err
	}
	for _, record := range records {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%ds\t%s\n", record.ID, record.Name, record.ProbeType,
			record.Enabled, record.IntervalSeconds, time.UnixMilli(record.NextRunAt).UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return w.Flush()
}

func runScheduleAdd(args []string, now func() time.Time, newID func() (string, error), output io.Writer) error {
	flags := flag.NewFlagSet("schedule add", flag.ContinueOnError)
	dbPath := flags.String("db", "404-probe.db", "SQLite database path")
	agentID := flags.String("agent-id", "", "Agent ID")
	name := flags.String("name", "", "schedule name")
	probeTypeValue := flags.String("type", "", "probe type")
	timeout := flags.Duration("timeout", 5*time.Second, "probe timeout")
	interval := flags.Duration("interval", time.Minute, "fixed interval")
	enabled := flags.Bool("enabled", true, "enable schedule")
	target := flags.String("target", "", "ICMP target")
	count := flags.Int("count", 4, "ICMP packet count")
	host := flags.String("host", "", "TCP host")
	port := flags.Int("port", 0, "TCP port")
	urlValue := flags.String("url", "", "HTTP URL")
	method := flags.String("method", "GET", "HTTP method")
	expectedStatus := flags.Int("expected-status", 0, "optional expected HTTP status")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("schedule add does not accept positional arguments")
	}
	probeType := protocol.ProbeType(*probeTypeValue)
	if err := probeType.Validate(); err != nil {
		return err
	}
	if !validCLIHexID(*agentID) {
		return errors.New("agent-id must be 32 lowercase hexadecimal characters")
	}
	setFlags := make(map[string]bool)
	flags.Visit(func(value *flag.Flag) { setFlags[value.Name] = true })
	allowed := map[string]bool{"db": true, "agent-id": true, "name": true, "type": true, "timeout": true, "interval": true, "enabled": true}
	var config protocol.ProbeConfig
	switch probeType {
	case protocol.ProbeTypeICMPPing:
		allowed["target"], allowed["count"] = true, true
		config.ICMPPing = &protocol.ICMPPingConfig{Target: *target, Count: *count}
	case protocol.ProbeTypeTCPConnect:
		allowed["host"], allowed["port"] = true, true
		config.TCPConnect = &protocol.TCPConnectConfig{Host: *host, Port: *port}
	case protocol.ProbeTypeHTTP:
		allowed["url"], allowed["method"], allowed["expected-status"] = true, true, true
		var expected *int
		if setFlags["expected-status"] {
			expected = expectedStatus
		}
		config.HTTP = &protocol.HTTPConfig{URL: *urlValue, Method: *method, ExpectedStatus: expected}
	}
	for flagName := range setFlags {
		if !allowed[flagName] {
			return fmt.Errorf("flag --%s is not valid for probe type %s", flagName, probeType)
		}
	}
	if err := config.Validate(probeType); err != nil {
		return err
	}
	timeoutMillis := timeout.Milliseconds()
	intervalSeconds := int64(*interval / time.Second)
	if *timeout <= 0 || time.Duration(timeoutMillis)*time.Millisecond != *timeout || timeoutMillis < protocol.MinProbeTimeoutMS || timeoutMillis > protocol.MaxProbeTimeoutMS {
		return fmt.Errorf("timeout must be between %dms and %dms in whole milliseconds", protocol.MinProbeTimeoutMS, protocol.MaxProbeTimeoutMS)
	}
	if *interval < 30*time.Second || *interval > 7*24*time.Hour || time.Duration(intervalSeconds)*time.Second != *interval {
		return errors.New("interval must be between 30s and 168h in whole seconds")
	}
	at := now().UnixMilli()
	if at <= 0 {
		return errors.New("schedule time is outside the supported range")
	}
	scheduleID, err := newID()
	if err != nil {
		return err
	}
	store, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	_, _, err = store.PutProbeSchedule(context.Background(), storage.PutScheduleParams{
		ID: scheduleID, AgentID: *agentID, Name: *name, ProbeType: probeType, Config: config,
		TimeoutMS: int(timeoutMillis), IntervalSeconds: int(intervalSeconds), Enabled: *enabled, Now: at,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Schedule ID: %s\n", scheduleID)
	return err
}

func runScheduleMutation(command string, args []string, now func() time.Time, output io.Writer) error {
	if len(args) == 0 || startsFlag(args[0]) || !validCLIHexID(args[0]) {
		return errors.New("schedule ID must be 32 lowercase hexadecimal characters")
	}
	scheduleID := args[0]
	flags := flag.NewFlagSet("schedule "+command, flag.ContinueOnError)
	dbPath := flags.String("db", "404-probe.db", "SQLite database path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("schedule %s accepts exactly one schedule ID", command)
	}
	var at int64
	if command != "delete" {
		at = now().UnixMilli()
		if at <= 0 {
			return errors.New("schedule time is outside the supported range")
		}
	}
	store, err := openStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	if command == "delete" {
		if err := store.DeleteProbeSchedule(context.Background(), scheduleID); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "Deleted schedule %s\n", scheduleID)
		return err
	}
	record, err := store.GetProbeSchedule(context.Background(), scheduleID)
	if err != nil {
		return err
	}
	enabled := command == "enable"
	_, _, err = store.PutProbeSchedule(context.Background(), storage.PutScheduleParams{
		ID: record.ID, AgentID: record.AgentID, Name: record.Name, ProbeType: record.ProbeType, Config: record.Config,
		TimeoutMS: record.TimeoutMS, IntervalSeconds: record.IntervalSeconds, Enabled: enabled, Now: at,
	})
	if err != nil {
		return err
	}
	verb := "Disabled"
	if enabled {
		verb = "Enabled"
	}
	_, err = fmt.Fprintf(output, "%s schedule %s\n", verb, scheduleID)
	return err
}

func startsFlag(value string) bool { return len(value) > 0 && value[0] == '-' }

func loadControlTokenHash(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open control token file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect control token file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("control token file must be a regular file")
	}
	if info.Size() > 1024 {
		return nil, errors.New("control token file is too large")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("control token file permissions must not allow group or other access")
	}
	content, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil {
		return nil, errors.New("read control token file")
	}
	if len(content) > 1024 {
		return nil, errors.New("control token file is too large")
	}
	token := string(content)
	if strings.HasSuffix(token, "\r\n") {
		token = strings.TrimSuffix(token, "\r\n")
	} else if strings.HasSuffix(token, "\n") {
		token = strings.TrimSuffix(token, "\n")
	}
	if len(token) != 43 || strings.ContainsAny(token, "\r\n\t ") {
		return nil, errors.New("control token file contains an invalid token")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return nil, errors.New("control token file contains an invalid token")
	}
	return auth.Hash(token), nil
}

func openStore(path string) (*storage.Store, error) {
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if dir != "." {
			if err := os.MkdirAll(dir, 0700); err != nil {
				return nil, err
			}
		}
	}
	store, err := storage.Open(context.Background(), path)
	if err != nil {
		return nil, err
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0600); err != nil {
			store.Close()
			return nil, err
		}
	}
	return store, nil
}
