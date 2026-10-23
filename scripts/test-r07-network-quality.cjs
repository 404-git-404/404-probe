// R07 stage D: user-entered network quality targets. The responses come from the Server's own tests
// (scripts/fixtures/r07), not from hand-written DTOs.
const {test} = require('node:test'), assert = require('node:assert/strict');
const fs = require('node:fs'), path = require('node:path');
const {v5Card, C, UI, load, clone, flush, setup, cardHistory, allowedTags, tags, AGENT} = require('./lib/r07-harness.cjs');

const screen = (w, h) => { globalThis.innerWidth = w; globalThis.innerHeight = h; };
const inc = v => String(BigInt(v) + 1n);
// The Server's answer to a save: the posted content, the next settings revision.
const saved = (body, ctx) => {
  const raw = clone(ctx.raw);
  raw.settings_revision = inc(raw.settings_revision); raw.interval_ms = body.interval_ms; raw.effective_interval_ms = body.interval_ms;
  raw.slots = body.slots.map(s => ({...s})); ctx.raw = raw;
  return ctx.reply(200, raw);
};
const toggled = (body, ctx) => {
  const raw = clone(ctx.raw); raw.enabled = body.enabled; raw.settings_revision = inc(raw.settings_revision); ctx.raw = raw;
  return ctx.reply(200, raw);
};

test('every committed Server response satisfies the page contract, and a damaged one is refused', () => {
  const names = fs.readdirSync(path.join(__dirname, 'fixtures', 'r07')).filter(n => n.startsWith('v4-'));
  assert.ok(names.length >= 6);
  for (const name of names) {
    const config = C.adapt(load(name.replace('.json', '')));
    assert.equal(config.slots.length, 3);
    assert.equal(config.lanes.size, 12);
  }
  const good = load('v4-running');
  for (const [what, mutate] of Object.entries({
    'revision as number': v => { v.settings_revision = 3; },
    'two slots': v => { v.slots.pop(); },
    'eleven lanes': v => { v.lanes.pop(); },
    'unknown lane status': v => { v.lanes[0].status = 'weird'; },
    'port as string': v => { v.slots[0].port = '443'; },
    'slot order': v => { v.slots.reverse(); },
  })) { const bad = clone(good); mutate(bad); assert.throws(() => C.adapt(bad), /契约/, what); }
});

test('the model: 31 provinces, three ISPs, ten frequencies from 30 to 300 seconds, four sub-items', () => {
  assert.equal(C.provinces.length, 31);
  assert.equal(new Set(C.provinces).size, 31);
  assert.deepEqual(C.isps, [['telecom', '电信'], ['unicom', '联通'], ['mobile', '移动']]);
  assert.deepEqual(C.intervals, [30000, 60000, 90000, 120000, 150000, 180000, 210000, 240000, 270000, 300000]);
  assert.deepEqual(C.subItems.map(s => s[1]), ['IPv4 TCP', 'IPv4 ICMP', 'IPv6 TCP', 'IPv6 ICMP']);
  // The same 31 names the Server accepts.
  const go = fs.readFileSync(path.join(__dirname, '..', 'internal', 'protocol', 'network_quality_user.go'), 'utf8');
  const listed = [...go.slice(go.indexOf('var QualityProvinces')).split('}')[0].matchAll(/"([^"]+)"/g)].map(m => m[1]);
  assert.deepEqual(C.provinces, listed);
});

test('save payload carries exactly the Server fields; ports and hosts are normalised for sending only', () => {
  const config = C.adapt(load('v4-saved-off')), draft = C.draftOf(config);
  draft.slots[2].host = '  Example.NET '; draft.slots[2].port = ' 8443 '; draft.slots[2].kind = 'custom'; draft.slots[2].region = 'a'; draft.slots[2].isp = 'b';
  const out = C.savePayload(draft, config.settings_revision);
  assert.deepEqual(Object.keys(out), ['expected_revision', 'interval_ms', 'slow_threshold_ms', 'slots']);
  assert.equal(out.expected_revision, '1'); assert.equal(out.slow_threshold_ms, 500);
  for (const bad of ['', '49', '3001', '12.5', 'abc', '1e3']) { draft.slow_threshold_ms = bad; assert.equal(C.thresholdHint(draft), 'invalid_threshold', bad); assert.throws(() => C.savePayload(draft, '1'), /门槛/); }
  draft.slow_threshold_ms = ' 800 '; assert.equal(C.savePayload(draft, '1').slow_threshold_ms, 800); draft.slow_threshold_ms = '500';
  assert.deepEqual(Object.keys(out.slots[0]), ['slot', 'kind', 'region', 'isp', 'host', 'port', 'slot_enabled', 'ipv4_tcp', 'ipv4_icmp', 'ipv6_tcp', 'ipv6_icmp']);
  assert.equal(out.slots[2].host, 'Example.NET'); assert.equal(out.slots[2].port, 8443);
  assert.equal(out.slots[1].port, 80); assert.equal(typeof out.slots[1].port, 'number');
  assert.throws(() => C.savePayload(draft, 1), /版本/);
  draft.interval_ms = 45000;
  assert.throws(() => C.savePayload(draft, '1'), /30–300/);
  assert.deepEqual(C.enabledPayload('7', true), {expected_revision: '7', enabled: true});
  assert.throws(() => C.enabledPayload(7, true));
});

test('the dialog: layout, fields, defaults, and the one-line notice', async () => {
  const f = setup({raw: load('v4-empty-unreported')});
  const panel = await f.open();
  assert.ok(panel, 'the settings button opens the dialog');
  const head = panel.querySelector('.nq-dialog-actions');
  assert.deepEqual(head.children.map(b => b.textContent), ['保存', '关闭'], '保存 sits left of 关闭');
  assert.ok(f.field('save-bottom'), 'a second 保存 at the bottom');
  assert.equal(f.field('interval').children.length, 10);
  assert.deepEqual(f.field('interval').children.map(o => o.textContent), ['30 秒', '60 秒', '90 秒', '120 秒', '150 秒', '180 秒', '210 秒', '240 秒', '270 秒', '300 秒']);
  for (const slot of C.slots) {
    for (const key of ['enabled', 'kind-builtin', 'kind-custom', 'region-select', 'region-text', 'isp-select', 'isp-text', 'host', 'port', 'ipv4_tcp', 'ipv4_icmp', 'ipv6_tcp', 'ipv6_icmp'])
      assert.ok(f.field(`${slot}-${key}`), `${slot}-${key}`);
    assert.equal(f.field(`${slot}-enabled`).checked, false);
    for (const key of ['ipv4_tcp', 'ipv4_icmp', 'ipv6_tcp', 'ipv6_icmp']) assert.equal(f.field(`${slot}-${key}`).checked, false, 'nothing ticked by default');
    assert.equal(f.field(`${slot}-kind-builtin`).checked, true);
    assert.equal(f.field(`${slot}-region-select`).children.length, 32, '31 provinces and a placeholder');
    assert.equal(f.field(`${slot}-isp-select`).children.length, 4, 'three ISPs and a placeholder');
    assert.equal(f.field(`${slot}-region-select`).hidden, false); assert.equal(f.field(`${slot}-region-text`).hidden, true);
  }
  const text = panel.all().map(n => n.ownText).join('\n');
  assert.equal((text.match(/请仅检测你有权检测的目标/g) || []).length, 1);
  assert.match(text, /地区和 ISP 是你的标注，404-probe 不核对/);
  assert.match(text, /保存只保存配置，不会开启检测/);
  assert.doesNotMatch(text, /V6 检测|搜索|三网共用/);
  assert.match(text, /启用检测（立即生效，按最后一次保存的配置执行）/);
  // An Agent that has not reported yet can still have its targets saved; they start when it negotiates.
  assert.equal(f.field('save-top').disabled, false);
  assert.equal(f.field('enabled').disabled, true, 'but detection cannot be switched on for an Agent that does not support it');
});

