const id = new URLSearchParams(location.search).get('id');
const empty = document.querySelector('#history-empty');
const outboundStatus = document.querySelector('#outbounds-status');
const outboundList = document.querySelector('#outbounds-list');
const agentRuntimeValues = document.querySelector('#agent-runtime-values');
const trafficReset = document.querySelector('#traffic-reset');
const trafficResetFeedback = document.querySelector('#traffic-reset-feedback');
const googleState = document.querySelector('#google-status-state');
const googleValues = document.querySelector('#google-status-values');
const googleChecked = document.querySelector('#google-status-checked');
const googleRerun = document.querySelector('#google-status-rerun');
const googleFeedback = document.querySelector('#google-status-feedback');
const securityState = document.querySelector('#security-state');
const securityWindow = document.querySelector('#security-window');
const securitySources = document.querySelector('#security-sources');
let mutationCSRFToken = '';
let currentAgent = null;
let switchingSelector = '';
let selectorFeedback = null;
const selectorOperations = new Map();
const selectorDrafts = new Map();
const expandedSelectors = new Set();
const selectorDraftKey = selector => `${id}\u0000${selector}`;

const selectorErrors = {
  selector_not_found: '本地 Selector 已不存在，请刷新后重试',
  choice_not_found: '本地选项已不存在，请刷新后重试',
  clash_api_unavailable: 'Agent 无法连接本地 Clash API',
  clash_api_not_detected: '未检测到 sing-box Clash API',
  clash_api_auth_required: '检测到 Clash API 鉴权；请移除 secret，404-probe 不读取密钥',
  switch_failed: 'Clash API 拒绝了切换',
  switch_verification_failed: '切换后的回读结果与目标不一致',
  selector_switch_pending: '该 Selector 已有等待或执行中的切换',
  agent_disabled: 'Agent 已暂停，无法执行切换',
  agent_revoked: 'Agent 已撤销，无法执行切换',
  agent_offline: 'Agent 离线，无法执行切换',
  outbounds_not_configured: 'Agent 未配置出站发现',
  outbounds_unavailable: 'Clash API 当前不可用',
  selector_choice_not_allowed: '目标已不在最新快照中，请刷新后重试',
};

function requestID() {
  return crypto.randomUUID().replaceAll('-', '');
}

async function readError(response) {
  try {
    const body = await response.json();
    return selectorErrors[body.error?.code] || body.error?.message || body.message || response.statusText;
  } catch (error) {
    return response.statusText;
  }
}

async function waitForSwitch(jobID) {
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    const job = await readJSON(`/api/v1/web/jobs/${encodeURIComponent(jobID)}`);
    if (['success', 'failed', 'expired'].includes(job.operation_status)) return job;
    await new Promise(resolve => setTimeout(resolve, 250));
  }
  throw new Error('切换仍在排队，可在 Probe Jobs 中查看结果');
}

function operationFeedback(operation) {
  if (!operation) return null;
  const choice = operation.config?.choice || '';
  switch (operation.operation_status) {
    case 'queued':
      return {message: `等待切换至 ${choice}`, error: false, pending: true};
    case 'running':
      return {message: `正在切换至 ${choice}`, error: false, pending: true};
    case 'expired':
      return {message: '切换任务已过期（可能因暂停或超时）', error: true, pending: false};
    case 'failed': {
      const category = operation.result?.error_category || 'switch_failed';
      return {message: selectorErrors[category] || operation.result?.error_message || category, error: true, pending: false};
    }
    case 'success': {
      const result = operation.result?.measurement?.selector_switch;
      if (!result) return {message: '切换成功', error: false, pending: false};
      return {message: result.changed ? `已切换到 ${result.current}` : `已经是 ${result.current}`, error: false, pending: false};
    }
    default:
      return null;
  }
}

async function loadSelectorOperations(encodedID) {
  try {
    const page = await readJSON(`/api/v1/web/jobs?agent_id=${encodedID}&probe_type=singbox_selector_switch&limit=20`);
    const details = await Promise.all((page.items || []).map(job => readJSON(`/api/v1/web/jobs/${encodeURIComponent(job.job_id)}`)));
    selectorOperations.clear();
    for (const operation of details) {
      const selector = operation.config?.selector;
      if (selector && !selectorOperations.has(selector)) selectorOperations.set(selector, operation);
    }
  } catch (error) {
    selectorOperations.clear();
  }
}

