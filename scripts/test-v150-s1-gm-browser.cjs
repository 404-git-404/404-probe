'use strict';
const {test, before, after} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {launch, open, ID} = require('./lib/v11-panel-fixture.cjs');

const screens = process.env.V150_S1_GM_SCREENS;
let browser;
before(async () => { browser = await launch(); });
after(async () => { await browser?.close(); });

const settle = page => page.waitForFunction(() => !renderFrame);
const cardOf = (page, i) => page.locator(`.agent-card[data-agent-id="${ID(i)}"]`);
const rowOf = (page, i) => page.locator(`.banner-row[data-agent-id="${ID(i)}"]`);

async function capture(page, name, options = {}) {
  if (!screens) return;
  fs.mkdirSync(screens, {recursive: true});
  await page.mouse.move(0, 0);
  await page.evaluate(() => document.fonts.ready);
  await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
  await page.screenshot({path: path.join(screens, name + '.png'), animations: 'disabled', ...options});
}

function makeScore(score, grade, {rank = 0, count = 6, high = false} = {}) {
  const part = (key, s, w) => ({key, score: s, weight: w, effective_weight: w});
  return {
    network: {score, grade, parts: [part('availability', score, 30), part('latency', score, 25), part('stability', score, 20), part('evening', score, 15), part('alerts', score, 10)]},
    overall: {score, grade, parts: [part('network', score, 50), part('uptime', score, 30), part('resources', score, 20)]},
    online_percent: 99.6,
    value_rank: rank,
    value_count: rank ? count : 0,
    high_value: high,
  };
}

