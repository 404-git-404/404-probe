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
const agents = new Map();
const revokedAgentIDs = new Set();
let mutationCSRFToken = '';
let pendingRemoveAgentID = '';
let pendingUpgradeAgentID = '';
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
    createdInstallCommand.textContent = enrollment.install_command;
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

const googleLabels = {
  youtube: {unknown: 'UNKNOWN', cn: 'CN · SENT TO CHINA'},
  search: {unknown: 'UNKNOWN', ok: 'OK', challenge: 'CHALLENGE', blocked: 'BLOCKED'},
  signin: {unknown: 'UNKNOWN', reachable: 'REACHABLE', challenge: 'CHALLENGE', blocked: 'BLOCKED'},
  gemini: {unknown: 'UNKNOWN', available: 'AVAILABLE', blocked: 'BLOCKED'},
};

function googleStatusSummary(agent) {
  const google = agent.google_status;
  if (!google?.supported) return 'Google: Unsupported';
  if (!google.result) return google.pending ? 'Google: 检测中…' : 'Google: 尚未检测';
  const result = google.result;
  const yt = result.youtube?.status === 'not_cn' ? result.youtube.region : googleLabels.youtube[result.youtube?.status] || 'UNKNOWN';
  const search = googleLabels.search[result.search?.status] || 'UNKNOWN';
  const signin = googleLabels.signin[result.signin?.status] || 'UNKNOWN';
  let gemini = googleLabels.gemini[result.gemini?.status] || 'UNKNOWN';
  if (result.gemini?.status === 'available' && result.gemini.region) gemini += ` ${result.gemini.region}`;
  return `Google: YT ${yt} · Search ${search} · Login ${signin} · Gemini ${gemini}`;
}

