const {test} = require('node:test');
const assert = require('node:assert/strict');
const {spawn, spawnSync} = require('node:child_process');
const {randomBytes, createHash} = require('node:crypto');
const {once} = require('node:events');
const fs = require('node:fs');
const http = require('node:http');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const {setTimeout: wait} = require('node:timers/promises');

const {chromium} = require(process.env.M02_PLAYWRIGHT);
const binary = process.env.R45_SERVER_BINARY;
const screens = process.env.V140_S4B_SCREENS;
const donor = process.env.R45_DONOR_FIXTURE;
const adminPassword = 'Synthetic R45 fixture password';
const access = 'synthetic-r45-access';
const secret = 'synthetic-r45-secret';
const metadata = 'x-amz-meta-404-probe-archive-sha256';
const hash = data => createHash('sha256').update(data).digest('hex');

function run(args, options = {}) {
  const child = spawnSync(binary, args, {encoding: 'utf8', windowsHide: true, ...options});
  assert.equal(child.status, 0, child.error?.message || child.stderr);
  return child.stdout;
}

async function listen(server) {
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  return server.address().port;
}

async function fixture() {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'probe-r45-'));
  const database = path.join(directory, '404-probe.db');
  const objects = new Map();
  let uploading = false;
  const remote = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1');
    const key = decodeURIComponent(url.pathname.slice('/fixture/'.length));
    if (!req.headers.authorization?.includes(`Credential=${access}/`)) {
      res.writeHead(403).end(`Untrusted R2 error with ${secret}`);
    } else if (!url.pathname.startsWith('/fixture/') && url.pathname !== '/fixture') {
      res.writeHead(404).end();
    } else if (req.method === 'PUT') {
      const parts = [];
      for await (const part of req) parts.push(part);
      const body = Buffer.concat(parts);
      if (req.headers['x-amz-content-sha256'] !== hash(body)) {
        res.writeHead(400).end();
        return;
      }
      if (req.headers[metadata]) {
        uploading = true;
        await wait(500);
      }
      objects.set(key, {body, metadata: req.headers[metadata], etag: `"${hash(body)}"`});
      res.writeHead(200).end();
    } else if (req.method === 'GET' && url.pathname === '/fixture') {
      const items = [...objects].filter(([name]) => name.startsWith(url.searchParams.get('prefix')));
      res.writeHead(200, {'content-type': 'application/xml'}).end('<ListBucketResult>' +
        items.map(([name, o]) => `<Contents><Key>${name}</Key><ETag>${o.etag}</ETag></Contents>`).join('') + '</ListBucketResult>');
    } else if (req.method === 'HEAD' && objects.has(key)) {
      const object = objects.get(key);
      const headers = {etag: object.etag};
      if (object.metadata) headers[metadata] = object.metadata;
      res.writeHead(200, headers).end();
    } else if (req.method === 'DELETE') {
      objects.delete(key);
      res.writeHead(204).end();
    } else {
      res.writeHead(404).end();
    }
  });
  let child;
  const cleanup = async () => {
    if (child && child.exitCode === null) {
      const closed = once(child, 'exit');
      child.kill();
      await closed;
    }
    await new Promise(resolve => remote.close(resolve));
    fs.rmSync(directory, {recursive: true, force: true});
  };
  try {
    const remotePort = await listen(remote);
    const portServer = net.createServer();
    const port = await listen(portServer);
    await new Promise(resolve => portServer.close(resolve));
    const origin = `http://127.0.0.1:${port}`;
    const password = path.join(directory, 'web-password.hash');
    const tokenPath = path.join(directory, 'control.token');
    fs.writeFileSync(password, run(['web', 'password-hash'], {input: adminPassword + '\n'}), {mode: 0o600});
    fs.writeFileSync(tokenPath, randomBytes(32).toString('base64url') + '\n', {mode: 0o600});
    fs.writeFileSync(path.join(directory, 'server.env'), `PROBE_404_WEB_PUBLIC_ORIGIN=${origin}\n`, {mode: 0o600});
    fs.writeFileSync(path.join(directory, 'update-check.json'), '{"enabled":false}\n', {mode: 0o600});
    const agent = run(['agent', 'add', 'R45 Donor Device', '--db', database]);
    const id = /^Agent ID: ([a-f0-9]+)$/m.exec(agent)[1];
    const agentToken = /^Token: (\S+)$/m.exec(agent)[1];
    let sequence = 0;
    function report() {
      return fetch(`${origin}/api/v1/report`, {
        method: 'POST', headers: {'content-type': 'application/json', authorization: `Bearer ${agentToken}`},
        body: JSON.stringify({agent_id: id, epoch: 1, session_id: 'r45-session', sequence: ++sequence, collected_at: Date.now(),
          hostname: 'restore-donor', os: 'linux', arch: 'amd64', boot_id: 'r45-boot', uptime: 600, cpu_percent: 25,
          ram_used: 30, ram_total: 100, disk_used: 30, disk_total: 100, rx_bytes: sequence*1024, tx_bytes: sequence*2048})
      });
    }
    let logs = '';
    child = spawn(binary, ['serve', '--listen', `127.0.0.1:${port}`, '--db', database, '--control-token-file', tokenPath,
      '--web-password-hash-file', password, '--web-public-origin', origin, '--web-allow-insecure-http'], {windowsHide: true});
    for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => logs += chunk);
    for (let attempts = 0; !logs.includes('server listening'); attempts++) {
      assert(attempts < 300 && child.exitCode === null, 'synthetic donor did not start');
      await wait(25);
    }
    const first = await report();
    await first.arrayBuffer();
    assert.equal(first.status, 200);
    return {origin, remotePort, objects, report, uploading: () => uploading, logs: () => logs, cleanup};
  } catch (error) {
    await cleanup();
    throw error;
  }
}

