package server

import (
	"bytes"
	_ "embed"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"404-probe/internal/storage"
)

//go:embed skin_example.json
var exampleSkin []byte

const skinsPath = "/api/v1/web/skins"

func (a *App) skinRoutes(mux *http.ServeMux) {
	mux.Handle("GET /skin.css", a.requireWebTarget(http.HandlerFunc(a.handleSkinCSS)))
	mux.Handle("GET "+skinsPath, a.requireWebSession(http.HandlerFunc(a.handleListSkins), true))
	mux.Handle("POST "+skinsPath, a.requireWebMutation(http.HandlerFunc(a.handleCreateSkin)))
	mux.Handle("DELETE "+skinsPath+"/{id}", a.requireWebMutation(http.HandlerFunc(a.handleDeleteSkin)))
	mux.Handle("PUT "+skinsPath+"/active", a.requireWebMutation(http.HandlerFunc(a.handleSetActiveSkin)))
	mux.Handle("GET "+skinsPath+"/{id}/download", a.requireWebSession(http.HandlerFunc(a.handleDownloadSkin), true))
	mux.Handle("GET "+skinsPath+"/example", a.requireWebSession(http.HandlerFunc(a.handleExampleSkin), true))
}

type skinSwatches struct {
	Light []string `json:"light"`
	Dark  []string `json:"dark"`
}

type skinSummary struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Author      string       `json:"author"`
	Description string       `json:"description"`
	CreatedAt   int64        `json:"created_at"`
	Builtin     bool         `json:"builtin"`
	Swatches    skinSwatches `json:"swatches"`
}

func (p skinPalette) summarize(skin storage.Skin) skinSummary {
	out := skinSummary{ID: skin.ID, Name: skin.Name, Author: skin.Author, Description: skin.Description,
		CreatedAt: skin.CreatedAt, Builtin: skin.ID == storage.DefaultSkinID}
	for _, name := range skinSwatchNames {
		light := p.light[name]
		dark := p.dark[name]
		if dark == "" {
			dark = light
		}
		if value := skin.Light[name]; value != "" {
			light = value
		}
		if value := skin.Dark[name]; value != "" {
			dark = value
		}
		out.Swatches.Light = append(out.Swatches.Light, light)
		out.Swatches.Dark = append(out.Swatches.Dark, dark)
	}
	return out
}

func (a *App) handleListSkins(w http.ResponseWriter, r *http.Request) {
	active, skins, err := a.store.ListSkins(r.Context())
	if err != nil {
		writeSkinStorageError(w, err)
		return
	}
	out := []skinSummary{a.skinPalette.summarize(storage.Skin{ID: storage.DefaultSkinID, Name: "默认"})}
	for _, skin := range skins {
		out = append(out, a.skinPalette.summarize(skin))
	}
	writeJSON(w, 200, struct {
		ActiveID string        `json:"active_id"`
		Skins    []skinSummary `json:"skins"`
	}{active, out})
}

func (a *App) handleCreateSkin(w http.ResponseWriter, r *http.Request) {
	if !hasJSONContentType(r) {
		writeJobError(w, 415, "invalid_content_type", "JSON required")
		return
	}
	body, err := readBoundedBody(w, r, skinFileLimit)
	if err != nil {
		writeInvalidSkin(w, []skinImportError{{"file", "", "invalid_size"}})
		return
	}
	skin, ignored, problems := a.skinPalette.parseFile(body)
	if len(problems) != 0 {
		writeInvalidSkin(w, problems)
		return
	}
	skin, err = a.store.CreateSkin(r.Context(), skin, a.now())
	if err != nil {
		writeSkinStorageError(w, err)
		return
	}
	if ignored == nil {
		ignored = []string{}
	}
	writeJSON(w, 201, struct {
		Skin    skinSummary `json:"skin"`
		Ignored []string    `json:"ignored"`
	}{a.skinPalette.summarize(skin), ignored})
}

func writeInvalidSkin(w http.ResponseWriter, details []skinImportError) {
	type body struct {
		Code    string            `json:"code"`
		Details []skinImportError `json:"details"`
	}
	writeJSON(w, 400, struct {
		Error body `json:"error"`
	}{body{"invalid_skin", details}})
}

func writeSkinStorageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrBuiltinSkin):
		writeJobError(w, 400, "builtin_skin", "the default skin cannot be deleted")
	case errors.Is(err, storage.ErrSkinNotFound):
		writeJobError(w, 404, "not_found", "skin not found")
	case errors.Is(err, storage.ErrSkinLimit):
		writeJobError(w, 409, "skin_limit", "skin limit reached")
	default:
		writeJobError(w, 503, "retryable", "skin store unavailable")
	}
}

func (a *App) handleDeleteSkin(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteSkin(r.Context(), r.PathValue("id")); err != nil {
		writeSkinStorageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleSetActiveSkin(w http.ResponseWriter, r *http.Request) {
	if !hasJSONContentType(r) {
		writeJobError(w, 415, "invalid_content_type", "JSON required")
		return
	}
	body, err := readBoundedBody(w, r, 1024)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var input struct {
		ID string `json:"id"`
	}
	if decodeAgentRemovalJSON(body, &input) != nil || input.ID == "" {
		writeJobError(w, 400, "invalid_request", "skin id required")
		return
	}
	if err := a.store.SetActiveSkin(r.Context(), input.ID); err != nil {
		writeSkinStorageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleDownloadSkin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validLowerHexID(id, 32) {
		writeSkinStorageError(w, storage.ErrSkinNotFound)
		return
	}
	skin, err := a.store.GetSkin(r.Context(), id)
	if err != nil {
		writeSkinStorageError(w, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="skin-`+id+`.json"`)
	writeJSON(w, 200, skinFileFrom(skin))
}

func (a *App) handleExampleSkin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Disposition", `attachment; filename="skin-example.json"`)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(exampleSkin)
}

func skinStylesheet(skin storage.Skin) []byte {
	if skin.ID == storage.DefaultSkinID {
		return nil
	}
	var css strings.Builder
	for _, mode := range []struct {
		selector string
		values   map[string]string
	}{{":root:not([data-theme=dark])", skin.Light}, {":root[data-theme=dark]", skin.Dark}} {
		css.WriteString(mode.selector)
		css.WriteByte('{')
		for _, name := range slices.Sorted(maps.Keys(mode.values)) {
			css.WriteString(name)
			css.WriteByte(':')
			css.WriteString(mode.values[name])
			css.WriteByte(';')
		}
		css.WriteString("}\n")
	}
	return []byte(css.String())
}

func (a *App) handleSkinCSS(w http.ResponseWriter, r *http.Request) {
	skin, err := a.store.ActiveSkin(r.Context())
	if err != nil {
		writeSkinStorageError(w, err)
		return
	}
	asset := makeStaticAsset("skin.css", skinStylesheet(skin))
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Del("Pragma")
	w.Header().Set("ETag", asset.etag)
	http.ServeContent(staticErrorCacheWriter{w}, r, asset.name, time.Time{}, bytes.NewReader(asset.data))
}
