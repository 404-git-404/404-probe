'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const AgentState = require('../web/static/agent-state.js');
const Selector = require('../web/static/selector.js');
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return {promise, resolve}; };
const response = body => ({ok: true, status: 200, json: async () => body});
function fixture(options = {}) {
  const state = AgentState.create();
  const out = current => ({configured: true, available: true, order_source: 'config', selectors: [
    {name: 'z-first', current, choices: ['old', 'new', 'third']},
    {name: 'a-second', current: 'old', choices: ['old', 'new']},
  ]});
  state.agents.set('a', {agent_id: 'a', online: true, name: 'original', state: {uptime: 1}, outbounds: out('old')});
  state.agents.set('b', {agent_id: 'b', online: true, outbounds: out('old')});
  const calls = [], complete = deferred();
  const fetcher = async (path, init) => {
    calls.push({path, init});
    if (init.method === 'POST') return response({job_id: 'job'});
    if (path.includes('/jobs/')) return complete.promise;
    return response({agent_id: 'a', name: 'STALE', state: {uptime: 0}, outbounds: out('new')});
  };
  const controller = Selector.create({state, fetcher, csrf: () => 'csrf', budgetMs: 1000, pollMs: 1, ...options});
  const finish = () => complete.resolve(response({operation_status: 'success', result: {success: true, measurement: {selector_switch: {current: 'new'}}}}));
  return {state, controller, calls, complete, finish};
}
test('production Selector: config order unchanged, one expanded, draft does not POST, agent isolation', () => {
  const f = fixture(), c = f.controller;
  c.open('a'); c.expand('a', 'z-first'); c.expand('a', 'a-second'); c.draft('a', 'z-first', 'new');
  assert.equal(c.model('a').expanded, 'a-second'); assert.equal(c.group('b', 'z-first').draft, null);
  assert.deepEqual(f.state.agents.get('a').outbounds.selectors.map(s => s.name), ['z-first', 'a-second']);
  assert.equal(f.calls.length, 0);
});
test('production Selector: same value and stale state never POST', async () => {
  const f = fixture(); assert.equal(await f.controller.submit('a', 'z-first', 'old'), false);
  f.state.agents.get('a').outbounds.stale = true;
  assert.equal(await f.controller.submit('a', 'z-first', 'new'), false); assert.equal(f.calls.length, 0);
});
test('production Selector: successful group collapses and resource merge preserves telemetry/name', async () => {
  const f = fixture(), c = f.controller;
  c.open('a'); c.expand('a', 'z-first'); c.draft('a', 'z-first', 'new');
  const task = c.submit('a', 'z-first', 'new');
  assert.equal(await c.submit('a', 'z-first', 'third'), false);
  f.finish(); assert.equal(await task, true);
  assert.equal(c.model('a').expanded, ''); assert.equal(c.model('a').open, true);
  assert.equal(c.group('a', 'z-first').draft, null);
  assert.equal(f.state.agents.get('a').name, 'original'); assert.equal(f.state.agents.get('a').state.uptime, 1);
  assert.equal(f.calls.filter(call => call.init.method === 'POST').length, 1);
});
test('production Selector: close wins over late result; refresh never opens', async () => {
  const f = fixture(), c = f.controller; c.open('a'); c.expand('a', 'z-first');
  const task = c.submit('a', 'z-first', 'new'); c.close('a'); c.sync('a'); f.finish(); await task;
  assert.equal(c.model('a').open, false); assert.equal(c.model('a').expanded, 'z-first');
});
test('production Selector: reopened/new draft cannot be cleared or collapsed by old success', async () => {
  const f = fixture(), c = f.controller; c.open('a'); c.expand('a', 'z-first'); c.draft('a', 'z-first', 'new');
  const task = c.submit('a', 'z-first', 'new'); c.close('a'); c.open('a'); c.draft('a', 'z-first', 'third');
  f.finish(); await task;
  assert.equal(c.model('a').expanded, 'z-first'); assert.equal(c.group('a', 'z-first').draft, 'third');
});
test('production Selector: terminal failure retains trusted current and draft', async () => {
  const f = fixture(), c = f.controller; c.draft('a', 'z-first', 'new');
  const task = c.submit('a', 'z-first', 'new');
  f.complete.resolve(response({operation_status: 'failed', result: {success: false, error_category: 'switch_verification_failed'}}));
  assert.equal(await task, false); assert.equal(c.group('a', 'z-first').draft, 'new');
  assert.equal(f.state.agents.get('a').outbounds.selectors[0].current, 'old');
  assert.match(c.group('a', 'z-first').feedback.message, /回读/);
});
test('production Selector: hung POST/body bound, uncertain identity retained, no write retry', async () => {
  for (const fetcher of [() => new Promise(() => {}), async () => ({ok: true, status: 200, json: () => new Promise(() => {})})]) {
    let writes = 0;
    const f = fixture({budgetMs: 15, fetcher: (...args) => { writes++; return fetcher(...args); }});
    assert.equal(await f.controller.submit('a', 'z-first', 'new'), false);
    assert.equal(writes, 1); assert.equal(f.controller.model('a').active, null);
    assert.equal(f.state.busy('a', 'outbounds'), false);
    const feedback = f.controller.group('a', 'z-first').feedback;
    assert.equal(feedback.uncertain, true); assert.equal(feedback.error, false); assert.match(feedback.requestID, /^[a-f0-9]{32}$/);
  }
});
test('production Selector: removed choice remains visible invalid draft; deletion/navigation cancel local wait', async () => {
  const f = fixture(), c = f.controller; c.draft('a', 'z-first', 'third');
  f.state.agents.get('a').outbounds.selectors[0].choices = ['old', 'new']; c.sync('a');
  assert.equal(c.group('a', 'z-first').draft, 'third'); assert.match(c.group('a', 'z-first').feedback.message, /已不在/);
  const task = c.submit('a', 'z-first', 'new'); f.state.beginDelete('a'); c.sync('a');
  assert.equal(await task, false); assert.equal(c.model('a').open, false); assert.equal(c.model('a').active, null);
  const g = fixture(); const navigationTask = g.controller.submit('a', 'z-first', 'new'); g.controller.closeAll();
  assert.equal(await navigationTask, false); assert.equal(g.controller.model('a').active, null);
});
test('production recovery: only two filtered pending pages plus necessary detail, never POST or auto-expand', async () => {
  const f = fixture(), c = f.controller, paths = []; c.open('a');
  const read = async path => {
    paths.push(path);
    if (path.includes('status=queued')) return {items: [{job_id: 'remote', operation_status: 'queued'}]};
    if (path.includes('status=leased')) return {items: []};
    return {job_id: 'remote', operation_status: 'queued', config: {selector: 'z-first', choice: 'new'}};
  };
  await c.recover('a', read);
  assert.equal(paths.length, 3); assert.ok(paths[0].includes('status=queued')); assert.ok(paths[1].includes('status=leased'));
  assert.equal(c.group('a', 'z-first').recovered.pending, true); assert.equal(c.model('a').expanded, '');
  assert.equal(await c.submit('a', 'z-first', 'new'), false); assert.equal(f.calls.length, 0);
  c.close('a'); c.open('a'); await c.recover('a', async () => ({items: []}));
  assert.equal(c.group('a', 'z-first').recovered, null); assert.equal(c.model('a').recoveryIncomplete, false);
});
test('production recovery: closing cancels hung read; new open generation and draft win over old result', async () => {
  const f = fixture(), c = f.controller, late = deferred(); let signal;
  c.open('a'); const old = c.recover('a', (path, s) => {signal = s; return late.promise;});
  c.close('a'); assert.equal(signal.aborted, true); await old;
  c.open('a'); c.draft('a', 'z-first', 'third');
  await c.recover('a', async () => ({items: []}));
  late.resolve({items: [{job_id: 'old', operation_status: 'queued'}]});
  assert.equal(c.model('a').open, true); assert.equal(c.model('a').inspection, null);
  assert.equal(c.group('a', 'z-first').draft, 'third'); assert.equal(c.group('a', 'z-first').recovered, null);
});
test('production recovery: pagination/failure truthfully blocks unverified writes and preserves local feedback', async () => {
  const f = fixture(), c = f.controller; c.open('a'); c.group('a', 'z-first').feedback = {message: 'trusted local'};
  await c.recover('a', async () => ({items: [], next_cursor: 'more'}));
  assert.equal(c.model('a').recoveryIncomplete, true); assert.equal(await c.submit('a', 'z-first', 'new'), false);
  await c.recover('a', async () => {throw Error('read failed');});
  assert.equal(c.group('a', 'z-first').feedback.message, 'trusted local'); assert.equal(f.calls.length, 0);
  assert.match(c.model('a').recoveryMessage, /尚未核实/);
});
test('production recovery: overall budget bounds ignored abort and deletion retires read lifetime', async () => {
  const f = fixture(), c = f.controller; c.open('a');
  await c.recover('a', () => new Promise(() => {}), 15);
  assert.equal(c.model('a').inspection, null); assert.equal(c.model('a').recoveryIncomplete, true);
  const late = deferred(); const task = c.recover('a', () => late.promise);
  f.state.beginDelete('a'); c.sync('a'); await task;
  late.resolve({items: [{job_id: 'old', operation_status: 'queued'}]});
  assert.equal(c.group('a', 'z-first').recovered, null); assert.equal(c.model('a').open, false);
});
test('production recovery: pending-detail late arrival cannot overwrite newly edited local group', async () => {
  const f = fixture(), c = f.controller, late = deferred(); c.open('a');
  const task = c.recover('a', async path => path.includes('status=queued')
    ? {items: [{job_id: 'remote', operation_status: 'queued'}]} : path.includes('status=leased') ? {items: []} : late.promise);
  await new Promise(resolve => setImmediate(resolve));
  c.draft('a', 'z-first', 'third'); c.group('a', 'z-first').feedback = {message: 'new local'};
  late.resolve({operation_status: 'queued', config: {selector: 'z-first', choice: 'new'}}); await task;
  assert.equal(c.group('a', 'z-first').recovered, null); assert.equal(c.group('a', 'z-first').feedback.message, 'new local');
  assert.equal(c.group('a', 'z-first').remotePending, true); assert.equal(await c.submit('a', 'z-first', 'third'), false);
});
test('production recovery: delayed pending observation cannot overwrite a newer completed local operation', async () => {
  const f = fixture(), c = f.controller, late = deferred(); c.open('a');
  const operation = c.submit('a', 'z-first', 'new');
  const recovery = c.recover('a', async path => path.includes('status=queued')
    ? {items: [{job_id: 'old', operation_status: 'queued'}]} : path.includes('status=leased') ? {items: []} : late.promise);
  await new Promise(resolve => setImmediate(resolve)); f.finish(); await operation;
  late.resolve({operation_status: 'queued', config: {selector: 'z-first', choice: 'third'}}); await recovery;
  assert.equal(c.group('a', 'z-first').remotePending, false);
  assert.match(c.group('a', 'z-first').feedback.message, /已切换到 new/);
});
test('production Selector: hung poll and hung detail body consume only the single overall budget', async () => {
  for (const stage of ['poll', 'detail']) {
    let writes = 0;
    const f = fixture({budgetMs: 15, fetcher: async (path, init) => {
      if (init.method === 'POST') {writes++; return response({job_id: 'job'});}
      if (path.includes('/jobs/')) return stage === 'poll' ? new Promise(() => {})
        : response({operation_status: 'success', result: {success: true, measurement: {selector_switch: {current: 'new'}}}});
      return {ok: true, status: 200, json: () => new Promise(() => {})};
    }});
    assert.equal(await f.controller.submit('a', 'z-first', 'new'), false);
    assert.equal(writes, 1); assert.equal(f.controller.model('a').active, null);
    assert.equal(f.controller.group('a', 'z-first').feedback.uncertain, true);
    assert.equal(f.controller.group('a', 'z-first').feedback.jobID, 'job');
    assert.equal(f.state.busy('a', 'outbounds'), false);
  }
});
test('production recovery: candidate cap twenty and at most two concurrent detail reads', async () => {
  const f = fixture(), c = f.controller; c.open('a'); let active = 0, maximum = 0, details = 0;
  await c.recover('a', async path => {
    if (path.includes('status=')) return {items: Array.from({length: 11}, (_, i) => ({job_id: path.includes('queued') ? `q${i}` : `l${i}`, operation_status: 'queued'}))};
    details++; active++; maximum = Math.max(maximum, active); await new Promise(resolve => setImmediate(resolve)); active--;
    return {operation_status: 'queued', config: {selector: 'z-first', choice: 'new'}};
  });
  assert.equal(details, 20); assert.equal(maximum, 2); assert.equal(c.model('a').recoveryIncomplete, true);
});
test('production Selector: all lifecycle/snapshot constraints block writes but preserve drafts', async () => {
  for (const patch of [{online: false}, {disabled_at: 1}, {revoked: true}, {outbounds: {configured: false}},
    {outbounds: {configured: true, available: false}}, {outbounds: {configured: true, available: true, stale: true}}]) {
    const f = fixture(); f.controller.draft('a', 'z-first', 'new'); Object.assign(f.state.agents.get('a'), patch);
    assert.equal(await f.controller.submit('a', 'z-first', 'new'), false); assert.equal(f.calls.length, 0);
    assert.equal(f.controller.group('a', 'z-first').draft, 'new');
  }
});
