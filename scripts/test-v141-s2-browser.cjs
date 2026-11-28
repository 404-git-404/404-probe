'use strict';
const {test, before, after} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const NQC = require('../web/static/network-quality-core.js');
const {launch, open, ID} = require('./lib/v11-panel-fixture.cjs');
const {load, v5Card} = require('./lib/r07-harness.cjs');

const screens = process.env.V141_S2_SCREENS;
let browser;
before(async () => { browser = await launch(); });
after(async () => { await browser?.close(); });

const settle = page => page.waitForFunction(() => !renderFrame);
const cardOf = (page, i) => page.locator(`.agent-card[data-agent-id="${ID(i)}"]`);
const rowOf = (page, i) => page.locator(`.banner-row[data-agent-id="${ID(i)}"]`);

async function capture(page, name, options = {}) {
  if (!screens) return;
  fs.mkdirSync(screens, {recursive: true});
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({path: path.join(screens, name + '.png'), animations: 'disabled', ...options});
}

function buildScenario() {
  const now = Date.UTC(2026, 10, 26, 14, 30, 0);
  const HOUR = 3600000, DAY = 24 * HOUR;
  const baseCfg = load('v4-running');
  const slots = [
    {...baseCfg.slots[0], slot: 'telecom', kind: 'builtin', region: '上海', isp: 'telecom', host: 'ct-sh.example.net', port: 443, slot_enabled: true, show_on_card: true},
    {...baseCfg.slots[1], slot: 'unicom', kind: 'builtin', region: '广州', isp: 'unicom', host: 'cu-gz.example.net', port: 443, slot_enabled: true, show_on_card: true},
    {...baseCfg.slots[2], slot: 'mobile', kind: 'builtin', region: '北京', isp: 'mobile', host: 'cm-bj.example.net', port: 443, slot_enabled: true, show_on_card: true},
  ];
  const cfg = {...baseCfg, slots, lanes: slots.flatMap(o => baseCfg.lanes.slice(0, 4).map(l => ({...l, slot: o.slot})))};

  const part = (key, score, weight) => ({key, score, weight, effective_weight: weight});
  const makeScore = (score, grade, {rank = 0, count = 6, high = false} = {}) => ({
    network: {score, grade, parts: [part('availability', score, 30), part('latency', score, 25), part('stability', score, 20), part('evening', score, 15), part('alerts', score, 10)]},
    overall: {score, grade, parts: [part('network', score, 50), part('uptime', score, 30), part('resources', score, 20)]},
    online_percent: 99.5,
    value_rank: rank,
    value_count: rank ? count : 0,
    high_value: high,
  });

  const scoreItems = {
    [ID(0)]: makeScore(88, 'S', {rank: 1, count: 6, high: true}),
    [ID(1)]: makeScore(79, 'A', {rank: 2, count: 6, high: true}),
    [ID(2)]: makeScore(71, 'B', {rank: 4, count: 6, high: false}),
    [ID(3)]: makeScore(92, 'SS', {rank: 3, count: 6, high: false}),
    [ID(4)]: makeScore(90, 'SS', {rank: 1, count: 6, high: true}), // Paused agent: ribbon must stay hidden.
    // ID(5): no score entry -> ribbon must stay hidden.
  };

  function makeCardData(family) {
    const c = v5Card(cfg, family, now);
    c.now = c.window_start + 19 * 180000 + 150000;
    const tcp = c.lanes[0].tcp;
    // Closed alert overlapping blocks 5 (2m), 6 (3m), 7 (1m); ongoing alert overlapping block 19 (2m).
    tcp.alert_runs = [
      {start: c.window_start + 5 * 180000 + 60000, end: c.window_start + 7 * 180000 + 30000},
      {start: c.window_start + 19 * 180000 + 60000, end: null},
    ];
    for (const [idx, b] of tcp.blocks.entries()) {
      const state = idx === 1 ? 'gray' : (idx === 5 || idx === 6 || idx === 19) ? 'red' : idx === 7 ? 'yellow' : 'green';
      const loss = state === 'red' ? 25.0 : state === 'yellow' ? 4.2 : 0.0;
      const p50 = state === 'red' ? 1024 : state === 'yellow' ? 123 : 28.4;
      b.state = state;
      b.metrics = state === 'gray'
        ? {samples: 0, p50_ms: null, max_ms: null, packet_loss_percent: null, loss_count: 0, loss_attempts: 0, error_count: 0, error_attempts: 0, reason: 'no_data'}
        : {samples: 6, p50_ms: p50, max_ms: p50 + 40, packet_loss_percent: loss, loss_count: Math.round(loss / 100 * 6), loss_attempts: 6, error_count: 0, error_attempts: 6, error_percent: 0, reason: state === 'red' ? 'loss_red' : state === 'yellow' ? 'loss_yellow' : 'normal'};
    }
    return c;
  }

  const handle = ({url, json}) => {
    if (url.pathname === '/api/v1/web/scores') {
      json({computed_at: now, server_now: now, items: scoreItems});
      return true;
    }
    if (url.pathname === '/api/v1/web/network-quality/fleet') {
      const id = url.searchParams.get('agent_id');
      const indices = id ? [parseInt(id, 16) - 1] : [0, 1, 2, 3, 4, 5];
      json({
        start: now - DAY, end: now, now,
        machines: indices.map(i => ({
          agent_id: ID(i), name: 'Synthetic-' + String(i + 1).padStart(3, '0'),
          server_local: i === 1 || i === 3, country_code: ['JP', 'HK', 'US', 'DE', 'SG', 'GB'][i],
          status: i === 4 ? 'paused' : 'online', last_seen: now - 5000, slow_threshold_ms: 500,
          alerts_30d: i === 0 ? 1 : 0, current_alerts: i === 0 ? 1 : 0,
          objects: slots,
          lines: slots.map(o => ({
            slot: o.slot, family: 'ipv4', protocol: 'tcp',
            stats: {state: 'green', samples: 6, p50_ms: 35, jitter_ms: 2, packet_loss_percent: 0},
            evening_p50_ms: 42, latest_p50_ms: 35, latest_bucket_at: now - 60000, status: 'normal',
            spark: Array.from({length: 12}, (_, k) => ({start: now - DAY + k * 2 * HOUR, p50_ms: 35 + k})),
          })),
        })),
      });
      return true;
    }
    const m = url.pathname.match(/^\/api\/v1\/web\/agents\/([a-f0-9]{32})\/network-quality\/(.+)$/);
    if (!m) return false;
    const suffix = m[2];
    if (suffix === 'v4/config') { json(cfg); return true; }
    if (suffix === 'v5/card') { json(makeCardData(url.searchParams.get('family') || 'ipv4')); return true; }
    if (suffix === 'range') {
      const start = now - DAY, points = [];
      for (let t = start; t < now; t += 300000) {
        points.push({start: t, end: t + 300000, state: 'green', samples: 6, p50_ms: 35, p95_ms: 48, jitter_ms: 2, packet_loss_percent: 0, reason: 'normal'});
      }
      json({slot: url.searchParams.get('slot') || 'telecom', start, end: now, resolution_ms: 300000, points, target_changes: [], gaps: []});
      return true;
    }
    if (suffix === 'alerts') {
      const TZ = 8 * HOUR;
      const midnight = Math.floor((now + TZ) / DAY) * DAY - TZ;
      const hourStart = midnight + 14 * HOUR;
      json([{
        slot: 'telecom', family: 'ipv4', protocol: 'tcp', target_id: 'a'.repeat(32),
        start: hourStart + 10 * 60000, end: hourStart + 17 * 60000, duration_ms: 7 * 60000,
        worst_p95_ms: 520, worst_packet_loss_percent: 12, reason: 'loss',
      }]);
      return true;
    }
    if (suffix === 'heatmap') {
      const TZ = 8 * HOUR;
      const midnight = Math.floor((now + TZ) / DAY) * DAY - TZ;
      json({
        slot: url.searchParams.get('slot') || 'telecom', timezone: 'Asia/Shanghai',
        days: [{
          date: new Date(midnight + TZ).toISOString().slice(0, 10),
          hours: Array.from({length: 24}, (_, h) => {
            const t = midnight + h * HOUR;
            if (h === 1) return {hour: h, start: t, state: 'gray', samples: 0, reason: 'no_data'};
            return {hour: h, start: t, state: h === 14 ? 'yellow' : 'green', samples: 12, p50_ms: h === 14 ? 123 : 32, packet_loss_percent: h === 14 ? 4.2 : 0, loss_count: h === 14 ? 1 : 0, loss_attempts: 24, reason: h === 14 ? 'loss_yellow' : 'normal'};
          }),
        }],
      });
      return true;
    }
    return false;
  };

  function seedItems(f) {
    f.state.tags = [
      {id: 'tag-prod', name: '生产核心'},
      {id: 'tag-jp', name: '东京直连'},
      {id: 'tag-bgp', name: 'BGP优化'},
      {id: 'tag-cdn', name: '边缘节点'},
      {id: 'tag-backup', name: '异地灾备'},
      {id: 'tag-media', name: '流媒体解锁'},
    ];
    f.state.revision++;
    const names = ['Synthetic-001', 'Synthetic-002', 'Synthetic-003', 'Synthetic-004', 'Synthetic-005', 'Synthetic-006'];
    const countries = ['JP', 'HK', 'US', 'DE', 'SG', 'GB'];
    for (const [i, a] of f.state.items.entries()) {
      a.name = names[i];
      a.country_code = countries[i];
      a.online = true;
      a.disabled_at = i === 4 ? now - DAY : null;
      a.version = 'v1.0.10';
      a.resources.cpu_percent = 25;
      a.resources.ram_percent = 40;
      a.resources.disk_percent = 35;
      a.state.cpu_percent = 25;
      a.state.ram_percent = 40;
      a.state.disk_percent = 35;
      a.state.host_health = null;
      a.startup_disk_failure = false;
      a.network = {known: true, active: false, monitored: true};
      a.system_attention = {};
      a.server_local = false;
      a.tag_ids = [];
    }
    // 0: no tags, no faults (+ high_value S)
    // 1: only tags (+ Server leftmost + high_value A)
    f.state.items[1].server_local = true;
    f.state.items[1].tag_ids = ['tag-prod', 'tag-jp'];
    // 2: only faults (service + certificate warning + network warning)
    f.state.items[2].system_attention = {certificate: 'warning', service: true};
    f.state.items[2].network = {known: true, active: true, monitored: true};
    // 3: overflowing tags + overflowing faults
    f.state.items[3].server_local = true;
    f.state.items[3].tag_ids = ['tag-prod', 'tag-jp', 'tag-bgp', 'tag-cdn', 'tag-backup', 'tag-media'];
    f.state.items[3].system_attention = {certificate: 'danger', service: true, service_restart: 'danger', time: true};
    f.state.items[3].network = {known: true, active: true, monitored: true};
    // 4: paused (high_value in scores must be ignored)
    f.state.items[4].tag_ids = ['tag-prod'];
    // 5: online, unscored (no ribbon), no tags/faults
  }

  return {handle, seedItems, now};
}

