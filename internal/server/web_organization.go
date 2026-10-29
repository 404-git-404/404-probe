package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"404-probe/internal/storage"
)

func (a *App) organizationRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/web/organization/catalog", a.requireWebSession(http.HandlerFunc(a.handleOrganizationCatalog), true))
	for _, resource := range []string{"groups", "tags", "exchange-rates"} {
		mux.Handle("GET /api/v1/web/"+resource, a.requireWebSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleOrganizationList(w, r, resource) }), true))
	}
	for _, route := range []struct{ method, path, kind string }{
		{"POST", "groups", "create_group"}, {"PATCH", "groups/{group_id}", "rename_group"}, {"DELETE", "groups/{group_id}", "delete_group"}, {"PUT", "groups/order", "sort_groups"},
		{"POST", "tags", "create_tag"}, {"PATCH", "tags/{tag_id}", "rename_tag"}, {"DELETE", "tags/{tag_id}", "delete_tag"},
		{"PUT", "exchange-rates", "save_rates"}, {"DELETE", "exchange-rates/{currency}", "delete_rate"},
		{"PUT", "agents/{agent_id}/organization", "assign"},
	} {
		mux.Handle(route.method+" /api/v1/web/"+route.path, a.requireWebMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleOrganizationMutation(w, r, route.kind) })))
	}
	mux.Handle("GET /api/v1/web/agents/{agent_id}/organization", a.requireWebSession(http.HandlerFunc(a.handleAgentOrganization), true))
}

// Directory-only snapshot: one catalog revision, no dashboard state or network
// computation. Counts and plan currency codes support the existing dialogs.
func (a *App) handleOrganizationCatalog(w http.ResponseWriter, r *http.Request) {
	if r.URL.EscapedPath() != "/api/v1/web/organization/catalog" || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid catalog path")
		return
	}
	c, items, err := a.store.OrganizationDirectory(r.Context())
	if err != nil {
		a.organizationError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revision": c.Revision, "groups": c.Groups, "tags": c.Tags, "exchange_rates": c.Rates, "items": items})
}

func (a *App) handleOrganizationList(w http.ResponseWriter, r *http.Request, resource string) {
	if r.URL.EscapedPath() != "/api/v1/web/"+resource || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid organization path")
		return
	}
	c, err := a.store.OrganizationCatalog(r.Context())
	if err != nil {
		a.organizationError(w, err)
		return
	}
	var items any
	switch resource {
	case "groups":
		items = c.Groups
	case "tags":
		items = c.Tags
	case "exchange-rates":
		items = c.Rates
	}
	writeJSON(w, 200, map[string]any{"revision": c.Revision, "items": items})
}

func (a *App) handleAgentOrganization(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent_id")
	if !validWebAgentID(id) || r.URL.EscapedPath() != webAgentPathPrefix+id+"/organization" || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_request", "invalid organization path")
		return
	}
	_, err := a.store.GetAgentSnapshot(r.Context(), id, a.now(), a.offlineTimeout)
	if err != nil {
		a.organizationError(w, err)
		return
	}
	c, err := a.store.OrganizationCatalog(r.Context())
	if err != nil {
		a.organizationError(w, err)
		return
	}
	item, ok := c.Assignments[id]
	if !ok {
		item = storage.AgentOrganization{AgentID: id, TagIDs: []string{}, Revision: c.Revision}
	}
	writeJSON(w, 200, map[string]any{"revision": c.Revision, "item": item})
}

func readOrganizationJSON(w http.ResponseWriter, r *http.Request, target any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return storage.ErrOrganizationInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return storage.ErrOrganizationInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if len(bytes.TrimSpace(body)) == 0 || bytes.TrimSpace(body)[0] != '{' {
		return storage.ErrOrganizationInvalid
	}
	if err = uniqueOrganizationJSON(decoder, 0); err != nil {
		return storage.ErrOrganizationInvalid
	}
	return decodeStrictJSON(body, target)
}

func unicodeFoldRune(r rune) rune {
	for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
		if next < r {
			return unicodeFoldRune(next)
		}
	}
	return r
}

func uniqueOrganizationJSON(d *json.Decoder, depth int) error {
	if depth > 8 {
		return storage.ErrOrganizationInvalid
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				// Match encoding/json's case-insensitive struct field matching,
				// including Unicode simple-fold equivalences (e.g. Kelvin sign).
				name = strings.Map(func(r rune) rune { return unicodeFoldRune(r) }, name)
				if !ok || seen[name] {
					return storage.ErrOrganizationInvalid
				}
				seen[name] = true
				if err = uniqueOrganizationJSON(d, depth+1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return storage.ErrOrganizationInvalid
			}
		case '[':
			for d.More() {
				if err := uniqueOrganizationJSON(d, depth+1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return storage.ErrOrganizationInvalid
			}
		default:
			return storage.ErrOrganizationInvalid
		}
	}
	return nil
}

