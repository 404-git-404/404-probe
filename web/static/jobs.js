const jobs = document.querySelector('#jobs');
const empty = document.querySelector('#job-empty');
const loadMore = document.querySelector('#load-more');
let cursor = '';
let agentNamesPromise;

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

async function loadAgentNames() {
  const names = new Map();
  for (const status of ['', 'revoked']) {
    let nextCursor = '';
    do {
      const query = new URLSearchParams({limit: '100'});
      if (status) query.set('status', status);
      if (nextCursor) query.set('cursor', nextCursor);
      const page = await readJSON(`/api/v1/web/agents?${query}`);
      for (const agent of page.items || []) names.set(agent.agent_id, agent.name || '未命名 Agent');
      nextCursor = page.next_cursor || '';
    } while (nextCursor);
  }
  return names;
}
const milliseconds = value => `${Number(value || 0).toFixed(2)} ms`;
const operationLabels = {queued: '等待执行', running: '执行中', success: '成功', failed: '失败', expired: '已过期'};
const selectorErrorLabels = {
  selector_not_found: '本地 Selector 已不存在',
  choice_not_found: '本地选项已不存在',
  clash_api_unavailable: '本地 Clash API 不可用',
  clash_api_not_detected: '未检测到本地 Clash API',
  clash_api_auth_required: 'Clash API 已启用鉴权；请移除 secret',
  switch_failed: 'Clash API 切换失败',
  switch_verification_failed: '切换回读验证失败',
};

function addLine(container, label, value) {
  const line = document.createElement('div');
  const name = document.createElement('span');
  name.textContent = label;
  const content = document.createElement('strong');
  content.textContent = value;
  line.append(name, content);
  container.append(line);
}

function jobTarget(job) {
  const config = job.config || {};
  switch (job.probe_type) {
    case 'http':
      return `${config.method} ${config.url}${config.expected_status ? ` · 期望 ${config.expected_status}` : ''}`;
    case 'tcp_connect':
      return `${config.host}:${config.port}`;
    case 'icmp_ping':
      return `${config.target} · ${config.count} 次`;
    case 'singbox_selector_switch':
      return `${config.selector} → ${config.choice}`;
    case 'google_status':
      return 'Google Status';
    default:
      return '未知配置';
  }
}

function measurementLines(job) {
  const measurement = job.result?.measurement || {};
  if (measurement.http) {
    const value = measurement.http;
    return [
      ['HTTP 状态', String(value.status_code)],
      ['总耗时', milliseconds(value.total_ms)],
      ['DNS / Connect / TLS / TTFB', [value.dns_ms, value.connect_ms, value.tls_ms, value.ttfb_ms].map(milliseconds).join(' / ')],
      ['响应体', `${value.body_bytes} bytes${value.body_truncated ? ' · truncated' : ''}`],
    ];
  }
  if (measurement.tcp_connect) {
    return [['连接耗时', milliseconds(measurement.tcp_connect.connect_ms)]];
  }
  if (measurement.icmp_ping) {
    const value = measurement.icmp_ping;
    return [
      ['收发', `${value.received} / ${value.sent}`],
      ['丢包', `${Number(value.packet_loss_percent).toFixed(1)}%`],
      ['延迟 min / avg / max', [value.latency_min_ms, value.latency_avg_ms, value.latency_max_ms].map(milliseconds).join(' / ')],
    ];
  }
  if (measurement.selector_switch) {
    const value = measurement.selector_switch;
    return [['当前出站', value.current || '—'], ['执行方式', value.changed ? '已切换' : '幂等，无需切换']];
  }
  if (measurement.google_status) {
    const value = measurement.google_status;
    const youtube = value.youtube?.status === 'cn' ? 'CN · SENT TO CHINA' : value.youtube?.status === 'not_cn' ? value.youtube.region : 'UNKNOWN';
    const gemini = `${(value.gemini?.status || 'unknown').toUpperCase()}${value.gemini?.region ? ` [${value.gemini.region}]` : ''}`;
    return [
      ['YouTube', youtube],
      ['Google Search', (value.search?.status || 'unknown').toUpperCase()],
      ['Google Sign-in', (value.signin?.status || 'unknown').toUpperCase()],
      ['Gemini', gemini],
    ];
  }
  return [];
}

