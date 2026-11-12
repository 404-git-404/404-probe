const assert = require('node:assert/strict');
const {test, before, after} = require('node:test');
const {fixture, card, row, block, settled, reload, nearBottom, tipGeometry, ID} = require('./lib/v120-youtube-fixture.cjs');

const {launch, start} = fixture();
let browser;
before(async () => { browser = await launch(); });
after(async () => { await browser?.close(); });
async function ui(run, options) {
  const session = await start(browser, options);
  try {
    await run(session);
    assert.deepEqual(session.errors, []);
  } finally {
    await session.close();
  }
}

test('R52 renders the same effective color in cards and list in both palettes', () => ui(async ({page, f}) => {
  const [a,b,c,d,e] = f.state.items;
  a.google_status.manual_region = 'US';
  a.google_status.result.youtube = {status: 'cn', region: 'CN'};
  a.plan.country_code_override = 'MO';
  d.google_status = {supported: true, result: {youtube: {status: 'unknown', error: {category: 'timeout'}}}};
  e.google_status.manual_region = 'US';
  e.google_status.supported = false;
  await reload(page);
  for (const theme of ['dark', 'light']) {
    await page.evaluate(value => ProbeTheme.set(value), theme);
    for (const mode of ['cards', 'list']) {
      await page.locator('[data-view=' + mode + ']').click();
      await settled(page);
      for (const [i, code, tone] of [[0, 'US', 'ok'], [1, 'CN', 'crit'], [2, 'MO', 'crit'], [3, '', 'unknown'], [4, 'US', 'ok'], [6, 'US', 'ok']]) {
        const badge = page.locator(mode === 'cards' ? card(i) + ' .check-result' : row(i) + ' .banner-youtube');
        assert.equal(await badge.textContent(), code ? (mode === 'cards' ? '地区 ' + code : code) : (mode === 'cards' ? '未知' : '—'));
        assert.equal(await badge.getAttribute(mode === 'cards' ? 'data-region-tone' : 'data-tone'), mode === 'cards' && tone === 'unknown' ? null : tone);
        assert.doesNotMatch(await badge.getAttribute('class'), /manual/);
      }
    }
  }
}));

test('R52 saves and clears on an unsupported Agent and protects an open selection from SSE', async () => {
  let response, request, success = true, agent;
  await ui(async ({page, f}) => {
    agent = f.state.items[2];
    agent.google_status.supported = false;
    await reload(page);
    await page.evaluate(id => deviceDetails.open(id, 'management'), ID(2));
    const select = page.locator('[data-focus-key=detail-youtube-region]');
    const egress = page.locator('[data-focus-key=detail-country]');
    const description = page.locator('.details-action-row').filter({hasText: 'YouTube 地区检测'}).locator('.details-note').first();
    assert.equal(await select.inputValue(), 'MO');
    assert.equal(await select.locator('option').first().textContent(), '自动（按检测结果）');
    assert.deepEqual(await select.locator('option').evaluateAll(o => o.map(n => n.value)),
      await egress.locator('option').evaluateAll(o => o.map(n => n.value)));
    assert.match(await description.textContent(), /澳门 MO（无广告地区）.*自动检测：不支持.*上次检测/);
    const feedback = page.locator('.details-youtube-feedback');
    await select.focus();
    await select.evaluate(el => { el.value = 'JP'; });
    f.publish('agent', {agent_id: ID(2), state: {cpu_percent: 41, collected_at: Date.now()}});
    await page.waitForFunction(id => agents.get(id)?.state?.cpu_percent === 41 && !renderFrame, ID(2));
    assert.equal(await select.inputValue(), 'JP');

    await select.evaluate(el => el.dispatchEvent(new Event('change', {bubbles: true})));
    await page.waitForFunction(() => document.querySelector('.details-youtube-feedback').textContent === '正在保存…');
    assert.equal(await select.isDisabled(), true);
    assert.equal(await select.inputValue(), 'JP');
    f.publish('agent', {agent_id: ID(2), state: {cpu_percent: 42, collected_at: Date.now()}});
    await page.waitForFunction(id => agents.get(id)?.state?.cpu_percent === 42 && !renderFrame, ID(2));
    assert.equal(await select.inputValue(), 'JP');
    assert.deepEqual(request, {method: 'PUT', body: {region: 'JP'}});
    response();
    await page.waitForFunction(() => !document.querySelector('[data-focus-key=detail-youtube-region]').disabled);
    assert.equal(await select.inputValue(), 'JP');
    assert.match(await description.textContent(), /日本 JP/);

    success = false;
    await select.selectOption('US');
    await page.waitForFunction(() => document.querySelector('.details-youtube-feedback').textContent === 'YouTube 地区保存失败');
    assert.equal(await select.inputValue(), 'JP');
    success = true;
    await select.selectOption('');
    await page.waitForFunction(() => !document.querySelector('[data-focus-key=detail-youtube-region]').disabled);
    assert.deepEqual(request, {method: 'DELETE', body: null});
    assert.equal(await select.inputValue(), '');
    assert.equal(await egress.inputValue(), '');
    assert.doesNotMatch(await description.textContent(), /手动设置/);
  }, {n: 4, handle: async ({req, url, body, json, fail}) => {
    if (!url.pathname.endsWith('/youtube-region')) return false;
    request = {method: req.method, body};
    if (req.headers['x-csrf-token'] !== 'synthetic-csrf') {
      fail('forbidden', 403);
    } else if (!success) {
      fail('unavailable', 503);
    } else {
      const region = body?.region || '';
      if (region === 'JP') await new Promise(resolve => { response = resolve; });
      agent.google_status.manual_region = region;
      json({region});
    }
    return true;
  }});
});

