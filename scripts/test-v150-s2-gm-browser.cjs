// v1.5.0 GM-S2 acceptance suite: real loopback Server + isolated SQLite + real Chromium.
// Tests Settings -> Network Targets, Device Network Quality editor (library picker, inline create,
// 8-target cap, jump to library, copy & save), and CSS-styled medal SVG across themes and skins.
'use strict';
const {test, before, after} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const cp = require('node:child_process');
const http = require('node:http');
const crypto = require('node:crypto');

const playwrightPath = process.env.M02_PLAYWRIGHT || (() => { throw Error('Set M02_PLAYWRIGHT'); })();
const chromiumPath = process.env.M02_CHROMIUM || (() => { throw Error('Set M02_CHROMIUM'); })();
const serverExe = process.env.M02_SERVER || (() => { throw Error('Set M02_SERVER'); })();
const screensDir = process.env.V150_S2_GM_SCREENS || '';

const {chromium} = require(playwrightPath);
const {listen} = require('./lib/browser-safe-port.cjs');

let browser;
before(async () => {
  browser = await chromium.launch({headless: true, executablePath: chromiumPath});
});
after(async () => {
  await browser?.close();
});

const delay = ms => new Promise(r => setTimeout(r, ms));

async function capture(page, name, locator = null) {
  if (!screensDir) return;
  fs.mkdirSync(screensDir, {recursive: true});
  await page.mouse.move(0, 0);
  await page.evaluate(() => document.fonts.ready);
  await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
  const outPath = path.join(screensDir, name + '.png');
  if (locator) await locator.screenshot({path: outPath, animations: 'disabled'});
  else await page.screenshot({path: outPath, animations: 'disabled'});
}

async function startRealServer() {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'v150-s2-gm-'));
  const db = path.join(tmp, 'isolated.sqlite');
  const hash = path.join(tmp, 'password.hash');
  const password = crypto.randomBytes(24).toString('hex');
  fs.writeFileSync(path.join(tmp, 'update-check.json'), JSON.stringify({enabled: false, latest: '', checked_at: 0}), {mode: 0o600});
  fs.writeFileSync(hash, cp.execFileSync(serverExe, ['web', 'password-hash'], {input: password + '\n', windowsHide: true}), {mode: 0o600});

  const portProbe = http.createServer();
  await listen(portProbe);
  const port = portProbe.address().port;
  await new Promise(r => portProbe.close(r));
  const base = 'http://127.0.0.1:' + port;

  const child = cp.spawn(serverExe, [
    'serve',
    '--listen', '127.0.0.1:' + port,
    '--db', db,
    '--web-password-hash-file', hash,
    '--web-public-origin', base,
    '--web-allow-insecure-http',
  ], {windowsHide: true, stdio: ['ignore', 'pipe', 'pipe']});

  let log = '';
  for (const s of [child.stdout, child.stderr]) s.on('data', b => { log = (log + b.toString()).slice(-16000); });
  for (let i = 0; ; i++) {
    try {
      const res = await fetch(base + '/login');
      if (res.ok) break;
    } catch { /* wait */ }
    if (child.exitCode !== null || i >= 400) {
      throw new Error(child.exitCode !== null ? `Server exited ${child.exitCode}: ${log}` : 'Server startup timeout');
    }
    await delay(50);
  }

  return {
    base,
    password,
    async close() {
      if (child.exitCode === null) {
        child.kill();
        await new Promise(r => child.once('exit', r));
      }
      fs.rmSync(tmp, {recursive: true, force: true});
    },
  };
}

async function loginAndOpen(server, {theme = 'light', width = 1440, height = 960} = {}) {
  const context = await browser.newContext({viewport: {width, height}, locale: 'zh-CN'});
  await context.route('**/*', route => new URL(route.request().url()).origin === server.base ? route.continue() : route.abort());
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  const pageErrors = [];
  page.on('pageerror', e => pageErrors.push(e.message));

  await page.goto(server.base + '/login', {waitUntil: 'domcontentloaded'});
  await page.locator('#password').fill(server.password);
  await page.locator('button[type=submit]').click();
  await page.waitForFunction(() => typeof dashboardData !== 'undefined' && dashboardData.ready());
  await page.evaluate(t => window.ProbeTheme.set(t), theme);
  await page.waitForFunction(t => document.documentElement.dataset.theme === t, theme);
  return {context, page, pageErrors};
}

