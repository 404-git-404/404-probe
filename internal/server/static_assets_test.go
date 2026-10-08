package server

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"404-probe/web"
)

func staticTestSource(t *testing.T) fstest.MapFS {
	t.Helper()
	source, err := fs.Sub(web.Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	result := fstest.MapFS{}
	err = fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, err error) error {
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
		result[name] = &fstest.MapFile{Data: data}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustStaticAssets(t *testing.T, source fs.FS) *staticAssets {
	t.Helper()
	a, err := newStaticAssets(source)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func staticResponse(a *staticAssets, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	setWebNoStore(w) // emulate the unchanged successful authentication wrapper
	a.ServeHTTP(w, r)
	return w
}

func TestStaticAssetsIdentityUpgradeAndRollback(t *testing.T) {
	sourceA := staticTestSource(t)
	a := mustStaticAssets(t, sourceA)
	sourceB := staticTestSource(t)
	sourceB["app.js"] = &fstest.MapFile{Data: append(append([]byte{}, sourceB["app.js"].Data...), []byte("\n// controlled version B\n")...)}
	sourceB["vendor/card-icons/cpu.svg"] = &fstest.MapFile{Data: append(append([]byte{}, sourceB["vendor/card-icons/cpu.svg"].Data...), []byte("\n<!-- controlled version B -->")...)}
	b := mustStaticAssets(t, sourceB)
	rollback := mustStaticAssets(t, staticTestSource(t))
	for _, name := range fingerprintAssetNames {
		changed := name == "app.js" || name == "vendor/card-icons/cpu.svg" || name == "card-layout.css"
		if (a.fingerprints[name] != b.fingerprints[name]) != changed {
			t.Fatalf("unexpected identity change: %s", name)
		}
		if a.fingerprints[name] != rollback.fingerprints[name] {
			t.Fatalf("rollback identity: %s", name)
		}
		asset := b.immutable[b.fingerprints[name]]
		if asset.etag != fmt.Sprintf(`"%x"`, sha256.Sum256(asset.data)) {
			t.Fatalf("hash does not identify response bytes: %s", name)
		}
	}
	if !bytes.Equal(a.pages["/index.html"].data, rollback.pages["/index.html"].data) {
		t.Fatal("rollback page mismatch")
	}
	if bytes.Equal(a.pages["/index.html"].data, b.pages["/index.html"].data) {
		t.Fatal("upgrade HTML not updated")
	}
	for _, target := range []*staticAssets{a, b, rollback} {
		for _, name := range staticPageNames {
			original := staticReference.FindAllStringSubmatch(string(target.legacy["/"+name].data), -1)
			served := staticReference.FindAllStringSubmatch(string(target.pages["/"+name].data), -1)
			if len(original) != len(served) {
				t.Fatalf("reference count: %s", name)
			}
			for i, ref := range original {
				want := ref[1]
				if want != "/style.css" {
					want = target.fingerprints[want[1:]]
				}
				if served[i][1] != want {
					t.Fatalf("reference/order: %s #%d", name, i)
				}
				if w := staticResponse(target, "GET", want, nil); w.Code != 200 {
					t.Fatalf("unreachable reference %s", want)
				}
			}
			body := string(target.pages["/"+name].data)
			if strings.Count(body, " defer") != strings.Count(string(target.legacy["/"+name].data), " defer") {
				t.Fatal("changed defer order/attributes")
			}
		}
		css := string(target.immutable[target.fingerprints["card-layout.css"]].data)
		for _, icon := range []string{"cpu", "memory", "disk", "io"} {
			path := target.fingerprints["vendor/card-icons/"+icon+".svg"]
			if !strings.Contains(css, "url('"+path+"')") || staticResponse(target, "GET", path, nil).Code != 200 {
				t.Fatal("CSS dependency missing")
			}
		}
	}
	if w := staticResponse(b, "GET", a.fingerprints["app.js"], nil); w.Code != 404 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("old identity fell back to current bytes")
	}
}

func TestStaticAssetsInitializationFailsClosed(t *testing.T) {
	for _, name := range []string{"app.js", "style.css", "history.html", "vendor/card-icons/cpu.svg"} {
		t.Run(name, func(t *testing.T) {
			source := staticTestSource(t)
			delete(source, name)
			if _, err := newStaticAssets(source); err == nil {
				t.Fatal("missing fixed input accepted")
			}
		})
	}
	for _, kind := range []string{"unlisted-reference", "missing-reference", "css-reference"} {
		t.Run(kind, func(t *testing.T) {
			source := staticTestSource(t)
			name, old, replacement := "index.html", "/app.js", "/unlisted.js"
			if kind == "missing-reference" {
				replacement = "/session.js"
			}
			if kind == "css-reference" {
				name, old, replacement = "card-layout.css", "cpu.svg", "unknown.svg"
			}
			source[name] = &fstest.MapFile{Data: []byte(strings.ReplaceAll(string(source[name].Data), old, replacement))}
			if _, err := newStaticAssets(source); err == nil {
				t.Fatal("incomplete references accepted")
			}
		})
	}
	for _, kind := range []string{"missing-shared-session", "missing-login-style", "reordered-scripts"} {
		t.Run(kind, func(t *testing.T) {
			source := staticTestSource(t)
			body := string(source["history.html"].Data)
			switch kind {
			case "missing-shared-session":
				body = strings.ReplaceAll(body, `<script src="/session.js" defer></script>`, "")
			case "missing-login-style":
				body = strings.ReplaceAll(body, `<link rel="stylesheet" href="/style.css">`, "")
			case "reordered-scripts":
				body = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(body, "/agent-state.js", "/temporary.js"), "/management.js", "/agent-state.js"), "/temporary.js", "/management.js")
			}
			source["history.html"] = &fstest.MapFile{Data: []byte(body)}
			if _, err := newStaticAssets(source); err == nil {
				t.Fatal("page-specific incomplete/order references accepted")
			}
		})
	}
}

