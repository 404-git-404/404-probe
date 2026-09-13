const container = document.querySelector('#agents');
const empty = document.querySelector('#empty');
const stream = document.querySelector('#stream');
const addAgentButton = document.querySelector('#add-agent');
const addAgentDialog = document.querySelector('#add-agent-dialog');
const addAgentCreate = document.querySelector('#add-agent-create');
const addAgentResult = document.querySelector('#add-agent-result');
const addAgentForm = document.querySelector('#add-agent-form');
const addAgentName = document.querySelector('#add-agent-name');
const addAgentSubmit = document.querySelector('#add-agent-submit');
const addAgentError = document.querySelector('#add-agent-error');
const createdInstallCommand = document.querySelector('#created-install-command');
const copyInstallCommand = document.querySelector('#copy-install-command');
const createdEnrollment = document.querySelector('#created-enrollment');
const removeAgentDialog = document.querySelector('#remove-agent-dialog');
const removeAgentForm = document.querySelector('#remove-agent-form');
const removeAgentName = document.querySelector('#remove-agent-name');
const removeAgentConfirm = document.querySelector('#remove-agent-confirm');
const removeAgentError = document.querySelector('#remove-agent-error');
const serverVersion = document.querySelector('#server-version');
const upgradeAgentDialog = document.querySelector('#upgrade-agent-dialog');
const upgradeAgentForm = document.querySelector('#upgrade-agent-form');
const upgradeCurrentVersion = document.querySelector('#upgrade-current-version');
const upgradeTargetVersion = document.querySelector('#upgrade-target-version');
const upgradeAgentConfirm = document.querySelector('#upgrade-agent-confirm');
const upgradeAgentError = document.querySelector('#upgrade-agent-error');
const planDialog = document.querySelector('#plan-dialog');
const planForm = document.querySelector('#plan-form');
const planAgentName = document.querySelector('#plan-agent-name');
const planTrafficEnabled = document.querySelector('#plan-traffic-enabled');
const planTrafficFields = document.querySelector('#plan-traffic-fields');
const planError = document.querySelector('#plan-error');
const agents = new Map();
const revokedAgentIDs = new Set();
const openManagementAgentIDs = new Set();
let mutationCSRFToken = '';
let pendingRemoveAgentID = '';
let pendingUpgradeAgentID = '';
let pendingPlanAgentID = '';
let serverBuild = {version: 'unknown', upgrade_eligible: false};

const bytes = (value, rate = false) => {
  let number = Number(value || 0);
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let index = 0;
  while (number >= 1024 && index < units.length - 1) {
    number /= 1024;
    index++;
  }
  return `${number.toFixed(index ? 1 : 0)} ${units[index]}${rate ? '/s' : ''}`;
};

const duration = value => {
  const seconds = Number(value || 0);
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor(seconds % 86400 / 3600);
  const minutes = Math.floor(seconds % 3600 / 60);
  return days ? `${days}天 ${hours}小时` : `${hours}小时 ${minutes}分`;
};

const seen = value => value ? new Date(value).toLocaleString() : '从未上报';
const pct = value => `${Number(value || 0).toFixed(1)}%`;
const upgradeLabel = operation => {
  if (!operation) return '';
  const labels = {requested: 'Requested', claimed: 'Claimed', downloading: 'Downloading', verifying: 'Verifying', staging: 'Staging', installing: 'Installing', restarting: 'Restarting', health_check: 'Checking health', succeeded: `Upgraded to ${operation.target_version}`, failed: 'Upgrade failed', rolled_back: 'Upgrade failed; previous version restored'};
  return labels[operation.status] || operation.status;
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

async function readOptionalJSON(path) {
  const response = await fetch(path, {cache: 'no-store'});
  if (response.status === 204) return null;
  if (response.status === 401) {
    location.assign('/login');
    throw new Error('authentication required');
  }
  if (!response.ok) throw new Error(response.statusText);
  return response.json();
}

async function loadMutationSession() {
  try {
    const session = await readJSON('/api/v1/web/session');
    mutationCSRFToken = session.csrf_token || '';
    serverBuild = await readJSON('/api/v1/web/version');
    serverVersion.textContent = `Server ${serverBuild.version || 'unknown'}`;
    addAgentButton.disabled = !mutationCSRFToken;
    render();
  } catch (error) {
    mutationCSRFToken = '';
    addAgentButton.disabled = true;
  }
}

function openUpgradeAgentDialog(agent) {
  pendingUpgradeAgentID = agent.agent_id;
  upgradeCurrentVersion.textContent = agent.version || 'unknown';
  upgradeTargetVersion.textContent = serverBuild.version || 'unknown';
  upgradeAgentError.classList.add('hidden');
  upgradeAgentConfirm.disabled = false;
  upgradeAgentDialog.showModal();
}

document.querySelector('#upgrade-agent-cancel').addEventListener('click', () => upgradeAgentDialog.close());
upgradeAgentDialog.addEventListener('close', () => { pendingUpgradeAgentID = ''; });
upgradeAgentForm.addEventListener('submit', async event => {
  event.preventDefault();
  const agentID = pendingUpgradeAgentID;
  if (!mutationCSRFToken || !agentID) return;
  upgradeAgentConfirm.disabled = true;
  upgradeAgentError.classList.add('hidden');
  try {
    const response = await fetch(`/api/v1/web/agents/${encodeURIComponent(agentID)}/upgrade`, {
      method: 'POST', cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken}, body: '{}',
    });
    if (response.status === 401) { location.assign('/login'); return; }
    if (!response.ok) throw new Error(response.statusText);
    const operation = await response.json();
    agents.set(agentID, {...agents.get(agentID), upgrade: operation});
    upgradeAgentDialog.close();
    render();
  } catch (error) {
    upgradeAgentError.classList.remove('hidden');
    upgradeAgentConfirm.disabled = false;
  }
});

