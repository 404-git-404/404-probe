const jobs = document.querySelector('#jobs');
const empty = document.querySelector('#job-empty');
const loadMore = document.querySelector('#load-more');
let cursor = '';

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
const milliseconds = value => `${Number(value || 0).toFixed(2)} ms`;

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
  return [];
}

function showDetail(container, detail) {
  container.replaceChildren();
  addLine(container, '目标', jobTarget(detail));
  addLine(container, 'Not Before', timestamp(detail.not_before));
  addLine(container, 'Expires At', timestamp(detail.expires_at));
  if (detail.lease) addLine(container, 'Lease', `${timestamp(detail.lease.leased_at)} → ${timestamp(detail.lease.expires_at)}`);
  if (!detail.result) return;
  addLine(container, '执行耗时', milliseconds(detail.result.duration_ms));
  addLine(container, '执行时间', `${timestamp(detail.result.started_at)} → ${timestamp(detail.result.finished_at)}`);
  if (detail.result.resolved_ip) addLine(container, 'Resolved IP', detail.result.resolved_ip);
  if (detail.result.error_category) addLine(container, '错误类别', detail.result.error_category);
  if (detail.result.error_message) addLine(container, '错误信息', detail.result.error_message);
  for (const [label, value] of measurementLines(detail)) addLine(container, label, value);
}

function appendJob(job) {
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
  const failed = job.result_summary && !job.result_summary.success;
  status.className = `status ${failed || job.status === 'expired' ? 'offline' : job.status === 'finished' ? 'online' : ''}`;
  status.textContent = job.status.toUpperCase();
  head.append(title, status);
  card.append(head);

  const summary = document.createElement('div');
  summary.className = 'schedule-details';
  addLine(summary, 'Agent', job.agent_id);
  addLine(summary, '创建时间', timestamp(job.created_at));
  addLine(summary, '来源', job.schedule_id ? `Schedule ${job.schedule_id}` : 'One-shot');
  addLine(summary, 'Attempt', String(job.attempt));
  if (job.result_summary) {
    addLine(summary, '结果', `${job.result_summary.success ? '成功' : '失败'} · ${milliseconds(job.result_summary.duration_ms)}`);
    if (job.result_summary.error_category) addLine(summary, '错误类别', job.result_summary.error_category);
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
    const suffix = cursor ? `&cursor=${encodeURIComponent(cursor)}` : '';
    const page = await readJSON(`/api/v1/web/jobs?limit=50${suffix}`);
    for (const job of page.items) appendJob(job);
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
loadPage();
