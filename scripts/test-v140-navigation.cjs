const {test,before,after}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path');
const {launch,open,ID}=require('./lib/v11-panel-fixture.cjs');

const screenshots=process.env.V140_S1_SCREENS;
let browser;
before(async()=>{browser=await launch();});
after(async()=>{await browser?.close();});

const settle=page=>page.waitForFunction(()=>!renderFrame);
const card=(page,i)=>page.locator('#agents .agent-card[data-agent-id="'+ID(i)+'"]');
const row=(page,i)=>page.locator('#agent-list .banner-row[data-agent-id="'+ID(i)+'"]');
async function shot(page,name){
  if(!screenshots) return;
  fs.mkdirSync(screenshots,{recursive:true});
  await page.mouse.move(0,0);
  await page.screenshot({path:path.join(screenshots,name+'.png')});
}

async function withPanel(options,run){
  const panel=await open(browser,options);
  try{await run(panel);assert.deepEqual(panel.errors,[]);}finally{await panel.close();}
}

test('total count digit growth refits +N without a resize or catalog change',async()=>{
  await withPanel({n:9,width:1500},async({page,f})=>{
    const more=page.locator('#dashboard-tags-more');
    const measurement=await page.evaluate(()=>{
      const list=document.querySelector('#dashboard-tags .dashboard-options');
      const gap=parseFloat(getComputedStyle(list).gap);
      return {width:list.clientWidth, tags:[...list.children].reduce((sum,tag)=>sum+tag.getBoundingClientRect().width+gap,-gap)};
    });
    const width=1500+Math.ceil(measurement.tags-measurement.width)+3;
    // Long labels leave the test inside the desktop layout at the threshold.
    if(width<1310){
      for(const tag of f.state.tags) tag.name+=' 测试标签内容';
      f.state.revision++;
      await page.evaluate(()=>dashboardData.reload());
      await page.waitForFunction(()=>document.querySelector('#dashboard-tags .dashboard-option')?.textContent.includes('测试标签内容'));
    }
    let fitted=false;
    for(let w=1300;w<=1500;w+=2){
      await page.setViewportSize({width:w,height:1000});
      await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
      if(await more.isHidden()){fitted=true;break;}
    }
    assert(fitted);
    const added=structuredClone(f.state.items[0]);
    Object.assign(added,{agent_id:ID(999),tag_ids:[],name:'Synthetic-010'});
    f.state.items.push(added);
    await page.evaluate(()=>dashboardData.reload());
    await page.waitForFunction(()=>document.querySelector('#dashboard-count').textContent==='10 / 10 台');
    await more.waitFor();
    assert.match(await more.locator('summary').textContent(),/^\+[1-9]\d*$/);
    f.state.items.pop();
    await page.evaluate(()=>dashboardData.reload());
    await more.waitFor({state:'hidden'});
  });
});

test('PUT renewal summary drives deadline and overview immediately',async()=>{
  const renewal={type:'due_soon',date:'2040-12-02',days:0,field:'renewal_date',timezone:'UTC'};
  let writes=0, deviceRecord;
  await withPanel({n:1,handle:async({req,url,body,json})=>{
    if(req.method!=='PUT'||!url.pathname.endsWith('/plan')) return false;
    writes++;
    deviceRecord.plan={...deviceRecord.plan,...body};
    deviceRecord.renewal=renewal;
    json({...deviceRecord.plan,renewal});
    return true;
  }},async({page,f})=>{
    deviceRecord=f.state.items[0];
    await card(page,0).locator('.edit-plan').click();
    const dialog=page.locator('#plan-dialog');
    assert.equal(await dialog.locator('h2').textContent(),'编辑套餐');
    const title=await dialog.locator('h2').boundingBox(),device=await dialog.locator('#plan-agent-name').boundingBox();
    assert(device.x>title.x+title.width);
    await page.locator('#plan-renewal-date').fill(renewal.date);
    await page.locator('#plan-save').click();
    await dialog.waitFor({state:'hidden'});
    assert.equal(writes,1);
    assert.match(await card(page,0).locator('.renew-plan.warn').textContent(),/续费日已到/);
    assert.equal(await page.locator('#overview-renewal-stat strong').textContent(),'1');
    await page.locator('[data-view=list]').click();
    await settle(page);
    assert.equal(await row(page,0).locator('[data-due=due_soon]').textContent(),'余 0 天');
  });
});