test('save only saves: no enable request, the switch stays as the Server has it', async () => {
  const f = setup({raw: load('v4-saved-off'), save: saved});
  const panel = await f.open();
  assert.equal(f.field('enabled').checked, false);
  f.type('mobile-host', 'example.net'); f.type('mobile-port', '443'); f.choose('mobile-region-select', '广东'); f.choose('mobile-isp-select', 'mobile');
  f.check('mobile-enabled'); f.check('mobile-ipv4_tcp');
  f.field('save-top').click(); await flush();
  assert.deepEqual(f.writes.map(w => w.kind), ['save']);
  const body = f.writes[0].body;
  assert.equal(body.expected_revision, '1');
  assert.equal(body.slots[2].host, 'example.net'); assert.equal(body.slots[2].port, 443); assert.equal(body.slots[2].ipv4_tcp, true);
  assert.equal(f.field('enabled').checked, false);
  assert.match(f.field('save-top').parent.parent.querySelector('.nq-dialog-foot')?.textContent || panel.querySelector('.nq-dialog-foot').textContent, /已保存（未开启检测）/);
  assert.equal(f.ui.snapshot().panel.dirty, false);
  // The second save uses the new revision.
  f.type('mobile-port', '444'); f.field('save-bottom').click(); await flush();
  assert.equal(f.writes[1].body.expected_revision, '2');
});

test('unsaved edits: switching on asks first; the three answers do what they say', async () => {
  for (const answer of ['保存并开启', '不保存，按上次保存的配置开启', '取消']) {
    const f = setup({raw: load('v4-saved-off'), save: saved, enable: toggled});
    const panel = await f.open();
    f.type('mobile-host', 'example.net'); f.type('mobile-port', '443'); f.choose('mobile-region-select', '广东'); f.choose('mobile-isp-select', 'mobile');
    f.check('enabled', true);
    assert.equal(f.writes.length, 0, 'nothing is sent before the answer');
    const guard = panel.all().find(n => n.className === 'nq-status nq-guard' && !n.hidden);
    assert.ok(guard, 'the question is shown');
    assert.deepEqual(guard.querySelectorAll('button').map(b => b.textContent), ['保存并开启', '不保存，按上次保存的配置开启', '取消']);
    assert.match(guard.textContent, /有未保存的修改，先保存吗？/);
    assert.equal(f.field('enabled').checked, false, 'still off until it really is on');
    f.byText(guard, answer).click(); await flush();
    if (answer === '保存并开启') {
      assert.deepEqual(f.writes.map(w => w.kind), ['save', 'enable']);
      assert.equal(f.writes[1].body.expected_revision, '2', 'enable follows the save with its new revision');
      assert.equal(f.field('enabled').checked, true);
    } else if (answer === '不保存，按上次保存的配置开启') {
      assert.deepEqual(f.writes.map(w => w.kind), ['enable']);
      assert.equal(f.writes[0].body.expected_revision, '1');
      assert.equal(f.ui.snapshot().panel.dirty, true, 'the draft is kept');
      assert.equal(f.field('mobile-host').value, 'example.net');
    } else {
      assert.equal(f.writes.length, 0);
      assert.equal(f.ui.snapshot().panel.dirty, true);
    }
  }
});

test('switching on after a failed save does not switch on', async () => {
  const f = setup({raw: load('v4-saved-off'), save: (_b, ctx) => ctx.reply(400, {error: {code: 'invalid_request', message: 'bad', details: [{slot: 'mobile', field: 'host', code: 'unsafe_address'}]}}), enable: toggled});
  const panel = await f.open();
  f.type('mobile-host', '10.0.0.1'); f.type('mobile-port', '443'); f.choose('mobile-region-select', '广东'); f.choose('mobile-isp-select', 'mobile');
  f.check('enabled', true);
  f.byText(panel, '保存并开启').click(); await flush();
  assert.deepEqual(f.writes.map(w => w.kind), ['save'], 'no enable request after a refused save');
  assert.equal(f.field('enabled').checked, false);
  assert.match(panel.querySelector('.nq-slot').parent.all().map(n => n.textContent).join('|'), /地址不是公网地址/, 'the refusal is shown at the input');
});

test('switching off needs no question and keeps the draft; a clean switch-on goes straight through', async () => {
  const f = setup({raw: load('v4-running'), save: saved, enable: toggled});
  const panel = await f.open();
  assert.equal(f.field('enabled').checked, true);
  f.type('unicom-port', '81');
  f.check('enabled', false); await flush();
  assert.deepEqual(f.writes.map(w => w.kind), ['enable']);
  assert.deepEqual(f.writes[0].body, {expected_revision: '2', enabled: false});
  assert.equal(panel.all().filter(n => n.className === 'nq-status nq-guard' && !n.hidden).length, 0);
  assert.equal(f.field('unicom-port').value, '81', 'the draft stays');
  assert.equal(f.ui.snapshot().panel.dirty, true);
  // Clean draft: switching on is a plain request.
  f.type('unicom-port', '80'); assert.equal(f.ui.snapshot().panel.dirty, false);
  f.check('enabled', true); await flush();
  assert.deepEqual(f.writes.map(w => w.kind), ['enable', 'enable']);
  assert.equal(f.writes[1].body.expected_revision, '3');
});

test('closing: clean closes, dirty asks, the question has two answers', async () => {
  const f = setup({raw: load('v4-saved-off')});
  let panel = await f.open();
  f.field('close').click();
  assert.equal(f.panel(), undefined, 'a clean dialog closes at once');
  panel = await f.open();
  f.type('mobile-host', 'example.net');
  for (const how of ['button', 'escape', 'outside']) {
    if (how === 'button') f.field('close').click();
    if (how === 'escape') f.doc.dispatch('keydown', {key: 'Escape', preventDefault() {}});
    if (how === 'outside') panel.dispatch('pointerdown', {target: panel});
    assert.ok(f.panel(), `${how}: still open`);
    const guard = panel.all().find(n => n.className === 'nq-status nq-guard' && !n.hidden);
    assert.match(guard.textContent, /放弃修改？/);
    assert.deepEqual(guard.querySelectorAll('button').map(b => b.textContent), ['放弃修改', '继续编辑']);
    f.byText(guard, '继续编辑').click();
    assert.equal(panel.all().filter(n => n.className === 'nq-status nq-guard' && !n.hidden).length, 0);
  }
  f.field('close').click();
  f.byText(panel.all().find(n => n.className === 'nq-status nq-guard' && !n.hidden), '放弃修改').click();
  assert.equal(f.panel(), undefined);
  assert.equal(f.writes.length, 0);
});

test('a literal address of one family locks the sub-items of the other; kind switch keeps address, port and ticks but clears labels', async () => {
  const f = setup({raw: load('v4-running')});
  await f.open();
  f.type('mobile-host', '93.184.216.34'); f.type('mobile-port', '443');
  for (const key of ['ipv6_tcp', 'ipv6_icmp']) assert.equal(f.field(`mobile-${key}`).disabled, true, key);
  for (const key of ['ipv4_tcp', 'ipv4_icmp']) assert.equal(f.field(`mobile-${key}`).disabled, false, key);
  f.type('mobile-host', '2606:4700::1111');
  for (const key of ['ipv4_tcp', 'ipv4_icmp']) assert.equal(f.field(`mobile-${key}`).disabled, true, key);
  f.type('mobile-host', 'example.net');
  for (const key of ['ipv4_tcp', 'ipv4_icmp', 'ipv6_tcp', 'ipv6_icmp']) assert.equal(f.field(`mobile-${key}`).disabled, false, key);
  f.check('mobile-ipv4_tcp'); f.choose('mobile-region-select', '广东'); f.choose('mobile-isp-select', 'telecom');
  f.check('mobile-kind-custom'); // radio change
  const draft = f.ui.snapshot().panel.draft.slots[2];
  assert.equal(draft.kind, 'custom'); assert.equal(draft.host, 'example.net'); assert.equal(draft.port, '443'); assert.equal(draft.ipv4_tcp, true);
  assert.equal(draft.region, ''); assert.equal(draft.isp, '', 'free text and enumeration never carry into each other');
  assert.equal(f.field('mobile-region-text').hidden, false); assert.equal(f.field('mobile-region-select').hidden, true);
  // Already-typed immediate hints: a host needs its labels and a port.
  const text = f.panel().all().map(n => n.textContent).join('|');
  assert.match(text, /已填地址时必须填写/);
});

