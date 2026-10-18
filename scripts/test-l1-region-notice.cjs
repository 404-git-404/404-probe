// "未选择地区" (a configuration state) versus "Agent 不支持" (a missing capability) on the mini curve and the square view.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const C = require('../web/static/network-quality-core.js');

const ready = {supported: true, state: 'ready', enabled: true, region_id: '["province","江苏",null]', targets: []};

test('L1: no region selected is "未选择地区", not "Agent 不支持", even when nothing else is configured', () => {
  for (const config of [
    {supported: true, state: 'region_selection_required', enabled: false, targets: []},
    {supported: true, state: 'region_selection_required', enabled: false, region_id: '', targets: [], card_lanes: []},
  ]) {
    const n = C.configNotice(config);
    assert.equal(n.key, 'region');
    assert.equal(n.text, '未选择地区');
    assert.doesNotMatch(n.text + n.detail, /不支持/);
  }
});

test('L1: "Agent 不支持" only appears when the capability is really missing', () => {
  for (const config of [
    {supported: false, state: 'not_scheduled_legacy', enabled: false, targets: []},
    {supported: false, state: 'ready', enabled: false, targets: []},
    {supported: undefined, targets: []},
  ]) {
    const n = C.configNotice(config);
    assert.equal(n.key, 'unsupported');
    assert.equal(n.text, 'Agent 不支持');
  }
});

test('L1: a configured, supported Agent and a missing config have their own outcomes', () => {
  assert.equal(C.configNotice(ready), null);
  assert.equal(C.configNotice(null).text, '等待地区配置');
  assert.equal(C.configNotice(undefined).key, 'waiting');
  // Region selection wins even if an older config copy still claims the Agent is unsupported.
  assert.equal(C.configNotice({supported: false, state: 'region_selection_required'}).key, 'region');
});

test('L1: both views take the message from the one shared decision', () => {
  const mini = fs.readFileSync(require.resolve('../web/static/latency-mini.js'), 'utf8');
  const squares = fs.readFileSync(require.resolve('../web/static/network-quality.js'), 'utf8');
  assert.match(mini, /C\.configNotice\(c\.config\)/);
  assert.match(mini, /notice\?\.key==='region'\?notice\.text:!c\.isps\.size\?'未选择 ISP'/);
  assert.doesNotMatch(mini, /!c\.config\.supported\?'Agent 不支持'/, 'the mini curve no longer maps "not supported" itself');
  assert.match(squares, /C\.configNotice\(card\.config\)/);
  assert.match(squares, /notice\?\.key==='region'\?notice:C\.cardCurrent/);
});
