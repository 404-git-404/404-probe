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
		`const metricsStale = Boolean(!agent.online || agent.disabled_at || state.stale)`,
		`metricTile('cpu', '◉', 'CPU'`,
		`['下载', '↓', state.rx_rate, state.rx_total]`, `metricsStale ? '—' : bytes(rate, true)`,
		"/${action}`", `method: 'POST'`, `'X-CSRF-Token': mutationCSRFToken`,
		`if (!agent.revoked) management.append(upgrade, stateAction)`,
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

func TestAgentCardsUseReadableResponsiveVPSLayout(t *testing.T) {
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
		`metricTile('cpu'`, `metricTile('memory'`, `metricTile('disk'`, `metricTile('disk-io'`,
		`note.title = detail`, `usedPercent >= 100 ? 'critical' : usedPercent >= 80 ? 'warning' : 'healthy'`,
		`cpuStealRow(state, metricsStale)`, `for (const agent of list)`,
		`setReadableText(heading, agent.name || state.hostname || '未命名 VPS')`,
		`googleStatusPanel(agent)`, `detailPanel.className = 'card-detail-panel'`,
		`detailToggle.textContent = '详情'`, `editPlan.textContent = agent.plan ? '编辑套餐' : '添加套餐'`,
		`compactPrice(agent.plan)`, `compactBandwidth(agent.plan)`,
	} {
		if !strings.Contains(javascript, required) {
			t.Fatalf("dashboard script missing fixed-card behavior %q", required)
		}
	}
	for _, required := range []string{
		`@media(min-width:821px){.agent-grid{grid-template-columns:repeat(4,minmax(0,1fr))`,
		`header,main,footer{width:min(1760px,calc(100% - 24px))`,
		`.agent-card{min-width:0;overflow:hidden;display:flex;flex-direction:column`,
		`.google-checks{display:grid;grid-template-columns:1fr 1fr`,
		`.agent-card .card-actions{display:flex`, `white-space:nowrap`,
		`.agent-card h2{font-size:15px`, `white-space:nowrap;overflow:hidden;text-overflow:ellipsis`,
		`.plan-drawer{position:fixed`, `@media(max-width:700px)`,
		`.resource-grid{grid-template-columns:repeat(2,minmax(0,1fr))`,
		`backdrop-filter:blur(16px)`,
		`.progress-track::after{content:""`,
	} {
		if !strings.Contains(stylesheet, required) {
			t.Fatalf("dashboard stylesheet missing fixed-card rule %q", required)
		}
	}
	for _, forbidden := range []string{`.agent-card{height:490px`, `min-height:490px`, `grid-template-rows:46px 116px 58px 88px 70px 36px`} {
		if strings.Contains(stylesheet, forbidden) {
			t.Fatalf("dashboard stylesheet retains fixed-card layout %q", forbidden)
		}
	}
	for _, forbidden := range []string{"resize:", "draggable", "localStorage", "dashboard-preference"} {
		if strings.Contains(stylesheet+javascript, forbidden) {
			t.Fatalf("dashboard fixed-card flow contains customization behavior %q", forbidden)
		}
	}
}

func TestDashboardContinuousListAndTelemetryStatusContracts(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	appBytes, _ := fs.ReadFile(static, "app.js")
	indexBytes, _ := fs.ReadFile(static, "index.html")
	flagBytes, flagErr := fs.ReadFile(static, "vendor/flag-icons/4x3/us.svg")
	licenseBytes, licenseErr := fs.ReadFile(static, "vendor/flag-icons/LICENSE")
	app, html := string(appBytes), string(indexBytes)
	for _, marker := range []string{
		`for (const agent of list)`,
		`Number(rate) * 8 / Number(plan.bandwidth_bps) * 100`,
		`utilizationTone(percent, 2, 10)`, `utilizationTone(percent, 60, 85)`,
		`state.cpu_steal_percent`, `state.disk_read_rate`, `state.disk_write_rate`, `state.disk_busy_percent`,
		`countryMark(agent.country_code, agent.country_source)`, `country_code_override: '#plan-country-code'`,
		`/vendor/flag-icons/4x3/${normalized.toLowerCase()}.svg`,
		`.focus({preventScroll: true})`,
	} {
		if !strings.Contains(app, marker) {
			t.Errorf("dashboard missing continuous-list/telemetry contract %q", marker)
		}
	}
	if flagErr != nil || len(flagBytes) == 0 || licenseErr != nil || !strings.Contains(string(licenseBytes), "MIT License") {
		t.Errorf("bundled local flag asset/license missing: flag=%v license=%v", flagErr, licenseErr)
	}
	for _, marker := range []string{`status-legend`, `id="plan-country-code"`} {
		if !strings.Contains(html, marker) {
			t.Errorf("dashboard HTML missing %q", marker)
		}
	}
	for _, forbidden := range []string{`agentsPerPage`, `currentAgentPage`, `list.slice(pageStart`, `id="agent-pagination"`} {
		if strings.Contains(app+html, forbidden) {
			t.Errorf("dashboard still contains removed UI pagination %q", forbidden)
		}
	}
}

func TestGoogleStatusUIShowsAllCanonicalStates(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	appBytes, _ := fs.ReadFile(static, "app.js")
	htmlBytes, _ := fs.ReadFile(static, "history.html")
	historyBytes, _ := fs.ReadFile(static, "history.js")
	jobsBytes, _ := fs.ReadFile(static, "jobs.js")
	app, historyHTML, history := string(appBytes), string(htmlBytes), string(historyBytes)
	for _, marker := range []string{"CN · 送中", "非 CN", "正常", "需验证", "受限", "可达", "可用", "未知", "尚未检测", "检测中", "旧结果", "重新检测", "/google-status"} {
		if !strings.Contains(app, marker) {
			t.Errorf("app.js missing %q", marker)
		}
	}
	if !strings.Contains(historyHTML, "Google / YouTube 检测") || !strings.Contains(history, "检测时间：") || !strings.Contains(history, "CN · 送中") || strings.Contains(app+history+string(jobsBytes), "SENT TO CHINA") {
		t.Fatal("history detail is missing complete Google Status rendering")
	}
	for _, marker := range []string{"Agent 详情", "Load 1 / 5 / 15", "Swap", "Agent 版本", "运行时长", "最近上报", "样本时间"} {
		if !strings.Contains(historyHTML+history, marker) {
			t.Errorf("history detail missing %q", marker)
		}
	}
}

func TestSelectorDraftsSurviveRefreshAndSecurityUIIsPresent(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	historyBytes, _ := fs.ReadFile(static, "history.js")
	historyHTMLBytes, _ := fs.ReadFile(static, "history.html")
	history, historyHTML := string(historyBytes), string(historyHTMLBytes)
	for _, required := range []string{
		`const selectorDrafts = new Map()`,
		"const selectorDraftKey = selector => `${id}\\u0000${selector}`",
		`selectorDrafts.set(draftKey, choices.value)`,
		`selectorDrafts.delete(selectorDraftKey(selector))`,
		`先前选择已不在最新选项中，请重新选择`,
		`renderSecurity(currentAgent)`,
		`security.delivery_gap`,
	} {
		if !strings.Contains(history, required) {
			t.Fatalf("history UI missing selector/Security state contract %q", required)
		}
	}
	for _, required := range []string{`class="security-detail"`, `id="security-state"`, `id="security-sources"`} {
		if !strings.Contains(historyHTML, required) {
			t.Fatalf("history page missing Security detail contract %q", required)
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