const withIcmp = (name, v4, v6) => { const r = clone(load(name)); if (v4 === undefined) delete r.icmp_support; else r.icmp_support = {ipv4: v4, ipv6: v6}; return r; };
// The same response with every ICMP tick taken away: what a user sees on a device that never had them.
const noIcmpTicks = raw => { const r = clone(raw); for (const s of r.slots) Object.assign(s, {ipv4_icmp: false, ipv6_icmp: false}); return r; };
const icmpBoxes = (f, slot = 'telecom') => ({ipv4: f.field(`${slot}-ipv4_icmp`).disabled, ipv6: f.field(`${slot}-ipv6_icmp`).disabled, tcp4: f.field(`${slot}-ipv4_tcp`).disabled, tcp6: f.field(`${slot}-ipv6_tcp`).disabled});

test('ICMP: the Server\'s capability answer disables the ICMP boxes of that family and says why, whatever the lanes say', async () => {
  assert.deepEqual(load('v4-no-ipv6-no-icmp').icmp_support, {ipv4: 'unsupported', ipv6: 'unsupported'});
  const f = setup({raw: noIcmpTicks(load('v4-no-ipv6-no-icmp'))});
  await f.open();
  // (unicom's address is an IPv4 literal, which locks its IPv6 boxes on its own account; telecom is a name.)
  assert.deepEqual(icmpBoxes(f, 'telecom'), {ipv4: true, ipv6: true, tcp4: false, tcp6: false});
  assert.equal(f.field('unicom-ipv4_icmp').disabled, true); assert.equal(f.field('mobile-ipv6_icmp').disabled, true);
  assert.match(f.panel().all().map(n => n.textContent).join('|'), /当前环境不支持 ICMP/);
  // A saved tick is kept, shown, and can be taken back - it only cannot be put there.
  const g = setup({raw: load('v4-no-ipv6-no-icmp')});
  await g.open();
  for (const k of ['ipv4_icmp', 'ipv6_icmp']) { assert.equal(g.field(`telecom-${k}`).checked, true, k); assert.equal(g.field(`telecom-${k}`).disabled, false, k + ' can be unticked'); }
  g.check('telecom-ipv4_icmp', false); assert.equal(g.field('telecom-ipv4_icmp').disabled, true);
});

test('ICMP matrix (review M5): the same unsupported answer disables the boxes with the switch off, nothing ticked, nothing configured, and only for the family it names', async () => {
  const cases = {
    'switch off, configured': noIcmpTicks(withIcmp('v4-saved-off', 'unsupported', 'unsupported')),
    'switch on, nothing ticked': noIcmpTicks(withIcmp('v4-running', 'unsupported', 'unsupported')),
    'nothing configured': withIcmp('v4-empty-unreported', 'unsupported', 'unsupported'),
    'IPv6 only': noIcmpTicks(withIcmp('v4-saved-off', 'supported', 'unsupported')),
    'IPv4 only': noIcmpTicks(withIcmp('v4-saved-off', 'unsupported', 'supported')),
  };
  const expect = {'switch off, configured': [true, true], 'switch on, nothing ticked': [true, true], 'nothing configured': [true, true], 'IPv6 only': [false, true], 'IPv4 only': [true, false]};
  for (const [what, raw] of Object.entries(cases)) {
    const f = setup({raw}); await f.open();
    const got = icmpBoxes(f); assert.deepEqual([got.ipv4, got.ipv6], expect[what], what);
    assert.equal(got.tcp4 || got.tcp6, false, 'TCP is never affected');
  }
});

test('ICMP matrix: "unknown", a missing field or a value we do not know never disables anything', async () => {
  for (const [what, raw] of Object.entries({
    'negotiating': load('v4-negotiating'), 'first upgrade step': load('v4-first-upgrade'), 'old agent': load('v4-old-agent'), 'never reported': load('v4-empty-unreported'),
    'field missing': withIcmp('v4-saved-off'), 'garbage': (() => { const r = clone(load('v4-saved-off')); r.icmp_support = {ipv4: 'no', ipv6: 7}; return r; })(), 'null': (() => { const r = clone(load('v4-saved-off')); r.icmp_support = null; return r; })(),
  })) {
    const f = setup({raw}); await f.open();
    assert.deepEqual(icmpBoxes(f), {ipv4: false, ipv6: false, tcp4: false, tcp6: false}, what);
    assert.doesNotMatch(f.panel().all().map(n => n.textContent).join('|'), /当前环境不支持 ICMP/, what);
  }
  assert.deepEqual(C.adapt(load('v4-negotiating')).icmpSupport, {ipv4: 'unknown', ipv6: 'unknown'});
  assert.deepEqual(C.adapt(withIcmp('v4-saved-off')).icmpSupport, {ipv4: 'unknown', ipv6: 'unknown'});
});

test('ICMP: capability lost and recovered while the dialog is open follows the Server and keeps the ticks', async () => {
  let offset = 0; screen(1000, 1000);
  const f = setup({raw: noIcmpTicks(withIcmp('v4-saved-off', 'supported', 'supported')), clock: () => offset});
  await f.open();
  assert.deepEqual(icmpBoxes(f), {ipv4: false, ipv6: false, tcp4: false, tcp6: false});
  f.setRaw(noIcmpTicks(withIcmp('v4-saved-off', 'unsupported', 'unsupported'))); offset += 31000; f.ui.tick(); await flush();
  assert.deepEqual(icmpBoxes(f), {ipv4: true, ipv6: true, tcp4: false, tcp6: false});
  f.setRaw(noIcmpTicks(withIcmp('v4-saved-off', 'supported', 'supported'))); offset += 31000; f.ui.tick(); await flush();
  assert.deepEqual(icmpBoxes(f), {ipv4: false, ipv6: false, tcp4: false, tcp6: false});
  assert.equal(f.ui.snapshot().panel.dirty, false, 'following the capability is not an edit');
});

test('server field errors appear next to their inputs; the rest of the dialog is untouched', async () => {
  const f = setup({raw: load('v4-saved-off'), save: (_b, ctx) => ctx.reply(400, {error: {code: 'invalid_request', message: 'x', details: [
    {slot: 'telecom', field: 'host', code: 'reserved_name'}, {slot: 'unicom', field: 'port', code: 'invalid_port'}, {slot: 'unicom', field: 'region', code: 'label_required'}]}})});
  const panel = await f.open();
  f.type('telecom-port', '444');
  f.field('save-bottom').click(); await flush();
  const slots = panel.querySelectorAll('.nq-slot'), at = i => slots[i].all().filter(n => n.className === 'nq-field-error').map(n => n.textContent).filter(Boolean);
  assert.deepEqual(at(0), ['这是保留名称，不能检测']);
  assert.deepEqual(at(1).sort(), ['已填地址时必须填写', '端口须为 1–65535'].sort());
  assert.match(panel.querySelector('.nq-dialog-foot').textContent, /请检查标出的输入/);
  assert.equal(f.ui.snapshot().panel.dirty, true);
  // Editing clears them.
  f.type('telecom-port', '445');
  assert.deepEqual(at(0), []);
});

test('an answer to the master switch that carries other content replaces the base and says so; the same content only moves the revision', async () => {
  const changed = clone(load('v4-saved-off')); changed.settings_revision = '9'; changed.enabled = true; changed.slots[0].port = 8443;
  const f = setup({raw: load('v4-saved-off'), enable: (_b, ctx) => (ctx.raw = changed, ctx.reply(200, changed))});
  const panel = await f.open();
  f.check('enabled', true); await flush();
  assert.match(panel.querySelector('.nq-dialog-foot').textContent, /配置已被修改，已载入最新内容/);
  assert.equal(f.field('telecom-port').value, '8443');
  assert.equal(f.ui.snapshot().panel.base, '9');
  // Same content: only the revision moves and an edited draft stays as it is.
  const g = setup({raw: load('v4-saved-off'), enable: toggled});
  await g.open();
  g.type('mobile-host', 'draft.example.org');
  g.check('enabled', false); // currently off: nothing to do
  g.ui.snapshot();
  const h = setup({raw: load('v4-running'), enable: toggled});
  await h.open(); h.type('mobile-host', 'draft.example.org');
  h.check('enabled', false); await flush();
  assert.equal(h.ui.snapshot().panel.base, '3');
  assert.equal(h.field('mobile-host').value, 'draft.example.org');
  assert.doesNotMatch(h.panel().querySelector('.nq-dialog-foot').textContent, /已被修改/);
});

