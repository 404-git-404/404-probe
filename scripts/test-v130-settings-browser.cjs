const assert = require('node:assert/strict');
const {test, before, after} = require('node:test');
const {launch, open, ID} = require('./lib/v11-panel-fixture.cjs');

let browser;
before(async () => { browser = await launch(); });
after(async () => { await browser?.close(); });

const regionsPath = '/api/v1/web/settings/youtube-ad-free-regions';
const defaults = ['AD','AL','CI','CN','ET','KH','LA','MD','MG','MM','MN','MO','MV','RU','SM'];

test('settings saves region edits explicitly and refreshes an already open dashboard', async () => {
  let fixture;
  const session = await open(browser, {n:6, handle: async ({req,url,body,json}) => {
    if (url.pathname !== regionsPath) return false;
    if (req.method === 'PUT') {
      assert.equal(req.headers['x-csrf-token'], 'synthetic-csrf');
      fixture.state.youtube_ad_free_regions = [...body.regions].sort();
      fixture.state.revision++;
      fixture.publish('dashboard', {event:'dashboard', kind:'youtube-ad-free-regions', agent_id:''});
    }
    json({regions:fixture.state.youtube_ad_free_regions, defaults});
    return true;
  }});
  try {
    const {page: panel, f, context} = session;
    fixture = f;
    f.state.youtube_ad_free_regions = [...defaults];
    f.state.revision++;
    const auto = code => ({supported:true, result:{youtube:{status:'not_cn', region:code}}});
    const [active, offline, paused, stale, other, unknown] = f.state.items;
    Object.assign(active, {online:true, disabled_at:null, google_status:auto('MO')});
    Object.assign(offline, {online:false, disabled_at:null, google_status:{...auto('US'), manual_region:'MO'}});
    Object.assign(paused, {online:false, disabled_at:123, google_status:{...auto('US'), manual_region:'CN'}});
    Object.assign(stale, {online:true, disabled_at:null, google_status:{...auto('CN'), stale:true}});
    Object.assign(other, {online:true, disabled_at:null, google_status:auto('US')});
    Object.assign(unknown, {online:false, disabled_at:null, google_status:auto('MO')});
    await panel.evaluate(() => dashboardData.reload());
    await panel.waitForFunction(() => !renderFrame);
    await panel.locator('#dashboard-countries > summary').click();
    const youtube = panel.locator('#dashboard-youtube-free');
    assert.equal(await panel.locator('#dashboard-youtube-count').textContent(),'3');
    await youtube.check();
    await panel.waitForFunction(() => document.querySelector('#dashboard-count').textContent === '3 / 6 台');
    await panel.reload();
    await panel.waitForFunction(() => document.querySelector('#dashboard-count').textContent === '3 / 6 台');
    assert(await youtube.isChecked());
    assert.equal(await panel.locator('.agent-card[data-agent-id="'+ID(1)+'"] .check-result').getAttribute('data-region-tone'), 'crit');

    const settings = await context.newPage();
    await settings.goto(f.base + '/settings.html#regions');
    await settings.locator('#rg-search').fill('澳');
    assert.equal(new URL(settings.url()).hash,'#regions');
    const ordered = await settings.locator('#rg-chips button').allTextContents();
    assert.deepEqual(ordered.map(label=>label.split(' ').at(-2)),defaults);
    const check = settings.locator('#rg-list input[value=MO]');
    await check.uncheck();
    assert.equal(await settings.locator('#rg-count').textContent(), '14');
    assert(await settings.locator('#rg-dirty').isVisible());
    assert.equal(f.state.youtube_ad_free_regions.length, 15);
    await settings.locator('#rg-undo').click();
    assert(await check.isChecked());
    await check.uncheck();
    await settings.locator('#rg-save').click();
    await settings.waitForFunction(() => document.querySelector('#rg-status').textContent.includes('已保存'));
    await panel.waitForFunction(() => !dashboardData.catalog().youtube_ad_free_regions.includes('MO') && !renderFrame);
    assert(await youtube.isChecked());
    await panel.waitForFunction(() => document.querySelector('#dashboard-count').textContent === '1 / 6 台');
    await youtube.uncheck();
    assert.equal(await panel.locator(`.agent-card[data-agent-id="${ID(1)}"] .check-result`).getAttribute('data-region-tone'), 'ok');
    await settings.locator('#rg-reset').click();
    assert(await check.isChecked());
    assert(!f.state.youtube_ad_free_regions.includes('MO'));
    assert.deepEqual(session.errors, []);
  } finally { await session.close(); }
});

test('settings password errors, all-session logout, lockout delay and narrow layout', async () => {
  let code = 'wrong_current';
  let token = 'synthetic-csrf';
  const posts = [];
  const session = await open(browser, {handle: async ({req,url,json}) => {
    if (url.pathname === '/api/v1/web/session') { json({csrf_token:token}); return true; }
    if (url.pathname === regionsPath) { json({regions:defaults,defaults}); return true; }
    if (url.pathname !== '/api/v1/web/settings/password') return false;
    posts.push(req.headers['x-csrf-token']);
    if (code === 'ok') { token = 'rotated-csrf'; json({}); }
    else json({error:{code,remaining:2,characters:9}}, code === 'locked_out' ? 401 : 400);
    return true;
  }});
  try {
    const {page} = session;
    await page.locator('#dashboard-settings > summary').click();
    await page.locator('[data-settings=about]').click();
    await page.waitForFunction(() => document.querySelector('#ab-schema').textContent === '38');
    await page.locator('[data-settings=password]').click();
    assert.equal(await page.locator('.settings-content > section:visible').count(),1);
    await page.locator('#pw-show').check();
    assert.equal(await page.locator('#pw-current').getAttribute('type'), 'text');
    const submit = async () => {
      for (const id of ['current','new','confirm']) await page.locator('#pw-'+id).fill('synthetic secret');
      await page.locator('#pw-save').click();
    };
    await submit();
    await page.waitForFunction(() => document.querySelector('#pw-current-msg').textContent.includes('再错 2 次'));
    for (const [error,field,pattern] of [['mismatch','confirm',/不一样/],['too_short','new',/9 个/],['same_as_current','new',/不能和当前密码一样/],['invalid_chars','new',/控制字符/]]) {
      code = error;
      await submit();
      await page.waitForFunction(({field,pattern}) => new RegExp(pattern).test(document.querySelector('#pw-'+field+'-msg').textContent), {field,pattern:pattern.source});
    }
    await page.clock.install();
    code = 'ok';
    await submit();
    await page.waitForFunction(() => document.querySelector('#pw-banner').textContent.includes('所有浏览器都已退出') && document.querySelector('#pw-save').disabled);
    assert.equal(await page.locator('#pw-new').inputValue(), '');
    assert.equal(await page.locator('#ab-schema').textContent(), '38');
    assert.equal(await page.getByText('构建时间').count(), 0);
    await page.setViewportSize({width:430,height:900});
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    assert.equal(posts.at(-1), 'synthetic-csrf');
    await page.reload();
    await page.clock.install();
    await page.route('**/login', route => route.fulfill({contentType:'text/html',body:'<title>登录</title>'}));
    code = 'locked_out';
    await submit();
    await page.waitForFunction(() => document.querySelector('#pw-banner').textContent.includes('连续输错 5 次'));
    assert.equal(posts.at(-1), 'rotated-csrf');
    assert.equal(new URL(page.url()).hash, '#password');
    await page.clock.fastForward(3000);
    await page.waitForURL('**/login');
    assert.deepEqual(session.errors, []);
  } finally { await session.close(); }
});
