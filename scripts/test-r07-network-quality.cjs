// R07 stage D: user-entered network quality targets. The responses come from the Server's own tests
// (scripts/fixtures/r07), not from hand-written DTOs.
const {test} = require('node:test'), assert = require('node:assert/strict');
const fs = require('node:fs'), path = require('node:path');
const {C, UI, load, clone, flush, setup, cardHistory, allowedTags, tags, AGENT} = require('./lib/r07-harness.cjs');

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
  assert.deepEqual(Object.keys(out), ['expected_revision', 'interval_ms', 'slots']);
  assert.equal(out.expected_revision, '1');
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

test('ICMP: unsupported on this host disables the ICMP sub-items of that family and says why', async () => {
  const f = setup({raw: load('v4-no-ipv6-no-icmp')});
  await f.open();
  for (const slot of C.slots) {
    // The IPv6 side is reported as "no IPv6" first, so only the IPv4 ICMP lane tells us the host cannot send ICMP.
    assert.equal(f.field(`${slot}-ipv4_icmp`).disabled, true, `${slot}-ipv4_icmp`);
    assert.equal(f.field(`${slot}-ipv4_tcp`).disabled, false);
  }
  assert.match(f.panel().all().map(n => n.textContent).join('|'), /当前环境不支持 ICMP/);
  // A saved tick on the disabled box is kept, not cleared.
  assert.equal(f.field('telecom-ipv4_icmp').checked, true);
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
  assert.equal(f.node.querySelector('.nq-status').textContent, '页面已过期，请刷新');
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
    const rows = f.node.querySelectorAll('.nq-row');
    return {f, rows, text: rows.map(r => [r.querySelector('.nq-target').textContent, r.querySelector('.nq-tcp-value').textContent, r.querySelector('.nq-icmp-value').textContent]), status: f.node.querySelector('.nq-status').textContent};
  };
  const running = await lanes('v4-running');
  assert.equal(running.text[0][0], '电信 · 广东'); assert.equal(running.text[1][0], 'NTT · Tokyo'); assert.equal(running.text[2][0], '移动');
  assert.match(running.text[0][1], /ms$/); assert.match(running.text[0][2], /%$/);
  assert.deepEqual(running.text[2].slice(1), ['未配置目标', '未配置目标'], 'a slot without an address');
  const v6 = await lanes('v4-running', 'ipv6');
  assert.deepEqual(v6.text[1].slice(1), ['未启用', '未启用'], 'ticked for IPv4 only');
  const off = await lanes('v4-saved-off');
  assert.deepEqual(off.text[0].slice(1), ['已关闭', '已关闭']); assert.equal(off.status, '已关闭');
  const old = await lanes('v4-old-agent');
  assert.deepEqual(old.text[0].slice(1), ['需升级 Agent', '需升级 Agent']); assert.match(old.status, /需升级 Agent/);
  const noV6 = await lanes('v4-no-ipv6-no-icmp', 'ipv6');
  assert.equal(noV6.text[0][1], '本机无 IPv6');
  const noIcmp = await lanes('v4-no-ipv6-no-icmp');
  assert.equal(noIcmp.text[0][2], '不支持 ICMP'); assert.match(noIcmp.text[0][1], /ms$/, 'TCP keeps working');
  const abnormal = await lanes('v4-target-abnormal');
  assert.equal(abnormal.text[0][1], '目标异常');
  assert.match(abnormal.f.node.querySelectorAll('.nq-tcp-value')[0].title, /域名不存在.*不计入失败率/);
  assert.match(abnormal.text[0][2], /%$/, 'the other lane of the slot is untouched');
  const empty = await lanes('v4-empty-unreported');
  assert.deepEqual(empty.text[0].slice(1), ['Agent 不支持', 'Agent 不支持']);
});

