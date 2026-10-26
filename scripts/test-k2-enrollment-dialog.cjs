// "Add Agent" dialog: one-click or manual choice, single-use command, copy, countdown, revoke, no re-display.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const E = require('../web/static/agent-enrollment.js');

class El {
  constructor() { this.textContent = ''; this.value = ''; this.disabled = false; this.listeners = {}; this.classes = new Set(['hidden']); this.classList = {
    add: c => this.classes.add(c), remove: c => this.classes.delete(c), toggle: (c, on) => { if (on === undefined) on = !this.classes.has(c); on ? this.classes.add(c) : this.classes.delete(c); return on; }, contains: c => this.classes.has(c) }; }
  setAttribute(k,v){this[k]=v;}
  reportValidity(){return !!this.value.trim();}
  addEventListener(type, f) { (this.listeners[type] || (this.listeners[type] = [])).push(f); }
  async fire(type, event = {preventDefault() {}}) { for (const f of this.listeners[type] || []) await f(event); }
  get hidden() { return this.classes.has('hidden'); }
}
const names = ['choice', 'manual', 'manualResult', 'oneclick', 'oneclickResult', 'chooseOneClick', 'chooseManual', 'reason', 'form', 'name', 'error', 'submit', 'oneclickBack', 'command', 'copy', 'countdown', 'status', 'revoke'];

const COMMAND = 'curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.4/install.sh | PROBE_404_ROLE=agent PROBE_404_SERVER=https://probe.example.com PROBE_404_ENROLL_TOKEN=' + 'T'.repeat(43) + " PROBE_404_AGENT_NAME='web 1' bash";
const T0 = 1800000000000;

function harness(options = {}) {
  const els = Object.fromEntries((options.frontChoice?[...names,'choiceName','next']:names).map(n => [n, new El()]));
  const clock = {now: T0}, calls = [], copied = [], timers = new Map(); let nextTimer = 1, unauthorizedCalls = 0;
  const respond = options.respond || ((url, init) => url.endsWith('/revoke')
    ? {status: 200, ok: true, json: async () => ({revoked: true})}
    : {status: 201, ok: true, json: async () => ({enrollment_id: 'e'.repeat(32), name: JSON.parse(init.body).name, command: COMMAND, created_at: clock.now, expires_at: clock.now + 30 * 60000})});
  const controller = E.createController({
    els, csrf: () => options.csrf === undefined ? 'csrf' : options.csrf, now: () => clock.now,
    fetcher: async (url, init) => { calls.push({url, init}); return respond(url, init); },
    setTimer: f => { const id = nextTimer++; timers.set(id, f); return id; }, clearTimer: id => timers.delete(id),
    clipboard: () => ({writeText: async text => { if (options.copyFails) throw new Error('denied'); copied.push(text); }}),
    available: () => options.available !== false, unavailableReason: () => '开发构建，没有正式 release',
    unauthorized: () => { unauthorizedCalls++; },
  });
  const tick = (ms) => { clock.now += ms; for (const f of [...timers.values()]) f(); };
  return {els, controller, calls, copied, clock, tick, timers, get unauthorized() { return unauthorizedCalls; }};
}
const visible = els => ['choice', 'manual', 'manualResult', 'oneclick', 'oneclickResult'].filter(n => !els[n].hidden);
async function generate(h, name = 'web 1') { h.els.chooseOneClick.fire('click'); h.els.name.value = name; return h.controller.submit(); }

test('K2 dialog: opens on the choice step with one-click available', () => {
  const h = harness();
  assert.deepEqual(visible(h.els), ['choice']);
  assert.equal(h.els.chooseOneClick.disabled, false);
  assert.ok(h.els.reason.hidden);
});

test('K2 dialog: one-click is disabled with the reason on development builds, manual still works', async () => {
  const h = harness({available: false});
  assert.equal(h.els.chooseOneClick.disabled, true);
  assert.ok(!h.els.reason.hidden);
  assert.match(h.els.reason.textContent, /开发构建/);
  await h.els.chooseOneClick.fire('click');
  assert.deepEqual(visible(h.els), ['choice'], 'a disabled one-click choice does not advance');
  await h.els.chooseManual.fire('click');
  assert.deepEqual(visible(h.els), ['manual']);
});

