package server

import (
	"404-probe/internal/protocol"
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

const (
	qualityV4BodyLimit    = 16384
	qualityDNSCheckBudget = 2 * time.Second
	qualityDNSCheckSlots  = 4
	qualityDNSMaxNames    = 3
)

// qualityLookupFunc is the Server's own resolver; tests replace it with a fake.
type qualityLookupFunc func(ctx context.Context, network, host string) ([]netip.Addr, error)

func systemQualityLookup(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, network, host)
}

func (a *App) qualityV4Routes(mux *http.ServeMux, path string) {
	mux.Handle("GET "+path+"v4/config", qualityBudgetHandler(a.requireWebSession(http.HandlerFunc(a.handleQualityV4Config), true)))
	mux.Handle("PUT "+path+"v4/config", qualityBudgetHandler(a.requireWebMutation(http.HandlerFunc(a.handleQualityV4Config))))
	mux.Handle("PUT "+path+"v4/enabled", qualityBudgetHandler(a.requireWebMutation(http.HandlerFunc(a.handleQualityV4Enabled))))
}

// writeQualityFieldErrors answers 400 with one entry per input to mark. The response never echoes
// what was typed or what a name resolved to.
func writeQualityFieldErrors(w http.ResponseWriter, errs []protocol.QualityFieldError) {
	if len(errs) == 0 {
		errs = []protocol.QualityFieldError{{Field: "request", Code: protocol.QualityErrInvalidRequest}}
	}
	type body struct {
		Code    string                       `json:"code"`
		Message string                       `json:"message"`
		Details []protocol.QualityFieldError `json:"details"`
	}
	writeJSON(w, http.StatusBadRequest, struct {
		Error body `json:"error"`
	}{Error: body{Code: "invalid_request", Message: "invalid network quality settings", Details: errs}})
}

func (a *App) handleQualityV4Config(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !qualityWebPath(r, "v4/config") || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid quality path")
		return
	}
	r, cancel := qualityHTTPBudget(w, r)
	defer cancel()
	id := r.PathValue("agent_id")
	if r.Method == http.MethodGet {
		config, err := a.store.GetQualityConfigV4(r.Context(), id, a.now())
		if err != nil {
			qualityHTTPError(w, err)
			return
		}
		writeJSON(w, 200, config)
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, 415, "invalid_content_type", "JSON required")
		return
	}
	body, err := readBoundedBody(w, r, qualityV4BodyLimit)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	if err := protocol.CheckQualityRequestEncoding(body); err != nil {
		writeQualityFieldErrors(w, []protocol.QualityFieldError{{Field: "request", Code: protocol.QualityErrInvalidEncoding}})
		return
	}
	var request protocol.QualityV4SaveRequest
	if err := decodeAgentRemovalJSON(body, &request); err != nil {
		writeQualityFieldErrors(w, nil)
		return
	}
	var errs []protocol.QualityFieldError
	if _, ok := protocol.ParseQualityExpectedRevision(request.ExpectedRevision); !ok {
		errs = append(errs, protocol.QualityFieldError{Field: "expected_revision", Code: protocol.QualityErrInvalidRequest})
	}
	if !protocol.QualityIntervalValid(request.IntervalMS) {
		errs = append(errs, protocol.QualityFieldError{Field: "interval_ms", Code: protocol.QualityErrInvalidInterval})
	}
	if len(request.Slots) != 3 {
		errs = append(errs, protocol.QualityFieldError{Field: "slots", Code: protocol.QualityErrInvalidRequest})
	}
	seen := map[string]bool{}
	literal := map[string]bool{}
	for i, slot := range request.Slots {
		normalized, slotErrs := protocol.NormalizeQualitySlot(slot)
		errs = append(errs, slotErrs...)
		if seen[slot.Slot] {
			errs = append(errs, protocol.QualityFieldError{Slot: slot.Slot, Field: "slot", Code: protocol.QualityErrInvalidRequest})
		}
		seen[slot.Slot] = true
		if len(slotErrs) == 0 {
			_, lit, _ := protocol.NormalizeQualityHost(slot.Host)
			literal[normalized.Host] = lit != ""
			request.Slots[i] = normalized
		}
	}
	if len(errs) != 0 {
		writeQualityFieldErrors(w, errs)
		return
	}
	stored, err := a.store.QualityV4SlotHosts(r.Context(), id)
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	var names []string
	byName := map[string][]string{}
	for _, slot := range request.Slots {
		if slot.Host == "" || literal[slot.Host] || stored[slot.Slot] == slot.Host {
			continue
		}
		if len(byName[slot.Host]) == 0 {
			names = append(names, slot.Host)
		}
		byName[slot.Host] = append(byName[slot.Host], slot.Slot)
	}
	unsafe, err := a.checkQualityNames(r.Context(), names)
	if err != nil {
		writeJobError(w, 503, "retryable", "address check is busy, try again")
		return
	}
	for _, name := range unsafe {
		for _, slot := range byName[name] {
			errs = append(errs, protocol.QualityFieldError{Slot: slot, Field: "host", Code: protocol.QualityErrUnsafeAddress})
		}
	}
	if len(errs) != 0 {
		writeQualityFieldErrors(w, errs)
		return
	}
	config, err := a.store.SaveQualityConfigV4(r.Context(), id, request, a.now())
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	writeJSON(w, 200, config)
}

func (a *App) handleQualityV4Enabled(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !qualityWebPath(r, "v4/enabled") || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid quality path")
		return
	}
	r, cancel := qualityHTTPBudget(w, r)
	defer cancel()
	if !hasJSONContentType(r) {
		writeJobError(w, 415, "invalid_content_type", "JSON required")
		return
	}
	body, err := readBoundedBody(w, r, 1024)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var request protocol.QualityV4EnabledRequest
	if err := decodeAgentRemovalJSON(body, &request); err != nil || request.Enabled == nil {
		writeJobError(w, 400, "invalid_request", "invalid quality switch request")
		return
	}
	config, err := a.store.SetQualityEnabledV4(r.Context(), r.PathValue("agent_id"), request, a.now())
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	writeJSON(w, 200, config)
}

var errQualityDNSBusy = errors.New("quality address checks are busy")

// checkQualityNames resolves the changed names once, in parallel, under one shared 2 s budget, and
// returns those with any non-public answer on either side. A name that fails to resolve on both
// sides is allowed: the Agent checks again before every connection. Nothing that was resolved is
// stored, logged or returned.
func (a *App) checkQualityNames(ctx context.Context, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) > qualityDNSMaxNames {
		return nil, errQualityDNSBusy
	}
	select {
	case a.qualityDNSSlots <- struct{}{}:
		defer func() { <-a.qualityDNSSlots }()
	default:
		return nil, errQualityDNSBusy
	}
	ctx, cancel := context.WithTimeout(ctx, qualityDNSCheckBudget)
	defer cancel()
	unsafe := make([]bool, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, network := range []string{"ip4", "ip6"} {
				addrs, err := a.qualityLookup(ctx, network, name)
				if err != nil {
					continue
				}
				for _, addr := range addrs {
					addr = addr.Unmap()
					family := "ipv6"
					if addr.Is4() {
						family = "ipv4"
					}
					if !protocol.PublicDestinationIP(addr.WithZone("").String(), family) {
						unsafe[i] = true
					}
				}
			}
		}()
	}
	wg.Wait()
	var out []string
	for i, bad := range unsafe {
		if bad {
			out = append(out, names[i])
		}
	}
	return out, nil
}