test('unit: alertOverlapMS and blockView format median latency, packet loss, and ceiling alert minutes', () => {
  const start = Date.UTC(2026, 10, 26, 6, 0, 0);
  const blockMS = 600000;
  assert.equal(NQC.alertOverlapMS(start, start + blockMS, [{start: start - 60000, end: start}], start + blockMS), 0);
  assert.equal(NQC.alertOverlapMS(start, start + blockMS, [{start: start + 60000, end: start + 60001}], start + blockMS), 1);
  assert.equal(NQC.alertOverlapMS(start, start + blockMS, [{start: start + 120000, end: start + 490000}], start + blockMS), 370000);
  assert.equal(NQC.alertOverlapMS(start, start + blockMS, [{start: start + 120000, end: null}], start + 300000), 180000);

  const metrics = {samples: 10, p50_ms: 123, packet_loss_percent: 4.2, loss_count: 1, loss_attempts: 24};
  assert.equal(NQC.blockMetricsSummary('14:00–14:10', metrics, 0), '14:00–14:10 · 延迟中位数 123 ms · 丢包 4.2%');
  assert.equal(NQC.blockMetricsSummary('14:00–14:10', metrics, 370000), '14:00–14:10 · 延迟中位数 123 ms · 丢包 4.2% · 告警 7 分钟');

  const viewNoAlert = NQC.blockView({start, state: 'yellow', metrics}, {blockMS, alertRuns: [], now: start + blockMS});
  assert.equal(viewNoAlert.alert, false);
  assert.match(viewNoAlert.text, /^\d\d:\d\d–\d\d:\d\d · 延迟中位数 123 ms · 丢包 4\.2%$/);

  const viewWithAlert = NQC.blockView({start, state: 'red', metrics}, {blockMS, alertRuns: [{start: start + 60000, end: null}], now: start + 430000});
  assert.equal(viewWithAlert.alert, true);
  assert.match(viewWithAlert.text, /延迟中位数 123 ms · 丢包 4\.2% · 告警 7 分钟$/);

  const gray = NQC.blockView({start, state: 'gray', judged: 0, slow: 0, failed: 0, unjudged: 0, metrics: {samples: 0, reason: 'no_data'}}, {blockMS});
  assert.match(gray.text, /灰：没有可用数据/);
});

