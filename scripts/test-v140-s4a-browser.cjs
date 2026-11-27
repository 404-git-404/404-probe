
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {test} = require('node:test');
const {launch, open, ID} = require('./lib/v11-panel-fixture.cjs');

const screenshots = process.env.V140_S4A_SCREENS;
const s1Screens = process.env.V141_S1_SCREENSHOTS;
const day = 86400000;
const choose = (page, view) => page.locator('[data-view=' + view + ']').click();

async function capture(page, theme, scene) {
  if (!screenshots) return;
  fs.mkdirSync(screenshots, {recursive: true});
  await page.mouse.move(0, 0);
  await page.screenshot({path: path.join(screenshots, theme + '-' + scene + '.png'), animations: 'disabled'});
}

async function captureReboot(section, name) {
  if (!s1Screens) return;
  fs.mkdirSync(s1Screens, {recursive: true});
  await section.page().evaluate(() => document.fonts.ready);
  await section.screenshot({path: path.join(s1Screens, name + '.png'), animations: 'disabled'});
}

for (const theme of ['dark', 'light']) {
  test(theme + ' system checks, settings, fail2ban and dashboard filters', {timeout: 240000}, async () => {
    const browser = await launch();
    let panel;
    try {
      panel = await open(browser, {
        n: 8, theme, width: 1720, height: 1120,
        handle: ({req, url, body, json}) => {
          if (req.method !== 'PUT' || url.pathname !== '/api/v1/web/agents/' + ID(0) + '/system-checks') return false;
          assert.equal(req.headers['x-csrf-token'], 'synthetic-csrf');
          panel.f.state.items[0].system_check_settings = body;
          json(body);
          return true;
        },
      });
      const {page, f} = panel;
      const now = Date.now();
      for (const [i, row] of f.state.items.entries()) {
        row.name = 'Synthetic-' + String(i + 1).padStart(3, '0');
        row.online = true;
        row.disabled_at = null;
        row.network.active = false;
        row.group_id = null;
        row.tag_ids = [];
        row.system_attention = [{certificate: 'warning'}, {}, {service: true}, {}, {time: true}, {}, {}, {}][i];
        row.server_local = i === 7;
        row.system_check_settings = {certificate_directories: ['/opt/certs'], watched_services: ['nginx', 'sing-box']};
        row.system_checks = {
          supported: true, root_status: 'disabled', unreadable_count: 1, unreadable: ['/etc/letsencrypt/live'],
          certificates: [
            {domains: 'node-a.example.test', expires_at: now + 5 * day, path: '/etc/tls/fullchain.pem'},
            {domains: 'panel.example.test', expires_at: now + 57 * day, path: '/opt/certs/panel.crt'},
          ],
          service_status: 'ok', services: [{name: 'nginx', state: 'inactive'}, {name: 'sing-box', state: 'active'}],
          failed_units: [], ntp_status: 'ok', ntp_synchronized: true, time_offset_ms: 200,
          reboot_status: 'ok', reboot_required: true, debian_major: 13, virtualization: 'kvm',
          packages: {status: 'disabled'}, fail2ban: {status: 'disabled'},
        };
        row.debian_stable_major = 13;
      }
      const checks = f.state.items[0].system_checks;
      checks.reboot_packages = ['linux-image-6.12.x-amd64', 'libc6'];
      await page.evaluate(() => dashboardData.reload());
      const card = i => page.locator('.agent-card[data-agent-id="' + ID(i) + '"]');
      const row = i => page.locator('.banner-row[data-agent-id="' + ID(i) + '"]');
      await card(0).locator('.system-pill[data-tone=warning]').waitFor();
      assert.equal(await card(0).locator('.system-pill').textContent(), '⚠ 证书将到期');
      assert.equal(await card(2).locator('.system-pill[data-tone=danger]').textContent(), '⚠ 服务异常');
      assert.equal(await card(4).locator('.system-pill[data-tone=warning]').textContent(), '⚠ 时间未同步');
      assert.equal(await card(7).locator('.system-pill[data-tone=server]').textContent(), 'Server');
      assert.match(await page.locator('#overview-health-stat').textContent(), /3 台需要关注|3 台.*关注|需要关注.*3/);
      await capture(page, theme, 'cards');
      await page.locator('#overview-health').click();
      await page.waitForFunction(() => document.querySelectorAll('.agent-card').length === 3);
      await page.locator('#overview-health').click();
      await choose(page, 'list');
      await row(7).locator('.system-pill[data-tone=server]').waitFor();
      assert.equal(await row(2).locator('.banner-system-badges .system-pill').textContent(), '⚠ 服务异常');
      await capture(page, theme, 'list');

      await row(0).locator('.banner-name').click();
      await page.getByRole('tab', {name: '系统', exact: true}).click();
      const system = page.locator('#details-pane-system');
      await system.getByText('node-a.example.test', {exact: true}).waitFor();
      assert.equal(await system.getByText('5 天', {exact: true}).getAttribute('data-tone'), 'warning');
      assert.match(await system.textContent(), /有 1 个目录没有权限读取/);
      assert.match(await system.textContent(), /404-probe-install setup-root-collector/);
      assert.match(await system.textContent(), /待更新软件包未开启/);
      assert.match(await page.locator('.details-machine').textContent(), /Debian 13.6 · KVM/);
      const reboot = system.locator('section').filter({has: page.getByRole('heading', {name: '需要重启', exact: true})});
      const rebootReason = reboot.locator('.details-fact').filter({hasText: '原因'});
      assert.equal(await rebootReason.textContent(), '原因linux-image-6.12.x-amd64、libc6');
      if (theme === 'light') await captureReboot(reboot, '04-reboot-with-packages');
      const rerenderSystem = async () => {
        await page.getByRole('tab', {name: '安全', exact: true}).click();
        await page.getByRole('tab', {name: '系统', exact: true}).click();
      };
      checks.reboot_packages = Array.from({length: 20}, (_, i) => 'package-' + i);
      checks.reboot_packages_truncated = true;
      await rerenderSystem();
      assert.match(await rebootReason.textContent(), /package-18、package-19 等$/);
      delete checks.reboot_packages;
      delete checks.reboot_packages_truncated;
      await rerenderSystem();
      assert.equal(await rebootReason.count(), 0);
      await reboot.getByText('需要重启', {exact: true}).last().waitFor();
      if (theme === 'light') await captureReboot(reboot, '05-reboot-without-packages');
      await capture(page, theme, 'system');
      await system.getByRole('heading', {name: '机器', exact: true}).scrollIntoViewIfNeeded();
      await capture(page, theme, 'system-lower');

      await page.getByRole('tab', {name: '安全', exact: true}).click();
      assert.match(await page.locator('.system-fail2ban').textContent(), /未开启/);
      f.state.items[0].system_checks.fail2ban = {
        status: 'ok', jail_count: 1, jails: [{name: 'sshd', total: 201, ips: ['192.0.2.14']}],
      };
      await page.getByRole('tab', {name: '系统', exact: true}).click();
      await page.getByRole('tab', {name: '安全', exact: true}).click();
      await page.locator('.system-jail summary').click();
      assert.match(await page.locator('.system-jail').textContent(), /sshd · 封锁 201 个.*192\.0\.2\.14/);

      await page.getByRole('tab', {name: '管理', exact: true}).click();
      const settings = page.locator('.system-settings');
      await settings.getByRole('button', {name: '移除 /opt/certs'}).waitFor();
      await settings.scrollIntoViewIfNeeded();
      await capture(page, theme, 'management');
      await settings.getByRole('textbox', {name: '添加证书目录'}).fill('/root/certs');
      await settings.getByRole('textbox', {name: '添加证书目录'}).press('Enter');
      await settings.getByRole('button', {name: '移除 /root/certs'}).waitFor();
      await settings.getByRole('button', {name: '移除 /root/certs'}).click();
      await settings.getByRole('button', {name: '移除 /root/certs'}).waitFor({state: 'detached'});
      await settings.getByRole('textbox', {name: '添加关注的服务'}).fill('caddy');
      await settings.getByRole('textbox', {name: '添加关注的服务'}).press('Enter');
      await settings.getByRole('button', {name: '移除 caddy'}).waitFor();
      await settings.getByRole('button', {name: '移除 nginx'}).click();
      await settings.getByRole('button', {name: '移除 nginx'}).waitFor({state: 'detached'});
      assert.deepEqual(f.state.items[0].system_check_settings, {
        certificate_directories: ['/opt/certs'], watched_services: ['sing-box', 'caddy'],
      });
      assert.deepEqual(panel.errors, []);
    } finally {
      if (panel) await panel.close();
      await browser.close();
    }
  });
}
