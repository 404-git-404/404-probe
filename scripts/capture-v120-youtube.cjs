// Exact-source loopback captures. Run once with R120_BASELINE and once without.
const fs = require('node:fs');
const path = require('node:path');
const {fixture, card, block, settled, nearBottom, tipGeometry, ID} = require('./lib/v120-youtube-fixture.cjs');

const output = process.env.R120_SCREENS;
const baseline = process.env.R120_BASELINE;
const side = baseline ? 'before' : 'after';
const {launch, start} = fixture(baseline);

(async () => {
  fs.mkdirSync(output, {recursive: true});
  const browser = await launch(), scenes = [];
  try {
    for (const theme of ['dark', 'light']) {
      const session = await start(browser, {theme});
      const {page} = session;
      const capture = async (scene, extra = {}) => {
        const file = side + '-' + theme + '-' + scene + '.png';
        await page.screenshot({path: path.join(output, file), animations: 'disabled', scale: 'device'});
        scenes.push({file, ...extra});
      };
      const measure = locator => locator.evaluate(el => el.getBoundingClientRect().toJSON());
      try {
        const primary = page.locator(card(0) + ' .c7-foot1');
        const badge = page.locator(card(2) + ' .check-result');
        if (!baseline) {
          await capture('02-cards', {footer: await measure(primary)});
          await badge.hover();
          if (await page.locator('.youtube-status-tip').count()) throw Error('Unexpected YouTube tooltip');
          await capture('youtube-hover', {badge: await measure(badge)});
          await page.mouse.move(1, 1);
          await page.locator('[data-view=list]').click();
          await settled(page);
          await capture('03-list', {table: await measure(page.locator('.banner-table'))});
          await page.evaluate(id => deviceDetails.open(id, 'management'), ID(2));
          await page.waitForFunction(id => agents.get(id)?.detail_loaded && !detailJobs.has(id) && !renderFrame, ID(2));
          await capture('05-management', {dialog: await measure(page.locator('#device-details'))});
          await page.keyboard.press('Escape');
          await page.locator('[data-view=cards]').click();
          await settled(page);
        }
        const tip = page.locator('.nq7-tip:visible');
        for (let lane = 0; lane < 3; lane++) {
          const anchor = block(page, 2, lane, 19);
          await anchor.hover();
          await capture('network-row-' + (lane + 1), {anchor: await measure(anchor), tip: await tipGeometry(tip)});
        }
        await page.mouse.move(1, 1);
        const bottom = block(page, 5, 2, 19);
        await nearBottom(page, 5, bottom);
        await bottom.hover();
        await capture('network-bottom', {anchor: await measure(bottom), tip: await tipGeometry(tip)});
        if (session.errors.length) throw Error(JSON.stringify(session.errors));
      } finally {
        await session.close();
      }
    }
  } finally {
    await browser.close();
  }
  fs.writeFileSync(path.join(output, side + '-geometry.json'), JSON.stringify(scenes, null, 2) + '\n');
})().catch(error => { console.error(error); process.exitCode = 1; });
