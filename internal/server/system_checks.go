package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
	Certificate    string `json:"certificate,omitempty"`
	Service        bool   `json:"service,omitempty"`
	ServiceRestart string `json:"service_restart,omitempty"`
	Time           bool   `json:"time,omitempty"`
}

func checkAttention(checks *protocol.SystemChecks, now time.Time) systemAttention {
	var attention systemAttention
	if checks == nil || !checks.Supported {
		return attention
	}
	for _, cert := range checks.Certificates {
		delta := time.UnixMilli(cert.ExpiresAt).Sub(now)
		if delta <= 0 {
			attention.Certificate = "danger"
			break
		}
		if delta <= 7*24*time.Hour {
			attention.Certificate = "warning"
		}
	}
	attention.Service = len(checks.FailedUnits) != 0
	for _, service := range checks.ServiceRestarts {
		if service.Count >= 3 {
			attention.ServiceRestart = "danger"
			break
		}
		attention.ServiceRestart = "warning"
	}
	for _, service := range checks.Services {
		attention.Service = attention.Service || service.State != "active"
	}
	attention.Time = (checks.NTPStatus == protocol.CheckOK && !checks.NTPSynchronized) || checks.TimeOffsetMS > 5000 || checks.TimeOffsetMS < -5000
	return attention
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