async function apiCall(page, method, pathname, body) {
  return page.evaluate(async ({method, pathname, body}) => {
    const sess = await fetch('/api/v1/web/session', {cache: 'no-store'}).then(r => r.json());
    const headers = {};
    if (method !== 'GET') headers['X-CSRF-Token'] = sess.csrf_token;
    const init = {method, cache: 'no-store', headers};
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = typeof body === 'string' ? body : JSON.stringify(body);
    }
    const res = await fetch(pathname, init);
    const json = res.status === 204 ? null : await res.json().catch(() => null);
    return {status: res.status, json};
  }, {method, pathname, body});
}

test('Settings -> Network Targets lifecycle, conflicts, limits, skipped reasons, and screenshots (light & dark)', {timeout: 180000}, async () => {
  const srv = await startRealServer();
  try {
    const {context, page, pageErrors} = await loginAndOpen(srv, {theme: 'light'});
    try {
      // Create two real synthetic devices via real API.
      const devA = (await apiCall(page, 'POST', '/api/v1/web/agents', {name: 'Synthetic-HK-01'})).json?.agent;
      const devB = (await apiCall(page, 'POST', '/api/v1/web/agents', {name: 'Synthetic-JP-02'})).json?.agent;
      assert.ok(devA?.agent_id && devB?.agent_id);
      await page.evaluate(() => dashboardData.reload());
      await page.waitForFunction(() => agents.size === 2 && !renderFrame);

      // Verify sidebar settings order includes "网络目标" right after "修改密码".
      await page.locator('#dashboard-settings > summary').click();
      const menuItems = await page.locator('#dashboard-settings [data-settings]').evaluateAll(ns =>
        ns.map(n => ({key: n.dataset.settings, text: n.textContent.trim()}))
      );
      assert.deepEqual(menuItems, [
        {key: 'update', text: '检查新版本'},
        {key: 'password', text: '修改密码'},
        {key: 'targets', text: '网络目标'},
        {key: 'regions', text: 'YouTube 无广告地区名单'},
        {key: 'backup', text: '备份'},
        {key: 'appearance', text: '外观'},
        {key: 'about', text: '关于'},
      ]);

      // Open Network Targets page -> empty state.
      await page.locator('[data-settings="targets"]').click();
      await page.locator('#nt-empty:not([hidden])').waitFor();
      assert.equal(await page.locator('#nt-empty').textContent(), '还没有网络目标。新建一个，然后应用到设备。');
      assert.equal(await page.locator('#nt-count').textContent(), '0 / 64');
      assert.equal(await page.locator('#nt-create').isEnabled(), true);
      await capture(page, 'light-targets-empty');

      await page.evaluate(() => window.ProbeTheme.set('dark'));
      await page.waitForFunction(() => document.documentElement.dataset.theme === 'dark');
      await capture(page, 'dark-targets-empty');
      await page.evaluate(() => window.ProbeTheme.set('light'));
      await page.waitForFunction(() => document.documentElement.dataset.theme === 'light');

      // Create dialog: verify field errors from real Server, capture light & dark, then create valid target.
      await page.locator('#nt-create').click();
      const editDialog = page.locator('#nt-edit-dialog');
      await editDialog.waitFor();
      assert.equal(await editDialog.locator('#nt-field-port').inputValue(), '443');
      await editDialog.locator('#nt-field-region').fill('');
      await editDialog.locator('#nt-field-isp').fill('电信');
      await editDialog.locator('#nt-field-host').fill('127.0.0.1');
      await editDialog.locator('#nt-field-port').fill('0');
      await editDialog.getByRole('button', {name: '保存'}).click();
      await editDialog.locator('[data-nt-error="host"]:not(:empty)').waitFor();
      assert.equal(await editDialog.locator('[data-nt-error="region"]').textContent(), '已填地址时必须填写');
      assert.equal(await editDialog.locator('[data-nt-error="host"]').textContent(), '地址不是公网地址');
      assert.equal(await editDialog.locator('[data-nt-error="port"]').textContent(), '端口须为 1–65535');

      await editDialog.locator('#nt-field-region').fill('华东');
      await editDialog.locator('#nt-field-host').fill('2606:4700:4700::1111');
      await editDialog.locator('#nt-field-port').fill('443');
      await capture(page, 'light-target-create-dialog', editDialog);
      await page.evaluate(() => window.ProbeTheme.set('dark'));
      await capture(page, 'dark-target-create-dialog', editDialog);
      await page.evaluate(() => window.ProbeTheme.set('light'));

      await editDialog.getByRole('button', {name: '保存'}).click();
      await editDialog.waitFor({state: 'detached'});
      await page.locator('#nt-tbody tr').first().waitFor();
      assert.equal(await page.locator('#nt-count').textContent(), '1 / 64');
      assert.equal(await page.locator('#nt-tbody tr .targets-unused').textContent(), '未使用');

      // Duplicate target 409 -> "已有相同的目标" (non-canonical IPv6 form normalizes to same host).
      await page.locator('#nt-create').click();
      await editDialog.waitFor();
      await editDialog.locator('#nt-field-region').fill('华东');
      await editDialog.locator('#nt-field-isp').fill('电信');
      await editDialog.locator('#nt-field-host').fill('2606:4700:4700:0:0:0:0:1111');
      await editDialog.locator('#nt-field-port').fill('443');
      await editDialog.getByRole('button', {name: '保存'}).click();
      await editDialog.locator('.dashboard-dialog-error:not([hidden])').waitFor();
      assert.equal(await editDialog.locator('.dashboard-dialog-error').textContent(), '已有相同的目标');
      await editDialog.getByRole('button', {name: '取消'}).click();
      await page.locator('#action-confirm').getByRole('button', {name: '放弃修改'}).click();
      await editDialog.waitFor({state: 'detached'});

      // Create a second unused target so list screenshot shows both used and unused rows.
      const secondCreated = await apiCall(page, 'POST', '/api/v1/web/network-targets', {
        region: '日本', isp: 'NTT', host: '8.8.8.8', port: 443,
      });
      assert.equal(secondCreated.status, 201);
      await page.locator('[data-settings="about"]').click();
      await page.locator('[data-settings="targets"]').click();
      await page.waitForFunction(() => document.querySelectorAll('#nt-tbody tr').length === 2);

      // Apply first target (华东 电信) to both devices.
      const firstRow = page.locator('#nt-tbody tr').filter({hasText: '华东'});
      await firstRow.locator('[data-nt-action="apply"]').click();
      const applyDialog = page.locator('#nt-apply-dialog');
      await applyDialog.waitFor();
      assert.equal(await applyDialog.locator('.nt-apply-row').count(), 2);
      await applyDialog.locator('.nt-apply-row input[type=checkbox]').nth(0).check();
      await applyDialog.locator('.nt-apply-row input[type=checkbox]').nth(1).check();
      await capture(page, 'light-target-apply-dialog', applyDialog);
      await page.evaluate(() => window.ProbeTheme.set('dark'));
      await capture(page, 'dark-target-apply-dialog', applyDialog);
      await page.evaluate(() => window.ProbeTheme.set('light'));

      await applyDialog.getByRole('button', {name: '添加到所选设备'}).click();
      await applyDialog.waitFor({state: 'detached'});
      await page.locator('#nt-notice:not([hidden])').waitFor();
      assert.match(await page.locator('#nt-notice').textContent(), /已添加到 2 台/);
      assert.equal(await firstRow.locator('.targets-used').textContent(), '2 台');
      const usedTitle = await firstRow.locator('.targets-used').getAttribute('title');
      assert.match(usedTitle, /Synthetic-HK-01/);
      assert.match(usedTitle, /Synthetic-JP-02/);

      // Reopen Apply dialog -> already used devices are checked, disabled, and labeled "已使用".
      await firstRow.locator('[data-nt-action="apply"]').click();
      await applyDialog.waitFor();
      for (let i = 0; i < 2; i++) {
        const box = applyDialog.locator('.nt-apply-row input[type=checkbox]').nth(i);
        assert.equal(await box.isChecked(), true);
        assert.equal(await box.isDisabled(), true);
      }
      assert.equal(await applyDialog.locator('.nt-apply-badge').count(), 2);
      assert.equal(await applyDialog.getByRole('button', {name: '添加到所选设备'}).isDisabled(), true);
      await applyDialog.getByRole('button', {name: '取消'}).click();
      await applyDialog.waitFor({state: 'detached'});

      // Capture Network Targets page with populated data (light & dark).
      await capture(page, 'light-targets-list');
      await page.evaluate(() => window.ProbeTheme.set('dark'));
      await capture(page, 'dark-targets-list');
      await page.evaluate(() => window.ProbeTheme.set('light'));

      // Edit in-use target -> verify "将同时修改 2 台设备", change host to single-family IPv4 literal -> family_adjusted notice!
      const targetId = await firstRow.getAttribute('data-target-id');
      await firstRow.locator('[data-nt-action="edit"]').click();
      await editDialog.waitFor();
      assert.equal(await editDialog.locator('.nt-edit-impact').isVisible(), true);
      assert.equal(await editDialog.locator('.nt-edit-impact').textContent(), '将同时修改 2 台设备');
      await capture(page, 'light-target-edit-dialog', editDialog);
      await page.evaluate(() => window.ProbeTheme.set('dark'));
      await capture(page, 'dark-target-edit-dialog', editDialog);
      await page.evaluate(() => window.ProbeTheme.set('light'));

      await editDialog.locator('#nt-field-host').fill('1.1.1.1');
      await editDialog.locator('#nt-field-port').fill('8443');
      await editDialog.getByRole('button', {name: '保存'}).click();
      await editDialog.waitFor({state: 'detached'});
      await page.locator('#nt-notice:not([hidden])').waitFor();
      const adjustNotice = await page.locator('#nt-notice').textContent();
      assert.match(adjustNotice, /以下设备已自动关闭另一地址族的检测：/);
      assert.match(adjustNotice, /Synthetic-HK-01/);
      assert.match(adjustNotice, /Synthetic-JP-02/);
      assert.equal(await firstRow.locator('.targets-host').textContent(), '1.1.1.1');
      assert.equal(await firstRow.locator('.targets-port').textContent(), '8443');

      // Revision conflict 409: open edit dialog, bump revision externally on real Server, submit stale edit -> conflict banner + refresh + retry succeeds!
      await firstRow.locator('[data-nt-action="edit"]').click();
      await editDialog.waitFor();
      const bumpRes = await apiCall(page, 'PUT', `/api/v1/web/network-targets/${targetId}`, {
        expected_revision: 2, region: '华东', isp: '电信', host: '1.1.1.1', port: 443,
      });
      assert.equal(bumpRes.status, 200);
      await editDialog.locator('#nt-field-port').fill('9443');
      await editDialog.getByRole('button', {name: '保存'}).click();
      await editDialog.locator('.dashboard-dialog-error:not([hidden])').waitFor();
      assert.equal(await editDialog.locator('.dashboard-dialog-error').textContent(), '目标已被别处修改，已刷新，请重试');
      // Underlying table was refreshed to port 443.
      assert.equal(await firstRow.locator('.targets-port').textContent(), '443');
      // Retrying save now uses the refreshed revision and succeeds!
      await editDialog.getByRole('button', {name: '保存'}).click();
      await editDialog.waitFor({state: 'detached'});
      assert.equal(await firstRow.locator('.targets-port').textContent(), '9443');

      // Delete in-use target -> confirm dialog shows device list on new line.
      await firstRow.locator('[data-nt-action="delete"]').click();
      const confirmDialog = page.locator('#action-confirm');
      await confirmDialog.waitFor();
      const confirmMsg = await confirmDialog.locator('.shell-content p').textContent();
      assert.match(confirmMsg, /删除目标「华东 电信」？\n将从以下 2 台设备移除：/);
      assert.match(confirmMsg, /Synthetic-HK-01/);
      assert.match(confirmMsg, /Synthetic-JP-02/);
      await capture(page, 'light-target-delete-dialog', confirmDialog);
      await page.evaluate(() => window.ProbeTheme.set('dark'));
      await capture(page, 'dark-target-delete-dialog', confirmDialog);
      await page.evaluate(() => window.ProbeTheme.set('light'));

      await confirmDialog.getByRole('button', {name: '删除'}).click();
      await confirmDialog.waitFor({state: 'detached'});
      await page.waitForFunction(() => document.querySelectorAll('#nt-tbody tr').length === 1);
      assert.equal(await page.locator('#nt-count').textContent(), '1 / 64');

      // Route-intercept checks for skipped reasons on edit & apply and 64-limit button disabled.
      const secondRow = page.locator('#nt-tbody tr').first();
      const secondId = await secondRow.getAttribute('data-target-id');
      await page.route(`**/api/v1/web/network-targets/${secondId}`, async route => {
        if (route.request().method() === 'PUT') {
          return route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({
              target: {id: secondId, region: '日本', isp: 'NTT', host: '8.8.4.4', port: 443, revision: 2, used_by: []},
              updated_agents: 0,
              family_adjusted: [],
              skipped: [{agent_id: devB.agent_id, name: 'Synthetic-JP-02', reason: 'unsupported'}],
            }),
          });
        }
        return route.continue();
      });
      await secondRow.locator('[data-nt-action="edit"]').click();
      await editDialog.waitFor();
      await editDialog.locator('#nt-field-host').fill('8.8.4.4');
      await editDialog.getByRole('button', {name: '保存'}).click();
      await editDialog.waitFor({state: 'detached'});
      await page.locator('#nt-notice:not([hidden])').waitFor();
      assert.match(await page.locator('#nt-notice').textContent(), /以下设备未修改（已撤销 \/ 正在删除 \/ 不支持）：Synthetic-JP-02/);
      await page.unroute(`**/api/v1/web/network-targets/${secondId}`);

      await page.route(`**/api/v1/web/network-targets/${secondId}/apply`, async route => {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            added: [devA.agent_id],
            skipped: [
              {agent_id: devB.agent_id, reason: 'full'},
              {agent_id: '0'.repeat(32), reason: 'not_found'},
              {agent_id: '1'.repeat(32), name: 'Revoked-Node', reason: 'revoked'},
              {agent_id: '2'.repeat(32), name: 'Removing-Node', reason: 'removing'},
              {agent_id: '3'.repeat(32), name: 'Unsupported-Node', reason: 'unsupported'},
              {agent_id: '4'.repeat(32), name: 'Already-Node', reason: 'already'},
            ],
          }),
        });
      });
      await secondRow.locator('[data-nt-action="apply"]').click();
      await applyDialog.waitFor();
      await applyDialog.locator('.nt-apply-row input[type=checkbox]').first().check();
      await applyDialog.getByRole('button', {name: '添加到所选设备'}).click();
      await applyDialog.waitFor({state: 'detached'});
      const applyResultText = await page.locator('#nt-notice').textContent();
      assert.match(applyResultText, /已添加到 1 台/);
      assert.match(applyResultText, /Synthetic-JP-02：已满 8 个/);
      assert.match(applyResultText, /设备不存在/);
      assert.match(applyResultText, /Revoked-Node：已撤销/);
      assert.match(applyResultText, /Removing-Node：正在删除/);
      assert.match(applyResultText, /Unsupported-Node：不支持网络质量/);
      assert.match(applyResultText, /Already-Node：已使用/);
      await page.unroute(`**/api/v1/web/network-targets/${secondId}/apply`);

      // 64-limit button disabled.
      const sixtyFour = Array.from({length: 64}, (_, i) => ({
        id: String(i + 1).padStart(32, '0'),
        region: '区域' + (i + 1),
        isp: '电信',
        host: `8.8.4.${(i % 250) + 1}`,
        port: 443,
        revision: 1,
        used_by: [],
      }));
      await page.route('**/api/v1/web/network-targets', route =>
        route.request().method() === 'GET'
          ? route.fulfill({status: 200, contentType: 'application/json', body: JSON.stringify({targets: sixtyFour})})
          : route.continue()
      );
      await page.locator('[data-settings="about"]').click();
      await page.locator('[data-settings="targets"]').click();
      await page.waitForFunction(() => document.querySelector('#nt-count')?.textContent === '64 / 64');
      assert.equal(await page.locator('#nt-create').isDisabled(), true);
      await page.unroute('**/api/v1/web/network-targets');

      assert.deepEqual(pageErrors, []);
    } finally {
      await context.close();
    }
  } finally {
    await srv.close();
  }
});

