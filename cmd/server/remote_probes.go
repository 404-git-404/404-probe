package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/netip"
	"net/url"
	"strconv"
	"text/tabwriter"

	"404-probe/internal/protocol"
)

type remoteJobLeaseView struct {
	LeasedAt  int64 `json:"leased_at"`
	ExpiresAt int64 `json:"expires_at"`
}

type remoteJobResultSummaryView struct {
	Success       bool    `json:"success"`
	DurationMS    float64 `json:"duration_ms"`
	ErrorCategory *string `json:"error_category"`
	FinishedAt    int64   `json:"finished_at"`
}

type remoteJobSummaryView struct {
	JobID         string                      `json:"job_id"`
	ScheduleID    *string                     `json:"schedule_id"`
	AgentID       string                      `json:"agent_id"`
	ProbeType     protocol.ProbeType          `json:"probe_type"`
	TimeoutMS     int                         `json:"timeout_ms"`
	CreatedAt     int64                       `json:"created_at"`
	ScheduledFor  int64                       `json:"scheduled_for"`
	NotBefore     int64                       `json:"not_before"`
	ExpiresAt     int64                       `json:"expires_at"`
	Status        string                      `json:"status"`
	Attempt       int64                       `json:"attempt"`
	Lease         *remoteJobLeaseView         `json:"lease"`
	FinishedAt    *int64                      `json:"finished_at"`
	ResultSummary *remoteJobResultSummaryView `json:"result_summary"`
}

type remoteJobCollection struct {
	Items      []remoteJobSummaryView `json:"items"`
	NextCursor *string                `json:"next_cursor"`
}

type remoteJobResultView struct {
	ReceivedAt    int64           `json:"received_at"`
	StartedAt     int64           `json:"started_at"`
	FinishedAt    int64           `json:"finished_at"`
	DurationMS    float64         `json:"duration_ms"`
	Success       bool            `json:"success"`
	ResolvedIP    *string         `json:"resolved_ip"`
	ErrorCategory *string         `json:"error_category"`
	ErrorMessage  *string         `json:"error_message"`
	Measurement   json.RawMessage `json:"measurement"`
}

type remoteJobView struct {
	JobID      string               `json:"job_id"`
	AgentID    string               `json:"agent_id"`
	ProbeType  protocol.ProbeType   `json:"probe_type"`
	Config     json.RawMessage      `json:"config"`
	TimeoutMS  int                  `json:"timeout_ms"`
	CreatedAt  int64                `json:"created_at"`
	NotBefore  int64                `json:"not_before"`
	ExpiresAt  int64                `json:"expires_at"`
	Status     string               `json:"status"`
	Attempt    int64                `json:"attempt"`
	Lease      *remoteJobLeaseView  `json:"lease"`
	FinishedAt *int64               `json:"finished_at"`
	Result     *remoteJobResultView `json:"result"`
}

