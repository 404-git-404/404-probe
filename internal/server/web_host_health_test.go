package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func TestHostHealthReportNegotiationAndDetail(t *testing.T) {
	app, store, id, token := testApp(t)
	defer store.Close()
	legacy := reportFor(id, 1)
	legacy.OS = "debian 12"
	response := postReport(t, app, token, legacy)
	var reply protocol.ReportResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &reply) != nil || !reply.Accepted || !reply.Capabilities.HostHealthReport {
		t.Fatalf("legacy report rejected: %d %s", response.Code, response.Body)
	}
	detail, err := app.webAgentDetail(context.Background(), id)
	if err != nil || detail.State == nil || detail.State.HostHealth != nil {
		t.Fatalf("legacy detail: %+v %v", detail.State, err)
	}
	modern := reportFor(id, 2)
	modern.OS = ""
	modern.HostHealth = &protocol.HostHealth{OSReadFailed: true, Disk: protocol.DiskHealth{Status: protocol.DiskOK, CheckedAt: modern.CollectedAt}}
	if response = postReport(t, app, token, modern); response.Code != http.StatusOK {
		t.Fatalf("modern report rejected: %d %s", response.Code, response.Body)
	}
	detail, err = app.webAgentDetail(context.Background(), id)
	if err != nil || detail.State == nil || detail.State.OS != "debian 12" || detail.State.HostHealth == nil || !detail.State.HostHealth.OSReadFailed || detail.State.HostHealth.Disk.Status != protocol.DiskOK {
		t.Fatalf("modern detail: %+v %v", detail.State, err)
	}
}

func TestWebHostHealthNeverShowsStaleProbeAsNormal(t *testing.T) {
	now := time.Now()
	for _, stale := range []bool{false, true} {
		for _, age := range []time.Duration{time.Minute, 3 * time.Minute} {
			state := &storage.HostHealthState{Current: &protocol.HostHealth{Disk: protocol.DiskHealth{
				Status: protocol.DiskOK, CheckedAt: now.Add(-age).UnixMilli(),
			}}}
			view := newWebHostHealth(state, stale, now.UnixMilli())
			fresh := !stale && age < 2*time.Minute
			if (view.Disk.Status == protocol.DiskOK) != fresh || view.Stale == fresh {
				t.Fatalf("stale=%v age=%s got %+v", stale, age, view)
			}
		}
	}
}

func TestStaleReportAlsoHidesNormalDiskStatusInSSE(t *testing.T) {
	now := time.Now()
	state := storage.State{LastSeen: now.UnixMilli(), CollectedAt: now.Add(-time.Minute).UnixMilli(),
		HostHealth: &storage.HostHealthState{Current: &protocol.HostHealth{Disk: protocol.DiskHealth{Status: protocol.DiskOK, CheckedAt: now.UnixMilli()}}}}
	view := newWebAgentEventView(state, 30*time.Second)
	if !view.State.HostHealth.Stale || view.State.HostHealth.Disk.Status == protocol.DiskOK {
		t.Fatalf("stale SSE view: %+v", view.State.HostHealth)
	}
}
