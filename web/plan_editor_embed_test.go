package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestPlanEditorEmbeddedAssetsAndRetainedFormFields(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	page, err := fs.ReadFile(static, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, asset := range []string{"plan-editor.js", "plan-editor.css"} {
		data, err := fs.ReadFile(static, asset)
		if err != nil || len(data) == 0 {
			t.Fatalf("missing embedded %s: %v", asset, err)
		}
		if !strings.Contains(html, `"/`+asset+`"`) {
			t.Fatalf("missing local dependency %s", asset)
		}
	}
	if strings.Index(html, `<script src="/plan-editor.js" defer>`) > strings.Index(html, `<script src="/app.js" defer>`) {
		t.Fatal("editor must load before app")
	}
	for _, id := range []string{
		"plan-dialog", "plan-form", "plan-save", "plan-cancel", "plan-dismiss", "plan-clear", "plan-error",
		"plan-traffic-enabled", "plan-traffic-mode", "plan-quota-value", "plan-quota-unit", "plan-cycle-kind", "plan-cycle-count", "plan-cycle-anchor",
		"plan-timezone", "plan-calibration-value", "plan-calibration-unit", "plan-bandwidth-value", "plan-bandwidth-unit", "plan-currency",
		"plan-purchase-price", "plan-purchase-period", "plan-renewal-price", "plan-renewal-period", "plan-purchase-date", "plan-renewal-date", "plan-expiry-date", "plan-country-code",
		"plan-confirmation", "plan-continue", "plan-discard",
	} {
		if strings.Count(html, `id="`+id+`"`) != 1 {
			t.Fatalf("field %s must occur exactly once", id)
		}
	}
}

func TestPlanEditorFailureFeedbackOutsideScrollableBody(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	page, err := fs.ReadFile(static, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	confirmation := strings.Index(html, `<section id="plan-confirmation"`)
	errorPosition := strings.Index(html, `<div id="plan-error"`)
	actions := strings.Index(html[errorPosition:], `<div class="drawer-actions"`)
	if confirmation < 0 || errorPosition < confirmation || actions < 0 {
		t.Fatal("plan error must be a fixed form sibling between confirmation and footer")
	}
	css, err := fs.ReadFile(static, "plan-editor.css")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(css), "#plan-dialog #plan-error {")
	if start < 0 {
		t.Fatal("missing scoped failure feedback")
	}
	rule := strings.SplitN(string(css)[start:], "}", 2)[0]
	for _, required := range []string{"flex-shrink:0", "max-height:min(120px,20dvh)", "overflow:auto", "overflow-wrap:anywhere"} {
		if !strings.Contains(rule, required) {
			t.Fatalf("fixed feedback missing %s", required)
		}
	}
}

func TestPlanTrafficInputDecimalOnlyAndExactAdapterEmbedded(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	page, err := fs.ReadFile(static, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, id := range []string{"plan-quota-unit", "plan-calibration-unit"} {
		start := strings.Index(html, `id="`+id+`"`)
		if start < 0 {
			t.Fatal("missing decimal unit select")
		}
		options := strings.SplitN(html[start:], "</select>", 2)[0]
		if !strings.Contains(options, "<option>GB</option>") || !strings.Contains(options, "<option>TB</option>") || strings.Contains(options, "GiB") || strings.Contains(options, "TiB") {
			t.Fatalf("%s must only offer decimal GB/TB", id)
		}
	}
	adapter := strings.Index(html, `<script src="/plan-units.js" defer>`)
	if adapter < 0 || adapter > strings.Index(html, `<script src="/app.js" defer>`) {
		t.Fatal("exact adapter must load before app")
	}
	if data, err := fs.ReadFile(static, "plan-units.js"); err != nil || len(data) == 0 {
		t.Fatalf("adapter not embedded: %v", err)
	}
}
