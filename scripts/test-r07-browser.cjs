// R07 stage D: the dialog in a real Chromium with the production style sheets and scripts. The fake DOM of
// test-r07-network-quality.cjs knows neither CSS nor browser focus rules; these checks are what it cannot see.
// Needs Playwright and a Chromium build (M02_PLAYWRIGHT / M02_CHROMIUM, as test-m02-real-chain.cjs); skipped without them.
'use strict';
const {test, before, after} = require('node:test'), assert = require('node:assert/strict');
const fs = require('node:fs'), path = require('node:path');

const playwrightPath = process.env.M02_PLAYWRIGHT || 'C:/Users/ufoli/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright';
const chromiumPath = process.env.M02_CHROMIUM || 'C:/Users/ufoli/AppData/Local/ms-playwright/chromium-1228/chrome-win64/chrome.exe';
let chromium = null;
try { ({chromium} = require(playwrightPath)); } catch (_) { /* skipped below */ }
const available = !!chromium && fs.existsSync(chromiumPath);

const root = path.join(__dirname, '..', 'web', 'static'), fixtures = path.join(__dirname, 'fixtures', 'r07');
const load = name => JSON.parse(fs.readFileSync(path.join(fixtures, name + '.json'), 'utf8'));
const AGENT = 'f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1';
const read = file => fs.readFileSync(path.join(root, file), 'utf8');

let browser;
before(async () => { if (available) browser = await chromium.launch({headless: true, executablePath: chromiumPath}); });
after(async () => { await browser?.close(); });

// A page with the production scripts and CSS; the Server answers from memory with the given v4 response.
async function open(raw) {
  const page = await browser.newPage({viewport: {width: 1000, height: 800}}), errors = [];
  page.on('pageerror', e => errors.push(e.message));
  await page.setContent('<!doctype html><html><head><meta charset="utf-8"></head><body><div id="host"></div><dialog id="network-quality-history"></dialog></body></html>');
  for (const css of ['style.css', 'network-quality.css']) await page.addStyleTag({content: read(css)});
  for (const js of ['agent-state.js', 'management.js', 'network-quality-core.js', 'network-quality.js']) await page.addScriptTag({content: read(js)});
  await page.evaluate(({raw, agent}) => {
    window.__raw = raw; window.__writes = []; window.__gets = 0; window.__clock = 0;
    const now = () => 1800000000000 + window.__clock;
    window.__q = window.NetworkQualityCore.coordinator({now});
    window.__ui = window.NetworkQuality.create({coordinator: window.__q, now, fetcher: async (url, init = {}) => { if (init.method === 'PUT') window.__writes.push(JSON.parse(init.body)); else if (url.endsWith('/v4/config')) window.__gets++; return {ok: true, status: 200, statusText: 'OK', json: async () => window.__raw}; },
      csrf: () => 'synthetic', interval: () => 0, clear() {}, Observer: null, Resize: null});
    const node = window.__ui.mount({agent_id: agent, name: '合成设备'});
    document.getElementById('host').append(node);
    window.__ui.setVisible(agent, true);
  }, {raw, agent: AGENT});
  await page.waitForFunction(() => document.querySelector('.nq-link'));
  await page.waitForTimeout(150);
  await page.click('.nq-link');
  await page.waitForSelector('.nq-overlay');
  await page.waitForTimeout(150);
  return {page, errors};
}
const visibleGuards = page => page.evaluate(() => [...document.querySelectorAll('.nq-guard')].filter(n => n.getBoundingClientRect().height > 0 && getComputedStyle(n).display !== 'none').map(n => n.querySelector('p').textContent));
const inDialog = page => page.evaluate(() => !!document.activeElement?.closest('.nq-overlay'));