for (const theme of ['light', 'dark']) {
  test(`${theme}: golden medal placement, hover tooltip, no +N collapse, no legacy ribbon`, {timeout: 120000}, async () => {
    const now = Date.UTC(2026, 10, 28, 12, 0, 0);
    const scoreItems = {
      [ID(0)]: makeScore(91, 'SS', {rank: 1, count: 6, high: true}),
      [ID(1)]: makeScore(84, 'S', {rank: 2, count: 6, high: true}),
      [ID(2)]: makeScore(76, 'A', {rank: 3, count: 6, high: false}),
      [ID(3)]: makeScore(90, 'SS', {rank: 1, count: 6, high: true}), // paused -> hidden
    };
    const panel = await open(browser, {
      width: 1520, height: 1000, n: 4, theme,
      handle: ({url, json}) => {
        if (url.pathname === '/api/v1/web/scores') {
          json({computed_at: now, server_now: now, items: scoreItems});
          return true;
        }
        return false;
      },
    });
    try {
      const {page, f} = panel;
      f.state.tags = [
        {id: 'tag-1', name: '生产核心', color: 'blue'},
        {id: 'tag-2', name: '东京直连', color: 'sky'},
        {id: 'tag-3', name: 'BGP优化', color: 'cyan'},
        {id: 'tag-4', name: '边缘节点', color: 'teal'},
        {id: 'tag-5', name: '流媒体解锁', color: 'purple'},
        {id: 'tag-6', name: '异地灾备', color: 'pink'},
      ];
      f.state.revision++;
      const [a0, a1, a2, a3] = f.state.items;
      Object.assign(a0, {name: 'Tokyo-Value-01', country_code: 'JP', server_local: true, tag_ids: ['tag-1', 'tag-2', 'tag-3', 'tag-4', 'tag-5', 'tag-6'], system_attention: {service: true, items: [{kind: 'service', item: 'exim4.service', level: 'danger', detail: 'exim4.service failed'}]}});
      Object.assign(a1, {name: 'Osaka-Value-02', country_code: 'JP', server_local: false, tag_ids: ['tag-1', 'tag-2'], system_attention: {}});
      Object.assign(a2, {name: 'Frankfurt-Std-03', country_code: 'DE', server_local: true, tag_ids: ['tag-3'], system_attention: {}});
      Object.assign(a3, {name: 'Singapore-Paused-04', country_code: 'SG', disabled_at: now - 3600000, tag_ids: ['tag-4'], system_attention: {}});
      await page.evaluate(() => dashboardData.reload());
      await page.waitForFunction(() => document.querySelectorAll('.agent-card').length === 4 && !renderFrame);
      await cardOf(page, 0).locator('.value-medal:not([hidden])').waitFor();
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));

      // Legacy ribbons must not exist anywhere.
      assert.equal(await page.locator('.value-ribbon,.value-ribbon-card,.value-ribbon-list').count(), 0);

      // Card 0 has overflowing tags + fault, yet golden medal stays leftmost before Server and never collapses into +N.
      const card0Geom = await cardOf(page, 0).evaluate(card => {
        const row = card.querySelector('.dashboard-card-tags');
        const side = card.querySelector('.status-row-tags');
        const medal = side.querySelector('.value-medal');
        const svg = medal?.querySelector('svg');
        const server = side.querySelector('.status-tag[data-tone=server]');
        const more = side.querySelector('.status-more-tags');
        const mr = medal.getBoundingClientRect();
        const sr = server.getBoundingClientRect();
        return {
          rowHeight: row.getBoundingClientRect().height,
          isFirst: side.firstElementChild === medal,
          medalWidth: mr.width,
          medalHeight: mr.height,
          beforeServer: mr.right <= sr.left + 1,
          hasSvg: !!svg && svg.querySelectorAll('circle,path').length >= 3,
          moreVisible: !more.hidden,
          title: medal.title,
          ariaLabel: medal.getAttribute('aria-label'),
        };
      });
      assert.equal(card0Geom.isFirst, true);
      assert.equal(card0Geom.beforeServer, true);
      assert.equal(card0Geom.hasSvg, true);
      assert.equal(card0Geom.moreVisible, true);
      assert(card0Geom.medalWidth >= 13 && card0Geom.medalWidth <= 15);
      assert(card0Geom.medalHeight >= 13 && card0Geom.medalHeight <= 15);
      assert.equal(card0Geom.title, '高性价比 · 第 1 / 6 名');
      assert.equal(card0Geom.ariaLabel, '高性价比 · 第 1 / 6 名');

      // Card 2 (no medal) has identical status-row height.
      const card2RowH = (await cardOf(page, 2).locator('.dashboard-card-tags').boundingBox()).height;
      assert(Math.abs(card0Geom.rowHeight - card2RowH) < 1);

      // Clicking +N on card 0 does not include medal in overflow popover.
      await cardOf(page, 0).locator('.status-more-tags').click();
      const pop = page.locator('.status-popover');
      await pop.waitFor();
      assert.equal(await pop.locator('.value-medal').count(), 0);
      assert.equal((await pop.locator('.status-item').first().textContent()), 'Server');
      await page.keyboard.press('Escape');
      await pop.waitFor({state: 'detached'});

      // Card 1 shows medal with rank 2/6; Card 2 & paused Card 3 hide medal.
      assert.equal(await cardOf(page, 1).locator('.value-medal').getAttribute('title'), '高性价比 · 第 2 / 6 名');
      assert.equal(await cardOf(page, 2).locator('.value-medal').isVisible(), false);
      assert.equal(await cardOf(page, 3).locator('.value-medal').isVisible(), false);

      await capture(page, `${theme}-medal-cards`);

      // List view: medal sits in the same status-row leftmost position, never collapses, height unchanged.
      await page.locator('[data-view=list]').click();
      await settle(page);
      await rowOf(page, 0).locator('.banner-system-badges .value-medal').waitFor();
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
      assert.equal(await page.locator('.value-ribbon,.value-ribbon-card,.value-ribbon-list').count(), 0);
      const listGeom = await page.evaluate(ids => {
        const r0 = document.querySelector(`.banner-row[data-agent-id="${ids[0]}"]`);
        const r2 = document.querySelector(`.banner-row[data-agent-id="${ids[2]}"]`);
        const side0 = r0.querySelector('.banner-system-badges .status-row-tags');
        const medal0 = side0.querySelector('.value-medal');
        return {
          h0: r0.getBoundingClientRect().height,
          h2: r2.getBoundingClientRect().height,
          isFirst: side0.firstElementChild === medal0,
          visible0: !medal0.hidden && medal0.getBoundingClientRect().width >= 13,
          title0: medal0.title,
        };
      }, [ID(0), ID(1), ID(2)]);
      assert.equal(listGeom.isFirst, true);
      assert.equal(listGeom.visible0, true);
      assert.equal(listGeom.title0, '高性价比 · 第 1 / 6 名');
      assert(Math.abs(listGeom.h0 - listGeom.h2) < 1);
      assert.equal(await rowOf(page, 1).locator('.banner-system-badges .value-medal').getAttribute('title'), '高性价比 · 第 2 / 6 名');
      assert.equal(await rowOf(page, 2).locator('.banner-system-badges .value-medal').isVisible(), false);
      assert.equal(await rowOf(page, 3).locator('.banner-system-badges .value-medal').isVisible(), false);

      // Widen Device column slightly for clean screenshot showing medal + tags + faults.
      const resizer = page.locator('.banner-header [data-column=name] .banner-col-resizer');
      const box = await resizer.boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      await page.mouse.down();
      await page.mouse.move(box.x + box.width / 2 + 160, box.y + box.height / 2, {steps: 5});
      await page.mouse.up();
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
      await capture(page, `${theme}-medal-list`);

      assert.deepEqual(panel.errors, []);
    } finally {
      await panel.close();
    }
  });
}