async function refreshAgentUntil(encodedID, selector, target) {
  let latest = currentAgent;
  for (let attempt = 0; attempt < 5; attempt++) {
    latest = await readJSON(`/api/v1/web/agents/${encodedID}`);
    const value = latest.outbounds?.selectors?.find(item => item.name === selector);
    if (value?.current === target) return {agent: latest, reflected: true};
    if (attempt < 4) await new Promise(resolve => setTimeout(resolve, 300));
  }
  return {agent: latest, reflected: false};
}

async function switchOutbound(selector, choice) {
  switchingSelector = selector;
  selectorFeedback = {selector, message: `正在切换至 ${choice}`, error: false};
  renderOutbounds(currentAgent);
  try {
    const encodedID = encodeURIComponent(id);
    const response = await fetch(`/api/v1/web/agents/${encodedID}/outbounds/switch`, {
      method: 'POST',
      cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken},
      body: JSON.stringify({request_id: requestID(), selector, choice}),
    });
    if (response.status === 401) {
      location.assign('/login');
      return;
    }
    if (!response.ok) throw new Error(await readError(response));
    const operation = await response.json();
    const job = await waitForSwitch(operation.job_id);
    selectorOperations.set(selector, job);
    if (job.operation_status === 'expired') {
      throw new Error('切换任务已过期（可能因暂停或超时）');
    }
    if (job.operation_status !== 'success' || !job.result?.success) {
      const category = job.result?.error_category || 'switch_failed';
      throw new Error(selectorErrors[category] || job.result?.error_message || category);
    }
    const result = job.result.measurement?.selector_switch;
    const target = result?.current || choice;
    const refreshed = await refreshAgentUntil(encodedID, selector, target);
    currentAgent = refreshed.agent;
	selectorDrafts.delete(selectorDraftKey(selector));
    switchingSelector = '';
    selectorFeedback = {
      selector,
      message: refreshed.reflected
        ? (result?.changed ? `已切换到 ${target}` : `已经是 ${target}`)
        : `切换成功，状态快照尚未刷新（目标 ${target}）`,
      error: false,
    };
    renderOutbounds(currentAgent);
  } catch (error) {
    switchingSelector = '';
    selectorFeedback = {selector, message: error.message || '切换失败', error: true};
    try {
      currentAgent = await readJSON(`/api/v1/web/agents/${encodeURIComponent(id)}`);
    } catch (refreshError) {
      // Keep the last trusted snapshot; a later discovery or page refresh can recover it.
    }
    renderOutbounds(currentAgent);
  }
}

