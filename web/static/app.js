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
const createdAgentID = document.querySelector('#created-agent-id');
const createdInstallCommand = document.querySelector('#created-install-command');
const createdEnrollment = document.querySelector('#created-enrollment');
const removeAgentDialog = document.querySelector('#remove-agent-dialog');
const removeAgentForm = document.querySelector('#remove-agent-form');
const removeAgentName = document.querySelector('#remove-agent-name');
const removeAgentID = document.querySelector('#remove-agent-id');
const removeAgentConfirm = document.querySelector('#remove-agent-confirm');
const removeAgentError = document.querySelector('#remove-agent-error');
const agents = new Map();
const revokedAgentIDs = new Set();
let mutationCSRFToken = '';
let pendingRemoveAgentID = '';

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

async function readJSON(path) {
  const response = await fetch(path, {cache: 'no-store'});
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
    addAgentButton.disabled = !mutationCSRFToken;
    render();
  } catch (error) {
    mutationCSRFToken = '';
    addAgentButton.disabled = true;
  }
}

function clearEnrollmentDialog() {
  createdAgentID.textContent = '';
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
    createdAgentID.textContent = enrollment.agent.agent_id;
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
  removeAgentID.textContent = '';
  removeAgentError.classList.add('hidden');
  removeAgentConfirm.disabled = false;
}

function openRemoveAgentDialog(agent) {
  clearRemoveAgentDialog();
  pendingRemoveAgentID = agent.agent_id;
  removeAgentName.textContent = agent.name || '未命名 Agent';
  removeAgentID.textContent = agent.agent_id;
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
    const card = document.createElement('article');
    card.className = 'card agent-card';
    card.dataset.agentId = agent.agent_id;
    card.setAttribute('aria-label', `Agent ${agent.name || agent.agent_id}`);
    const head = document.createElement('div');
    head.className = 'card-head';
    const title = document.createElement('div');
    title.className = 'card-identity';
    const heading = document.createElement('h2');
    setReadableText(heading, state.hostname || agent.name || '未知主机');
    const name = document.createElement('div');
    name.className = 'name';
    setReadableText(name, agent.name || agent.agent_id);
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
      metric('CPU', pct(state.cpu_percent)),
      metric('RAM', pct(state.ram_percent)),
      metric('Swap', pct(state.swap_percent)),
      metric('Disk', pct(state.disk_percent)),
      metric('Load', `${Number(state.load1 || 0).toFixed(2)} / ${Number(state.load5 || 0).toFixed(2)} / ${Number(state.load15 || 0).toFixed(2)}`),
    );
    card.append(metrics);

    const network = document.createElement('div');
    network.className = 'network';
    for (const [arrow, rate, total] of [['↓', state.rx_rate, state.rx_total], ['↑', state.tx_rate, state.tx_total]]) {
      const box = document.createElement('div');
      const strong = document.createElement('strong');
      setReadableText(strong, `${arrow} ${bytes(rate, true)}`);
      const small = document.createElement('small');
      setReadableText(small, `累计 ${bytes(total)}`);
      box.append(strong, document.createElement('br'), small);
      network.append(box);
    }
    card.append(network);

    const meta = document.createElement('div');
    meta.className = 'meta';
    for (const text of [
      `Uptime: ${duration(state.uptime)}`,
      `${state.os || '未知 OS'} / ${state.arch || '未知架构'}`,
      `Last Seen: ${seen(agent.last_seen)}`,
    ]) {
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
    const management = document.createElement('div');
    management.className = 'agent-actions';
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
  const pending = records.filter(record => !record.detail_loaded);
  let next = 0;
  const worker = async () => {
    while (next < pending.length) {
      const record = pending[next++];
      const detail = await readJSON(`/api/v1/web/agents/${encodeURIComponent(record.agent_id)}`);
      agents.set(record.agent_id, {...agents.get(record.agent_id), ...detail, detail_loaded: true});
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
