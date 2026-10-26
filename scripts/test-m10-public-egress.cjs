const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const State = require('../web/static/agent-state.js');

const available = ip => ({family: 'ipv4', status: 'available', ip, checked_at: 1000});
const state = (ip, extra = {}) => ({cpu_percent: 12, public_egress: {version: 1, supported: true, ipv4: available(ip), ...extra}});
const seed = (store, records) => {
  const cycle = store.beginRefresh();
  store.commitList(cycle, records);
  store.endRefresh(cycle);
};

test('M10: in-order events replace egress, including restart, invalid report and unsupported', () => {
  const store = State.create(), a = 'a';
  seed(store, [{agent_id: a, state: state('8.8.8.8')}]);
  const ipv4 = () => store.agents.get(a).state.public_egress.ipv4;
  store.event({agent_id: a, state: state('4.2.2.2')});
  assert.equal(ipv4().ip, '4.2.2.2');
  store.event({agent_id: a, state: state('', {ipv4: {family: 'ipv4', status: 'unknown', reason: 'not_checked', checked_at: 0}})});
  assert.equal(ipv4().reason, 'not_checked');
  store.event({agent_id: a, state: state('', {ipv4: {family: 'ipv4', status: 'unknown', reason: 'invalid_report', checked_at: 0}})});
  assert.equal(ipv4().reason, 'invalid_report');
  store.event({agent_id: a, state: {public_egress: {version: 1, supported: false, ipv4: {family: 'ipv4', status: 'unknown', reason: 'not_reported'}}}});
  assert.equal(store.agents.get(a).state.public_egress.supported, false);
  assert.equal(ipv4().status, 'unknown');
});

test('M10: a list snapshot cannot overwrite egress from an event that arrived during the refresh', () => {
  const store = State.create(), a = 'a';
  seed(store, [{agent_id: a, state: state('8.8.8.8')}]);
  const cycle = store.beginRefresh();
  store.event({agent_id: a, state: state('4.2.2.2')});
  store.commitList(cycle, [{agent_id: a, state: state('8.8.8.8')}]);
  store.endRefresh(cycle);
  assert.equal(store.agents.get(a).state.public_egress.ipv4.ip, '4.2.2.2');
});

test('M10: a detail read started before an event cannot overwrite its egress', () => {
  const store = State.create(), a = 'a';
  seed(store, [{agent_id: a, state: state('8.8.8.8')}]);
  const token = store.read(a);
  store.event({agent_id: a, state: state('4.2.2.2')});
  store.commitDetail(token, {agent_id: a, state: state('8.8.8.8')});
  assert.equal(store.agents.get(a).state.public_egress.ipv4.ip, '4.2.2.2');
});

test('M10: egress never crosses Agents', () => {
  const store = State.create(), a = 'a', b = 'b';
  seed(store, [{agent_id: a, state: state('8.8.8.8')}, {agent_id: b, state: state('1.1.1.1')}]);
  const token = store.read(a);
  assert.equal(store.commitDetail(token, {agent_id: b, state: state('9.9.9.9')}), false);
  assert.equal(store.agents.get(a).state.public_egress.ipv4.ip, '8.8.8.8');
  assert.equal(store.agents.get(b).state.public_egress.ipv4.ip, '1.1.1.1');
});

test('M10: the Web store does not depend on report epoch or sequence', () => {
  const source = fs.readFileSync(require.resolve('../web/static/agent-state.js'), 'utf8');
  assert.doesNotMatch(source, /epoch|sequence/i);
});

