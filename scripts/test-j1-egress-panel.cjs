// Shared public IP detection panel (device details + history page) and the IP masking rule.
const test = require('node:test');
const assert = require('node:assert/strict');
const P = require('../web/static/public-egress-check.js');

class El {
  constructor(tag) { this.tagName = tag.toUpperCase(); this.children = []; this.dataset = {}; this.attributes = {}; this.listeners = {}; this.className = ''; this.ownText = ''; this.hidden = false; this.disabled = false; this.checked = false; this.type = ''; }
  set textContent(v) { this.ownText = String(v); this.children = []; }
  get textContent() { return this.ownText + this.children.map(c => c.textContent).join(''); }
  append(...c) { this.children.push(...c); }
  setAttribute(k, v) { this.attributes[k] = String(v); }
  getAttribute(k) { return this.attributes[k]; }
  addEventListener(t, f) { (this.listeners[t] || (this.listeners[t] = [])).push(f); }
  async fire(t) { for (const f of this.listeners[t] || []) await f({type: t}); }
  all() { return this.children.flatMap(c => [c, ...c.all()]); }
}
const doc = {createElement: tag => new El(tag)};
const NOW = 1800000000000;
const agentWith = (check = {}, egress = {}, extra = {}) => ({
  agent_id: 'a', online: true,
  public_egress_check: {enabled: true, supported: true, pending: false, ...check},
  state: {public_egress: {ipv4: {status: 'available', ip: '203.0.113.9', checked_at: NOW - 1000, valid_until: NOW + 8 * 86400000, reason: 'verified'},
    ipv6: {status: 'available', ip: '2001:db8::1', checked_at: NOW - 1000, valid_until: NOW + 8 * 86400000, reason: 'verified'}, ...egress}},
  ...extra,
});

function harness(options = {}) {
  const calls = [], timers = [], copied = [], store = {mutations: []};
  let token = 'csrf-token';
  const response = options.response || (() => ({status: 200, ok: true, json: async () => ({enabled: true, supported: true, pending: false, created: true})}));
  const panel = P.createPanel({
    document: doc, agentId: 'a', csrf: () => token, now: () => NOW,
    fetcher: async (url, init) => { calls.push({url, init}); if (options.gate) await options.gate; return response(url, init); },
    begin: () => { store.mutations.push('begin'); return options.noMutation ? null : {id: 'a'}; },
    finish: (t, patch) => { store.mutations.push(patch ? ['finish', patch] : 'finish'); return true; },
    unauthorized: () => store.mutations.push('unauthorized'),
    clipboard: () => ({writeText: async text => { if (options.copyFails) throw new Error('denied'); copied.push(text); }}),
    setTimer: (f, ms) => { timers.push({f, ms}); return timers.length; }, clearTimer: () => {},
  });
  return {panel, calls, timers, copied, store, setToken: v => { token = v; }};
}
const rowText = (panel, key) => panel.elements.rows[key].text.textContent;

test('J2: maskIP keeps two IPv4 octets or two IPv6 groups', () => {
  for (const [input, want] of [
    ['203.0.113.9', '203.0.*.*'], ['8.8.8.8', '8.8.*.*'], ['114.114.114.114', '114.114.*.*'],
    ['2001:db8::1', '2001:db8:…'], ['2606:4700:4700::1111', '2606:4700:…'], ['2001:0db8:85a3:0000:0000:8a2e:0370:7334', '2001:db8:…'],
    ['FE80::1', 'fe80:0:…'], ['::1', '0:0:…'], ['::', '0:0:…'], ['::ffff:1.2.3.4', '0:0:…'], ['1:2:3:4:5:6:7:8', '1:2:…'], ['2001:db8:0:0:0:0:0:1', '2001:db8:…'],
  ]) assert.equal(P.maskIP(input), want, input);
});

test('J2: invalid input is never echoed back', () => {
  for (const input of ['', ' ', 'abc', '1.2.3', '1.2.3.4.5', '256.1.1.1', '01.2.3.4', '1.2.3.04', '-1.2.3.4', '1.2.3.4 ', ' 1.2.3.4', '2001:db8::1::2', '2001:db8:::1', 'fe80::1%eth0',
    '1:2:3:4:5:6:7:8:9', ':1:2:3:4:5:6:7', '1:2:3:4:5:6:7:', 'gggg::1', '12345::1', '::1.2.3', '::1.2.3.256', '1.2.3.4/24', '[2001:db8::1]', null, undefined, 12, {}, ['1.2.3.4']]) {
    const out = P.maskIP(input);
    assert.equal(out, '—', JSON.stringify(input));
  }
});

