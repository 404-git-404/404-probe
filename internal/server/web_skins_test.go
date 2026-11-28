package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSkinPaletteAndValues(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	palette := a.skinPalette
	if len(palette.allowed) <= 300 || !palette.allowed["--color-page"] || !palette.allowed["--color-text"] || !palette.allowed["--tag-blue-swatch"] {
		t.Fatal("unexpected embedded skin whitelist", len(palette.allowed))
	}
	for _, value := range []string{"#abc", "#abba", "#123456", "#12345678", "transparent", "#AABBCC", " RGB(1,2,3) ",
		"rgba(1, 2, 3, .4)", "rgb(10% 20% 30% / 40%)", "hsl(-20deg 40% 60%)", "hsla(300, 25%, 50%, .75)", "hsl(12 4% 50% / .4)"} {
		if _, ok := normalizeSkinColor(value); !ok {
			t.Errorf("valid color rejected: %q", value)
		}
	}
	for _, value := range []string{"url(https://probe.test/a)", "#abc;--x:0", "#abc}", "expression(abc)", "var(--color-page)",
		"", "  ", strings.Repeat(" ", 62) + "#abc", "#abcde", "rgb(1 2)", "rgba(1,2,3,)", "rgb(1 2 3 /)",
		"rgb(1deg 2 3)", "rgb(1%, 2, 3)", "hsl(20, 3, 4)", "rgb(1 2 3 / 10deg)", "rgb(1 2 3 / .5 / .6)", "hsl(1deg2deg 3% 4%)", "rgb(1 2 3)\n", "white"} {
		if _, ok := normalizeSkinColor(value); ok {
			t.Errorf("invalid color accepted: %q", value)
		}
	}
	skin, ignored, problems := palette.parseFile(exampleSkin)
	if len(problems) != 0 || len(ignored) != 0 || len(skin.Light) != 10 || len(skin.Dark) != 10 {
		t.Fatal(skin, ignored, problems)
	}
}

func TestSkinHTTPImportActivationAndCaching(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	css := func(tag, host string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", "/skin.css", nil)
		r.Host = host
		if tag != "" {
			r.Header.Set("If-None-Match", tag)
		}
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w
	}
	base := css("", "probe.test")
	if base.Code != 200 || base.Body.Len() != 0 || base.Header().Get("Cache-Control") != "no-cache" ||
		base.Header().Get("ETag") == "" || !strings.HasPrefix(base.Header().Get("Content-Type"), "text/css") {
		t.Fatal(base.Result().Status, base.Header(), base.Body.String())
	}
	if w := css("", "wrong.test"); w.Code != http.StatusForbidden {
		t.Fatal("wrong Host", w.Code)
	}
	for _, route := range []string{skinsPath, skinsPath + "/example", skinsPath + "/" + strings.Repeat("a", 32) + "/download"} {
		r := httptest.NewRequest("GET", route, nil)
		r.Host = "probe.test"
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatal(route, w.Code)
		}
	}
	for _, route := range []struct{ method, path string }{
		{"POST", skinsPath}, {"PUT", skinsPath + "/active"}, {"DELETE", skinsPath + "/default"},
	} {
		r := httptest.NewRequest(route.method, route.path, nil)
		addTestWebSession(t, a, r)
		r.Header.Set("Origin", "https://probe.test")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatal(route, w.Code)
		}
	}
	list := func() (string, []skinSummary) {
		t.Helper()
		var got struct {
			Active string `json:"active_id"`
			Skins  []skinSummary
		}
		checkS1WebJSON(t, a, "GET", skinsPath, "", 200, &got)
		return got.Active, got.Skins
	}
	if id, skins := list(); id != "default" || len(skins) != 1 || !skins[0].Builtin || skins[0].Name != "默认" ||
		len(skins[0].Swatches.Light) != 5 || len(skins[0].Swatches.Dark) != 5 {
		t.Fatal(id, skins)
	}
	w := qualityWebRequest(t, a, "GET", skinsPath+"/example", "")
	var sample skinFile
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &sample) != nil || sample.Name != "示例 · 冷灰蓝" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	var imported struct {
		Skin    skinSummary
		Ignored []string
	}
	valid := `{"format":"404-probe-skin","version":1,"name":"冷蓝","note":42,"light":{"--color-page":" #ABC ","--unknown-light":"url(ignored)"},"dark":{"--color-page":"hsl(28deg 20% 30% / .5)","--unknown-light":0}}`
	checkS1WebJSON(t, a, "POST", skinsPath, valid, 201, &imported)
	if len(imported.Ignored) != 1 || imported.Ignored[0] != "--unknown-light" ||
		imported.Skin.Swatches.Light[0] != "#abc" || imported.Skin.Swatches.Light[1] != a.skinPalette.light["--color-card"] ||
		imported.Skin.Swatches.Dark[1] != a.skinPalette.dark["--color-card"] {
		t.Fatal(imported)
	}
	id := imported.Skin.ID
	var failure struct {
		Error struct {
			Code    string
			Details []skinImportError
		}
	}
	for _, invalid := range []struct{ body, reason string }{
		{`{"format":"404-probe-skin","version":1,"name":"Bad","light":{"--x":"#abc"}}`, "no_valid_variables"},
		{`{"format":"404-probe-skin","version":1,"name":"Bad","light":{"--color-page":"#abc"},"dark":{"--color-page":"var(--color-page)"}}`, "invalid_value"},
		{strings.Repeat(" ", skinFileLimit+1), "invalid_size"},
	} {
		checkS1WebJSON(t, a, "POST", skinsPath, invalid.body, 400, &failure)
		if failure.Error.Code != "invalid_skin" || len(failure.Error.Details) != 1 || failure.Error.Details[0].Reason != invalid.reason {
			t.Fatal(failure)
		}
	}
	w = qualityWebRequest(t, a, "GET", skinsPath+"/"+id+"/download", "")
	var exported skinFile
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") ||
		json.Unmarshal(w.Body.Bytes(), &exported) != nil || len(exported.Light) != 1 || exported.Light["--color-page"] != "#abc" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	checkS1WebJSON(t, a, "PUT", skinsPath+"/active", `{"id":"`+id+`"}`, 204, nil)
	active := css(base.Header().Get("ETag"), "probe.test")
	tag := active.Header().Get("ETag")
	const want = ":root:not([data-theme=dark]){--color-page:#abc;}\n:root[data-theme=dark]{--color-page:hsl(28deg 20% 30% / .5);}\n"
	if active.Code != 200 || tag == "" || tag == base.Header().Get("ETag") || active.Body.String() != want {
		t.Fatal(active.Code, active.Header(), active.Body.String())
	}
	if w := css(tag, "probe.test"); w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("ETag") != tag {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	checkS1WebJSON(t, a, "DELETE", skinsPath+"/default", "", 400, nil)
	for range 19 {
		checkS1WebJSON(t, a, "POST", skinsPath, valid, 201, nil)
	}
	var limit v4ErrorBody
	checkS1WebJSON(t, a, "POST", skinsPath, valid, 409, &limit)
	if limit.Error.Code != "skin_limit" {
		t.Fatal(limit)
	}
	checkS1WebJSON(t, a, "DELETE", skinsPath+"/"+id, "", 204, nil)
	if w := css(tag, "probe.test"); w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("ETag") != base.Header().Get("ETag") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	if active, skins := list(); active != "default" || len(skins) != 20 {
		t.Fatal(active, len(skins))
	}
}