// Card rendering: only usable, fresh address families are shown.
const vm = require('node:vm'), path = require('node:path');
class Element {
  constructor(tag = 'div') {this.tagName = tag.toUpperCase(); this.children = []; this.dataset = {}; this.attributes = {}; this.style = {}; this.ownText = ''; this.classList = {add() {}, remove() {}, toggle() {}};}
  set textContent(value) {this.ownText = String(value); this.children = [];}
  get textContent() {return this.ownText + this.children.map(child => child.textContent).join('');}
  append(...children) {this.children.push(...children);}
  replaceChildren(...children) {this.ownText = ''; this.children = children;}
  setAttribute(key, value) {this.attributes[key] = value;}
  addEventListener() {} querySelector() {return new Element();} querySelectorAll() {return [];}
}
function loadApp() {
  const nodes = new Map(), node = key => {if (!nodes.has(key)) nodes.set(key, new Element()); return nodes.get(key);};
  const sandbox = {AgentState: {...State, fetchJSON: () => new Promise(() => {})}, AbortController, URLSearchParams, Intl, Date, Map, Set, Number, Object, JSON,
    document: {querySelector: node, querySelectorAll: () => [], activeElement: null, createElement: tag => new Element(tag), addEventListener() {}, removeEventListener() {}},
    location: {search: '', assign() {}}, setInterval() {}, setTimeout, queueMicrotask, addEventListener() {},
    navigator: {clipboard: {writeText: async () => {}}}, crypto: {randomUUID: () => 'b'.repeat(32)},
    fetch: () => new Promise(() => {}), EventSource: class {addEventListener() {} close() {}}};
  sandbox.DialogShell={attach:()=>({heading(){}}),confirm:async()=>sandbox.confirm?sandbox.confirm():true};vm.createContext(sandbox);
  for (const file of ['management.js', 'selector.js', 'range-slider.js', 'network-quality-core.js', 'network-quality.js', 'traffic-chart.js', 'card-metrics.js', 'card.js', 'resource-history.js', 'device-details.js', 'plan-editor.js', 'plan-renewal.js', 'overview.js', 'plan-units.js', 'youtube-status.js', 'public-egress-check.js', 'agent-enrollment.js', 'latency-mini.js', 'app.js'])
    vm.runInContext(fs.readFileSync(path.join(__dirname, '../web/static', file), 'utf8'), sandbox);
  const render = input => {sandbox.input = input; return vm.runInContext('(()=>{const e=createPublicEgress("a");e.update(input);return e.element;})()', sandbox);};
  render.live = () => {const e = vm.runInContext('createPublicEgress("a")', sandbox); return input => {sandbox.live = e; sandbox.input = input; vm.runInContext('live.update(input)', sandbox); return e.element;};};
  return render;
}
const render = loadApp();
const now = Date.now();
const fresh = (family, ip, extra = {}) => ({family, status: 'available', ip, reason: 'verified', checked_at: now - 1000, valid_until: now + 600000, ...extra});
const card = (egress, extra = {}) => ({agent_id: 'a', online: true, state: {public_egress: {version: 1, supported: true, ...egress}}, ...extra});
const shown = element => element.children.filter(c => c.tagName === 'BUTTON').map(c => c.textContent);

test('M10 UI: fresh available families are shown', () => {
  assert.deepEqual(shown(render(card({ipv4: fresh('ipv4', '8.8.8.8'), ipv6: fresh('ipv6', '2001:db8::1')}))), ['V4', 'V6']);
  assert.deepEqual(shown(render(card({ipv4: fresh('ipv4', '8.8.8.8')}))), ['V4']);
  const el = render(card({ipv6: fresh('ipv6', '2001:db8::1')}));
  assert.deepEqual(shown(el), ['V6']);
  assert.match(el.children[0].attributes['aria-label'], /2001:db8:…/);
  assert.doesNotMatch(el.children[0].attributes['aria-label'], /2001:db8::1/, 'aria-label shows the half-hidden address only');
  assert.doesNotMatch(el.children[1].textContent, /2001:db8::1/, 'tooltip shows the half-hidden address only');
});

test('M10 UI: unavailable, unknown, stale or empty-IP families are hidden and the container is empty', () => {
  for (const one of [
    {family: 'ipv4', status: 'unavailable', reason: 'no_route', checked_at: now - 1000, valid_until: now + 600000},
    {family: 'ipv4', status: 'unknown', reason: 'not_checked', checked_at: 0},
    fresh('ipv4', '8.8.8.8', {stale: true}),
    fresh('ipv4', '8.8.8.8', {valid_until: now - 1}),
    fresh('ipv4', '8.8.8.8', {checked_at: 0}),
    fresh('ipv4', ''),
  ]) assert.equal(render(card({ipv4: one, ipv6: one.family ? {...one, family: 'ipv6'} : one})).children.length, 0);
  assert.equal(render(card({})).children.length, 0);
});

test('M10 UI: offline, disabled, revoked or stale Agents hide every family', () => {
  const egress = {ipv4: fresh('ipv4', '8.8.8.8'), ipv6: fresh('ipv6', '2001:db8::1')};
  for (const extra of [{online: false}, {disabled_at: 1}, {revoked: true}]) assert.equal(render(card(egress, extra)).children.length, 0);
  const staleState = card(egress); staleState.state.stale = true;
  assert.equal(render(staleState).children.length, 0);
});

test('M10 UI: labels appear and disappear on the same card as state changes', () => {
  const update = render.live();
  const up = card({ipv4: fresh('ipv4', '8.8.8.8')});
  assert.deepEqual(shown(update(up)), ['V4']);
  assert.equal(update(card({ipv4: {family: 'ipv4', status: 'unavailable', reason: 'timeout', checked_at: now}})).children.length, 0);
  assert.deepEqual(shown(update(card({ipv4: fresh('ipv4', '8.8.8.8'), ipv6: fresh('ipv6', '2001:db8::1')}))), ['V4', 'V6']);
  assert.equal(update({...up, online: false}).children.length, 0);
});