function clearEnrollmentDialog() {
  createdInstallCommand.textContent = '';
	copyInstallCommand.disabled = true;
  createdEnrollment.textContent = '';
  addAgentForm.reset();
  addAgentError.classList.add('hidden');
  addAgentCreate.classList.remove('hidden');
  addAgentResult.classList.add('hidden');
  addAgentSubmit.disabled = false;
}

addAgentButton.addEventListener('click', () => {
  clearEnrollmentDialog();
  addAgentDialog.showModal();
  addAgentName.focus();
});

document.querySelector('#add-agent-cancel').addEventListener('click', () => addAgentDialog.close());
document.querySelector('#add-agent-done').addEventListener('click', () => addAgentDialog.close());
addAgentDialog.addEventListener('close', clearEnrollmentDialog);

addAgentForm.addEventListener('submit', async event => {
  event.preventDefault();
  if (!mutationCSRFToken) return;
  addAgentSubmit.disabled = true;
  addAgentError.classList.add('hidden');
  try {
    const response = await fetch('/api/v1/web/agents', {
      method: 'POST',
      cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken},
      body: JSON.stringify({name: addAgentName.value}),
    });
    if (response.status === 401) {
      location.assign('/login');
      return;
    }
    if (!response.ok) throw new Error(response.statusText);
    const enrollment = await response.json();
    createdInstallCommand.textContent = enrollment.install_available ? enrollment.install_command : (enrollment.install_message || '当前 Server 构建无法生成安全安装命令');
	copyInstallCommand.disabled = !enrollment.install_available;
    createdEnrollment.textContent = enrollment.enrollment_value;
    addAgentCreate.classList.add('hidden');
    addAgentResult.classList.remove('hidden');
    await refresh();
  } catch (error) {
    addAgentError.classList.remove('hidden');
    addAgentSubmit.disabled = false;
  }
});

function clearRemoveAgentDialog() {
  pendingRemoveAgentID = '';
  removeAgentName.textContent = '';
  removeAgentError.classList.add('hidden');
  removeAgentConfirm.disabled = false;
}

function openRemoveAgentDialog(agent) {
  clearRemoveAgentDialog();
  pendingRemoveAgentID = agent.agent_id;
  removeAgentName.textContent = agent.name || '未命名 Agent';
  removeAgentDialog.showModal();
}

document.querySelector('#remove-agent-cancel').addEventListener('click', () => removeAgentDialog.close());
removeAgentDialog.addEventListener('close', clearRemoveAgentDialog);

removeAgentForm.addEventListener('submit', async event => {
  event.preventDefault();
  const agentID = pendingRemoveAgentID;
  if (!mutationCSRFToken || !agentID) return;
  removeAgentConfirm.disabled = true;
  removeAgentError.classList.add('hidden');
  try {
    const response = await fetch(`/api/v1/web/agents/${encodeURIComponent(agentID)}/revoke`, {
      method: 'POST',
      cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken},
      body: '{}',
    });
    if (response.status === 401) {
      location.assign('/login');
      return;
    }
    if (!response.ok) throw new Error(response.statusText);
    const revoked = await response.json();
    if (!revoked.agent || !revoked.agent.revoked) throw new Error('Agent was not revoked');
    revokedAgentIDs.add(agentID);
    agents.delete(agentID);
    removeAgentDialog.close();
    render();
    await refresh();
  } catch (error) {
    removeAgentError.classList.remove('hidden');
    removeAgentConfirm.disabled = false;
  }
});

async function setAgentDisabled(agent, disabled, button) {
  if (!mutationCSRFToken || agent.revoked) return;
  button.disabled = true;
  const action = disabled ? 'disable' : 'enable';
  try {
    const response = await fetch(`/api/v1/web/agents/${encodeURIComponent(agent.agent_id)}/${action}`, {
      method: 'POST',
      cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken},
      body: '{}',
    });
    if (response.status === 401) {
      location.assign('/login');
      return;
    }
    if (!response.ok) throw new Error(response.statusText);
    const changed = await response.json();
    if (!changed.agent || changed.agent.revoked || Boolean(changed.agent.disabled_at) !== disabled) {
      throw new Error('Agent state did not change');
    }
    agents.set(agent.agent_id, {...agent, ...changed.agent});
    render();
    await refresh();
  } catch (error) {
    button.disabled = false;
  }
}

function googleCheckItems(agent) {
  const google = agent.google_status;
  const all = (value, tone) => [
    ['YouTube', value, tone], ['Search', value, tone], ['登录', value, tone], ['Gemini', value, tone],
  ];
  if (!google?.supported) return all('不支持', 'unknown');
  if (google.pending) return all('检测中', 'pending');
  if (!google.result) return all('尚未检测', 'unknown');
  const result = google.result;
  const youtube = result.youtube?.status === 'cn'
    ? ['CN · 送中', 'danger']
    : result.youtube?.status === 'not_cn'
      ? [`${result.youtube.region ? `${result.youtube.region} · ` : ''}非 CN`, 'ok']
      : ['未知', 'unknown'];
  const search = ({ok: ['正常', 'ok'], challenge: ['需验证', 'warn'], blocked: ['受限', 'danger']})[result.search?.status] || ['未知', 'unknown'];
  const signin = ({reachable: ['可达', 'ok'], challenge: ['需验证', 'warn'], blocked: ['受限', 'danger']})[result.signin?.status] || ['未知', 'unknown'];
  const gemini = result.gemini?.status === 'available'
    ? [`可用${result.gemini.region ? ` · ${result.gemini.region}` : ''}`, 'ok']
    : result.gemini?.status === 'blocked' ? ['受限', 'danger'] : ['未知', 'unknown'];
  const items = [['YouTube', ...youtube], ['Search', ...search], ['登录', ...signin], ['Gemini', ...gemini]];
  return google.stale ? items.map(([label, value]) => [label, value, 'stale']) : items;
}

