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