test('R45 private key, backup operations, report continuity and failure notice in both themes', {timeout: 360000}, async () => {
  const server = await fixture();
  const browser = await chromium.launch({headless: true, executablePath: process.env.M02_CHROMIUM});
  try {
    const context = await browser.newContext({viewport: {width: 1440, height: 950}, locale: 'zh-CN', acceptDownloads: true});
    const page = await context.newPage();
    const pageErrors = [];
    page.on('pageerror', err => pageErrors.push(err.message));
    const credentials = [access, secret];
    const responseChecks = [];
    page.on('response', response => {
      if (response.url().includes('/api/v1/web/settings/backup')) {
        responseChecks.push(response.text().then(body => {
          for (const credential of credentials) assert(!body.includes(credential), 'an API response exposed a secret');
        }));
      }
    });
    async function shot(name, key = false) {
      if (!screens) return;
      fs.mkdirSync(screens, {recursive: true});
      for (const theme of ['dark', 'light']) {
        await page.evaluate(theme => ProbeTheme.set(theme), theme);
        await page.screenshot({path: path.join(screens, `${theme}-${name}.png`), mask: key ? [page.locator('#backup-private')] : [],
          maskColor: theme === 'dark' ? '#444b57' : '#c6ced8'});
      }
    }
    await page.goto(`${server.origin}/login`);
    await page.locator('#password').fill(adminPassword);
    await page.getByRole('button', {name: '登录', exact: true}).click();
    await page.waitForURL(`${server.origin}/`);
    assert.deepEqual(await page.evaluate(async () => {
      const session = await fetch('/api/v1/web/session').then(r => r.json());
      const update = await fetch('/api/v1/web/settings/youtube-ad-free-regions', {
        method: 'PUT', headers: {'content-type': 'application/json', 'x-csrf-token': session.csrf_token},
        body: JSON.stringify({regions: ['CN', 'MO']})
      });
      return (await update.json()).regions;
    }), ['CN', 'MO']);
    if (!await page.locator('#dashboard-settings').evaluate(details => details.open)) {
      await page.locator('#dashboard-settings > summary').click();
    }
    await page.getByRole('button', {name: '备份', exact: true}).click();
    await page.waitForFunction(() => !document.querySelector('#backup-fields').disabled);
    await shot('disabled');
    await page.locator('#backup-endpoint').fill(`http://127.0.0.1:${server.remotePort}`);
    await page.locator('#backup-bucket').fill('fixture');
    await page.locator('#backup-prefix').fill('r45-fixture');
    await page.locator('#backup-access').fill(access);
    await page.locator('#backup-secret').fill(secret);
    await page.locator('#backup-test').click();
    await page.getByText('连接成功，测试对象已删除。', {exact: true}).waitFor();
    assert.equal(server.objects.size, 0);
    await page.locator('#backup-enabled').check();
    await page.locator('#backup-private').waitFor();
    const identity = await page.locator('#backup-private').textContent();
    assert(/^AGE-SECRET-KEY-1[A-Z0-9]+$/.test(identity));
    credentials.push(identity);
    assert(await page.locator('#backup-save').isDisabled());
    await shot('first-key', true);
    const downloadPromise = page.waitForEvent('download');
    await page.locator('#backup-download').click();
    const download = await downloadPromise;
    assert.equal(fs.readFileSync(await download.path(), 'utf8').trim(), identity);
    assert(await page.locator('#backup-download').isDisabled());
    await page.locator('#backup-key-saved').check();
    await page.locator('#backup-save').click();
    await page.getByText('已保存。', {exact: true}).waitFor();
    assert.equal(await page.locator('#backup-private').textContent(), '');
    assert.equal(await page.locator('#backup-access').inputValue(), '');
    assert.equal(await page.locator('#backup-secret').inputValue(), '');
    const source = await (await page.request.get(`${server.origin}/`)).text();
    for (const credential of credentials) assert(!source.includes(credential), 'page source exposed a credential');

    await page.locator('#backup-run').click();
    for (let i = 0; !server.uploading(); i++) {
      assert(i < 300, 'backup did not start streaming');
      const report = await server.report();
      await report.arrayBuffer();
      assert.equal(report.status, 200);
      await wait(10);
    }
    assert.equal(await page.evaluate(async () => (await fetch('/api/v1/web/dashboard')).status), 200);
    for (let i = 0; i < 4; i++) {
      const report = await server.report();
      await report.arrayBuffer();
      assert.equal(report.status, 200);
    }
    await page.waitForFunction(() => document.querySelector('#backup-count').textContent === '1');
    await shot('success');
    if (donor) {
      fs.mkdirSync(donor, {recursive: true});
      const [object] = server.objects.values();
      fs.writeFileSync(path.join(donor, 'donor.tar.zst.age'), object.body, {mode: 0o600});
      fs.writeFileSync(path.join(donor, 'donor.agekey'), identity + '\n', {mode: 0o600});
    }

    await page.locator('#backup-access').fill('wrong-fixture-access');
    await page.locator('#backup-secret').fill('wrong-fixture-secret');
    await page.locator('#backup-save').click();
    await page.getByText('已保存。', {exact: true}).waitFor();
    await page.locator('#backup-run').click();
    const alert = page.getByRole('link', {name: '备份失败', exact: true});
    await alert.waitFor();
    assert.match(await page.locator('#backup-last-failure').textContent(), /credentials or permissions/);
    await shot('failure-settings');
    await page.locator('#dashboard-all').click();
    await shot('failure-footer');
    await alert.click();
    await page.locator('#backup').waitFor();
    assert.equal(new URL(page.url()).hash, '#backup');
    await page.locator('#backup-access').fill(access);
    await page.locator('#backup-secret').fill(secret);
    await page.locator('#backup-save').click();
    await page.getByText('已保存。', {exact: true}).waitFor();
    await page.locator('#backup-run').click();
    await alert.waitFor({state: 'hidden'});
    await page.locator('#backup-rekey').click();
    await page.locator('#backup-private').waitFor();
    const rotated = await page.locator('#backup-private').textContent();
    assert(/^AGE-SECRET-KEY-1[A-Z0-9]+$/.test(rotated) && rotated !== identity);
    credentials.push(rotated);
    assert(await page.locator('#backup-save').isDisabled());
    await page.locator('#backup-key-saved').check();
    await page.locator('#backup-save').click();
    await page.getByText('已保存。', {exact: true}).waitFor();
    assert.equal(await page.locator('#backup-private').textContent(), '');
    await Promise.all(responseChecks);
    for (const credential of credentials) assert(!server.logs().includes(credential), 'Server logs exposed a credential');
    assert.deepEqual(pageErrors, []);
  } finally {
    await browser.close();
    await server.cleanup();
  }
});