func runRemoteProbeList(args []string, options remoteClientOptions, output io.Writer) error {
	flags := flag.NewFlagSet("remote probe list", flag.ContinueOnError)
	common := addRemoteCommandFlags(flags)
	agentID := flags.String("agent-id", "", "agent ID filter")
	scheduleID := flags.String("schedule-id", "", "schedule ID filter")
	probeType := flags.String("probe-type", "", "probe type filter")
	status := flags.String("status", "", "job status filter")
	success := flags.String("success", "", "result success filter")
	createdAfter := flags.Int64("created-after", 0, "minimum creation time in Unix milliseconds")
	createdBefore := flags.Int64("created-before", 0, "maximum creation time in Unix milliseconds")
	finishedAfter := flags.Int64("finished-after", 0, "minimum finish time in Unix milliseconds")
	finishedBefore := flags.Int64("finished-before", 0, "maximum finish time in Unix milliseconds")
	limit := flags.Int("limit", 50, "maximum probes to return")
	cursor := flags.String("cursor", "", "opaque pagination cursor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("remote probe list does not accept positional arguments")
	}
	set := make(map[string]bool)
	flags.Visit(func(current *flag.Flag) { set[current.Name] = true })
	for name, value := range map[string]string{"agent-id": *agentID, "schedule-id": *scheduleID, "probe-type": *probeType, "status": *status, "success": *success, "cursor": *cursor} {
		if set[name] && value == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
	}
	if *agentID != "" && !validCLIHexID(*agentID) {
		return errors.New("agent-id must be 32 lowercase hexadecimal characters")
	}
	if *scheduleID != "" && !validCLIHexID(*scheduleID) {
		return errors.New("schedule-id must be 32 lowercase hexadecimal characters")
	}
	if *probeType != "" && protocol.ProbeType(*probeType).Validate() != nil {
		return errors.New("probe-type must be http, tcp_connect, or icmp_ping")
	}
	if *status != "" && !validRemoteJobStatus(*status) {
		return errors.New("status must be queued, leased, finished, or expired")
	}
	if *success != "" && *success != "true" && *success != "false" {
		return errors.New("success must be true or false")
	}
	for name, value := range map[string]int64{"created-after": *createdAfter, "created-before": *createdBefore, "finished-after": *finishedAfter, "finished-before": *finishedBefore} {
		if set[name] && value <= 0 {
			return fmt.Errorf("%s must be a positive Unix millisecond timestamp", name)
		}
	}
	if (*createdAfter != 0 && *createdBefore != 0 && *createdAfter >= *createdBefore) ||
		(*finishedAfter != 0 && *finishedBefore != 0 && *finishedAfter >= *finishedBefore) {
		return errors.New("after timestamps must be less than before timestamps")
	}
	if (*success != "" || *finishedAfter != 0 || *finishedBefore != 0) && *status != "" && *status != "finished" {
		return errors.New("result filters require status finished or no status filter")
	}
	if *limit < 1 || *limit > 100 || len(*cursor) > maxRemoteCursorBytes {
		return errors.New("limit must be 1..100 and cursor must not exceed 2048 bytes")
	}
	client, err := newRemoteClient(*common.server, *common.tokenFile, *common.allowInsecureHTTP, options)
	if err != nil {
		return err
	}
	query := make(url.Values)
	values := map[string]string{"agent_id": *agentID, "schedule_id": *scheduleID, "probe_type": *probeType, "status": *status,
		"success": *success, "created_after": remoteOptionalInt(*createdAfter), "created_before": remoteOptionalInt(*createdBefore),
		"finished_after": remoteOptionalInt(*finishedAfter), "finished_before": remoteOptionalInt(*finishedBefore), "cursor": *cursor}
	for name, value := range values {
		if value != "" {
			query.Set(name, value)
		}
	}
	query.Set("limit", strconv.Itoa(*limit))
	var collection remoteJobCollection
	if err := client.get(context.Background(), "/api/v1/control/jobs", query, &collection); err != nil {
		return err
	}
	if err := validateRemoteJobCollection(collection); err != nil {
		return err
	}
	if *common.jsonOutput {
		return writeRemoteJSON(output, collection)
	}
	return writeRemoteJobTable(output, collection)
}

func runRemoteProbeGet(args []string, options remoteClientOptions, output io.Writer) error {
	if len(args) == 0 || startsFlag(args[0]) || !validCLIJobID(args[0]) {
		return errors.New("probe ID must be 32 or 64 lowercase hexadecimal characters")
	}
	jobID := args[0]
	flags := flag.NewFlagSet("remote probe get", flag.ContinueOnError)
	common := addRemoteCommandFlags(flags)
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("remote probe get accepts exactly one probe ID")
	}
	client, err := newRemoteClient(*common.server, *common.tokenFile, *common.allowInsecureHTTP, options)
	if err != nil {
		return err
	}
	var job remoteJobView
	if err := client.get(context.Background(), "/api/v1/control/jobs/"+jobID, nil, &job); err != nil {
		return err
	}
	if err := normalizeRemoteJob(&job); err != nil {
		return err
	}
	if *common.jsonOutput {
		return writeRemoteJSON(output, job)
	}
	return writeRemoteJobDetail(output, job)
}

