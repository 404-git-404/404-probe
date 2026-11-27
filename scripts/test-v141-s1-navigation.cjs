const {test,before,after}=require('node:test');
const assert=require('node:assert/strict');
const M=require('../web/static/dashboard-model.js');
const {launch,open}=require('./lib/v11-panel-fixture.cjs');

let browser;
before(async()=>{browser=await launch();});
after(async()=>{await browser?.close();});

const settle=page=>page.waitForFunction(()=>!renderFrame);

async function ui(run,options={}){
  const f=await open(browser,{width:1440,height:960,...options});
  try{
    await run(f);
    assert.deepEqual(f.errors,[]);
  }finally{
    await f.close();
  }
}

test('systemBadges labels service failure as 服务异常 with danger tone',()=>{
  const badges=M.systemBadges({online:true,system_attention:{service:true}});
  assert.deepEqual(badges,[['服务异常','danger']]);
  assert.equal(badges.some(([label])=>label==='服务停止'),false);
});

test('Devices row clears all filters, preserves sort and view, shows 28 vs 3 / 28, and follows drawer rules',()=>ui(async({page})=>{
  assert.equal(await page.locator('#dashboard-all').count(),0);
  assert.equal(await page.locator('#dashboard-count').count(),0);
  assert.equal(await page.locator('#dashboard-devices-count').textContent(),'28');
  assert.equal(await page.locator('#dashboard-devices > summary').getAttribute('aria-pressed'),'true');

  // Set non-default sort, direction, and list view.
  await page.locator('#dashboard-sort').selectOption('cpu');
  await page.locator('#dashboard-direction').click();
  await page.locator('[data-view=list]').click();
  await settle(page);
  assert.equal(await page.locator('#dashboard-sort').inputValue(),'cpu');
  assert.equal(await page.locator('#dashboard-direction').textContent(),'↓');
  assert.equal(await page.locator('[data-view=list]').getAttribute('aria-pressed'),'true');

  // Apply group, country, status, YouTube, quick, tag, and search filters.
  await page.locator('#dashboard-groups .dashboard-option').filter({hasText:'常用'}).click();
  await settle(page);
  assert.equal(await page.locator('#dashboard-devices-count').textContent(),'3 / 28');
  assert.equal(await page.locator('#dashboard-devices > summary').getAttribute('aria-pressed'),'false');

  await page.locator('#dashboard-countries > summary').click();
  await page.locator('#dashboard-countries .dashboard-option input').first().check();
  await page.locator('#dashboard-youtube-free').check();
  await page.locator('#dashboard-statuses > summary').click();
  await page.locator('#dashboard-statuses .dashboard-option').filter({hasText:'在线'}).locator('input').check();
  await page.locator('#dashboard-tags .dashboard-options .dashboard-option').first().click();
  await page.locator('#overview-health').click();
  await page.locator('#dashboard-search').fill('Synthetic');
  await settle(page);
  assert.match(await page.locator('#dashboard-devices-count').textContent(),/^\d+ \/ 28$/);

  // Clicking Devices row on devices page while drawer is open clears all filters, collapses drawer, and preserves sort + view.
  assert.equal(await page.locator('#dashboard-devices').evaluate(n=>n.open),true);
  await page.locator('#dashboard-devices > summary').click();
  await settle(page);
  assert.equal(await page.locator('#dashboard-devices').evaluate(n=>n.open),false);
  assert.equal(await page.locator('#dashboard-devices-count').textContent(),'28');
  assert.equal(await page.locator('#dashboard-devices > summary').getAttribute('aria-pressed'),'true');
  assert.equal(await page.locator('#dashboard-search').inputValue(),'');
  assert.equal(await page.locator('#dashboard-tags-clear').getAttribute('aria-pressed'),'true');
  assert.deepEqual(await page.evaluate(()=>({
    groups:dashboardUI.preferences.value.groups,
    countries:dashboardUI.preferences.value.countries,
    statuses:dashboardUI.preferences.value.statuses,
    youtubeRegions:dashboardUI.preferences.value.youtubeRegions,
    quick:dashboardUI.preferences.value.quick,
    tags:dashboardUI.preferences.value.tags,
    search:dashboardUI.preferences.value.search,
    sort:dashboardUI.preferences.value.sort,
    sortDir:dashboardUI.preferences.value.sortDir,
    viewMode:dashboardUI.preferences.value.viewMode,
  })),{
    groups:[],countries:[],statuses:[],youtubeRegions:[],quick:[],tags:[],search:'',
    sort:'cpu',sortDir:'desc',viewMode:'list',
  });

  // Clicking Devices row when drawer is closed expands it.
  await page.locator('#dashboard-devices > summary').click();
  assert.equal(await page.locator('#dashboard-devices').evaluate(n=>n.open),true);

  // Collapse drawer, navigate to Network Status, then click Devices row: returns to devices AND opens drawer.
  await page.locator('#dashboard-devices > summary').click();
  assert.equal(await page.locator('#dashboard-devices').evaluate(n=>n.open),false);
  await page.locator('#dashboard-network').click();
  await settle(page);
  assert.equal(await page.locator('#dashboard-devices > summary').getAttribute('aria-pressed'),'false');
  await page.locator('#dashboard-devices > summary').click();
  await settle(page);
  assert.equal(await page.locator('#network-placeholder').isVisible(),false);
  assert.equal(await page.locator('#dashboard-devices').evaluate(n=>n.open),true);
  assert.equal(await page.locator('#dashboard-devices > summary').getAttribute('aria-pressed'),'true');

  // Collapse panel, click Devices icon: expands panel and opens drawer.
  await page.locator('#panel-collapse').click();
  assert.equal(await page.locator('.panel-collapsed').count(),1);
  assert.equal(await page.locator('#dashboard-devices-count').isVisible(),false);
  await page.locator('#dashboard-devices > summary').click();
  assert.equal(await page.locator('.panel-collapsed').count(),0);
  assert.equal(await page.locator('#dashboard-devices').evaluate(n=>n.open),true);
}));

