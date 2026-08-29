package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestAgentEnrollmentUIUsesShownOnceDOMFlow(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	htmlBytes, err := fs.ReadFile(static, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	jsBytes, err := fs.ReadFile(static, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	javascript := string(jsBytes)
	for _, required := range []string{
		`id="add-agent"`, `id="add-agent-dialog"`, `id="add-agent-form"`,
		`id="created-agent-id"`, `id="created-install-command"`, `id="created-enrollment"`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("dashboard missing enrollment UI %q", required)
		}
	}
	for _, required := range []string{
		`fetch('/api/v1/web/agents'`,
		`'X-CSRF-Token': mutationCSRFToken`,
		`createdEnrollment.textContent = enrollment.enrollment_value`,
		`createdEnrollment.textContent = ''`,
		`addAgentDialog.addEventListener('close', clearEnrollmentDialog)`,
	} {
		if !strings.Contains(javascript, required) {
			t.Fatalf("dashboard script missing enrollment behavior %q", required)
		}
	}
	for _, forbidden := range []string{"localStorage", "sessionStorage", "?enrollment=", "?token="} {
		if strings.Contains(javascript, forbidden) {
			t.Fatalf("dashboard persists or places credential in URL through %q", forbidden)
		}
	}
}
