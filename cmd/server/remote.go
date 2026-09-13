package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const (
	remoteRequestTimeout = 15 * time.Second
	maxRemoteBodyBytes   = 2 << 20
	maxRemoteCursorBytes = 2048
)

type remoteClientOptions struct {
	Transport http.RoundTripper
	Timeout   time.Duration
}

type remoteClient struct {
	origin string
	token  string
	http   *http.Client
}

type remoteCommandFlags struct {
	server            *string
	tokenFile         *string
	allowInsecureHTTP *bool
	jsonOutput        *bool
}

type remoteAgentSummaryView struct {
	AgentID   string `json:"agent_id"`
	Name      string `json:"name"`
	Revoked   bool   `json:"revoked"`
	CreatedAt int64  `json:"created_at"`
	Online    bool   `json:"online"`
	LastSeen  *int64 `json:"last_seen"`
}

type remoteAgentView struct {
	remoteAgentSummaryView
	State *remoteAgentStateView `json:"state"`
}

type remoteAgentStateView struct {
	CountryCode     string   `json:"country_code,omitempty"`
	Hostname        string   `json:"hostname"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	Uptime          uint64   `json:"uptime"`
	CPUPercent      float64  `json:"cpu_percent"`
	CPUStealPercent *float64 `json:"cpu_steal_percent,omitempty"`
	Load1           float64  `json:"load1"`
	Load5           float64  `json:"load5"`
	Load15          float64  `json:"load15"`
	RAMUsed         uint64   `json:"ram_used"`
	RAMTotal        uint64   `json:"ram_total"`
	RAMPercent      float64  `json:"ram_percent"`
	SwapUsed        uint64   `json:"swap_used"`
	SwapTotal       uint64   `json:"swap_total"`
	SwapPercent     float64  `json:"swap_percent"`
	DiskUsed        uint64   `json:"disk_used"`
	DiskTotal       uint64   `json:"disk_total"`
	DiskPercent     float64  `json:"disk_percent"`
	DiskReadRate    *float64 `json:"disk_read_rate,omitempty"`
	DiskWriteRate   *float64 `json:"disk_write_rate,omitempty"`
	DiskBusyPercent *float64 `json:"disk_busy_percent,omitempty"`
	RXRate          float64  `json:"rx_rate"`
	TXRate          float64  `json:"tx_rate"`
	RXTotal         uint64   `json:"rx_total"`
	TXTotal         uint64   `json:"tx_total"`
	CollectedAt     int64    `json:"collected_at"`
}

type remoteAgentCollection struct {
	Items      []remoteAgentSummaryView `json:"items"`
	NextCursor *string                  `json:"next_cursor"`
}

type remoteAPIErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func remoteCommand(args []string) error {
	return runRemoteCommand(args, remoteClientOptions{}, os.Stdout)
}

func runRemoteCommand(args []string, options remoteClientOptions, output io.Writer) error {
	if len(args) < 2 {
		return usageError()
	}
	switch args[0] {
	case "agent":
		switch args[1] {
		case "list":
			return runRemoteAgentList(args[2:], options, output)
		case "get":
			return runRemoteAgentGet(args[2:], options, output)
		}
	case "schedule":
		switch args[1] {
		case "list":
			return runRemoteScheduleList(args[2:], options, output)
		case "get":
			return runRemoteScheduleGet(args[2:], options, output)
		}
	case "probe":
		switch args[1] {
		case "list":
			return runRemoteProbeList(args[2:], options, output)
		case "get":
			return runRemoteProbeGet(args[2:], options, output)
		}
	}
	return usageError()
}

func addRemoteCommandFlags(flags *flag.FlagSet) remoteCommandFlags {
	return remoteCommandFlags{
		server:            flags.String("server", "", "404-probe Server origin"),
		tokenFile:         flags.String("control-token-file", "", "path to the Control API token file"),
		allowInsecureHTTP: flags.Bool("allow-insecure-http", false, "allow plaintext HTTP to a loopback Server"),
		jsonOutput:        flags.Bool("json", false, "write JSON output"),
	}
}

func runRemoteAgentList(args []string, options remoteClientOptions, output io.Writer) error {
	flags := flag.NewFlagSet("remote agent list", flag.ContinueOnError)
	common := addRemoteCommandFlags(flags)
	status := flags.String("status", "", "agent status filter")
	limit := flags.Int("limit", 50, "maximum agents to return")
	cursor := flags.String("cursor", "", "opaque pagination cursor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("remote agent list does not accept positional arguments")
	}
	setFlags := make(map[string]bool)
	flags.Visit(func(current *flag.Flag) { setFlags[current.Name] = true })
	if setFlags["status"] && *status == "" {
		return errors.New("status must not be empty")
	}
	switch *status {
	case "", "online", "offline", "revoked":
	default:
		return errors.New("status must be online, offline, or revoked")
	}
	if *limit < 1 || *limit > 100 {
		return errors.New("limit must be between 1 and 100")
	}
	if len(*cursor) > maxRemoteCursorBytes {
		return errors.New("cursor is too large")
	}
	if setFlags["cursor"] && *cursor == "" {
		return errors.New("cursor must not be empty")
	}
	client, err := newRemoteClient(*common.server, *common.tokenFile, *common.allowInsecureHTTP, options)
	if err != nil {
		return err
	}
	query := make(url.Values)
	if *status != "" {
		query.Set("status", *status)
	}
	query.Set("limit", strconv.Itoa(*limit))
	if *cursor != "" {
		query.Set("cursor", *cursor)
	}
	var collection remoteAgentCollection
	if err := client.get(context.Background(), "/api/v1/control/agents", query, &collection); err != nil {
		return err
	}
	if err := validateRemoteAgentCollection(collection); err != nil {
		return err
	}
	if *common.jsonOutput {
		return writeRemoteJSON(output, collection)
	}
	return writeRemoteAgentTable(output, collection)
}

func runRemoteAgentGet(args []string, options remoteClientOptions, output io.Writer) error {
	if len(args) == 0 || startsFlag(args[0]) || !validCLIHexID(args[0]) {
		return errors.New("agent ID must be 32 lowercase hexadecimal characters")
	}
	agentID := args[0]
	flags := flag.NewFlagSet("remote agent get", flag.ContinueOnError)
	common := addRemoteCommandFlags(flags)
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("remote agent get accepts exactly one agent ID")
	}
	client, err := newRemoteClient(*common.server, *common.tokenFile, *common.allowInsecureHTTP, options)
	if err != nil {
		return err
	}
	var agent remoteAgentView
	if err := client.get(context.Background(), "/api/v1/control/agents/"+agentID, nil, &agent); err != nil {
		return err
	}
	if err := validateRemoteAgentSummary(agent.remoteAgentSummaryView); err != nil {
		return err
	}
	if agent.State != nil && agent.State.CollectedAt <= 0 {
		return errors.New("remote response contains invalid agent state")
	}
	if *common.jsonOutput {
		return writeRemoteJSON(output, agent)
	}
	return writeRemoteAgentDetail(output, agent)
}

func newRemoteClient(serverOrigin, tokenFile string, allowInsecureHTTP bool, options remoteClientOptions) (*remoteClient, error) {
	origin, err := validateRemoteOrigin(serverOrigin, allowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(tokenFile) == "" {
		return nil, errors.New("control-token-file is required")
	}
	token, err := loadControlToken(tokenFile)
	if err != nil {
		return nil, err
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = remoteRequestTimeout
	}
	if timeout < 0 {
		return nil, errors.New("remote request timeout is invalid")
	}
	transport := options.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &remoteClient{origin: origin, token: token, http: &http.Client{
		Transport: transport, Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func validateRemoteOrigin(value string, allowInsecureHTTP bool) (string, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", errors.New("server must be an HTTPS origin URL")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" || parsed.ForceQuery || parsed.RawQuery != "" ||
		parsed.Fragment != "" || strings.Contains(value, "#") || strings.HasSuffix(parsed.Host, ":") {
		return "", errors.New("server must be an origin URL without credentials, path, query, or fragment")
	}
	if parsed.Scheme != "https" {
		if parsed.Scheme != "http" || !allowInsecureHTTP || !remoteLoopbackHost(parsed.Hostname()) {
			return "", errors.New("server must use HTTPS; insecure HTTP is allowed only for loopback with --allow-insecure-http")
		}
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return "", errors.New("server port is invalid")
		}
	}
	parsed.Path = ""
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func remoteLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (client *remoteClient) get(ctx context.Context, path string, query url.Values, destination any) error {
	requestURL := client.origin + path
	if len(query) != 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return errors.New("could not create remote request")
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Accept", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("remote request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return fmt.Errorf("remote server returned redirect status %d", response.StatusCode)
	}
	body, err := readRemoteBody(response.Body)
	if err != nil {
		return err
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("remote response Content-Type must be application/json")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope remoteAPIErrorEnvelope
		if err := decodeRemoteJSON(body, &envelope); err != nil || envelope.Error.Code == "" || envelope.Error.Message == "" {
			return fmt.Errorf("remote API returned HTTP %d", response.StatusCode)
		}
		code := redactRemoteSecret(envelope.Error.Code, client.token)
		message := redactRemoteSecret(envelope.Error.Message, client.token)
		return fmt.Errorf("remote API %s: %s", code, message)
	}
	if err := decodeRemoteJSON(body, destination); err != nil {
		return errors.New("remote response contains invalid JSON")
	}
	return nil
}

func readRemoteBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxRemoteBodyBytes+1))
	if err != nil {
		return nil, errors.New("could not read remote response")
	}
	if len(body) > maxRemoteBodyBytes {
		return nil, errors.New("remote response is too large")
	}
	return body, nil
}

func decodeRemoteJSON(body []byte, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("remote response contains multiple JSON values")
	}
	return nil
}

func redactRemoteSecret(value, token string) string {
	if token == "" {
		return value
	}
	return strings.ReplaceAll(value, token, "[REDACTED]")
}

func validateRemoteAgentCollection(collection remoteAgentCollection) error {
	if collection.Items == nil {
		return errors.New("remote agent collection is missing items")
	}
	if collection.NextCursor != nil && (*collection.NextCursor == "" || len(*collection.NextCursor) > maxRemoteCursorBytes) {
		return errors.New("remote agent collection has an invalid next cursor")
	}
	for _, agent := range collection.Items {
		if err := validateRemoteAgentSummary(agent); err != nil {
			return err
		}
	}
	return nil
}

func validateRemoteAgentSummary(agent remoteAgentSummaryView) error {
	if !validCLIHexID(agent.AgentID) || strings.TrimSpace(agent.Name) == "" || agent.CreatedAt <= 0 ||
		(agent.LastSeen != nil && *agent.LastSeen <= 0) || (agent.Revoked && agent.Online) {
		return errors.New("remote response contains an invalid agent")
	}
	return nil
}

func writeRemoteJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writeRemoteAgentTable(output io.Writer, collection remoteAgentCollection) error {
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tSTATUS\tLAST SEEN\tCREATED"); err != nil {
		return err
	}
	for _, agent := range collection.Items {
		lastSeen := "-"
		if agent.LastSeen != nil {
			lastSeen = formatCLIUnixMilli(*agent.LastSeen)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", agent.AgentID, remoteHumanText(agent.Name),
			remoteAgentStatus(agent), lastSeen, formatCLIUnixMilli(agent.CreatedAt)); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if collection.NextCursor != nil {
		_, err := fmt.Fprintf(output, "next_cursor: %s\n", remoteHumanText(*collection.NextCursor))
		return err
	}
	return nil
}

func remoteAgentStatus(agent remoteAgentSummaryView) string {
	if agent.Revoked {
		return "revoked"
	}
	if agent.Online {
		return "online"
	}
	return "offline"
}

func writeRemoteAgentDetail(output io.Writer, agent remoteAgentView) error {
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	write := func(label string, value any) error {
		_, err := fmt.Fprintf(w, "%s\t%v\n", label, value)
		return err
	}
	fields := [][2]any{
		{"AGENT ID", agent.AgentID}, {"NAME", remoteHumanText(agent.Name)}, {"STATUS", remoteAgentStatus(agent.remoteAgentSummaryView)},
		{"CREATED", formatCLIUnixMilli(agent.CreatedAt)},
	}
	if agent.LastSeen != nil {
		fields = append(fields, [2]any{"LAST SEEN", formatCLIUnixMilli(*agent.LastSeen)})
	}
	if agent.State != nil {
		state := agent.State
		fields = append(fields,
			[2]any{"HOSTNAME", remoteHumanText(state.Hostname)}, [2]any{"OS", remoteHumanText(state.OS)}, [2]any{"ARCH", remoteHumanText(state.Arch)},
			[2]any{"UPTIME", fmt.Sprintf("%ds", state.Uptime)}, [2]any{"CPU", fmt.Sprintf("%g%%", state.CPUPercent)},
			[2]any{"LOAD", fmt.Sprintf("%g %g %g", state.Load1, state.Load5, state.Load15)},
			[2]any{"RAM", fmt.Sprintf("%d/%d (%g%%)", state.RAMUsed, state.RAMTotal, state.RAMPercent)},
			[2]any{"SWAP", fmt.Sprintf("%d/%d (%g%%)", state.SwapUsed, state.SwapTotal, state.SwapPercent)},
			[2]any{"DISK", fmt.Sprintf("%d/%d (%g%%)", state.DiskUsed, state.DiskTotal, state.DiskPercent)},
			[2]any{"RX RATE", fmt.Sprintf("%g B/s", state.RXRate)}, [2]any{"TX RATE", fmt.Sprintf("%g B/s", state.TXRate)},
			[2]any{"RX TOTAL", state.RXTotal}, [2]any{"TX TOTAL", state.TXTotal},
			[2]any{"COLLECTED", formatCLIUnixMilli(state.CollectedAt)},
		)
	}
	for _, field := range fields {
		if err := write(field[0].(string), field[1]); err != nil {
			return err
		}
	}
	return w.Flush()
}

func remoteHumanText(value string) string {
	return strings.Map(func(char rune) rune {
		if strconv.IsPrint(char) {
			return char
		}
		return '\ufffd'
	}, value)
}
