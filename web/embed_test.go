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
		`id="created-install-command"`, `id="created-enrollment"`,
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
		`id="remove-agent-dialog"`, `id="remove-agent-form"`, `id="remove-agent-name"`,
		`永久撤销 Agent credential`, `历史 telemetry、Schedules、Probe Jobs/Results 会保留`, `不会远程卸载 Agent`,
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

func TestOrdinaryUIHidesAgentIDs(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	indexBytes, err := fs.ReadFile(static, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	appBytes, err := fs.ReadFile(static, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	schedulesBytes, err := fs.ReadFile(static, "schedules.js")
	if err != nil {
		t.Fatal(err)
	}
	jobsBytes, err := fs.ReadFile(static, "jobs.js")
	if err != nil {
		t.Fatal(err)
	}
	index, app := string(indexBytes), string(appBytes)
	if strings.Contains(index, "Agent ID") || strings.Contains(index, `id="created-agent-id"`) || strings.Contains(index, `id="remove-agent-id"`) {
		t.Fatalf("dashboard exposes Agent ID controls: %s", index)
	}
	for _, forbidden := range []string{"agent.name || agent.agent_id", "createdAgentID", "removeAgentID"} {
		if strings.Contains(app, forbidden) {
			t.Fatalf("dashboard visibly falls back to Agent ID through %q", forbidden)
		}
	}
	if strings.Contains(string(schedulesBytes), "addLine(details, 'Agent', schedule.agent_id)") ||
		strings.Contains(string(jobsBytes), "addLine(summary, 'Agent', job.agent_id)") {
		t.Fatal("ordinary schedule or job cards expose Agent IDs")
	}
}

func TestAgentPauseResumeUIUsesDistinctNonDestructiveFlow(t *testing.T) {
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
	for _, required := range []string{
		`agent.revoked ? 'revoked' : agent.disabled_at ? 'paused'`,
		`agent.disabled_at ? '● PAUSED'`,
		`stateAction.dataset.agentAction = agent.disabled_at ? 'enable' : 'disable'`,
		`stateAction.textContent = agent.disabled_at ? '恢复' : '暂停'`,
		`const metricsStale = Boolean(agent.disabled_at || state.stale)`,
		`metric('CPU', metricsStale ? '—' : pct(state.cpu_percent))`,
		`['↓', metricsStale ? 0 : state.rx_rate, state.rx_total]`,
		"/${action}`", `method: 'POST'`, `'X-CSRF-Token': mutationCSRFToken`,
		`if (!agent.revoked) management.append(stateAction)`,
	} {
		if !strings.Contains(javascript, required) {
			t.Fatalf("dashboard script missing pause/resume behavior %q", required)
		}
	}
	for _, required := range []string{`.paused{color:#ffd58a}`, `.agent-state-action{`, `.agent-actions{`} {
		if !strings.Contains(stylesheet, required) {
			t.Fatalf("dashboard stylesheet missing paused state %q", required)
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

func TestOutboundSelectorUIUsesControlledMutationAndShowsAllStates(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	htmlBytes, _ := fs.ReadFile(static, "history.html")
	jsBytes, _ := fs.ReadFile(static, "history.js")
	jobsBytes, _ := fs.ReadFile(static, "jobs.js")
	combined := string(htmlBytes) + string(jsBytes) + string(jobsBytes)
	for _, required := range []string{
		`id="outbounds-status"`, `id="outbounds-list"`, `未配置出站发现`, `当前不可用`, `未发现 Selector`,
		`document.createElement('details')`, `document.createElement('select')`, `button.textContent = '切换'`,
		`/outbounds/switch`, `method: 'POST'`, `'X-CSRF-Token': mutationCSRFToken`,
		`body: JSON.stringify({request_id: requestID(), selector, choice})`,
		`choices.value === selector.current`, `agent.disabled_at`, `agent.revoked`, `switchingSelector !== ''`,
		`job.result?.error_category`, `currentAgent = await readJSON`,
		`case 'singbox_selector_switch'`, `measurement.selector_switch`,
		`operation.operation_status`, `selectorOperations`, `refreshAgentUntil`, `attempt < 5`,
		`outbounds.stale`, `状态已过期`, `切换任务已过期`, `selector_switch_pending`,
		`未检测到 sing-box Clash API`, `请移除 secret`, `new EventSource('/api/v1/web/events')`,
	} {
		if !strings.Contains(combined, required) {
			t.Fatalf("outbound UI missing %q", required)
		}
	}
	for _, forbidden := range []string{"PUT /proxies", "method: 'PUT'", "/api/v1/control/", "clash_api_url", "secret-input", "clash-secret"} {
		if strings.Contains(strings.ToLower(combined), strings.ToLower(forbidden)) {
			t.Fatalf("outbound UI contains mutation behavior %q", forbidden)
		}
	}
}
