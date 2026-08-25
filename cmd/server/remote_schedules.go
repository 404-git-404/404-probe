package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"

	"404-probe/internal/protocol"
)

const (
	minRemoteScheduleInterval = 30
	maxRemoteScheduleInterval = 604800
)

type remoteScheduleView struct {
	ScheduleID      string             `json:"schedule_id"`
	AgentID         string             `json:"agent_id"`
	Name            string             `json:"name"`
	ProbeType       protocol.ProbeType `json:"probe_type"`
	Config          json.RawMessage    `json:"config"`
	TimeoutMS       int                `json:"timeout_ms"`
	IntervalSeconds int                `json:"interval_seconds"`
	Enabled         bool               `json:"enabled"`
	NextRunAt       int64              `json:"next_run_at"`
	CreatedAt       int64              `json:"created_at"`
	UpdatedAt       int64              `json:"updated_at"`
}

type remoteScheduleCollection struct {
	Items      []remoteScheduleView `json:"items"`
	NextCursor *string              `json:"next_cursor"`
}

func runRemoteScheduleList(args []string, options remoteClientOptions, output io.Writer) error {
	flags := flag.NewFlagSet("remote schedule list", flag.ContinueOnError)
	common := addRemoteCommandFlags(flags)
	agentID := flags.String("agent-id", "", "agent ID filter")
	enabled := flags.String("enabled", "", "enabled state filter")
	probeType := flags.String("probe-type", "", "probe type filter")
	limit := flags.Int("limit", 50, "maximum schedules to return")
	cursor := flags.String("cursor", "", "opaque pagination cursor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("remote schedule list does not accept positional arguments")
	}
	setFlags := make(map[string]bool)
	flags.Visit(func(current *flag.Flag) { setFlags[current.Name] = true })
	if setFlags["agent-id"] && *agentID == "" {
		return errors.New("agent-id must not be empty")
	}
	if *agentID != "" && !validCLIHexID(*agentID) {
		return errors.New("agent-id must be 32 lowercase hexadecimal characters")
	}
	if setFlags["enabled"] && *enabled == "" {
		return errors.New("enabled must not be empty")
	}
	if *enabled != "" && *enabled != "true" && *enabled != "false" {
		return errors.New("enabled must be true or false")
	}
	if setFlags["probe-type"] && *probeType == "" {
		return errors.New("probe-type must not be empty")
	}
	if *probeType != "" {
		if err := protocol.ProbeType(*probeType).Validate(); err != nil {
			return errors.New("probe-type must be http, tcp_connect, or icmp_ping")
		}
	}
	if *limit < 1 || *limit > 100 {
		return errors.New("limit must be between 1 and 100")
	}
	if setFlags["cursor"] && *cursor == "" {
		return errors.New("cursor must not be empty")
	}
	if len(*cursor) > maxRemoteCursorBytes {
		return errors.New("cursor is too large")
	}
	client, err := newRemoteClient(*common.server, *common.tokenFile, *common.allowInsecureHTTP, options)
	if err != nil {
		return err
	}
	query := make(url.Values)
	if *agentID != "" {
		query.Set("agent_id", *agentID)
	}
	if *enabled != "" {
		query.Set("enabled", *enabled)
	}
	if *probeType != "" {
		query.Set("probe_type", *probeType)
	}
	query.Set("limit", strconv.Itoa(*limit))
	if *cursor != "" {
		query.Set("cursor", *cursor)
	}
	var collection remoteScheduleCollection
	if err := client.get(context.Background(), "/api/v1/control/schedules", query, &collection); err != nil {
		return err
	}
	if err := normalizeRemoteScheduleCollection(&collection); err != nil {
		return err
	}
	if *common.jsonOutput {
		return writeRemoteJSON(output, collection)
	}
	return writeRemoteScheduleTable(output, collection)
}

