const schedules = document.querySelector('#schedules');
const empty = document.querySelector('#schedule-empty');

async function readJSON(path) {
  const response = await fetch(path, {cache: 'no-store'});
  if (response.status === 401) {
    location.assign('/login');
    throw new Error('authentication required');
  }
  if (!response.ok) throw new Error(response.statusText);
  return response.json();
}

const timestamp = value => value ? new Date(value).toLocaleString() : '—';

function duration(seconds) {
  if (seconds % 86400 === 0) return `${seconds / 86400} 天`;
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`;
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`;
  return `${seconds} 秒`;
}

function target(schedule) {
  const config = schedule.config || {};
  switch (schedule.probe_type) {
    case 'http':
      return `${config.method} ${config.url}${config.expected_status ? ` · 期望 ${config.expected_status}` : ''}`;
    case 'tcp_connect':
      return `${config.host}:${config.port}`;
    case 'icmp_ping':
      return `${config.target} · ${config.count} 次`;
    default:
      return '未知配置';
  }
}

function addLine(container, label, value) {
  const line = document.createElement('div');
  const name = document.createElement('span');
  name.textContent = label;
  const content = document.createElement('strong');
  content.textContent = value;
  line.append(name, content);
  container.append(line);
}

function render(items) {
  schedules.replaceChildren();
  empty.classList.toggle('hidden', items.length > 0);
  if (!items.length) empty.textContent = '尚未配置 Schedule';
  for (const schedule of items) {
    const card = document.createElement('article');
    card.className = 'card schedule-card';
    const head = document.createElement('div');
    head.className = 'card-head';
    const title = document.createElement('div');
    const heading = document.createElement('h2');
    heading.textContent = schedule.name;
    const type = document.createElement('div');
    type.className = 'name';
    type.textContent = schedule.probe_type;
    title.append(heading, type);
    const status = document.createElement('span');
    status.className = `status ${schedule.enabled ? 'online' : 'offline'}`;
    status.textContent = schedule.enabled ? '● ENABLED' : '● DISABLED';
    head.append(title, status);
    card.append(head);

    const details = document.createElement('div');
    details.className = 'schedule-details';
    addLine(details, '目标', target(schedule));
    addLine(details, 'Agent', schedule.agent_id);
    addLine(details, '间隔', duration(schedule.interval_seconds));
    addLine(details, '超时', `${schedule.timeout_ms} ms`);
    addLine(details, '下次运行', schedule.enabled ? timestamp(schedule.next_run_at) : '已停用');
    addLine(details, '更新时间', timestamp(schedule.updated_at));
    card.append(details);
    schedules.append(card);
  }
}

async function load() {
  try {
    const summaries = [];
    const seenCursors = new Set();
    let cursor = '';
    do {
      const suffix = cursor ? `&cursor=${encodeURIComponent(cursor)}` : '';
      const page = await readJSON(`/api/v1/web/schedules?limit=100${suffix}`);
      summaries.push(...page.items);
      cursor = page.next_cursor || '';
      if (cursor && seenCursors.has(cursor)) throw new Error('pagination loop');
      if (cursor) seenCursors.add(cursor);
    } while (cursor);

    let next = 0;
    const details = new Array(summaries.length);
    const worker = async () => {
      while (next < summaries.length) {
        const index = next++;
        details[index] = await readJSON(`/api/v1/web/schedules/${encodeURIComponent(summaries[index].schedule_id)}`);
      }
    };
    await Promise.all(Array.from({length: Math.min(8, summaries.length)}, worker));
    render(details);
  } catch (error) {
    empty.textContent = '加载失败';
  }
}

load();