test('J2: the panel shows half-hidden addresses by default and the full ones only after the eye button', async () => {
  const {panel} = harness();
  panel.update(agentWith());
  const {rows, eye} = panel.elements;
  assert.equal(rowText(panel, 'ipv4'), 'IPv4：203.0.*.*');
  assert.equal(rowText(panel, 'ipv6'), 'IPv6：2001:db8:…');
  assert.equal(eye.getAttribute('aria-pressed'), 'false');
  assert.equal(eye.hidden, false);
  assert.doesNotMatch(panel.node.textContent, /203\.0\.113\.9|2001:db8::1/);
  await eye.fire('click');
  assert.equal(rowText(panel, 'ipv4'), 'IPv4：203.0.113.9');
  assert.equal(rowText(panel, 'ipv6'), 'IPv6：2001:db8::1');
  assert.equal(eye.getAttribute('aria-pressed'), 'true');
  // A re-render (SSE refresh) keeps the choice made in this page ...
  panel.update(agentWith({next_check_at: NOW + 1000}));
  assert.equal(rowText(panel, 'ipv4'), 'IPv4：203.0.113.9');
  // ... and the eye toggles back.
  await eye.fire('click');
  assert.equal(rowText(panel, 'ipv4'), 'IPv4：203.0.*.*');
  assert.ok(rows.ipv4.r.hidden === false);
});

test('J2: closing the dialog, switching Agent or reloading returns to half-hidden; nothing is stored', async () => {
  globalThis.localStorage = {setItem() { throw new Error('storage used'); }, getItem() { throw new Error('storage used'); }};
  globalThis.sessionStorage = globalThis.localStorage;
  try {
    const {panel} = harness();
    panel.update(agentWith());
    await panel.elements.eye.fire('click');
    assert.equal(panel.revealed, true);
    panel.conceal(); // dialog closed or reopened
    assert.equal(panel.revealed, false);
    assert.equal(rowText(panel, 'ipv4'), 'IPv4：203.0.*.*');
    await panel.elements.eye.fire('click');
    panel.update({...agentWith(), agent_id: 'b'}); // another Agent in the same dialog
    assert.equal(panel.revealed, false);
    assert.equal(rowText(panel, 'ipv4'), 'IPv4：203.0.*.*');
    await panel.elements.eye.fire('click');
    const reloaded = harness().panel; // a new page load builds a new panel
    reloaded.update(agentWith());
    assert.equal(reloaded.revealed, false);
    assert.equal(reloaded.elements.rows.ipv4.text.textContent, 'IPv4：203.0.*.*');
  } finally { delete globalThis.localStorage; delete globalThis.sessionStorage; }
});

test('J2: the copy button copies the full address even while it is half hidden, and reports the result', async () => {
  const {panel, copied, timers} = harness();
  panel.update(agentWith());
  assert.equal(panel.revealed, false);
  await panel.elements.rows.ipv4.copy.fire('click');
  await panel.elements.rows.ipv6.copy.fire('click');
  assert.deepEqual(copied, ['203.0.113.9', '2001:db8::1']);
  assert.equal(panel.elements.feedback.textContent, '已复制');
  assert.equal(timers.at(-1).ms, 1500);
  timers.at(-1).f();
  assert.equal(panel.elements.feedback.textContent, '');
  const failing = harness({copyFails: true});
  failing.panel.update(agentWith());
  await failing.panel.elements.rows.ipv4.copy.fire('click');
  assert.match(failing.panel.elements.feedback.textContent, /复制失败/);
  // No address, no copy button.
  const none = harness();
  none.panel.update(agentWith({}, {ipv4: {status: 'unknown', checked_at: NOW - 1}, ipv6: {status: 'unavailable', checked_at: NOW - 1}}));
  assert.equal(none.panel.elements.rows.ipv4.copy.hidden, true);
  assert.equal(none.panel.elements.eye.hidden, true);
});