test('K2 dialog: choosing one-click shows the name form; generating shows command, countdown and buttons', async () => {
  const h = harness();
  await h.els.chooseOneClick.fire('click');
  assert.deepEqual(visible(h.els), ['oneclick']);
  h.els.name.value = 'web 1';
  assert.equal(await h.controller.submit(), true);
  assert.deepEqual(visible(h.els), ['oneclickResult']);
  assert.equal(h.els.command.textContent, COMMAND);
  assert.equal(h.els.countdown.textContent, '剩余 30:00');
  assert.equal(h.els.copy.disabled, false);
  assert.equal(h.els.revoke.disabled, false);
  const call = h.calls[0];
  assert.equal(call.url, '/api/v1/web/agent-enrollments');
  assert.equal(call.init.method, 'POST');
  assert.equal(call.init.headers['X-CSRF-Token'], 'csrf');
  assert.deepEqual(JSON.parse(call.init.body), {name: 'web 1'});
});

test('K2 dialog: the countdown ticks down and at zero the command disappears and cannot be copied', async () => {
  const h = harness();
  await generate(h);
  h.tick(61000);
  assert.equal(h.els.countdown.textContent, '剩余 28:59');
  h.tick(28 * 60000 + 58000);
  assert.equal(h.els.countdown.textContent, '剩余 00:01');
  assert.equal(h.els.command.textContent, COMMAND);
  h.tick(1000);
  assert.equal(h.els.countdown.textContent, '已过期，请重新生成');
  assert.equal(h.els.command.textContent, '');
  assert.equal(h.els.copy.disabled, true);
  assert.equal(h.els.revoke.disabled, true);
  assert.equal(h.timers.size, 0, 'the timer stops once expired');
  assert.equal(await h.controller.copy(), false);
  assert.deepEqual(h.copied, []);
});

test('K2 dialog: copy writes the command, shows brief feedback and restores the label', async () => {
  const h = harness();
  h.els.copy.textContent = '复制命令';
  await generate(h);
  await h.els.copy.fire('click');
  assert.deepEqual(h.copied, [COMMAND]);
  assert.equal(h.els.copy.textContent, '已复制');
  h.tick(1500);
  assert.equal(h.els.copy.textContent, '复制命令');
  const failing = harness({copyFails: true});
  failing.els.copy.textContent = '复制命令';
  await generate(failing);
  await failing.els.copy.fire('click');
  assert.equal(failing.els.copy.textContent, '复制失败');
});

test('K2 dialog: revoke posts the token handle, clears the command and disables further actions', async () => {
  const h = harness();
  await generate(h);
  await h.els.revoke.fire('click');
  const call = h.calls[1];
  assert.equal(call.url, '/api/v1/web/agent-enrollments/' + 'e'.repeat(32) + '/revoke');
  assert.equal(call.init.headers['X-CSRF-Token'], 'csrf');
  assert.equal(h.els.command.textContent, '');
  assert.equal(h.els.countdown.textContent, '已作废');
  assert.match(h.els.status.textContent, /已作废/);
  assert.equal(h.els.copy.disabled, true);
  assert.equal(h.els.revoke.disabled, true);
  assert.equal(await h.controller.copy(), false);
});

test('K2 dialog: revoking an already-used command explains why and removes it', async () => {
  const h = harness({respond: (url, init) => url.endsWith('/revoke')
    ? {status: 409, ok: false, json: async () => ({error: {code: 'enrollment_used'}})}
    : {status: 201, ok: true, json: async () => ({enrollment_id: 'e'.repeat(32), command: COMMAND, expires_at: T0 + 1800000})}});
  await generate(h);
  await h.els.revoke.fire('click');
  assert.match(h.els.status.textContent, /已被使用/);
  assert.equal(h.els.command.textContent, '');
});

test('K2 dialog: closing the dialog drops the command; it can never be shown again', async () => {
  const h = harness();
  await generate(h);
  assert.equal(h.controller.hasCommand, true);
  h.controller.close();
  assert.equal(h.els.command.textContent, '');
  assert.equal(h.controller.hasCommand, false);
  assert.deepEqual(visible(h.els), ['choice']);
  assert.equal(h.timers.size, 0);
  assert.equal(await h.controller.copy(), false);
  assert.equal(await h.controller.revoke(), false);
  // Reopening starts from scratch: nothing from the earlier command is left anywhere in the controller's elements.
  for (const name of names) assert.ok(!String(h.els[name].textContent).includes('T'.repeat(43)), name);
  assert.equal(h.els.name.value, '');
  // Generating again makes a fresh request; it never reuses the old one.
  await generate(h, 'second');
  assert.equal(h.calls.filter(c => c.url === '/api/v1/web/agent-enrollments').length, 2);
});

