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
  vm.createContext(sandbox);
  for (const file of ['management.js', 'selector.js', 'network-quality-core.js', 'network-quality.js', 'traffic-chart.js', 'card-metrics.js', 'resource-history.js', 'device-details.js', 'plan-editor.js', 'plan-renewal.js', 'overview.js', 'plan-units.js', 'youtube-status.js', 'latency-mini.js', 'app.js'])
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
  assert.match(el.children[0].attributes['aria-label'], /2001:db8::1/);
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