async function assertInside(tip) {
  const g = await tipGeometry(tip);
  assert.equal(g.body, true, JSON.stringify(g));
  assert(g.rect.width > 0 && g.rect.height > 0, JSON.stringify(g));
  assert(g.rect.left >= 0 && g.rect.top >= 0 && g.rect.right <= g.width && g.rect.bottom <= g.height, JSON.stringify(g));
  assert(!g.ancestors.some(p => p.tag !== 'BODY' && p.tag !== 'HTML'), JSON.stringify(g));
  return g.rect;
}

test('B1 shows all network rows, remains visible through real SSE and releases on leave and scroll', () => ui(async ({page, f}) => {
  const tip = page.locator('.nq7-tip:visible');
  for (let i = 0; i < 3; i++) {
    await block(page, 0, i).hover();
    assert.equal(await tip.count(), 1);
    await assertInside(tip);
    assert.match(await tip.textContent(), [/\u4e0a\u6d77/, /\u5e7f\u5dde/, /\u5317\u4eac/][i]);
  }
  for (const cpu_percent of [26, 27, 28]) {
    f.publish('agent', {agent_id: ID(0), state: {cpu_percent, collected_at: Date.now()}});
    await page.waitForFunction(({id, cpu}) => agents.get(id)?.state?.cpu_percent === cpu && !renderFrame, {id: ID(0), cpu: cpu_percent});
    assert.equal(await tip.count(), 1);
    await assertInside(tip);
  }
  await page.mouse.move(1, 1);
  assert.equal(await tip.count(), 0);
  const bottom = block(page, 5, 2, 19);
  await nearBottom(page, 5, bottom);
  await bottom.hover();
  const box = await assertInside(tip);
  assert(box.bottom <= (await bottom.boundingBox()).y);
  await page.evaluate(() => scrollBy(0, 24));
  await tip.waitFor({state: 'hidden'});

  const plot = page.locator(card(0) + ' .lm-plot');
  await page.locator(card(0) + ' .dv-all').click();
  await plot.hover({position: {x: 55, y: 20}});
  await assertInside(page.locator('.lm7-tip:visible'));
  await page.mouse.move(0, 0);
  assert.equal(await page.locator('.lm7-tip:visible').count(), 0);
}));

test('B2 doubles repeated detail failures to 30 s and recovers to 2 s', async () => {
  let failReads = false;
  await ui(async ({page, f}) => {
    f.state.items[0].google_status.pending = true;
    await reload(page);
    await page.evaluate(id => deviceDetails.open(id, 'management'), ID(0));
    await page.waitForFunction(id => agents.get(id)?.detail_loaded && !detailJobs.has(id), ID(0));
    failReads = true;
    const poll = () => page.evaluate(async () => {
      clearTimeout(detailCheckTimer);
      await pollActiveDetail();
      const delay = detailPollDelay;
      clearTimeout(detailCheckTimer);
      return delay;
    });
    for (const delay of [4000, 8000, 16000, 30000, 30000]) assert.equal(await poll(), delay);
    failReads = false;
    assert.equal(await poll(), 2000);
    assert.equal(await poll(), 2000);
  }, {n: 1, handle: async ({url, fail}) => {
    if (failReads && url.pathname.endsWith('/agents/' + ID(0))) {
      fail('unavailable', 503);
      return true;
    }
    return false;
  }});
});

test('B3 explains the wait after two minutes using the downloading transition timestamp', async () => {
  let upgrade;
  await ui(async ({page}) => {
    await page.evaluate(id => deviceDetails.open(id, 'management'), ID(0));
    await page.waitForFunction(id => !detailJobs.has(id), ID(0));
    const message = page.locator('[data-upgrade-region] .details-note').first();
    for (const [status, elapsed, expected] of [
      ['downloading', 0, /^正在下载新版本$/],
      ['downloading', 125000, /^正在下载新版本。网络较慢，最长等待 30 分钟$/],
      ['verifying', 125000, /v1\.0\.10/],
      ['downloading', null, /^正在下载新版本$/]
    ]) {
      upgrade = {operation_id: ID(301), status, started_at: Date.now() - 500000,
        updated_at: elapsed === null ? undefined : Date.now() - elapsed};
      await page.evaluate(id => hydrateActiveDetail(id), ID(0));
      await settled(page);
      assert.match(await message.textContent(), expected);
    }
  }, {n: 1, handle: async ({url, json}) => {
    if (upgrade && url.pathname.endsWith('/upgrade')) {
      json(upgrade);
      return true;
    }
    return false;
  }});
});
