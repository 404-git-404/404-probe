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
const agentState = AgentState.create();
const monitorCoordinator = NetworkQualityCore.coordinator();
const networkQuality = NetworkQuality.create({csrf: () => mutationCSRFToken, coordinator: monitorCoordinator,
  historyHost: {open: (id,trigger,exact) => deviceDetails.open(id,'network',trigger,exact), container: () => deviceDetails.networkContainer(),
    active: id => deviceDetails.active('network',id), close: () => deviceDetails.close()}});
const trafficCharts = TrafficChart.create({coordinator: monitorCoordinator,openHistory: (id,trigger) => deviceDetails.open(id,'resources',trigger)});
const cardObservations = CardMetrics.createObserver();
const agents = agentState.agents;
let overviewReady = false;
const overview = Overview.create({size:CardMetrics.size,deadline:planDeadline});
const deviceDetails = DeviceDetails.create({agents,coordinator:monitorCoordinator,
  network: () => networkQuality, identity: agent => countryMark(agent?.country_code,agent?.country_source),
  management: agent => document.querySelector(`.agent-card[data-agent-id="${agent.agent_id}"] .card-detail-panel`),
  hydrate: id => { if(agents.has(id)&&!agents.get(id).detail_loaded)reconciliation.notify(id); }});
const revokedAgentIDs = agentState.revokedAgentIDs;
const selectorController = Selector.create({
  state: agentState, fetcher: (...args) => fetch(...args), csrf: () => mutationCSRFToken,
  unauthorized: () => location.assign('/login'), onchange: agentID => selectorView.refresh(agentID),
});
const selectorView = Selector.view({
  controller: selectorController, state: agentState, csrf: () => mutationCSRFToken,
  onopen: agentID => { planRenewal.close(); reconciliation.notify(agentID); selectorController.recover(agentID, readJSON); },
});
const editingAgentNames = new Set();
const agentNameDrafts = new Map();
const agentNameEditGeneration = new Map();
const agentNameErrors = new Map();
const nameWriteControllers = new Map();
const countryWriteControllers = new Map();
const trafficResetRequests = new Map();
const trafficResetControllers = new Map();
const trafficResetFeedback = new Map();
const uncertainCountryRequests = new Map();
let planCountryPicker;
const planWriteControllers = new Map();
const agentActionErrors = new Map();
let planEditGeneration = 0;
let mutationCSRFToken = '';
let pendingRemoveAgentID = '';
let pendingUpgradeAgentID = '';
let pendingPlanAgentID = '';
let planQuotaSession;
let serverBuild = {version: 'unknown', upgrade_eligible: false};

const bytes = (value, rate = false, binary = false) => {
  let number = Number(value || 0);
  const units = binary ? ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'] : ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const base = binary ? 1024 : 1000;
  let index = 0;
  while (number >= base && index < units.length - 1) {
    number /= base;
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

async function readJSON(path, signal) {
  return AgentState.fetchJSON(path, {fetcher: fetch, signal, unauthorized: () => location.assign('/login')});
}

async function readOptionalJSON(path, signal) {
  return AgentState.fetchJSON(path, {fetcher: fetch, signal, optional: true, unauthorized: () => location.assign('/login')});
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
  deviceDetails.close();
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
  const mutation = agentState.beginMutation(agentID, ['upgrade']);
  if (!mutation) return;
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
    if (!finishResource(mutation, {upgrade: operation})) return;
    if (pendingUpgradeAgentID === agentID) upgradeAgentDialog.close();
    render();
  } catch (error) {
    upgradeAgentError.classList.remove('hidden');
    upgradeAgentConfirm.disabled = false;
  } finally {
    finishResource(mutation);
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
  deviceDetails.close();
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
  const barrier = agentState.beginDelete(agentID);
  if (!barrier) return;
  reconciliation.cancel(agentID);
  nameWriteControllers.get(agentID)?.abort();
  countryWriteControllers.get(agentID)?.abort();
  trafficResetControllers.get(agentID)?.abort();
  planWriteControllers.get(agentID)?.abort();
  selectorController.cancel(agentID);
  pendingTelemetry.delete(agentID);
  let removed = false;
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
    removed = true;
    agentState.finishDelete(barrier, true);
    editingAgentNames.delete(agentID); agentNameDrafts.delete(agentID);
    agentNameErrors.delete(agentID); agentNameEditGeneration.delete(agentID);
    agentActionErrors.delete(`${agentID}/google`); agentActionErrors.delete(`${agentID}/country`);
    if (pendingRemoveAgentID === agentID) removeAgentDialog.close();
    render();
    await refresh();
  } catch (error) {
    removeAgentError.classList.remove('hidden');
    removeAgentConfirm.disabled = false;
  } finally {
    if (!removed) {
      agentState.finishDelete(barrier, false);
      reconciliation.notify(agentID);
      render();
    }
  }
});

async function setAgentDisabled(agent, disabled, button) {
  if (!mutationCSRFToken || agent.revoked) return;
  const mutation = agentState.beginMutation(agent.agent_id, ['lifecycle']);
  if (!mutation) return;
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
    if (!finishResource(mutation, changed.agent)) return;
    render();
    await refresh();
  } catch (error) {
    button.disabled = false;
  } finally {
    finishResource(mutation);
  }
}

