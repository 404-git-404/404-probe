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
    window.__raw = raw;
    window.__ui = window.NetworkQuality.create({fetcher: async (url, init = {}) => ({ok: true, status: 200, statusText: 'OK', json: async () => window.__raw}),
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
