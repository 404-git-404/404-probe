package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestAgentStateModuleIsEmbeddedBeforePageConsumers(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(static, "agent-state.js"); err != nil {
		t.Fatal(err)
	}
	for _, page := range []struct{ html, script string }{{"index.html", "app.js"}, {"history.html", "history.js"}} {
		content, err := fs.ReadFile(static, page.html)
		if err != nil {
			t.Fatal(err)
		}
		html := string(content)
		stateIndex := strings.Index(html, `<script src="/agent-state.js" defer>`)
		consumerIndex := strings.Index(html, `<script src="/`+page.script+`" defer>`)
		if stateIndex < 0 || consumerIndex < stateIndex {
			t.Fatalf("%s must load shared state before %s", page.html, page.script)
		}
	}
}

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
		`撤销后不能恢复原身份`, `历史记录、计划和任务会保留`, `Agent 程序不会被卸载`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("dashboard missing revoke UI %q", required)
		}
	}
	for _, required := range []string{
		`/revoke`, `method: 'POST'`, `'X-CSRF-Token': mutationCSRFToken`, `body: '{}'`,
		`agentState.beginDelete(agentID)`, `agentState.finishDelete(barrier, true)`,
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
		`agent.revoked?'revoked':agent.disabled_at?'paused'`,
		`online:'在线',offline:'离线',paused:'暂停',revoked:'已撤销',unknown:'状态未知'`,
		`stateAction.dataset.agentAction = agent.disabled_at ? 'enable' : 'disable'`,
		`stateAction.textContent = agent.disabled_at ? '恢复' : '暂停'`,
		`const metricsStale = Boolean(!agent.online || agent.disabled_at || state.stale)`,
		`Card.machineView(agent, observed, oldMetrics)`,
		`Card.trafficView(agent, {old, quotaDisplay: quotaDisplay(agent.plan)})`,
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
	cardCSS, err := fs.ReadFile(static, "card-layout.css")
	v7CSS, err2 := fs.ReadFile(static, "card-v7.css")
	if err != nil || err2 != nil || !strings.Contains(string(v7CSS), `.c7 .c7-foot1 .google-checks{display:flex;flex:0 1 auto;min-width:0`) || strings.Index(html, `href="/card-v7.css"`) <= strings.Index(html, `href="/style.css"`) {
		t.Fatal("current card service row needs its loaded single-column override")
	}
	for _, required := range []string{
		`card.className = 'card agent-card c7'`, `identity.className = 'c7-id'`,
		`card.dataset.agentId = agent.agent_id`, `status.dataset.agentState = stateName`,
		`remove.dataset.agentId = agent.agent_id`,
		`Card.machineFaces(document)`, `Card.trafficFaces(document`, `Card.createModule(`,
		`cardObservations.observe(agent)`, `for (const agent of list)`,
		`agentNameControl(agent, state)`,
		`googleStatusPanel(agent)`, `detailPanel.className = 'card-detail-panel'`,
		`detailToggle.textContent = '详情'`, `editHint.textContent = agent.plan ? '编辑套餐 ✎' : '添加套餐 ✎'`,
		`compactPrice(agent.plan)`,
	} {
		if !strings.Contains(javascript, required) {
			t.Fatalf("dashboard script missing fixed-card behavior %q", required)
		}
	}
	if strings.Contains(javascript, `CardMetrics.tile('disk-io'`) || strings.Contains(javascript, `CardMetrics.stamp(`) {
		t.Fatal("real cards must have three resource rings and no fabricated score")
	}
	if !strings.Contains(string(cardCSS), `repeat(4,minmax(0,1fr))`) {
		t.Fatal("card grid columns missing")
	}
	for _, required := range []string{`.c7-ring`, `.nq7-b-red`, `--c7-ring-ok:#2A8C7C`, `.c7-offline`, `.c7 .c7-mod[data-module=network]{height:164px}`} {
		if !strings.Contains(string(v7CSS), required) {
			t.Fatalf("card layout missing %q", required)
		}
	}
	for _, required := range []string{
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
		`utilizationTone(percent, 2, 10)`, `utilizationTone(percent, 60, 85)`,
		`state.cpu_steal_percent`, `state.disk_read_rate`, `state.disk_write_rate`, `state.disk_busy_percent`,
		`countryMark(agent.country_code, agent.country_source)`, `country_code_override: '#plan-country-code'`,
		`country-code/refresh`, `country_code_lookup_operation`, `升级 Agent 后可重新识别`,
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

func TestGoogleStatusUIShowsPageObservationStates(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	appBytes, _ := fs.ReadFile(static, "app.js")
	htmlBytes, _ := fs.ReadFile(static, "history.html")
	historyBytes, _ := fs.ReadFile(static, "history.js")
	jobsBytes, _ := fs.ReadFile(static, "jobs.js")
	youtubeBytes, _ := fs.ReadFile(static, "youtube-status.js")
	youtube := string(youtubeBytes)
	app, historyHTML, history := string(appBytes), string(htmlBytes), string(historyBytes)
	for _, marker := range []string{"地区 ", "未知", "尚未检测", "检测中", "过期", "重新检测", "/google-status", "未验证视频播放或 Premium 解锁"} {
		if !strings.Contains(app+youtube, marker) {
			t.Errorf("YouTube observation UI missing %q", marker)
		}
	}
	if !strings.Contains(historyHTML, "YouTube 检测") || !strings.Contains(history, "YouTubeStatus.view(agent)") || !strings.Contains(app, "YouTubeStatus.view(agent)") || !strings.Contains(historyHTML, `src="/youtube-status.js"`) || strings.Contains(app+history+youtube+string(jobsBytes), "SENT TO CHINA") || strings.Contains(app+history+youtube, "CN · 送中") {
		t.Fatal("history detail is missing complete Google Status rendering")
	}
	for _, removed := range []string{"Gemini", "gemini", "result.search", "result.signin", "value.search", "value.signin", "Google Search", "Google Sign-in", "['Search'", "['登录'"} {
		if strings.Contains(app+history+youtube+historyHTML+string(jobsBytes), removed) {
			t.Fatalf("current Google Status UI still renders removed service %q", removed)
		}
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
	selectorBytes, _ := fs.ReadFile(static, "selector.js")
	historyHTMLBytes, _ := fs.ReadFile(static, "history.html")
	history, historyHTML := string(historyBytes)+string(selectorBytes), string(historyHTMLBytes)
	for _, required := range []string{
		`groups: new Map()`, `g.draft = value; g.generation++`,
		`g.generation === operation.generation`, `g.draft = null`,
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
	selectorBytes, _ := fs.ReadFile(static, "selector.js")
	combined := string(htmlBytes) + string(jsBytes) + string(jobsBytes) + string(selectorBytes)
	for _, required := range []string{
		`id="outbounds-status"`, `id="outbounds-list"`, `未配置出站发现`, `当前不可用`, `未发现 Selector`,
		`panel = el('dialog', '', 'selector-dialog')`, `panel.showModal()`,
		`select.dataset.selectorKind='choice'`, `select.disabled=Boolean(blocked||m.active)`,
		`panel.addEventListener('change'`, `controller.draft(activeID,event.target.dataset.selectorName,event.target.value)`,
		`kind==='submit'`, `controller.submit(activeID,name,g.draft)`,
		`submit.dataset.selectorName=selector.name`, `el('button',m.active&&g.feedback?.pending?'正在切换…':'切换')`,
		`submit.disabled=Boolean(blocked||m.active||m.inspection||m.recoveryIncomplete||g.remotePending||!selector.choices?.includes(value)||value===selector.current)`,
		`el('h2','出站选择')`, `el('button','关闭')`, `document.createElementNS(svgNS,'svg')`, `icon.setAttribute('stroke','currentColor')`,
		`/outbounds/switch`, `method: 'POST'`, `'X-CSRF-Token': csrf()`,
		`body: JSON.stringify({request_id: operation.requestID, selector: name, choice})`,
		`choice === selector.current`, `agent.disabled_at`, `agent.revoked`, `m.active`,
		`job.result?.error_category`, `state.beginMutation(id, ['outbounds'])`,
		`state.finishMutation(mutation, {outbounds: latest.outbounds})`,
		`case 'singbox_selector_switch'`, `measurement.selector_switch`,
		`job.operation_status`, `m.intent === operation.intent`, `g.generation === operation.generation`, `attempt < 5`,
		`out.stale`, `状态已过期`, `切换任务已过期`, `selector_switch_pending`,
		`controller.signal`, `Promise.race`, `closeAll()`,
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

func TestSharedSelectorLoadsBeforeBothPagesWithLocalTheme(t *testing.T) {
	static, err := fs.Sub(Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []struct{ html, script string }{{"index.html", "app.js"}, {"history.html", "history.js"}} {
		content, _ := fs.ReadFile(static, page.html)
		js, _ := fs.ReadFile(static, page.script)
		html := string(content)
		state := strings.Index(html, `<script src="/agent-state.js" defer>`)
		selector := strings.Index(html, `<script src="/selector.js" defer>`)
		consumer := strings.Index(html, `<script src="/`+page.script+`" defer>`)
		if state < 0 || selector < state || consumer < selector {
			t.Fatalf("%s must load state, selector, then page", page.html)
		}
		for _, marker := range []string{"Selector.create(", "Selector.view(", "selectorView.trigger(agent)", "selectorController.recover(agentID, readJSON)", "selectorController.closeAll()"} {
			if !strings.Contains(string(js), marker) {
				t.Fatalf("%s missing shared Selector hook %q", page.script, marker)
			}
		}
		if strings.Contains(string(js), "/outbounds/switch") {
			t.Fatalf("%s duplicates the shared mutation algorithm", page.script)
		}
	}
	css, _ := fs.ReadFile(static, "card-v7.css")
	for _, marker := range []string{"dialog.selector-dialog{position:fixed;inset:0;margin:auto", "color-scheme:light", ".selector-dialog select", ".selector-dialog .selector-row{display:grid", ".selector-dialog button:disabled"} {
		if !strings.Contains(string(css), marker) {
			t.Fatalf("missing local Selector style %q", marker)
		}
	}
}