test('M1 (Chromium): a clean dialog shows no question; each question appears alone and goes away again', {skip: !available && 'Playwright/Chromium not available'}, async () => {
  const {page, errors} = await open(load('v4-saved-off'));
  try {
    assert.deepEqual(await visibleGuards(page), []);
    await page.fill('[data-nq-field="telecom-port"]', '444');
    await page.click('[data-nq-field="enabled"]'); // the switch stays off and asks
    assert.deepEqual(await visibleGuards(page), ['有未保存的修改，先保存吗？']);
    await page.click('[data-nq-field="guard-cancel"]');
    assert.deepEqual(await visibleGuards(page), []);
    await page.click('[data-nq-field="close"]');
    assert.deepEqual(await visibleGuards(page), ['放弃修改？']);
    await page.click('[data-nq-field="guard-keep"]');
    assert.deepEqual(await visibleGuards(page), []);
    assert.deepEqual(errors, []);
  } finally { await page.close(); }
});

test('M2 (Chromium): with the master switch disabled the focus still starts inside, and Tab never leaves', {skip: !available && 'Playwright/Chromium not available'}, async () => {
  const {page, errors} = await open(load('v4-empty-unreported'));
  try {
    assert.equal(await page.evaluate(() => document.querySelector('[data-nq-field="enabled"]').disabled), true);
    assert.equal(await inDialog(page), true, 'initial focus');
    for (let i = 0; i < 60; i++) { await page.keyboard.press('Tab'); assert.equal(await inDialog(page), true, `Tab ${i + 1}`); }
    for (let i = 0; i < 60; i++) { await page.keyboard.press('Shift+Tab'); assert.equal(await inDialog(page), true, `Shift+Tab ${i + 1}`); }
    // Focus put outside by script comes back in on the next Tab.
    await page.evaluate(() => document.querySelector('.nq-link').focus());
    await page.keyboard.press('Tab');
    assert.equal(await inDialog(page), true);
    await page.click('[data-nq-field="close"]');
    assert.equal(await page.evaluate(() => document.activeElement.classList.contains('nq-link')), true, 'back on the button that opened it');
    assert.deepEqual(errors, []);
  } finally { await page.close(); }
});

test('M3 (Chromium): a ticked box of the other family can be unticked after typing a literal address, then saving is possible', {skip: !available && 'Playwright/Chromium not available'}, async () => {
  const {page, errors} = await open(load('v4-running'));
  try {
    await page.fill('[data-nq-field="telecom-host"]', '93.184.216.34');
    const state = () => page.evaluate(() => ['ipv6_tcp', 'ipv6_icmp'].map(k => { const b = document.querySelector(`[data-nq-field="telecom-${k}"]`); return {checked: b.checked, disabled: b.disabled}; }));
    assert.deepEqual(await state(), [{checked: true, disabled: false}, {checked: true, disabled: false}]);
    await page.uncheck('[data-nq-field="telecom-ipv6_tcp"]');
    await page.uncheck('[data-nq-field="telecom-ipv6_icmp"]');
    assert.deepEqual(await state(), [{checked: false, disabled: true}, {checked: false, disabled: true}]);
    await page.fill('[data-nq-field="telecom-host"]', '::ffff:93.184.216.34');
    assert.equal(await page.evaluate(() => document.querySelector('[data-nq-field="telecom-ipv4_tcp"]').disabled), false, 'mapped IPv4 is IPv4');
    assert.deepEqual(errors, []);
  } finally { await page.close(); }
});

test('M5 (Chromium): the three cases of the review - switch off, nothing ticked, IPv6 hidden by "no IPv6" - all close the unticked ICMP boxes the Server says are unsupported (ticked ones stay clearable, see R1)', {skip: !available && 'Playwright/Chromium not available'}, async () => {
  const clearTicks = raw => { for (const s of raw.slots) Object.assign(s, {ipv4_icmp: false, ipv6_icmp: false}); return raw; };
  const off = clearTicks(load('v4-saved-off')); off.icmp_support = {ipv4: 'unsupported', ipv6: 'unsupported'};
  const unticked = load('v4-running'); unticked.icmp_support = {ipv4: 'unsupported', ipv6: 'unsupported'}; clearTicks(unticked);
  const noV6 = clearTicks(load('v4-no-ipv6-no-icmp'));
  for (const [what, raw] of [['switch off', off], ['nothing ticked', unticked], ['no IPv6', noV6]]) {
    const {page, errors} = await open(raw);
    try {
      const boxes = await page.evaluate(() => ['ipv4_icmp', 'ipv6_icmp', 'ipv4_tcp'].map(k => document.querySelector(`[data-nq-field="telecom-${k}"]`).disabled));
      assert.deepEqual(boxes, [true, true, false], what);
      assert.deepEqual(errors, []);
    } finally { await page.close(); }
  }
});