test('M10 UI: an unchanged set of families keeps its nodes in the DOM and refreshes the tooltip text', () => {
  const update = render.live();
  const el = update(card({ipv4: fresh('ipv4', '8.8.8.8')}));
  let rebuilt = 0;
  const original = el.replaceChildren.bind(el);
  el.replaceChildren = (...children) => {rebuilt++; original(...children);};
  const v4 = el.children[0], tip = el.children[1];
  update(card({ipv4: fresh('ipv4', '9.9.4.4')}));
  assert.equal(rebuilt, 0);
  assert.equal(el.children[0], v4);
  assert.match(tip.textContent, /9\.9\.\*\.\*/);
  assert.match(v4.attributes['aria-label'], /9\.9\.\*\.\*/);
  assert.doesNotMatch(tip.textContent + v4.attributes['aria-label'], /9\.9\.4\.4/);
  update(card({ipv4: fresh('ipv4', '9.9.4.4'), ipv6: fresh('ipv6', '2001:db8::1')}));
  assert.equal(rebuilt, 1);
  assert.equal(el.children[0], v4);
});

test('M10 UI: the tooltip shows a reason only when it is verified', () => {
  const text = reason => render(card({ipv4: fresh('ipv4', '8.8.8.8', {reason})})).children[1].textContent;
  assert.match(text('verified'), /已验证/);
  assert.doesNotMatch(text('network'), /尚无可靠结果|已验证/);
  assert.match(text('network'), /8\.8\.\*\.\* · .*；Agent/);
});

// ---------- detail page: switch and "check now" button ----------
const PublicEgressCheck = require('../web/static/public-egress-check.js');
const T0 = 1800000000000;
const panelAgent = (check = {}, extra = {}) => ({
  agent_id: 'a', online: true,
  public_egress_check: {enabled: true, supported: true, pending: false, server_now: T0, ...check},
  state: {public_egress: {ipv4: {status: 'unknown', checked_at: 0}, ipv6: {status: 'unknown', checked_at: 0}}},
  ...extra,
});

test('M10 panel: off by default shows 未启用 and nothing can be triggered', () => {
  const view = PublicEgressCheck.view(panelAgent({enabled: false}), T0);
  assert.equal(view.state, 'off');
  assert.equal(view.enabled, false);
  assert.equal(view.note, '未启用');
  assert.equal(view.buttonDisabled, true);
  assert.equal(view.switchDisabled, false);
  assert.deepEqual(view.lines, ['公网 IP 检测：未启用']);
  assert.equal(PublicEgressCheck.view({agent_id: 'a', online: true}, T0).state, 'off', 'an agent without the field is off');
});

test('M10 panel: ready when on, online, idle and out of cooldown', () => {
  const view = PublicEgressCheck.view(panelAgent({next_check_at: T0 + 3600000}), T0);
  assert.equal(view.state, 'ready');
  assert.equal(view.buttonDisabled, false);
  assert.equal(view.buttonLabel, '立即检测');
  assert.equal(view.note, '');
  assert.equal(view.enabled, true);
  assert.match(view.lines.join('\n'), /下次检测：/);
});

test('M10 panel: cooling down always shows the local date and 24-hour time', () => {
  const now = new Date(2026, 9, 20, 18, 0).getTime();
  const until = new Date(2026, 9, 20, 23, 59).getTime();
  const view = PublicEgressCheck.view(panelAgent({cooldown_until: until}), now);
  assert.equal(view.state, 'cooldown');
  assert.equal(view.buttonDisabled, true);
  assert.equal(view.note, '可在 10-20 23:59 后再次检测');
  // A cooldown that has ended no longer blocks the button.
  assert.equal(PublicEgressCheck.view(panelAgent({cooldown_until: now - 1}), now).state, 'ready');
});

test('M10 panel: cooling down across days includes the date and pads midnight', () => {
  const now = new Date(2026, 9, 20, 23, 59).getTime();
  const until = new Date(2026, 9, 21, 0, 5).getTime();
  const view = PublicEgressCheck.view(panelAgent({cooldown_until: until}), now);
  assert.equal(view.note, '可在 10-21 00:05 后再次检测');
});

test('M10 panel: cooling down across years includes the destination year', () => {
  const now = new Date(2026, 11, 31, 23, 59).getTime();
  const until = new Date(2027, 0, 1, 0, 0).getTime();
  const view = PublicEgressCheck.view(panelAgent({cooldown_until: until}), now);
  assert.equal(view.note, '可在 2027-01-01 00:00 后再次检测');
});

test('M10 panel: a check in flight shows 检测中, ahead of the cooldown', () => {
  const view = PublicEgressCheck.view(panelAgent({pending: true, cooldown_until: T0 + 1000}), T0);
  assert.equal(view.state, 'pending');
  assert.equal(view.note, '检测中');
  assert.equal(view.buttonLabel, '检测中…');
  assert.equal(view.buttonDisabled, true);
});