function googleStatusState(google) {
  if (!google?.supported) return 'Unsupported';
  if (google.pending) return '检测中…';
  if (!google.result) return '尚未检测';
  const statuses = [google.result.youtube?.status, google.result.search?.status, google.result.signin?.status, google.result.gemini?.status];
  const unknown = statuses.filter(status => !status || status === 'unknown').length;
  if (unknown === statuses.length) return '检测失败';
  if (unknown) return '部分结果未知';
  if (google.stale) return '最后结果（stale）';
  return '';
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

function setReadableText(element, value, accessibleValue = value) {
  element.textContent = value;
  element.title = value;
  element.setAttribute('aria-label', accessibleValue);
}

function render() {
  container.replaceChildren();
  const list = [...agents.values()].sort((left, right) =>
    (left.name || '').localeCompare(right.name || '') || left.agent_id.localeCompare(right.agent_id));
  empty.classList.toggle('hidden', list.length > 0);
  for (const agent of list) {
    const state = agent.state || {};
    const metricsStale = Boolean(agent.disabled_at || state.stale);
    const card = document.createElement('article');
    card.className = 'card agent-card';
    card.dataset.agentId = agent.agent_id;
    card.setAttribute('aria-label', `Agent ${agent.name || '未命名'}`);
    const head = document.createElement('div');
    head.className = 'card-head';
    const title = document.createElement('div');
    title.className = 'card-identity';
    const heading = document.createElement('h2');
    setReadableText(heading, state.hostname || agent.name || '未知主机');
    const name = document.createElement('div');
    name.className = 'name';
    setReadableText(name, agent.name || '未命名 Agent');
    title.append(heading, name);
    const status = document.createElement('span');
    const stateName = agent.revoked ? 'revoked' : agent.disabled_at ? 'paused' : agent.online ? 'online' : 'offline';
    status.className = `status ${stateName}`;
    status.dataset.agentState = stateName;
    status.textContent = agent.revoked ? '● REVOKED' : agent.disabled_at ? '● PAUSED' : agent.online ? '● ONLINE' : '● OFFLINE';
    head.append(title, status);
    card.append(head);

    const metrics = document.createElement('div');
    metrics.className = 'metrics';
    metrics.append(
      metric('CPU', metricsStale ? '—' : pct(state.cpu_percent)),
      metric('RAM', metricsStale ? '—' : pct(state.ram_percent)),
      metric('Swap', metricsStale ? '—' : pct(state.swap_percent)),
      metric('Disk', metricsStale ? '—' : pct(state.disk_percent)),
      metric('Load', metricsStale ? '—' : `${Number(state.load1 || 0).toFixed(2)} / ${Number(state.load5 || 0).toFixed(2)} / ${Number(state.load15 || 0).toFixed(2)}`),
    );
    card.append(metrics);

    const network = document.createElement('div');
    network.className = 'network';
    for (const [arrow, rate, total] of [['↓', metricsStale ? 0 : state.rx_rate, state.rx_total], ['↑', metricsStale ? 0 : state.tx_rate, state.tx_total]]) {
      const box = document.createElement('div');
      const strong = document.createElement('strong');
      setReadableText(strong, `${arrow} ${bytes(rate, true)}`);
      const small = document.createElement('small');
      setReadableText(small, `累计 ${bytes(total)}`);
      box.append(strong, document.createElement('br'), small);
      network.append(box);
    }
    card.append(network);

    const google = document.createElement('div');
    google.className = 'google-status-compact';
    const googleText = document.createElement('div');
    googleText.className = 'google-service-values';
    setReadableText(googleText, googleStatusSummary(agent));
    if (agent.google_status?.result) {
      const result = agent.google_status.result;
      googleText.replaceChildren();
      for (const [label, value] of [
        ['YT', result.youtube.status === 'not_cn' ? result.youtube.region : googleLabels.youtube[result.youtube.status]],
        ['Search', googleLabels.search[result.search.status]],
        ['Sign-in', googleLabels.signin[result.signin.status]],
        ['Gemini', `${googleLabels.gemini[result.gemini.status]}${result.gemini.region ? ` [${result.gemini.region}]` : ''}`],
      ]) {
        const item = document.createElement('span');
        item.textContent = `${label}: ${value || 'UNKNOWN'}`;
        if (label === 'YT' && result.youtube.status === 'cn') item.className = 'google-cn';
        googleText.append(item);
      }
    }
    const googleFeedback = document.createElement('small');
    googleFeedback.textContent = googleStatusState(agent.google_status);
    const googleButton = document.createElement('button');
    googleButton.type = 'button';
    googleButton.textContent = agent.google_status?.pending ? '检测中…' : '重新检测';
    googleButton.disabled = !mutationCSRFToken || !agent.google_status?.supported || !agent.online || Boolean(agent.disabled_at) || agent.revoked || Boolean(agent.google_status?.pending);
    googleButton.addEventListener('click', () => rerunGoogleStatus(agent, googleButton, googleFeedback));
    google.append(googleText, googleFeedback, googleButton);
    card.append(google);

    const meta = document.createElement('div');
    meta.className = 'meta';
    for (const text of [
      `Uptime: ${duration(state.uptime)}`,
      `${state.os || '未知 OS'} / ${state.arch || '未知架构'}`,
      `Version: ${agent.version || 'unknown'}`,
      `Last Seen: ${seen(agent.last_seen)}`,
      agent.google_status?.checked_at ? `Google checked: ${seen(agent.google_status.checked_at)}${agent.google_status.stale ? ' · stale' : ''}` : '',
      agent.upgrade ? `Upgrade: ${upgradeLabel(agent.upgrade)}` : '',
    ]) {
      if (!text) continue;
      const line = document.createElement('span');
      line.className = 'meta-line';
      setReadableText(line, text);
      meta.append(line);
    }
    card.append(meta);

    const link = document.createElement('a');
    link.className = 'details';
    link.href = `/history.html?id=${encodeURIComponent(agent.agent_id)}`;
    link.textContent = '查看 24 小时历史 →';
    const remove = document.createElement('button');
    remove.className = 'remove-agent';
    remove.dataset.agentId = agent.agent_id;
    remove.type = 'button';
    remove.textContent = '永久移除';
    remove.disabled = !mutationCSRFToken;
    remove.addEventListener('click', () => openRemoveAgentDialog(agent));
    const stateAction = document.createElement('button');
    stateAction.className = 'agent-state-action';
    stateAction.dataset.agentId = agent.agent_id;
    stateAction.dataset.agentAction = agent.disabled_at ? 'enable' : 'disable';
    stateAction.type = 'button';
    stateAction.textContent = agent.disabled_at ? '恢复' : '暂停';
    stateAction.disabled = !mutationCSRFToken || agent.revoked;
    stateAction.addEventListener('click', () => setAgentDisabled(agent, !agent.disabled_at, stateAction));
    const upgrade = document.createElement('button');
    upgrade.className = 'upgrade-agent';
    upgrade.type = 'button';
    upgrade.textContent = '升级';
    const versionParts = value => /^v\d+\.\d+\.\d+$/.test(value || '') ? value.slice(1).split('.').map(Number) : null;
    const currentParts = versionParts(agent.version);
    const targetParts = versionParts(serverBuild.version);
    const newer = currentParts && targetParts && targetParts.some((part, index) => part > currentParts[index] && targetParts.slice(0, index).every((value, prior) => value === currentParts[prior]));
    upgrade.disabled = !mutationCSRFToken || !agent.online || Boolean(agent.disabled_at) || !agent.upgrade_capable || !serverBuild.upgrade_eligible || !newer || Boolean(agent.upgrade && !['succeeded', 'failed', 'rolled_back'].includes(agent.upgrade.status));
    upgrade.title = !agent.upgrade_capable ? '需要先在主机上完成 v0.8 bootstrap' : !agent.online ? 'Agent 离线时不能升级' : agent.disabled_at ? '请先恢复 Agent' : '升级到当前 Server 对应版本';
    upgrade.addEventListener('click', () => openUpgradeAgentDialog(agent));
    const management = document.createElement('div');
    management.className = 'agent-actions';
    if (!agent.revoked) management.append(upgrade);
    if (!agent.revoked) management.append(stateAction);
    management.append(remove);
    const actions = document.createElement('div');
    actions.className = 'card-actions';
    actions.append(link, management);
    card.append(actions);
    container.append(card);
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