test('Devices summary click follows all 5 desktop rules including search and overview quick filters', {timeout: 120000}, async () => {
  const panel = await open(browser, {width: 1440, height: 960, n: 10, theme: 'light'});
  try {
    const {page} = panel;
    const devices = page.locator('#dashboard-devices');
    const summary = page.locator('#dashboard-devices > summary');
    const isOpen = () => devices.evaluate(n => n.open);

    // Preserve non-default sort & view across filter clears.
    await page.locator('#dashboard-sort').selectOption('cpu');
    await page.locator('[data-view=list]').click();
    await settle(page);

    // Rule 4: on devices page, no filters, drawer open -> collapses drawer.
    assert.equal(await isOpen(), true);
    await summary.click();
    assert.equal(await isOpen(), false);

    // Rule 5: on devices page, no filters, drawer closed -> expands drawer.
    await summary.click();
    assert.equal(await isOpen(), true);

    // Rule 3a: with search query (even if drawer closed first) -> clears search, opens/keeps drawer open.
    const firstAgentName = await page.evaluate(() => [...agents.values()][0].name);
    await summary.click(); // close drawer
    assert.equal(await isOpen(), false);
    await page.locator('#dashboard-search').fill(firstAgentName);
    await settle(page);
    assert.equal(await page.locator('#dashboard-devices-count').textContent(), '1 / 10');
    await summary.click();
    await settle(page);
    assert.equal(await page.locator('#dashboard-search').inputValue(), '');
    assert.equal(await page.locator('#dashboard-devices-count').textContent(), '10');
    assert.equal(await isOpen(), true);

    // Rule 3b: with overview quick filter (#overview-health) while drawer open -> clears quick filter, keeps drawer open.
    await page.locator('#overview-health').click();
    await settle(page);
    assert.match(await page.locator('#dashboard-devices-count').textContent(), /^\d+ \/ 10$/);
    await summary.click();
    await settle(page);
    assert.deepEqual(await page.evaluate(() => dashboardUI.preferences.value.quick), []);
    assert.equal(await page.locator('#dashboard-devices-count').textContent(), '10');
    assert.equal(await isOpen(), true);
    // Sort and view remain unchanged.
    assert.equal(await page.evaluate(() => dashboardUI.preferences.value.sort), 'cpu');
    assert.equal(await page.evaluate(() => dashboardUI.preferences.value.viewMode), 'list');

    // Rule 2: not on devices page (on #appearance with drawer closed & filter active) -> goes to devices, opens drawer, clears filter.
    await page.locator('#dashboard-search').fill('Synthetic');
    await settle(page);
    await summary.click(); // clears search, drawer open
    await summary.click(); // collapses drawer
    assert.equal(await isOpen(), false);
    await page.locator('#dashboard-settings > summary').click();
    await page.locator('[data-settings=appearance]').click();
    await settle(page);
    assert.equal(await page.locator('#appearance').isVisible(), true);
    await summary.click();
    await settle(page);
    assert.equal(await page.locator('#appearance').isVisible(), false);
    assert.equal(await page.locator('#dashboard-toolbar').isVisible(), true);
    assert.equal(await isOpen(), true);

    // Rule 1: panel collapsed (from #network with drawer closed) -> expands panel, opens drawer, clears filter, goes to devices.
    await summary.click(); // collapse drawer
    assert.equal(await isOpen(), false);
    await page.locator('#dashboard-network').click();
    await settle(page);
    await page.locator('#panel-collapse').click();
    assert.equal(await page.locator('.panel-collapsed').count(), 1);
    await summary.click();
    await settle(page);
    assert.equal(await page.locator('.panel-collapsed').count(), 0);
    assert.equal(await page.locator('#network-placeholder').isVisible(), false);
    assert.equal(await page.locator('#dashboard-toolbar').isVisible(), true);
    assert.equal(await isOpen(), true);

    assert.deepEqual(panel.errors, []);
  } finally {
    await panel.close();
  }
});