test('K2 dialog: errors keep the form usable; unavailable builds and limits get their own message', async () => {
  const h = harness({respond: () => ({status: 409, ok: false, json: async () => ({error: {code: 'install_unavailable', message: '无法使用一键安装'}})})});
  await h.els.chooseOneClick.fire('click');
  h.els.name.value = 'x';
  assert.equal(await h.controller.submit(), false);
  assert.ok(!h.els.error.hidden);
  assert.equal(h.els.error.textContent, '无法使用一键安装');
  assert.equal(h.els.submit.disabled, false);
  assert.deepEqual(visible(h.els), ['oneclick']);
  const limited = harness({respond: () => ({status: 429, ok: false, json: async () => ({error: {code: 'enrollment_pending_limit'}})})});
  await limited.els.chooseOneClick.fire('click');
  assert.equal(await limited.controller.submit(), false);
  assert.equal(limited.els.error.textContent, '已有 10 条未使用的安装命令，请先使用或作废其中一条，或等它们过期（30 分钟）。');
  const broken = harness({respond: () => { throw new Error('network'); }});
  await broken.els.chooseOneClick.fire('click');
  assert.equal(await broken.controller.submit(), false);
  assert.equal(broken.els.error.textContent, '生成失败，请重试。');
});

test('K2 dialog: a missing CSRF token sends nothing and a 401 returns to the login page', async () => {
  const h = harness({csrf: ''});
  await h.els.chooseOneClick.fire('click');
  assert.equal(await h.controller.submit(), false);
  assert.equal(h.calls.length, 0);
  const expired = harness({respond: () => ({status: 401, ok: false, json: async () => ({})})});
  await expired.els.chooseOneClick.fire('click');
  await expired.controller.submit();
  assert.equal(expired.unauthorized, 1);
});

test('K2 dialog: a double submit sends one request; back returns to the choice step', async () => {
  let release;
  const gate = new Promise(r => { release = r; });
  const h = harness({respond: async () => { await gate; return {status: 201, ok: true, json: async () => ({enrollment_id: 'e'.repeat(32), command: COMMAND, expires_at: T0 + 1800000})}; }});
  await h.els.chooseOneClick.fire('click');
  h.els.name.value = 'x';
  const first = h.controller.submit();
  assert.equal(await h.controller.submit(), false);
  release();
  await first;
  assert.equal(h.calls.length, 1);
  const g = harness();
  await g.els.chooseOneClick.fire('click');
  await g.els.oneclickBack.fire('click');
  assert.deepEqual(visible(g.els), ['choice']);
});

test('K2 dialog: the page wires the dialog, nothing is persisted, and the manual flow keeps its endpoint', () => {
  const read = file => fs.readFileSync(require.resolve('../web/static/' + file), 'utf8');
  const html = read('index.html'), app = read('app.js'), module_ = read('agent-enrollment.js');
  for (const id of ['add-agent-choice', 'add-agent-choose-oneclick', 'add-agent-choose-manual', 'oneclick-pane', 'oneclick-form', 'oneclick-name', 'oneclick-result', 'oneclick-command', 'oneclick-copy', 'oneclick-countdown', 'oneclick-revoke', 'oneclick-done', 'add-agent-create', 'add-agent-result']) {
    assert.ok(html.includes(`id="${id}"`), id);
  }
  assert.ok(html.indexOf('/agent-enrollment.js') > 0 && html.indexOf('/agent-enrollment.js') < html.indexOf('/app.js'));
  assert.ok(app.includes('serverBuild.one_click_install'), 'availability comes from the Server version endpoint');
  assert.ok(app.includes('AgentEnrollment.createController') && app.includes('enrollmentDialog.reset()'));
  assert.ok(app.includes("fetch('/api/v1/web/agents'"), 'manual install still uses the existing endpoint');
  assert.match(html, /请以 root 执行；非 root 用户先运行 sudo -i。需要该版本已发布到 GitHub。/);
  assert.doesNotMatch(COMMAND, /sudo|env/);
  assert.doesNotMatch(module_, /localStorage|sessionStorage|document\.cookie|indexedDB/);
  assert.match(html, /<p class="install-note">在目标 Linux 主机执行命令，然后在隐藏提示中粘贴下面的 enrollment value/, 'the manual instructions are unchanged');
});