func TestStaticAssetsHTTPStandardConditions(t *testing.T) {
	a := mustStaticAssets(t, staticTestSource(t))
	for _, path := range []string{a.fingerprints["app.js"], "/app.js", "/style.css", "/vendor/card-icons/debian.svg", "/vendor/flag-icons/4x3/us.svg"} {
		t.Run(path, func(t *testing.T) {
			get := staticResponse(a, "GET", path, nil)
			if get.Code != 200 || get.Header().Get("ETag") == "" || get.Header().Get("Content-Type") == "" || get.Header().Get("Last-Modified") != "" {
				t.Fatalf("GET: %d %v", get.Code, get.Header())
			}
			cache := "private, no-cache"
			if strings.HasPrefix(path, "/assets/") {
				cache = "private, max-age=31536000, immutable"
			}
			if path == "/style.css" {
				cache = "public, no-cache"
			}
			if get.Header().Get("Cache-Control") != cache || get.Header().Get("Pragma") != "" {
				t.Fatal("wrong success cache policy")
			}
			if get.Header().Get("ETag") != fmt.Sprintf(`"%x"`, sha256.Sum256(get.Body.Bytes())) {
				t.Fatal("body/ETag mismatch")
			}
			for _, method := range []string{"GET", "HEAD"} {
				w := staticResponse(a, method, path, map[string]string{"If-None-Match": get.Header().Get("ETag")})
				if w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != cache {
					t.Fatalf("conditional %s: %d", method, w.Code)
				}
			}
			head := staticResponse(a, "HEAD", path, nil)
			if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
				t.Fatal("HEAD semantics")
			}
			rangeOK := staticResponse(a, "GET", path, map[string]string{"Range": "bytes=0-3", "If-Range": get.Header().Get("ETag")})
			if rangeOK.Code != 206 || !bytes.Equal(rangeOK.Body.Bytes(), get.Body.Bytes()[:4]) || rangeOK.Header().Get("Cache-Control") != cache {
				t.Fatal("standard range failed")
			}
			for _, tc := range []struct {
				headers map[string]string
				code    int
			}{{map[string]string{"Range": "bytes=999999999-"}, 416}, {map[string]string{"If-Match": `"wrong"`}, 412}} {
				w := staticResponse(a, "GET", path, tc.headers)
				if w.Code != tc.code || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Pragma") != "no-cache" || w.Header().Get("ETag") != "" {
					t.Fatalf("failure cache: %d %v", w.Code, w.Header())
				}
			}
			changed := staticResponse(a, "GET", path, map[string]string{"If-None-Match": `"other-version"`})
			if changed.Code != 200 || !bytes.Equal(changed.Body.Bytes(), get.Body.Bytes()) {
				t.Fatal("changed validator returned stale 304")
			}
			full := staticResponse(a, "GET", path, map[string]string{"Range": "bytes=0-3", "If-Range": `"other-version"`})
			if full.Code != 200 || !bytes.Equal(full.Body.Bytes(), get.Body.Bytes()) {
				t.Fatal("If-Range mismatch did not send full bytes")
			}
		})
	}
	for _, path := range []string{"/", "/index.html", "/history.html", "/jobs.html", "/schedules.html"} {
		if w := staticResponse(a, "GET", path, nil); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("HTML cache")
		}
	}
	for _, path := range []string{"/assets/" + strings.Repeat("0", 64) + "/app.js", "/assets/invalid/app.js", "/missing", "/vendor/", "/assets/"} {
		if w := staticResponse(a, "GET", path, nil); w.Code != 404 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("bad identity/path: %s", path)
		}
	}
	if w := staticResponse(a, "POST", a.fingerprints["app.js"], nil); w.Code != 405 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("method policy")
	}
}