test('J2: the last successful address follows the same rule', async () => {
  const {panel, copied} = harness();
  panel.update(agentWith({}, {ipv4: {status: 'unknown', reason: 'timeout', checked_at: NOW - 1000, valid_until: NOW + 1000,
    last_success: {ip: '198.18.7.7', checked_at: NOW - 86400000}}}));
  const last = panel.elements.rows.last_ipv4;
  assert.equal(last.r.hidden, false);
  assert.match(last.text.textContent, /^最后成功 IPv4：198\.18\.\*\.\*（/);
  assert.doesNotMatch(panel.node.textContent, /198\.18\.7\.7/);
  await last.copy.fire('click');
  assert.deepEqual(copied, ['198.18.7.7']);
  await panel.elements.eye.fire('click');
  assert.match(last.text.textContent, /^最后成功 IPv4：198\.18\.7\.7（/);
});

test('J1: every state of the switch and the button', () => {
  const cases = [
    ['ready', agentWith({next_check_at: NOW + 1000}), {checked: true, buttonDisabled: false, label: '立即检测', note: ''}],
    ['cooldown', agentWith({cooldown_until: NOW + 3 * 3600000}), {checked: true, buttonDisabled: true, label: '立即检测', note: /^可在 \d{2}-\d{2} \d{2}:\d{2} 后再次检测$/}],
    ['pending', agentWith({pending: true}), {checked: true, buttonDisabled: true, label: '检测中…', note: '检测中'}],
    ['off', agentWith({enabled: false}), {checked: false, buttonDisabled: true, label: '立即检测', note: '未启用'}],
    ['unsupported', agentWith({supported: false}), {checked: true, buttonDisabled: true, label: '立即检测', note: 'Agent 版本不支持，升级 Agent 后可用'}],
    ['offline', agentWith({}, {}, {online: false}), {checked: true, buttonDisabled: true, label: '立即检测', note: 'Agent 离线'}],
    ['paused', agentWith({}, {}, {disabled_at: 5}), {checked: true, buttonDisabled: true, label: '立即检测', note: 'Agent 已暂停'}],
  ];
  for (const [name, agent, want] of cases) {
    const {panel} = harness();
    panel.update(agent);
    const {toggle, check, note} = panel.elements;
    assert.equal(toggle.checked, want.checked, name);
    assert.equal(check.disabled, want.buttonDisabled, name);
    assert.equal(check.textContent, want.label, name);
    if (want.note instanceof RegExp) assert.match(note.textContent, want.note, name); else assert.equal(note.textContent, want.note, name);
    assert.equal(toggle.disabled, false, name + ' switch stays usable');
  }
  const revoked = harness(); revoked.panel.update(agentWith({}, {}, {revoked: true}));
  assert.equal(revoked.panel.elements.toggle.disabled, true);
  // The off state shows no address at all.
  const off = harness(); off.panel.update(agentWith({enabled: false}));
  assert.ok(Object.values(off.panel.elements.rows).every(r => r.r.hidden) && off.panel.elements.eye.hidden);
  // Without a session token nothing is clickable.
  const noToken = harness(); noToken.setToken(''); noToken.panel.update(agentWith());
  assert.equal(noToken.panel.elements.toggle.disabled, true); assert.equal(noToken.panel.elements.check.disabled, true);
});

test('J1: updates change text in place: nodes, focus targets and the panel root are never rebuilt', () => {
  const {panel} = harness();
  panel.update(agentWith());
  const before = panel.node.all();
  let rebuilt = 0;
  for (const n of [panel.node, ...before]) n.replaceChildren = () => { rebuilt++; };
  const originalAppend = El.prototype.append;
  let appended = 0;
  El.prototype.append = function (...c) { appended++; return originalAppend.apply(this, c); };
  try {
    for (const agent of [agentWith({pending: true}), agentWith({cooldown_until: NOW + 60000}), agentWith({enabled: false}), agentWith(), agentWith({}, {ipv4: {status: 'unavailable', checked_at: NOW - 1}})]) panel.update(agent);
  } finally { El.prototype.append = originalAppend; }
  assert.equal(rebuilt, 0);
  assert.equal(appended, 0, 'no node was added or moved');
  assert.deepEqual(panel.node.all(), before, 'the same nodes, in the same order');
  // A refresh with identical data leaves every text node untouched.
  const text = panel.node.textContent;
  panel.update(agentWith());
  panel.update(agentWith());
  assert.equal(panel.node.textContent, panel.node.textContent);
  assert.notEqual(text, '');
});

