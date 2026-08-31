const id = new URLSearchParams(location.search).get('id');
const empty = document.querySelector('#history-empty');
const outboundStatus = document.querySelector('#outbounds-status');
const outboundList = document.querySelector('#outbounds-list');

function renderOutbounds(outbounds) {
  outboundList.replaceChildren();
  if (!outbounds || !outbounds.configured) {
    outboundStatus.textContent = '未配置只读发现';
    return;
  }
  const lastUpdated = outbounds.updated_at ? new Date(outbounds.updated_at).toLocaleString() : '尚无成功快照';
  outboundStatus.textContent = outbounds.available
    ? `可用 · 更新于 ${lastUpdated}`
    : `当前不可用 · 上次成功：${lastUpdated}`;
  for (const selector of outbounds.selectors || []) {
    const details = document.createElement('details');
    const summary = document.createElement('summary');
    const name = document.createElement('strong');
    name.textContent = selector.name;
    const current = document.createElement('span');
    current.textContent = selector.current;
    summary.append(name, current);
    const choices = document.createElement('ul');
    for (const choice of selector.choices || []) {
      const item = document.createElement('li');
      item.textContent = choice;
      if (choice === selector.current) item.className = 'selected-outbound';
      choices.append(item);
    }
    details.append(summary, choices);
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
    const [agent, history] = await Promise.all([
      readJSON(`/api/v1/web/agents/${encodedID}`),
      readJSON(`/api/v1/web/agents/${encodedID}/history?hours=24`),
    ]);
    const state = agent.state || {};
    document.querySelector('#title').textContent = `${state.hostname || agent.name} · 历史`;
    renderOutbounds(agent.outbounds);
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