func TestStaticAssetsAuthenticationAndNoStoreBoundaries(t *testing.T) {
	app, store, _ := newWebAgentTestApp(t)
	defer store.Close()
	defer app.cancel()
	path := app.staticAssets.fingerprints["app.js"]
	probe := httptest.NewRequest("GET", path, nil)
	session := addTestWebSession(t, app, probe)
	cookies := probe.Cookies()
	if len(cookies) != 1 {
		t.Fatal("test session cookie")
	}
	cookie := cookies[0]
	for _, tc := range []struct {
		name, host string
		cookie     *http.Cookie
		code       int
	}{
		{"missing", "probe.test", nil, 303},
		{"invalid", "probe.test", &http.Cookie{Name: cookie.Name, Value: "invalid"}, 303},
		{"wrong-domain", "attacker.test", cookie, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", path, nil)
			r.Host = tc.host
			if tc.cookie != nil {
				r.AddCookie(tc.cookie)
			}
			r.Header.Set("If-None-Match", app.staticAssets.immutable[path].etag)
			w := httptest.NewRecorder()
			app.Handler().ServeHTTP(w, r)
			if w.Code != tc.code || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("auth bypass: %d %v", w.Code, w.Header())
			}
		})
	}
	if w := webRequest(app, "GET", path, nil, cookie); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("valid session blocked")
	}
	for _, path := range []string{"/api/v1/web/session", "/api/v1/web/agents", "/api/v1/web/events"} {
		if w := webRequest(app, "GET", path, nil, cookie); w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("dynamic endpoint cached")
		}
	}
	_, _, login := webLoginPage(t, app)
	if login.Header().Get("Cache-Control") != "no-store" || len(webLoginTokenPattern.FindStringSubmatch(login.Body.String())) != 2 {
		t.Fatal("login form cached or missing CSRF")
	}
	style := webRequest(app, "GET", "/style.css", nil, nil)
	if style.Code != 200 || style.Header().Get("Cache-Control") != "public, no-cache" {
		t.Fatal("login style lost original target-only access")
	}
	r := httptest.NewRequest("GET", "/style.css", nil)
	r.Host = "attacker.test"
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("style domain gate weakened")
	}
	form := url.Values{"csrf_token": {session.csrfToken}}
	r = httptest.NewRequest("POST", "/logout", strings.NewReader(form.Encode()))
	r.Host = "probe.test"
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://probe.test")
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logout: %d", w.Code)
	}
	if after := webRequest(app, "GET", path, nil, cookie); after.Code != 303 || after.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("revoked session fetched fingerprint")
	}
	select {
	case <-session.done:
	default:
		t.Fatal("logout did not cancel session")
	}
}

// Requests remain pure lookups even if the source FS becomes unavailable.
type staticCountingFS struct {
	fs.FS
	opens  int
	closed bool
}

func (s *staticCountingFS) Open(name string) (fs.File, error) {
	s.opens++
	if s.closed {
		return nil, fs.ErrClosed
	}
	return s.FS.Open(name)
}

func TestStaticAssetsStartupOnlyAndPerAppIsolation(t *testing.T) {
	source := &staticCountingFS{FS: staticTestSource(t)}
	a := mustStaticAssets(t, source)
	initial := source.opens
	source.closed = true
	for i := 0; i < 3; i++ {
		for _, path := range []string{"/app.js", "/vendor/flag-icons/4x3/us.svg", a.fingerprints["app.js"]} {
			w := staticResponse(a, "GET", path, nil)
			if w.Code != 200 {
				t.Fatal("request attempted source read")
			}
			if staticResponse(a, "GET", path, map[string]string{"If-None-Match": w.Header().Get("ETag")}).Code != 304 {
				t.Fatal("revalidation failed")
			}
		}
	}
	if source.opens != initial {
		t.Fatal("request reopened source")
	}
	b := mustStaticAssets(t, staticTestSource(t))
	delete(a.immutable, a.fingerprints["app.js"])
	if staticResponse(b, "GET", b.fingerprints["app.js"], nil).Code != 200 {
		t.Fatal("Apps share mutable directory")
	}
	retained := 0
	for _, e := range b.legacy {
		retained += len(e.data)
	}
	for _, e := range b.immutable {
		if e.name == "card-layout.css" {
			retained += len(e.data)
		}
	}
	for path, e := range b.pages {
		if path != "/" {
			retained += len(e.data)
		}
	}
	t.Logf("startup directory: legacy=%d immutable=%d page aliases=%d retained bytes=%d", len(b.legacy), len(b.immutable), len(b.pages), retained)
	if len(b.legacy) != 318 || retained > 3<<20 {
		t.Fatal("unexpected resource footprint")
	}
}