function renderOutbounds(agent) {
  const focusedSelector = document.activeElement?.closest?.('details')?.querySelector('summary strong')?.textContent || '';
  const outbounds = agent?.outbounds;
  outboundList.replaceChildren();
  if (!outbounds || !outbounds.configured) {
    outboundStatus.textContent = '未配置出站发现';
    return;
  }
  const lastUpdated = outbounds.updated_at ? new Date(outbounds.updated_at).toLocaleString() : '尚无成功快照';
  const lastChecked = outbounds.checked_at ? new Date(outbounds.checked_at).toLocaleString() : '未知';
  if (agent.revoked) outboundStatus.textContent = `Agent 已撤销 · 最后更新：${lastUpdated}`;
  else if (agent.disabled_at) outboundStatus.textContent = `Agent 已暂停 · 最后更新：${lastUpdated}`;
  else if (outbounds.status === 'not_detected') outboundStatus.textContent = '未检测到 sing-box Clash API（默认 http://127.0.0.1:9090）';
  else if (outbounds.status === 'auth_required') outboundStatus.textContent = '检测到 Clash API 鉴权；请移除 secret，404-probe 不读取或保存密钥';
  else if (!outbounds.available) outboundStatus.textContent = `Clash API 不可用 · 上次成功：${lastUpdated}`;
  else if (outbounds.stale) outboundStatus.textContent = `状态已过期 · 最后检查：${lastChecked}`;
  else outboundStatus.textContent = switchingSelector ? `正在切换 ${switchingSelector}…` : `已连接 · 更新于 ${lastUpdated}`;
  if (outbounds.available && !switchingSelector) {
    outboundStatus.textContent += outbounds.order_source === 'config'
      ? ' · 按 sing-box 配置顺序'
      : ' · 名称回退排序（尚未加载配置顺序）';
  }
  for (const selector of outbounds.selectors || []) {
    const details = document.createElement('details');
    details.open = expandedSelectors.has(selector.name) || switchingSelector === selector.name || selectorFeedback?.selector === selector.name;
    const summary = document.createElement('summary');
    const name = document.createElement('strong');
    name.textContent = selector.name;
    const current = document.createElement('span');
    current.textContent = selector.current;
    summary.append(name, current);
    const controls = document.createElement('div');
    controls.className = 'selector-controls';
    const choices = document.createElement('select');
    choices.setAttribute('aria-label', `${selector.name} 目标出站`);
	const draftKey = selectorDraftKey(selector.name);
	const draft = selectorDrafts.get(draftKey);
	const availableChoices = selector.choices || [];
	if (draft && !availableChoices.includes(draft)) {
	  selectorDrafts.delete(draftKey);
	  selectorFeedback = {selector: selector.name, message: '先前选择已不在最新选项中，请重新选择', error: true};
	}
	const selectedChoice = draft && availableChoices.includes(draft) ? draft : selector.current;
    for (const choice of availableChoices) {
      const option = document.createElement('option');
      option.value = choice;
      option.textContent = choice === selector.current ? `${choice}（当前）` : choice;
      option.selected = choice === selectedChoice;
      choices.append(option);
    }
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = '切换';
    const operationStatus = document.createElement('span');
    operationStatus.className = 'selector-operation-status';
    const persistedFeedback = operationFeedback(selectorOperations.get(selector.name));
    const feedback = selectorFeedback?.selector === selector.name ? selectorFeedback : persistedFeedback;
    if (feedback) {
      operationStatus.textContent = feedback.message;
      operationStatus.classList.toggle('switch-error', feedback.error);
    }
    const blocked = !agent.online || !outbounds.available || outbounds.stale || agent.disabled_at || agent.revoked || !mutationCSRFToken || switchingSelector !== '' || persistedFeedback?.pending;
    const updateButton = () => {
      button.disabled = blocked || choices.value === selector.current;
    };
    choices.disabled = blocked;
	choices.addEventListener('change', () => { selectorDrafts.set(draftKey, choices.value); updateButton(); });
    button.addEventListener('click', () => switchOutbound(selector.name, choices.value));
    updateButton();
    controls.append(choices, button, operationStatus);
    details.append(summary, controls);
    details.addEventListener('toggle', () => {
      if (details.open) expandedSelectors.add(selector.name);
      else expandedSelectors.delete(selector.name);
    });
    outboundList.append(details);
  }
  if (!outboundList.children.length && outbounds.available) {
    outboundStatus.textContent += ' · 未发现 Selector';
  }
  if (focusedSelector) {
    queueMicrotask(() => {
      for (const details of outboundList.querySelectorAll('details')) {
        if (details.querySelector('summary strong')?.textContent === focusedSelector) {
          details.querySelector('select')?.focus({preventScroll: true});
          break;
        }
      }
    });
  }
}

function detailBytes(value) {
  let number = Number(value || 0);
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let index = 0;
  while (number >= 1024 && index < units.length - 1) { number /= 1024; index++; }
  return `${number.toFixed(index ? 1 : 0)} ${units[index]}`;
}

function detailDuration(value) {
  const seconds = Number(value || 0);
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor(seconds % 86400 / 3600);
  const minutes = Math.floor(seconds % 3600 / 60);
  return days ? `${days} 天 ${hours} 小时` : `${hours} 小时 ${minutes} 分`;
}