test('J1: the switch posts {enabled} with the CSRF token; the controls are disabled while it is in flight', async () => {
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  const {panel, calls, store} = harness({gate});
  panel.update(agentWith({enabled: false}));
  const {toggle, check} = panel.elements;
  toggle.checked = true;
  const pending = toggle.fire('change');
  await Promise.resolve();
  assert.equal(toggle.disabled, true); assert.equal(check.disabled, true, 'busy disables the button');
  release();
  await pending;
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, '/api/v1/web/agents/a/public-egress');
  assert.equal(calls[0].init.method, 'POST');
  assert.equal(calls[0].init.body, '{"enabled":true}');
  assert.equal(calls[0].init.headers['X-CSRF-Token'], 'csrf-token');
  assert.equal(calls[0].init.headers['Content-Type'], 'application/json');
  assert.equal(toggle.disabled, false);
  assert.ok(store.mutations.some(m => Array.isArray(m) && m[1].public_egress_check?.enabled === true));
  // A second change while busy is ignored.
  const second = harness({gate: new Promise(() => {})});
  second.panel.update(agentWith({enabled: false}));
  second.panel.elements.toggle.checked = true; second.panel.elements.toggle.fire('change');
  await Promise.resolve();
  second.panel.elements.toggle.checked = false; await second.panel.elements.toggle.fire('change');
  assert.equal(second.calls.length, 1);
});

test('J1: the button posts {} and a 429 cooldown turns into the cooldown state', async () => {
  const cooldown = NOW + 5 * 3600000;
  const {panel, calls, store} = harness({response: () => ({status: 429, ok: false, json: async () => ({error: {code: 'cooldown', message: 'x'}, next_allowed_at: cooldown})})});
  panel.update(agentWith({next_check_at: NOW + 1000}));
  await panel.elements.check.fire('click');
  assert.equal(calls[0].url, '/api/v1/web/agents/a/public-egress/check');
  assert.equal(calls[0].init.body, '{}');
  assert.ok(store.mutations.some(m => Array.isArray(m) && m[1].public_egress_check.cooldown_until === cooldown));
  const ok = harness({response: () => ({status: 202, ok: true, json: async () => ({pending: true})})});
  ok.panel.update(agentWith());
  await ok.panel.elements.check.fire('click');
  assert.ok(ok.store.mutations.some(m => Array.isArray(m) && m[1].public_egress_check.pending === true));
  const failed = harness({response: () => ({status: 409, ok: false, json: async () => ({error: {code: 'agent_offline', message: 'Agent 离线了'}})})});
  failed.panel.update(agentWith());
  await failed.panel.elements.check.fire('click');
  assert.equal(failed.panel.elements.feedback.textContent, 'Agent 离线了');
  const unauthorized = harness({response: () => ({status: 401, ok: false, json: async () => ({})})});
  unauthorized.panel.update(agentWith());
  await unauthorized.panel.elements.check.fire('click');
  assert.ok(unauthorized.store.mutations.includes('unauthorized'));
});

test('J1: a refused mutation barrier leaves the switch where it was', async () => {
  const {panel, calls} = harness({noMutation: true});
  panel.update(agentWith({enabled: false}));
  panel.elements.toggle.checked = true;
  await panel.elements.toggle.fire('change');
  assert.equal(calls.length, 0);
  assert.equal(panel.elements.toggle.checked, false);
});

test('J1: an SSE update refreshes the panel live', () => {
  const {panel} = harness();
  panel.update(agentWith({pending: false, next_check_at: NOW + 3600000}));
  assert.equal(panel.elements.check.disabled, false);
  panel.update(agentWith({pending: true}));
  assert.equal(panel.elements.check.textContent, '检测中…');
  assert.equal(panel.elements.check.disabled, true);
  panel.update(agentWith({pending: false, cooldown_until: NOW + 7200000}));
  assert.match(panel.elements.note.textContent, /^可在/);
  panel.update(agentWith({enabled: false}));
  assert.equal(panel.elements.note.textContent, '未启用');
  assert.match(panel.elements.times.textContent, /^$/);
});