func remoteOptionalInt(value int64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func validRemoteJobStatus(status string) bool {
	return status == "queued" || status == "leased" || status == "finished" || status == "expired"
}

func validateRemoteJobCollection(collection remoteJobCollection) error {
	if collection.Items == nil {
		return errors.New("remote probe collection is missing items")
	}
	if collection.NextCursor != nil && (*collection.NextCursor == "" || len(*collection.NextCursor) > maxRemoteCursorBytes) {
		return errors.New("remote probe collection has an invalid next cursor")
	}
	for _, job := range collection.Items {
		if err := validateRemoteJobSummary(job); err != nil {
			return err
		}
	}
	return nil
}

func validateRemoteJobSummary(job remoteJobSummaryView) error {
	if !validCLIJobID(job.JobID) || !validCLIHexID(job.AgentID) || job.ProbeType.Validate() != nil ||
		job.TimeoutMS < protocol.MinProbeTimeoutMS || job.TimeoutMS > protocol.MaxProbeTimeoutMS || job.CreatedAt <= 0 ||
		job.NotBefore < job.CreatedAt || job.ExpiresAt <= job.NotBefore || job.Attempt < 0 || !validRemoteJobStatus(job.Status) {
		return errors.New("remote response contains an invalid probe")
	}
	if job.ScheduleID != nil && !validCLIHexID(*job.ScheduleID) {
		return errors.New("remote response contains an invalid probe")
	}
	switch job.Status {
	case "queued", "expired":
		if job.Lease != nil || job.FinishedAt != nil || job.ResultSummary != nil {
			return errors.New("remote response contains an invalid probe state")
		}
	case "leased":
		if job.Attempt < 1 || job.Lease == nil || job.Lease.LeasedAt <= 0 || job.Lease.ExpiresAt <= job.Lease.LeasedAt || job.FinishedAt != nil || job.ResultSummary != nil {
			return errors.New("remote response contains an invalid probe state")
		}
	case "finished":
		if job.Attempt < 1 || job.Lease != nil || job.FinishedAt == nil || *job.FinishedAt <= 0 || job.ResultSummary == nil ||
			job.ResultSummary.FinishedAt <= 0 || math.IsNaN(job.ResultSummary.DurationMS) || math.IsInf(job.ResultSummary.DurationMS, 0) ||
			job.ResultSummary.DurationMS < 0 || (job.ResultSummary.Success && job.ResultSummary.ErrorCategory != nil) ||
			(!job.ResultSummary.Success && (job.ResultSummary.ErrorCategory == nil || *job.ResultSummary.ErrorCategory == "")) {
			return errors.New("remote response contains an invalid probe state")
		}
	}
	return nil
}

func normalizeRemoteJob(job *remoteJobView) error {
	base := remoteJobSummaryView{JobID: job.JobID, AgentID: job.AgentID, ProbeType: job.ProbeType, TimeoutMS: job.TimeoutMS,
		CreatedAt: job.CreatedAt, NotBefore: job.NotBefore, ExpiresAt: job.ExpiresAt, Status: job.Status, Attempt: job.Attempt,
		Lease: job.Lease, FinishedAt: job.FinishedAt}
	if job.Result != nil {
		base.ResultSummary = &remoteJobResultSummaryView{Success: job.Result.Success, DurationMS: job.Result.DurationMS, ErrorCategory: job.Result.ErrorCategory, FinishedAt: job.Result.FinishedAt}
	}
	if err := validateRemoteJobSummary(base); err != nil {
		return err
	}
	config, err := protocol.DecodeProbeConfig(job.ProbeType, job.Config)
	if err != nil {
		return errors.New("remote response contains an invalid probe config")
	}
	job.Config, err = protocol.MarshalProbeConfig(job.ProbeType, config)
	if err != nil {
		return errors.New("remote response contains an invalid probe config")
	}
	if job.Status == "finished" {
		if job.Result == nil || job.Result.ReceivedAt != *job.FinishedAt || job.Result.StartedAt <= 0 || job.Result.FinishedAt < job.Result.StartedAt {
			return errors.New("remote response contains an invalid probe result")
		}
		if job.Result.ResolvedIP != nil {
			if _, err := netip.ParseAddr(*job.Result.ResolvedIP); err != nil {
				return errors.New("remote response contains an invalid probe result")
			}
		}
		if job.Result.Success && (job.Result.ErrorCategory != nil || job.Result.ErrorMessage != nil) || !job.Result.Success && job.Result.ErrorCategory == nil {
			return errors.New("remote response contains an invalid probe result")
		}
		measurement, err := protocol.DecodeProbeResult(job.ProbeType, job.Result.Measurement)
		if err != nil {
			return errors.New("remote response contains an invalid probe measurement")
		}
		job.Result.Measurement, err = protocol.MarshalProbeResult(job.ProbeType, measurement)
		if err != nil {
			return errors.New("remote response contains an invalid probe measurement")
		}
	} else if job.Result != nil {
		return errors.New("remote response contains an invalid probe result")
	}
	return nil
}

func writeRemoteJobTable(output io.Writer, collection remoteJobCollection) error {
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tAGENT ID\tSCHEDULE ID\tTYPE\tSTATUS\tSUCCESS\tDURATION\tCREATED\tFINISHED"); err != nil {
		return err
	}
	for _, job := range collection.Items {
		schedule, success, duration, finished := "-", "-", "-", "-"
		if job.ScheduleID != nil {
			schedule = *job.ScheduleID
		}
		if job.ResultSummary != nil {
			success = strconv.FormatBool(job.ResultSummary.Success)
			duration = fmt.Sprintf("%gms", job.ResultSummary.DurationMS)
		}
		if job.FinishedAt != nil {
			finished = formatCLIUnixMilli(*job.FinishedAt)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", job.JobID, job.AgentID, schedule, job.ProbeType, job.Status, success, duration, formatCLIUnixMilli(job.CreatedAt), finished); err != nil {
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

func writeRemoteJobDetail(output io.Writer, job remoteJobView) error {
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fields := [][2]string{{"PROBE ID", job.JobID}, {"AGENT ID", job.AgentID}, {"TYPE", string(job.ProbeType)}, {"CONFIG", remoteHumanText(string(job.Config))},
		{"STATUS", job.Status}, {"ATTEMPT", strconv.FormatInt(job.Attempt, 10)}, {"TIMEOUT", fmt.Sprintf("%dms", job.TimeoutMS)}, {"CREATED", formatCLIUnixMilli(job.CreatedAt)}, {"NOT BEFORE", formatCLIUnixMilli(job.NotBefore)}, {"EXPIRES", formatCLIUnixMilli(job.ExpiresAt)}}
	if job.Lease != nil {
		fields = append(fields, [2]string{"LEASED", formatCLIUnixMilli(job.Lease.LeasedAt)}, [2]string{"LEASE EXPIRES", formatCLIUnixMilli(job.Lease.ExpiresAt)})
	}
	if job.FinishedAt != nil {
		fields = append(fields, [2]string{"FINISHED", formatCLIUnixMilli(*job.FinishedAt)})
	}
	if job.Result != nil {
		result, _ := json.Marshal(job.Result)
		fields = append(fields, [2]string{"RESULT", remoteHumanText(string(result))})
	}
	for _, field := range fields {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", field[0], field[1]); err != nil {
			return err
		}
	}
	return w.Flush()
}
