const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {test} = require('node:test');
const {launch, open, ID} = require('./lib/v11-panel-fixture.cjs');

const output = process.env.V140_S2A_SCREENS;
const names = ['Synthetic-300 GB', 'Synthetic-1500 GB', 'Synthetic-2500 GB'];
const card = i => `.agent-card[data-agent-id="${ID(i)}"]`;
const row = i => `.banner-row[data-agent-id="${ID(i)}"]`;

async function screenshot(page, theme, name) {
  if (!output) return;
  fs.mkdirSync(output, {recursive: true});
  await page.mouse.move(0, 0);
  await page.screenshot({path: path.join(output, `${theme}-${name}.png`), animations: 'disabled'});
}

async function setVisible(locator, visible) {
  await locator.waitFor({state: visible ? 'visible' : 'hidden'});
  assert.equal(await locator.isVisible(), visible);
}

for (const theme of ['dark', 'light']) {
  test(`${theme}: detection switches, unlimited traffic and disk acknowledgement across contexts`, {timeout: 90000}, async () => {
    const browser = await launch();
    let fixture;
    const panel = await open(browser, {
      n: 3, theme, width: 1720, height: 1080,
      handle: ({req, url, body, json, fail}) => {
        const prefix = '/api/v1/web/agents/' + ID(0);
        if (req.method !== 'PUT' || !url.pathname.startsWith(prefix + '/')) return false;
        const action = url.pathname.slice(prefix.length);
        const checks = {'/checks/country': 'country_lookup_enabled', '/checks/youtube': 'youtube_check_enabled'};
        if (!(action in checks) && action !== '/disk-fault-acknowledgement') return false;
        assert.equal(req.headers['x-csrf-token'], 'synthetic-csrf');
        const a = fixture.state.items[0];
        let kind;
        if (action in checks) {
          a[checks[action]] = body.enabled;
          kind = action.split('/').at(-1) + '-check';
          json({enabled: body.enabled});
        } else {
          if (body.fault_at !== a.state.host_health.last_fault_at) { fail('disk_fault_changed', 409); return true; }
          a.disk_fault_acknowledged_at = body.fault_at;
          kind = 'disk-fault-acknowledgement';
          json({disk_fault_acknowledged_at: body.fault_at});
        }
        fixture.publish('dashboard', {agent_id: a.agent_id, kind});
        return true;
      },
    });
    const {page, f} = panel;
    fixture = f;
    let second;
    try {
      const now = Date.now();
      f.state.youtube_ad_free_regions = ['AL'];
      for (const [i, a] of f.state.items.entries()) {
        a.name = names[i];
        a.resources.collected_at = a.state.collected_at = now;
        a.network.active = false;
        a.traffic.quota_state = 'unlimited';
        a.plan.usage_bytes = [300, 1500, 2500][i] * 1e9;
        a.plan.cycle_rx_bytes = a.plan.usage_bytes * 0.6;
        a.plan.cycle_tx_bytes = a.plan.usage_bytes * 0.4;
        a.accumulated_traffic.rx_total += a.plan.cycle_rx_bytes;
        a.accumulated_traffic.tx_total += a.plan.cycle_tx_bytes;
        delete a.plan.quota_bytes;
        delete a.plan.quota_value;
        delete a.plan.quota_unit;
      }
      const a = f.state.items[0];
      a.google_status.manual_region = 'AL';
      a.state.host_health = {last_fault_at: now - 86400000, disk: {status: 'ok', checked_at: now}};
      await page.evaluate(() => dashboardData.reload());
      await page.locator(card(0)).getByRole('heading', {name: names[0], exact: true}).waitFor();
      async function assertBars(view, barSelector, fillSelector) {
        for (const [i, scale] of [1000, 2000, 3000].entries()) {
          const bar = page.locator(view(i)).locator(barSelector);
          await bar.and(page.locator('[data-tone=neutral]')).waitFor();
          assert.equal(await bar.getAttribute('data-tone'), 'neutral');
          assert.equal(await bar.getAttribute('title'), `流量条满格 ${scale} GB`);
          assert.match(await bar.textContent(), /无限/);
          const fill = await bar.locator(fillSelector).evaluate(el => parseFloat(el.style.width));
          assert(Math.abs(fill - [30, 75, 250 / 3][i]) < 0.001, `fill ${i}: ${fill}`);
        }
        const health = page.locator('#overview-health');
        assert.match(await health.textContent(), /全部正常/);
        assert.equal(await health.isDisabled(), true);
      }
      await assertBars(card, '.c7-quota', '.c7-quota-fill');
      await screenshot(page, theme, '01-cards');

      const dialog = page.locator('#device-details');
      const openCard = () => page.locator(card(0)).locator('.score-stamp-card').click();
      const tab = name => dialog.getByRole('tab', {name, exact: true}).click();
      const close = () => page.keyboard.press('Escape');
      const country = dialog.getByRole('checkbox', {name: '出口地区', exact: true});
      const youtube = dialog.getByRole('checkbox', {name: 'YouTube 检测', exact: true});
      const manualRegion = dialog.getByRole('combobox', {name: 'YouTube 手动地区'});
      const filter = page.locator('#dashboard-youtube-free');
      const filterCount = page.locator('#dashboard-youtube-count');
      const regions = page.locator('#dashboard-countries summary');
      await openCard();
      await tab('管理');
      assert.equal(await country.isChecked(), true);
      assert.equal(await youtube.isChecked(), true);
      assert.equal(await manualRegion.inputValue(), 'AL');
      await screenshot(page, theme, '02-management');
      await country.uncheck();
      await youtube.uncheck();
      await setVisible(manualRegion, false);
      assert.equal(await dialog.locator('#details-pane-management').getByText('检测已关闭', {exact: true}).count(), 2);
      await screenshot(page, theme, '03-checks-disabled');
      await close();
      await setVisible(page.locator(card(0)).locator('.country-mark'), false);
      await setVisible(page.locator(card(0)).locator('.google-checks'), false);
      assert.equal((await filterCount.textContent()).trim(), '0');
      await page.locator('[data-view=list]').click();
      await setVisible(page.locator(row(0)).locator('.banner-flag'), false);
      await setVisible(page.locator(row(0)).locator('.banner-youtube'), false);
      await assertBars(row, '[data-column=traffic] .banner-metric', '.banner-track i');
      await screenshot(page, theme, '04-list');
      await page.locator(row(0)).locator('.banner-name').click();
      await tab('管理');
      await country.check();
      await youtube.check();
      await setVisible(manualRegion, true);
      assert.equal(await manualRegion.inputValue(), 'AL');
      await close();
      await setVisible(page.locator(row(0)).locator('.banner-flag'), true);
      await setVisible(page.locator(row(0)).locator('.banner-youtube'), true);
      assert.equal((await filterCount.textContent()).trim(), '1');
      if (!(await filter.isVisible())) await regions.click();
      await filter.check();
      await page.waitForFunction(() => document.querySelectorAll('#agent-list .banner-row').length === 1);
      assert.equal(await page.locator(row(0)).isVisible(), true);
      await filter.uncheck();

      await page.locator('[data-view=cards]').click();
      await openCard();
      const detailsAck = dialog.getByRole('button', {name: '知道了', exact: true});
      await setVisible(detailsAck, true);
      await screenshot(page, theme, '05-recovered-disk');
      second = await browser.newContext({locale: 'zh-CN'});
      await second.route('**/*', request => new URL(request.request().url()).origin === f.base ? request.continue() : request.abort());
      const peer = await second.newPage();
      await peer.goto(f.base);
      const peerAck = peer.locator(card(0)).getByRole('button', {name: '知道了', exact: true});
      await setVisible(peerAck, true);
      await detailsAck.click();
      await setVisible(detailsAck, false);
      await setVisible(peerAck, false);
      await close();
      await setVisible(page.locator(card(0)).locator('.dashboard-disk-warning'), false);

      a.state.host_health = {last_fault_at: now - 1, first_fault_at: now - 1, disk: {status: 'error', reasons: ['write_eio'], checked_at: now}};
      f.publish('agent', {agent_id: a.agent_id, state: a.state});
      await page.locator(card(0)).getByText(/磁盘异常/).waitFor();
      a.state.host_health = {last_fault_at: now - 1, disk: {status: 'ok', checked_at: now}};
      f.publish('agent', {agent_id: a.agent_id, state: a.state});
      const cardAck = page.locator(card(0)).getByRole('button', {name: '知道了', exact: true});
      await setVisible(cardAck, true);
      await setVisible(peerAck, true);
      await cardAck.click();
      await setVisible(cardAck, false);
      await setVisible(peerAck, false);
      assert.deepEqual(panel.errors, []);
    } finally {
      await second?.close();
      await panel.close();
      await browser.close();
    }
  });
}