function googleStatusSummary(agent) {
  return googleCheckItems(agent).map(([name, value]) => `${name} ${value}`).join(' · ');
}

function googleStatusState(google) {
  if (!google?.supported) return '当前 Agent 不支持检测';
  if (google.pending) return '检测中';
  if (!google.result) return '尚未检测';
  if (google.stale) return '旧结果 · 需重新检测';
  return '';
}

function googleStatusPanel(agent) {
  const panel = document.createElement('section');
  panel.className = 'google-checks';
  panel.setAttribute('aria-label', 'Google 与 YouTube 检测结果');
  for (const [label, value, tone] of googleCheckItems(agent)) {
    const item = document.createElement('div');
    item.className = 'google-check';
    const name = document.createElement('span');
    name.textContent = label;
    const result = document.createElement('strong');
    result.className = `check-result ${tone}`;
    result.textContent = value;
    item.append(name, result);
    panel.append(item);
  }
  const noteText = googleStatusState(agent.google_status);
  if (noteText) {
    const note = document.createElement('small');
    note.textContent = noteText;
    panel.append(note);
  }
  return panel;
}

async function rerunGoogleStatus(agent, button, feedback) {
  if (!mutationCSRFToken) return;
  button.disabled = true;
  button.textContent = '检测中…';
  feedback.textContent = '';
  try {
    const response = await fetch(`/api/v1/web/agents/${encodeURIComponent(agent.agent_id)}/google-status`, {
      method: 'POST', cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken}, body: '{}',
    });
    if (response.status === 401) { location.assign('/login'); return; }
    if (!response.ok) {
      let message = '检测请求失败';
      try { message = (await response.json()).error?.message || message; } catch (error) { /* use safe fallback */ }
      throw new Error(message);
    }
    agents.set(agent.agent_id, {...agent, google_status: {...agent.google_status, pending: true}});
    render();
  } catch (error) {
    button.textContent = '重新检测';
    button.disabled = false;
    feedback.textContent = error.message || '检测请求失败';
  }
}

for (const button of document.querySelectorAll('[data-copy-target]')) {
  button.addEventListener('click', async () => {
    const target = document.querySelector(`#${button.dataset.copyTarget}`);
    if (!target || !target.textContent) return;
    const label = button.textContent;
    try {
      await navigator.clipboard.writeText(target.textContent);
      button.textContent = '已复制';
      setTimeout(() => { button.textContent = label; }, 1200);
    } catch (error) {
      button.textContent = '复制失败';
      setTimeout(() => { button.textContent = label; }, 1200);
    }
  });
}

const planFields = {
  traffic_mode: '#plan-traffic-mode', quota_value: '#plan-quota-value', quota_unit: '#plan-quota-unit',
  cycle_kind: '#plan-cycle-kind', cycle_count: '#plan-cycle-count', cycle_anchor: '#plan-cycle-anchor', timezone: '#plan-timezone',
  calibration_value: '#plan-calibration-value', calibration_unit: '#plan-calibration-unit',
  bandwidth_value: '#plan-bandwidth-value', bandwidth_unit: '#plan-bandwidth-unit', currency: '#plan-currency',
  purchase_price: '#plan-purchase-price', purchase_price_period: '#plan-purchase-period',
  renewal_price: '#plan-renewal-price', renewal_price_period: '#plan-renewal-period',
  purchase_date: '#plan-purchase-date', renewal_date: '#plan-renewal-date', expiry_date: '#plan-expiry-date',
  country_code_override: '#plan-country-code',
};

function togglePlanTrafficFields() {
  planTrafficFields.classList.toggle('hidden', !planTrafficEnabled.checked);
}

function openPlanDialog(agent) {
  pendingPlanAgentID = agent.agent_id;
  planForm.reset();
  const plan = agent.plan || {};
  planAgentName.textContent = agent.name || agent.state?.hostname || '未命名 Agent';
  planTrafficEnabled.checked = Boolean(plan.traffic_mode);
  for (const [key, selector] of Object.entries(planFields)) {
    const input = document.querySelector(selector);
    const fallback = key === 'cycle_count' ? '1' : key === 'timezone' ? (Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC') : '';
    input.value = key.startsWith('calibration_') ? (key === 'calibration_unit' ? (plan.quota_unit || 'GB') : '') : (plan[key] ?? fallback);
  }
  if (!plan.traffic_mode) document.querySelector('#plan-traffic-mode').value = 'sum';
  if (!plan.quota_unit) document.querySelector('#plan-quota-unit').value = 'TB';
  if (!plan.cycle_kind) document.querySelector('#plan-cycle-kind').value = 'monthly';
  planError.classList.add('hidden');
  planError.textContent = '';
  togglePlanTrafficFields();
  planDialog.showModal();
}

planTrafficEnabled.addEventListener('change', togglePlanTrafficFields);
document.querySelector('#plan-cancel').addEventListener('click', () => planDialog.close());
planDialog.addEventListener('close', () => { pendingPlanAgentID = ''; });

async function savePlanPayload(payload) {
  const response = await fetch(`/api/v1/web/agents/${encodeURIComponent(pendingPlanAgentID)}/plan`, {
    method: 'PUT', cache: 'no-store',
    headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken},
    body: JSON.stringify(payload),
  });
  if (response.status === 401) { location.assign('/login'); return null; }
  if (!response.ok) {
    let message = '套餐资料保存失败';
    try { message = (await response.json()).error?.message || message; } catch (error) { /* keep fallback */ }
    throw new Error(message);
  }
  return response.status === 204 ? null : response.json();
}

