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
const agentState = AgentState.create();
let loadController = null;
let pendingTelemetry = null;
let trafficRequest = null;
let trafficWriteController = null;
const selectorErrors = Selector.errors;
function requestID() { return crypto.randomUUID().replaceAll('-', ''); }
async function readError(response) {
  try { const body = await response.json(); return selectorErrors[body.error?.code] || body.error?.message || response.statusText; }
  catch (error) { return response.statusText; }
}
const selectorController = Selector.create({
  state: agentState, fetcher: (...args) => fetch(...args), csrf: () => mutationCSRFToken,
  unauthorized: () => location.assign('/login'),
  onchange: () => { currentAgent = agentState.agents.get(id); selectorView.refresh(id); },
});
const selectorView = Selector.view({
  controller: selectorController, state: agentState, csrf: () => mutationCSRFToken,
  onopen: agentID => { reconciliation.notify(agentID); selectorController.recover(agentID, readJSON); },
});
async function switchOutbound(selector, choice) {
  const result = await selectorController.submit(id, selector, choice);
  currentAgent = agentState.agents.get(id);
  reconciliation.notify(id);
  return result;
}
function renderOutbounds(agent) {
  const restoreTriggerFocus = document.activeElement?.dataset?.focusKey === 'selector';
  outboundList.replaceChildren();
  const trigger = agent && selectorView.trigger(agent);
  if (trigger) outboundList.append(trigger);
  outboundStatus.textContent = agent?.outbounds?.configured
    ? Selector.reason(agent, mutationCSRFToken) || '点击查看各组当前出站'
    : '未配置出站发现';
  selectorView.refresh(id);
  if (restoreTriggerFocus && trigger) trigger.focus({preventScroll: true});
}
function detailBytes(value, binary = true) {
  let number = Number(value || 0);
  const units = binary ? ['B', 'KiB', 'MiB', 'GiB', 'TiB'] : ['B', 'KB', 'MB', 'GB', 'TB'];
  const base = binary ? 1024 : 1000;
  let index = 0;
  while (number >= base && index < units.length - 1) { number /= base; index++; }
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
  trafficReset.disabled = !mutationCSRFToken || !agent?.online || !state || !traffic || Boolean(state.stale) || Boolean(agent?.disabled_at) || Boolean(agent?.revoked) || agentState.busy(id, 'traffic') || agentState.blocked(id);
  trafficReset.textContent = trafficRequest ? '用原请求核实累计起点' : '重新开始累计统计';
  trafficReset.title = !agent?.online ? '设备离线，不能重新起算' : agent?.disabled_at ? '设备已暂停' : !state || state.stale || !traffic ? '需要在线有效流量样本' : '只改变累计显示，不改历史或套餐周期用量';
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
    ['累计入站', traffic ? detailBytes(traffic.rx_total,false) : '—'],
    ['累计出站', traffic ? detailBytes(traffic.tx_total,false) : '—'],
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
  if (!confirm(trafficRequest ? `使用原请求 ${trafficRequest} 核实同一次累计重新起算，不创建新重置。继续？` : '只重新开始页面累计入站/出站统计；历史与套餐周期用量不会改变。继续？')) return;
  const mutation = agentState.beginMutation(id, ['traffic']);
  if (!mutation) return;
  const verifying = Boolean(trafficRequest);
  trafficRequest ||= requestID();
  const operationID = trafficRequest;
  const controller = new AbortController(); trafficWriteController = controller;
  trafficReset.disabled = true;
  trafficResetFeedback.textContent = '正在记录新的累计起点…';
  try {
    const traffic = await Management.write(`/api/v1/web/agents/${encodeURIComponent(id)}/traffic/reset`, {
      csrf: mutationCSRFToken, payload: {request_id: operationID}, signal: controller.signal,
      unauthorized: () => location.assign('/login'),
    });
    // A reused id returns the receipt of that old reset, not current totals.
    if (!agentState.finishMutation(mutation, verifying ? {} : {traffic})) return;
    trafficRequest = null;
    currentAgent = agentState.agents.get(id);
    trafficResetFeedback.textContent = verifying
      ? '已确认原请求执行；正在核对当前累计，不使用旧回执覆盖新样本或后续起点。'
      : '累计统计已从当前有效样本重新开始；套餐周期用量未改变。';
  } catch (error) {
    if (controller.signal.aborted || agentState.blocked(id) || !agentState.agents.has(id)) return;
    const uncertain = !error.status || error.status >= 500;
    if (!uncertain) trafficRequest = null;
    trafficResetFeedback.textContent = uncertain
      ? `响应不确定，保留原累计和请求 ${operationID}；不会自动重试，可用原请求核实。`
      : error.message || '无法重新开始累计统计';
  } finally {
    if (trafficWriteController === controller) trafficWriteController = null;
    agentState.finishMutation(mutation);
    reconciliation.notify(id);
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
  const statuses = google.result ? [google.result.youtube?.status] : [];
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
  const mutation = agentState.beginMutation(id, ['google']);
  if (!mutation) return;
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
    agentState.finishMutation(mutation, {google_status: {...agentState.agents.get(id)?.google_status, pending: true}});
    currentAgent = agentState.agents.get(id);
    renderGoogleStatus(currentAgent);
  } catch (error) {
    googleFeedback.textContent = error.message || '检测请求失败';
    renderGoogleStatus(currentAgent);
  } finally {
    agentState.finishMutation(mutation);
    reconciliation.notify(id);
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
	const statusLabel = security.status === 'unavailable' && security.reason === 'platform_unsupported' ? '此平台暂不支持（v1.1）' : labels[security.status] || security.status;
	securityState.textContent = `${statusLabel}${security.stale ? ' · stale' : ''}${security.reason ? ` · ${security.reason}` : ''}`;
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
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s', 'TB/s'];
  let number = value;
  let index = 0;
  while (number >= 1000 && index < units.length - 1) {
    number /= 1000;
    index++;
  }
  return `${number.toFixed(0)}${units[index]}`;
};

async function readJSON(path, signal) {
  return AgentState.fetchJSON(path, {fetcher: fetch, signal, unauthorized: () => location.assign('/login')});
}

async function load() {
  if (!id || !/^[0-9a-f]{32}$/.test(id)) {
    empty.textContent = '无效的 Agent ID';
    return;
  }
  loadController?.abort();
  const controller = new AbortController();
  loadController = controller;
  const ticket = agentState.read(id);
  try {
    const encodedID = encodeURIComponent(id);
    const [agent, history, session] = await Promise.all([
      readJSON(`/api/v1/web/agents/${encodedID}`, controller.signal),
      readJSON(`/api/v1/web/agents/${encodedID}/history?hours=24`, controller.signal),
      readJSON('/api/v1/web/session', controller.signal),
    ]);
    mutationCSRFToken = session.csrf_token || '';
    if (controller.signal.aborted) return;
    agentState.commitDetail(ticket, agent);
    currentAgent = agentState.agents.get(id);
    if (pendingTelemetry && currentAgent) { agentState.event(pendingTelemetry); pendingTelemetry = null; currentAgent = agentState.agents.get(id); }
    renderCurrentAgent();
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
    if (controller.signal.aborted || error.name === 'AbortError') return;
    empty.textContent = '加载失败';
    outboundStatus.textContent = '出站状态加载失败';
    googleState.textContent = 'Google Status 加载失败';
	securityState.textContent = 'Security 数据加载失败';
  }
}

function renderCurrentAgent() {
  currentAgent = agentState.agents.get(id);
  const state = currentAgent?.state || {};
  document.querySelector('#title').textContent = `${state.hostname || currentAgent?.name || 'Agent'} · 历史`;
  renderAgentRuntime(currentAgent);
  renderOutbounds(currentAgent);
  renderGoogleStatus(currentAgent);
  renderSecurity(currentAgent);
}
async function reconcileAgent(agentID, signal) {
  const ticket = agentState.read(agentID);
  try {
    const detail = await readJSON(`/api/v1/web/agents/${encodeURIComponent(agentID)}`, signal);
    return !signal.aborted && agentState.commitDetail(ticket, detail);
  } catch (error) {
    if (error.status === 404) { if (agentState.missing(ticket)) renderCurrentAgent(); return false; }
    throw error;
  }
}
const reconciliation = AgentState.reconcile(agentState, reconcileAgent, renderCurrentAgent, () => {
  outboundStatus.textContent = '资料刷新失败，保留最后可信状态';
});
let suspended = false;
setInterval(() => { if (!suspended) reconciliation.retry(); }, 15000);
addEventListener('pagehide', () => { suspended = true; trafficWriteController?.abort(); selectorController.closeAll(); events.close(); loadController?.abort(); reconciliation.close(); });
addEventListener('pageshow', event => { if (event.persisted) location.reload(); });
load();

const events = new EventSource('/api/v1/web/events');
events.onopen = () => {
  if (!suspended && id && /^[0-9a-f]{32}$/.test(id)) reconciliation.notify(id);
};
events.addEventListener('agent', event => {
  const update = JSON.parse(event.data);
  if (update.agent_id !== id) return;
  if (agentState.fullEvent(update)) reconciliation.notify(id);
  else if (!currentAgent) pendingTelemetry = update;
  else if (agentState.event(update)) renderCurrentAgent();
});
