'use strict';
const {test, before, after} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const M = require('../web/static/dashboard-model.js');
const StatusRow = require('../web/static/status-row.js');
const {launch, open, ID} = require('./lib/v11-panel-fixture.cjs');

const screens = process.env.V141_S3_GM_SCREENS;
const TAG_COLORS = [
  ['blue', '蓝'],
  ['sky', '天蓝'],
  ['cyan', '青'],
  ['teal', '青绿'],
  ['indigo', '靛'],
  ['purple', '紫'],
  ['pink', '粉'],
  ['gray', '灰'],
];

let browser;
before(async () => { browser = await launch(); });
after(async () => { await browser?.close(); });

const settle = page => page.waitForFunction(() => !renderFrame);
const cardOf = (page, i) => page.locator(`.agent-card[data-agent-id="${ID(i)}"]`);
const rowOf = (page, i) => page.locator(`.banner-row[data-agent-id="${ID(i)}"]`);

async function capture(page, name, options = {}) {
  if (!screens) return;
  fs.mkdirSync(screens, {recursive: true});
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({path: path.join(screens, name + '.png'), animations: 'disabled', ...options});
}

test('unit: hover reason formatting, remediation templates, unknown kind badges, and 8-color WCAG contrast', () => {
  assert.equal(StatusRow.formatHoverReason([], '⚠ 服务异常'), '⚠ 服务异常');
  assert.equal(StatusRow.formatHoverReason([{detail: 'exim4.service 处于 failed 状态'}], '⚠ 服务异常'), 'exim4.service 处于 failed 状态');
  assert.equal(
    StatusRow.formatHoverReason([
      {detail: 'exim4.service 处于 failed 状态'},
      {detail: 'backup.timer 处于 failed 状态'},
    ], '⚠ 服务异常'),
    'exim4.service 处于 failed 状态 等 2 项'
  );

  const svc = StatusRow.remediationFor('service', 'exim4.service');
  assert.deepEqual(
    svc.steps.flatMap(s => s.commands),
    [
      'systemctl status exim4.service --no-pager',
      'journalctl -u exim4.service -n 50 --no-pager',
      'systemctl reset-failed exim4.service',
      'systemctl disable --now exim4.service',
    ]
  );

  const restart = StatusRow.remediationFor('service_restart', 'worker.service');
  assert.deepEqual(
    restart.steps.flatMap(s => s.commands),
    ['journalctl -u worker.service -n 100 --no-pager', 'systemctl status worker.service --no-pager']
  );

  const cert = StatusRow.remediationFor('certificate', '/etc/tls/site.pem');
  assert.deepEqual(cert.steps.flatMap(s => s.commands), ['openssl x509 -noout -enddate -in /etc/tls/site.pem']);
  assert.equal(cert.note, '续期并替换这个文件；不再使用就删除它');

  const time = StatusRow.remediationFor('time', 'time');
  assert.deepEqual(
    time.steps.flatMap(s => s.commands),
    ['timedatectl status', 'systemctl restart systemd-timesyncd', 'systemctl restart chrony']
  );

  const dns = StatusRow.remediationFor('dns', '127.0.0.1');
  assert.deepEqual(
    dns.steps.flatMap(s => s.commands),
    ['dig @127.0.0.1 cloudflare.com +short', "ss -lunpt 'sport = :53'"]
  );
  assert.equal(dns.note, '重启该 DNS 服务（如 smartdns）后再观察');

  assert.equal(StatusRow.remediationFor('custom_unknown', 'foo'), null);

  // Shell-escaping for special characters in unit names and certificate paths.
  assert.equal(
    StatusRow.remediationFor('service', 'mnt-data\\x2dbackup.mount').steps[0].commands[0],
    "systemctl status 'mnt-data\\x2dbackup.mount' --no-pager"
  );
  assert.equal(
    StatusRow.remediationFor('service_restart', 'mnt-data\\x2dbackup.mount').steps[0].commands[0],
    "journalctl -u 'mnt-data\\x2dbackup.mount' -n 100 --no-pager"
  );
  assert.equal(
    StatusRow.remediationFor('certificate', '/etc/ssl/my certs/a.pem').steps[0].commands[0],
    "openssl x509 -noout -enddate -in '/etc/ssl/my certs/a.pem'"
  );
  assert.equal(
    StatusRow.remediationFor('certificate', '/etc/ssl/a;touch /tmp/pwn;.pem').steps[0].commands[0],
    "openssl x509 -noout -enddate -in '/etc/ssl/a;touch /tmp/pwn;.pem'"
  );
  assert.equal(
    StatusRow.remediationFor('certificate', '/etc/ssl/$(id).pem').steps[0].commands[0],
    "openssl x509 -noout -enddate -in '/etc/ssl/$(id).pem'"
  );
  assert.equal(
    StatusRow.remediationFor('certificate', "/etc/ssl/it's.pem").steps[0].commands[0],
    "openssl x509 -noout -enddate -in '/etc/ssl/it'\\''s.pem'"
  );

  const badges = M.systemBadges({
    online: true,
    system_attention: {
      dns: true,
      items: [
        {kind: 'dns', item: '127.0.0.1', level: 'danger', detail: '127.0.0.1 解析失败'},
        {kind: 'custom_fw', item: 'ufw', level: 'warning', detail: '防火墙规则异常'},
      ],
    },
  }, false);
  assert.deepEqual(badges, [['DNS 异常', 'danger'], ['custom_fw 异常', 'warning']]);

  // Verify 8 preset tag colors >= 4.5:1 in both light and dark modes using check-card-contrast math.
  const styleCSS = fs.readFileSync(path.join(__dirname, '..', 'web/static/style.css'), 'utf8');
  const declarations = s => Object.fromEntries([...s.matchAll(/(--[a-z0-9-]+):\s*([^;}\n]+)/g)].map(m => [m[1], m[2].trim()]));
  const [lightPart, rest] = styleCSS.split(':root[data-theme=dark]{');
  const darkPart = rest.slice(0, rest.indexOf('}'));
  const rgba = value => {
    if (value.startsWith('rgb')) {
      const a = value.match(/[\d.]+/g).map(Number);
      return [...a.slice(0, 3), a[3] ?? 1];
    }
    let h = value.slice(1);
    if (h.length < 5) h = [...h].map(c => c + c).join('');
    return [0, 2, 4].map(i => parseInt(h.slice(i, i + 2), 16)).concat(h.length === 8 ? parseInt(h.slice(6), 16) / 255 : 1);
  };
  const lin = c => (c /= 255) <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  const lum = c => { const [r, g, b] = c.map(lin); return 0.2126 * r + 0.7152 * g + 0.0722 * b; };
  const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
  const composite = (a, b) => a.slice(0, 3).map((v, i) => v * a[3] + b[i] * (1 - a[3]));

  for (const mode of ['light', 'dark']) {
    const vars = {...declarations(lightPart), ...(mode === 'dark' ? declarations(darkPart) : {})};
    const card = rgba(vars['--color-card']);
    const page = rgba(vars['--color-page']);
    for (const [key] of TAG_COLORS) {
      const fg = rgba(vars[`--tag-${key}-text`]);
      const bg = rgba(vars[`--tag-${key}-bg`]);
      const act = rgba(vars[`--tag-${key}-bg-active`]);
      assert(ratio(fg, card) >= 4.5, `${mode} ${key} text on card < 4.5`);
      assert(ratio(fg, composite(bg, card)) >= 4.5, `${mode} ${key} text on tinted card < 4.5`);
      assert(ratio(fg, composite(bg, page)) >= 4.5, `${mode} ${key} text on tinted page < 4.5`);
      assert(ratio(fg, composite(act, page)) >= 4.5, `${mode} ${key} text on active filter < 4.5`);
    }
  }
});