test('three-state theme switching and system mode following prefers-color-scheme live', {timeout: 120000}, async () => {
  const panel = await open(browser, {width: 1440, height: 960, n: 2, theme: 'system'});
  try {
    const {page} = panel;
    await page.emulateMedia({colorScheme: 'light'});
    await page.locator('#dashboard-settings > summary').click();
    await page.locator('[data-settings=appearance]').click();
    await page.locator('#appearance').waitFor();

    // Default / system follows OS light -> dark -> light live.
    await page.locator('[data-appearance-mode=system]').click();
    assert.equal(await page.evaluate(() => ProbeTheme.preference()), 'system');
    assert.equal(await page.evaluate(() => ProbeTheme.get()), 'light');
    assert.equal(await page.locator('html').getAttribute('data-theme'), 'light');
    assert.equal(await page.locator('[data-appearance-mode=system]').getAttribute('aria-pressed'), 'true');

    await page.evaluate(() => {
      window.__themeEvents = [];
      window.addEventListener('404-probe:themechange', e => window.__themeEvents.push(e.detail));
    });
    await page.emulateMedia({colorScheme: 'dark'});
    await page.waitForFunction(() => document.documentElement.dataset.theme === 'dark');
    assert.equal(await page.evaluate(() => ProbeTheme.preference()), 'system');
    assert.equal(await page.evaluate(() => ProbeTheme.get()), 'dark');
    assert.deepEqual(await page.evaluate(() => window.__themeEvents.at(-1)), {theme: 'dark', preference: 'system'});

    await page.emulateMedia({colorScheme: 'light'});
    await page.waitForFunction(() => document.documentElement.dataset.theme === 'light');

    // Explicit dark ignores OS light.
    await page.locator('[data-appearance-mode=dark]').click();
    assert.equal(await page.evaluate(() => ProbeTheme.preference()), 'dark');
    assert.equal(await page.locator('html').getAttribute('data-theme'), 'dark');
    await page.emulateMedia({colorScheme: 'light'});
    await page.waitForTimeout(30);
    assert.equal(await page.locator('html').getAttribute('data-theme'), 'dark');

    // Explicit light ignores OS dark.
    await page.locator('[data-appearance-mode=light]').click();
    await page.emulateMedia({colorScheme: 'dark'});
    await page.waitForTimeout(30);
    assert.equal(await page.locator('html').getAttribute('data-theme'), 'light');

    // Secondary page theme-toggle.js cycles ☀ -> ☾ -> ◐ -> ☀.
    await page.goto(new URL('/jobs.html', page.url()).href);
    await page.evaluate(() => ProbeTheme.set('light'));
    const toggle = page.locator('[data-theme-toggle]');
    for (const [icon, title, pref] of [
      ['☀', '当前：浅色', 'light'],
      ['☾', '当前：深色', 'dark'],
      ['◐', '当前：跟随系统', 'system'],
      ['☀', '当前：浅色', 'light'],
    ]) {
      assert.equal(await toggle.textContent(), icon);
      assert.equal(await toggle.getAttribute('title'), title);
      assert.equal(await page.evaluate(() => ProbeTheme.preference()), pref);
      await toggle.click();
    }
    assert.deepEqual(panel.errors, []);
  } finally {
    await panel.close();
  }
});