func (a *App) handleOrganizationMutation(w http.ResponseWriter, r *http.Request, kind string) {
	c := storage.OrganizationChange{Kind: kind}
	path := "/api/v1/web/"
	switch kind {
	case "create_group":
		path += "groups"
	case "create_tag":
		path += "tags"
	case "rename_group", "delete_group":
		c.ID = r.PathValue("group_id")
		path += "groups/" + c.ID
		if !validWebAgentID(c.ID) {
			a.organizationError(w, storage.ErrOrganizationInvalid)
			return
		}
	case "rename_tag", "delete_tag":
		c.ID = r.PathValue("tag_id")
		path += "tags/" + c.ID
		if !validWebAgentID(c.ID) {
			a.organizationError(w, storage.ErrOrganizationInvalid)
			return
		}
	case "sort_groups":
		path += "groups/order"
	case "assign":
		c.AgentID = r.PathValue("agent_id")
		path += "agents/" + c.AgentID + "/organization"
		if !validWebAgentID(c.AgentID) {
			a.organizationError(w, storage.ErrOrganizationInvalid)
			return
		}
	case "save_rates":
		path += "exchange-rates"
	case "delete_rate":
		c.ID = r.PathValue("currency")
		path += "exchange-rates/" + c.ID
	}
	if r.URL.EscapedPath() != path || r.URL.RawQuery != "" {
		a.organizationError(w, storage.ErrOrganizationInvalid)
		return
	}
	var err error
	switch {
	case strings.HasPrefix(kind, "create_") || strings.HasPrefix(kind, "rename_"):
		var input struct {
			ExpectedRevision int64  `json:"expected_revision"`
			Name             string `json:"name"`
		}
		err = readOrganizationJSON(w, r, &input)
		c.ExpectedRevision, c.Name = input.ExpectedRevision, input.Name
	case kind == "sort_groups":
		var input struct {
			ExpectedRevision int64    `json:"expected_revision"`
			IDs              []string `json:"ids"`
		}
		err = readOrganizationJSON(w, r, &input)
		c.ExpectedRevision, c.Order = input.ExpectedRevision, input.IDs
	case kind == "assign":
		var input struct {
			ExpectedRevision int64     `json:"expected_revision"`
			GroupID          *string   `json:"group_id"`
			TagIDs           *[]string `json:"tag_ids"`
			NewGroup         string    `json:"new_group"`
			NewTags          []string  `json:"new_tags"`
		}
		err = readOrganizationJSON(w, r, &input)
		c.ExpectedRevision, c.GroupID, c.NewGroup, c.NewTags = input.ExpectedRevision, input.GroupID, input.NewGroup, input.NewTags
		if input.TagIDs == nil {
			err = storage.ErrOrganizationInvalid
		} else {
			c.TagIDs = *input.TagIDs
		}
	case kind == "save_rates":
		var input struct {
			ExpectedRevision int64 `json:"expected_revision"`
			Rates            []struct {
				Currency    string `json:"currency"`
				UnitsPerUSD string `json:"units_per_usd"`
			} `json:"rates"`
		}
		err = readOrganizationJSON(w, r, &input)
		c.ExpectedRevision = input.ExpectedRevision
		for _, r := range input.Rates {
			c.Rates = append(c.Rates, storage.ExchangeRate{Currency: r.Currency, UnitsPerUSD: r.UnitsPerUSD})
		}
	default:
		var input struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if values, present := r.Header["If-Match"]; present {
			if len(values) != 1 {
				err = storage.ErrOrganizationInvalid
			} else {
				value := values[0]
				if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
					value = value[1 : len(value)-1]
				}
				input.ExpectedRevision, err = strconv.ParseInt(value, 10, 64)
				if err == nil && (input.ExpectedRevision <= 0 || strconv.FormatInt(input.ExpectedRevision, 10) != value) {
					err = storage.ErrOrganizationInvalid
				}
			}
		} else {
			err = readOrganizationJSON(w, r, &input)
		}
		c.ExpectedRevision = input.ExpectedRevision
	}
	if err != nil {
		a.organizationError(w, storage.ErrOrganizationInvalid)
		return
	}
	result, err := a.store.ChangeOrganization(r.Context(), c, a.now())
	if err != nil {
		a.organizationError(w, err)
		return
	}
	// Catalog events are independent of runtime samples and carry committed CAS.
	payload, _ := json.Marshal(map[string]any{"event": "organization", "kind": kind, "revision": result.Revision, "id": result.ID, "changed_agent_ids": result.ChangedAgentIDs})
	a.hub.publish(payload)
	status := http.StatusOK
	if strings.HasPrefix(kind, "create_") {
		status = http.StatusCreated
	}
	writeJSON(w, status, result)
}

func (a *App) organizationError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "could not update organization"
	switch {
	case errors.Is(err, storage.ErrOrganizationConflict):
		status, code, message = 409, "revision_conflict", "organization changed; reload before saving"
	case errors.Is(err, storage.ErrOrganizationNameConflict):
		status, code, message = 409, "name_conflict", "name already exists"
	case errors.Is(err, storage.ErrOrganizationNotFound), errors.Is(err, storage.ErrAgentNotFound):
		status, code, message = 404, "not_found", "organization resource not found"
	case errors.Is(err, storage.ErrAgentRevoked):
		status, code, message = 409, "agent_revoked", "revoked devices cannot be edited"
	case errors.Is(err, storage.ErrOrganizationInvalid):
		status, code, message = 400, "invalid_request", "invalid organization input"
	case errors.Is(err, storage.ErrOrganizationLimit):
		status, code, message = 400, "limit_reached", "organization limit reached"
	case errors.Is(err, storage.ErrExchangeRateReadOnly):
		status, code, message = 400, "usd_read_only", "USD exchange rate is fixed at 1"
	default:
		a.logger.Error("organization request", "error", err)
	}
	writeJobError(w, status, code, message)
}