function buildScenario() {
  const now = Date.UTC(2026, 10, 27, 12, 0, 0);
  const HOUR = 3600000, DAY = 24 * HOUR;
  let tagCounter = 800;
  let failTagColorPatch = false;
  let failIgnorePut = false;
  const tagRequests = [];
  const ignoreRequests = [];
  const ignoredByAgent = new Map();

  const initialIssues = new Map([
    [ID(0), [
      {kind: 'service', item: 'exim4.service', level: 'danger', detail: 'exim4.service 处于 failed 状态'},
      {kind: 'service', item: 'backup.timer', level: 'danger', detail: 'backup.timer 处于 failed 状态'},
      {kind: 'service_restart', item: 'worker.service', level: 'danger', detail: '近 10 分钟重启 4 次'},
    ]],
    [ID(1), [
      {kind: 'certificate', item: '/etc/tls/site.pem', level: 'danger', detail: '/etc/tls/site.pem 已于 2026-11-20 到期'},
      {kind: 'time', item: 'time', level: 'warning', detail: 'NTP 未同步，时间偏差 +8.4 秒'},
    ]],
    [ID(2), [
      {kind: 'dns', item: '127.0.0.1', level: 'danger', detail: '127.0.0.1 解析 probe.example.com 失败：超时'},
      {kind: 'custom_fw', item: 'ufw-rule', level: 'warning', detail: '自定义检查：防火墙规则未生效'},
    ]],
    [ID(3), []],
  ]);

  function computeAttention(agentId) {
    const all = initialIssues.get(agentId) || [];
    const muted = ignoredByAgent.get(agentId) || new Set();
    const keyOf = it => `${it.kind}::${it.item}`;
    const items = all.filter(it => !muted.has(keyOf(it)));
    const ignored = all.filter(it => muted.has(keyOf(it)));
    const attention = {};
    for (const it of items) {
      if (it.kind === 'certificate' && attention.certificate !== 'danger') attention.certificate = it.level;
      if (it.kind === 'service') attention.service = true;
      if (it.kind === 'service_restart' && attention.service_restart !== 'danger') attention.service_restart = it.level;
      if (it.kind === 'time') attention.time = true;
      if (it.kind === 'dns') attention.dns = true;
    }
    if (items.length) attention.items = items;
    if (ignored.length) attention.ignored = ignored;
    return attention;
  }

  function seed(f) {
    f.state.tags = [
      {id: 'tag-blue', name: '生产核心', color: 'blue'},
      {id: 'tag-sky', name: '东京直连', color: 'sky'},
      {id: 'tag-cyan', name: 'BGP优化', color: 'cyan'},
      {id: 'tag-teal', name: '边缘节点', color: 'teal'},
      {id: 'tag-indigo', name: '内网中继', color: 'indigo'},
      {id: 'tag-purple', name: '流媒体解锁', color: 'purple'},
      {id: 'tag-pink', name: '高防线路', color: 'pink'},
      {id: 'tag-gray', name: '备用节点', color: 'gray'},
    ];
    f.state.revision = 10;
    const names = ['Tokyo-Core-01', 'Frankfurt-TLS-02', 'Singapore-DNS-03', 'LosAngeles-Net-04'];
    const countries = ['JP', 'DE', 'SG', 'US'];
    const tagSets = [
      ['tag-blue', 'tag-sky'],
      ['tag-cyan', 'tag-teal'],
      ['tag-indigo', 'tag-purple'],
      ['tag-pink', 'tag-gray'],
    ];
    for (const [i, a] of f.state.items.entries()) {
      a.name = names[i];
      a.country_code = countries[i];
      a.online = true;
      a.disabled_at = null;
      a.version = 'v1.4.1';
      a.server_local = i === 0;
      a.tag_ids = tagSets[i];
      a.state.host_health = null;
      a.startup_disk_failure = false;
      a.network = {known: true, active: i === 3, monitored: true};
      a.system_attention = computeAttention(a.agent_id);
    }
  }

  function makeHandle(getFixture) {
    return ({req, url, body, json, fail}) => {
      const f = getFixture();
      if (!f) return false;
      if (url.pathname === '/api/v1/web/tags' && req.method === 'POST') {
        tagRequests.push({method: 'POST', path: url.pathname, body});
        if (body?.expected_revision !== f.state.revision) { fail('revision_conflict', 409); return true; }
        const color = body.color || 'sky';
        const id = ID(tagCounter++);
        f.state.tags.push({id, name: body.name, color});
        f.state.revision++;
        json({revision: f.state.revision, id, changed_agent_ids: []}, 201);
        return true;
      }
      const tagMatch = /^\/api\/v1\/web\/tags\/([^/]+)$/.exec(url.pathname);
      if (tagMatch && req.method === 'PATCH') {
        tagRequests.push({method: 'PATCH', path: url.pathname, body});
        if (failTagColorPatch) {
          json({error: {code: 'internal_error', message: '标签颜色保存失败'}}, 500);
          return true;
        }
        if (body?.expected_revision !== f.state.revision) { fail('revision_conflict', 409); return true; }
        const tag = f.state.tags.find(t => t.id === tagMatch[1]);
        if (!tag) { fail('not_found', 404); return true; }
        if (body.name) tag.name = body.name;
        if (body.color) tag.color = body.color;
        f.state.revision++;
        json({revision: f.state.revision, id: tag.id, changed_agent_ids: f.state.items.map(a => a.agent_id)});
        return true;
      }
      const ignoreMatch = /^\/api\/v1\/web\/agents\/([a-f0-9]{32})\/system-check-ignores$/.exec(url.pathname);
      if (ignoreMatch && req.method === 'PUT') {
        const agentId = ignoreMatch[1];
        ignoreRequests.push({method: 'PUT', agentId, csrf: req.headers['x-csrf-token'], body});
        if (failIgnorePut) {
          json({error: {code: 'internal_error'}}, 500);
          return true;
        }
        if (!ignoredByAgent.has(agentId)) ignoredByAgent.set(agentId, new Set());
        ignoredByAgent.get(agentId).add(`${body.kind}::${body.item}`);
        const row = f.state.items.find(a => a.agent_id === agentId);
        if (row) row.system_attention = computeAttention(agentId);
        json({...body, ignored: true});
        return true;
      }
      if (url.pathname === '/api/v1/web/network-quality/fleet') {
        const id = url.searchParams.get('agent_id');
        const list = id ? f.state.items.filter(a => a.agent_id === id) : f.state.items;
        json({
          start: now - DAY, end: now, now,
          machines: list.map(a => ({
            agent_id: a.agent_id, name: a.name, server_local: a.server_local, country_code: a.country_code,
            status: 'online', last_seen: now - 5000, slow_threshold_ms: 500,
            alerts_30d: a.network?.active ? 1 : 0, current_alerts: a.network?.active ? 1 : 0,
            objects: [{slot: 'telecom', kind: 'builtin', region: '上海', isp: 'telecom', host: 'ct-sh.example.net', port: 443, slot_enabled: true, show_on_card: true}],
            lines: [{slot: 'telecom', family: 'ipv4', protocol: 'tcp', stats: {state: a.network?.active ? 'red' : 'green', samples: 6, p50_ms: 120, jitter_ms: 3, packet_loss_percent: a.network?.active ? 15 : 0}, evening_p50_ms: 140, latest_p50_ms: 120, latest_bucket_at: now - 60000, status: a.network?.active ? 'alert' : 'normal', spark: []}],
          })),
        });
        return true;
      }
      if (url.pathname.includes('/network-quality/range')) {
        json({slot: 'telecom', start: now - DAY, end: now, resolution_ms: 300000, points: [], target_changes: [], gaps: []});
        return true;
      }
      if (url.pathname.includes('/network-quality/alerts')) {
        json([]);
        return true;
      }
      if (url.pathname.includes('/network-quality/heatmap')) {
        json({slot: 'telecom', timezone: 'Asia/Shanghai', days: []});
        return true;
      }
      return false;
    };
  }

  return {
    seed,
    makeHandle,
    tagRequests,
    ignoreRequests,
    setFailTagColorPatch(v) { failTagColorPatch = v; },
    setFailIgnorePut(v) { failIgnorePut = v; },
  };
}