function renderAgentRuntime(agent) {
  const state = agent?.state;
  const traffic = agent?.traffic;
  agentRuntimeValues.replaceChildren();
  trafficReset.disabled = !mutationCSRFToken || !agent?.online || !state || Boolean(state.stale) || Boolean(agent?.disabled_at) || Boolean(agent?.revoked);
  const rows = [
    ['主机名', state?.hostname || '—'],
    ['系统', [state?.os, state?.arch].filter(Boolean).join(' · ') || '—'],
    ['Agent 版本', agent?.version || '—'],
    ['CPU / 核心', state ? `${Number(state.cpu_percent || 0).toFixed(1)}%${state.cpu_cores ? ` · ${state.cpu_cores} 核` : ''}` : '—'],
    ['CPU steal', state?.cpu_steal_percent == null ? '—' : `${Number(state.cpu_steal_percent).toFixed(1)}% · 等待宿主机 CPU`],
    ['Load 1 / 5 / 15', state ? `${Number(state.load1 || 0).toFixed(2)} / ${Number(state.load5 || 0).toFixed(2)} / ${Number(state.load15 || 0).toFixed(2)}` : '—'],
    ['RAM', state ? `${detailBytes(state.ram_used)} / ${detailBytes(state.ram_total)} · ${Number(state.ram_percent || 0).toFixed(1)}%` : '—'],
    ['Swap', state ? `${detailBytes(state.swap_used)} / ${detailBytes(state.swap_total)} · ${Number(state.swap_percent || 0).toFixed(1)}%` : '—'],
    ['磁盘', state ? `${detailBytes(state.disk_used)} / ${detailBytes(state.disk_total)} · ${Number(state.disk_percent || 0).toFixed(1)}%` : '—'],
    ['磁盘 I/O', state?.disk_busy_percent == null ? '—' : `最忙磁盘 ${Number(state.disk_busy_percent).toFixed(1)}% · 所选设备合计读 ${detailBytes(state.disk_read_rate)}/s · 写 ${detailBytes(state.disk_write_rate)}/s`],
    ['累计入站', traffic ? detailBytes(traffic.rx_total) : '—'],
    ['累计出站', traffic ? detailBytes(traffic.tx_total) : '—'],
    ['累计起点', traffic?.started_at ? new Date(traffic.started_at).toLocaleString() : '首次有效样本'],
    ['运行时长', state ? detailDuration(state.uptime) : '—'],
    ['最近上报', agent?.last_seen ? new Date(agent.last_seen).toLocaleString() : '从未上报'],
    ['样本时间', state?.collected_at ? new Date(state.collected_at).toLocaleString() : '—'],
  ];
  for (const [label, value] of rows) {
    const term = document.createElement('dt'); term.textContent = label;
    const description = document.createElement('dd'); description.textContent = value;
    agentRuntimeValues.append(term, description);
  }
}

trafficReset.addEventListener('click', async () => {
  if (trafficReset.disabled || !currentAgent) return;
  if (!confirm('只重新开始页面累计入站/出站统计；历史与套餐周期用量不会改变。继续？')) return;
  trafficReset.disabled = true;
  trafficResetFeedback.textContent = '正在记录新的累计起点…';
  try {
    const response = await fetch(`/api/v1/web/agents/${encodeURIComponent(id)}/traffic/reset`, {
      method: 'POST', cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken},
      body: JSON.stringify({request_id: requestID()}),
    });
    if (response.status === 401) { location.assign('/login'); return; }
    if (!response.ok) throw new Error(await readError(response));
    currentAgent = {...currentAgent, traffic: await response.json()};
    trafficResetFeedback.textContent = '累计统计已从当前有效样本重新开始；套餐周期用量未改变。';
  } catch (error) {
    trafficResetFeedback.textContent = error.message || '无法重新开始累计统计';
  }
  renderAgentRuntime(currentAgent);
});

