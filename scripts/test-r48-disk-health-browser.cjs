const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {test} = require('node:test');
const {launch, open, ID} = require('./lib/v11-panel-fixture.cjs');
const {diskReport, diskTone} = require('../web/static/dashboard-model.js');

const card = i => `.agent-card[data-agent-id="${ID(i)}"]`;
const row = i => `.banner-row[data-agent-id="${ID(i)}"]`;
const output = process.env.R48_SCREENS;

test('disk history and check freshness have inclusive cutoffs', () => {
  const now = 1800000000000;
  const health = {
    last_fault_at: now - 7 * 24 * 60 * 60 * 1000,
    disk: {status: 'ok', checked_at: now - 150 * 1000}
  };
  const agent = {online: true, state: {host_health: health}};
  assert.equal(diskReport(agent, now), health);
  assert.equal(diskTone(agent, now), 'warn');
  health.last_fault_at--;
  assert.equal(diskTone(agent, now), '');
  health.disk.checked_at--;
  assert.equal(diskReport(agent, now), null);
});

for (const theme of ['dark', 'light']) {
  test(`${theme} disk health, filtering, and incremental rendering`, {timeout: 60000}, async () => {
    const browser = await launch();
    const x = await open(browser, {width: 2048, height: 1152, deviceScaleFactor: 1.25, n: 6, theme});
    try {
      const {page, f} = x;
      const now = Date.now();
      const healthy = {status: 'ok', checked_at: now - 61000, probe_ms: 15, read_back_verified: true, read_only_super: false};
      for (const [i, a] of f.state.items.entries()) {
        a.name = 'Synthetic-' + String(i + 1).padStart(3, '0');
        a.state.hostname = 'synthetic-' + i;
        a.search_fields = [a.name];
        a.online = true;
        a.disabled_at = null;
        a.last_seen = now - 1000;
        a.resources.stale = false;
        a.resources.collected_at = now;
        a.state.collected_at = now;
        a.version = 'v1.0.10';
        a.network.active = false;
        a.renewal = {type: 'upcoming', days: 40 + i, date: '2026-12-10', timezone: 'UTC'};
        a.state.host_health = {disk: {...healthy}};
        a.network_slots.forEach(s => {s.state = 'ok'; s.latest_ms = 20 + i; s.sampled_at = now - 1000;});
      }
      const [red, orange, normal, old, container, gray] = f.state.items;
      red.state.host_health = {os_read_failed: true, first_fault_at: now - 720000, last_fault_at: now - 1000,
        disk: {...healthy, status: 'error', reasons: ['write_eio', 'unwritable'], started_at: now - 720000, filesystems: [{scope: 'root', ext4_errors: 3}]}};
      orange.state.host_health.last_fault_at = now - 2 * 86400000;
      delete old.state.host_health;
      container.state.os = 'Alpine 3.24';
      container.state.host_health.disk = {status: 'ok', checked_at: now - 1000, container: true, read_back_verified: false};
      gray.state.host_health.disk = {status: 'unknown', checked_at: now - 1000, reasons: ['full', 'scsi_errors', 'slow'],
        filesystems: [{scope: 'root', scsi_errors: 7, scsi_state: 'running'}]};

      const redraw = () => page.evaluate(async () => {await dashboardData.reload(); await new Promise(requestAnimationFrame);});
      const select = (i, child) => page.locator(`${card(i)} ${child}`);
      const disk = '.disk-health-badge';
      const capture = async (name, figure) => {
        if (!output) return;
        fs.mkdirSync(output, {recursive: true});
        const file = path.join(output, `${theme}-${name}.png`);
        await page.screenshot({path: file, animations: 'disabled'});
        if (figure === 4) {
          const box = await page.locator('#device-details').boundingBox();
          fs.writeFileSync(path.join(output, `${theme}-detail-box.json`), JSON.stringify(box));
        }
      };
      await redraw();
      await select(0, disk).waitFor();
      assert.match(await select(0, disk).textContent(), /磁盘异常 · 写入失败/);
      assert.equal(await select(0, '.dashboard-card-tags').evaluate(n => n.firstElementChild.className), 'disk-health-badge');
      assert.equal(await page.locator(card(0)).evaluate(n => n.classList.contains('disk-health-critical')), true);
      assert.equal(await select(0, '.c7-ring').nth(2).getAttribute('data-tone'), 'crit');
      assert.equal(await select(0, '.disk-os-failed').isVisible(), true);
      assert.match(await select(1, disk).textContent(), /磁盘曾出错 · \d\d-\d\d \d\d:\d\d/);
      assert.equal(await page.locator(card(1)).evaluate(n => n.classList.contains('disk-health-critical')), false);
      assert.notEqual(await select(1, '.c7-ring').nth(2).getAttribute('data-tone'), 'crit');
      for (const i of [2, 3, 4, 5]) assert.equal(await select(i, disk).isVisible(), false);
      assert.match(await page.locator('#overview-health').textContent(), /1 台需要关注/);
      assert.match(await page.locator('#overview-health-note').textContent(), /磁盘异常 1/);
      assert.equal(await page.locator('#dashboard-quick [data-quick]').nth(1).getAttribute('data-quick'), 'disk');
      await capture('01-cards');

      await page.locator('[data-quick=disk]').click();
      await page.waitForFunction(() => document.querySelectorAll('.agent-card').length === 1);
      assert.equal(await page.locator(card(0)).isVisible(), true);
      await page.locator('[data-quick=disk]').click();
      await select(1, disk).waitFor();

      const details = async (i, expected, name, figure) => {
        await select(i, '.c7-detail').click();
        const dialog = page.locator('#device-details');
        await dialog.locator('.details-disk-status').waitFor();
        assert.match(await dialog.locator('.details-disk-health').textContent(), expected);
        if (i === 0) {
          assert.match(await dialog.locator('.disk-fault-text').textContent(), /写入失败（I\/O 错误）/);
          assert.match(await dialog.locator('.details-box .disk-os-failed').textContent(), /读取失败，显示上次的值/);
          assert.doesNotMatch(await dialog.locator('.details-disk-health').textContent(), /未变只读|配置问题/);
        }
        if (name) await capture(name, figure);
        await dialog.locator('.details-close').click();
      };
      await details(0, /异常 · 写入失败（I\/O 错误）.*ext4 错误记录 3 次/, '03-detail', 4);
      await details(2, /正常 · 1 分钟前检查/);
      await details(3, /未检测（需升级 Agent）/, '05-legacy');
      await details(4, /部分支持（容器）.*读回未验证/, '06-container');
      await details(5, /磁盘空间或 inode 已满.*SCSI 错误计数增加.*探测偏慢/, '07-gray');

      await page.locator('#dashboard-search').fill(orange.name);
      await page.waitForFunction(() => document.querySelectorAll('.agent-card').length === 1);
      await select(1, disk).waitFor();
      await capture('04-orange');
      await page.locator('#dashboard-search').fill('');
      await select(0, disk).waitFor();

      await page.evaluate(s => {window.r48Card = document.querySelector(s); window.r48Button = window.r48Card.querySelector('.c7-detail');}, card(0));
      red.state.host_health.disk.reasons = ['ext4_errors', 'stalled', 'read_eio', 'write_eio'];
      red.state.host_health.disk.stalled_since = now - 34000;
      red.tag_ids = [];
      f.publish('agent', {...red, sequence: 20});
      await page.waitForFunction(s => {
        const n = document.querySelector(s);
        return n?.querySelector('.dashboard-card-tags').children.length === 1;
      }, card(0));
      assert.equal(await page.evaluate(() => r48Card.isConnected && r48Button === r48Card.querySelector('.c7-detail')), true);
      assert.match(await select(0, disk).textContent(), /磁盘异常 · 写入失败/);
      await capture('08-tagless');
      await details(0, /写入失败（I\/O 错误） · 读回失败（I\/O 错误） · I\/O 卡住（已超过 \d+ 秒无响应） · ext4 记录到新错误/);

      await page.locator('[data-view=list]').click();
      await page.locator(`${row(0)} .banner-disk-status`).waitFor();
      assert.match(await page.locator(`${row(0)} .banner-disk-status`).textContent(), /异常/);
      assert.equal(await page.locator(`${row(0)} [data-column=disk] .banner-metric`).getAttribute('data-tone'), 'crit');
      assert.match(await page.locator(`${row(1)} .banner-disk-status`).textContent(), /曾出错/);
      assert.notEqual(await page.locator(`${row(1)} [data-column=disk] .banner-metric`).getAttribute('data-tone'), 'crit');
      await capture('02-list');

      red.online = false;
      normal.state.host_health.disk.checked_at = now - 180000;
      await redraw();
      assert.equal(await page.locator(`${row(0)} .banner-disk-status`).textContent(), '');
      assert.match(await page.locator('#overview-health-note').textContent(), /磁盘异常 0/);
      assert.deepEqual(x.errors, []);
    } finally {
      await x.close();
      await browser.close();
    }
  });
}