test('cards: only measuring lanes read history; a changed address starts a new series, a changed label does not', async () => {
  const f = setup({raw: load('v4-running')});
  f.ui.setVisible(AGENT, true); await flush();
  const history = () => f.requests.filter(r => r.url.includes('card-history')).map(r => new URL(r.url, 'http://x').searchParams.get('target_id'));
  const config = C.adapt(f.raw());
  const running = config.targets.filter(t => t.family === 'ipv4' && t.status === 'active').map(t => t.id).sort();
  assert.deepEqual([...new Set(history())].sort(), running, 'one read per running IPv4 lane');
  const squares = f.node.querySelectorAll('.nq-block');
  assert.equal(squares.length, 12 * running.length);
  const before = history().length, configsBefore = f.configs();
  // Only the labels change: same targets, same revisions.
  const relabelled = clone(f.raw()); relabelled.settings_revision = inc(relabelled.settings_revision);
  relabelled.slots[0].region = '浙江'; relabelled.slots[0].isp = 'unicom'; relabelled.slots[1].kind = 'builtin'; relabelled.slots[1].region = '北京'; relabelled.slots[1].isp = 'mobile';
  f.setRaw(relabelled); f.ui.openHistoryFor; // keep the API surface referenced
  await f.open(); f.field('close').click(); await flush();
  assert.equal(f.node.querySelectorAll('.nq-row')[0].querySelector('.nq-target').textContent, '联通 · 浙江', 'row label follows the slot labels');
  assert.equal(f.node.querySelectorAll('.nq-row')[1].querySelector('.nq-target').textContent, '移动 · 北京');
  assert.equal(history().length, before, 'no history was read again');
  assert.deepEqual(f.node.querySelectorAll('.nq-block'), squares, 'the same twelve cells, not rebuilt');
  assert.ok(f.configs() > configsBefore, 'the latency chart is told (it relabels its legend and keeps its curves)');
  // The address of slot 1 changes: new target ids, the old series is gone from the card.
  const moved = clone(f.raw()); moved.settings_revision = inc(moved.settings_revision); moved.config_revision = inc(moved.config_revision);
  for (const lane of moved.lanes) if (lane.slot === 'telecom' && lane.target_id) { lane.target_id = lane.target_id.replace(/^./, c => c === 'a' ? 'b' : 'a'); lane.slot_revision = moved.config_revision; }
  f.setRaw(moved); await f.open(); f.field('close').click(); await flush();
  const after = C.adapt(moved).targets.filter(t => t.slot === 'telecom' && t.family === 'ipv4' && t.status === 'active').map(t => t.id);
  assert.ok(after.every(id => history().includes(id)), 'the new targets were read');
  assert.ok(history().length > before);
  const oldIds = config.targets.filter(t => t.slot === 'telecom' && t.family === 'ipv4').map(t => t.id);
  assert.ok(f.node.all().every(n => !oldIds.some(id => (n.title || '').includes(id))), 'no cell of the old target is left');
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
    const expected = `${evil} · ${evil}`;
    assert.equal(f.node.querySelectorAll('.nq-row')[0].querySelector('.nq-target').textContent, expected);
    assert.equal(f.node.querySelectorAll('.nq-row')[0].querySelector('.nq-target').title, `${expected} · TCP/ICMP 各自 target`);
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

test('N6: no TCP lane running -> "无当前 TCP 检测", no alert mark, no coverage warning, whatever the old evidence says', async () => {
  const after = await chart(C.adapt(load('v4-saved-off')), load('tcp-mini-alert-switched-off'));
  assert.equal(load('tcp-mini-alert-switched-off').state, 'alert'); assert.equal(load('tcp-mini-alert-switched-off').current_tcp, false);
  assert.ok(texts(after).includes('无当前 TCP 检测'));
  assert.equal(after.svg().filter(n => n.dataset.alert === 'true').length, 0);
  assert.ok(!texts(after).includes('告警') && !texts(after).includes('告警覆盖不足'));
  assert.equal(after.plot.dataset.warning, 'false');
  assert.doesNotMatch(after.plot.title, /告警来源日|峰值/);
  assert.match(after.note.textContent, /无当前 TCP 检测/);
  assert.doesNotMatch(after.note.textContent, /4700|2026-10/, 'no peak and no source day of the old alert');
  const none = await chart(C.adapt(load('v4-saved-off')), load('tcp-mini-none'));
  assert.ok(texts(none).includes('无当前 TCP 检测'));
});

test('N6: a running IPv4 TCP lane shows the alert with its source day and the persistence rule', async () => {
  const status = load('tcp-mini-alert-running'), running = await chart(C.adapt(load('v4-running')), status);
  assert.equal(status.state, 'alert'); assert.equal(status.current_tcp, true);
  const mark = running.svg().filter(n => n.dataset.alert === 'true');
  assert.equal(mark.length, 1); assert.equal(mark[0].ownText, '告警');
  assert.match(mark[0].textContent, new RegExp(`告警来源日 ${status.source_day}`));
  assert.match(mark[0].textContent, /告警持续到之后某一天出现正常 TCP 分钟为止，可能来自已更换的目标/);
  assert.equal(running.plot.dataset.warning, 'true');
  assert.match(running.note.textContent, new RegExp(`告警：告警来源日 ${status.source_day}；告警持续到之后某一天出现正常 TCP 分钟为止，可能来自已更换的目标`));
  assert.ok(!texts(running).includes('无当前 TCP 检测'));
});

test('N6: unknown evidence and a failed read keep the coverage warning only while a TCP lane is running', async () => {
  const unknown = await chart(C.adapt(load('v4-running')), {...load('tcp-mini-none'), current_tcp: true});
  assert.ok(texts(unknown).includes('告警覆盖不足'));
  const failed = await chart(C.adapt(load('v4-running')), new Error('boom'));
  assert.ok(texts(failed).includes('告警覆盖不足'));
  const bad = await chart(C.adapt(load('v4-running')), {...load('tcp-mini-alert-running'), current_tcp: 'yes'});
  assert.ok(texts(bad).includes('告警覆盖不足'), 'a response without a boolean current_tcp is not trusted');
});

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

test('the latency chart relabels on a label-only change and keeps its curves and cached history; a new address clears them', async () => {
  const running = C.adapt(load('v4-running'));
  const c = await chart(running, load('tcp-mini-none'), {history: minuteHistory(running)});
  assert.deepEqual(c.legend.map(b => b.attributes['aria-label']), ['电信 · 广东 RTT 曲线', 'NTT · Tokyo RTT 曲线', '移动 RTT 曲线']);
  const cached = c.api.snapshot().views[0].cachedTargets;
  assert.ok(cached >= 2, 'history of the running lanes is cached: ' + cached);
  const relabelled = clone(load('v4-running')); relabelled.slots[0].region = '浙江'; relabelled.slots[0].isp = 'mobile';
  const snapshotBefore = JSON.stringify(c.api.snapshot().views[0]);
  c.api.bindConfig(AGENT, C.adapt(relabelled));
  assert.equal(c.legend[0].attributes['aria-label'], '移动 · 浙江 RTT 曲线');
  assert.equal(JSON.stringify(c.api.snapshot().views[0]), snapshotBefore, 'view, cache and follow state are untouched');
  assert.equal(c.api.snapshot().views[0].cachedTargets, cached);
  assert.ok(c.svg().some(n => n.tagName === 'path' && n.attributes.d?.includes('L')), 'the curve is still drawn');
  // A new address is a new series: the old curve is gone at once.
  const moved = clone(load('v4-running')); moved.config_revision = String(BigInt(moved.config_revision) + 1n);
  for (const l of moved.lanes) if (l.slot === 'telecom' && l.target_id) { l.target_id = 'a'.repeat(32); l.slot_revision = moved.config_revision; }
  moved.slots[0].host = 'new.example.org';
  c.api.bindConfig(AGENT, C.adapt(moved));
  assert.ok(c.api.snapshot().views[0].cachedTargets < cached, 'the replaced target\'s history is dropped');
});

test('XSS in the latency chart: legend, tooltip text and SVG titles are text', async () => {
  const evil = hostile[1], raw = clone(load('v4-running'));
  raw.slots[0] = {...raw.slots[0], kind: 'custom', region: evil, isp: '<script>1</script>'};
  const c = await chart(C.adapt(raw), load('tcp-mini-alert-running'));
  assert.ok(c.legend[0].attributes['aria-label'].startsWith('<script>1</script> · ' + evil));
  for (const n of [c.node, ...c.node.all()]) assert.ok(['section', 'div', 'button', 'span', 'input', 'output', 'small', 'svg', 'path', 'text', 'title', 'circle'].includes(n.tagName), n.tagName);
  assert.ok(!c.node.all().some(n => n.tagName === 'script' || n.tagName === 'img'));
});

test('the page uses a recorded history shape for the cards (cardHistory helper) and the contract helper agrees with it', () => {
  const t = C.adapt(load('v4-running')).targets.find(x => x.protocol === 'tcp' && x.family === 'ipv4');
  const h = cardHistory(t);
  assert.equal(C.cardCurrent(h, t, {revision: '2'}).text, '21.0ms');
  assert.equal(C.cardPoints(h, t).length, 12);
});

test('the card header: 设置 replaces the region button, the IPv4/IPv6 switch stays, there is no V6 detection switch', async () => {
  const f = setup({raw: load('v4-running')});
  f.ui.setVisible(AGENT, true); await flush();
  const labels = f.node.querySelectorAll('button').filter(b => b.className.split(' ').includes('nq-link') || b.parent?.className === 'nq-tabs').map(b => b.textContent);
  assert.deepEqual(labels, ['设置', 'IPv4', 'IPv6']);
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

test('a history answer for another protocol or slot never fills a card lane', async () => {
  for (const field of ['protocol', 'slot']) {
    const f = setup({raw: load('v4-running'), history: h => ({...h, target: {...h.target, [field]: field === 'protocol' ? (h.target.protocol === 'tcp' ? 'icmp' : 'tcp') : 'wrong-slot'}})});
    f.ui.setVisible(AGENT, true); await flush();
    assert.equal(f.node.querySelectorAll('.nq-low').length, 0, field);
    assert.ok(f.node.querySelectorAll('.nq-metric').filter(m => m.dataset.laneState === 'measuring').every(m => !/[0-9]/.test(m.textContent)), field);
  }
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

test('M6: switching detection off removes the old alert at once and the new answer decides', async () => {
  const status = {...load('tcp-mini-alert-running')};
  const c = await chart(C.adapt(load('v4-running')), status, {history: async () => { throw new Error('no history here'); }});
  assert.equal(c.plot.dataset.warning, 'true');
  Object.assign(status, load('tcp-mini-alert-switched-off'));
  c.api.bindConfig(AGENT, C.adapt(load('v4-saved-off')));
  assert.equal(c.plot.dataset.warning, 'false', 'no old alert while the new answer is awaited');
  assert.equal(c.svg().filter(n => n.dataset.alert === 'true').length, 0);
  assert.ok(!texts(c).includes('告警') && !c.note.textContent.includes('告警来源日'));
  for (let i = 0; i < 30; i++) { await new Promise(r => setImmediate(r)); c.api.tick(); }
  assert.ok(texts(c).includes('无当前 TCP 检测'));
  assert.equal(c.plot.dataset.warning, 'false');
  // Back on: the alert is read again, not remembered.
  Object.assign(status, load('tcp-mini-alert-running'));
  c.api.bindConfig(AGENT, C.adapt(load('v4-running')));
  assert.equal(c.plot.dataset.warning, 'false', 'nothing is shown until the Server has answered');
  for (let i = 0; i < 30; i++) { await new Promise(r => setImmediate(r)); c.api.tick(); }
  assert.equal(c.plot.dataset.warning, 'true');
});

test('M6: the last IPv4 TCP lane turning into something else drops the old alert too', async () => {
  const status = {...load('tcp-mini-alert-running')};
  const c = await chart(C.adapt(load('v4-running')), status, {history: async () => { throw new Error('no history here'); }});
  const icmpOnly = clone(load('v4-running'));
  for (const l of icmpOnly.lanes) if (l.family === 'ipv4' && l.protocol === 'tcp') l.status = 'inactive';
  for (const s of icmpOnly.slots) s.ipv4_tcp = false;
  icmpOnly.config_revision = String(BigInt(icmpOnly.config_revision) + 1n);
  Object.assign(status, load('tcp-mini-none'));
  c.api.bindConfig(AGENT, C.adapt(icmpOnly));
  assert.equal(c.plot.dataset.warning, 'false');
  for (let i = 0; i < 30; i++) { await new Promise(r => setImmediate(r)); c.api.tick(); }
  assert.ok(texts(c).includes('无当前 TCP 检测'));
});

const screen = (w, h) => { globalThis.innerWidth = w; globalThis.innerHeight = h; };
const configReads = f => f.requests.filter(r => r.method === 'GET' && r.url.endsWith('/v4/config')).length;

test('M7: a visible card reads the v4 state again every 30 seconds, so negotiation finishing shows up without reopening anything', async () => {
  screen(1000, 1000);
  let offset = 0;
  const f = setup({raw: load('v4-empty-unreported'), clock: () => offset});
  f.ui.setVisible(AGENT, true); await flush();
  assert.equal(configReads(f), 1);
  assert.match(f.node.querySelector('.nq-status').textContent, /Agent 尚未支持网络质量/);
  f.setRaw(load('v4-running'));
  offset += 10000; f.ui.tick(); await flush();
  assert.equal(configReads(f), 1, 'not before the period is over');
  offset += 21000; f.ui.tick(); await flush();
  assert.equal(configReads(f), 2);
  assert.doesNotMatch(f.node.querySelector('.nq-status').textContent, /尚未支持/);
  assert.ok(f.requests.some(r => r.url.includes('card-history')), 'the lanes that now run read their buckets');
});

test('M7: a target that turns abnormal and recovers is followed', async () => {
  screen(1000, 1000);
  let offset = 0;
  const f = setup({raw: load('v4-running'), clock: () => offset});
  f.ui.setVisible(AGENT, true); await flush();
  const lanesWith = c => c.lanes.map(l => l.status);
  assert.ok(!lanesWith(f.raw()).includes('target_abnormal'));
  f.setRaw(load('v4-target-abnormal')); offset += 31000; f.ui.tick(); await flush();
  assert.ok(f.node.all().some(n => n.dataset.laneState === 'abnormal'));
  f.setRaw(load('v4-running')); offset += 31000; f.ui.tick(); await flush();
  assert.ok(!f.node.all().some(n => n.dataset.laneState === 'abnormal'));
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
  assert.match(f.node.querySelector('.nq-status').textContent, /配置读取失败，保留上次可信状态/);
  assert.ok(f.node.all().some(n => n.dataset.laneState === 'measuring'), 'the last good lanes are still shown');
  failing = false; offset += 241000; f.ui.tick(); await flush();
  assert.equal(configReads(f) - reads, 1); reads = configReads(f);
  assert.equal(f.node.querySelector('.nq-status').hidden, true);
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
