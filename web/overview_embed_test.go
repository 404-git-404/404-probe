package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestOverviewEmbeddedStableNativeDetails(t *testing.T) {
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
	for _, id := range []string{"overview", "overview-summary", "overview-action", "overview-online", "overview-health", "overview-renewal", "overview-money", "dashboard-search", "dashboard-chips", "dashboard-sticky"} {
		if strings.Count(html, `id="`+id+`"`) != 1 {
			t.Fatalf("missing or duplicate %s", id)
		}
	}
	if !strings.Contains(html, `<details id="overview" class="overview" open>`) || strings.Count(html, `class="overview-group"`) != 3 {
		t.Fatal("native expanded details and exactly three groups required")
	}
	if strings.Index(html, `id="overview"`) > strings.Index(html, `id="agents"`) || strings.Index(html, `<script src="/overview.js" defer>`) > strings.Index(html, `<script src="/app.js" defer>`) {
		t.Fatal("stable overview must precede grid / actual app")
	}
}