for (const theme of ['light', 'dark']) {
  test(`${theme}: appearance skins list, import (ok/ignored/errors/limit), activate with skin.css reload + event, delete confirm`, {timeout: 120000}, async () => {
    const skins = [
      {
        id: 'default', name: '默认', author: '', description: '内置暖纸浅色与炭灰深色主题。',
        created_at: 0, builtin: true,
        swatches: {
          light: ['#F7F4EF', '#FFFDFA', '#26221E', '#3C8A6E', '#B33A2A'],
          dark: ['#0E1012', '#16191C', '#E7E4DF', '#4CB38A', '#EA6B57'],
        },
      },
      {
        id: 'cool-slate', name: '示例 · 冷灰蓝', author: '404-probe', description: '低饱和度冷灰蓝配色示例。',
        created_at: 1700000000000, builtin: false,
        swatches: {
          light: ['#F1F5F9', '#FFFFFF', '#0F172A', '#0F766E', '#DC2626'],
          dark: ['#0B1120', '#131C31', '#E2E8F0', '#2DD4BF', '#F87171'],
        },
      },
    ];
    let activeId = 'default';
    let skinCssText = '';
    const requests = [];
    let importBehavior = 'ok';

    const panel = await open(browser, {
      width: 1440, height: 1000, n: 2, theme,
      handle: ({req, url, body, json, fail}) => {
        if (url.pathname === '/api/v1/web/skins' && req.method === 'GET') {
          requests.push({method: 'GET', path: url.pathname});
          json({active_id: activeId, skins});
          return true;
        }
        if (url.pathname === '/api/v1/web/skins' && req.method === 'POST') {
          requests.push({method: 'POST', path: url.pathname, csrf: req.headers['x-csrf-token'], body});
          if (importBehavior === 'invalid') {
            json({
              error: {
                code: 'invalid_skin',
                details: Array.from({length: 12}, (_, i) => ({
                  mode: i % 2 === 0 ? 'light' : 'dark',
                  variable: `--color-bad-${i + 1}`,
                  reason: 'invalid_value',
                })),
              },
            }, 400);
            return true;
          }
          if (importBehavior === 'limit') {
            json({error: {code: 'skin_limit'}}, 409);
            return true;
          }
          const created = {
            id: 'skin-' + (skins.length + 1),
            name: body?.name || '导入测试皮肤',
            author: body?.author || 'Tester',
            description: body?.description || '',
            created_at: Date.now(),
            builtin: false,
            swatches: {
              light: ['#F5F7F2', '#FFFFFF', '#1F2933', '#2E7D32', '#C62828'],
              dark: ['#101411', '#182019', '#E6EFE7', '#66BB6A', '#EF5350'],
            },
          };
          skins.push(created);
          json({skin: created, ignored: importBehavior === 'ignored' ? ['--unknown-a', '--unknown-b'] : []}, 201);
          return true;
        }
        if (url.pathname === '/api/v1/web/skins/active' && req.method === 'PUT') {
          requests.push({method: 'PUT', path: url.pathname, csrf: req.headers['x-csrf-token'], body});
          activeId = body.id;
          skinCssText = activeId === 'default' ? '' : ':root:not([data-theme=dark]){--color-page:#F1F5F9}:root[data-theme=dark]{--color-page:#0B1120}';
          json(null, 204);
          return true;
        }
        const delMatch = /^\/api\/v1\/web\/skins\/([^/]+)$/.exec(url.pathname);
        if (delMatch && req.method === 'DELETE') {
          const id = decodeURIComponent(delMatch[1]);
          requests.push({method: 'DELETE', path: url.pathname, csrf: req.headers['x-csrf-token']});
          const idx = skins.findIndex(s => s.id === id);
          if (idx === -1) { fail('not_found', 404); return true; }
          skins.splice(idx, 1);
          if (activeId === id) { activeId = 'default'; skinCssText = ''; }
          json(null, 204);
          return true;
        }
        return false;
      },
    });
    try {
      const {page} = panel;
      // Simulate post-merge <link rel="stylesheet" href="/skin.css"> and serve dynamic /skin.css.
      await page.route('**/skin.css*', route => route.fulfill({contentType: 'text/css', body: skinCssText}));
      await page.evaluate(() => {
        const style = document.querySelector('link[href="/style.css"]');
        const link = document.createElement('link');
        link.rel = 'stylesheet';
        link.href = '/skin.css';
        style.after(link);
        window.__skinEvents = [];
        window.addEventListener('404-probe:skinchange', e => window.__skinEvents.push(e.detail));
      });

      await page.locator('#dashboard-settings > summary').click();
      await page.locator('[data-settings=appearance]').click();
      await page.locator('.skin-card[data-skin-id="default"]').waitFor();
      await page.locator('.skin-card[data-skin-id="cool-slate"]').waitFor();

      // Verify builtin default card only has "使用" (disabled while active), custom card has 使用/下载/删除.
      const defaultCard = page.locator('.skin-card[data-skin-id="default"]');
      assert.equal(await defaultCard.locator('.skin-active-badge').textContent(), '当前');
      assert.equal(await defaultCard.locator('[data-skin-action="activate"]').isDisabled(), true);
      assert.equal(await defaultCard.locator('[data-skin-action="download"]').count(), 0);
      assert.equal(await defaultCard.locator('[data-skin-action="delete"]').count(), 0);
      assert.equal(await defaultCard.locator('.skin-swatch-row[data-mode="light"] .skin-swatch').count(), 5);
      assert.equal(await defaultCard.locator('.skin-swatch-row[data-mode="dark"] .skin-swatch').count(), 5);

      const slateCard = page.locator('.skin-card[data-skin-id="cool-slate"]');
      assert.equal(await slateCard.locator('[data-skin-action="activate"]').isEnabled(), true);
      assert.equal(await slateCard.locator('[data-skin-action="download"]').getAttribute('href'), '/api/v1/web/skins/cool-slate/download');
      assert.equal(await slateCard.locator('[data-skin-action="delete"]').isEnabled(), true);
      assert.equal(await page.locator('#skin-example').getAttribute('href'), '/api/v1/web/skins/example');

      // Import success (clean)
      importBehavior = 'ok';
      await page.locator('#skin-file-input').setInputFiles({
        name: 'forest.json',
        mimeType: 'application/json',
        buffer: Buffer.from(JSON.stringify({format: '404-probe-skin', version: 1, name: '森林浅绿', author: 'Alice', description: '护眼绿', light: {'--color-page': '#F5F7F2'}})),
      });
      await page.locator('#skin-status').filter({hasText: '已导入「森林浅绿」'}).waitFor();
      assert.equal(await page.locator('#skin-status').textContent(), '已导入「森林浅绿」');

      // Import success with ignored variables
      importBehavior = 'ignored';
      await page.locator('#skin-file-input').setInputFiles({
        name: 'ocean.json',
        mimeType: 'application/json',
        buffer: Buffer.from(JSON.stringify({format: '404-probe-skin', version: 1, name: '深海蓝', light: {'--color-page': '#EEF4FA', '--unknown-a': '#111', '--unknown-b': '#222'}})),
      });
      await page.locator('#skin-status').filter({hasText: '已导入「深海蓝」，忽略了 2 个不认识的变量'}).waitFor();

      // Activate cool-slate -> /skin.css href updates with ?v= timestamp & 404-probe:skinchange fires.
      await slateCard.locator('[data-skin-action="activate"]').click();
      await page.waitForFunction(() => document.querySelector('.skin-card[data-skin-id="cool-slate"]')?.dataset.active === 'true');
      const linkHref = await page.evaluate(() => [...document.querySelectorAll('link[rel="stylesheet"]')].find(n => (n.getAttribute('href') || '').startsWith('/skin.css'))?.getAttribute('href'));
      assert.match(linkHref, /^\/skin\.css\?v=\d+$/);
      const ev = await page.evaluate(() => window.__skinEvents.at(-1));
      assert.equal(ev.id, 'cool-slate');
      assert.equal(ev.href, linkHref);

      // Import failure (12 details -> lists first 10 + extra note) & capture appearance screenshot.
      importBehavior = 'invalid';
      await page.locator('#skin-file-input').setInputFiles({
        name: 'broken.json',
        mimeType: 'application/json',
        buffer: Buffer.from(JSON.stringify({format: '404-probe-skin', version: 1, name: '坏皮肤', light: {'--color-bad-1': 'javascript:alert(1)'}})),
      });
      await page.locator('#skin-errors').waitFor();
      assert.equal(await page.locator('#skin-errors li').count(), 10);
      assert.match(await page.locator('#skin-errors').textContent(), /另有 2 条错误未显示/);

      await capture(page, `${theme}-appearance`);

      // Import 409 limit error
      importBehavior = 'limit';
      await page.locator('#skin-file-input').setInputFiles({
        name: 'limit.json',
        mimeType: 'application/json',
        buffer: Buffer.from(JSON.stringify({format: '404-probe-skin', version: 1, name: '超出上限', light: {'--color-page': '#FFFFFF'}})),
      });
      await page.locator('#skin-errors').filter({hasText: '最多只能保存 20 个皮肤'}).waitFor();

      // Delete confirmation: cancel sends no DELETE; confirm deletes active skin, reloads /skin.css, reverts to default.
      const beforeDeletes = requests.filter(r => r.method === 'DELETE').length;
      await slateCard.locator('[data-skin-action="delete"]').click();
      const confirmDlg = page.locator('#action-confirm[open]');
      await confirmDlg.waitFor();
      assert.equal(await confirmDlg.locator('.shell-content p').textContent(), '删除皮肤「示例 · 冷灰蓝」？正在使用时会回到默认。');
      await confirmDlg.getByRole('button', {name: '取消'}).click();
      await confirmDlg.waitFor({state: 'detached'});
      assert.equal(requests.filter(r => r.method === 'DELETE').length, beforeDeletes);

      const beforeSkinEvents = await page.evaluate(() => window.__skinEvents.length);
      await slateCard.locator('[data-skin-action="delete"]').click();
      await confirmDlg.waitFor();
      await confirmDlg.getByRole('button', {name: '删除', exact: true}).click();
      await confirmDlg.waitFor({state: 'detached'});
      await page.waitForFunction(() => document.querySelector('.skin-card[data-skin-id="default"]')?.dataset.active === 'true');
      assert.equal(await page.locator('.skin-card[data-skin-id="cool-slate"]').count(), 0);
      const delReq = requests.find(r => r.method === 'DELETE');
      assert.equal(delReq.csrf, 'synthetic-csrf');
      assert.equal(await page.evaluate(() => window.__skinEvents.length), beforeSkinEvents + 1);
      assert.equal(await page.evaluate(() => window.__skinEvents.at(-1).id), 'default');

      assert.deepEqual(panel.errors, []);
    } finally {
      await panel.close();
    }
  });
}

