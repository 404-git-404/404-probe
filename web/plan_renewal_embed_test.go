package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestPlanRenewalEmbeddedLocalAssetsAndNativeDialog(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	page, err := fs.ReadFile(static, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, asset := range []string{"plan-renewal.js", "plan-renewal.css"} {
		data, err := fs.ReadFile(static, asset)
		if err != nil || len(data) == 0 || !strings.Contains(html, `"/`+asset+`"`) {
			t.Fatalf("missing local %s", asset)
		}
	}
	if !strings.Contains(html, `<dialog id="renewal-dialog"`) {
		t.Fatal("renewal must use native dialog")
	}
	for _, id := range []string{"dialog", "title", "name", "cancel", "dismiss", "field", "confirm", "from", "to", "dates", "detail", "anchor", "warning", "error", "loading", "result", "refresh", "edit", "undo", "undo-note", "recovery", "request-id", "copy", "verify"} {
		if strings.Count(html, `id="renewal-`+id+`"`) != 1 {
			t.Fatalf("missing or duplicate renewal-%s", id)
		}
	}
	if strings.Index(html, `<script src="/plan-renewal.js" defer>`) > strings.Index(html, `<script src="/app.js" defer>`) {
		t.Fatal("module must precede actual app")
	}
	css, _ := fs.ReadFile(static, "plan-renewal.css")
	for _, rule := range []string{"position:fixed", "flex-shrink:0", "max-height:30dvh", "overflow-wrap:anywhere", "height:100dvh", "[hidden]"} {
		if !strings.Contains(string(css), rule) {
			t.Fatalf("missing scoped rule %s", rule)
		}
	}
}