func runRemoteScheduleGet(args []string, options remoteClientOptions, output io.Writer) error {
	if len(args) == 0 || startsFlag(args[0]) || !validCLIHexID(args[0]) {
		return errors.New("schedule ID must be 32 lowercase hexadecimal characters")
	}
	scheduleID := args[0]
	flags := flag.NewFlagSet("remote schedule get", flag.ContinueOnError)
	common := addRemoteCommandFlags(flags)
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("remote schedule get accepts exactly one schedule ID")
	}
	client, err := newRemoteClient(*common.server, *common.tokenFile, *common.allowInsecureHTTP, options)
	if err != nil {
		return err
	}
	var schedule remoteScheduleView
	if err := client.get(context.Background(), "/api/v1/control/schedules/"+scheduleID, nil, &schedule); err != nil {
		return err
	}
	if err := normalizeRemoteSchedule(&schedule); err != nil {
		return err
	}
	if *common.jsonOutput {
		return writeRemoteJSON(output, schedule)
	}
	return writeRemoteScheduleDetail(output, schedule)
}

func normalizeRemoteScheduleCollection(collection *remoteScheduleCollection) error {
	if collection.Items == nil {
		return errors.New("remote schedule collection is missing items")
	}
	if collection.NextCursor != nil && (*collection.NextCursor == "" || len(*collection.NextCursor) > maxRemoteCursorBytes) {
		return errors.New("remote schedule collection has an invalid next cursor")
	}
	for index := range collection.Items {
		if err := normalizeRemoteSchedule(&collection.Items[index]); err != nil {
			return err
		}
	}
	return nil
}

func normalizeRemoteSchedule(schedule *remoteScheduleView) error {
	if !validCLIHexID(schedule.ScheduleID) || !validCLIHexID(schedule.AgentID) || strings.TrimSpace(schedule.Name) == "" ||
		schedule.ProbeType.Validate() != nil || schedule.TimeoutMS < protocol.MinProbeTimeoutMS ||
		schedule.TimeoutMS > protocol.MaxProbeTimeoutMS || schedule.IntervalSeconds < minRemoteScheduleInterval ||
		schedule.IntervalSeconds > maxRemoteScheduleInterval || schedule.CreatedAt <= 0 || schedule.NextRunAt < schedule.CreatedAt ||
		schedule.UpdatedAt < schedule.CreatedAt {
		return errors.New("remote response contains an invalid schedule")
	}
	config, err := protocol.DecodeProbeConfig(schedule.ProbeType, schedule.Config)
	if err != nil {
		return errors.New("remote response contains an invalid schedule config")
	}
	schedule.Config, err = protocol.MarshalProbeConfig(schedule.ProbeType, config)
	if err != nil {
		return errors.New("remote response contains an invalid schedule config")
	}
	return nil
}

func writeRemoteScheduleTable(output io.Writer, collection remoteScheduleCollection) error {
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tAGENT ID\tTYPE\tENABLED\tNEXT RUN"); err != nil {
		return err
	}
	for _, schedule := range collection.Items {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%t\t%s\n", schedule.ScheduleID, remoteHumanText(schedule.Name),
			schedule.AgentID, schedule.ProbeType, schedule.Enabled, formatCLIUnixMilli(schedule.NextRunAt)); err != nil {
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

func writeRemoteScheduleDetail(output io.Writer, schedule remoteScheduleView) error {
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fields := [][2]string{
		{"SCHEDULE ID", schedule.ScheduleID}, {"AGENT ID", schedule.AgentID}, {"NAME", remoteHumanText(schedule.Name)},
		{"TYPE", string(schedule.ProbeType)}, {"CONFIG", remoteHumanText(string(schedule.Config))},
		{"TIMEOUT", fmt.Sprintf("%dms", schedule.TimeoutMS)}, {"INTERVAL", fmt.Sprintf("%ds", schedule.IntervalSeconds)},
		{"ENABLED", strconv.FormatBool(schedule.Enabled)}, {"NEXT RUN", formatCLIUnixMilli(schedule.NextRunAt)},
		{"CREATED", formatCLIUnixMilli(schedule.CreatedAt)}, {"UPDATED", formatCLIUnixMilli(schedule.UpdatedAt)},
	}
	for _, field := range fields {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", field[0], field[1]); err != nil {
			return err
		}
	}
	return w.Flush()
}
