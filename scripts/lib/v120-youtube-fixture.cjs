const path = require('node:path');
const {load, v5Card} = require('./r07-harness.cjs');
const {ID} = require('./v110-dashboard-fixture.cjs');

const card = i => '.agent-card[data-agent-id="' + ID(i) + '"]';
const row = i => '.banner-row[data-agent-id="' + ID(i) + '"]';
const block = (page, agent, lane, index = 10) => page.locator(
  card(agent) + ' .nq7-row[data-slot="' + ['telecom', 'unicom', 'mobile'][lane] + '"] .nq7-b[data-i="' + index + '"]'
);
const settled = page => page.waitForFunction(() => !renderFrame);
const reload = async page => {
  await page.evaluate(() => dashboardData.reload());
  await settled(page);
};

function fixture(source = path.join(__dirname, '../..')) {
  const {launch, open} = require(path.join(source, 'scripts/lib/v11-panel-fixture.cjs'));
  async function start(browser, {theme = 'light', n = 9, handle, activeLanes = true} = {}) {
    const config = load('v4-running');
    config.slots.forEach((slot, i) => Object.assign(slot, {
      kind: 'custom', region: ['上海', '广州', '北京'][i], isp: ['电信', '联通', '移动'][i],
      host: 'example.test', port: 443, slot_enabled: true, ipv4_tcp: true
    }));
    const mobile = config.lanes.find(lane => lane.slot === 'mobile' && lane.family === 'ipv4' && lane.protocol === 'tcp');
    Object.assign(mobile, {status: 'active', condition: '', target_id: ID(300)});
    const ui = await open(browser, {n, width: 2048, height: 1152, deviceScaleFactor: 1.25, theme,
      handle: async request => {
        if (handle && await handle(request)) return true;
        if (!activeLanes) return false;
        if (request.url.pathname.endsWith('/v4/config')) {
          request.json(config);
          return true;
        }
        if (request.url.pathname.endsWith('/v5/card')) {
          request.json(v5Card(config, request.url.searchParams.get('family') || 'ipv4', Date.now()));
          return true;
        }
        return false;
      }
    });
    const now = Date.now();
    ui.f.state.youtube_ad_free_regions = ['CN', 'MO'];
    for (const [i, a] of ui.f.state.items.entries()) {
      a.name = 'Synthetic-' + String(i + 1).padStart(3, '0');
      a.search_fields = [a.name, a.state.hostname];
      a.online = true;
      a.disabled_at = null;
      a.version = 'v1.0.10';
      a.last_seen = now - 4000;
      a.country_code = 'US';
      a.resources.stale = false;
      a.network.active = false;
      a.renewal = {type: 'upcoming', days: 40 + i, date: '2026-12-10', timezone: 'UTC'};
      a.google_status = {supported: true, pending: false, checked_at: now - 7200000, next_due_at: now + 86400000,
        result: {youtube: i === 1 ? {status: 'cn', region: 'US'} : {status: 'not_cn', region: 'US'}}};
      if (i === 2) a.google_status.manual_region = 'MO';
    }
    await reload(ui.page);
    return ui;
  }
  return {launch, start};
}

async function nearBottom(page, agent, anchor) {
  await page.locator(card(agent)).scrollIntoViewIfNeeded();
  await anchor.scrollIntoViewIfNeeded();
  await anchor.evaluate(el => scrollBy(0, el.getBoundingClientRect().bottom - innerHeight + 24));
  await page.waitForFunction(el => el.getBoundingClientRect().bottom >= innerHeight - 45, await anchor.elementHandle());
}

async function tipGeometry(tip) {
  return tip.evaluate(node => {
    const rect = node.getBoundingClientRect();
    const ancestors = [];
    for (let p = node.parentElement; p; p = p.parentElement) {
      const css = getComputedStyle(p);
      if ([css.overflowX, css.overflowY].some(x => ['clip', 'hidden', 'auto', 'scroll'].includes(x))) {
        ancestors.push({box: p.getBoundingClientRect().toJSON(), x: css.overflowX, y: css.overflowY, tag: p.tagName});
      }
    }
    return {rect: rect.toJSON(), width: document.documentElement.clientWidth,
      height: document.documentElement.clientHeight, body: node.parentElement === document.body, ancestors};
  });
}

module.exports = {fixture, card, row, block, settled, reload, nearBottom, tipGeometry, ID};
