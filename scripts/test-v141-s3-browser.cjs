'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {test} = require('node:test');
const {launch, open, ID} = require('./lib/v11-panel-fixture.cjs');

const screenshots = process.env.V141_S3_SCREENSHOTS;
const agentPath = '/api/v1/web/agents/' + ID(0);

async function capture(page, name) {
  if (!screenshots) return;
  fs.mkdirSync(screenshots, {recursive: true});
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({path: path.join(screenshots, name + '.png'), animations: 'disabled'});
}

async function openDetail(page, tab) {
  await page.locator('[data-view=list]').click();
  await page.locator('.banner-name').click();
  await page.getByRole('tab', {name: tab, exact: true}).click();
}

test('S3 offline delete confirms the exact name and removes the Agent live in a second tab', {timeout: 90000}, async t => {
  const browser = await launch();
  t.after(() => browser.close());
  let panel;
  const requests = [];
  panel = await open(browser, {n: 1, width: 1440, height: 1100, handle({req, url, body, json}) {
    if (url.pathname === agentPath + '/upgrade') {
      json({status: 'succeeded', failure_message: '管理脚本未更新：下载 install.sh 失败'});
      return true;
    }
    if (url.pathname !== agentPath + '/delete') return false;
    requests.push({method: req.method, csrf: req.headers['x-csrf-token'], body});
    panel.f.state.items = [];
    panel.f.publish('dashboard', {agent_id: ID(0), kind: 'delete', revoked: false});
    json(null, 204);
    return true;
  }});
  t.after(() => panel.close());
  const {page, f, context} = panel;
  page.setDefaultTimeout(10000);
  const agent = f.state.items[0];
  Object.assign(agent, {online: true, disabled_at: null, name: 'S3 测试设备'});
  await page.evaluate(() => dashboardData.reload());
  await openDetail(page, '管理');
  const management = page.locator('#details-pane-management');
  const remove = management.getByRole('button', {name: '彻底删除', exact: true});
  await management.getByText('管理脚本未更新：下载 install.sh 失败', {exact: true}).waitFor();
  await remove.waitFor();
  await remove.scrollIntoViewIfNeeded();
  assert.equal(await remove.isDisabled(), true);
  assert.equal(await remove.getAttribute('title'), '设备在线时不能删除。先在机器上卸载 Agent 或等它离线');
  await capture(page, '01-在线时禁止删除');

  Object.assign(agent, {online: false, disabled_at: Date.now(), delete_blocked_online: true});
  f.publish('dashboard', {agent_id: ID(0), kind: 'disabled', revoked: false});
  await page.waitForFunction(() => agents.values().next().value.delete_blocked_online === true);
  assert.equal(await remove.isDisabled(), true);
  Object.assign(agent, {delete_blocked_online: false});
  f.publish('dashboard', {agent_id: ID(0), kind: 'paused-test-update', revoked: false});
  await page.waitForFunction(() => !document.querySelector('[data-focus-key=detail-delete-agent]').disabled);

  const other = await context.newPage();
  await other.goto(f.base, {waitUntil: 'domcontentloaded'});
  await other.waitForFunction(() => document.querySelector('#stream')?.classList.contains('live') && agents.size === 1);

  await remove.click();
  const dialog = page.getByRole('dialog', {name: '彻底删除设备'});
  const input = dialog.locator('input');
  const confirm = dialog.getByRole('button', {name: '彻底删除', exact: true});
  assert.equal(await confirm.isDisabled(), true);
  await input.fill(agent.name + ' ');
  assert.equal(await confirm.isDisabled(), true);
  await input.fill('S3 测试');
  assert.equal(await confirm.isDisabled(), true);
  await capture(page, '02-名称不一致无法确认');
  await input.fill(agent.name);
  assert.equal(await confirm.isEnabled(), true);
  await confirm.click();
  await dialog.waitFor({state: 'hidden'});
  await other.waitForFunction(() => agents.size === 0, null, {timeout: 3500});
  assert.deepEqual(requests, [{method: 'POST', csrf: 'synthetic-csrf', body: {name: agent.name}}]);
  assert.deepEqual(panel.errors, []);
});

