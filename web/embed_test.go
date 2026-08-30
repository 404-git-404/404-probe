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

func TestAgentRevokeUIUsesExplicitPreservingFlow(t *testing.T) {
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
		`id="remove-agent-dialog"`, `id="remove-agent-form"`, `id="remove-agent-id"`,
		`历史 telemetry、Schedules、Probe Jobs/Results 会保留`, `不会远程卸载 Agent`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("dashboard missing revoke UI %q", required)
		}
	}
	for _, required := range []string{
		`/revoke`, `method: 'POST'`, `'X-CSRF-Token': mutationCSRFToken`, `body: '{}'`,
		`revokedAgentIDs.add(agentID)`, `agents.delete(agentID)`,
		`if (revokedAgentIDs.has(update.agent_id)) return`,
	} {
		if !strings.Contains(javascript, required) {
			t.Fatalf("dashboard script missing revoke behavior %q", required)
		}
	}
	for _, forbidden := range []string{"remote uninstall", "/api/v1/control/"} {
		if strings.Contains(javascript, forbidden) {
			t.Fatalf("dashboard revoke flow contains forbidden behavior %q", forbidden)
		}
	}
}

func TestAgentCardsUseCompactResponsiveLayout(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	jsBytes, err := fs.ReadFile(static, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	cssBytes, err := fs.ReadFile(static, "style.css")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(jsBytes)
	stylesheet := string(cssBytes)
	htmlBytes, err := fs.ReadFile(static, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	if !strings.Contains(html, `id="agents" class="grid agent-grid"`) {
		t.Fatal("dashboard Agent collection is missing its responsive grid hook")
	}
	for _, required := range []string{
		`card.className = 'card agent-card'`, `title.className = 'card-identity'`,
		`card.dataset.agentId = agent.agent_id`, `status.dataset.agentState = stateName`,
		`remove.dataset.agentId = agent.agent_id`,
		`line.className = 'meta-line'`, `element.title = value`,
		`element.setAttribute('aria-label', accessibleValue)`, `state.hostname || agent.name || '未知主机'`,
	} {
		if !strings.Contains(javascript, required) {
			t.Fatalf("dashboard script missing fixed-card behavior %q", required)
		}
	}
	for _, required := range []string{
		`.agent-grid{grid-template-columns:repeat(auto-fill,minmax(min(100%,280px),1fr))`,
		`header,main,footer{width:min(1440px,calc(100% - 32px))`,
		`.agent-card{height:386px`, `grid-template-rows:46px 116px 58px 58px 36px`,
		`grid-template-columns:repeat(3,minmax(0,1fr))`, `.agent-card .metric:last-child{grid-column:2/-1}`,
		`max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap`,
		`.agent-card .card-actions{align-items:center;flex-direction:row}`,
	} {
		if !strings.Contains(stylesheet, required) {
			t.Fatalf("dashboard stylesheet missing fixed-card rule %q", required)
		}
	}
	for _, forbidden := range []string{"resize:", "draggable", "localStorage", "dashboard-preference"} {
		if strings.Contains(stylesheet+javascript, forbidden) {
			t.Fatalf("dashboard fixed-card flow contains customization behavior %q", forbidden)
		}
	}
}
