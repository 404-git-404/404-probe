package web

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

func TestN3EmbeddedFixedLocalQualityDependencies(t *testing.T) {
	assets, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	for name, digest := range map[string]string{
		"vendor/uplot/uPlot.iife.min.js": "19c8d4c6ad88929a79f4ae49d6f7161566dfd0ba3d15cc495e974f787eb78f1f",
		"vendor/uplot/uPlot.min.css":     "df630c6a8d6f8eeaff264b50f73ce5b114f646ffd9a0bb74f049b0a00135fa04",
		"vendor/uplot/LICENSE":           "8f989229699b4fe2f1a0432d0e9edc338a8a911e250e2d1b01ecd770a5f5b1bd",
	} {
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != digest {
			t.Fatalf("%s fixed official SHA changed: %s", name, got)
		}
	}
	html, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	previous := -1
	for _, script := range []string{"agent-state.js", "management.js", "vendor/uplot/uPlot.iife.min.js", "network-quality-core.js", "network-quality.js", "app.js"} {
		index := strings.Index(string(html), `<script src="/`+script+`" defer>`)
		if index <= previous {
			t.Fatalf("script dependency order: %s", script)
		}
		previous = index
	}
	if strings.Count(string(html), `id="network-quality-history"`) != 1 {
		t.Fatal("must embed exactly one quality history dialog")
	}
	for _, name := range []string{"network-quality-core.js", "network-quality.js", "network-quality.css"} {
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"https://", "http://", "localStorage", "sessionStorage", "eval(", "new Function("} {
			if strings.Contains(string(data), forbidden) {
				t.Fatalf("%s runtime dependency/persistence: %s", name, forbidden)
			}
		}
	}
}