test('N1: every failure reason has its own text; only a truly unknown one is generic', async () => {
  const cases = [
    [429, 'enrollment_pending_limit', /已有 10 条未使用的安装命令.*30 分钟/],
    [400, 'invalid_request', /名称无效/],
    [413, 'request_too_large', /名称无效/],
    [403, 'forbidden', /请求被拒绝/],
    [415, 'invalid_content_type', /请求被拒绝/],
    [500, 'internal_error', /Server 内部错误/],
    [502, '', /Server 内部错误/],
    [409, 'install_unavailable', /无法使用一键安装/],
    [429, '', /请求过于频繁/],
  ];
  for (const [status, code, pattern] of cases) {
    const h = harness({respond: () => ({status, ok: false, json: async () => (code ? {error: {code, message: '此构建无法使用一键安装'}} : {})})});
    await h.els.chooseOneClick.fire('click');
    h.els.name.value = 'x';
    assert.equal(await h.controller.submit(), false);
    assert.match(h.els.error.textContent, pattern, status + ' ' + code);
    assert.doesNotMatch(h.els.error.textContent, /^生成失败/);
    assert.equal(h.els.submit.disabled, false);
  }
  // 401 also returns to the login page.
  const expired = harness({respond: () => ({status: 401, ok: false, json: async () => ({error: {code: 'unauthorized'}})})});
  await expired.els.chooseOneClick.fire('click');
  await expired.controller.submit();
  assert.equal(expired.unauthorized, 1);
  // A page without a CSRF token explains it instead of staying silent.
  const anonymous = harness({csrf: ''});
  await anonymous.els.chooseOneClick.fire('click');
  assert.equal(await anonymous.controller.submit(), false);
  assert.match(anonymous.els.error.textContent, /未登录或登录已过期/);
  assert.ok(!anonymous.els.error.hidden);
  // Network failure and a truly unknown error.
  const offline = harness({respond: () => { throw new TypeError('Failed to fetch'); }});
  await offline.els.chooseOneClick.fire('click');
  await offline.controller.submit();
  assert.match(offline.els.error.textContent, /无法连接到 Server/);
  const odd = harness({respond: () => ({status: 418, ok: false, json: async () => ({error: {code: 'something_new'}})})});
  await odd.els.chooseOneClick.fire('click');
  await odd.controller.submit();
  assert.equal(odd.els.error.textContent, '生成失败，请重试。');
  const broken = harness({respond: () => { throw new Error('boom'); }});
  await broken.els.chooseOneClick.fire('click');
  await broken.controller.submit();
  assert.equal(broken.els.error.textContent, '生成失败，请重试。');
});

test('R38 name and method selection create no credentials before explicit next/generate',async()=>{
 const h=harness({frontChoice:true});h.els.choiceName.value='synthetic';await h.els.chooseManual.fire('click');assert.deepEqual(visible(h.els),['choice']);assert.equal(h.calls.length,0);await h.els.chooseOneClick.fire('click');await h.els.next.fire('click');assert.deepEqual(visible(h.els),['oneclick']);assert.equal(h.els.name.value,'synthetic');assert.equal(h.calls.length,0);await h.controller.submit();assert.equal(h.calls.length,1);
});
test('R38 late enrollment response after closing never restores secret or opens a result',async()=>{
 let resolve;const h=harness({respond:()=>new Promise(r=>resolve=r)});h.controller.chooseOneClick();h.els.name.value='synthetic';const task=h.controller.submit();h.controller.close();resolve({ok:true,status:201,json:async()=>({command:COMMAND,enrollment_id:'e',expires_at:T0+60000})});await task;assert.equal(h.els.command.textContent,'');assert.deepEqual(visible(h.els),['choice']);assert.equal(h.controller.hasCommand,false);
});