test('logout requires confirmation and a canceled prompt sends no request',async()=>{
  let writes=0;
  await withPanel({n:2,handle:async({req,url,json})=>{
    if(req.method!=='POST'||url.pathname!=='/logout') return false;
    writes++;
    json({});
    return true;
  }},async({page})=>{
    await page.route('**/login',route=>route.fulfill({contentType:'text/html',body:'<title>登录</title>'}));
    await page.locator('#dashboard-settings > summary').click();
    await page.locator('#logout').click();
    const prompt=page.locator('#action-confirm');
    await prompt.getByRole('button',{name:'取消'}).click();
    assert.equal(writes,0);
    await page.locator('#logout').click();
    await prompt.getByRole('button',{name:'退出登录'}).click();
    await page.waitForURL('**/login');
    assert.equal(writes,1);
  });
});

for(const theme of ['dark','light']){
  test(theme+' cards, list, group modes and warning markers',async()=>{
    await withPanel({n:8,theme,width:1680,height:1050},async({page,f})=>{
      f.state.youtube_ad_free_regions=['CN','MO'];
      for(const [i,a] of f.state.items.entries()){
        a.name='Synthetic-'+String(i+1).padStart(3,'0');
        a.search_fields=[a.name,'synthetic-host-'+i,'203.0.*.*'];
      }
      const [first,second,third]=f.state.items;
      first.google_status={supported:true,manual_region:'MO'};
      first.state.cpu_percent=95;
      first.resources.cpu_percent=95;
      first.plan.usage_bytes=first.plan.quota_bytes*.95;
      first.state.host_health={disk:{status:'error',reasons:['write_eio']}};
      second.renewal={type:'due_soon',days:0,field:'renewal_date',date:'2026-11-22',timezone:'UTC'};
      third.country_code='DE';
      third.renewal={type:'overdue',days:-2,field:'expiry_date',date:'2026-11-20',timezone:'UTC'};
      await page.evaluate(()=>dashboardData.reload());
      await settle(page);
      assert(await card(page,0).locator('.dashboard-disk-warning').isVisible());
      assert.equal(await card(page,0).locator('.c7-ring').first().getAttribute('data-tone'),'warn');
      assert.equal(await card(page,0).locator('.c7-quota').getAttribute('data-tone'),'warn');
      assert(await card(page,0).locator('.dashboard-version-upgrade').isVisible());
      assert(await card(page,1).locator('.dashboard-network-warning').isVisible());
      assert(await card(page,1).locator('.renew-plan.warn').isVisible());
      assert(await card(page,2).locator('.renew-plan.danger').isVisible());
      await page.locator('#dashboard-countries > summary').click();
      await shot(page,theme+'-cards-flat');
      await page.locator('#dashboard-all-groups').click();
      await settle(page);
      await shot(page,theme+'-cards-groups');
      const group=page.locator('#dashboard-groups .dashboard-option').filter({hasText:'常用'});
      await group.click();
      await settle(page);
      assert.equal(await page.locator('.dashboard-group-header:visible').count(),1);
      await shot(page,theme+'-cards-single-group');
      await page.locator('#dashboard-all').click();
      await page.locator('[data-view=list]').click();
      await settle(page);
      assert.equal(await row(page,0).locator('[data-column=disk] .banner-metric').getAttribute('data-tone'),'warn');
      assert.equal(await row(page,0).locator('[data-column=traffic] .banner-metric').getAttribute('data-tone'),'warn');
      assert(await row(page,1).locator('.banner-network-warning').isVisible());
      assert(await row(page,2).locator('[data-due=overdue]').isVisible());
      await shot(page,theme+'-list-flat');
      await page.locator('#dashboard-all-groups').click();
      await settle(page);
      await shot(page,theme+'-list-groups');
      await group.click();
      await settle(page);
      await shot(page,theme+'-list-single-group');
      await page.locator('#dashboard-all').click();
      await page.locator('#dashboard-youtube-free').check();
      const country=page.locator('#dashboard-countries .dashboard-option').filter({hasText:'德国'}).locator('input');
      await country.check();
      await settle(page);
      assert(await row(page,0).isVisible());
      assert(await row(page,2).isVisible());
      await page.locator('#overview-upgrade').click();
      await settle(page);
      assert(await row(page,0).isVisible());
      assert.equal(await row(page,2).count(),0);
      await shot(page,theme+'-list-combined');
      await page.locator('#dashboard-all').click();
      await page.setViewportSize({width:390,height:844});
      await page.locator('#panel-mobile-open').click();
      await shot(page,theme+'-phone-navigation');
    });
  });
}