function showDetail(container, detail) {
  container.replaceChildren();
  addLine(container, '目标', jobTarget(detail));
  addLine(container, 'Not Before', timestamp(detail.not_before));
  addLine(container, 'Expires At', timestamp(detail.expires_at));
  if (detail.lease) addLine(container, 'Lease', `${timestamp(detail.lease.leased_at)} → ${timestamp(detail.lease.expires_at)}`);
  if (detail.probe_type === 'singbox_selector_switch' && detail.operation_status === 'expired') {
    addLine(container, '结果', '切换任务已过期（可能因暂停或超时）');
  }
  if (!detail.result) return;
  addLine(container, '执行耗时', milliseconds(detail.result.duration_ms));
  addLine(container, '执行时间', `${timestamp(detail.result.started_at)} → ${timestamp(detail.result.finished_at)}`);
  if (detail.result.resolved_ip) addLine(container, 'Resolved IP', detail.result.resolved_ip);
  if (detail.result.error_category) addLine(container, '错误类别', selectorErrorLabels[detail.result.error_category] || detail.result.error_category);
  if (detail.result.error_message) addLine(container, '错误信息', detail.result.error_message);
  for (const [label, value] of measurementLines(detail)) addLine(container, label, value);
}

function appendJob(job, agentNames) {
  const card = document.createElement('article');
  card.className = 'card job-card';
  const head = document.createElement('div');
  head.className = 'card-head';
  const title = document.createElement('div');
  const heading = document.createElement('h2');
  heading.textContent = job.probe_type;
  const id = document.createElement('div');
  id.className = 'name';
  id.textContent = job.job_id;
  title.append(heading, id);
  const status = document.createElement('span');
  const failed = job.operation_status === 'failed' || job.operation_status === 'expired';
  status.className = `status ${failed ? 'offline' : job.operation_status === 'success' ? 'online' : ''}`;
  status.textContent = (operationLabels[job.operation_status] || job.operation_status).toUpperCase();
  head.append(title, status);
  card.append(head);

  const summary = document.createElement('div');
  summary.className = 'schedule-details';
  addLine(summary, 'Agent', agentNames.get(job.agent_id) || '未知 Agent');
  addLine(summary, '创建时间', timestamp(job.created_at));
  addLine(summary, '来源', job.schedule_id ? `Schedule ${job.schedule_id}` : 'One-shot');
  addLine(summary, 'Attempt', String(job.attempt));
  if (job.result_summary) {
    addLine(summary, '结果', `${job.result_summary.success ? '成功' : '失败'} · ${milliseconds(job.result_summary.duration_ms)}`);
    if (job.result_summary.error_category) addLine(summary, '错误类别', selectorErrorLabels[job.result_summary.error_category] || job.result_summary.error_category);
  }
  card.append(summary);

  const detail = document.createElement('div');
  detail.className = 'job-detail hidden';
  const button = document.createElement('button');
  button.className = 'detail-button';
  button.type = 'button';
  button.textContent = '查看详情';
  button.addEventListener('click', async () => {
    button.disabled = true;
    try {
      const value = await readJSON(`/api/v1/web/jobs/${encodeURIComponent(job.job_id)}`);
      showDetail(detail, value);
      detail.classList.remove('hidden');
      button.remove();
    } catch (error) {
      button.textContent = '加载失败，请重试';
      button.disabled = false;
    }
  });
  card.append(detail, button);
  jobs.append(card);
}

async function loadPage() {
  loadMore.disabled = true;
  try {
    const agentNames = await agentNamesPromise;
    const suffix = cursor ? `&cursor=${encodeURIComponent(cursor)}` : '';
    const page = await readJSON(`/api/v1/web/jobs?limit=50${suffix}`);
    for (const job of page.items) appendJob(job, agentNames);
    cursor = page.next_cursor || '';
    empty.classList.toggle('hidden', jobs.childElementCount > 0);
    if (!jobs.childElementCount) empty.textContent = '尚无 Probe Job';
    loadMore.classList.toggle('hidden', !cursor);
    loadMore.textContent = '加载更多';
  } catch (error) {
    if (!jobs.childElementCount) empty.textContent = '加载失败';
    else loadMore.textContent = '加载失败，请重试';
  } finally {
    loadMore.disabled = false;
  }
}

loadMore.addEventListener('click', loadPage);
agentNamesPromise = loadAgentNames();
loadPage();