planForm.addEventListener('submit', async event => {
  event.preventDefault();
  if (!pendingPlanAgentID || !mutationCSRFToken) return;
  const save = document.querySelector('#plan-save');
  save.disabled = true;
  planError.classList.add('hidden');
  const payload = {};
  for (const [key, selector] of Object.entries(planFields)) payload[key] = document.querySelector(selector).value.trim();
  payload.cycle_count = Number(payload.cycle_count || 0);
  payload.currency = payload.currency.toUpperCase();
  payload.country_code_override = payload.country_code_override.toUpperCase();
  if (!planTrafficEnabled.checked) {
    for (const key of ['traffic_mode','quota_value','quota_unit','cycle_kind','cycle_anchor','calibration_value','calibration_unit']) payload[key] = '';
    payload.cycle_count = 0;
  }
  if (!payload.quota_value) payload.quota_unit = '';
  if (!payload.calibration_value) payload.calibration_unit = '';
  if (!payload.bandwidth_value) payload.bandwidth_unit = '';
  if (!payload.purchase_price) payload.purchase_price_period = '';
  if (!payload.renewal_price) payload.renewal_price_period = '';
  try {
    const agentID = pendingPlanAgentID;
    const plan = await savePlanPayload(payload);
    const detail = await readJSON(`/api/v1/web/agents/${encodeURIComponent(agentID)}`);
    agents.set(agentID, {...agents.get(agentID), ...detail, plan});
    planDialog.close();
    render();
  } catch (error) {
    planError.textContent = error.message || '套餐资料保存失败';
    planError.classList.remove('hidden');
  } finally {
    save.disabled = false;
  }
});

document.querySelector('#plan-clear').addEventListener('click', async () => {
  if (!pendingPlanAgentID || !mutationCSRFToken || !confirm('清除这台 VPS 的套餐与计费周期资料？永久流量累计不会删除。')) return;
  try {
    const agentID = pendingPlanAgentID;
    await savePlanPayload({});
    const detail = await readJSON(`/api/v1/web/agents/${encodeURIComponent(agentID)}`);
    agents.set(agentID, {...agents.get(agentID), ...detail, plan: null});
    planDialog.close();
    render();
  } catch (error) {
    planError.textContent = error.message || '套餐资料清除失败';
    planError.classList.remove('hidden');
  }
});

function metric(label, value) {
  const box = document.createElement('div');
  box.className = 'metric';
  const name = document.createElement('label');
  name.textContent = label;
  const strong = document.createElement('strong');
  setReadableText(strong, value, `${label}: ${value}`);
  box.append(name, strong);
  return box;
}

function progressBar(percent, tone = '') {
  const track = document.createElement('div');
  track.className = `progress-track ${tone}`.trim();
  track.setAttribute('role', 'progressbar');
  const bounded = Math.max(0, Math.min(100, Number(percent || 0)));
  track.setAttribute('aria-valuenow', bounded.toFixed(1));
  track.setAttribute('aria-valuemin', '0');
  track.setAttribute('aria-valuemax', '100');
  const fill = document.createElement('span');
  fill.style.width = `${bounded}%`;
  track.append(fill);
  return track;
}

function resourceRow(label, value, detail, percent, tone = '') {
  const row = document.createElement('div');
  row.className = `resource-row ${label === 'CPU' ? 'cpu-resource' : ''} ${label === '内存' ? 'memory-resource' : ''}`.trim();
  const head = document.createElement('div');
  const name = document.createElement('span');
  name.textContent = label;
  const strong = document.createElement('strong');
  strong.textContent = value;
  head.append(name, strong);
  row.append(head);
  if (percent != null) row.append(progressBar(percent, tone));
  if (detail) {
    const small = document.createElement('small');
    small.textContent = detail;
    row.append(small);
  }
  return row;
}

function utilizationTone(percent, warning = 70, critical = 90) {
  if (percent == null || !Number.isFinite(Number(percent))) return 'unknown';
  return Number(percent) >= critical ? 'critical' : Number(percent) >= warning ? 'warning' : 'healthy';
}

function stealTone(percent) {
  return utilizationTone(percent, 2, 10);
}

function diskBusyTone(percent) {
  return utilizationTone(percent, 60, 85);
}

function bandwidthTone(rate, plan) {
  if (!plan?.bandwidth_bps || rate == null) return 'neutral';
  return utilizationTone(Number(rate) * 8 / Number(plan.bandwidth_bps) * 100, 70, 90);
}

function metricTile(kind, icon, label, value, detail, percent, tone, extra) {
  const tile = document.createElement('section');
  tile.className = `metric-tile metric-${kind} tone-${tone || 'unknown'}`;
  const header = document.createElement('div');
  header.className = 'metric-tile-head';
  const title = document.createElement('span');
  title.className = 'metric-tile-label';
  title.innerHTML = `<i aria-hidden="true">${icon}</i>${label}`;
  const strong = document.createElement('strong');
  strong.textContent = value;
  header.append(title, strong);
  const note = document.createElement('small');
  note.textContent = detail;
  note.title = detail;
  tile.append(header, note, progressBar(percent, tone || 'unknown'));
  if (extra) tile.append(extra);
  return tile;
}