function renderGoogleStatus(agent) {
  const google = agent?.google_status;
  googleValues.replaceChildren();
  googleRerun.disabled = !mutationCSRFToken || !agent?.online || Boolean(agent?.disabled_at) || agent?.revoked || !google?.supported || google?.pending;
  googleRerun.textContent = google?.pending ? '检测中…' : '重新检测';
  if (!google?.supported) {
    googleState.textContent = '当前 Agent 不支持检测';
    googleChecked.textContent = '';
    return;
  }
  const statuses = google.result ? [google.result.youtube?.status, google.result.search?.status, google.result.signin?.status, google.result.gemini?.status] : [];
  const unknown = statuses.filter(status => !status || status === 'unknown').length;
  googleState.textContent = google.pending ? '检测中…'
    : !google.result ? '尚未检测'
      : google.stale ? '旧结果 · 等待在线后重新核实'
        : unknown === statuses.length ? '检测失败'
        : unknown ? '部分结果未知'
          : '当前结果';
  if (!google.result) {
    googleValues.textContent = '尚未检测';
    googleChecked.textContent = '';
    return;
  }
  const rows = [
    ['YouTube', google.result.youtube?.status === 'cn' ? 'CN · 送中' : google.result.youtube?.status === 'not_cn' ? `${google.result.youtube.region || ''}${google.result.youtube.region ? ' · ' : ''}非 CN` : '未知'],
    ['Google Search', ({ok: '正常', challenge: '需验证', blocked: '受限'})[google.result.search?.status] || '未知'],
    ['Google Sign-in', ({reachable: '可达', challenge: '需验证', blocked: '受限'})[google.result.signin?.status] || '未知'],
    ['Gemini', google.result.gemini?.status === 'available' ? `可用${google.result.gemini.region ? ` · ${google.result.gemini.region}` : ''}` : google.result.gemini?.status === 'blocked' ? '受限' : '未知'],
  ];
  for (const [name, value] of rows) {
    const term = document.createElement('dt'); term.textContent = name;
    const description = document.createElement('dd'); description.textContent = value;
    googleValues.append(term, description);
  }
  googleChecked.textContent = google.checked_at ? `检测时间：${new Date(google.checked_at).toLocaleString()}` : '';
}

googleRerun.addEventListener('click', async () => {
  if (!currentAgent || !mutationCSRFToken || googleRerun.disabled) return;
  googleRerun.disabled = true;
  googleFeedback.textContent = '';
  try {
    const response = await fetch(`/api/v1/web/agents/${encodeURIComponent(id)}/google-status`, {
      method: 'POST', cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken}, body: '{}',
    });
    if (response.status === 401) { location.assign('/login'); return; }
    if (!response.ok) {
      let message = '检测请求失败';
      try { message = (await response.json()).error?.message || message; } catch (error) { /* keep fallback */ }
      throw new Error(message);
    }
    currentAgent = {...currentAgent, google_status: {...currentAgent.google_status, pending: true}};
    renderGoogleStatus(currentAgent);
  } catch (error) {
    googleFeedback.textContent = error.message || '检测请求失败';
    renderGoogleStatus(currentAgent);
  }
});

function renderSecurity(agent) {
	const security = agent?.security;
	securitySources.replaceChildren();
	if (!security?.supported) {
	  securityState.textContent = 'Unsupported（需要 v0.9 Agent）';
	  securityWindow.textContent = '';
	  return;
	}
	const labels = {unavailable: 'Setup required', no_data: '等待首次本地审计', complete: '完整', partial: '部分结果', failed: '采集失败'};
	securityState.textContent = `${labels[security.status] || security.status}${security.stale ? ' · stale' : ''}${security.reason ? ` · ${security.reason}` : ''}`;
	const batch = security.current;
	if (!batch) {
	  securityWindow.textContent = '';
	  return;
	}
	securityWindow.textContent = `实际审计窗口：${new Date(batch.window_start).toLocaleString()} – ${new Date(batch.window_end).toLocaleString()} · ${batch.total_events} 条观察 · ${batch.tracked_sources} 个来源${security.delivery_gap ? ` · 历史缺口（上次已接收 ${new Date(security.previous_collected_at).toLocaleString()}）` : ''}`;
	for (const source of batch.sources || []) {
	  const row = document.createElement('div'); row.className = 'security-source';
	  const identity = document.createElement('code'); identity.textContent = source.ip;
	  const behavior = document.createElement('strong'); behavior.textContent = (source.classifications || []).join(' + ');
	  const detail = document.createElement('span'); detail.textContent = `${source.count} 次 · ${new Date(source.first_seen).toLocaleString()} – ${new Date(source.last_seen).toLocaleString()}`;
	  row.append(identity, behavior, detail); securitySources.append(row);
	}
	if (!securitySources.children.length) securitySources.textContent = batch.status === 'complete' ? '本窗口未观察到匹配事件' : '没有可展示的完整来源数据';
}