test('Device Network Quality editor: picker, inline create, real save, 8-limit, jump to library, and copy (light & dark)', {timeout: 180000}, async () => {
  const srv = await startRealServer();
  try {
    const {context, page, pageErrors} = await loginAndOpen(srv, {theme: 'light'});
    try {
      const dev1 = (await apiCall(page, 'POST', '/api/v1/web/agents', {name: 'Synthetic-LAX-01'})).json?.agent;
      const dev2 = (await apiCall(page, 'POST', '/api/v1/web/agents', {name: 'Synthetic-SIN-02'})).json?.agent;
      assert.ok(dev1?.agent_id && dev2?.agent_id);
      // Seed 8 targets in real Server library (7 before, 1 created inline inside picker).
      const seedSpecs = [
        {region: '上海', isp: '电信', host: '1.1.1.1', port: 443},
        {region: '东京', isp: 'NTT', host: '2606:4700:4700::1111', port: 443},
        {region: '广州', isp: '联通', host: '1.0.0.1', port: 443},
        {region: '北京', isp: '移动', host: '2606:4700:4700::1001', port: 443},
        {region: '大阪', isp: 'IIJ', host: '8.8.8.8', port: 443},
        {region: '法兰克福', isp: 'Telia', host: '8.8.4.4', port: 443},
        {region: '新加坡', isp: 'Singtel', host: '9.9.9.9', port: 443},
      ];
      const seeded = [];
      for (const spec of seedSpecs) {
        const res = await apiCall(page, 'POST', '/api/v1/web/network-targets', spec);
        assert.equal(res.status, 201, JSON.stringify({spec, res}));
        seeded.push(res.json.target);
      }

      await page.evaluate(() => dashboardData.reload());
      await page.waitForFunction(() => agents.size === 2 && !renderFrame);

      // Open Device 1 Network Quality editor.
      await page.evaluate(id => networkQuality.openSettings(id), dev1.agent_id);
      const dialog = page.locator('#network-quality-settings');
      await dialog.waitFor();
      await page.waitForFunction(() => Boolean(networkQuality.snapshot().panel?.base));

      // No empty placeholder rows.
      assert.equal(await dialog.locator('.nq-slot').count(), 0);
      assert.equal(await dialog.locator('.nq-empty-rows').isVisible(), true);

      // Open picker -> filter by search -> pick IPv4 target (上海 电信).
      await dialog.locator('[data-nq-field="add"]').click();
      const picker = page.locator('#nq-target-picker');
      await picker.waitFor();
      await picker.locator('.nq-picker-item').first().waitFor();
      assert.equal(await picker.locator('.nq-picker-item').count(), 7);
      await capture(page, 'light-device-picker', picker);
      await page.evaluate(() => window.ProbeTheme.set('dark'));
      await capture(page, 'dark-device-picker', picker);
      await page.evaluate(() => window.ProbeTheme.set('light'));

      await picker.locator('[data-nq-picker="search"]').fill('上海');
      assert.equal(await picker.locator('.nq-picker-item').count(), 1);
      assert.match(await picker.locator('.nq-picker-item').textContent(), /上海 电信.*1\.1\.1\.1:443.*使用中 0 台/);
      await picker.locator('.nq-picker-item').first().click();
      await picker.waitFor({state: 'detached'});

      // Verify IPv4 literal defaults: v4 checked, v6 unchecked & disabled, show_on_card checked.
      assert.equal(await dialog.locator('.nq-slot').count(), 1);
      const row0 = dialog.locator('.nq-slot').first();
      assert.equal(await row0.locator('.nq-target-name').textContent(), '上海 电信');
      assert.equal(await row0.locator('.nq-target-endpoint').textContent(), '1.1.1.1:443');
      assert.equal(await dialog.locator('[data-nq-field="new-0-ipv4_tcp"]').isChecked(), true);
      assert.equal(await dialog.locator('[data-nq-field="new-0-ipv4_icmp"]').isChecked(), true);
      assert.equal(await dialog.locator('[data-nq-field="new-0-ipv6_tcp"]').isChecked(), false);
      assert.equal(await dialog.locator('[data-nq-field="new-0-ipv6_tcp"]').isDisabled(), true);
      assert.equal(await dialog.locator('[data-nq-field="new-0-show-on-card"]').isChecked(), true);
      assert.equal(await dialog.locator('[data-nq-field="new-0-slot-enabled"]').isChecked(), true);

      // Open picker again -> inline "新建目标…" creates target on real Server and auto-adds to draft!
      await dialog.locator('[data-nq-field="add"]').click();
      await picker.waitFor();
      await picker.locator('.nq-picker-item').first().waitFor();
      assert.equal(await picker.locator('.nq-picker-item').count(), 6); // 上海 电信 already in draft
      await picker.locator('[data-nq-picker="new-toggle"]').click();
      await picker.locator('[data-nq-picker-field="region"]').fill('香港');
      await picker.locator('[data-nq-picker-field="isp"]').fill('PCCW');
      await picker.locator('[data-nq-picker-field="host"]').fill('149.112.112.112');
      await picker.locator('[data-nq-picker-field="port"]').fill('443');
      await picker.locator('[data-nq-picker="create-submit"]').click();
      await picker.waitFor({state: 'detached'});
      assert.equal(await dialog.locator('.nq-slot').count(), 2);
      assert.equal(await dialog.locator('.nq-slot').nth(1).locator('.nq-target-name').textContent(), '香港 PCCW');

      // Capture Device Network Quality editor screenshot (light & dark).
      await capture(page, 'light-device-editor', dialog);
      await page.evaluate(() => window.ProbeTheme.set('dark'));
      await capture(page, 'dark-device-editor', dialog);
      await page.evaluate(() => window.ProbeTheme.set('light'));

      // Save to real Server -> MUST succeed (HTTP 200, no 400 library_required!).
      const saveRespPromise = page.waitForResponse(r => r.url().endsWith('/network-quality/v4/config') && r.request().method() === 'PUT');
      await dialog.locator('[data-nq-field="save-bottom"]').click();
      const saveResp = await saveRespPromise;
      assert.equal(saveResp.status(), 200);
      await page.waitForFunction(() => networkQuality.snapshot().panel?.dirty === false);
      const savedConfig = await saveResp.json();
      assert.equal(savedConfig.slots.length, 2);
      assert.equal(savedConfig.slots[0].library_id, seeded[0].id);
      assert.ok(savedConfig.slots[1].library_id);

      // Add 6 more targets to reach 8 -> "+ 添加目标" disabled with title "每台最多 8 个".
      for (let i = 0; i < 6; i++) {
        await dialog.locator('[data-nq-field="add"]').click();
        await picker.waitFor();
        await picker.locator('.nq-picker-item').first().click();
        await picker.waitFor({state: 'detached'});
      }
      assert.equal(await dialog.locator('.nq-slot').count(), 8);
      assert.equal(await dialog.locator('[data-nq-field="add"]').isDisabled(), true);
      assert.equal(await dialog.locator('[data-nq-field="add"]').getAttribute('title'), '每台最多 8 个');

      // Save all 8 targets to real Server.
      const save8Promise = page.waitForResponse(r => r.url().endsWith('/network-quality/v4/config') && r.request().method() === 'PUT');
      await dialog.locator('[data-nq-field="save-bottom"]').click();
      assert.equal((await save8Promise).status(), 200);
      await page.waitForFunction(() => networkQuality.snapshot().panel?.dirty === false);

      // Make draft dirty and click "在目标库中编辑" -> confirm discard -> closes editor, opens Settings -> Network Targets + Edit dialog!
      await dialog.locator('[data-nq-field="threshold"]').fill('650');
      const firstEditLink = dialog.locator('.nq-edit-library').first();
      await firstEditLink.click();
      const discard = page.locator('#action-confirm');
      await discard.waitFor();
      await discard.getByRole('button', {name: '放弃修改'}).click();
      await dialog.waitFor({state: 'detached'});
      const libEditDialog = page.locator('#nt-edit-dialog');
      await libEditDialog.waitFor();
      assert.equal(new URL(page.url()).hash, '#targets');
      assert.equal(await libEditDialog.locator('#nt-field-region').inputValue(), '上海');
      assert.equal(await libEditDialog.locator('#nt-field-host').inputValue(), '1.1.1.1');
      await libEditDialog.getByRole('button', {name: '取消'}).click();
      await libEditDialog.waitFor({state: 'detached'});

      // Return to Devices, open Device 2 -> Copy from Device 1 -> Save succeeds on real Server!
      await page.locator('#dashboard-devices > summary').click();
      await page.evaluate(id => networkQuality.openSettings(id), dev2.agent_id);
      await dialog.waitFor();
      await page.waitForFunction(() => Boolean(networkQuality.snapshot().panel?.base));
      await dialog.locator('[data-nq-field="copy"]').click();
      const copyDialog = page.getByRole('dialog', {name: '从其他设备复制', exact: true});
      await copyDialog.waitFor();
      await copyDialog.locator('select').selectOption(dev1.agent_id);
      await copyDialog.getByRole('button', {name: '复制到当前草稿'}).click();
      await copyDialog.waitFor({state: 'detached'});
      assert.equal(await dialog.locator('.nq-slot').count(), 8);

      const copySavePromise = page.waitForResponse(r => r.url().includes(dev2.agent_id) && r.url().endsWith('/network-quality/v4/config') && r.request().method() === 'PUT');
      await dialog.locator('[data-nq-field="save-bottom"]').click();
      const copySaveResp = await copySavePromise;
      assert.equal(copySaveResp.status(), 200);
      const dev2Cfg = await copySaveResp.json();
      assert.equal(dev2Cfg.slots.length, 8);
      assert.equal(dev2Cfg.slots[0].library_id, seeded[0].id);

      assert.deepEqual(pageErrors, []);
    } finally {
      await context.close();
    }
  } finally {
    await srv.close();
  }
});