function googleCheckItems(agent) {
  const google = agent.google_status;
  const all = (value, tone) => [
    ['YouTube', value, tone],
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
  const items = [['YouTube', ...youtube]];
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
  panel.setAttribute('aria-label', 'YouTube 检测结果');
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
  const mutation = agentState.beginMutation(agent.agent_id, ['google']);
  if (!mutation) return;
  agentActionErrors.delete(`${agent.agent_id}/google`);
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
    finishResource(mutation, {google_status: {...agents.get(agent.agent_id)?.google_status, pending: true}});
    render();
  } catch (error) {
    button.textContent = '重新检测';
    button.disabled = false;
    feedback.textContent = error.message || '检测请求失败';
    agentActionErrors.set(`${agent.agent_id}/google`, feedback.textContent);
  } finally {
    finishResource(mutation);
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
const planEditor = PlanEditor.create({agents,
  read: () => ({traffic_enabled: planTrafficEnabled.checked, ...Object.fromEntries(Object.entries(planFields).map(([key,selector])=>[key,document.querySelector(selector).value]))}),
  onclose: () => { pendingPlanAgentID = ''; planEditGeneration++; }, onclear: () => clearPlan(),
});
const planRenewal = PlanRenewal.create({agents,
  canWrite: id => Boolean(mutationCSRFToken && agents.has(id) && !agents.get(id).revoked && !agentState.blocked(id) && !agentState.busy(id,'plan')),
  csrf: () => mutationCSRFToken, begin: id => agentState.beginMutation(id,['plan']),
  finish: (token,patch) => finishPlanResource(token,patch),
  changed: () => { if(!suspended)render(); },
  opening: () => { deviceDetails.close({restoreFocus:false}); selectorView.hide(); },
  edit: (id,trigger) => { const agent=agents.get(id);if(agent)openPlanDialog(agent,trigger); },
});
function openPlanRenewal(agent,trigger=null) {
  if(!agents.has(agent.agent_id)||agent.revoked||agentState.blocked(agent.agent_id))return;
  planEditor.handoff(()=>{if(agents.has(agent.agent_id)&&!agentState.blocked(agent.agent_id))planRenewal.open(agent.agent_id,trigger);});
}

function togglePlanTrafficFields() {
  planTrafficFields.classList.toggle('hidden', !planTrafficEnabled.checked);
}

function openPlanDialog(agent, trigger = null) {
  if(planRenewal.unconfirmed(agent.agent_id))return;
  planRenewal.close();
  planEditor.open(agent.agent_id,trigger,()=>{
  planEditGeneration++;
  pendingPlanAgentID = agent.agent_id;
  planForm.reset();
  const plan = agent.plan || {};
  planQuotaSession = PlanUnits.open(plan);
  planCountryPicker ||= Management.countryPicker({
    search: document.querySelector('#plan-country-search'), select: document.querySelector('#plan-country-code'),
    clear: document.querySelector('#plan-country-auto'), preview: document.querySelector('#plan-country-preview'),
    status: document.querySelector('#plan-country-status'), onchange: () => { planEditGeneration++; },
  });
  planCountryPicker.set(plan.country_code_override || '');
  planAgentName.textContent = agent.name || agent.state?.hostname || '未命名 Agent';
  planTrafficEnabled.checked = Boolean(plan.traffic_mode);
  for (const [key, selector] of Object.entries(planFields)) {
    const input = document.querySelector(selector);
    const fallback = key === 'cycle_count' ? '1' : key === 'timezone' ? (Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC') : '';
    input.value = key.startsWith('calibration_') ? (key === 'calibration_unit' ? 'GB' : '') : (plan[key] ?? fallback);
  }
  if (!plan.traffic_mode) document.querySelector('#plan-traffic-mode').value = 'sum';
  if (!plan.quota_unit) document.querySelector('#plan-quota-unit').value = 'TB';
  if (plan.quota_value) {
    document.querySelector('#plan-quota-value').value = planQuotaSession.shown.value;
    document.querySelector('#plan-quota-unit').value = planQuotaSession.shown.unit;
  }
  const quotaNote = document.querySelector('#plan-quota-note');
  quotaNote.textContent = planQuotaSession.legacy ? '旧额度已精确换算为十进制。未改额度保存时保留原字节定义；修改后的额度最多 3 位小数。' : '流量单位采用十进制：1 TB = 1000 GB；额度与校准最多 3 位小数。';
  if (!plan.cycle_kind) document.querySelector('#plan-cycle-kind').value = 'monthly';
  planError.classList.add('hidden');
  planError.textContent = '';
  togglePlanTrafficFields();
  });
}

planTrafficEnabled.addEventListener('change', togglePlanTrafficFields);
planForm.addEventListener('input', () => { planEditGeneration++; });
planForm.addEventListener('change', () => { planEditGeneration++; });

async function savePlanPayload(agentID, payload, signal) {
  return Management.write(`/api/v1/web/agents/${encodeURIComponent(agentID)}/plan`, {
    method: 'PUT', csrf: mutationCSRFToken, payload, signal, unauthorized: () => location.assign('/login'),
  });
}

planForm.addEventListener('submit', async event => {
  event.preventDefault();
  if (!pendingPlanAgentID || !mutationCSRFToken || !planEditor.canSubmit() || planRenewal.unconfirmed(pendingPlanAgentID)) return;
  let payload = {};
  for (const [key, selector] of Object.entries(planFields)) payload[key] = document.querySelector(selector).value.trim();
  try { payload = PlanUnits.payload(planQuotaSession, payload, planTrafficEnabled.checked); }
  catch (error) { planError.textContent = error.message; planError.classList.remove('hidden'); return; }
  const agentID = pendingPlanAgentID, editGeneration = planEditGeneration;
  const mutation = agentState.beginMutation(agentID, ['plan', 'country']);
  if (!mutation) return;
  const editorToken = planEditor.begin();
  const controller = new AbortController(); planWriteControllers.set(agentID, controller);
  planError.classList.add('hidden');
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
    const plan = await savePlanPayload(agentID, payload, controller.signal);
    planRenewal.invalidate(agentID);
    if (!finishPlanResource(mutation, {plan})) return;
    if (pendingPlanAgentID === agentID && planEditGeneration === editGeneration) planDialog.close();
    if(!suspended)render();
  } catch (error) {
    if (!controller.signal.aborted && !agentState.blocked(agentID) && pendingPlanAgentID === agentID && planEditGeneration === editGeneration) {
      planError.textContent = error.message || '套餐资料保存失败';
      planError.classList.remove('hidden');
    }
  } finally {
    if (planWriteControllers.get(agentID) === controller) planWriteControllers.delete(agentID);
    finishPlanResource(mutation);
    planEditor.finish(editorToken);
  }
});

async function clearPlan() {
  if (!pendingPlanAgentID || !mutationCSRFToken || !planEditor.canSubmit() || planRenewal.unconfirmed(pendingPlanAgentID)) return;
  const agentID = pendingPlanAgentID, editGeneration = planEditGeneration;
  const mutation = agentState.beginMutation(agentID, ['plan', 'country']);
  if (!mutation) return;
  const editorToken = planEditor.begin();
  const controller = new AbortController(); planWriteControllers.set(agentID, controller);
  try {
    await savePlanPayload(agentID, {}, controller.signal);
    planRenewal.invalidate(agentID);
    if (!finishPlanResource(mutation, {plan: null})) return;
    if (pendingPlanAgentID === agentID && planEditGeneration === editGeneration) planDialog.close();
    if(!suspended)render();
  } catch (error) {
    if (!controller.signal.aborted && !agentState.blocked(agentID) && pendingPlanAgentID === agentID && planEditGeneration === editGeneration) {
      planError.textContent = error.message || '套餐资料清除失败';
      planError.classList.remove('hidden');
    }
  } finally {
    if (planWriteControllers.get(agentID) === controller) planWriteControllers.delete(agentID);
    finishPlanResource(mutation);
    planEditor.finish(editorToken);
  }
}

function finishPlanResource(token,patch={}) {
  return suspended ? agentState.finishMutation(token,patch) : finishResource(token,patch);
}

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
  return CardMetrics.price(plan);
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
    const quota = PlanUnits.display(plan.quota_value,plan.quota_unit);
    value.textContent = plan.quota_bytes == null ? bytes(plan.usage_bytes) : `${bytes(plan.usage_bytes)} / ${quota.value} ${quota.unit}`;
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
    const sourceLabel = source === 'manual' ? '手动覆盖' : source === 'lookup' ? 'Agent 主动重取' : 'Agent 自动识别';
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

async function verifyCountryRequest(agent, button, feedback) {
  const agentID = agent.agent_id;
  if (countryWriteControllers.has(agentID) || agentState.blocked(agentID)) return;
  const controller = new AbortController(); countryWriteControllers.set(agentID, controller);
  button.disabled = true; feedback.textContent = '正在核实服务器上的单次识别记录…';
  let offerNew = false;
  try {
    const committed = await readAgentDetail(agentID, controller.signal);
    if (!committed || controller.signal.aborted || agentState.blocked(agentID)) return;
    agent = agents.get(agentID);
    const operation = agent?.country_code_lookup_operation;
    if (operation && operation.operation_id !== uncertainCountryRequests.get(agentID)) {
      uncertainCountryRequests.delete(agentID); agentActionErrors.delete(`${agentID}/country`);
    } else {
      offerNew = true;
      agentActionErrors.set(`${agentID}/country`, '服务器尚未确认前请求；无法断言失败。可再次核实，或明确确认发起新的单次识别。');
    }
  } catch (error) {
    if (!controller.signal.aborted && !agentState.blocked(agentID)) agentActionErrors.set(`${agentID}/country`, '核实失败，前请求仍不确定；不会自动重新提交。');
  } finally {
    if (countryWriteControllers.get(agentID) === controller) countryWriteControllers.delete(agentID);
    render();
  }
  if (offerNew && !controller.signal.aborted) await refreshCountryCodeLookup(agent, button, feedback, true);
}

async function refreshCountryCodeLookup(agent, button, feedback, newAfterVerification = false) {
  agent = agents.get(agent.agent_id);
  if (!agent || !mutationCSRFToken || !agent.management?.country_code_lookup || !agent.online
    || agent.disabled_at || agent.revoked || (uncertainCountryRequests.has(agent.agent_id) && !newAfterVerification)
    || ['requested', 'delivered'].includes(agent.country_code_lookup_operation?.status)) return;
  if (!confirm(newAfterVerification
    ? '前请求结果仍不确定，服务器未见新的操作记录；是否明确发起一个新的单次识别？这不是原请求重试，前请求可能仍执行；服务器会拒绝已有待执行操作。取消可继续核实。'
    : '将由 Agent 向 ipwho.is 发起一次 HTTPS 国家/地区识别请求，只回传 ISO 两字母代码。失败时保留当前值。继续？')) return;
  const mutation = agentState.beginMutation(agent.agent_id, ['country']);
  if (!mutation) return;
  const controller = new AbortController(); countryWriteControllers.set(agent.agent_id, controller);
  agentActionErrors.delete(`${agent.agent_id}/country`);
  button.disabled = true;
  button.textContent = '已触发…';
  feedback.textContent = '';
  try {
    const operation = await Management.write(`/api/v1/web/agents/${encodeURIComponent(agent.agent_id)}/country-code/refresh`, {
      csrf: mutationCSRFToken, payload: {}, signal: controller.signal, unauthorized: () => location.assign('/login'),
    });
    if (!finishResource(mutation, {country_code_lookup_operation: operation})) return;
    uncertainCountryRequests.delete(agent.agent_id);
    render();
  } catch (error) {
    if (controller.signal.aborted || agentState.blocked(agent.agent_id) || !agents.has(agent.agent_id)) return;
    if (!error.status || error.status >= 500) uncertainCountryRequests.set(agent.agent_id, agent.country_code_lookup_operation?.operation_id || '');
    button.textContent = '重新识别一次';
    button.disabled = false;
    feedback.textContent = uncertainCountryRequests.has(agent.agent_id)
      ? '提交结果不确定；不会自动重试。请刷新状态核实，当前国家/地区保留。'
      : error.message || '国家/地区识别请求失败';
    agentActionErrors.set(`${agent.agent_id}/country`, error.message || '国家/地区识别请求失败');
  } finally {
    if (countryWriteControllers.get(agent.agent_id) === controller) countryWriteControllers.delete(agent.agent_id);
    finishResource(mutation);
    reconciliation.notify(agent.agent_id);
    render();
  }
}

async function readMutationError(response) {
  try {
    const body = await response.json();
    return body.error?.message || body.message || response.statusText;
  } catch (error) {
    return response.statusText;
  }
}

async function saveAgentName(agent, input, saveButton) {
  const name = input.value.trim();
  if (!Management.validName(input.value)) {
    input.setCustomValidity('名称不能为空，最多 100 UTF-8 字节，且不能包含换行或 NUL');
    input.reportValidity();
    return;
  }
  input.setCustomValidity('');
  const mutation = agentState.beginMutation(agent.agent_id, ['name']);
  if (!mutation) return;
  const draft = input.value, editGeneration = agentNameEditGeneration.get(agent.agent_id);
  const controller = new AbortController();
  nameWriteControllers.set(agent.agent_id, controller);
  saveButton.disabled = true;
  try {
    const updated = await Management.write(`/api/v1/web/agents/${encodeURIComponent(agent.agent_id)}/name`, {
      method: 'PUT', csrf: mutationCSRFToken, payload: {name}, signal: controller.signal,
      unauthorized: () => location.assign('/login'),
    });
    if (!finishResource(mutation, {name: updated.name})) return;
    if (agentNameDrafts.get(agent.agent_id) === draft && agentNameEditGeneration.get(agent.agent_id) === editGeneration) {
      editingAgentNames.delete(agent.agent_id);
      agentNameDrafts.delete(agent.agent_id);
      agentNameErrors.delete(agent.agent_id);
    }
    render();
  } catch (error) {
    if (controller.signal.aborted || agentState.blocked(agent.agent_id) || !agents.has(agent.agent_id)
      || !editingAgentNames.has(agent.agent_id) || agentNameEditGeneration.get(agent.agent_id) !== editGeneration
      || agentNameDrafts.get(agent.agent_id) !== draft) return;
    agentNameErrors.set(agent.agent_id, error.message || '名称保存失败');
    input.setCustomValidity(error.message || '名称保存失败');
    input.reportValidity();
    saveButton.disabled = false;
  } finally {
    if (nameWriteControllers.get(agent.agent_id) === controller) nameWriteControllers.delete(agent.agent_id);
    finishResource(mutation);
    if (!controller.signal.aborted && agents.has(agent.agent_id) && !agentState.blocked(agent.agent_id)) render();
  }
}

function agentNameControl(agent, state) {
  if (!editingAgentNames.has(agent.agent_id)) {
    const group = document.createElement('span'); group.className = 'agent-name-display';
    const heading = document.createElement('h2');
    setReadableText(heading, agent.name || state.hostname || '未命名 VPS');
    const edit = document.createElement('button');
    edit.type = 'button'; edit.className = 'agent-name-edit'; edit.textContent = '✎'; edit.title = '编辑名称'; edit.setAttribute('aria-label', '编辑 Agent 名称');
    edit.disabled = !mutationCSRFToken || agent.revoked || agentState.blocked(agent.agent_id);
    edit.addEventListener('click', () => {
      editingAgentNames.add(agent.agent_id); agentNameDrafts.set(agent.agent_id, agents.get(agent.agent_id)?.name || '');
      agentNameEditGeneration.set(agent.agent_id, (agentNameEditGeneration.get(agent.agent_id) || 0) + 1);
      agentNameErrors.delete(agent.agent_id); render();
      container.querySelector(`[data-agent-id="${agent.agent_id}"] [data-focus-key="name-input"]`)?.focus({preventScroll: true});
    });
    group.append(heading, edit);
    return group;
  }
  const group = document.createElement('span'); group.className = 'agent-name-editor';
  const input = document.createElement('input');
  input.value = agentNameDrafts.get(agent.agent_id) ?? agent.name ?? '';
  input.maxLength = 100; input.setAttribute('aria-label', 'Agent 名称'); input.dataset.focusKey = 'name-input';
  input.setCustomValidity(agentNameErrors.get(agent.agent_id) || '');
  const save = document.createElement('button'); save.type = 'button'; save.textContent = '保存';
  save.disabled = agentState.busy(agent.agent_id, 'name') || agentState.blocked(agent.agent_id);
  const cancel = document.createElement('button'); cancel.type = 'button'; cancel.textContent = '取消';
  input.addEventListener('input', () => { agentNameDrafts.set(agent.agent_id, input.value); agentNameErrors.delete(agent.agent_id); });
  save.addEventListener('click', () => saveAgentName(agent, input, save));
  cancel.addEventListener('click', () => {
    agentNameEditGeneration.set(agent.agent_id, (agentNameEditGeneration.get(agent.agent_id) || 0) + 1);
    nameWriteControllers.get(agent.agent_id)?.abort();
    editingAgentNames.delete(agent.agent_id); agentNameDrafts.delete(agent.agent_id); agentNameErrors.delete(agent.agent_id); render();
  });
  input.addEventListener('keydown', event => {
    if (event.key === 'Enter') { event.preventDefault(); save.click(); }
    if (event.key === 'Escape') { event.preventDefault(); cancel.click(); }
  });
  group.append(input, save, cancel);
  return group;
}

function countryCodeLookupControls(agent) {
  const controls = document.createElement('div');
  controls.className = 'card-detail-actions country-lookup-controls';
  const button = document.createElement('button');
  button.type = 'button';
  const status = document.createElement('span');
  status.setAttribute('role', 'status');
  status.setAttribute('aria-live', 'polite');
  const operation = agent.country_code_lookup_operation;
  if (operation && uncertainCountryRequests.has(agent.agent_id)
    && operation.operation_id !== uncertainCountryRequests.get(agent.agent_id)) {
    uncertainCountryRequests.delete(agent.agent_id); agentActionErrors.delete(`${agent.agent_id}/country`);
  }
  const capable = Boolean(agent.management?.country_code_lookup);
  const pending = operation?.status === 'requested' || operation?.status === 'delivered';
  button.textContent = !capable ? '升级 Agent 后可重新识别' : pending ? '识别中…' : uncertainCountryRequests.has(agent.agent_id) ? '核实前次识别请求' : '重新识别一次';
  button.disabled = !mutationCSRFToken || !capable || !agent.online || Boolean(agent.disabled_at) || agent.revoked || pending || countryWriteControllers.has(agent.agent_id) || agentState.busy(agent.agent_id, 'country') || agentState.blocked(agent.agent_id);
  button.title = !capable ? '当前 Agent 版本不支持主动重取，需要升级' : !agent.online ? 'Agent 离线时不能触发' : pending ? '本次请求只会触发一次，等待结果或状态超时' : '手动触发一次 HTTPS 国家/地区识别';
  if (!capable) status.textContent = '需要升级 Agent 后才能主动重新识别。';
  else if (agent.revoked || agentState.blocked(agent.agent_id)) status.textContent = '设备已撤销或正在删除，不能触发识别。';
  else if (agent.disabled_at) status.textContent = '设备已暂停，不能触发识别。';
  else if (!agent.online) status.textContent = '设备离线，不能触发识别。';
  else if (operation?.status === 'requested') status.textContent = '已排队，等待 Agent 领取。';
  else if (operation?.status === 'delivered') status.textContent = 'Agent 已领取，等待一次性查询结果。';
  else if (operation?.status === 'unknown') status.textContent = '上次结果待核实；当前显示值未变。重新触发会创建新的单次请求。';
  else if (operation?.status === 'failed') status.textContent = '上次识别失败，已保留此前国家/地区。';
  else if (operation?.status === 'succeeded') status.textContent = `上次识别成功${operation.result_code ? `：${operation.result_code}` : ''}。`;
  else status.textContent = '不会自动查询；只有手动触发时才发起一次识别。';
  if (agent.plan?.country_code_override) status.textContent += ' 当前启用了手动覆盖，识别结果不会改变当前显示。';
  if (agentActionErrors.has(`${agent.agent_id}/country`)) status.textContent = agentActionErrors.get(`${agent.agent_id}/country`);
  if (uncertainCountryRequests.has(agent.agent_id)) status.textContent += ' 提交结果不确定，不会自动重试；请刷新状态核实，当前值保留。';
  button.addEventListener('click', () => uncertainCountryRequests.has(agent.agent_id)
    ? verifyCountryRequest(agent, button, status) : refreshCountryCodeLookup(agent, button, status));
  controls.append(button, status);
  return controls;
}

function render() {
  overview.update(agents,overviewReady);
  const focusedControl = document.activeElement?.closest?.('[data-focus-key]');
  const focusedAgentID = focusedControl?.closest?.('.agent-card')?.dataset.agentId || '';
  const focusedKey = focusedControl?.dataset.focusKey || '';
  const selection = focusedControl && typeof focusedControl.selectionStart === 'number'
    ? [focusedControl.selectionStart, focusedControl.selectionEnd, focusedControl.selectionDirection] : null;
  networkQuality.sync([...agents.keys()]);
  trafficCharts.sync([...agents.keys()]);
  cardObservations.sync([...agents.keys()]);
  container.replaceChildren();
  selectorView.refreshActive();
  const list = [...agents.values()].sort((left, right) =>
    (left.name || '').localeCompare(right.name || '') || left.agent_id.localeCompare(right.agent_id));
  empty.classList.toggle('hidden', list.length > 0);
  for (const agent of list) {
    const state = agent.state || {};
    const hasState = Boolean(agent.state);
    const metricsStale = Boolean(!agent.online || agent.disabled_at || state.stale);
    const observed = cardObservations.observe(agent);
    const oldMetrics = metricsStale || observed.stale;
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
    const name = document.createElement('div');
    name.className = 'name';
    const system = CardMetrics.os(state.os);
    const logo = document.createElement('img');
    logo.src = `/vendor/card-icons/${system.icon}.svg`;
    logo.alt = '';
    logo.setAttribute('aria-hidden', 'true');
    const systemText = document.createElement('span');
    setReadableText(systemText, system.text);
    name.append(logo, systemText);
    const identityText = document.createElement('div');
    identityText.className = 'identity-text';
    identityText.append(agentNameControl(agent, state), name);
    titleLine.append(countryMark(agent.country_code, agent.country_source), identityText);
    const runtime = document.createElement('small');
    runtime.className = 'card-runtime';
    runtime.textContent = !hasState ? '— · 等待首次上报' : oldMetrics ? `运行 ${duration(state.uptime)} · 旧数据 · 最后上报 ${seen(state.collected_at)}` : `运行 ${duration(state.uptime)}`;
    title.append(titleLine, runtime);
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
    const metricTone = percent => oldMetrics ? 'unknown' : CardMetrics.tone(percent);
    const diskBusy = state.disk_busy_percent;
    const diskRead = state.disk_read_rate;
    const diskWrite = state.disk_write_rate;
    metrics.append(
      CardMetrics.tile('cpu', 'CPU', hasState ? state.cpu_percent : null, state.cpu_cores ? `${state.cpu_cores} 核` : '核心数未知', observed.cpu, `steal ${CardMetrics.pct(state.cpu_steal_percent)}`, oldMetrics),
      CardMetrics.tile('memory', '内存', hasState ? state.ram_percent : null, `${CardMetrics.size(state.ram_used,false,true)} / ${CardMetrics.size(state.ram_total,false,true)}`, metricTone(state.ram_percent), `Swap ${CardMetrics.size(state.swap_used,false,true)} / ${CardMetrics.size(state.swap_total,false,true)}`, oldMetrics),
      CardMetrics.tile('disk', '磁盘', hasState ? state.disk_percent : null, `${CardMetrics.size(state.disk_used,false,true)} / ${CardMetrics.size(state.disk_total,false,true)}`, metricTone(state.disk_percent), '', oldMetrics),
      CardMetrics.tile('disk-io', '磁盘 I/O', diskBusy, `读 ${CardMetrics.size(diskRead,true,true)}`, observed.io, `写 ${CardMetrics.size(diskWrite,true,true)}`, oldMetrics),
    );
    const ioTile = metrics.querySelector('.metric-disk-io');
    ioTile.title = '读写速率为所选顶层逻辑设备合计；圆环为最忙磁盘的观测忙碌时间占比。';
    card.append(metrics);

    const network = document.createElement('div');
    network.className = 'network';
    for (const [label, arrow, rate, total] of [['下载', '↓', state.rx_rate, state.rx_total], ['上传', '↑', state.tx_rate, state.tx_total]]) {
      const box = document.createElement('div');
      const directionTone = oldMetrics ? 'unknown' : bandwidthTone(rate, agent.plan);
      box.className = `${label === '下载' ? 'network-rx' : 'network-tx'} tone-${directionTone}`;
      const networkName = document.createElement('span');
      networkName.textContent = `${arrow} ${label}${oldMetrics ? ' · 旧数据' : ''}`;
      const strong = document.createElement('strong');
      setReadableText(strong, oldMetrics ? '—' : CardMetrics.size(rate, true));
      const small = document.createElement('small');
      setReadableText(small, `${label === '下载' ? '入站' : '出站'} ${hasState ? CardMetrics.size(total) : '—'}${oldMetrics && hasState ? ' · 旧值' : ''}`);
      box.append(networkName, strong, small);
      network.append(box);
    }
    const bandwidth = document.createElement('span');
    bandwidth.className = 'network-bandwidth';
    bandwidth.textContent = `带宽 ${compactBandwidth(agent.plan)}`;
    network.append(bandwidth);
    network.append(trafficCharts.mount(agent));
    card.append(network);

    const googleTools = document.createElement('div');
    googleTools.className = 'google-selector-tools';
    googleTools.append(googleStatusPanel(agent));
    const selectorTrigger = selectorView.trigger(agent);
    if (selectorTrigger) googleTools.append(selectorTrigger);
    card.append(googleTools);
    selectorView.refresh(agent.agent_id);
    card.append(networkQuality.mount(agent));
    card.append(planPanel(agent.plan || {}));

    const deadline = planDeadline(agent.plan);
    const due = document.createElement('span');
    due.className = deadline.tone;
    due.textContent = deadline.label;

    const remove = document.createElement('button');
    remove.className = 'remove-agent';
    remove.dataset.agentId = agent.agent_id;
    remove.type = 'button';
    remove.textContent = '永久移除';
    remove.dataset.focusKey = 'remove';
    remove.disabled = !mutationCSRFToken || agentState.blocked(agent.agent_id);
    remove.addEventListener('click', () => openRemoveAgentDialog(agent));
    const stateAction = document.createElement('button');
    stateAction.className = 'agent-state-action';
    stateAction.dataset.agentId = agent.agent_id;
    stateAction.dataset.agentAction = agent.disabled_at ? 'enable' : 'disable';
    stateAction.type = 'button';
    stateAction.textContent = agent.disabled_at ? '恢复' : '暂停';
    stateAction.dataset.focusKey = 'state';
    stateAction.disabled = !mutationCSRFToken || agent.revoked || agentState.busy(agent.agent_id, 'lifecycle') || agentState.blocked(agent.agent_id);
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
    upgrade.disabled = !mutationCSRFToken || !agent.online || Boolean(agent.disabled_at) || !agent.upgrade_capable || !serverBuild.upgrade_eligible || !newer || Boolean(agent.upgrade && !['succeeded', 'failed', 'rolled_back'].includes(agent.upgrade.status)) || agentState.busy(agent.agent_id, 'upgrade') || agentState.blocked(agent.agent_id);
    upgrade.title = !agent.upgrade_capable ? '需要先在主机上完成 v0.8 bootstrap' : !agent.online ? 'Agent 离线时不能升级' : agent.disabled_at ? '请先恢复 Agent' : '升级到当前 Server 对应版本';
    upgrade.addEventListener('click', () => openUpgradeAgentDialog(agent));
    const detailPanel = document.createElement('section');
    detailPanel.className = 'card-detail-panel';
    detailPanel.hidden = true;
    const facts = document.createElement('div');
    facts.className = 'card-detail-facts';
    facts.append(
      detailFact('主机名', state.hostname),
      detailFact('系统', [state.os, state.arch].filter(Boolean).join(' · ')),
      detailFact('负载', hasState ? `${Number(state.load1 || 0).toFixed(2)} / ${Number(state.load5 || 0).toFixed(2)} / ${Number(state.load15 || 0).toFixed(2)}` : '—'),
      detailFact('CPU steal', state.cpu_steal_percent == null || metricsStale ? '—' : `${pct(state.cpu_steal_percent)} · 等待宿主机 CPU`),
      detailFact('Swap', hasState ? `${bytes(state.swap_used,false,true)} / ${bytes(state.swap_total,false,true)}` : '—'),
      detailFact('磁盘 I/O', state.disk_busy_percent == null || metricsStale ? '—' : `最忙 ${pct(state.disk_busy_percent)} · 读 ${bytes(state.disk_read_rate,true,true)} · 写 ${bytes(state.disk_write_rate,true,true)}`),
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
    historyLink.textContent = '旧版历史页面（兼容入口）';
    historyLink.dataset.focusKey = 'history';
    const recheck = document.createElement('button');
    recheck.type = 'button';
    recheck.textContent = agent.google_status?.pending ? '检测中…' : '重新检测';
    recheck.dataset.focusKey = 'recheck';
    recheck.disabled = !mutationCSRFToken || !agent.online || Boolean(agent.disabled_at) || agent.revoked || !agent.google_status?.supported || agent.google_status?.pending || agentState.busy(agent.agent_id, 'google') || agentState.blocked(agent.agent_id);
    const checkFeedback = document.createElement('span');
    checkFeedback.setAttribute('aria-live', 'polite');
    checkFeedback.textContent = agentActionErrors.get(`${agent.agent_id}/google`) || '';
    recheck.addEventListener('click', () => rerunGoogleStatus(agent, recheck, checkFeedback));
    detailActions.append(historyLink, recheck, checkFeedback);
    const management = document.createElement('div');
    management.className = 'agent-actions';
    if (!agent.revoked) management.append(upgrade, stateAction);
    management.append(remove);
    const resetTraffic = document.createElement('button');
    resetTraffic.type = 'button'; resetTraffic.dataset.focusKey = 'traffic-reset';
    resetTraffic.textContent = trafficResetRequests.has(agent.agent_id) ? '用原请求核实累计起点' : '重新开始累计统计';
    resetTraffic.disabled = !canResetTraffic(agent);
    resetTraffic.addEventListener('click', () => resetAccumulatedTraffic(agent.agent_id));
    const resetFeedback = document.createElement('span');resetFeedback.setAttribute('aria-live','polite');
    resetFeedback.textContent = trafficResetFeedback.get(agent.agent_id) || '只改变累计显示，不改历史或套餐周期用量';
    detailActions.append(resetTraffic,resetFeedback);
    detailPanel.append(detailActions, countryCodeLookupControls(agent), management);
    card.append(detailPanel);

    const detailToggle = document.createElement('button');
    detailToggle.className = 'detail-toggle';
    detailToggle.type = 'button';
    detailToggle.textContent = '详情';
    detailToggle.dataset.focusKey = 'manage';
    detailToggle.setAttribute('aria-haspopup', 'dialog');
    detailToggle.addEventListener('click', () => {planRenewal.close();deviceDetails.open(agent.agent_id,'management',detailToggle);});
    const editPlan = document.createElement('button');
    editPlan.className = 'edit-plan';
    editPlan.type = 'button';
    editPlan.textContent = agent.plan ? '编辑套餐' : '添加套餐';
    editPlan.dataset.focusKey = 'plan';
    editPlan.disabled = !mutationCSRFToken || agent.revoked || agentState.busy(agent.agent_id, 'plan') || agentState.blocked(agent.agent_id) || planRenewal.unconfirmed(agent.agent_id);
    editPlan.dataset.focusKey = 'plan';
    editPlan.addEventListener('click', () => openPlanDialog(agent,editPlan));
    const renewPlan = document.createElement('button');
    renewPlan.type='button';renewPlan.className='renew-plan';renewPlan.textContent='已续费';renewPlan.dataset.focusKey='renewal';
    renewPlan.setAttribute('aria-haspopup','dialog');
    renewPlan.disabled=!mutationCSRFToken||agent.revoked||agentState.busy(agent.agent_id,'plan')||agentState.blocked(agent.agent_id);
    renewPlan.addEventListener('click',()=>openPlanRenewal(agent,renewPlan));
    const actions = document.createElement('div');
    actions.className = 'card-actions';
    const price = document.createElement('span');
    price.className = 'footer-price';
    price.textContent = compactPrice(agent.plan);
    actions.append(price, due, renewPlan, editPlan, detailToggle);
    card.append(actions);
    container.append(card);
    deviceDetails.update(agent);
  }
  if (focusedAgentID && focusedKey) {
    for (const card of container.querySelectorAll('.agent-card')) {
      if (card.dataset.agentId === focusedAgentID) {
        const control = card.querySelector(`[data-focus-key="${focusedKey}"]`);
        control?.focus({preventScroll: true});
        if (selection) control?.setSelectionRange?.(...selection);
      }
    }
  }
  deviceDetails.sync();
  planEditor.sync();
  planRenewal.sync();
  for(const id of new Set([...trafficResetRequests.keys(),...trafficResetFeedback.keys(),...trafficResetControllers.keys()]))if(!agents.has(id)){trafficResetControllers.get(id)?.abort();trafficResetControllers.delete(id);trafficResetRequests.delete(id);trafficResetFeedback.delete(id);}
}

function canResetTraffic(agent) {
  return Boolean(mutationCSRFToken && agent?.online && agent.state && !agent.state.stale && agent.traffic
    && !agent.disabled_at && !agent.revoked && !agentState.busy(agent.agent_id,'traffic') && !agentState.blocked(agent.agent_id)
    && !trafficResetControllers.has(agent.agent_id));
}
async function resetAccumulatedTraffic(id) {
  const agent=agents.get(id);if(!canResetTraffic(agent))return;
  const oldRequest=trafficResetRequests.get(id);
  if(!confirm(oldRequest ? `使用原请求 ${oldRequest} 核实同一次累计重新起算，不创建新重置。继续？` : '只重新开始页面累计入站/出站统计；历史与套餐周期用量不会改变。继续？'))return;
  const mutation=agentState.beginMutation(id,['traffic']);if(!mutation)return;
  const verifying=Boolean(oldRequest),operationID=oldRequest||crypto.randomUUID().replaceAll('-','');trafficResetRequests.set(id,operationID);
  const controller=new AbortController();trafficResetControllers.set(id,controller);trafficResetFeedback.set(id,'正在记录新的累计起点…');render();
  try {
    const traffic=await Management.write(`/api/v1/web/agents/${encodeURIComponent(id)}/traffic/reset`,{
      csrf:mutationCSRFToken,payload:{request_id:operationID},signal:controller.signal,unauthorized:()=>location.assign('/login')});
    if(!finishResource(mutation,verifying?{}:{traffic}))return;
    trafficResetRequests.delete(id);trafficResetFeedback.set(id,verifying?'已确认原请求执行；正在核对当前累计，不使用旧回执覆盖新样本或后续起点。':'累计统计已从当前有效样本重新开始；套餐周期用量未改变。');
  } catch(error) {
    if(controller.signal.aborted||agentState.blocked(id)||!agents.has(id))return;
    const uncertain=!error.status||error.status>=500;if(!uncertain)trafficResetRequests.delete(id);
    trafficResetFeedback.set(id,uncertain?`响应不确定，保留原累计和请求 ${operationID}；不会自动重试，可用原请求核实。`:error.message||'无法重新开始累计统计');
  } finally {
    if(trafficResetControllers.get(id)===controller)trafficResetControllers.delete(id);
    agentState.finishMutation(mutation);reconciliation.notify(id);if(!suspended)render();
  }
}

function finishResource(token, patch = {}) {
  const committed = agentState.finishMutation(token, patch);
  if (committed) { reconciliation.notify(token.id); render(); }
  return committed;
}

async function hydrateDetails(records, context) {
  const pending = records;
  let next = 0;
  const worker = async () => {
    while (next < pending.length) {
      const record = pending[next++];
      const results = await Promise.allSettled([readAgentDetail(record.agent_id, context?.controller.signal, context),
        readAgentUpgrade(record.agent_id, context)]);
      if (results.some(result => result.status === 'rejected') && (!context || agentState.validCycle(context))) {
        stream.textContent = '部分资料读取失败'; stream.classList.remove('live');
      }
    }
  };
  const workers = Array.from({length: Math.min(8, pending.length)}, worker);
  await Promise.all(workers);
}

async function readAgentDetail(id, signal, context = null) {
  const ticket = agentState.read(id, context);
  const eventAtStart = pendingTelemetry.get(id);
  try {
    const detail = await readJSON(`/api/v1/web/agents/${encodeURIComponent(id)}`, signal);
    if (signal?.aborted) return false;
    const committed = agentState.commitDetail(ticket, {...detail, detail_loaded: true});
    if (committed) {
      const pending = pendingTelemetry.get(id);
      if (pending) {
        pendingTelemetry.delete(id);
        if (pending !== eventAtStart) agentState.event(pending);
      }
      render();
    }
    return committed;
  } catch (error) {
    if (signal?.aborted || error.name === 'AbortError') return false;
    if (error.status === 404) {
      if (agentState.missing(ticket)) { pendingTelemetry.delete(id); reconciliation.cancel(id); }
      return false;
    }
    throw error;
  }
}

async function readAgentUpgrade(id, context) {
  const ticket = agentState.read(id, context, ['upgrade']);
  try {
    const upgrade = await readOptionalJSON(`/api/v1/web/agents/${encodeURIComponent(id)}/upgrade`, context?.controller.signal);
    if (agentState.commitRead(ticket, {upgrade})) render();
  } catch (error) {
    if (!context?.controller.signal.aborted && error.name !== 'AbortError' && error.status !== 404) throw error;
  }
}

const pendingTelemetry = new Map();
const reconciliation = AgentState.reconcile(agentState, readAgentDetail, render, () => {
  stream.textContent = '部分资料读取失败'; stream.classList.remove('live');
});
async function refresh() {
  const context = agentState.beginRefresh();
  try {
    const snapshot = [];
    const seenCursors = new Set();
    let cursor = '';
    do {
      const suffix = cursor ? `&cursor=${encodeURIComponent(cursor)}` : '';
      const page = await readJSON(`/api/v1/web/agents?limit=100${suffix}`, context.controller.signal);
      for (const summary of page.items) {
        snapshot.push(summary);
      }
      cursor = page.next_cursor || '';
      if (cursor && seenCursors.has(cursor)) throw new Error('pagination loop');
      if (cursor) seenCursors.add(cursor);
    } while (cursor);
    if (!agentState.commitList(context, snapshot)) return;
    overviewReady = true;
    render();
    await hydrateDetails([...agents.values()], context);
    if (agentState.validCycle(context)) render();
  } catch (error) {
    if (!agentState.validCycle(context) || error.name === 'AbortError') return;
    stream.textContent = '读取失败';
    stream.classList.remove('live');
  } finally {
    agentState.endRefresh(context);
  }
}

loadMutationSession();
refresh();
let suspended = false;
setInterval(() => { if (suspended) return; reconciliation.retry(); if (!agentState.refreshing()) refresh(); }, 15000);
addEventListener('pagehide', () => { suspended = true; planRenewal.shutdown(); planEditor.shutdown(); deviceDetails.shutdown(); networkQuality.close(); trafficCharts.close(); monitorCoordinator.close(); for (const controller of [...planWriteControllers.values(), ...nameWriteControllers.values(), ...countryWriteControllers.values(), ...trafficResetControllers.values()]) controller.abort(); trafficResetControllers.clear();trafficResetRequests.clear();trafficResetFeedback.clear(); selectorController.closeAll(); events.close(); reconciliation.close(); pendingTelemetry.clear(); agentState.beginRefresh().controller.abort(); });
// A bfcache restore must not retain closed reconciliation or a dead SSE stream.
addEventListener('pageshow', event => { if (event.persisted) location.reload(); });

const events = new EventSource('/api/v1/web/events');
events.onopen = () => {
  if (suspended) return;
  stream.textContent = '实时连接';
  stream.classList.add('live');
  if (!agentState.refreshing()) refresh();
};
events.onerror = () => {
  stream.textContent = '正在重连';
  stream.classList.remove('live');
};
events.addEventListener('agent', event => {
  const update = JSON.parse(event.data);
  if (revokedAgentIDs.has(update.agent_id)) return;
  if (agentState.fullEvent(update)) reconciliation.notify(update.agent_id);
  else if (!agents.has(update.agent_id)) {
    pendingTelemetry.set(update.agent_id, update);
    reconciliation.notify(update.agent_id);
  } else if (agentState.event(update)) render();
});