test('an old page gets 410: the card and the dialog say the page is out of date and nothing can be saved', async () => {
  const f = setup({readStatus: 410});
  f.ui.setVisible(AGENT, true); await flush();
  assert.equal(f.node.querySelector('.nq7-note').textContent, '页面已过期，请刷新');
  const g = setup({raw: load('v4-saved-off'), save: (_b, ctx) => ctx.reply(410, {error: {code: 'client_outdated', message: '页面版本已过期，请刷新页面后重试'}})});
  const panel = await g.open();
  g.type('telecom-port', '444'); g.field('save-top').click(); await flush();
  assert.match(panel.querySelector('.nq-dialog-foot').textContent, /页面已过期，请刷新/);
  assert.equal(g.field('save-top').disabled, true); assert.equal(g.field('save-bottom').disabled, true);
});

test('cards: every state the Server can report is shown as text, never inferred', async () => {
  const lanes = async (name, family = 'ipv4') => {
    const f = setup({raw: load(name)}); f.ui.setVisible(AGENT, true); await flush();
    if (family === 'ipv6') { f.node.querySelectorAll('button').find(b => b.textContent === 'IPv6').click(); await flush(); }
    const rows = f.node.querySelectorAll('.nq7-row');
    return {f, rows, text: rows.map(r => [r.querySelector('.nq7-label').textContent, r.dataset.lane, r.querySelector('.nq7-text').textContent, r.querySelector('.nq7-right').textContent]), status: f.node.querySelector('.nq7-note').textContent};
  };
  const running = await lanes('v4-running');
  assert.equal(running.text[0][0], '广东电信'); assert.equal(running.text[1][0], 'Tokyo NTT'); assert.equal(running.text[2][0], '移动');
  assert.equal(running.text[0][1], 'blocks'); assert.match(running.text[0][3], /^丢包 0%$/);
  assert.deepEqual(running.text[2].slice(1, 3), ['unset', '未配置目标'], 'a slot without an address');
  const v6 = await lanes('v4-running', 'ipv6');
  assert.deepEqual(v6.text[1].slice(1, 3), ['inactive', '未启用'], 'ticked for IPv4 only');
  const off = await lanes('v4-saved-off');
  assert.deepEqual(off.text[0].slice(1, 3), ['off', '已关闭']);
  const old = await lanes('v4-old-agent');
  assert.deepEqual(old.text[0].slice(1, 3), ['upgrade', '需升级 Agent']); assert.match(old.status, /需升级 Agent/);
  const noV6 = await lanes('v4-no-ipv6-no-icmp', 'ipv6');
  assert.equal(noV6.text[0][2], '本机无 IPv6');
  const noIcmp = await lanes('v4-no-ipv6-no-icmp');
  assert.equal(noIcmp.text[0][1], 'blocks', 'TCP keeps working without ICMP'); assert.equal(noIcmp.text[0][3], '失败 0%', 'no ICMP: the number is a TCP failure rate, never called loss');
  const abnormal = await lanes('v4-target-abnormal');
  assert.equal(abnormal.text[0][2], '目标异常');
  const empty = await lanes('v4-empty-unreported');
  assert.equal(empty.text[0][2], 'Agent 不支持');
});

test('cards: the v5 answer is read for the shown family; blocks come from it, a changed label does not read again', async () => {
  const f = setup({raw: load('v4-running')});
  f.ui.setVisible(AGENT, true); await flush();
  const reads = () => f.requests.filter(r => r.url.includes('/v5/card')).map(r => new URL(r.url, 'http://x').searchParams.get('family'));
  assert.deepEqual(reads(), ['ipv4'], 'one read for the whole card, not one per lane');
  assert.ok(!f.requests.some(r => r.url.includes('card-history')), 'the retired per-lane read is gone');
  const cells = f.node.querySelectorAll('.nq7-row')[0].querySelector('.nq7-blocks').querySelectorAll('.nq7-b');
  assert.equal(cells.length, 20); assert.ok(cells.every(c => /nq7-b-green/.test(c.className)));
  assert.match(cells[0].tipText, /正常/); assert.match(cells[19].tipText, /数据仍在到达/);
  assert.match(f.node.querySelector('.nq7-sub').textContent, /门槛 500 ms/);
  const relabelled = clone(f.raw()); relabelled.settings_revision = inc(relabelled.settings_revision);
  relabelled.slots[0].region = '浙江'; relabelled.slots[0].isp = 'unicom';
  f.setRaw(relabelled);
  await f.open(); f.field('close').click(); await flush();
  assert.equal(f.node.querySelectorAll('.nq7-row')[0].querySelector('.nq7-label').textContent, '浙江联通', 'row label follows the slot labels');
  assert.deepEqual(f.node.querySelectorAll('.nq7-row')[0].querySelector('.nq7-blocks').querySelectorAll('.nq7-b'), cells, 'the same twenty cells, not rebuilt');
  // A new settings revision (the threshold may have changed) reads the judgement again.
  assert.equal(reads().length, 2);
  f.node.querySelectorAll('button').find(b => b.textContent === 'IPv6').click(); await flush();
  assert.deepEqual(reads().slice(-1), ['ipv6']);
});

test('cards: a failed v5 read keeps the last answer and says it is not current; 410 asks for a refresh', async () => {
  screen(1000, 1000);
  let offset = 0, failing = 0;
  const f = setup({raw: load('v4-running'), clock: () => offset, cardFail: () => failing});
  f.ui.setVisible(AGENT, true); await flush();
  assert.equal(f.node.querySelector('.nq7-row').dataset.lane, 'blocks');
  failing = 503; offset += 31000; f.ui.tick(); await flush();
  assert.match(f.node.querySelector('.nq7-note').textContent, /读取失败，显示上次结果（非当前）/);
  assert.equal(f.node.querySelector('.nq7-row').dataset.lane, 'blocks', 'the last answer stays');
  assert.equal(f.node.querySelector('.nq7-row').dataset.stale, 'true');
});

test('history opens for the current targets with the slot label, and the device-details list uses the same labels', async () => {
  const f = setup({raw: load('v4-running')});
  f.ui.setVisible(AGENT, true); await flush();
  const targets = f.ui.historyTargets(AGENT);
  assert.ok(targets.length >= 6);
  assert.ok(targets.every(t => t.label.startsWith(C.slotLabel(C.adapt(f.raw()), t.slot))));
  assert.ok(targets.some(t => t.label === '电信 · 广东 · TCP · V4'));
  assert.ok(targets.some(t => /当前未检测/.test(t.label)), 'a target that does not run is marked');
  const t = targets.find(x => x.protocol === 'tcp' && x.family === 'ipv4' && x.slot === 'telecom');
  assert.equal(f.ui.openHistoryFor(AGENT, t.id, 'ipv4', 'latency', null), true);
  await flush();
  assert.match(f.dialog.all().map(n => n.textContent).join('|'), /网络质量 · 电信 · 广东 · TCP · V4/);
});

// ---- XSS ----
const hostile = ['<script>alert(1)</script>', '"\'><img src=x onerror=alert(1)>', '&lt;b&gt;bold&lt;/b&gt;', '</title><svg onload=1>', '`${1+1}`', 'a&amp;b'];

test('hostile labels and addresses are text everywhere: card, tooltips, dialog fields, history header, device-details list', async () => {
  for (const evil of hostile) {
    const raw = clone(load('v4-running'));
    raw.slots[0] = {...raw.slots[0], kind: 'custom', region: evil, isp: evil, host: evil};
    raw.slots[1] = {...raw.slots[1], kind: 'custom', region: 'x' + evil, isp: evil + 'y'};
    const f = setup({raw});
    f.ui.setVisible(AGENT, true); await flush();
    const expected = `${evil} · ${evil}`, shown = `${evil} ${evil}`;
    assert.equal(f.node.querySelectorAll('.nq7-row')[0].querySelector('.nq7-label').textContent, shown);
    assert.equal(f.node.querySelectorAll('.nq7-row')[0].querySelector('.nq7-label').title, shown);
    for (const n of f.node.all()) assert.ok(allowedTags.has(n.tagName), `${n.tagName} appeared`);
    const panel = await f.open();
    assert.equal(f.field('telecom-region-text').value, evil); assert.equal(f.field('telecom-host').value, evil);
    for (const n of panel.all()) assert.ok(allowedTags.has(n.tagName), `${n.tagName} appeared in the dialog`);
    f.field('close').click();
    const list = f.ui.historyTargets(AGENT).filter(t => t.slot === 'telecom');
    assert.ok(list.length && list.every(t => t.label.startsWith(expected)));
    const t = list.find(x => x.protocol === 'tcp' && x.family === 'ipv4');
    f.ui.openHistoryFor(AGENT, t.id, 'ipv4', 'latency', null); await flush();
    assert.ok(f.dialog.all().some(n => n.textContent === `网络质量 · ${expected} · TCP · V4`));
    for (const n of f.dialog.all()) assert.ok(allowedTags.has(n.tagName), `${n.tagName} appeared in the history`);
  }
});