test('1. mobile ☰ panel clicking Devices clears filters, closes panel, and keeps drawer open', async () => {
  const f = await open(browser, {width: 390, height: 844, n: 6, theme: 'light'});
  try {
    const {page} = f;
    await page.locator('#panel-mobile-open').click();
    await page.locator('#panel-mobile-dialog[open]').waitFor();
    await page.locator('#dashboard-groups .dashboard-option').first().click();
    await settle(page);
    assert.match(await page.locator('#dashboard-devices-count').textContent(), /^\d+ \/ 6$/);

    await page.locator('#dashboard-devices > summary').click();
    await settle(page);
    assert.equal(await page.locator('#panel-mobile-dialog').isVisible(), false);
    assert.equal(await page.locator('#dashboard-devices-count').textContent(), '6');
    assert.equal(await page.locator('#dashboard-devices').evaluate(n => n.open), true);

    await page.locator('#panel-mobile-open').click();
    await page.locator('#panel-mobile-dialog[open]').waitFor();
    assert.equal(await page.locator('#dashboard-devices').evaluate(n => n.open), true);
    assert.deepEqual(f.errors, []);
  } finally {
    await f.close();
  }
});

for (const theme of ['dark', 'light']) {
  test(`${theme}: 2–5 status row, network blocks, column resize, high-value ribbon & screenshots`, {timeout: 240000}, async () => {
    const scenario = buildScenario();
    const f = await open(browser, {width: 1520, height: 1020, n: 6, theme, handle: scenario.handle});
    try {
      const {page} = f;
      page.setDefaultTimeout(12000);
      scenario.seedItems(f.f);
      await page.evaluate(() => dashboardData.reload());
      await page.waitForFunction(() => document.querySelectorAll('.agent-card').length === 6 && !renderFrame);
      await page.locator('.agent-card .value-medal:not([hidden])').first().waitFor();
      await cardOf(page, 0).locator('.nq7-alert-run').first().waitFor();
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));

      // ---- 2. Card status row fixed height, shapes, network badge placement, +N overflow & popover ----
      const cardHeights = [];
      const rowHeights = [];
      for (const i of [0, 1, 2, 3]) {
        const cb = await cardOf(page, i).boundingBox();
        const rb = await cardOf(page, i).locator('.dashboard-card-tags').boundingBox();
        assert(rb && rb.height >= 20, `card ${i} status row must stay visible with fixed height`);
        cardHeights.push(cb.height);
        rowHeights.push(rb.height);
      }
      for (let i = 1; i < cardHeights.length; i++) {
        assert(Math.abs(cardHeights[i] - cardHeights[0]) < 1, `card heights differ: ${cardHeights.join(', ')}`);
        assert(Math.abs(rowHeights[i] - rowHeights[0]) < 1, `status row heights differ: ${rowHeights.join(', ')}`);
      }

      // Network warning moved out of network module into status row as danger pill with ⚠ prefix.
      assert.equal(await cardOf(page, 2).locator('.network-quality .dashboard-network-warning').count(), 0);
      const netPill = cardOf(page, 2).locator('.dashboard-card-tags .dashboard-network-warning');
      assert.equal(await netPill.isVisible(), true);
      assert.equal(await netPill.textContent(), '⚠ 网络异常');
      assert.equal(await netPill.getAttribute('data-tone'), 'danger');

      // Shape distinction: tag = 4px radius, fault = full capsule; Server leftmost on tag side.
      const shapeCheck = await cardOf(page, 1).evaluate(card => {
        const leftItems = [...card.querySelectorAll('.status-row-tags .status-item:not([hidden])')];
        return {
          texts: leftItems.map(n => n.textContent),
          radii: leftItems.map(n => getComputedStyle(n).borderTopLeftRadius),
        };
      });
      assert.deepEqual(shapeCheck.texts, ['Server', '生产核心', '东京直连']);
      assert.deepEqual(shapeCheck.radii, ['4px', '4px', '4px']);
      const faultRadius = await netPill.evaluate(n => parseFloat(getComputedStyle(n).borderTopLeftRadius));
      assert(faultRadius >= 999, `fault badge should be capsule, got ${faultRadius}`);

      // Card 3 overflows tags into +N and also overflows faults into +N when faults exceed width.
      const moreTags = cardOf(page, 3).locator('.status-more-tags');
      assert.equal(await moreTags.isVisible(), true);
      assert.match(await moreTags.textContent(), /^\+[1-9]\d*$/);
      const noClip = await cardOf(page, 3).evaluate(card => {
        const left = card.querySelector('.status-row-tags');
        const right = card.querySelector('.status-row-faults');
        const row = card.querySelector('.dashboard-card-tags');
        return left.scrollWidth <= left.clientWidth + 1 && right.scrollWidth <= right.clientWidth + 1 && row.scrollWidth <= row.clientWidth + 1;
      });
      assert.equal(noClip, true, 'overflowing status row must not clip visible items or +N buttons');

      // ---- 5. High-value golden medal on cards (replaces diagonal ribbon) ----
      assert.equal(await page.locator('.value-ribbon,.value-ribbon-card,.value-ribbon-list').count(), 0);
      for (const [i, wantTip] of [[0, '高性价比 · 第 1 / 6 名'], [1, '高性价比 · 第 2 / 6 名']]) {
        const medal = cardOf(page, i).locator('.status-row-tags > .value-medal');
        assert.equal(await medal.isVisible(), true);
        assert.equal(await medal.locator('svg').count(), 1);
        assert.equal(await medal.getAttribute('title'), wantTip);
        const geom = await cardOf(page, i).evaluate(card => {
          const leftSide = card.querySelector('.status-row-tags');
          const medalNode = leftSide.querySelector('.value-medal');
          const firstTag = leftSide.querySelector('.status-item');
          return {
            isFirstChild: leftSide.firstElementChild === medalNode,
            beforeTag: !firstTag || medalNode.getBoundingClientRect().right <= firstTag.getBoundingClientRect().left + 1,
          };
        });
        assert.equal(geom.isFirstChild, true, 'medal must be leftmost in status row');
        assert.equal(geom.beforeTag, true, 'medal must appear before Server/tags');
      }
      for (const i of [2, 3, 4, 5]) {
        assert.equal(await cardOf(page, i).locator('.value-medal').isVisible(), false, `card ${i} should not show medal`);
      }

      // Screenshot 1: cards (4 status row cases + high-value ribbons)
      await page.mouse.move(0, 0);
      await capture(page, `${theme}-cards`);

      // Click +N on card 3 tags -> popover lists all 7 tag-side items (Server + 6 tags).
      await moreTags.click();
      const popover = page.locator('.status-popover');
      await popover.waitFor();
      assert.deepEqual(
        await popover.locator('.status-item').allTextContents(),
        ['Server', '生产核心', '东京直连', 'BGP优化', '边缘节点', '异地灾备', '流媒体解锁']
      );
      // Screenshot 2: +N popover
      await capture(page, `${theme}-popover`);
      assert.equal(await popover.isVisible(), true, 'popover must stay visible during screenshot');
      await page.keyboard.press('Escape');
      await popover.waitFor({state: 'detached'});

      // Click outside also closes popover.
      await moreTags.click();
      await popover.waitFor();
      await page.mouse.click(10, 10);
      await popover.waitFor({state: 'detached'});

      // Test fault-side +N popover when faults alone overflow.
      const moreFaults = cardOf(page, 3).locator('.status-more-faults');
      if (await moreFaults.isVisible()) {
        await moreFaults.click();
        await popover.waitFor();
        const faultTexts = await popover.locator('.status-item').allTextContents();
        assert(faultTexts.includes('⚠ 网络异常') && faultTexts.includes('⚠ 服务异常') && faultTexts.length === 5);
        await page.keyboard.press('Escape');
        await popover.waitFor({state: 'detached'});
      }

      // ---- 3. Network blocks: per-block red line width + hover tooltip ----
      const blockReport = await cardOf(page, 0).evaluate(card => {
        const row = card.querySelector('.nq7-row[data-slot="telecom"]');
        const cells = [...row.querySelectorAll('.nq7-b[data-i]')];
        return cells.map((cell, i) => {
          const mark = cell.querySelector('.nq7-alert-run');
          const cb = cell.getBoundingClientRect();
          const mb = mark ? mark.getBoundingClientRect() : null;
          return {i, hasMark: !!mark, widthDiff: mb ? Math.abs(mb.width - cb.width) : null, leftDiff: mb ? Math.abs(mb.left - cb.left) : null};
        });
      });
      const alertIndices = blockReport.filter(b => b.hasMark).map(b => b.i);
      assert.deepEqual(alertIndices, [5, 6, 7, 19]);
      for (const b of blockReport.filter(b => b.hasMark)) {
        assert(b.widthDiff <= 0.6, `block ${b.i} red line width diff ${b.widthDiff}`);
        assert(b.leftDiff <= 0.6, `block ${b.i} red line left diff ${b.leftDiff}`);
      }

      // Hover block 5 (2 min alert), block 2 (no alert), block 1 (gray), block 19 (ongoing 2 min alert).
      await cardOf(page, 0).locator('.nq7-row[data-slot="telecom"] .nq7-b[data-i="5"]').hover();
      const tip = page.locator('.nq7-tip:visible');
      await tip.waitFor();
      assert.match(await tip.textContent(), /^\d\d:\d\d–\d\d:\d\d · 延迟中位数 1024 ms · 丢包 25\.0% · 告警 2 分钟$/);
      // Screenshot 4: network block hover tooltip with alert
      const qBox = await cardOf(page, 0).locator('.network-quality').boundingBox();
      const tBox = await tip.boundingBox();
      const clipX = Math.max(0, Math.floor(Math.min(qBox.x, tBox.x)) - 8);
      const clipY = Math.max(0, Math.floor(Math.min(qBox.y, tBox.y)) - 8);
      const clipW = Math.ceil(Math.max(qBox.x + qBox.width, tBox.x + tBox.width) - clipX) + 8;
      const clipH = Math.ceil(Math.max(qBox.y + qBox.height, tBox.y + tBox.height) - clipY) + 8;
      await capture(page, `${theme}-network-tip`, {clip: {x: clipX, y: clipY, width: clipW, height: clipH}});

      await cardOf(page, 0).locator('.nq7-row[data-slot="telecom"] .nq7-b[data-i="19"]').hover();
      assert.match(await tip.textContent(), /延迟中位数 1024 ms · 丢包 25\.0% · 告警 2 分钟$/);
      await cardOf(page, 0).locator('.nq7-row[data-slot="telecom"] .nq7-b[data-i="2"]').hover();
      assert.match(await tip.textContent(), /^\d\d:\d\d–\d\d:\d\d · 延迟中位数 28\.4 ms · 丢包 0\.0%$/);
      await cardOf(page, 0).locator('.nq7-row[data-slot="telecom"] .nq7-b[data-i="1"]').hover();
      assert.match(await tip.textContent(), /灰：没有可用数据/);
      await page.mouse.move(0, 0);

      // Network status page heatmap also aligns alert underline and uses unified tooltip.
      await page.locator('#dashboard-network').click();
      await page.locator('.net-fleet tbody tr').first().click();
      await page.locator('.net-heat-cell .net-heat-alert').first().waitFor();
      const heatAlertInfo = await page.evaluate(() => {
        const cells = [...document.querySelectorAll('.net-heat-cell')];
        const withAlert = cells.filter(c => c.querySelector('.net-heat-alert'));
        const c = withAlert[0], m = c.querySelector('.net-heat-alert');
        return {
          count: withAlert.length,
          title: c.title,
          widthDiff: Math.abs(c.getBoundingClientRect().width - m.getBoundingClientRect().width),
          grayTitle: cells[1].title,
        };
      });
      assert.equal(heatAlertInfo.count, 1);
      assert(heatAlertInfo.widthDiff <= 0.6);
      assert.match(heatAlertInfo.title, /14:00–15:00 · 延迟中位数 123 ms · 丢包 4\.2% · 告警 7 分钟$/);
      assert.match(heatAlertInfo.grayTitle, /灰：没有可用数据/);

      // Return to devices and switch to List View.
      await page.locator('#dashboard-devices > summary').click();
      await settle(page);
      await page.locator('[data-view=list]').click();
      await settle(page);
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));

      // ---- 2 & 5 in List View ----
      const listHeights = [];
      for (const i of [0, 1, 2, 3]) {
        const rb = await rowOf(page, i).boundingBox();
        listHeights.push(rb.height);
      }
      for (let i = 1; i < listHeights.length; i++) {
        assert(Math.abs(listHeights[i] - listHeights[0]) < 1, `list row heights differ: ${listHeights.join(', ')}`);
      }
      assert.equal(await rowOf(page, 2).locator('[data-column=latency] .banner-network-warning').count(), 0);
      assert.equal(await rowOf(page, 2).locator('[data-column=name] .banner-network-warning').count(), 1);
      // In the default narrow name column, trailing fault badges collapse into +N; clicking +N opens popover without opening details.
      const listMoreFaults = rowOf(page, 2).locator('.status-more-faults');
      if (await listMoreFaults.isVisible()) {
        await listMoreFaults.click();
        await popover.waitFor();
        assert.equal(await page.locator('#device-details[open]').count(), 0);
        assert((await popover.locator('.status-item').allTextContents()).includes('⚠ 网络异常'));
        await page.keyboard.press('Escape');
        await popover.waitFor({state: 'detached'});
      } else {
        assert.equal(await rowOf(page, 2).locator('[data-column=name] .banner-network-warning').isVisible(), true);
      }
      assert.equal(await rowOf(page, 0).locator('.banner-system-badges .value-medal').isVisible(), true);
      assert.equal(await rowOf(page, 0).locator('.banner-system-badges .value-medal').getAttribute('title'), '高性价比 · 第 1 / 6 名');
      assert.equal(await rowOf(page, 1).locator('.banner-system-badges .value-medal').isVisible(), true);
      assert.equal(await rowOf(page, 2).locator('.banner-system-badges .value-medal').isVisible(), false);
      assert.equal(await rowOf(page, 4).locator('.banner-system-badges .value-medal').isVisible(), false);
      assert.equal(await rowOf(page, 5).locator('.banner-system-badges .value-medal').isVisible(), false);

      // ---- 4. Banner column resize: cursor, drag, sync, min widths, reload persistence, dblclick reset, corrupt storage ----
      assert.equal(await page.locator('.banner-header [data-column=upgrade] .banner-col-resizer').count(), 0);
      const nameResizer = page.locator('.banner-header [data-column=name] .banner-col-resizer');
      const cpuResizer = page.locator('.banner-header [data-column=cpu] .banner-col-resizer');
      assert.equal(await nameResizer.evaluate(n => getComputedStyle(n).cursor), 'col-resize');

      const dragResizer = async (locator, dx) => {
        const box = await locator.boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        await page.mouse.down();
        await page.mouse.move(box.x + box.width / 2 + dx, box.y + box.height / 2, {steps: 6});
        await page.mouse.up();
      };

      const defaultNameW = (await page.locator('.banner-header [data-column=name]').boundingBox()).width;
      await dragResizer(nameResizer, 160);
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
      const wideHeaderW = (await page.locator('.banner-header [data-column=name]').boundingBox()).width;
      const wideRowW = (await rowOf(page, 0).locator('[data-column=name]').boundingBox()).width;
      assert(wideHeaderW > defaultNameW + 120, `expected wider name col, got ${wideHeaderW} vs ${defaultNameW}`);
      assert(Math.abs(wideHeaderW - wideRowW) < 1, `header (${wideHeaderW}) and row (${wideRowW}) widths must match`);
      // Widening the Device column automatically expands collapsed fault badges into view.
      assert.equal(await rowOf(page, 2).locator('[data-column=name] .banner-network-warning').isVisible(), true);

      // Screenshot 3: list view (status rows + gold ribbon icon + widened Device column)
      await page.mouse.move(0, 0);
      await capture(page, `${theme}-list`);

      // Min width enforcement: name >= 160px, cpu >= 56px.
      await dragResizer(nameResizer, -500);
      await dragResizer(cpuResizer, -300);
      const minNameW = (await page.locator('.banner-header [data-column=name]').boundingBox()).width;
      const minCpuW = (await page.locator('.banner-header [data-column=cpu]').boundingBox()).width;
      assert(Math.abs(minNameW - 160) < 2, `name min width should be 160, got ${minNameW}`);
      assert(Math.abs(minCpuW - 56) < 2, `cpu min width should be 56, got ${minCpuW}`);
      const stored = await page.evaluate(() => JSON.parse(localStorage.getItem('404-probe.banner-columns.v1')));
      assert.equal(stored.name, 160);
      assert.equal(stored.cpu, 56);

      // Reload retains column widths.
      await page.reload();
      await page.waitForFunction(() => document.querySelectorAll('.banner-row').length === 6 && !renderFrame);
      assert(Math.abs((await page.locator('.banner-header [data-column=name]').boundingBox()).width - 160) < 2);
      assert(Math.abs((await page.locator('.banner-header [data-column=cpu]').boundingBox()).width - 56) < 2);

      // Double-click restores that column only.
      await page.locator('.banner-header [data-column=cpu] .banner-col-resizer').dblclick();
      const resetCpuW = (await page.locator('.banner-header [data-column=cpu]').boundingBox()).width;
      assert(resetCpuW > 80, `cpu should reset to default (~100px), got ${resetCpuW}`);
      assert(Math.abs((await page.locator('.banner-header [data-column=name]').boundingBox()).width - 160) < 2);

      // Corrupt localStorage falls back cleanly to default widths.
      await page.evaluate(() => localStorage.setItem('404-probe.banner-columns.v1', '{bad-json'));
      await page.reload();
      await page.waitForFunction(() => document.querySelectorAll('.banner-row').length === 6 && !renderFrame);
      assert(Math.abs((await page.locator('.banner-header [data-column=cpu]').boundingBox()).width - resetCpuW) < 2);

      // Phone cards screenshot + zero horizontal overflow check.
      await page.locator('[data-view=cards]').click();
      await settle(page);
      await page.setViewportSize({width: 390, height: 844});
      await settle(page);
      await page.locator('.agent-card .value-medal:not([hidden])').first().waitFor();
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
      await capture(page, `${theme}-phone-cards`);

      assert.deepEqual(f.errors, []);
    } finally {
      await f.close();
    }
  });
}