test('toolbar keeps only tags and sort; view switch and add-agent live in left panel and work collapsed and on phone',async()=>{
  for(const theme of ['light','dark']){
    await ui(async({page})=>{
      // Toolbar only keeps tags on left and sort+direction on right.
      assert.equal(await page.locator('#dashboard-toolbar #dashboard-count').count(),0);
      assert.equal(await page.locator('#dashboard-toolbar .dashboard-views').count(),0);
      assert.equal(await page.locator('#dashboard-toolbar #add-agent').count(),0);
      assert.deepEqual(
        await page.locator('.dashboard-toolbar-actions > *').evaluateAll(ns=>ns.map(n=>n.id||n.getAttribute('for'))),
        ['dashboard-sort','dashboard-sort','dashboard-direction']
      );

      // View switch is between search slot and Devices drawer, equal-width segments, solid accent active style.
      const searchBox=await page.locator('#dashboard-search-slot').boundingBox();
      const switchBox=await page.locator('#dashboard-panel .dashboard-views').boundingBox();
      const devicesBox=await page.locator('#dashboard-devices > summary').boundingBox();
      const cardsBox=await page.locator('[data-view=cards]').boundingBox();
      const listBox=await page.locator('[data-view=list]').boundingBox();
      assert(switchBox.y>=searchBox.y+searchBox.height);
      assert(devicesBox.y>=switchBox.y+switchBox.height);
      assert(Math.abs(switchBox.width-searchBox.width)<2);
      assert(Math.abs(cardsBox.width-listBox.width)<2);

      const viewStyles=await page.evaluate(()=>{
        const rootStyle=getComputedStyle(document.documentElement);
        const probe=(prop,value)=>{
          const s=document.createElement('span');
          s.style[prop]=value;
          document.body.append(s);
          const out=getComputedStyle(s)[prop];
          s.remove();
          return out;
        };
        const cards=getComputedStyle(document.querySelector('[data-view=cards]'));
        const list=getComputedStyle(document.querySelector('[data-view=list]'));
        const add=getComputedStyle(document.querySelector('#add-agent'));
        return {
          accent:probe('backgroundColor',rootStyle.getPropertyValue('--color-accent')),
          onAccent:probe('color',rootStyle.getPropertyValue('--color-text-02')),
          cardsBg:cards.backgroundColor,
          cardsColor:cards.color,
          listBg:list.backgroundColor,
          addBg:add.backgroundColor,
          addBorderWidth:add.borderTopWidth,
        };
      });
      assert.equal(viewStyles.cardsBg,viewStyles.accent);
      assert.equal(viewStyles.cardsColor,viewStyles.onAccent);
      assert.notEqual(viewStyles.listBg,viewStyles.accent);
      assert.equal(viewStyles.addBg,'rgba(0, 0, 0, 0)');
      assert.equal(viewStyles.addBorderWidth,'1px');

      // Add Agent sits below .panel-overview and above .panel-status in .panel-bottom.
      const overviewBox=await page.locator('.panel-overview').boundingBox();
      const addBox=await page.locator('#add-agent').boundingBox();
      const statusBox=await page.locator('.panel-status').boundingBox();
      assert(addBox.y>=overviewBox.y+overviewBox.height);
      assert(statusBox.y>=addBox.y+addBox.height);

      // Expanded view switch works.
      await page.locator('[data-view=list]').click();
      await settle(page);
      assert.equal(await page.locator('#agent-list').isVisible(),true);
      await page.locator('[data-view=cards]').click();
      await settle(page);
      assert.equal(await page.locator('#agents .agent-card').count(),28);

      // Collapsed panel exposes both icon buttons and they work.
      await page.locator('#panel-collapse').click();
      assert.equal(await page.locator('.panel-collapsed').count(),1);
      assert.equal(await page.locator('.dashboard-views').isVisible(),false);
      const toggle=page.locator('#panel-view-toggle');
      assert.equal(await toggle.isVisible(),true);
      assert.equal(await toggle.getAttribute('title'),'当前：卡片，点击切换到列表');
      assert.equal(await toggle.locator('.view-icon-cards').isVisible(),true);
      assert.equal(await toggle.locator('.view-icon-list').isVisible(),false);

      await toggle.click();
      await settle(page);
      assert.equal(await page.locator('#agent-list').isVisible(),true);
      assert.equal(await toggle.getAttribute('title'),'当前：列表，点击切换到卡片');
      assert.equal(await toggle.locator('.view-icon-cards').isVisible(),false);
      assert.equal(await toggle.locator('.view-icon-list').isVisible(),true);

      await toggle.click();
      await settle(page);
      assert.equal(await page.locator('#agents .agent-card').count(),28);
      assert.equal(await toggle.getAttribute('title'),'当前：卡片，点击切换到列表');

      // Collapsed Add Agent icon button sits at bottom and opens dialog.
      const collapsedAdd=page.locator('#add-agent');
      assert.equal(await collapsedAdd.isVisible(),true);
      assert.equal(await collapsedAdd.getAttribute('title'),'添加 Agent');
      assert.equal(await collapsedAdd.locator('svg').isVisible(),true);
      assert.equal(await collapsedAdd.locator('span').isVisible(),false);
      assert.equal(await page.locator('.panel-overview').isVisible(),false);
      assert.equal(await page.locator('.panel-status').isVisible(),false);
      await collapsedAdd.click();
      assert.equal(await page.locator('#add-agent-dialog').isVisible(),true);
      await page.keyboard.press('Escape');
      assert.equal(await page.locator('#add-agent-dialog').isVisible(),false);

      // Mobile ☰ panel hides view switch and keeps Add Agent at bottom.
      await page.locator('#panel-collapse').click();
      await page.setViewportSize({width:390,height:844});
      await settle(page);
      await page.locator('#panel-mobile-open').click();
      assert.equal(await page.locator('#panel-mobile-dialog').isVisible(),true);
      assert.equal(await page.locator('.panel-view-switch').isVisible(),false);
      assert.equal(await page.locator('#add-agent').isVisible(),true);
      assert.equal(await page.locator('#add-agent span').isVisible(),true);
      await page.locator('#add-agent').click();
      assert.equal(await page.locator('#add-agent-dialog').isVisible(),true);
      assert.equal(await page.locator('#panel-mobile-dialog').isVisible(),false);
      await page.keyboard.press('Escape');
    },{theme});
  }
});