test('no code path builds markup from text', () => {
  for (const file of ['network-quality.js', 'network-quality-core.js', 'latency-mini.js', 'device-details.js']) {
    const text = fs.readFileSync(path.join(__dirname, '..', 'web', 'static', file), 'utf8');
    assert.doesNotMatch(text, /innerHTML|outerHTML|insertAdjacentHTML|document\.write|createContextualFragment|DOMParser|new Function|eval\(/, file);
  }
});

test('the old region flow is gone from the page code', () => {
  for (const file of ['network-quality.js', 'network-quality-core.js', 'latency-mini.js']) {
    const text = fs.readFileSync(path.join(__dirname, '..', 'web', 'static', file), 'utf8');
    assert.doesNotMatch(text, /catalog|card-config|v2\/config|region_id|chooseRegion|laneDraft|lanePayload|V6 检测|三网共用地区/, file);
  }
});

// ---- TCP Mini (N6) through the latency chart ----
const L = require('../web/static/latency-mini.js');
class El {
  constructor(tag, ns) { this.tagName = tag; this.namespace = ns; this.children = []; this.attributes = {}; this.dataset = {}; this.style = {setProperty(k, v) { this[k] = v; }}; this.listeners = {}; this.className = ''; this.ownText = ''; this.isConnected = true; this.hidden = false; }
  set textContent(v) { this.ownText = String(v); this.children = []; }
  get textContent() { return this.ownText + this.children.map(c => c.textContent).join(''); }
  append(...c) { this.children.push(...c); }
  replaceChildren(...c) { this.ownText = ''; this.children = c; }
  setAttribute(k, v) { this.attributes[k] = String(v); }
  getAttribute(k) { return this.attributes[k]; }
  addEventListener(t, f) { (this.listeners[t] || (this.listeners[t] = [])).push(f); }
  removeEventListener() {}
  dispatch(t, e = {}) { for (const f of this.listeners[t] || []) f({preventDefault() {}, stopPropagation() {}, ...e}); }
  getBoundingClientRect() { return this.rect || {width: 0, height: 0, left: 0, top: 0, right: 0, bottom: 0}; }
  remove() { this.isConnected = false; }
  getContext() { return {measureText: t => ({width: t.length * 5.5})}; }
  hasPointerCapture() { return false; } setPointerCapture() {} releasePointerCapture() {}
  all() { return this.children.flatMap(c => [c, ...c.all()]); }
}
const fakeDoc = {hidden: false, createElement: t => new El(t), createElementNS: (ns, t) => new El(t, ns), addEventListener() {}, removeEventListener() {}};
const NOW = 1800000000000;
async function chart(config, status, opts = {}) {
  const state = {fetchJSON: async url => { if (url.includes('/tcp-mini')) { if (status instanceof Error) throw status; return status; } return opts.history?.(url) ?? new Promise(() => {}); }};
  const api = L.create({document: fakeDoc, state, interval: () => 0, clear() {}, now: () => NOW, monotonicNow: () => 0, unauthorized() {}});
  const node = api.mount({agent_id: AGENT, online: true});
  const plot = node.all().find(n => n.className === 'lm-plot'); plot.rect = {width: 300, height: 80, left: 0, top: 0, right: 300, bottom: 80}; node.rect = {width: 300, height: 200, left: 0, top: 0, right: 300, bottom: 200};
  globalThis.innerHeight = 1000; globalThis.innerWidth = 1000;
  api.bindConfig(AGENT, config); api.setVisible(AGENT, true);
  for (let i = 0; i < 30; i++) { await new Promise(r => setImmediate(r)); api.tick(); }
  return {api, node, plot, svg: () => plot.children[0].all(), note: node.all().find(n => n.className === 'lm-alert-note'), legend: node.all().filter(n => n.tagName === 'button' && n.dataset.focusKey && n.dataset.focusKey.startsWith('lm-') && C.slots.includes(n.dataset.focusKey.slice(3)))};
}
const texts = c => c.svg().filter(n => n.tagName === 'text').map(n => n.ownText);

// A one-day minute history for a target of the given configuration, in the shape the chart accepts.
const minuteHistory = (config, now = NOW) => url => {
  const id = new URL(url, 'http://local').searchParams.get('target_id'), t = config.targets.find(x => x.id === id);
  if (!t) return Promise.reject(new Error('unknown target'));
  const windowStart = Math.floor(now / 60000) * 60000 - 1439 * 60000;
  const points = Array.from({length: 1440}, (_, i) => { const start = windowStart + i * 60000;
    return {start, end: Math.min(start + 60000, now), count: 2, valid_rtts: 2, not_executed: 0, sent: 6, received: 6, p50_5m_ms: 20, p50_ms: 20, p95_ms: 30, completeness: 'complete', execution_coverage: 'unverified',
      config_revision_min: config.revision, config_revision_max: config.revision, slot_revision_min: t.slot_revision, slot_revision_max: t.slot_revision, categories: {success: 2}}; });
  return Promise.resolve({target: {id: t.id, slot: t.slot, family: t.family, protocol: t.protocol, slot_revision: t.slot_revision, status: t.status}, view: '1day', resolution_ms: 60000, now, window_start: windowStart,
    algorithm: 'weighted-grid-rtt-adjacent-v3', histogram_version: 1, grid_ms: .25, percentile_max_error_ms: .125, groups: [{semantics: 'staged_probe_v2', metric: t.protocol === 'tcp' ? 'pure_connect' : 'echo_rtt', points}]});
};

test('the card header: 设置 replaces the region button, the IPv4/IPv6 switch stays, there is no V6 detection switch', async () => {
  const f = setup({raw: load('v4-running')});
  f.ui.setVisible(AGENT, true); await flush();
  const labels = f.node.querySelectorAll('button').filter(b => b.className.split(' ').includes('nq-link') || b.parent?.className.includes('nq-tabs')).map(b => b.textContent);
  assert.deepEqual(labels.sort(), ['IPv4', 'IPv6', '设置']);
  assert.doesNotMatch(f.node.all().map(n => n.ownText).join('|'), /地区选择|V6 检测/);
});

test('an unconfirmed save (network error / 5xx) is not retried by the page; the dialog reads the Server back and says so', async () => {
  const f = setup({raw: load('v4-saved-off'), save: (_b, ctx) => ctx.reply(500, {error: {code: 'internal', message: '内部错误'}})});
  const panel = await f.open();
  f.type('telecom-port', '444');
  f.field('save-top').click(); await flush();
  assert.match(panel.querySelector('.nq-dialog-foot').textContent, /未确认保存成功/);
  const saves = () => f.writes.filter(w => w.kind === 'save').length;
  assert.equal(saves(), 1);
  f.ui.tick(); await flush();
  assert.equal(saves(), 1, 'no automatic second save');
  assert.ok(f.requests.filter(r => r.url.endsWith('/v4/config') && r.method === 'GET').length >= 2, 'the Server was read back');
  assert.equal(f.field('telecom-port').value, '444', 'the draft is kept');
  const busy = setup({raw: load('v4-saved-off'), save: (_b, ctx) => ctx.reply(503, {error: {code: 'retryable', message: 'x'}})});
  const p2 = await busy.open(); busy.type('telecom-port', '444'); busy.field('save-top').click(); await flush();
  assert.match(p2.querySelector('.nq-dialog-foot').textContent, /地址检查繁忙，请稍后重试/);
});

test('a save in flight survives the card leaving the viewport and is not cancelled', async () => {
  let release; const gate = new Promise(r => { release = r; });
  const f = setup({raw: load('v4-saved-off'), save: async (b, ctx) => { await gate; return saved(b, ctx); }});
  await f.open();
  f.type('telecom-port', '444'); f.field('save-top').click(); await flush();
  assert.equal(f.writes.length, 1);
  f.ui.setVisible(AGENT, false); await flush();
  assert.ok(f.ui.snapshot().panel, 'the dialog stays');
  release(); await flush();
  assert.equal(f.ui.snapshot().panel.dirty, false);
  assert.equal(f.writes.length, 1);
});

// ---- review fixes (R07-D-review M1-M7) ----
const keydown = (f, key, extra = {}) => { let prevented = false; f.doc.dispatch('keydown', {key, preventDefault() { prevented = true; }, ...extra}); return prevented; };
const inside = f => f.panel().contains(f.doc.activeElement);
const guards = f => f.panel().querySelectorAll('.nq-guard').filter(n => !n.hidden).map(n => n.querySelector('p').textContent);

test('M1: the guards are hidden unless asked for, and the stylesheet lets hidden win over the flex rule that lays them out', async () => {
  const css = fs.readFileSync(path.join(__dirname, '..', 'web', 'static', 'network-quality.css'), 'utf8');
  assert.match(css, /\.nq-overlay \[hidden\]\{display:none!important\}/, 'a rule with display on .nq-guard would otherwise show hidden guards');
  const f = setup({raw: load('v4-saved-off'), save: saved, enable: toggled});
  await f.open();
  assert.deepEqual(guards(f), [], 'a clean dialog shows no question');
  f.type('telecom-port', '444'); f.check('enabled', true);
  assert.deepEqual(guards(f), ['有未保存的修改，先保存吗？']);
  f.byText(f.panel(), '取消').click();
  assert.deepEqual(guards(f), []);
  f.field('close').click();
  assert.deepEqual(guards(f), ['放弃修改？']);
  f.field('guard-keep').click();
  assert.deepEqual(guards(f), []);
});

test('M2: the focus starts inside the dialog even when the master switch is disabled, and Tab brings stray focus back in', async () => {
  const f = setup({raw: load('v4-empty-unreported')});
  await f.open();
  assert.equal(f.field('enabled').disabled, true);
  assert.ok(inside(f), 'initial focus is in the dialog');
  assert.notEqual(f.doc.activeElement, f.field('enabled'));
  f.doc.activeElement = f.node.querySelector('.nq-link'); // the trigger behind the dialog
  assert.equal(keydown(f, 'Tab'), true); assert.ok(inside(f), 'Tab from outside enters the dialog');
  f.doc.activeElement = f.node;
  keydown(f, 'Tab', {shiftKey: true}); assert.ok(inside(f), 'Shift+Tab from outside enters it too');
  // Wrapping at both ends.
  const reachable = f.panel().querySelectorAll('button,input,select').filter(n => !n.disabled && !n.hidden && !n.parentNode?.hidden);
  f.doc.activeElement = reachable.at(-1); keydown(f, 'Tab'); assert.equal(f.doc.activeElement, reachable[0]);
  keydown(f, 'Tab', {shiftKey: true}); assert.equal(f.doc.activeElement, reachable.at(-1));
  f.field('close').click(); assert.ok(!f.panel());
  assert.equal(f.doc.activeElement, f.node.querySelector('.nq-link'), 'closing returns to the button that opened it');
});

test('M3: after switching to a literal address the ticks of the other family can be cleared, and nothing wrong can be newly ticked', async () => {
  const f = setup({raw: load('v4-running'), save: saved});
  await f.open();
  for (const key of ['ipv4_tcp', 'ipv4_icmp', 'ipv6_tcp', 'ipv6_icmp']) assert.equal(f.field(`telecom-${key}`).checked, true, key);
  f.type('telecom-host', '93.184.216.34');
  for (const key of ['ipv6_tcp', 'ipv6_icmp']) { assert.equal(f.field(`telecom-${key}`).checked, true); assert.equal(f.field(`telecom-${key}`).disabled, false, `${key} can be cleared`); }
  f.field('save-bottom').click(); await flush();
  assert.equal(f.writes.length, 0, 'the mismatch still blocks saving');
  f.check('telecom-ipv6_tcp', false); f.check('telecom-ipv6_icmp', false);
  for (const key of ['ipv6_tcp', 'ipv6_icmp']) assert.equal(f.field(`telecom-${key}`).disabled, true, `${key} cannot be ticked again`);
  f.field('save-bottom').click(); await flush();
  assert.equal(f.writes.length, 1);
  assert.deepEqual([f.writes[0].body.slots[0].ipv6_tcp, f.writes[0].body.slots[0].ipv6_icmp, f.writes[0].body.slots[0].ipv4_tcp], [false, false, true]);
  // And the other way round, then back to a name.
  const g = setup({raw: load('v4-running')});
  await g.open();
  g.type('telecom-host', '2606:4700::1111');
  for (const key of ['ipv4_tcp', 'ipv4_icmp']) { assert.equal(g.field(`telecom-${key}`).checked, true); assert.equal(g.field(`telecom-${key}`).disabled, false); }
  g.type('telecom-host', 'example.net');
  for (const key of ['ipv4_tcp', 'ipv4_icmp', 'ipv6_tcp', 'ipv6_icmp']) assert.equal(g.field(`telecom-${key}`).disabled, false);
});

test('M3: an IPv4-mapped IPv6 address counts as IPv4, as it does on the Server', () => {
  for (const host of ['::ffff:93.184.216.34', '::FFFF:93.184.216.34', '::ffff:5db8:d822', '0:0:0:0:0:ffff:93.184.216.34', '0000:0000:0000:0000:0000:ffff:5db8:d822', ' ::ffff:1.2.3.4 ']) assert.equal(C.literalFamily(host), 'ipv4', host);
  for (const host of ['2606:4700::1111', '::1', '::', '::ffff', '64:ff9b::1.2.3.4', '2001:db8::ffff:1.2.3.4', '::fffe:1.2.3.4', '1::ffff:1.2.3.4']) assert.equal(C.literalFamily(host), 'ipv6', host);
  assert.equal(C.literalFamily('93.184.216.34'), 'ipv4'); assert.equal(C.literalFamily('example.net'), '');
  const draft = C.draftOf(C.adapt(load('v4-running')));
  draft.slots[0].host = '::ffff:93.184.216.34'; draft.slots[0].port = '443';
  assert.deepEqual(Object.keys(C.hints(draft).telecom).sort(), ['ipv6_icmp', 'ipv6_tcp']);
  assert.deepEqual(C.disabledSubs('::ffff:93.184.216.34'), {ipv4_tcp: false, ipv4_icmp: false, ipv6_tcp: true, ipv6_icmp: true});
});

// A second page changed the same slot while this one edited only its label.
const remoteChange = () => { const r = clone(load('v4-running')); r.settings_revision = '7'; Object.assign(r.slots[0], {host: 'new.example.net', port: 8443, ipv4_icmp: false}); return r; };
const conflictSetup = (extra = {}) => {
  const remote = remoteChange();
  return setup({raw: load('v4-running'), save: (_b, ctx) => (ctx.raw = remote, ctx.reply(409, {error: {code: 'revision_conflict', message: 'x'}})), enable: toggled, ...extra});
};

test('M4: a refused save is never merged: the user is asked, "load the latest" drops the draft, nothing is written meanwhile', async () => {
  const f = conflictSetup();
  await f.open();
  const before = load('v4-running').slots[0];
  f.choose('telecom-region-select', '浙江');
  f.field('save-top').click(); await flush();
  assert.deepEqual(guards(f), ['配置已被其他页面修改']);
  assert.match(f.panel().querySelector('.nq-dialog-foot').textContent, /配置已被其他页面修改/);
  assert.equal(f.writes.length, 1, 'no automatic second write');
  const draft = f.ui.snapshot().panel.draft.slots[0];
  assert.equal(draft.region, '浙江'); assert.equal(draft.host, before.host, 'the other page\'s address is not mixed into the draft');
  assert.equal(draft.port, String(before.port)); assert.equal(draft.ipv4_icmp, before.ipv4_icmp);
  assert.equal(f.field('telecom-host').value, before.host);
  f.field('guard-load-latest').click(); await flush();
  const now = f.ui.snapshot().panel;
  assert.equal(now.dirty, false); assert.equal(now.base, '7'); assert.equal(now.guard, null);
  assert.equal(f.field('telecom-host').value, 'new.example.net'); assert.equal(f.field('telecom-port').value, '8443'); assert.equal(f.field('telecom-ipv4_icmp').checked, false);
  assert.match(f.panel().querySelector('.nq-dialog-foot').textContent, /已载入最新配置，本页草稿已放弃/);
  assert.equal(f.writes.length, 1);
});

test('M4: "cancel" keeps the draft and the dialog; saving it again is refused again instead of overwriting the other page', async () => {
  const f = conflictSetup();
  await f.open();
  f.choose('telecom-region-select', '浙江');
  f.field('save-top').click(); await flush();
  f.field('guard-conflict-cancel').click();
  assert.deepEqual(guards(f), []); assert.ok(f.panel(), 'the dialog stays');
  assert.equal(f.field('telecom-region-select').value, '浙江');
  assert.equal(f.ui.snapshot().panel.dirty, true);
  f.field('save-top').click(); await flush();
  assert.equal(f.writes.length, 2);
  assert.equal(f.writes[1].body.expected_revision, load('v4-running').settings_revision, 'the revision the user saw, never the latest one');
  assert.deepEqual(guards(f), ['配置已被其他页面修改'], 'asked again');
});

test('M4: a conflict while "save and switch on" stops there: no switch-on request', async () => {
  const off = clone(load('v4-saved-off')); const g = setup({raw: off, save: (_b, ctx) => (ctx.raw = Object.assign(clone(off), {settings_revision: '9', interval_ms: 90000}), ctx.reply(409, {error: {code: 'revision_conflict', message: 'x'}})), enable: toggled});
  await g.open();
  g.type('telecom-port', '444'); g.check('enabled', true);
  g.byText(g.panel(), '保存并开启').click(); await flush();
  assert.deepEqual(guards(g), ['配置已被其他页面修改']);
  assert.deepEqual(g.writes.map(w => w.kind), ['save']);
});

test('M4: a changed configuration found by a later read asks the same question; the unchanged case follows silently', async () => {
  let offset = 0;
  const f = setup({raw: load('v4-running'), clock: () => offset});
  await f.open();
  f.choose('telecom-region-select', '浙江');
  f.setRaw(remoteChange()); offset += 31000; f.ui.tick(); await flush();
  assert.deepEqual(guards(f), ['配置已被其他页面修改']);
  assert.equal(f.field('telecom-host').value, load('v4-running').slots[0].host, 'the draft is untouched');
  f.field('guard-conflict-cancel').click();
  offset += 31000; f.ui.tick(); await flush();
  assert.deepEqual(guards(f), [], 'the same latest is not asked about again');
  // A clean dialog simply follows.
  const g = setup({raw: load('v4-running'), clock: () => offset});
  await g.open();
  g.setRaw(remoteChange()); offset += 31000; g.ui.tick(); await flush();
  assert.deepEqual(guards(g), []); assert.equal(g.field('telecom-host').value, 'new.example.net');
});

const configReads = f => f.requests.filter(r => r.method === 'GET' && r.url.endsWith('/v4/config')).length;

test('M7: a visible card reads the v4 state again every 30 seconds, so negotiation finishing shows up without reopening anything', async () => {
  screen(1000, 1000);
  let offset = 0;
  const f = setup({raw: load('v4-empty-unreported'), clock: () => offset});
  f.ui.setVisible(AGENT, true); await flush();
  assert.equal(configReads(f), 1);
  assert.match(f.node.querySelector('.nq7-row').querySelector('.nq7-text').title + f.node.querySelector('.nq7-note').textContent, /Agent/);
  f.setRaw(load('v4-running'));
  offset += 10000; f.ui.tick(); await flush();
  assert.equal(configReads(f), 1, 'not before the period is over');
  offset += 21000; f.ui.tick(); await flush();
  assert.equal(configReads(f), 2);
  assert.doesNotMatch(f.node.querySelector('.nq7-note').textContent, /尚未支持/);
  assert.ok(f.requests.some(r => r.url.includes('/v5/card')), 'the lanes that now run read their judgement');
});

test('M7: a target that turns abnormal and recovers is followed', async () => {
  screen(1000, 1000);
  let offset = 0;
  const f = setup({raw: load('v4-running'), clock: () => offset});
  f.ui.setVisible(AGENT, true); await flush();
  const lanesWith = c => c.lanes.map(l => l.status);
  assert.ok(!lanesWith(f.raw()).includes('target_abnormal'));
  f.setRaw(load('v4-target-abnormal')); offset += 31000; f.ui.tick(); await flush();
  assert.ok(f.node.all().some(n => n.dataset.lane === 'abnormal'));
  f.setRaw(load('v4-running')); offset += 31000; f.ui.tick(); await flush();
  assert.ok(!f.node.all().some(n => n.dataset.lane === 'abnormal'));
});

test('M7: no reads while hidden or off screen, one catch-up read when back', async () => {
  let offset = 0;
  screen(1000, 1000);
  const f = setup({raw: load('v4-running'), clock: () => offset});
  f.ui.setVisible(AGENT, true); await flush();
  const base = configReads(f);
  f.doc.hidden = true; offset += 120000; f.ui.tick(); await flush();
  assert.equal(configReads(f), base, 'a hidden page does not read');
  f.doc.hidden = false; screen(0, 0); f.ui.tick(); offset += 120000; f.ui.tick(); await flush(); // the geometry says: not on screen
  assert.equal(configReads(f), base, 'a card out of view does not read');
  screen(1000, 1000); f.ui.tick(); await flush();
  assert.equal(configReads(f), base + 1, 'back in view: one read');
});

test('M7: a failing read backs off 30 s, 60 s, 120 s and a good one resets it; the last good state stays on screen', async () => {
  screen(1000, 1000);
  let offset = 0, failing = false;
  const f = setup({raw: load('v4-running'), clock: () => offset, readFail: () => failing});
  f.ui.setVisible(AGENT, true); await flush();
  let reads = configReads(f);
  failing = true;
  for (const [step, expected] of [[31000, 1], [31000, 0], [30000, 1], [60000, 0], [61000, 1]]) {
    offset += step; f.ui.tick(); await flush();
    assert.equal(configReads(f) - reads, expected, 'after +' + step); reads = configReads(f);
  }
  assert.match(f.node.querySelector('.nq7-note').textContent, /配置读取失败，保留上次可信状态/);
  assert.ok(f.node.all().some(n => n.dataset.lane === 'blocks'), 'the last good lanes are still shown');
  failing = false; offset += 241000; f.ui.tick(); await flush();
  assert.equal(configReads(f) - reads, 1); reads = configReads(f);
  assert.equal(f.node.querySelector('.nq7-note').textContent, '每格 3 分钟');
  offset += 31000; f.ui.tick(); await flush();
  assert.equal(configReads(f) - reads, 1, 'back to the 30 s pace');
});

test('M7: a refresh while the dialog is saving does not start, and writes are never made by a refresh', async () => {
  screen(1000, 1000);
  let offset = 0;
  const gate = {}; gate.promise = new Promise(r => { gate.release = r; });
  const f = setup({raw: load('v4-saved-off'), clock: () => offset, save: async (b, ctx) => { await gate.promise; return saved(b, ctx); }});
  await f.open();
  f.type('telecom-port', '444'); f.field('save-top').click(); await flush();
  const reads = configReads(f);
  offset += 31000; f.ui.tick(); await flush();
  assert.equal(configReads(f), reads, 'no read while a save is in flight');
  gate.release(); await flush();
  offset += 31000; f.ui.tick(); await flush();
  assert.ok(configReads(f) > reads);
  assert.equal(f.writes.length, 1, 'only the user\'s own save');
});

test('S2: a restarted Agent that was authorized shows "正在连接…"; a first upgrade whose first step has arrived still reads as not supported (the Server decides, the page maps)', async () => {
  const negotiating = setup({raw: load('v4-negotiating')}); negotiating.ui.setVisible(AGENT, true); await flush();
  assert.equal(load('v4-negotiating').agent_support, 'negotiating');
  assert.equal(negotiating.node.querySelector('.nq7-note').textContent, '正在连接…');
  assert.ok(negotiating.node.all().some(n => n.dataset.lane === 'negotiating'));
  const first = setup({raw: load('v4-first-upgrade')}); first.ui.setVisible(AGENT, true); await flush();
  assert.equal(load('v4-first-upgrade').agent_support, 'unsupported');
  assert.equal(first.node.querySelector('.nq7-note').textContent, 'Agent 尚未支持网络质量');
});

test('R1: ICMP unsupported + a literal address of the other family: ticked boxes can always be unticked, the save succeeds, nothing wrong can be ticked again', async () => {
  for (const [literal, clear, keep] of [['93.184.216.34', ['ipv6_tcp', 'ipv6_icmp'], ['ipv4_tcp', 'ipv4_icmp']], ['2606:4700::1111', ['ipv4_tcp', 'ipv4_icmp'], ['ipv6_tcp', 'ipv6_icmp']]]) {
    const f = setup({raw: load('v4-no-ipv6-no-icmp'), save: saved});
    await f.open();
    f.type('telecom-host', literal);
    for (const k of clear) { assert.equal(f.field(`telecom-${k}`).checked, true); assert.equal(f.field(`telecom-${k}`).disabled, false, `${literal}: ${k}`); }
    f.field('save-bottom').click(); await flush();
    assert.equal(f.writes.length, 0, 'the mismatch still blocks the save');
    for (const k of clear) f.check(`telecom-${k}`, false);
    for (const k of clear) { assert.equal(f.field(`telecom-${k}`).checked, false); assert.equal(f.field(`telecom-${k}`).disabled, true, 'not ticked again'); }
    f.field('save-bottom').click(); await flush();
    assert.equal(f.writes.length, 1, literal);
    for (const k of clear) assert.equal(f.writes[0].body.slots[0][k], false, k);
    for (const k of keep) assert.equal(f.writes[0].body.slots[0][k], true, k);
  }
});

test('R1: an unsupported ICMP box that is ticked can be unticked (and is then closed again); an unticked one cannot be ticked', async () => {
  const f = setup({raw: withIcmp('v4-saved-off', 'unsupported', 'supported')});
  await f.open();
  assert.equal(f.field('telecom-ipv4_icmp').checked, true);
  assert.equal(f.field('telecom-ipv4_icmp').disabled, false, 'a tick can always be taken back');
  f.check('telecom-ipv4_icmp', false);
  assert.equal(f.field('telecom-ipv4_icmp').disabled, true);
  assert.equal(f.ui.snapshot().panel.draft.slots[0].ipv4_icmp, false);
  assert.equal(f.field('telecom-ipv6_icmp').disabled, false, 'the supported family is untouched');
});

test('R2: an automatic refresh queued behind busy slots is dropped when the card leaves the viewport or the page is hidden, and read once on return', async () => {
  for (const how of ['setVisible', 'geometry', 'hidden']) {
    screen(1000, 1000);
    let offset = 0;
    const q = C.coordinator({now: () => 1800000000000 + offset});
    const f = setup({raw: load('v4-running'), clock: () => offset, coordinator: q});
    await f.open();
    const before = configReads(f);
    const owner = {}, release = [];
    const blockers = Array.from({length: 4}, (_, i) => q.request('block-' + i, owner, () => new Promise(r => release.push(r))));
    offset = 31000; f.ui.tick(); await flush();
    assert.equal(configReads(f), before, how + ': queued, not started');
    if (how === 'setVisible') f.ui.setVisible(AGENT, false);
    else if (how === 'geometry') { screen(0, 0); f.ui.tick(); }
    else { f.doc.hidden = true; f.doc.dispatch('visibilitychange'); }
    for (const r of release) r('done'); await Promise.all(blockers); await flush();
    assert.equal(configReads(f), before, how + ': nothing is read after the card left');
    assert.equal(f.writes.length, 0);
    f.doc.hidden = false; screen(1000, 1000);
    if (how === 'hidden') f.doc.dispatch('visibilitychange');
    f.ui.setVisible(AGENT, true); offset += 1000; f.ui.tick(); await flush();
    assert.equal(configReads(f), before + 1, how + ': one read on return');
    assert.notEqual(f.ui.snapshot().panel, null, 'the dialog stayed open');
  }
});

const changedRemote = rev => { const r = clone(load('v4-running')); r.settings_revision = rev; r.slots[0].host = 'changed.example.net'; return r; };

test('R3: the user\'s read is adopted when the queued automatic refresh is cancelled by the card leaving; both read paths keep their own state', async () => {
  for (const how of ['setVisible', 'geometry', 'hidden']) {
    screen(1000, 1000);
    let offset = 0;
    const q = C.coordinator({now: () => 1800000000000 + offset});
    const f = setup({raw: load('v4-running'), clock: () => offset, coordinator: q});
    f.ui.setVisible(AGENT, true); await flush();
    const before = configReads(f);
    const owner = {}, release = [];
    const blockers = Array.from({length: 4}, (_, i) => q.request('block-' + i, owner, () => new Promise(r => release.push(r))));
    offset = 31000; f.ui.tick(); await flush(); // the automatic refresh is queued
    f.setRaw(changedRemote('7'));
    f.node.querySelector('.nq-link').click(); await flush(); // the user's read is queued too
    if (how === 'setVisible') f.ui.setVisible(AGENT, false);
    else if (how === 'geometry') { screen(0, 0); f.ui.tick(); }
    else { f.doc.hidden = true; f.doc.dispatch('visibilitychange'); }
    for (const r of release) r('done'); await Promise.all(blockers); await flush();
    if (how === 'hidden') { // the page hiding stops every read, the user's too (old policy); showing it again reads for the open dialog
      f.doc.hidden = false; f.doc.dispatch('visibilitychange'); f.ui.setVisible(AGENT, true); await flush();
    }
    assert.equal(f.field('telecom-host').value, 'changed.example.net', how);
    assert.equal(f.ui.snapshot().panel.base, '7', how);
    assert.equal(f.writes.length, 0);
    if (how !== 'hidden') assert.equal(configReads(f), before + 1, how + ': only the user\'s read ran');
  }
});

test('R3: an older answer never overwrites a newer one, in either order, and a held automatic read does not block the user\'s', async () => {
  screen(1000, 1000);
  let offset = 0;
  const hold = []; // the first GET after the initial ones is held
  const f = setup({raw: load('v4-running'), clock: () => offset, holdRead: i => (i === 1 ? new Promise(r => hold.push(r)) : undefined)});
  f.ui.setVisible(AGENT, true); await flush(); // GET 0: initial
  offset = 31000; f.ui.tick(); await flush();    // GET 1: automatic, held (answers with the content of the moment it was asked: old)
  f.setRaw(changedRemote('7'));
  f.node.querySelector('.nq-link').click(); await flush(); // GET 2: the user's, new content, answered at once
  assert.equal(f.field('telecom-host').value, 'changed.example.net');
  hold[0](); await flush(); // the old automatic answer arrives late
  assert.equal(f.field('telecom-host').value, 'changed.example.net', 'the late old answer is dropped');
  assert.equal(f.ui.snapshot().panel.base, '7');
  assert.equal(f.writes.length, 0);
});

// ---- D1 (stage E): the page and the Server's real history answers ----
const serverHistory = () => ({card: load('history-card-tcp'), day: load('history-1day-tcp')});
const laneTarget = (raw, slot, family, protocol) => raw.lanes.find(l => l.slot === slot && l.family === family && l.protocol === protocol).target_id;

test('D1: the history answers of the real Server name the lane\'s target by its id', () => {
  const raw = load('v4-running'), id = laneTarget(raw, 'telecom', 'ipv4', 'tcp'), h = serverHistory();
  assert.equal(h.card.target.id, id); assert.equal(h.day.target.id, id);
  assert.equal(h.card.target.slot_revision, raw.lanes.find(l => l.target_id === id).slot_revision);
});

test('D1: the history dialog opens on the Server\'s real answer and shows its statistics', async () => {
  screen(1000, 1000);
  const raw = load('v4-running'), id = laneTarget(raw, 'telecom', 'ipv4', 'tcp');
  const f = setup({raw, serverHistory: serverHistory()});
  f.ui.setVisible(AGENT, true); await flush();
  assert.equal(f.ui.openHistoryFor(AGENT, id, 'ipv4', 'latency', f.node), true);
  await flush();
  const text = f.dialog.all().map(n => n.ownText).join('\n');
  assert.match(text, /窗口统计：P50/);
  assert.match(text, /样本 4/);
  assert.doesNotMatch(text, /读取失败/);
});
