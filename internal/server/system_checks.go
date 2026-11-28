package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const systemCheckFreshness = 3 * time.Minute

type systemAttention struct {
	Certificate    string                `json:"certificate,omitempty"`
	Service        bool                  `json:"service,omitempty"`
	ServiceRestart string                `json:"service_restart,omitempty"`
	Time           bool                  `json:"time,omitempty"`
	DNS            bool                  `json:"dns,omitempty"`
	Items          []systemAttentionItem `json:"items,omitempty"`
	Ignored        []systemAttentionItem `json:"ignored,omitempty"`
}

type systemAttentionItem struct {
	protocol.SystemCheckKey
	Level  string `json:"level"`
	Detail string `json:"detail"`
}

func checkAttention(checks *protocol.SystemChecks, now time.Time, ignores storage.SystemCheckIgnores) systemAttention {
	var attention systemAttention
	if checks == nil || !checks.Supported {
		return attention
	}
	indices := make(map[protocol.SystemCheckKey]int)
	add := func(kind, item, level, detail string) {
		key := protocol.SystemCheckKey{Kind: kind, Item: item}
		list := &attention.Items
		if ignores[key] {
			list = &attention.Ignored
		}
		if i, exists := indices[key]; exists {
			if (*list)[i].Level == "warning" && level == "danger" {
				(*list)[i].Level, (*list)[i].Detail = level, detail
			}
			return
		}
		indices[key] = len(*list)
		*list = append(*list, systemAttentionItem{SystemCheckKey: key, Level: level, Detail: detail})
	}
	for _, cert := range checks.Certificates {
		expires := time.UnixMilli(cert.ExpiresAt).UTC()
		delta := expires.Sub(now)
		switch {
		case delta <= 0:
			add(protocol.SystemCheckCertificate, cert.Path, "danger", fmt.Sprintf("%s 已于 %s 到期", cert.Path, expires.Format(time.DateOnly)))
		case delta <= 7*24*time.Hour:
			add(protocol.SystemCheckCertificate, cert.Path, "warning", fmt.Sprintf("%s 将于 %s 到期", cert.Path, expires.Format(time.DateOnly)))
		}
	}
	for _, unit := range checks.FailedUnits {
		add(protocol.SystemCheckService, unit, "danger", unit+" 处于 failed 状态")
	}
	for _, service := range checks.Services {
		if service.State != "active" {
			unit := watchedServiceUnit(service.Name)
			add(protocol.SystemCheckService, unit, "danger", fmt.Sprintf("%s 处于 %s 状态", unit, service.State))
		}
	}
	for _, service := range checks.ServiceRestarts {
		level := "warning"
		if service.Count >= 3 {
			level = "danger"
		}
		add(protocol.SystemCheckRestart, service.Unit, level, fmt.Sprintf("近 10 分钟重启 %d 次", service.Count))
	}
	var clock []string
	if checks.NTPStatus == protocol.CheckOK && !checks.NTPSynchronized {
		clock = append(clock, "NTP 未同步")
	}
	if checks.TimeOffsetMS > 5000 || checks.TimeOffsetMS < -5000 {
		clock = append(clock, fmt.Sprintf("时间偏差 %+.1f 秒", float64(checks.TimeOffsetMS)/1000))
	}
	if len(clock) > 0 {
		add(protocol.SystemCheckTime, protocol.SystemCheckTime, "warning", strings.Join(clock, "，"))
	}
	if dns := checks.LocalDNS; dns != nil {
		for _, server := range dns.Servers {
			if server.ConsecutiveFailures < 2 || !protocol.IsLocalDNSFailure(server.Result) {
				continue
			}
			reason := "服务器错误"
			switch server.Result {
			case protocol.DNSTimeout:
				reason = "超时"
			case protocol.DNSRefused:
				reason = "拒绝连接"
			}
			add(protocol.SystemCheckDNS, server.Address, "danger", fmt.Sprintf("%s 解析 %s 失败：%s", server.Address, dns.Query, reason))
		}
	}
	for _, item := range attention.Items {
		switch item.Kind {
		case protocol.SystemCheckCertificate:
			attention.Certificate = maxAttentionLevel(attention.Certificate, item.Level)
		case protocol.SystemCheckService:
			attention.Service = true
		case protocol.SystemCheckRestart:
			attention.ServiceRestart = maxAttentionLevel(attention.ServiceRestart, item.Level)
		case protocol.SystemCheckTime:
			attention.Time = true
		case protocol.SystemCheckDNS:
			attention.DNS = true
		}
	}
	return attention
}

