// R07 stage D: the dialog in a real Chromium with the production style sheets and scripts. The fake DOM of
// test-r07-network-quality.cjs knows neither CSS nor browser focus rules; these checks are what it cannot see.
// Needs Playwright and a Chromium build (M02_PLAYWRIGHT / M02_CHROMIUM, as test-m02-real-chain.cjs); skipped without them.
'use strict';
const {v5Card} = require('./lib/r07-harness.cjs');
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
async function open(raw, {dialog = true, history = null, plot = false, host = false, scrollerHeight = 300, v5 = null} = {}) {
  const page = await browser.newPage({viewport: {width: 1000, height: 800}}), errors = [];
  page.on('pageerror', e => errors.push(e.message));
  await page.setContent('<!doctype html><html><head><meta charset="utf-8"></head><body><div id="host" class="c7"></div><dialog id="network-quality-history"></dialog></body></html>');
  for (const css of ['style.css', 'network-quality.css', 'card-v7.css']) await page.addStyleTag({content: read(css)});
  if (plot) { await page.addStyleTag({content: fs.readFileSync(path.join(root, 'vendor', 'uplot', 'uPlot.min.css'), 'utf8')}); await page.addScriptTag({content: fs.readFileSync(path.join(root, 'vendor', 'uplot', 'uPlot.iife.min.js'), 'utf8')}); }
  for (const js of ['agent-state.js', 'management.js', 'network-quality-core.js', 'network-quality.js']) await page.addScriptTag({content: read(js)});
  await page.evaluate(({raw, agent, history, plot, host, scrollerHeight, v5}) => {
    window.__hist = history; window.__v5 = v5; window.__plots = []; window.__scrollerHeight = scrollerHeight;
    const Plot = plot ? function(opts, data, target) { const u = new window.uPlot(opts, data, target); window.__plots.push(u); return u; } : undefined;
    // The device-details dialog hosts the history in a container that scrolls (its body): the same shape here.
    const historyHost = host ? (() => { const wrap = document.createElement('div'); wrap.id = 'scroller'; wrap.style.cssText = 'height:' + (window.__scrollerHeight || 300) + 'px;overflow:auto;width:700px'; const box = document.createElement('div'); box.id = 'history-box'; wrap.append(box); document.body.append(wrap); return {container: () => box, active: () => true, open() {}, close() {}}; })() : null;
    window.__raw = raw; window.__writes = []; window.__gets = 0; window.__clock = 0;
    const now = () => 1800000000000 + window.__clock;
    window.__q = window.NetworkQualityCore.coordinator({now});
    window.__ui = window.NetworkQuality.create({coordinator: window.__q, now, ...(Plot ? {Plot} : {}), ...(historyHost ? {historyHost} : {}), onConfig: id => { if (window.__restoreTarget) window.__ui.openHistoryFor(id, window.__restoreTarget, 'ipv4', 'latency', document.body); }, fetcher: async (url, init = {}) => { if (window.__v5 && url.includes('/v5/card')) return {ok: true, status: 200, statusText: 'OK', json: async () => structuredClone(window.__v5)}; if (window.__hist && url.includes('v3/history') && url.includes('view=1hour')) return {ok: true, status: 200, statusText: 'OK', json: async () => structuredClone(window.__hist.card)}; if (init.method === 'PUT') window.__writes.push(JSON.parse(init.body)); else if (url.endsWith('/v4/config')) window.__gets++; return {ok: true, status: 200, statusText: 'OK', json: async () => window.__raw}; },
      csrf: () => 'synthetic', interval: () => 0, clear() {}, Observer: null, Resize: null});
    const node = window.__ui.mount({agent_id: agent, name: '合成设备'});
    document.getElementById('host').append(node);
    window.__ui.setVisible(agent, true);
  }, {raw, agent: AGENT, history, plot, host, scrollerHeight, v5});
  await page.waitForFunction(() => document.querySelector('.nq-link'));
  await page.waitForTimeout(150);
  if (dialog) {
    await page.click('.nq-link');
    await page.waitForSelector('.nq-overlay');
    await page.waitForTimeout(150);
  }
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

test('R3 (Chromium): a read the user asked for (opening the dialog) is adopted even when a queued automatic refresh is cancelled because the card left the viewport', {skip: !available && 'Playwright/Chromium not available'}, async () => {
  for (let round = 1; round <= 3; round++) {
    const {page, errors} = await open(load('v4-running'), {dialog: false});
    try {
      const before = await page.evaluate(() => window.__gets);
      // 1. the automatic refresh is queued behind four busy slots
      await page.evaluate(() => { const owner = {}; window.__release = []; window.__blockers = Array.from({length: 4}, (_, i) => window.__q.request('block-' + i, owner, () => new Promise(r => window.__release.push(r)))); window.__clock = 31000; window.__ui.tick(); });
      // 2. another page changed the address; the user opens the settings, which queues the explicit read
      await page.evaluate(() => { const r = structuredClone(window.__raw); r.settings_revision = String(BigInt(r.settings_revision) + 1n); r.slots[0].host = 'changed.example.net'; window.__raw = r; });
      await page.click('.nq-link');
      await page.waitForSelector('.nq-overlay');
      // 3. the card leaves the viewport (the dialog stays), the slots are released
      await page.evaluate(() => { document.getElementById('host').style.marginTop = '4000px'; window.__ui.tick(); });
      await page.evaluate(() => { for (const r of window.__release) r('done'); });
      await page.waitForTimeout(300);
      assert.equal(await page.inputValue('[data-nq-field="telecom-host"]'), 'changed.example.net', 'round ' + round + ': the dialog shows what the user\'s own read returned');
      assert.equal((await page.evaluate(() => window.__ui.snapshot().panel)).base, String(BigInt(load('v4-running').settings_revision) + 1n));
      assert.equal(await page.evaluate(() => window.__gets), before + 1, 'only the user\'s read ran; the automatic one was dropped (R2 stays fixed)');
      assert.equal(await page.evaluate(() => window.__writes.length), 0);
      assert.deepEqual(errors, []);
    } finally { await page.close(); }
  }
});

test('D1 (Chromium): the card draws the v5/card answer of the Server: twenty blocks per lane, the shapes of yellow and red, the number at the right', {skip: !available && 'Playwright/Chromium not available'}, async () => {
  const raw = load('v4-running'), v5 = v5Card(raw, 'ipv4');
  const blocks = v5.lanes[0].tcp.blocks; blocks[3].state = 'yellow'; blocks[4].state = 'red'; blocks[5].state = 'gray';
  const {page, errors} = await open(raw, {dialog: false, v5});
  try {
    await page.waitForFunction(() => document.querySelectorAll('.nq7-row')[0]?.querySelectorAll('.nq7-b').length === 20, null, {timeout: 8000});
    const facts = await page.evaluate(() => { const cells = [...document.querySelectorAll('.nq7-row')[0].querySelectorAll('.nq7-b')], px = (el, p) => getComputedStyle(el, '::after')[p];
      return {n: cells.length, green: getComputedStyle(cells[0]).backgroundColor, greenEdge: getComputedStyle(cells[0]).borderTopColor, h: cells[0].getBoundingClientRect().height,
        yellowBar: [px(cells[3], 'width'), px(cells[3], 'height')], redDot: [px(cells[4], 'width'), px(cells[4], 'height'), px(cells[4], 'backgroundColor')], grayAfter: px(cells[5], 'content'), greenAfter: px(cells[0], 'content'),
        right: document.querySelector('.nq7-right').textContent}; });
    assert.equal(facts.n, 20); assert.equal(facts.green, 'rgb(140, 197, 181)'); assert.equal(facts.greenEdge, 'rgb(62, 156, 134)'); assert.equal(facts.h, 18);
    assert.deepEqual(facts.yellowBar, ['8px', '2px']); assert.deepEqual(facts.redDot, ['3px', '3px', 'rgb(255, 255, 255)']);
    assert.ok(facts.grayAfter === 'none' && facts.greenAfter === 'none', 'green and gray carry no mark');
    assert.equal(facts.right, '失败 0%');
    assert.deepEqual(errors, []);
  } finally { await page.close(); }
});

// 1h answer of the Server with the latency of the maintainer's screenshot: P50 1.36 - 1.47 ms, P95 up to 1.52 ms.
const narrowRange = () => { const h = load('history-card-tcp'), points = h.groups[0].points; points.forEach((p, i) => { p.p50_ms = 1.36 + i * 0.01; p.p95_ms = 1.4 + i * 0.01; p.count = 6; p.valid_rtts = 6; p.probe_attempts = 6; p.probe_successes = 6; p.connect_attempts = 6; p.connect_successes = 6; }); return h; };
const openHistory = async (page, targetId) => page.evaluate(id => window.__ui.openHistoryFor('f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1', id, 'ipv4', 'latency', document.body), targetId);
const laneId = raw => raw.lanes.find(l => l.slot === 'telecom' && l.family === 'ipv4' && l.protocol === 'tcp').target_id;

test('O1 (Chromium): the Y axis of the history plot is wide enough for its labels and uses one number of decimals (1.36-1.52 ms)', {skip: !available && 'Playwright/Chromium not available'}, async () => {
  const raw = load('v4-running'), history = {card: narrowRange()};
  const {page, errors} = await open(raw, {dialog: false, history, plot: true});
  try {
    await page.waitForFunction(() => document.querySelector('.nq-link'));
    assert.equal(await openHistory(page, laneId(raw)), true);
    await page.waitForFunction(() => window.__plots.length > 0, null, {timeout: 8000});
    const axis = await page.evaluate(() => { const u = window.__plots.at(-1), y = u.axes[1], labels = y._values || [];
      const ctx = document.createElement('canvas').getContext('2d'); ctx.font = (y.font && y.font[0]) || '12px system-ui';
      return {labels, size: y._size, widest: Math.max(...labels.filter(Boolean).map(t => ctx.measureText(t).width)), font: ctx.font}; });
    const shown = axis.labels.filter(Boolean);
    assert.ok(shown.length >= 3, JSON.stringify(axis));
    const decimals = new Set(shown.map(t => (t.match(/\.(\d+)/) || [, ''])[1].length));
    assert.equal(decimals.size, 1, 'one number of decimals on the axis: ' + shown.join(' | '));
    assert.ok(axis.size >= axis.widest, 'the axis is as wide as its widest label (' + axis.size + ' < ' + axis.widest + '): ' + shown.join(' | '));
    assert.deepEqual(errors, []);
  } finally { await page.close(); }
});

test('Scroll (Chromium): a refresh of the history keeps the scroll position of the container it lives in', {skip: !available && 'Playwright/Chromium not available'}, async () => {
  const raw = load('v4-running'), history = {card: narrowRange()};
  const {page, errors} = await open(raw, {dialog: false, history, plot: true, host: true, scrollerHeight: 560});
  try {
    await page.waitForFunction(() => document.querySelector('.nq-link'));
    assert.equal(await openHistory(page, laneId(raw)), true);
    await page.waitForFunction(() => document.querySelector('#history-box .nq-plot canvas'), null, {timeout: 8000});
    // Make it scroll, scroll down, then let the 30 s refresh bring a new answer.
    await page.evaluate(() => { document.querySelector('#scroller').scrollTop = 10000; }); // as far down as it goes: the plot is below the fold
    const before = await page.evaluate(() => document.querySelector('#scroller').scrollTop);
    assert.ok(before > 50, 'the container scrolls: ' + before);
    await page.evaluate(() => { window.__clock += 31000; window.__ui.tick(); });
    await page.waitForTimeout(800);
    const redrawn = await page.evaluate(() => window.__plots.length);
    assert.ok(redrawn >= 2, 'the history was drawn again (' + redrawn + ' plots)');
    assert.equal(await page.evaluate(() => document.querySelector('#scroller').scrollTop), before, 'the scroll position stays');
    assert.deepEqual(errors, []);
  } finally { await page.close(); }
});

test('Scroll (Chromium): a change elsewhere in the configuration neither closes nor rebuilds the history that is open', {skip: !available && 'Playwright/Chromium not available'}, async () => {
  const raw = load('v4-running'), history = {card: narrowRange()};
  const {page, errors} = await open(raw, {dialog: false, history, plot: true, host: true, scrollerHeight: 560});
  try {
    await page.waitForFunction(() => document.querySelector('.nq-link'));
    const id = laneId(raw);
    await page.evaluate(id => { window.__restoreTarget = id; }, id);
    assert.equal(await openHistory(page, id), true);
    await page.waitForFunction(() => document.querySelector('#history-box .nq-plot canvas'), null, {timeout: 8000});
    await page.evaluate(() => { document.querySelector('#scroller').scrollTop = 10000; });
    const before = await page.evaluate(() => document.querySelector('#scroller').scrollTop);
    assert.ok(before > 50, 'the container scrolls: ' + before);
    // The Server reports another lane differently (the unicom target of the old fixture turns abnormal): the measurement
    // signature of the card changes, the viewed telecom TCP target does not.
    await page.evaluate(() => { const r = structuredClone(window.__raw); const lane = r.lanes.find(l => l.slot === 'unicom' && l.family === 'ipv4' && l.protocol === 'tcp'); lane.status = 'target_abnormal'; lane.condition = 'not_found'; window.__raw = r; window.__clock += 31000; window.__ui.tick(); });
    await page.waitForTimeout(1200);
    assert.equal(await page.evaluate(() => document.querySelector('#scroller').scrollTop), before, 'the scroll position stays');
    assert.equal(await page.evaluate(() => /正在读取指定目标/.test(document.querySelector('#history-box').innerText)), false, 'the history was not rebuilt from scratch');
    assert.deepEqual(errors, []);
  } finally { await page.close(); }
});


test('R09 card lines have no button semantics or history opening on click',{skip:!available},async()=>{
 const {page}=await open(load('v4-running'),{dialog:false});try{const row=page.locator('.nq7-row').first();assert.equal(await row.evaluate(n=>n.tagName),'DIV');assert.equal(await row.getAttribute('title'),null);await row.click();assert.equal(await page.locator('#network-quality-history').evaluate(n=>n.open),false);}finally{await page.close();}
});
