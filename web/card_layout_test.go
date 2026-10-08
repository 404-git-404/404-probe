package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestP4aLocalCardDependenciesAndIconsAreEmbedded(t *testing.T) {
	assets, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	html, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	if strings.Index(page, `<script src="/card-metrics.js" defer>`) < 0 || strings.Index(page, `<script src="/card-metrics.js" defer>`) > strings.Index(page, `<script src="/app.js" defer>`) {
		t.Fatal("card metrics must load before dashboard")
	}
	if !strings.Contains(page, `href="/card-layout.css"`) {
		t.Fatal("local card style missing")
	}
	for _, name := range []string{"card-metrics.js", "card-layout.css", "vendor/card-icons/debian.svg", "vendor/card-icons/alpine.svg", "vendor/card-icons/cpu.svg", "vendor/card-icons/memory.svg", "vendor/card-icons/disk.svg", "vendor/card-icons/io.svg", "vendor/card-icons/generic.svg", "vendor/card-icons/DEVICON-LICENSE.txt", "vendor/card-icons/HEROICONS-LICENSE.txt", "vendor/card-icons/DEBIAN-CC-BY-SA-3.0.txt", "vendor/card-icons/SIMPLE-ICONS-LICENSE.md", "vendor/card-icons/SOURCES.md"} {
		data, err := fs.ReadFile(assets, name)
		if err != nil || len(data) == 0 {
			t.Fatalf("missing local dependency %s: %v", name, err)
		}
	}
	app, _ := fs.ReadFile(assets, "app.js")
	if strings.Contains(string(app), "CardMetrics.stamp(") {
		t.Fatal("no approved score: production must not render synthetic stamp")
	}
}