test('R1 (Chromium): ICMP unsupported + a literal address of the other family: every ticked box can be unticked and the save goes out with the right payload', {skip: !available && 'Playwright/Chromium not available'}, async () => {
  for (const [literal, clear, keep] of [['93.184.216.34', ['ipv6_tcp', 'ipv6_icmp'], ['ipv4_tcp', 'ipv4_icmp']], ['2606:4700::1111', ['ipv4_tcp', 'ipv4_icmp'], ['ipv6_tcp', 'ipv6_icmp']]]) {
    const {page, errors} = await open(load('v4-no-ipv6-no-icmp')); // both ICMP families unsupported, telecom = a name with all four ticked
    try {
      const box = k => page.evaluate(k => { const b = document.querySelector(`[data-nq-field="telecom-${k}"]`); return {checked: b.checked, disabled: b.disabled}; }, k);
      await page.fill('[data-nq-field="telecom-host"]', literal);
      for (const k of clear) assert.equal((await box(k)).disabled, false, `${literal}: ${k} must stay clearable`);
      for (const k of clear) await page.uncheck(`[data-nq-field="telecom-${k}"]`);
      for (const k of clear) assert.deepEqual(await box(k), {checked: false, disabled: true}, `${literal}: ${k} cannot be ticked again`);
      await page.click('[data-nq-field="save-top"]');
      await page.waitForFunction(() => window.__writes.length === 1);
      const telecom = (await page.evaluate(() => window.__writes[0])).slots[0];
      assert.equal(telecom.host, literal);
      for (const k of clear) assert.equal(telecom[k], false, k);
      for (const k of keep) assert.equal(telecom[k], true, k + ' stays as it was (an unsupported ICMP tick is kept, not cleared)');
      assert.deepEqual(errors, []);
    } finally { await page.close(); }
  }
});

test('R2 (Chromium): an automatic refresh that is still queued when the card leaves the viewport is dropped, and read once on return', {skip: !available && 'Playwright/Chromium not available'}, async () => {
  const {page, errors} = await open(load('v4-running'));
  try {
    const before = await page.evaluate(() => window.__gets);
    // Hold all four physical slots, then let 31 s pass so the refresh is queued behind them.
    await page.evaluate(() => { const owner = {}; window.__release = []; window.__blockers = Array.from({length: 4}, (_, i) => window.__q.request('block-' + i, owner, () => new Promise(r => window.__release.push(r)))); window.__clock = 31000; window.__ui.tick(); });
    await page.waitForTimeout(100);
    assert.equal(await page.evaluate(() => window.__gets), before, 'queued, not started');
    await page.evaluate(() => { document.getElementById('host').style.marginTop = '4000px'; window.__ui.tick(); }); // far below the viewport
    await page.evaluate(() => { for (const r of window.__release) r('done'); });
    await page.waitForTimeout(300);
    assert.equal(await page.evaluate(() => window.__gets), before, 'nothing is read for a card that is not on screen');
    assert.equal(await page.evaluate(() => window.__writes.length), 0);
    await page.evaluate(() => { document.getElementById('host').style.marginTop = '0'; window.__ui.tick(); });
    await page.waitForTimeout(300);
    assert.equal(await page.evaluate(() => window.__gets), before + 1, 'one catch-up read when it is back');
    assert.deepEqual(errors, []);
  } finally { await page.close(); }
});
