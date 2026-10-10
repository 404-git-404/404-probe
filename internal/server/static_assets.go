package server

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// These are the reviewed production references, not a request-populated cache.
var fingerprintAssetNames = []string{
	"agent-state.js", "app.js", "card-metrics.js", "device-details.js", "history.js",
	"jobs.js", "management.js", "network-quality-core.js", "network-quality.js",
	"overview.js", "plan-editor.js", "plan-renewal.js", "plan-units.js",
	"resource-history.js", "schedules.js", "selector.js", "session.js", "traffic-chart.js",
	"network-quality.css", "card-layout.css", "traffic-chart.css", "device-details.css",
	"plan-editor.css", "plan-renewal.css", "overview.css",
	"vendor/uplot/uPlot.min.css", "vendor/uplot/uPlot.iife.min.js",
	"vendor/card-icons/cpu.svg", "vendor/card-icons/memory.svg",
	"vendor/card-icons/disk.svg", "vendor/card-icons/io.svg",
}

var staticPageNames = []string{"index.html", "history.html", "jobs.html", "schedules.html"}
var staticPageReferences = map[string][]string{
	"index.html":     {"style.css", "vendor/uplot/uPlot.min.css", "network-quality.css", "card-layout.css", "traffic-chart.css", "device-details.css", "plan-editor.css", "plan-renewal.css", "overview.css", "agent-state.js", "management.js", "selector.js", "vendor/uplot/uPlot.iife.min.js", "network-quality-core.js", "network-quality.js", "traffic-chart.js", "card-metrics.js", "resource-history.js", "device-details.js", "plan-editor.js", "plan-renewal.js", "overview.js", "plan-units.js", "app.js", "session.js"},
	"history.html":   {"style.css", "agent-state.js", "management.js", "selector.js", "history.js", "session.js"},
	"jobs.html":      {"style.css", "jobs.js", "session.js"},
	"schedules.html": {"style.css", "schedules.js", "session.js"},
}
var staticReference = regexp.MustCompile(`(?:src|href)="(/[^"\s]+\.(?:js|css))"`)

type staticAsset struct {
	name string
	data []byte
	etag string
}

type staticAssets struct {
	legacy       map[string]staticAsset
	immutable    map[string]staticAsset
	pages        map[string]staticAsset
	fingerprints map[string]string
}

func makeStaticAsset(name string, data []byte) staticAsset {
	return staticAsset{name: name, data: data, etag: fmt.Sprintf(`"%x"`, sha256.Sum256(data))}
}

// The directory and all validators are built once per App from embedded bytes.
// Requests only look up immutable entries; no hashing, disk or network work.
func newStaticAssets(source fs.FS) (*staticAssets, error) {
	a := &staticAssets{legacy: make(map[string]staticAsset), immutable: make(map[string]staticAsset), pages: make(map[string]staticAsset), fingerprints: make(map[string]string)}
	err := fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		a.legacy["/"+name] = makeStaticAsset(name, data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := a.legacy["/style.css"]; !ok {
		return nil, fmt.Errorf("missing style.css")
	}
	add := func(name string, data []byte) {
		asset := makeStaticAsset(name, data)
		url := "/assets/" + strings.Trim(asset.etag, `"`) + "/" + name
		a.fingerprints[name] = url
		a.immutable[url] = asset
	}
	for _, name := range fingerprintAssetNames {
		asset, ok := a.legacy["/"+name]
		if !ok {
			return nil, fmt.Errorf("missing fixed asset %s", name)
		}
		if name != "card-layout.css" {
			add(name, asset.data)
		}
	}
	css := string(a.legacy["/card-layout.css"].data)
	for _, icon := range []string{"cpu", "memory", "disk", "io"} {
		name := "vendor/card-icons/" + icon + ".svg"
		old := "url('/" + name + "')"
		// M04 renders resource names inside rings, so CSS no longer references
		// these icons. Retain immutable icon URLs for prior pages and rollback.
		if strings.Count(css, old) > 1 {
			return nil, fmt.Errorf("CSS reference mismatch %s", name)
		}
		css = strings.ReplaceAll(css, old, "url('"+a.fingerprints[name]+"')")
	}
	if strings.Contains(css, "url('/vendor/card-icons/") {
		return nil, fmt.Errorf("unlisted CSS icon reference")
	}
	add("card-layout.css", []byte(css))
	used := make(map[string]bool)
	for _, name := range staticPageNames {
		page, ok := a.legacy["/"+name]
		if !ok {
			return nil, fmt.Errorf("missing page %s", name)
		}
		refs := staticReference.FindAllSubmatch(page.data, -1)
		expected := staticPageReferences[name]
		if len(refs) != len(expected) {
			return nil, fmt.Errorf("page reference count mismatch %s", name)
		}
		for i, ref := range refs {
			if string(ref[1]) != "/"+expected[i] {
				return nil, fmt.Errorf("page reference order mismatch %s #%d", name, i)
			}
		}
		var refErr error
		rewritten := staticReference.ReplaceAllStringFunc(string(page.data), func(match string) string {
			path := staticReference.FindStringSubmatch(match)[1]
			if path == "/style.css" {
				return match
			}
			target, ok := a.fingerprints[path[1:]]
			if !ok {
				refErr = fmt.Errorf("unlisted reference %s in %s", path, name)
				return match
			}
			used[path[1:]] = true
			return strings.Replace(match, path, target, 1)
		})
		if refErr != nil {
			return nil, refErr
		}
		a.pages["/"+name] = makeStaticAsset(name, []byte(rewritten))
	}
	for _, name := range fingerprintAssetNames {
		if !strings.HasSuffix(name, ".svg") && !used[name] {
			return nil, fmt.Errorf("unreferenced fixed asset %s", name)
		}
	}
	a.pages["/"] = a.pages["/index.html"]
	return a, nil
}

func (a *staticAssets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		setWebNoStore(w)
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Path
	asset, ok := a.pages[path]
	cache := "no-store"
	if !ok {
		asset, ok = a.immutable[path]
		cache = "private, max-age=31536000, immutable"
	}
	if !ok && !strings.HasPrefix(path, "/assets/") {
		asset, ok = a.legacy[path]
		cache = "private, no-cache"
		if path == "/style.css" {
			cache = "public, no-cache"
		}
	}
	if !ok {
		setWebNoStore(w)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", cache)
	if cache != "no-store" {
		w.Header().Del("Pragma")
	}
	w.Header().Set("ETag", asset.etag)
	// Standard library owns MIME, HEAD, conditional requests and byte ranges.
	http.ServeContent(staticErrorCacheWriter{w}, r, asset.name, time.Time{}, bytes.NewReader(asset.data))
}

// ServeContent clears validators/cache headers on errors. Restore only the
// failure policy at commit time; do not buffer or reimplement HTTP conditions.
type staticErrorCacheWriter struct{ http.ResponseWriter }

func (w staticErrorCacheWriter) WriteHeader(status int) {
	if status >= 300 && status != http.StatusNotModified {
		setWebNoStore(w.ResponseWriter)
		w.Header().Del("ETag")
		w.Header().Del("Last-Modified")
	}
	w.ResponseWriter.WriteHeader(status)
}