test('S3 one-click only adds root collection when selected', {timeout: 90000}, async t => {
  const browser = await launch();
  t.after(() => browser.close());
  const panel = await open(browser, {n: 1, width: 1440, height: 1000});
  t.after(() => panel.close());
  const {page} = panel;
  page.setDefaultTimeout(10000);
  const requests = [];
  await page.route('**/api/v1/web/agent-enrollments', async route => {
    const body = route.request().postDataJSON();
    requests.push(body);
    const root = body.root_collector ? ' PROBE_404_SETUP_ROOT_COLLECTOR=1' : '';
    await route.fulfill({status: 201, json: {enrollment_id: 'd'.repeat(32), expires_at: Date.now()+1800000,
      command: `curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.4.1/install.sh | PROBE_404_ROLE=agent PROBE_404_SERVER=https://probe.example.test PROBE_404_ENROLL_TOKEN=${'S'.repeat(43)} PROBE_404_AGENT_NAME=test-node${root} bash`}});
  });
  const command = page.locator('#oneclick-command');
  const option = page.getByRole('checkbox', {name: '同时开启 root 采集'});
  for (const checked of [false, true]) {
    await page.locator('#add-agent').click();
    await page.locator('#add-agent-choice-name').fill('test-node');
    assert.equal(await option.isChecked(), false);
    if (checked) {
      await page.getByRole('radio', {name: /^手动安装/}).click();
      assert.equal(await option.isVisible(), false);
      await page.getByRole('radio', {name: /^一键安装/}).click();
      await option.check();
      await capture(page, '03-一键安装开启-root-采集');
    }
    await page.locator('#add-agent-next').click();
    await command.waitFor();
    assert.equal((await command.textContent()).includes(' PROBE_404_SETUP_ROOT_COLLECTOR=1 bash'), checked);
    await page.locator('#oneclick-done').click();
  }
  assert.deepEqual(requests, [{name: 'test-node'}, {name: 'test-node', root_collector: true}]);
  assert.deepEqual(panel.errors, []);
});

test('S3 DNS detail shows server results and supports per-address ignore', {timeout: 90000}, async t => {
  const browser = await launch();
  t.after(() => browser.close());
  let panel;
  const issue = {kind: 'dns', item: '127.0.0.53', level: 'danger', detail: '127.0.0.53 解析 probe.example.test 失败：超时'};
  const requests = [];
  panel = await open(browser, {n: 1, width: 1440, height: 1080, handle({req, url, body, json}) {
    if (url.pathname !== agentPath + '/system-check-ignores') return false;
    const ignored = req.method === 'PUT';
    requests.push({method: req.method, body});
    panel.f.state.items[0].system_attention = ignored ? {ignored: [issue]} : {dns: true, items: [issue]};
    panel.f.publish('dashboard', {agent_id: ID(0), kind: 'system-check-ignores', revoked: false});
    json({...body, ignored});
    return true;
  }});
  t.after(() => panel.close());
  const {page, f} = panel;
  page.setDefaultTimeout(10000);
  const agent = f.state.items[0];
  Object.assign(agent, {online: true, disabled_at: null, system_checks: {supported: true, ntp_status: 'ok', ntp_synchronized: true, time_offset_ms: 12}});
  await page.evaluate(() => dashboardData.reload());
  await openDetail(page, '系统');
  const dns = page.locator('#details-pane-system section').filter({has: page.getByRole('heading', {name: '本机 DNS', exact: true})});
  await dns.getByText('Agent 版本不支持', {exact: true}).waitFor();
  agent.system_checks.local_dns = {status: 'not_applicable'};
  f.publish('dashboard', {agent_id: ID(0), kind: 'dns-test-update', revoked: false});
  await dns.getByText('resolv.conf 里没有本机 DNS 服务器', {exact: true}).waitFor();
  const checked = Date.now();
  agent.system_checks.local_dns = {status: 'ok', query: 'probe.example.test', servers: [
    {address: '127.0.0.53', result: 'timeout', checked_at: checked, consecutive_failures: 2},
    {address: '::1', result: 'ok', checked_at: checked, consecutive_failures: 0},
  ]};
  agent.system_attention = {dns: true, items: [issue]};
  f.publish('dashboard', {agent_id: ID(0), kind: 'dns-test-update', revoked: false});
  const ignore = dns.getByRole('button', {name: '忽略 127.0.0.53', exact: true});
  await ignore.waitFor();
  assert.equal(await dns.locator('time').count(), 2);
  await dns.scrollIntoViewIfNeeded();
  await capture(page, '04-本机-DNS-告警');
  await ignore.click();
  const undo = dns.getByRole('button', {name: '取消忽略 127.0.0.53', exact: true});
  await undo.waitFor();
  assert.equal(await dns.locator('.system-ignore-muted').count(), 1);
  await capture(page, '05-本机-DNS-已忽略');
  await undo.click();
  await ignore.waitFor();
  assert.deepEqual(requests, ['PUT', 'DELETE'].map(method => ({method, body: {kind: 'dns', item: '127.0.0.53'}})));
  assert.deepEqual(panel.errors, []);
});
