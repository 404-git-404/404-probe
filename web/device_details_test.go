package web

import (
	"io/fs"
	"strings"
	"testing"
)

// Embed checks complement the actual-module Node/browser lifecycle tests;
// they do not stand in for transport or UI behavior acceptance.
func TestDeviceDetailsEmbeddedAssets(t *testing.T) {
	assets, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	html, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	previous := -1
	for _, name := range []string{"agent-state.js", "network-quality-core.js", "network-quality.js", "resource-history.js", "device-details.js", "app.js"} {
		index := strings.Index(string(html), `<script src="/`+name+`" defer>`)
		if index <= previous {
			t.Fatalf("missing or unordered embed script %s", name)
		}
		previous = index
	}
	if !strings.Contains(string(html), `href="/device-details.css"`) || !strings.Contains(string(html), `id="device-details"`) {
		t.Fatal("details dialog or stylesheet not embedded")
	}
	for _, name := range []string{"resource-history.js", "device-details.js", "device-details.css"} {
		data, err := fs.ReadFile(assets, name)
		if err != nil || len(data) == 0 {
			t.Fatalf("asset %s empty or missing: %v", name, err)
		}
		for _, banned := range []string{"https://", "http://", "localStorage", "sessionStorage", "eval(", "new Function(", "setInterval(", "innerHTML"} {
			if strings.Contains(string(data), banned) {
				t.Fatalf("%s unexpected dependency, persistence, timer or HTML injection: %s", name, banned)
			}
		}
	}
	for _, name := range []string{"history.html", "history.js"} {
		if _, err := fs.ReadFile(assets, name); err != nil {
			t.Fatalf("legacy compatibility asset missing: %s", name)
		}
	}
}
