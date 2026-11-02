package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestPanelEmbeddedStableControls(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	page, err := fs.ReadFile(static, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, asset := range []string{"overview.js", "overview.css", "dashboard-data.js", "dashboard-model.js", "dashboard-view.js", "dashboard.css"} {
		data, err := fs.ReadFile(static, asset)
		if err != nil || len(data) == 0 || !strings.Contains(html, `"/`+asset+`"`) {
			t.Fatalf("missing local %s", asset)
		}
	}
	for _, id := range []string{"dashboard-panel", "panel-collapse", "panel-more", "overview-online", "overview-health", "overview-renewal", "overview-money", "dashboard-search", "dashboard-chips", "dashboard-toolbar"} {
		if strings.Count(html, `id="`+id+`"`) != 1 {
			t.Fatalf("missing or duplicate %s", id)
		}
	}
	if !strings.Contains(html, `<aside id="dashboard-panel"`) || strings.Count(html, `class="overview-group"`) != 3 {
		t.Fatal("panel and exactly three summary groups required")
	}
	if strings.Index(html, `id="dashboard-panel"`) > strings.Index(html, `id="agents"`) || strings.Index(html, `<script src="/overview.js" defer>`) > strings.Index(html, `<script src="/app.js" defer>`) {
		t.Fatal("stable overview must precede grid / actual app")
	}
	for _, dead := range []string{"dashboard-sticky", "dashboard-anchor", "dashboard-expand", `<details id="overview"`} {
		if strings.Contains(html, dead) {
			t.Fatalf("obsolete dashboard component %s", dead)
		}
	}
}
