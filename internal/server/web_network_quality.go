package server

import (
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

//go:embed quality_catalog.json
var qualityCatalogFiles embed.FS

type qualityEndpoint struct {
	Endpoint   *string `json:"endpoint"`
	Provenance string  `json:"provenance"`
}
type qualityRegion struct {
	ID        string                                `json:"id"`
	Label     string                                `json:"label"`
	Province  string                                `json:"province"`
	City      *string                               `json:"city"`
	Endpoints map[string]map[string]qualityEndpoint `json:"endpoints"`
}
type qualityCatalog struct {
	Source       string          `json:"source"`
	UpdatedAt    string          `json:"updated_at"`
	RawSHA       string          `json:"raw_sha256"`
	FrontendSHA  string          `json:"frontend_sha256"`
	Connectivity string          `json:"connectivity"`
	Regions      []qualityRegion `json:"regions"`
}

var qualityDirectory = loadQualityCatalog()

func loadQualityCatalog() qualityCatalog {
	raw, err := qualityCatalogFiles.ReadFile("quality_catalog.json")
	if err != nil {
		panic(err)
	}
	var catalog qualityCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		panic(err)
	}
	return catalog
}

func resolveQualityTarget(c protocol.QualityChoice, family string) (protocol.QualityTarget, error) {
	t := protocol.QualityTarget{}
	if c.Source == "catalog" {
		if c.Host != "" || c.Port != 0 {
			return t, storage.ErrQualityInvalid
		}
		if c.Region == "" {
			return t, nil
		}
		found := false
		for _, region := range qualityDirectory.Regions {
			if region.ID == c.Region {
				found = true
				fact := region.Endpoints[c.Slot][family]
				if fact.Endpoint != nil {
					host, port, err := net.SplitHostPort(*fact.Endpoint)
					if err != nil {
						return t, err
					}
					p, err := strconv.Atoi(port)
					if err != nil {
						return t, err
					}
					t.Host = host
					t.Port = p
				}
				break
			}
		}
		if !found {
			return t, storage.ErrQualityInvalid
		}
	} else if c.Source == "manual" {
		if c.Region != "" || c.Host == "" || strings.ContainsAny(c.Host, " /\\\x00\r\n\t") || (c.Protocol == "tcp" && (c.Port < 1 || c.Port > 65535)) || (c.Protocol == "icmp" && c.Port != 0) {
			return t, storage.ErrQualityInvalid
		}
		t.Host = strings.ToLower(c.Host)
		t.Port = c.Port
		probe := protocol.ProbeConfig{}
		kind := protocol.ProbeTypeTCPConnect
		if c.Protocol == "icmp" {
			kind = protocol.ProbeTypeICMPPing
			probe.ICMPPing = &protocol.ICMPPingConfig{Target: t.Host, Count: 1}
		} else {
			probe.TCPConnect = &protocol.TCPConnectConfig{Host: t.Host, Port: t.Port}
		}
		if err := probe.Validate(kind); err != nil {
			return t, storage.ErrQualityInvalid
		}
		if ip, err := netip.ParseAddr(t.Host); err == nil {
			if (family == "ipv4") != ip.Unmap().Is4() {
				return protocol.QualityTarget{}, nil
			}
		}
	} else {
		return t, storage.ErrQualityInvalid
	}
	if c.Protocol == "icmp" {
		t.Port = 0
	}
	if t.Host != "" {
		digest := sha256.Sum256([]byte(t.Host + ":" + strconv.Itoa(t.Port) + "/" + c.Protocol + "/" + family))
		t.EndpointVersion = hex.EncodeToString(digest[:])[:16]
	}
	return t, nil
}

