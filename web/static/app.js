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
const agents = new Map();
let mutationCSRFToken = '';

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
  strong.textContent = value;
  box.append(name, strong);
  return box;
}

function render() {
  container.replaceChildren();
  const list = [...agents.values()].sort((left, right) =>
    (left.name || '').localeCompare(right.name || '') || left.agent_id.localeCompare(right.agent_id));
  empty.classList.toggle('hidden', list.length > 0);
  for (const agent of list) {
    const state = agent.state || {};
    const card = document.createElement('article');
    card.className = 'card';
    const head = document.createElement('div');
    head.className = 'card-head';
    const title = document.createElement('div');
    const heading = document.createElement('h2');
    heading.textContent = state.hostname || agent.name;
    const name = document.createElement('div');
    name.className = 'name';
    name.textContent = agent.name || agent.agent_id;
    title.append(heading, name);
    const status = document.createElement('span');
    const stateName = agent.revoked ? 'revoked' : agent.online ? 'online' : 'offline';
    status.className = `status ${stateName}`;
    status.textContent = agent.revoked ? '● REVOKED' : agent.online ? '● ONLINE' : '● OFFLINE';
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
      strong.textContent = `${arrow} ${bytes(rate, true)}`;
      const small = document.createElement('small');
      small.textContent = `累计 ${bytes(total)}`;
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
      line.textContent = text;
      meta.append(line);
    }
    card.append(meta);

    const link = document.createElement('a');
    link.className = 'details';
    link.href = `/history.html?id=${encodeURIComponent(agent.agent_id)}`;
    link.textContent = '查看 24 小时历史 →';
    card.append(link);
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
  agents.set(update.agent_id, {...agents.get(update.agent_id), ...update, detail_loaded: true});
  render();
});