func watchedServiceUnit(name string) string {
	if strings.HasSuffix(name, ".service") {
		return name
	}
	return name + ".service"
}

func maxAttentionLevel(current, level string) string {
	if current == "danger" {
		return current
	}
	return level
}

func (a *App) currentSystemChecks(record storage.AgentSnapshot, now time.Time) *protocol.SystemChecks {
	if !record.Online || record.State == nil {
		return nil
	}
	a.mu.RLock()
	state := a.states[record.Agent.ID]
	a.mu.RUnlock()
	if state.Epoch != record.State.Epoch || now.Sub(time.UnixMilli(state.SystemChecksAt)) > systemCheckFreshness {
		return nil
	}
	return state.SystemChecks
}

func (a *App) handlePutSystemCheckSettings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent_id")
	if !validWebAgentID(id) || r.URL.EscapedPath() != webAgentPathPrefix+id+"/system-checks" || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid system check settings request")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, 415, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	raw, err := readBoundedBody(w, r, 24<<10)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var settings protocol.SystemCheckSettings
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil || ensureJSONEOF(decoder) != nil {
		writeJobError(w, 400, "invalid_request", "invalid system check settings")
		return
	}
	if err := settings.Normalize(); err != nil {
		writeJobError(w, 400, "invalid_request", err.Error())
		return
	}
	if err := a.store.SaveSystemCheckSettings(r.Context(), id, settings); err != nil {
		a.organizationError(w, err)
		return
	}
	a.publishDashboardChange(id, "system-check-settings", false)
	writeJSON(w, 200, settings)
}

func (a *App) handleSystemCheckIgnore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent_id")
	if !validWebAgentID(id) || r.URL.EscapedPath() != webAgentPathPrefix+id+"/system-check-ignores" || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid system check ignore request")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, 415, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	raw, err := readBoundedBody(w, r, 4<<10)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var key protocol.SystemCheckKey
	if err := decodeStrictJSON(raw, &key); err != nil || !key.Valid() {
		writeJobError(w, 400, "invalid_request", "invalid system check kind or item")
		return
	}
	ignore := r.Method == http.MethodPut
	changed, err := a.store.ChangeSystemCheckIgnore(r.Context(), id, key, ignore, a.now())
	switch {
	case errors.Is(err, storage.ErrSystemCheckIgnoreLimit):
		writeJobError(w, 400, "limit_reached", "system check ignore limit reached")
		return
	case errors.Is(err, storage.ErrAgentNotFound):
		writeJobError(w, 404, "agent_not_found", "agent not found")
		return
	case err != nil:
		a.logger.Error("change system check ignore", "agent_id", id, "error", err)
		writeJobError(w, 500, "internal_error", "could not change system check ignore")
		return
	}
	if changed {
		a.publishDashboardChange(id, "system-check-ignores", false)
	}
	writeJSON(w, 200, struct {
		protocol.SystemCheckKey
		Ignored bool `json:"ignored"`
	}{key, ignore})
}

type debianStable struct {
	mu    sync.Mutex
	next  time.Time
	major int
}

// One asynchronous lookup per Server each day. The request contains no Agent
// information. Keep the last good value during mirror outages.
func (d *debianStable) observe(ctx context.Context, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if now.Before(d.next) {
		return
	}
	d.next = now.Add(24 * time.Hour)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://deb.debian.org/debian/dists/stable/Release", nil)
		if err != nil {
			return
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return
		}
		scanner := bufio.NewScanner(response.Body)
		for i := 0; i < 32 && scanner.Scan(); i++ {
			value, ok := strings.CutPrefix(scanner.Text(), "Version: ")
			if !ok {
				continue
			}
			major, _, _ := strings.Cut(strings.TrimSpace(value), ".")
			n, err := strconv.Atoi(major)
			if err == nil && n > 0 && n < 100 {
				d.mu.Lock()
				d.major = n
				d.mu.Unlock()
			}
			return
		}
	}()
}

func (d *debianStable) current() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.major
}