func (a *App) qualityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/agent/network-quality", a.handleAgentQuality)
	mux.HandleFunc("POST /api/v1/agent/network-quality/config", a.handleAgentQualityConfig)
	mux.Handle("GET /api/v1/web/network-quality/catalog", qualityBudgetHandler(a.requireWebSession(http.HandlerFunc(a.handleQualityCatalog), true)))
	path := "/api/v1/web/agents/{agent_id}/network-quality/"
	mux.Handle("GET "+path+"config", qualityBudgetHandler(a.requireWebSession(http.HandlerFunc(a.handleQualityConfig), true)))
	mux.Handle("PUT "+path+"config", qualityBudgetHandler(a.requireWebMutation(http.HandlerFunc(a.handleQualityConfig))))
	mux.Handle("GET "+path+"history", qualityBudgetHandler(a.requireWebSession(http.HandlerFunc(a.handleQualityHistory), true)))
}
func (a *App) handleQualityCatalog(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_query", "query not allowed")
		return
	}
	// Region picker publishes availability facts, not domain labels.
	type option struct {
		ID        string                     `json:"id"`
		Label     string                     `json:"label"`
		Province  string                     `json:"province"`
		Available map[string]map[string]bool `json:"available"`
	}
	options := []option{}
	for _, region := range qualityDirectory.Regions {
		available := map[string]map[string]bool{}
		for slot, families := range region.Endpoints {
			available[slot] = map[string]bool{}
			for family, fact := range families {
				available[slot][family] = fact.Endpoint != nil
			}
		}
		options = append(options, option{region.ID, region.Label, region.Province, available})
	}
	writeJSON(w, 200, struct {
		Source       string   `json:"source"`
		SHA          string   `json:"raw_sha256"`
		Connectivity string   `json:"connectivity"`
		Regions      []option `json:"regions"`
	}{qualityDirectory.Source, qualityDirectory.RawSHA, "not_tested", options})
}
func qualityWebPath(r *http.Request, suffix string) bool {
	return validWebAgentID(r.PathValue("agent_id")) && r.URL.EscapedPath() == webAgentPathPrefix+r.PathValue("agent_id")+"/network-quality/"+suffix
}
func (a *App) handleQualityConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !qualityWebPath(r, "config") || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid quality path")
		return
	}
	r, cancel := qualityHTTPBudget(w, r)
	defer cancel()
	ctx := r.Context()
	var config protocol.QualityConfig
	var err error
	if r.Method == http.MethodGet {
		config, err = a.store.GetQualityConfig(ctx, r.PathValue("agent_id"))
	} else {
		if !hasJSONContentType(r) {
			writeJobError(w, 415, "invalid_content_type", "JSON required")
			return
		}
		body, readErr := readBoundedBody(w, r, 4096)
		if readErr != nil {
			writeBodyError(w, readErr)
			return
		}
		var request protocol.QualityConfigUpdate
		if err := decodeAgentRemovalJSON(body, &request); err != nil {
			writeJobError(w, 400, "invalid_request", "invalid config JSON")
			return
		}
		config, err = a.store.UpdateQualityConfig(ctx, r.PathValue("agent_id"), request, resolveQualityTarget, a.now())
	}
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	writeJSON(w, 200, config)
}
func (a *App) handleQualityHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !qualityWebPath(r, "history") {
		writeJobError(w, 400, "invalid_request", "invalid quality path")
		return
	}
	q, parseErr := url.ParseQuery(r.URL.RawQuery)
	if parseErr != nil || len(q) != 2 || len(q["target_id"]) != 1 || len(q["hours"]) != 1 || !validLowerHexID(q.Get("target_id"), 32) {
		writeJobError(w, 400, "invalid_query", "target_id and hours required")
		return
	}
	hours, err := strconv.Atoi(q.Get("hours"))
	if err != nil || (hours != 1 && hours != 24) {
		writeJobError(w, 400, "invalid_query", "hours must be 1 or 24")
		return
	}
	history, err := a.store.QualityHistory(r.Context(), r.PathValue("agent_id"), q.Get("target_id"), hours, a.now())
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	writeJSON(w, 200, history)
}
func qualityHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrQualityConflict):
		writeJobError(w, 409, "revision_conflict", "configuration changed")
	case errors.Is(err, storage.ErrQualityQuota):
		writeJobError(w, 409, "quality_quota", "quality capacity reached")
	case errors.Is(err, storage.ErrQualityInvalid):
		writeJobError(w, 400, "invalid_request", "invalid quality data")
	case errors.Is(err, storage.ErrQualityUnsupported):
		writeJobError(w, 409, "unsupported", "current session unsupported")
	case errors.Is(err, storage.ErrQualityStoragePressure):
		writeJobError(w, 503, "storage_pressure", "quality store under pressure")
	case errors.Is(err, storage.ErrAgentNotFound):
		writeJobError(w, 404, "agent_not_found", "Agent not found")
	case errors.Is(err, storage.ErrUnauthorized), errors.Is(err, storage.ErrAgentRemovalPending):
		writeJobError(w, 403, "unavailable", "Agent unavailable")
	default:
		writeJobError(w, 503, "retryable", "quality store unavailable")
	}
}