test('server_local Agent upgrade button is always disabled with "随 Server 一起升级"', {timeout: 120000}, async () => {
  const panel = await open(browser, {
    width: 1440, height: 1000, n: 2, theme: 'light',
    handle: ({url, json}) => {
      if (url.pathname.endsWith('/upgrade-targets')) {
        json({targets: [{channel: 'stable', version: 'v1.5.0', supported: true}]});
        return true;
      }
      return false;
    },
  });
  try {
    const {page, f} = panel;
    Object.assign(f.state.items[0], {
      name: 'Server-Local-01',
      server_local: true,
      online: true,
      disabled_at: null,
      version: 'v1.0.9',
      upgrade_capable: true,
      upgrade_v2: true,
    });
    Object.assign(f.state.items[1], {
      name: 'Remote-Node-02',
      server_local: false,
      online: true,
      disabled_at: null,
      version: 'v1.0.9',
      upgrade_capable: true,
      upgrade_v2: true,
    });
    await page.evaluate(() => dashboardData.reload());
    await settle(page);

    // Server-local agent: upgrade button disabled even though version < server and upgrade-targets returned supported:true.
    await page.evaluate(id => deviceDetails.open(id, 'management'), ID(0));
    await page.locator('#details-pane-management [data-upgrade-region]').waitFor();
    await page.waitForTimeout(100);
    const localRow = page.locator('#details-pane-management [data-upgrade-region]');
    const localBtn = localRow.locator('.upgrade-agent');
    assert.equal(await localBtn.isDisabled(), true);
    assert.equal(await localBtn.getAttribute('title'), '随 Server 一起升级');
    assert.match(await localRow.textContent(), /随 Server 一起升级/);

    // Non-local agent: upgrade button remains enabled.
    await page.evaluate(id => deviceDetails.open(id, 'management'), ID(1));
    await page.waitForFunction(() => !document.querySelector('#details-pane-management .upgrade-agent')?.disabled);
    const remoteBtn = page.locator('#details-pane-management .upgrade-agent');
    assert.equal(await remoteBtn.isDisabled(), false);

    assert.deepEqual(panel.errors, []);
  } finally {
    await panel.close();
  }
});