test('Golden medal SVG uses CSS classes (no fill/stroke attributes) and responds to light, dark, and custom skin', {timeout: 120000}, async () => {
  const srv = await startRealServer();
  try {
    const {context, page, pageErrors} = await loginAndOpen(srv, {theme: 'light'});
    try {
      const dev = (await apiCall(page, 'POST', '/api/v1/web/agents', {name: 'Medal-Device-01'})).json?.agent;
      assert.ok(dev?.agent_id);
      const now = Date.now();
      await page.route('**/api/v1/web/scores', route => route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          computed_at: now,
          server_now: now,
          items: {
            [dev.agent_id]: {
              network: {score: 92, grade: 'SS', parts: []},
              overall: {score: 92, grade: 'SS', parts: []},
              online_percent: 99.9,
              value_rank: 1,
              value_count: 5,
              high_value: true,
            },
          },
        }),
      }));
      await page.reload({waitUntil: 'domcontentloaded'});
      await page.waitForFunction(() => typeof dashboardData !== 'undefined' && dashboardData.ready());
      const medal = page.locator(`.agent-card[data-agent-id="${dev.agent_id}"] .value-medal:not([hidden])`);
      await medal.waitFor();

      const inspectMedal = () => medal.evaluate(node => {
        const svg = node.querySelector('svg');
        const parts = [...svg.querySelectorAll('path,circle')];
        const hasInlinePaintAttr = parts.some(el => el.hasAttribute('fill') || el.hasAttribute('stroke'));
        const disc = svg.querySelector('.value-medal-disc');
        const ribbon = svg.querySelector('.value-medal-ribbon');
        const ring = svg.querySelector('.value-medal-ring');
        const star = svg.querySelector('.value-medal-star');
        return {
          hasInlinePaintAttr,
          discFill: getComputedStyle(disc).fill,
          ribbonFill: getComputedStyle(ribbon).fill,
          ringStroke: getComputedStyle(ring).stroke,
          starFill: getComputedStyle(star).fill,
        };
      });

      const light = await inspectMedal();
      assert.equal(light.hasInlinePaintAttr, false, 'SVG must not use fill/stroke attributes');
      assert.equal(light.discFill, 'rgb(212, 155, 26)');
      assert.equal(light.ribbonFill, 'rgb(184, 116, 20)');
      assert.equal(light.ringStroke, 'rgb(184, 116, 20)');

      await page.evaluate(() => window.ProbeTheme.set('dark'));
      await page.waitForFunction(() => document.documentElement.dataset.theme === 'dark');
      const dark = await inspectMedal();
      assert.equal(dark.discFill, 'rgb(240, 189, 69)');
      assert.equal(dark.ribbonFill, 'rgb(212, 147, 36)');
      assert.equal(dark.ringStroke, 'rgb(212, 147, 36)');

      // Import and activate a real custom skin overriding --color-medal and --color-medal-ribbon.
      const skinRes = await apiCall(page, 'POST', '/api/v1/web/skins', {
        format: '404-probe-skin',
        version: 1,
        name: '奖章皮肤验证',
        author: 'test',
        description: 'verify medal CSS variables',
        light: {'--color-medal': '#123456', '--color-medal-ribbon': '#654321'},
        dark: {'--color-medal': '#abcdef', '--color-medal-ribbon': '#fedcba'},
      });
      assert.equal(skinRes.status, 201);
      const skinId = skinRes.json.skin.id;
      const actRes = await apiCall(page, 'PUT', '/api/v1/web/skins/active', {id: skinId});
      assert.equal(actRes.status, 204);

      // Reload /skin.css in page and verify computed fills update in both dark and light modes.
      await page.evaluate(() => new Promise(resolve => {
        const link = [...document.querySelectorAll('link[rel="stylesheet"]')].find(l => (l.getAttribute('href') || '').startsWith('/skin.css'));
        link.addEventListener('load', () => resolve(), {once: true});
        link.setAttribute('href', '/skin.css?v=' + Date.now());
      }));
      const darkSkin = await inspectMedal();
      assert.equal(darkSkin.discFill, 'rgb(171, 205, 239)');
      assert.equal(darkSkin.ribbonFill, 'rgb(254, 220, 186)');

      await page.evaluate(() => window.ProbeTheme.set('light'));
      await page.waitForFunction(() => document.documentElement.dataset.theme === 'light');
      const lightSkin = await inspectMedal();
      assert.equal(lightSkin.discFill, 'rgb(18, 52, 86)');
      assert.equal(lightSkin.ribbonFill, 'rgb(101, 67, 33)');

      assert.deepEqual(pageErrors, []);
    } finally {
      await context.close();
    }
  } finally {
    await srv.close();
  }
});