for (const theme of ['dark', 'light']) {
  test(`${theme}: colored tags, manage-tags color picker, fault badge hover/popover/copy/ignore/network/unknown`, {timeout: 240000}, async () => {
    const scenario = buildScenario();
    let panel;
    panel = await open(browser, {
      width: 1560, height: 1020, n: 4, theme,
      handle: scenario.makeHandle(() => panel?.f),
    });
    try {
      const {page, f} = panel;
      page.setDefaultTimeout(12000);
      scenario.seed(f);
      await page.evaluate(() => dashboardData.reload());
      await page.waitForFunction(() => document.querySelectorAll('.agent-card').length === 4 && !renderFrame);
      await cardOf(page, 0).locator('.status-tag[data-tag-color="blue"]').waitFor();
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));

      // 1. Verify colored tags on top filter bar, cards, and list view.
      for (const [key, label] of TAG_COLORS) {
        const barBtn = page.locator(`#dashboard-tags .dashboard-option[data-tag-color="${key}"]`);
        assert.equal(await barBtn.count(), 1, `top bar should have tag with color ${key} (${label})`);
      }
      const blueBar = page.locator('#dashboard-tags .dashboard-option[data-tag-color="blue"]');
      const barBgBefore = await blueBar.evaluate(n => getComputedStyle(n).backgroundColor);
      await blueBar.click();
      await settle(page);
      assert.equal(await blueBar.getAttribute('aria-pressed'), 'true');
      const barBgSelected = await blueBar.evaluate(n => getComputedStyle(n).backgroundColor);
      assert.notEqual(barBgSelected, barBgBefore, 'selected tag filter should deepen background');
      await blueBar.click();
      await settle(page);

      // Verify cards show colored tags with 4px radius and colored styles.
      const cardTagStyles = await cardOf(page, 0).evaluate(card => {
        const blue = card.querySelector('.status-tag[data-tag-color="blue"]');
        const sky = card.querySelector('.status-tag[data-tag-color="sky"]');
        const bs = getComputedStyle(blue);
        const ss = getComputedStyle(sky);
        return {
          blueRadius: bs.borderTopLeftRadius,
          blueColor: bs.color,
          blueBg: bs.backgroundColor,
          skyColor: ss.color,
        };
      });
      assert.equal(cardTagStyles.blueRadius, '4px');
      assert.notEqual(cardTagStyles.blueColor, cardTagStyles.skyColor);
      assert.notEqual(cardTagStyles.blueBg, 'rgba(0, 0, 0, 0)');

      // Screenshot: cards with colored tags + fault badges.
      await page.mouse.move(0, 0);
      await capture(page, `${theme}-tags-cards`);

      // Switch to List View, widen Device column so colored tags & fault badges are both visible, capture list screenshot.
      await page.locator('[data-view=list]').click();
      await settle(page);
      await rowOf(page, 0).locator('.status-tag[data-tag-color="blue"]').waitFor({state: 'attached'});
      const nameResizer = page.locator('.banner-header [data-column=name] .banner-col-resizer');
      const rBox = await nameResizer.boundingBox();
      await page.mouse.move(rBox.x + rBox.width / 2, rBox.y + rBox.height / 2);
      await page.mouse.down();
      await page.mouse.move(rBox.x + rBox.width / 2 + 180, rBox.y + rBox.height / 2, {steps: 6});
      await page.mouse.up();
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
      await rowOf(page, 0).locator('.status-tag[data-tag-color="blue"]').waitFor({state: 'visible'});
      assert.equal(await rowOf(page, 1).locator('.status-tag[data-tag-color="cyan"]').isVisible(), true);
      assert.equal(await rowOf(page, 3).locator('.status-tag[data-tag-color="pink"]').isVisible(), true);
      await page.mouse.move(0, 0);
      await capture(page, `${theme}-tags-list`);

      // Clicking a fault badge in List View opens fault popover without opening device details dialog.
      const listNetBadge = rowOf(page, 3).locator('.status-fault[data-fault="network"]');
      if (await listNetBadge.isVisible()) {
        await listNetBadge.click();
        await page.locator('.status-fault-popover').waitFor();
        assert.equal(await page.locator('#device-details[open]').count(), 0);
        await page.keyboard.press('Escape');
        await page.locator('.status-fault-popover').waitFor({state: 'detached'});
      }

      // Return to Cards View.
      await page.locator('[data-view=cards]').click();
      await settle(page);
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));

      // 2. Manage Tags: color picker, Chinese hover titles, immediate PATCH save, failure rollback, create with/without color.
      await page.locator('#manage-tags').click();
      const manageDlg = page.locator('#organization-manage-dialog[open]');
      await manageDlg.waitFor();
      const prodRow = manageDlg.locator('.organization-manage-row[data-organization-id="tag-blue"]');
      const prodTrigger = prodRow.locator('.tag-color-trigger');
      assert.equal(await prodTrigger.getAttribute('data-tag-color'), 'blue');

      await prodTrigger.click();
      const palette = prodRow.locator('.tag-color-palette');
      await palette.waitFor();
      const swatchTitles = await palette.locator('.tag-color-swatch').evaluateAll(ns => ns.map(n => [n.dataset.color, n.title]));
      assert.deepEqual(swatchTitles, TAG_COLORS);

      // Screenshot: manage tags with 8-color picker open.
      await capture(page, `${theme}-manage-tags-picker`);

      // Save success: change tag-blue from blue -> purple.
      await palette.locator('.tag-color-swatch[data-color="purple"]').click();
      await page.waitForFunction(() => {
        const btn = document.querySelector('#organization-manage-dialog .organization-manage-row[data-organization-id="tag-blue"] .tag-color-trigger');
        return btn && btn.dataset.tagColor === 'purple' && !btn.disabled && !renderFrame;
      });
      const lastPatch = scenario.tagRequests.at(-1);
      assert.equal(lastPatch.method, 'PATCH');
      assert.equal(lastPatch.path, '/api/v1/web/tags/tag-blue');
      assert.equal(lastPatch.body.color, 'purple');
      assert.equal(typeof lastPatch.body.expected_revision, 'number');
      // Card and top bar updated too.
      await cardOf(page, 0).locator('.status-tag[data-tag-color="purple"]', {hasText: '生产核心'}).waitFor({state: 'attached'});
      assert.equal(await cardOf(page, 0).locator('.status-tag[data-tag-color="purple"]', {hasText: '生产核心'}).count(), 1);
      assert.equal(await page.locator('#dashboard-tags .dashboard-option[data-tag-color="purple"]', {hasText: '生产核心'}).count(), 1);

      // Save failure: revert back to purple and show inline error.
      scenario.setFailTagColorPatch(true);
      await prodRow.locator('.tag-color-trigger').click();
      await prodRow.locator('.tag-color-swatch[data-color="pink"]').click();
      await prodRow.locator('.tag-color-error').waitFor();
      assert.equal(await prodRow.locator('.tag-color-trigger').getAttribute('data-tag-color'), 'purple');
      scenario.setFailTagColorPatch(false);

      // Create new tag WITH chosen color (teal).
      await manageDlg.locator('.organization-create-row .tag-color-trigger').click();
      await manageDlg.locator('.organization-create-row .tag-color-swatch[data-color="teal"]').click();
      await manageDlg.locator('#organization-create-name').fill('自选青绿');
      await manageDlg.locator('.organization-create-button').click();
      await manageDlg.locator('.organization-manage-row', {hasText: '自选青绿'}).waitFor();
      const postWithColor = scenario.tagRequests.at(-1);
      assert.equal(postWithColor.method, 'POST');
      assert.deepEqual({name: postWithColor.body.name, color: postWithColor.body.color}, {name: '自选青绿', color: 'teal'});

      // Create new tag WITHOUT color (omits color, uses backend default).
      await manageDlg.locator('#organization-create-name').fill('默认颜色');
      await manageDlg.locator('.organization-create-button').click();
      await manageDlg.locator('.organization-manage-row', {hasText: '默认颜色'}).waitFor();
      const postDefault = scenario.tagRequests.at(-1);
      assert.equal(postDefault.method, 'POST');
      assert.equal(postDefault.body.name, '默认颜色');
      assert.equal('color' in postDefault.body, false);

      // Close Manage Tags dialog.
      await manageDlg.getByRole('button', {name: '关闭'}).click();
      await manageDlg.waitFor({state: 'detached'});

      // 3. Hover reason: multi-item ("等 N 项") & single-item.
      const svcBadge = cardOf(page, 0).locator('.status-fault[data-fault-kind="service"]');
      await svcBadge.hover();
      const tip = page.locator('.status-fault-tip');
      await tip.waitFor();
      assert.equal(await tip.textContent(), 'exim4.service 处于 failed 状态 等 2 项');

      // Screenshot: hover reason tooltip on card 0.
      const c0Box = await cardOf(page, 0).boundingBox();
      const tipBox = await tip.boundingBox();
      const clipX = Math.max(0, Math.floor(Math.min(c0Box.x, tipBox.x)) - 10);
      const clipY = Math.max(0, Math.floor(Math.min(c0Box.y, tipBox.y)) - 10);
      const clipW = Math.ceil(Math.max(c0Box.x + c0Box.width, tipBox.x + tipBox.width) - clipX) + 10;
      const clipH = Math.ceil(Math.max(c0Box.y + 130, tipBox.y + tipBox.height) - clipY) + 10;
      await capture(page, `${theme}-hover-reason`, {clip: {x: clipX, y: clipY, width: clipW, height: clipH}});

      const dnsBadge = cardOf(page, 2).locator('.status-fault[data-fault-kind="dns"]');
      await dnsBadge.hover();
      await tip.waitFor();
      assert.equal(await tip.textContent(), '127.0.0.1 解析 probe.example.com 失败：超时');
      await page.mouse.move(0, 0);

      // 4. Service fault popover: title, commands, copy button (1.5s), ignore failure, ignore partial & last item.
      await svcBadge.click();
      const faultPop = page.locator('.status-fault-popover');
      await faultPop.waitFor();
      assert.equal(await faultPop.locator('.status-fault-popover-title').textContent(), '服务异常');
      assert.equal(await faultPop.locator('.status-fault-popover-device').textContent(), 'Tokyo-Core-01');
      assert.equal(await faultPop.locator('.fault-popover-item').count(), 2);
      const eximCard = faultPop.locator('.fault-popover-item[data-item="exim4.service"]');
      assert.equal(await eximCard.locator('.fault-item-detail').textContent(), 'exim4.service 处于 failed 状态');
      const eximCmds = await eximCard.locator('.fault-cmd-row code').allTextContents();
      assert.deepEqual(eximCmds, [
        'systemctl status exim4.service --no-pager',
        'journalctl -u exim4.service -n 50 --no-pager',
        'systemctl reset-failed exim4.service',
        'systemctl disable --now exim4.service',
      ]);
      assert.equal(
        await faultPop.locator('.fault-popover-footer').textContent(),
        '忽略后这一项不再提醒，可在 详情 → 系统 里取消。'
      );

      // Screenshot: service fault popover.
      await capture(page, `${theme}-fault-service`);

      // Copy button changes to 已复制, matches rendered <code> text exactly, and reverts after 1.5s.
      const firstCodeText = await eximCard.locator('.fault-cmd-row code').first().textContent();
      const firstCopyBtn = eximCard.locator('.fault-cmd-copy').first();
      await firstCopyBtn.click();
      await page.waitForFunction(
        () => document.querySelector('.status-fault-popover .fault-popover-item[data-item="exim4.service"] .fault-cmd-copy')?.textContent === '已复制',
        null,
        {timeout: 3000}
      );
      const copiedText = await page.evaluate(() => navigator.clipboard.readText());
      assert.equal(copiedText, firstCodeText);
      assert.equal(copiedText, 'systemctl status exim4.service --no-pager');
      await page.waitForFunction(
        () => document.querySelector('.status-fault-popover .fault-popover-item[data-item="exim4.service"] .fault-cmd-copy')?.textContent === '复制',
        null,
        {timeout: 4000}
      );

      // Ignore failure shows inline error and preserves item.
      scenario.setFailIgnorePut(true);
      await eximCard.locator('.fault-ignore-btn').click();
      await eximCard.locator('.fault-item-error').filter({hasText: '忽略失败'}).waitFor();
      assert.equal(await faultPop.locator('.fault-popover-item').count(), 2);
      scenario.setFailIgnorePut(false);

      // Ignore 1st item: disappears from open popover, 2nd remains, badge stays visible.
      await eximCard.locator('.fault-ignore-btn').click();
      await eximCard.waitFor({state: 'detached'});
      assert.equal(await faultPop.isVisible(), true);
      assert.equal(await faultPop.locator('.fault-popover-item').count(), 1);
      assert.equal(await cardOf(page, 0).locator('.status-fault[data-fault-kind="service"]').count(), 1);
      const req1 = scenario.ignoreRequests.at(-1);
      assert.deepEqual(req1, {method: 'PUT', agentId: ID(0), csrf: 'synthetic-csrf', body: {kind: 'service', item: 'exim4.service'}});

      // Ignore 2nd (last) item: popover closes and badge disappears.
      await faultPop.locator('.fault-popover-item[data-item="backup.timer"] .fault-ignore-btn').click();
      await faultPop.waitFor({state: 'detached'});
      await cardOf(page, 0).locator('.status-fault[data-fault-kind="service"]').waitFor({state: 'detached'});

      // 5. Certificate fault popover + screenshot.
      const certBadge = cardOf(page, 1).locator('.status-fault[data-fault-kind="certificate"]');
      await certBadge.click();
      await faultPop.waitFor();
      assert.equal(await faultPop.locator('.status-fault-popover-title').textContent(), '证书已过期');
      assert.equal(await faultPop.locator('.status-fault-popover-device').textContent(), 'Frankfurt-TLS-02');
      assert.deepEqual(await faultPop.locator('.fault-cmd-row code').allTextContents(), [
        'openssl x509 -noout -enddate -in /etc/tls/site.pem',
      ]);
      assert.match(await faultPop.textContent(), /续期并替换这个文件；不再使用就删除它/);
      await capture(page, `${theme}-fault-certificate`);
      await page.keyboard.press('Escape');
      await faultPop.waitFor({state: 'detached'});

      // Verify shell-escaped certificate path renders identically in <code> and clipboard.
      const origCertAttention = structuredClone(f.state.items[1].system_attention);
      f.state.items[1].system_attention = {
        ...origCertAttention,
        certificate: 'danger',
        items: [
          {kind: 'certificate', item: "/etc/ssl/my certs/it's.pem", level: 'danger', detail: "/etc/ssl/my certs/it's.pem 已于 2026-11-20 到期"},
          ...(origCertAttention.items || []).filter(i => i.kind !== 'certificate'),
        ],
      };
      await page.evaluate(() => dashboardData.reload());
      await settle(page);
      await cardOf(page, 1).locator('.status-fault[data-fault-kind="certificate"]').click();
      await faultPop.waitFor();
      const escapedCode = await faultPop.locator('.fault-cmd-row code').first().textContent();
      assert.equal(escapedCode, "openssl x509 -noout -enddate -in '/etc/ssl/my certs/it'\\''s.pem'");
      await faultPop.locator('.fault-cmd-copy').first().click();
      await page.waitForFunction(
        () => document.querySelector('.status-fault-popover .fault-cmd-copy')?.textContent === '已复制',
        null,
        {timeout: 3000}
      );
      assert.equal(await page.evaluate(() => navigator.clipboard.readText()), escapedCode);
      await page.keyboard.press('Escape');
      await faultPop.waitFor({state: 'detached'});
      f.state.items[1].system_attention = origCertAttention;
      await page.evaluate(() => dashboardData.reload());
      await settle(page);

      // 6. Time & DNS popovers check.
      await cardOf(page, 1).locator('.status-fault[data-fault-kind="time"]').click();
      await faultPop.waitFor();
      assert.deepEqual(await faultPop.locator('.fault-cmd-row code').allTextContents(), [
        'timedatectl status',
        'systemctl restart systemd-timesyncd',
        'systemctl restart chrony',
      ]);
      await page.keyboard.press('Escape');
      await faultPop.waitFor({state: 'detached'});

      await cardOf(page, 2).locator('.status-fault[data-fault-kind="dns"]').click();
      await faultPop.waitFor();
      assert.deepEqual(await faultPop.locator('.fault-cmd-row code').allTextContents(), [
        'dig @127.0.0.1 cloudflare.com +short',
        "ss -lunpt 'sport = :53'",
      ]);
      assert.match(await faultPop.textContent(), /重启该 DNS 服务（如 smartdns）后再观察/);
      await page.keyboard.press('Escape');
      await faultPop.waitFor({state: 'detached'});

      // 7. Unknown kind: shows detail only, no commands, still ignorable.
      const unknownBadge = cardOf(page, 2).locator('.status-fault[data-fault-kind="custom_fw"]');
      assert.equal(await unknownBadge.textContent(), '⚠ custom_fw 异常');
      await unknownBadge.click();
      await faultPop.waitFor();
      assert.equal(await faultPop.locator('.fault-item-detail').textContent(), '自定义检查：防火墙规则未生效');
      assert.equal(await faultPop.locator('.fault-cmd-row').count(), 0);
      assert.equal(await faultPop.locator('.fault-item-remediation').count(), 0);
      await faultPop.locator('.fault-ignore-btn').click();
      await faultPop.waitFor({state: 'detached'});
      await unknownBadge.waitFor({state: 'detached'});

      // 8. Network fault popover: no ignore button, no ignore footer, jumps to #network/<id>.
      const netBadge = cardOf(page, 3).locator('.status-fault[data-fault="network"]');
      await netBadge.click();
      await faultPop.waitFor();
      assert.equal(await faultPop.locator('.status-fault-popover-title').textContent(), '网络异常');
      assert.equal(await faultPop.locator('.status-fault-popover-device').textContent(), 'LosAngeles-Net-04');
      assert.equal(await faultPop.locator('.fault-network-desc').textContent(), '这台机器有线路在持续丢包或延迟超过门槛');
      assert.equal(await faultPop.locator('.fault-ignore-btn').count(), 0);
      assert.equal(await faultPop.locator('.fault-popover-footer').count(), 0);
      await capture(page, `${theme}-fault-network`);

      await faultPop.locator('.fault-network-action', {hasText: '查看网络状态'}).click();
      await faultPop.waitFor({state: 'detached'});
      await page.waitForFunction(id => location.hash === '#network/' + id && !document.getElementById('network-placeholder').hidden, ID(3));

      assert.deepEqual(panel.errors, []);
    } finally {
      await panel.close();
    }
  });
}
