const id = new URLSearchParams(location.search).get('id');
const empty = document.querySelector('#history-empty');
const outboundStatus = document.querySelector('#outbounds-status');
const outboundList = document.querySelector('#outbounds-list');
let mutationCSRFToken = '';
let currentAgent = null;
let switchingSelector = '';
let selectorFeedback = null;
const selectorOperations = new Map();

const selectorErrors = {
  selector_not_found: '本地 Selector 已不存在，请刷新后重试',
  choice_not_found: '本地选项已不存在，请刷新后重试',
  clash_api_unavailable: 'Agent 无法连接本地 Clash API',
  clash_api_unauthorized: 'Clash API 鉴权失败',
  switch_failed: 'Clash API 拒绝了切换',
  switch_verification_failed: '切换后的回读结果与目标不一致',
  selector_switch_pending: '该 Selector 已有等待或执行中的切换',
  agent_disabled: 'Agent 已暂停，无法执行切换',
  agent_revoked: 'Agent 已撤销，无法执行切换',
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
    await new Promise(resolve => setTimeout(resolve, 500));
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
  else if (!outbounds.available) outboundStatus.textContent = `Clash API 当前不可用 · 上次成功：${lastUpdated}`;
  else if (outbounds.stale) outboundStatus.textContent = `状态已过期 · 最后检查：${lastChecked}`;
  else outboundStatus.textContent = `可用 · 更新于 ${lastUpdated}`;
  for (const selector of outbounds.selectors || []) {
    const details = document.createElement('details');
    details.open = switchingSelector === selector.name || selectorFeedback?.selector === selector.name || selectorOperations.has(selector.name);
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
    for (const choice of selector.choices || []) {
      const option = document.createElement('option');
      option.value = choice;
      option.textContent = choice === selector.current ? `${choice}（当前）` : choice;
      option.selected = choice === selector.current;
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
    const blocked = !outbounds.available || outbounds.stale || agent.disabled_at || agent.revoked || !mutationCSRFToken || switchingSelector !== '' || persistedFeedback?.pending;
    const updateButton = () => {
      button.disabled = blocked || choices.value === selector.current;
    };
    choices.disabled = blocked;
    choices.addEventListener('change', updateButton);
    button.addEventListener('click', () => switchOutbound(selector.name, choices.value));
    updateButton();
    controls.append(choices, button, operationStatus);
    details.append(summary, controls);
    outboundList.append(details);
  }
  if (!outboundList.children.length && outbounds.available) {
    outboundStatus.textContent += ' · 未发现 Selector';
  }
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
  context.strokeStyle = '#253044';
  context.fillStyle = '#8f9bad';
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
    renderOutbounds(agent);
    const points = history.points;
    empty.classList.toggle('hidden', points.length > 0);
    if (!points.length) {
      empty.textContent = '最近 24 小时暂无数据';
      return;
    }
    const render = () => {
      draw(document.querySelector('#cpu'), points, 'cpu', '#53a7ff', value => `${value.toFixed(0)}%`);
      draw(document.querySelector('#ram'), points, 'ram_percent', '#43d17e', value => `${value.toFixed(0)}%`);
      draw(document.querySelector('#rx'), points, 'rx_rate', '#53a7ff', rate);
      draw(document.querySelector('#tx'), points, 'tx_rate', '#4bd7dc', rate);
    };
    render();
    addEventListener('resize', render);
  } catch (error) {
    empty.textContent = '加载失败';
    outboundStatus.textContent = '出站状态加载失败';
  }
}

load();