function cpuStealRow(state, stale) {
  const value = stale ? null : state.cpu_steal_percent;
  const row = document.createElement('div');
  row.className = `steal-row tone-${value == null ? 'unknown' : stealTone(value)}`;
  const label = document.createElement('span');
  label.textContent = 'CPU steal · 等待宿主机 CPU';
  label.title = '虚拟机等待宿主机 CPU 的时间占比';
  const amount = document.createElement('strong');
  amount.textContent = value == null ? '—' : pct(value);
  row.append(label, amount, progressBar(value, value == null ? 'unknown' : stealTone(value)));
  return row;
}

function pricePeriod(value) {
  return ({once: '一次性', monthly: '/ 月', yearly: '/ 年'})[value] || '';
}

function calendarDaysRemaining(date, timezone = 'UTC') {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date || '')) return '';
  const [year, month, day] = date.split('-').map(Number);
  let todayParts;
  try {
    const parts = new Intl.DateTimeFormat('en-CA', {timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit'}).formatToParts(new Date());
    const values = Object.fromEntries(parts.map(part => [part.type, part.value]));
    todayParts = [Number(values.year), Number(values.month), Number(values.day)];
  } catch (error) {
    const today = new Date();
    todayParts = [today.getUTCFullYear(), today.getUTCMonth() + 1, today.getUTCDate()];
  }
  return Math.ceil((Date.UTC(year, month - 1, day) - Date.UTC(todayParts[0], todayParts[1] - 1, todayParts[2])) / 86400000);
}

function calendarCountdown(date, timezone = 'UTC') {
  const remaining = calendarDaysRemaining(date, timezone);
  if (remaining === '') return '';
  if (remaining < 0) return `已过期 ${Math.abs(remaining)} 天`;
  if (remaining === 0) return '今天';
  if (remaining < 60) return `${remaining} 天`;
  const months = Math.floor(remaining / 30);
  return months < 24 ? `约 ${months} 个月` : `约 ${(remaining / 365).toFixed(1)} 年`;
}

function dateInTimezone(timestamp, timezone = 'UTC') {
  try {
    return new Date(Number(timestamp)).toLocaleDateString(undefined, {timeZone: timezone});
  } catch (error) {
    return new Date(Number(timestamp)).toLocaleDateString(undefined, {timeZone: 'UTC'});
  }
}

function planFact(label, value, detail = '') {
  const item = document.createElement('div');
  const name = document.createElement('span');
  name.textContent = label;
  const strong = document.createElement('strong');
  strong.textContent = value;
  item.append(name, strong);
  if (detail) {
    const small = document.createElement('small');
    small.textContent = detail;
    item.append(small);
  }
  return item;
}

function compactPrice(plan) {
  if (!plan) return '未设价格';
  const value = plan.renewal_price || plan.purchase_price;
  const period = plan.renewal_price ? plan.renewal_price_period : plan.purchase_price_period;
  if (!value) return '未设价格';
  const amount = plan.currency === 'USD' && Number.isFinite(Number(value)) ? `$${Number(value).toFixed(2)}` : `${plan.currency || ''} ${value}`.trim();
  return `${amount}${({monthly: '/mo', yearly: '/yr', once: ' once'})[period] || ''}`;
}

function compactBandwidth(plan) {
  return plan?.bandwidth_value ? `${plan.bandwidth_value} ${plan.bandwidth_unit}` : '未设带宽';
}

function planDeadline(plan) {
  if (!plan) return {label: '未设续费日', tone: ''};
  const date = plan.renewal_date || plan.expiry_date;
  if (!date) return {label: '未设续费日', tone: ''};
  const days = calendarDaysRemaining(date, plan.timezone || 'UTC');
  const isRenewal = Boolean(plan.renewal_date);
  let label;
  if (days < 0) label = isRenewal ? `续费日已过 ${Math.abs(days)} 天` : `已到期 ${Math.abs(days)} 天`;
  else if (days === 0) label = isRenewal ? '续费日已到' : '今天到期';
  else label = isRenewal ? `${days} 天后续费` : `${days} 天后到期`;
  return {
    label,
    tone: days <= 0 ? 'danger' : days <= 7 ? 'warn' : '',
  };
}

function detailFact(label, value) {
  const item = document.createElement('div');
  const name = document.createElement('span');
  name.textContent = label;
  const content = document.createElement('strong');
  content.textContent = value || '—';
  item.append(name, content);
  return item;
}

function planPanel(plan) {
  const panel = document.createElement('section');
  panel.className = 'plan-summary';
  if (plan.traffic_mode) {
    const traffic = document.createElement('div');
    traffic.className = 'traffic-plan';
    const heading = document.createElement('div');
    heading.className = 'section-label';
    const title = document.createElement('span');
    title.textContent = `本周期流量 · ${{rx: 'RX', tx: 'TX', sum: 'RX + TX'}[plan.traffic_mode]}`;
    const value = document.createElement('strong');
    const unit = plan.quota_unit || 'GiB';
    value.textContent = plan.quota_bytes == null ? bytes(plan.usage_bytes) : `${bytes(plan.usage_bytes)} / ${plan.quota_value} ${unit}`;
    heading.append(title, value);
    traffic.append(heading);
    if (plan.quota_bytes != null) {
      const usedPercent = Number(plan.usage_bytes) / Number(plan.quota_bytes) * 100;
      traffic.append(progressBar(usedPercent, usedPercent >= 100 ? 'critical' : usedPercent >= 80 ? 'warning' : 'healthy'));
    }
    const note = document.createElement('small');
    const cycle = plan.cycle_end ? `周期至 ${dateInTimezone(plan.cycle_end, plan.timezone || 'UTC')}` : '周期尚未开始';
    note.textContent = cycle;
    note.title = plan.usage_status === 'calibrated'
      ? '人工校准值，不补齐历史'
      : '仅统计可观测增量，存在缺测';
    traffic.append(note);
    panel.append(traffic);
  } else {
    const note = document.createElement('span');
    note.className = 'plan-empty';
    note.textContent = '未设置周期流量';
    panel.append(note);
  }
  return panel;
}

function setReadableText(element, value, accessibleValue = value) {
  element.textContent = value;
  element.title = value;
  element.setAttribute('aria-label', accessibleValue);
}

function countryMark(code, source) {
  const mark = document.createElement('span');
  mark.className = 'country-mark';
  const normalized = /^[A-Z]{2}$/.test(code || '') ? code : '';
  if (normalized) {
    mark.dataset.countryCode = normalized;
    const sourceLabel = source === 'manual' ? '手动覆盖' : 'Agent 安装时识别';
    mark.title = `${normalized} · ${sourceLabel}`;
    mark.setAttribute('aria-label', `出口国家或地区 ${normalized}，${sourceLabel}`);
    const flag = document.createElement('img');
    flag.className = 'country-flag';
    flag.src = `/vendor/flag-icons/4x3/${normalized.toLowerCase()}.svg`;
    flag.alt = '';
    flag.setAttribute('aria-hidden', 'true');
    flag.addEventListener('error', () => {
      mark.replaceChildren('🌐');
      mark.removeAttribute('data-country-code');
      mark.title = '出口国家/地区未知';
      mark.setAttribute('aria-label', '出口国家或地区未知');
    }, {once: true});
    mark.append(flag);
  } else {
    mark.textContent = '🌐';
    mark.title = '出口国家/地区未知';
    mark.setAttribute('aria-label', '出口国家或地区未知');
  }
  return mark;
}

function render() {
  const focusedControl = document.activeElement?.closest?.('[data-focus-key]');
  const focusedAgentID = focusedControl?.closest?.('.agent-card')?.dataset.agentId || '';
  const focusedKey = focusedControl?.dataset.focusKey || '';
  container.replaceChildren();
  const list = [...agents.values()].sort((left, right) =>
    (left.name || '').localeCompare(right.name || '') || left.agent_id.localeCompare(right.agent_id));
  empty.classList.toggle('hidden', list.length > 0);
  for (const agent of list) {
    const state = agent.state || {};
    const hasState = Boolean(agent.state);
    const metricsStale = Boolean(!agent.online || agent.disabled_at || state.stale);
    const card = document.createElement('article');
    card.className = 'card agent-card';
    card.dataset.agentId = agent.agent_id;
    card.setAttribute('aria-label', `Agent ${agent.name || '未命名'}`);
    const head = document.createElement('div');
    head.className = 'card-head';
    const title = document.createElement('div');
    title.className = 'card-identity';
    const titleLine = document.createElement('div');
    titleLine.className = 'card-title-line';
    const heading = document.createElement('h2');
    setReadableText(heading, agent.name || state.hostname || '未命名 VPS');
    const name = document.createElement('div');
    name.className = 'name';
    setReadableText(name, [state.cpu_cores ? `${state.cpu_cores} 核` : '', state.os, state.arch].filter(Boolean).join(' · ') || '等待首次上报');
    titleLine.append(countryMark(agent.country_code, agent.country_source), heading);
    title.append(titleLine, name);
    const status = document.createElement('span');
    const stateName = agent.revoked ? 'revoked' : agent.disabled_at ? 'paused' : agent.online ? 'online' : 'offline';
    status.className = `status ${stateName}`;
    status.dataset.agentState = stateName;
    status.textContent = agent.revoked ? '● REVOKED' : agent.disabled_at ? '● PAUSED' : agent.online ? '● ONLINE' : '● OFFLINE';
    head.append(title, status);
    card.classList.add(`state-${stateName}`);
    card.append(head);

    const metrics = document.createElement('div');
    metrics.className = 'resource-grid';
    const metricTone = percent => metricsStale ? 'unknown' : utilizationTone(percent);
    const steal = cpuStealRow(state, metricsStale);
    const diskBusy = metricsStale ? null : state.disk_busy_percent;
    const diskRead = metricsStale ? null : state.disk_read_rate;
    const diskWrite = metricsStale ? null : state.disk_write_rate;
    metrics.append(
      metricTile('cpu', '◉', 'CPU', hasState ? pct(state.cpu_percent) : '—', state.cpu_cores ? `${state.cpu_cores} 核` : '核心数未知', hasState && !metricsStale ? state.cpu_percent : null, metricTone(hasState ? state.cpu_percent : null), steal),
      metricTile('memory', '▦', '内存', hasState ? pct(state.ram_percent) : '—', hasState ? `${bytes(state.ram_used)} / ${bytes(state.ram_total)}` : '已用 / 总量未知', hasState && !metricsStale ? state.ram_percent : null, metricTone(hasState ? state.ram_percent : null)),
      metricTile('disk', '▰', '磁盘', hasState ? pct(state.disk_percent) : '—', hasState ? `${bytes(state.disk_used)} / ${bytes(state.disk_total)}` : '已用 / 总量未知', hasState && !metricsStale ? state.disk_percent : null, metricTone(hasState ? state.disk_percent : null)),
      metricTile('disk-io', '↕', '磁盘 I/O', diskBusy == null ? '—' : pct(diskBusy), diskRead == null || diskWrite == null ? '等待稳定的第二个样本' : `读 ${bytes(diskRead, true)} · 写 ${bytes(diskWrite, true)}`, diskBusy, diskBusyTone(diskBusy)),
    );
    const ioTile = metrics.querySelector('.metric-disk-io');
    ioTile.title = '读写速率为所选顶层逻辑设备合计；进度条为最忙磁盘的观测忙碌时间占比。';
    card.append(metrics);

    const network = document.createElement('div');
    network.className = 'network';
    for (const [label, arrow, rate, total] of [['下载', '↓', state.rx_rate, state.rx_total], ['上传', '↑', state.tx_rate, state.tx_total]]) {
      const box = document.createElement('div');
      const directionTone = metricsStale ? 'unknown' : bandwidthTone(rate, agent.plan);
      box.className = `${label === '下载' ? 'network-rx' : 'network-tx'} tone-${directionTone}`;
      const networkName = document.createElement('span');
      networkName.textContent = `${arrow} ${label}${metricsStale ? ' · 离线' : ''}`;
      const strong = document.createElement('strong');
      setReadableText(strong, metricsStale ? '—' : bytes(rate, true));
      const small = document.createElement('small');
      setReadableText(small, `${label === '下载' ? '入站' : '出站'} ${hasState ? bytes(total) : '—'}`);
      box.append(networkName, strong, small);
      network.append(box);
    }
    card.append(network);

    card.append(planPanel(agent.plan || {}));
    card.append(googleStatusPanel(agent));

    const deadline = planDeadline(agent.plan);
    const times = document.createElement('div');
    times.className = 'card-times';
    const onlineTime = document.createElement('span');
    onlineTime.textContent = !hasState ? '等待首次上报' : metricsStale ? `上次采样 ${seen(state.collected_at)}` : `在线 ${duration(state.uptime)}`;
    const due = document.createElement('span');
    due.className = deadline.tone;
    due.textContent = deadline.label;
    times.append(onlineTime, due);
    card.append(times);

    const remove = document.createElement('button');
    remove.className = 'remove-agent';
    remove.dataset.agentId = agent.agent_id;
    remove.type = 'button';
    remove.textContent = '永久移除';
    remove.dataset.focusKey = 'remove';
    remove.disabled = !mutationCSRFToken;
    remove.addEventListener('click', () => openRemoveAgentDialog(agent));
    const stateAction = document.createElement('button');
    stateAction.className = 'agent-state-action';
    stateAction.dataset.agentId = agent.agent_id;
    stateAction.dataset.agentAction = agent.disabled_at ? 'enable' : 'disable';
    stateAction.type = 'button';
    stateAction.textContent = agent.disabled_at ? '恢复' : '暂停';
    stateAction.dataset.focusKey = 'state';
    stateAction.disabled = !mutationCSRFToken || agent.revoked;
    stateAction.addEventListener('click', () => setAgentDisabled(agent, !agent.disabled_at, stateAction));
    const upgrade = document.createElement('button');
    upgrade.className = 'upgrade-agent';
    upgrade.type = 'button';
    upgrade.textContent = '升级';
    upgrade.dataset.focusKey = 'upgrade';
    const versionParts = value => /^v\d+\.\d+\.\d+$/.test(value || '') ? value.slice(1).split('.').map(Number) : null;
    const currentParts = versionParts(agent.version);
    const targetParts = versionParts(serverBuild.version);
    const newer = currentParts && targetParts && targetParts.some((part, index) => part > currentParts[index] && targetParts.slice(0, index).every((value, prior) => value === currentParts[prior]));
    upgrade.disabled = !mutationCSRFToken || !agent.online || Boolean(agent.disabled_at) || !agent.upgrade_capable || !serverBuild.upgrade_eligible || !newer || Boolean(agent.upgrade && !['succeeded', 'failed', 'rolled_back'].includes(agent.upgrade.status));
    upgrade.title = !agent.upgrade_capable ? '需要先在主机上完成 v0.8 bootstrap' : !agent.online ? 'Agent 离线时不能升级' : agent.disabled_at ? '请先恢复 Agent' : '升级到当前 Server 对应版本';
    upgrade.addEventListener('click', () => openUpgradeAgentDialog(agent));
    const detailPanel = document.createElement('section');
    detailPanel.className = 'card-detail-panel';
    detailPanel.hidden = !openManagementAgentIDs.has(agent.agent_id);
    const facts = document.createElement('div');
    facts.className = 'card-detail-facts';
    facts.append(
      detailFact('主机名', state.hostname),
      detailFact('系统', [state.os, state.arch].filter(Boolean).join(' · ')),
      detailFact('负载', hasState ? `${Number(state.load1 || 0).toFixed(2)} / ${Number(state.load5 || 0).toFixed(2)} / ${Number(state.load15 || 0).toFixed(2)}` : '—'),
      detailFact('CPU steal', state.cpu_steal_percent == null || metricsStale ? '—' : `${pct(state.cpu_steal_percent)} · 等待宿主机 CPU`),
      detailFact('Swap', hasState ? `${bytes(state.swap_used)} / ${bytes(state.swap_total)}` : '—'),
      detailFact('磁盘 I/O', state.disk_busy_percent == null || metricsStale ? '—' : `最忙 ${pct(state.disk_busy_percent)} · 读 ${bytes(state.disk_read_rate, true)} · 写 ${bytes(state.disk_write_rate, true)}`),
      detailFact('Agent 版本', agent.version),
      detailFact('运行时长', hasState ? duration(state.uptime) : '—'),
      detailFact('最近上报', seen(agent.last_seen)),
      detailFact('检测时间', agent.google_status?.checked_at ? seen(agent.google_status.checked_at) : '尚未检测'),
    );
    detailPanel.append(facts);
    const detailActions = document.createElement('div');
    detailActions.className = 'card-detail-actions';
    const historyLink = document.createElement('a');
    historyLink.href = `/history.html?id=${encodeURIComponent(agent.agent_id)}`;
    historyLink.textContent = '24h 历史 / 选择器 / 安全观察';
    historyLink.dataset.focusKey = 'history';
    const recheck = document.createElement('button');
    recheck.type = 'button';
    recheck.textContent = agent.google_status?.pending ? '检测中…' : '重新检测';
    recheck.dataset.focusKey = 'recheck';
    recheck.disabled = !mutationCSRFToken || !agent.online || Boolean(agent.disabled_at) || agent.revoked || !agent.google_status?.supported || agent.google_status?.pending;
    const checkFeedback = document.createElement('span');
    checkFeedback.setAttribute('aria-live', 'polite');
    recheck.addEventListener('click', () => rerunGoogleStatus(agent, recheck, checkFeedback));
    detailActions.append(historyLink, recheck, checkFeedback);
    const management = document.createElement('div');
    management.className = 'agent-actions';
    if (!agent.revoked) management.append(upgrade, stateAction);
    management.append(remove);
    detailPanel.append(detailActions, management);
    card.append(detailPanel);

    const detailToggle = document.createElement('button');
    detailToggle.className = 'detail-toggle';
    detailToggle.type = 'button';
    detailToggle.textContent = '详情';
    detailToggle.dataset.focusKey = 'manage';
    detailToggle.setAttribute('aria-expanded', String(!detailPanel.hidden));
    detailToggle.addEventListener('click', () => {
      detailPanel.hidden = !detailPanel.hidden;
      detailToggle.setAttribute('aria-expanded', String(!detailPanel.hidden));
      if (detailPanel.hidden) openManagementAgentIDs.delete(agent.agent_id);
      else openManagementAgentIDs.add(agent.agent_id);
    });
    const editPlan = document.createElement('button');
    editPlan.className = 'edit-plan';
    editPlan.type = 'button';
    editPlan.textContent = agent.plan ? '编辑套餐' : '添加套餐';
    editPlan.dataset.focusKey = 'plan';
    editPlan.disabled = !mutationCSRFToken || agent.revoked;
    editPlan.addEventListener('click', () => openPlanDialog(agent));
    const actions = document.createElement('div');
    actions.className = 'card-actions';
    const price = document.createElement('span');
    price.className = 'footer-price';
    price.textContent = compactPrice(agent.plan);
    const bandwidth = document.createElement('span');
    bandwidth.className = 'footer-bandwidth';
    bandwidth.textContent = compactBandwidth(agent.plan);
    actions.append(detailToggle, price, bandwidth, editPlan);
    card.append(actions);
    container.append(card);
  }
  if (focusedAgentID && focusedKey) {
    for (const card of container.querySelectorAll('.agent-card')) {
      if (card.dataset.agentId === focusedAgentID) card.querySelector(`[data-focus-key="${focusedKey}"]`)?.focus({preventScroll: true});
    }
  }
}

async function hydrateDetails(records) {
  const pending = records;
  let next = 0;
  const worker = async () => {
    while (next < pending.length) {
      const record = pending[next++];
      const detail = await readJSON(`/api/v1/web/agents/${encodeURIComponent(record.agent_id)}`);
      const upgrade = await readOptionalJSON(`/api/v1/web/agents/${encodeURIComponent(record.agent_id)}/upgrade`);
      agents.set(record.agent_id, {...agents.get(record.agent_id), ...detail, upgrade, detail_loaded: true});
    }
  };
  const workers = Array.from({length: Math.min(8, pending.length)}, worker);
  await Promise.all(workers);
}

async function refresh() {
  try {
    const snapshot = new Map();
    const seenCursors = new Set();
    let cursor = '';
    do {
      const suffix = cursor ? `&cursor=${encodeURIComponent(cursor)}` : '';
      const page = await readJSON(`/api/v1/web/agents?limit=100${suffix}`);
      for (const summary of page.items) {
        snapshot.set(summary.agent_id, {...agents.get(summary.agent_id), ...summary});
      }
      cursor = page.next_cursor || '';
      if (cursor && seenCursors.has(cursor)) throw new Error('pagination loop');
      if (cursor) seenCursors.add(cursor);
    } while (cursor);
    agents.clear();
    for (const [id, agent] of snapshot) agents.set(id, agent);
    await hydrateDetails([...agents.values()]);
    render();
  } catch (error) {
    stream.textContent = '读取失败';
    stream.classList.remove('live');
  }
}

loadMutationSession();
refresh();
setInterval(refresh, 15000);

const events = new EventSource('/api/v1/web/events');
events.onopen = () => {
  stream.textContent = '实时连接';
  stream.classList.add('live');
};
events.onerror = () => {
  stream.textContent = '正在重连';
  stream.classList.remove('live');
};
events.addEventListener('agent', event => {
  const update = JSON.parse(event.data);
  if (revokedAgentIDs.has(update.agent_id)) return;
  agents.set(update.agent_id, {...agents.get(update.agent_id), ...update, detail_loaded: true});
  render();
});