test('StatusRow.fit() caches width + signature and performs zero DOM writes when unchanged', {timeout: 120000}, async () => {
  const panel = await open(browser, {width: 1440, height: 960, n: 1, theme: 'light'});
  try {
    const {page} = panel;
    const result = await page.evaluate(() => {
      const host = document.createElement('div');
      host.style.width = '220px';
      host.style.position = 'fixed';
      host.style.top = '0';
      host.style.left = '0';
      document.body.append(host);

      const row = StatusRow.create({document, variant: 'card'});
      host.append(row.node);
      const payload = {
        medal: {title: '高性价比 · 第 1 / 4 名'},
        tags: [
          {kind: 'server', label: 'Server', tone: 'server'},
          {kind: 'tag', id: 't1', label: '生产核心', color: 'blue'},
          {kind: 'tag', id: 't2', label: '东京直连', color: 'sky'},
          {kind: 'tag', id: 't3', label: '边缘节点', color: 'teal'},
        ],
        faults: [],
      };
      row.update(payload);
      row.fit();

      let mutations = 0;
      const obs = new MutationObserver(list => { mutations += list.length; });
      obs.observe(row.node, {attributes: true, childList: true, subtree: true, characterData: true});

      // Same width + same signature: calling fit() and update(payload) repeatedly must not touch DOM.
      row.fit();
      row.fit();
      row.update(payload);
      row.fit();
      const unchangedMutations = obs.takeRecords().length + mutations;

      // Changing width must trigger refit.
      mutations = 0;
      host.style.width = '110px';
      row.fit();
      const resizedMutations = obs.takeRecords().length + mutations;

      // Calling fit() again at new width must again produce zero DOM writes.
      mutations = 0;
      row.fit();
      const afterResizeStableMutations = obs.takeRecords().length + mutations;

      // Changing content signature must trigger refit.
      mutations = 0;
      row.update({...payload, tags: payload.tags.slice(0, 1)});
      row.fit();
      const contentChangedMutations = obs.takeRecords().length + mutations;

      obs.disconnect();
      row.destroy();
      host.remove();
      return {unchangedMutations, resizedMutations, afterResizeStableMutations, contentChangedMutations};
    });

    assert.equal(result.unchangedMutations, 0);
    assert(result.resizedMutations > 0, 'resizing container should refit DOM');
    assert.equal(result.afterResizeStableMutations, 0);
    assert(result.contentChangedMutations > 0, 'changing content signature should refit DOM');
    assert.deepEqual(panel.errors, []);
  } finally {
    await panel.close();
  }
});

