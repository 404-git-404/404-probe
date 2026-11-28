'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {test} = require('node:test');
const {launch, open, ID} = require('./lib/v11-panel-fixture.cjs');

const screenshots = process.env.V141_S2_CX_SCREENSHOTS;
const day = 86400000;

async function capture(page, name) {
  if (!screenshots) return;
  fs.mkdirSync(screenshots, {recursive: true});
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({path: path.join(screenshots, name + '.png'), animations: 'disabled'});
}

test('S2 system tab ignores individual issues, refreshes and shows failures inline', {timeout: 90000}, async t => {
  const browser = await launch();
  t.after(() => browser.close());
  const now = Date.now();
  const issues = [
    {kind: 'service', item: 'exim4.service', level: 'danger', detail: 'exim4.service 处于 failed 状态'},
    {kind: 'service', item: 'backup.timer', level: 'danger', detail: 'backup.timer 处于 failed 状态'},
    {kind: 'service_restart', item: 'worker.service', level: 'warning', detail: '近 10 分钟重启 2 次'},
    {kind: 'certificate', item: '/etc/certs/node.crt', level: 'warning', detail: '证书即将到期'},
    {kind: 'time', item: 'time', level: 'warning', detail: 'NTP 未同步，时间偏差 +7.2 秒'},
  ];
  const ignored = new Set();
  const requests = [];
  let changeFails = false;
  let panel;
  function updateAttention() {
    const row = panel.f.state.items[0];
    const pending = issues.filter(item => !ignored.has(item.item));
    row.system_attention = {
      service: pending.some(item => item.kind === 'service'),
      service_restart: pending.find(item => item.kind === 'service_restart')?.level,
      certificate: pending.find(item => item.kind === 'certificate')?.level,
      time: pending.some(item => item.kind === 'time'),
      items: pending,
      ignored: issues.filter(item => ignored.has(item.item)),
    };
  }
  panel = await open(browser, {
    n: 1, theme: 'light', width: 1640, height: 1100,
    handle({req, url, body, json}) {
      if (url.pathname !== '/api/v1/web/agents/' + ID(0) + '/system-check-ignores') return false;
      requests.push({method: req.method, csrf: req.headers['x-csrf-token'], body});
      if (changeFails) {
        json({error: {code: 'internal_error'}}, 500);
        return true;
      }
      if (req.method === 'PUT') ignored.add(body.item);
      if (req.method === 'DELETE') ignored.delete(body.item);
      updateAttention();
      panel.f.publish('dashboard', {agent_id: ID(0), kind: 'system-check-ignores', revoked: false});
      json({...body, ignored: req.method === 'PUT'});
      return true;
    },
  });
  t.after(() => panel.close());
  const {page, f} = panel;
  page.setDefaultTimeout(10000);
  Object.assign(f.state.items[0], {
    online: true, disabled_at: null, network: {active: false},
    system_checks: {
      supported: true, root_status: 'ok', service_status: 'ok',
      failed_units: ['exim4.service', 'backup.timer'], services: [],
      service_restarts: [{unit: 'worker.service', count: 2}],
      certificates: [{domains: 'node.example.test', expires_at: now + 4 * day, path: '/etc/certs/node.crt'}],
      ntp_synchronized: false, ntp_status: 'ok', time_offset_ms: 7200,
      packages: {status: 'ok', security: 0, upgradable: 0},
    },
  });
  updateAttention();
  await page.evaluate(() => dashboardData.reload());
  const card = page.locator('.agent-card');
  const serviceBadge = card.getByText('⚠ 服务异常', {exact: true});
  await serviceBadge.waitFor();
  await page.locator('[data-view=list]').click();
  await page.locator('.banner-name').click();
  await page.getByRole('tab', {name: '系统', exact: true}).click();
  const pane = page.locator('#details-pane-system');
  const button = (label, item) => pane.getByRole('button', {name: label + ' ' + item, exact: true});
  const change = async (item, undo = false) => {
    await button(undo ? '取消忽略' : '忽略', item).click();
    await button(undo ? '忽略' : '取消忽略', item).waitFor();
  };
  const first = 'exim4.service';
  await button('忽略', first).waitFor();
  assert.equal(await pane.getByRole('button', {name: /^忽略 /}).count(), 5);
  await capture(page, '01-问题未忽略');

  changeFails = true;
  await button('忽略', first).click();
  await pane.getByText('保存失败，请重试', {exact: true}).waitFor();
  assert.equal(ignored.size, 0);
  changeFails = false;
  await change(first);
  await pane.locator('.system-ignore-muted').getByText('已忽略', {exact: true}).waitFor();
  assert.equal(await button('忽略', 'backup.timer').count(), 1);
  await capture(page, '02-已忽略');

  await change('backup.timer');
  await change('worker.service 重启');
  await change('/etc/certs/node.crt');
  await change('时间同步');
  await pane.getByText('无告警', {exact: true}).waitFor();
  await page.locator('.banner-row').getByText('⚠ 服务异常', {exact: true}).waitFor({state: 'detached'});
  await change(first, true);
  await page.locator('.banner-row').getByText('⚠ 服务异常', {exact: true}).waitFor();
  assert.deepEqual(requests[0], {method: 'PUT', csrf: 'synthetic-csrf', body: {kind: 'service', item: first}});
  assert.deepEqual(requests.at(-1), {method: 'DELETE', csrf: 'synthetic-csrf', body: {kind: 'service', item: first}});

  const row = f.state.items[0];
  row.system_checks.certificates[0].expires_at = now + 15 * day;
  row.system_attention.ignored = row.system_attention.ignored.filter(item => item.kind !== 'certificate');
  f.publish('dashboard', {agent_id: ID(0), kind: 'system-checks', revoked: false});
  await button('取消忽略', '/etc/certs/node.crt').waitFor({state: 'detached'});
  assert.deepEqual(panel.errors, []);
});

test('S2 evening with no samples is displayed as excluded', {timeout: 60000}, async t => {
  const browser = await launch();
  t.after(() => browser.close());
  const panel = await open(browser, {
    n: 1,
    handle({url, json}) {
      if (url.pathname !== '/api/v1/web/scores') return false;
      const network = {score: 100, grade: 'SSS', parts: [
        {key: 'availability', score: 100}, {key: 'evening', score: null, reason: 'no_evening_data'},
      ]};
      const overall = {score: 100, grade: 'SSS', parts: [{key: 'network', score: 100}]};
      const now = Date.now();
      json({computed_at: now, server_now: now, items: {[ID(0)]: {network, overall}}});
      return true;
    },
  });
  t.after(() => panel.close());
  const {page} = panel;
  page.setDefaultTimeout(10000);
  await page.locator('.score-stamp-card').click();
  const breakdown = page.locator('#device-details .score-breakdown');
  await breakdown.getByText('晚高峰 未计入 · 还没有晚高峰数据', {exact: true}).waitFor();
  assert.doesNotMatch(await breakdown.textContent(), /晚高峰\s*0/);
  assert.deepEqual(panel.errors, []);
});