test('M10 panel: an old Agent is unsupported, while the switch and any legacy result stay usable', () => {
  const agent = panelAgent({supported: false}, {state: {public_egress: {ipv4: {status: 'available', ip: '8.8.8.8', checked_at: T0 - 1000, valid_until: T0 + 1000000}, ipv6: {status: 'unknown', checked_at: 0}}}});
  const view = PublicEgressCheck.view(agent, T0);
  assert.equal(view.state, 'unsupported');
  assert.equal(view.note, 'Agent 版本不支持，升级 Agent 后可用');
  assert.equal(view.buttonDisabled, true);
  assert.equal(view.switchDisabled, false);
  assert.match(view.lines.join('\n'), /IPv4：8\.8\.\*\.\*/);
  assert.doesNotMatch(view.lines.join('\n'), /8\.8\.8\.8/);
});

test('M10 panel: offline, paused and revoked Agents', () => {
  const offline = PublicEgressCheck.view(panelAgent({}, {online: false}), T0);
  assert.equal(offline.state, 'offline'); assert.equal(offline.note, 'Agent 离线'); assert.equal(offline.buttonDisabled, true);
  const paused = PublicEgressCheck.view(panelAgent({}, {disabled_at: T0 - 5}), T0);
  assert.equal(paused.state, 'paused'); assert.equal(paused.note, 'Agent 已暂停'); assert.equal(paused.buttonDisabled, true);
  const revoked = PublicEgressCheck.view(panelAgent({}, {revoked: true}), T0);
  assert.equal(revoked.state, 'revoked'); assert.equal(revoked.buttonDisabled, true); assert.equal(revoked.switchDisabled, true);
});

test('M10 panel: results, last check and next check are shown only while enabled', () => {
  const fresh = {status: 'available', ip: '8.8.8.8', checked_at: T0 - 60000, valid_until: T0 + 86400000, stale: false};
  const agent = panelAgent({next_check_at: T0 + 604800000}, {state: {public_egress: {ipv4: fresh, ipv6: {status: 'unavailable', checked_at: T0 - 60000, valid_until: T0 + 1}}}});
  const text = PublicEgressCheck.view(agent, T0).lines.join('\n');
  assert.match(text, /公网 IP 检测：已启用/);
  assert.match(text, /IPv4：8\.8\.\*\.\*/);
  assert.match(text, /IPv6：不可用/);
  assert.match(text, /上次检测：/);
  assert.match(text, /下次检测：/);
  const stale = panelAgent({}, {state: {public_egress: {ipv4: {...fresh, stale: true}, ipv6: {status: 'unknown', checked_at: 0}}}});
  assert.match(PublicEgressCheck.view(stale, T0).lines.join('\n'), /IPv4：旧数据 8\.8\.\*\.\*/);
  assert.match(PublicEgressCheck.view(stale, T0, {reveal: true}).lines.join('\n'), /IPv4：旧数据 8\.8\.8\.8/);
  assert.match(PublicEgressCheck.view(stale, T0).lines.join('\n'), /IPv6：尚未检测/);
  assert.doesNotMatch(PublicEgressCheck.view(panelAgent({enabled: false}), T0).lines.join('\n'), /IPv4/);
});

test('M10 panel: both pages build the one shared panel, which posts to the two endpoints with the CSRF token', () => {
  const read = file => fs.readFileSync(require.resolve('../web/static/' + file), 'utf8');
  const history = read('history.js'), html = read('history.html'), app = read('app.js'), shared = read('public-egress-check.js'), index = read('index.html');
  assert.ok(html.includes('id="public-egress-mount"') && html.includes('src="/public-egress-check.js"'));
  assert.ok(index.includes('src="/public-egress-check.js"') && index.indexOf('public-egress-check.js') < index.indexOf('/app.js'));
  assert.ok(history.includes('PublicEgressCheck.createPanel') && app.includes('PublicEgressCheck.createPanel'));
  assert.ok(app.includes('detailPanel.append(detailActions, egressPanel.node'), 'the panel sits in the device-details management tab');
  assert.ok(app.includes('conceal: () =>') && read('device-details.js').includes('conceal();leave()'));
  assert.ok(shared.includes("'/public-egress'") && shared.includes("'/public-egress/check'") && /'X-CSRF-Token':\s*csrf\(\)/.test(shared));
  assert.ok(shared.includes('JSON.stringify({enabled:wanted})'));
  // No copy of the state logic in the pages and nothing is persisted.
  for (const text of [history, app, shared]) assert.doesNotMatch(text.replace(/localStorage\.[a-z]+\([^)]*(sidebar|view|mode)[^)]*\)/g, ''), /egress[^\n]*(localStorage|sessionStorage|document\.cookie)/i);
  assert.doesNotMatch(shared, /localStorage|sessionStorage|document\.cookie|indexedDB/);
});
