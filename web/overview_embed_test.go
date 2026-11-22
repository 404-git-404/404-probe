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
	for _, asset := range []string{"overview.js", "overview.css", "dashboard-data.js", "dashboard-model.js", "dashboard-view.js", "dashboard.css", "settings.js", "settings.css"} {
		data, err := fs.ReadFile(static, asset)
		if err != nil || len(data) == 0 || !strings.Contains(html, `"/`+asset+`"`) {
			t.Fatalf("missing local %s", asset)
		}
	}
	for _, id := range []string{"dashboard-panel", "panel-collapse", "dashboard-devices", "dashboard-settings", "dashboard-network", "dashboard-groups", "dashboard-all-groups", "dashboard-countries", "dashboard-statuses", "dashboard-youtube-free", "overview-online", "overview-health", "overview-renewal", "overview-money", "dashboard-search", "dashboard-settings-content", "manage-tags", "dashboard-toolbar"} {
		if strings.Count(html, `id="`+id+`"`) != 1 {
			t.Fatalf("missing or duplicate %s", id)
		}
	}
	if !strings.Contains(html, `<aside id="dashboard-panel"`) || strings.Count(html, `class="overview-row panel-item"`) != 2 {
		t.Fatal("panel and two full-width summary filters required")
	}
	if strings.Index(html, `id="dashboard-panel"`) > strings.Index(html, `id="agents"`) || strings.Index(html, `<script src="/overview.js" defer>`) > strings.Index(html, `<script src="/app.js" defer>`) {
		t.Fatal("stable overview must precede grid / actual app")
	}
	for _, dead := range []string{"dashboard-sticky", "dashboard-anchor", "dashboard-expand", "dashboard-chips", "dashboard-quick", "panel-mobile-close", "/jobs.html", "/schedules.html", `href="/history.html"`, "overview-overdue"} {
		if strings.Contains(html, dead) {
			t.Fatalf("obsolete dashboard component %s", dead)
		}
	}
}