test('JS-drawn charts re-read CSS variables and redraw on 404-probe:skinchange', {timeout: 120000}, async () => {
  const {open: themeOpen, scene, NOW} = require('./lib/v11-theme-fixture.cjs');
  const f = await themeOpen(browser, {width: 1440, height: 1000, theme: 'light', n: 2});
  try {
    const {page} = f;
    // 1. Latency-mini SVG & legend redraw on skinchange
    await scene(page, 'Home-B');
    await page.waitForSelector('.lm-plot svg [data-grid]', {state: 'attached'});
    const gridBefore = await page.locator('.lm-plot svg [data-grid]').first().getAttribute('stroke');
    const dotBefore = await page.locator('.lm-dot').first().evaluate(n => getComputedStyle(n).backgroundColor);
    await page.evaluate(() => {
      const style = document.createElement('style');
      style.id = 'test-skin-override';
      style.textContent = ':root{--color-chart-grid:#112233;--color-chart-memory:#ff5500;--color-net-series-1:#123456;}';
      document.head.append(style);
      window.dispatchEvent(new CustomEvent('404-probe:skinchange', {detail: {id: 'test-skin'}}));
    });
    assert.equal(await page.locator('.lm-plot svg [data-grid]').first().getAttribute('stroke'), '#112233');
    assert.notEqual(await page.locator('.lm-plot svg [data-grid]').first().getAttribute('stroke'), gridBefore);
    assert.notEqual(await page.locator('.lm-dot').first().evaluate(n => getComputedStyle(n).backgroundColor), dotBefore);

    // 2. Device-details uPlot canvas redraws on skinchange without refetching history
    await scene(page, 'Details-resources');
    const canvas = page.locator('.resource-canvas canvas').first();
    const beforeCanvas = await canvas.evaluate(n => n.toDataURL());
    let reads = 0;
    page.on('request', r => { if (r.url().includes('/resources/history')) reads++; });
    await page.evaluate(() => {
      document.getElementById('test-skin-override').textContent = ':root{--color-chart-grid:#aa1122;--color-chart-down:#00aa44;--color-chart-up:#9900cc;--color-chart-memory:#ff5500;--color-net-series-1:#cc3366;}';
      window.dispatchEvent(new CustomEvent('404-probe:skinchange', {detail: {id: 'test-skin-2'}}));
    });
    assert.notEqual(await page.locator('.resource-canvas canvas').first().evaluate(n => n.toDataURL()), beforeCanvas);
    assert.equal(reads, 0);
    await page.keyboard.press('Escape');

    // 3. Network status SVG series redraws on skinchange using --color-net-series-1
    const HOUR = 3600000, DAY = 24 * HOUR;
    const slots = [{slot: 'telecom', kind: 'builtin', region: '上海', isp: 'telecom', host: 'ct-sh.example.net', port: 443, slot_enabled: true, show_on_card: true, ipv4_tcp: true}];
    await page.route('**/api/v1/web/network-quality/fleet*', r => r.fulfill({json: {
      start: NOW - DAY, end: NOW, now: NOW,
      machines: [{agent_id: ID(0), name: 'Synthetic-001', server_local: false, country_code: 'JP', status: 'online', last_seen: NOW - 1000, slow_threshold_ms: 500, alerts_30d: 0, current_alerts: 0, objects: slots, lines: [{slot: 'telecom', family: 'ipv4', protocol: 'tcp', stats: {state: 'green', samples: 6, p50_ms: 30, jitter_ms: 2, packet_loss_percent: 0}, evening_p50_ms: 35, latest_p50_ms: 30, latest_bucket_at: NOW - 60000, status: 'normal', spark: []}]}],
    }}));
    await page.route('**/api/v1/web/agents/*/network-quality/range*', r => r.fulfill({json: {
      slot: 'telecom', start: NOW - DAY, end: NOW, resolution_ms: 300000,
      points: [
        {start: NOW - 900000, end: NOW - 600000, state: 'green', samples: 6, p50_ms: 30, p95_ms: 40, jitter_ms: 2, packet_loss_percent: 0},
        {start: NOW - 600000, end: NOW - 300000, state: 'green', samples: 6, p50_ms: 55, p95_ms: 68, jitter_ms: 3, packet_loss_percent: 0},
      ],
      target_changes: [], gaps: [],
    }}));
    await page.route('**/api/v1/web/agents/*/network-quality/alerts*', r => r.fulfill({json: []}));
    await page.route('**/api/v1/web/agents/*/network-quality/heatmap*', r => r.fulfill({json: {slot: 'telecom', timezone: 'Asia/Shanghai', days: []}}));

    await page.locator('#dashboard-network').click();
    await page.locator('.net-fleet tbody tr').first().click();
    await page.locator('.net-chart .net-series').first().waitFor({state: 'attached'});
    assert.equal(await page.locator('.net-chart .net-series').first().getAttribute('style'), 'stroke:#cc3366');

    await page.evaluate(() => {
      document.getElementById('test-skin-override').textContent = ':root{--color-net-series-1:#1188ff;}';
      window.dispatchEvent(new CustomEvent('404-probe:skinchange', {detail: {id: 'test-skin-3'}}));
    });
    assert.equal(await page.locator('.net-chart .net-series').first().getAttribute('style'), 'stroke:#1188ff');

    assert.deepEqual(f.errors, []);
  } finally {
    await f.close();
  }
});