function draw(canvas, points, key, color, format) {
  const ratio = devicePixelRatio || 1;
  const rect = canvas.getBoundingClientRect();
  canvas.width = Math.max(1, rect.width * ratio);
  canvas.height = Math.max(1, rect.height * ratio);
  const context = canvas.getContext('2d');
  context.scale(ratio, ratio);
  const width = rect.width;
  const height = rect.height;
  const padding = 30;
  context.clearRect(0, 0, width, height);
  const values = points.map(point => Number(point[key] || 0));
  const maximum = Math.max(1, ...values) * 1.1;
  context.strokeStyle = '#c8beb1';
  context.fillStyle = '#7d7368';
  context.font = '11px system-ui';
  for (let index = 0; index <= 4; index++) {
    const y = padding + (height - padding * 2) * index / 4;
    context.beginPath();
    context.moveTo(padding, y);
    context.lineTo(width - 8, y);
    context.stroke();
    context.fillText(format(maximum * (1 - index / 4)), 2, y + 4);
  }
  if (points.length < 2) return;
  context.strokeStyle = color;
  context.lineWidth = 2;
  context.beginPath();
  values.forEach((value, index) => {
    const x = padding + (width - padding - 10) * index / (values.length - 1);
    const y = padding + (height - padding * 2) * (1 - value / maximum);
    if (index) context.lineTo(x, y); else context.moveTo(x, y);
  });
  context.stroke();
}

const rate = value => {
  const units = ['B', 'K', 'M', 'G'];
  let number = value;
  let index = 0;
  while (number >= 1024 && index < units.length - 1) {
    number /= 1024;
    index++;
  }
  return `${number.toFixed(0)}${units[index]}`;
};

async function readJSON(path) {
  const response = await fetch(path, {cache: 'no-store'});
  if (response.status === 401) {
    location.assign('/login');
    throw new Error('authentication required');
  }
  if (!response.ok) throw new Error(response.statusText);
  return response.json();
}

async function load() {
  if (!id || !/^[0-9a-f]{32}$/.test(id)) {
    empty.textContent = '无效的 Agent ID';
    return;
  }
  try {
    const encodedID = encodeURIComponent(id);
    const [agent, history, session] = await Promise.all([
      readJSON(`/api/v1/web/agents/${encodedID}`),
      readJSON(`/api/v1/web/agents/${encodedID}/history?hours=24`),
      readJSON('/api/v1/web/session'),
      loadSelectorOperations(encodedID),
    ]);
    mutationCSRFToken = session.csrf_token || '';
    currentAgent = agent;
    const state = agent.state || {};
    document.querySelector('#title').textContent = `${state.hostname || agent.name || 'Agent'} · 历史`;
    renderAgentRuntime(agent);
    renderOutbounds(agent);
    renderGoogleStatus(agent);
	renderSecurity(agent);
    const points = history.points;
    empty.classList.toggle('hidden', points.length > 0);
    if (!points.length) {
      empty.textContent = '最近 24 小时暂无数据';
      return;
    }
    const render = () => {
      draw(document.querySelector('#cpu'), points, 'cpu', '#4f8b63', value => `${value.toFixed(0)}%`);
      draw(document.querySelector('#ram'), points, 'ram_percent', '#c47729', value => `${value.toFixed(0)}%`);
      draw(document.querySelector('#rx'), points, 'rx_rate', '#826247', rate);
      draw(document.querySelector('#tx'), points, 'tx_rate', '#b84f48', rate);
    };
    render();
    addEventListener('resize', render);
  } catch (error) {
    empty.textContent = '加载失败';
    outboundStatus.textContent = '出站状态加载失败';
    googleState.textContent = 'Google Status 加载失败';
	securityState.textContent = 'Security 数据加载失败';
  }
}

load();

const events = new EventSource('/api/v1/web/events');
events.addEventListener('agent', event => {
  const update = JSON.parse(event.data);
  if (!currentAgent || update.agent_id !== id) return;
  currentAgent = {...currentAgent, ...update};
  renderAgentRuntime(currentAgent);
  renderOutbounds(currentAgent);
  renderGoogleStatus(currentAgent);
	renderSecurity(currentAgent);
});
