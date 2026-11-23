'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {start,ID} = require('./lib/v110-dashboard-fixture.cjs');

const expected = ['SSS','SS','S','A','B','C','D','none'];
const points = [98,92,87,80,71,58,44,null];
const now = Date.now();
const day = 86400000;
const part = (key,score,weight,reason) => ({key,score,weight,effective_weight:reason?0:weight,reason});
const fixtureScore = i => {
  const score = points[i%8], grade = expected[i%8];
  if(score==null) return {overall:{score,reason:'paused'},network:{score,reason:'paused'}};
  const network = {score,grade,parts:[
    part('availability',score,30),part('latency',score,25),part('stability',score,20),
    part('evening',score,15),part('alerts',score,10),
  ]};
  const value = i%8===3 ? part('value',null,20,'once') : part('value',score,20);
  return {network,overall:{score,grade,parts:[
    part('network',score,40),part('uptime',score,25),value,part('resources',score,15),
  ]},online_percent:95+score/20,value_rank:value.score==null?0:i+1,value_count:value.score==null?0:8};
};

async function scoreFleet(n){
  const fixture = await start({n,handle({url,json}){
    if(url.pathname==='/api/v1/web/scores'){
      json({computed_at:now,server_now:now,items:Object.fromEntries(Array.from({length:n},(_,i)=>[ID(i),fixtureScore(i)]))});
      return true;
    }
    if(url.pathname==='/api/v1/web/network-quality/fleet'){
      const id=url.searchParams.get('agent_id');
      const indices=id?[parseInt(id,16)-1]:Array.from({length:n},(_,i)=>i);
      json({start:now-day,end:now,now,machines:indices.map(i=>({
        agent_id:ID(i),name:'Synthetic-'+String(i+1).padStart(3,'0'),country_code:'HK',
        status:'online',last_seen:now,slow_threshold_ms:500,alerts_30d:0,current_alerts:0,
        objects:[],lines:[],
      }))});
      return true;
    }
    if(url.pathname.endsWith('/network-quality/alerts')){
      json([]);return true;
    }
    return false;
  }});
  for(const [i,row] of fixture.state.items.entries()){
    row.name='Synthetic-'+String(i+1).padStart(3,'0');
    row.online=i%8!==7;
    row.disabled_at=i%8===7?now-1000:null;
    row.group_id=null;
  }
  return fixture;
}

const captureDir = process.env.S3C_SCREENSHOTS;
const baselineDir = process.env.S3C_BASELINE_STATIC;
async function capture(page,name,options={}){
  if(!captureDir)return;
  fs.mkdirSync(captureDir,{recursive:true});
  await page.evaluate(()=>document.fonts.ready);
  await page.screenshot({path:path.join(captureDir,name+'.png'),animations:'disabled',...options});
}

async function open(browser,fixture,{theme='dark',phone=false,baseline=false}={}){
  const context = await browser.newContext({
    viewport:{width:phone?390:1600,height:phone?844:1070},hasTouch:phone,isMobile:phone,
    locale:'zh-CN',
  });
  await context.route('**/*',async route=>{
    const url=new URL(route.request().url());
    if(url.origin!==fixture.base) return route.abort();
    if(baseline&&!url.pathname.startsWith('/api/')){
      const file=path.resolve(baselineDir,'.'+(url.pathname==='/'?'/index.html':url.pathname));
      if(file.startsWith(path.resolve(baselineDir)+path.sep)&&fs.existsSync(file)) return route.fulfill({path:file});
    }
    return route.continue();
  });
  const page=await context.newPage();
  page.setDefaultTimeout(20000);
  const errors = [];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(fixture.base);
  await page.waitForFunction(()=>typeof dashboardData!=='undefined'&&dashboardData.ready());
  await page.evaluate(theme=>window.ProbeTheme.set(theme),theme);
  return {context,page,errors};
}

test('S3-C displays all score tiers, breakdowns and touch navigation',async t=>{
  const {chromium}=require(process.env.M02_PLAYWRIGHT);
  const browser=await chromium.launch({headless:true,executablePath:process.env.M02_CHROMIUM});
  const fixture=await scoreFleet(8);
  t.after(async()=>{await browser.close();fixture.close();});
  for(const theme of ['dark','light']){
    const {page,context,errors}=await open(browser,fixture,{theme});
    try{
      const top=page.locator('.score-stamp-card[data-tier="SSS"]');
      await top.waitFor();
      await page.waitForFunction(()=>document.querySelectorAll('.score-stamp-card').length===8);
      assert.equal(await top.getAttribute('aria-label'),'综合评分 SSS 98 分，点击查看详情');
      assert(await top.evaluate(button=>{
        const card=button.closest('.agent-card').getBoundingClientRect();
        const stamp=button.getBoundingClientRect();
        return button.contains(document.elementFromPoint(card.right+16,stamp.top+stamp.height/2));
      }),'the part of the SSS stamp over the adjacent card remains clickable');
      assert.deepEqual((await page.locator('.score-stamp-card').evaluateAll(nodes=>nodes.map(node=>node.dataset.tier))).sort(),[...expected].sort());
      assert.equal(await top.locator('svg use').getAttribute('href'),'/score-stamps.svg#grade-SSS');
      await capture(page,theme+'-cards',{fullPage:true});
      await top.hover();
      const tooltip=page.getByRole('tooltip');
      await tooltip.waitFor();
      assert.match(await tooltip.textContent(),/可用性.*晚高峰.*告警.*近 30 天在线率.*排名.*点击查看详情/);
      await capture(page,theme+'-hover');
      await top.click();
      const dialog=page.locator('#device-details[open]');
      await dialog.waitFor();
      assert.match(await dialog.locator('.score-breakdown').textContent(),/综合评分 SSS.*98/);
      await capture(page,theme+'-details');
      await page.keyboard.press('Escape');
      await dialog.waitFor({state:'hidden'});
      await page.locator('[data-view=list]').click();
      const listStamp=page.locator('.score-stamp-list[data-tier="SSS"]');
      await listStamp.waitFor();
      assert.equal(await page.locator('.score-stamp-list').count(),8);
      await capture(page,theme+'-list');
      await listStamp.click();
      await dialog.waitFor();
      await page.keyboard.press('Escape');
      await dialog.waitFor({state:'hidden'});
      await page.locator('.banner-name').first().click();
      await dialog.waitFor();
      await page.keyboard.press('Escape');
      await dialog.waitFor({state:'hidden'});
      await page.locator('#dashboard-network').click();
      await page.locator('.net-fleet tbody tr').first().click();
      const rating=page.locator('.score-network-badge');
      await rating.waitFor();
      assert.match(await rating.textContent(),/SSS.*98/);
      await rating.hover();
      await tooltip.waitFor();
      assert.match(await tooltip.textContent(),/网络评分 SSS.*可用性.*稳定性.*告警/);
      await capture(page,theme+'-network');
      assert.deepEqual(errors,[]);
    }finally{await context.close();}

    const mobile=await open(browser,fixture,{theme,phone:true});
    try{
      const top=mobile.page.locator('.score-stamp-card[data-tier="SSS"]');
      await top.waitFor();
      await mobile.page.emulateMedia({reducedMotion:'reduce'});
      assert.equal(await top.locator('svg').evaluate(node=>getComputedStyle(node).animationName),'none');
      assert(await top.locator('xpath=ancestor::*[contains(@class,"agent-card")]').locator('.c7-flip').evaluate(button=>{
        const r=button.getBoundingClientRect();
        return button.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2));
      }),'the card flip stays clickable on phones');
      const mobileLayout=await mobile.page.evaluate(()=>({
        width:document.documentElement.scrollWidth,
        overflow:[...document.querySelectorAll('body *')].flatMap(node=>{
          const right=node.getBoundingClientRect().right;
          return right>392?[[node.getAttribute('class'),Math.round(right)]]:[];
        }).slice(0,10),
      }));
      assert.equal(mobileLayout.width,390,JSON.stringify(mobileLayout.overflow));
      await capture(mobile.page,theme+'-390-cards');
      await top.tap();
      await mobile.page.locator('#device-details[open] .score-breakdown').waitFor();
      await capture(mobile.page,theme+'-390-details');
      assert.deepEqual(mobile.errors,[]);
    }finally{await mobile.context.close();}
  }
});

test('S3-C compares 200-device card scrolling against S3-B',{skip:!baselineDir&&'Optional S3-B comparison assets unavailable (S3C_BASELINE_STATIC)'},async t=>{
  const {chromium}=require(process.env.M02_PLAYWRIGHT);
  const browser=await chromium.launch({headless:true,executablePath:process.env.M02_CHROMIUM});
  const fixture=await scoreFleet(200);
  t.after(async()=>{await browser.close();fixture.close();});
  for(const baseline of [true,false]){
    const {page,context,errors}=await open(browser,fixture,{baseline});
    try{
      await page.waitForFunction(()=>document.querySelectorAll('.agent-card').length===200);
      if(!baseline)await page.locator('.score-stamp-card[data-tier="SSS"]').first().waitFor();
      const result=await page.evaluate(async()=>{
        const range=document.documentElement.scrollHeight-innerHeight;
        const frames=[];
        const frame=()=>new Promise(resolve=>requestAnimationFrame(resolve));
        for(let repeat=0;repeat<3;repeat++){
          window.scrollTo(0,0);await frame();let previous=await frame();
          for(let i=0;i<120;i++){
            window.scrollTo(0,range*(i<60?i:120-i)/60);
            const next=await frame();
            frames.push(next-previous);previous=next;
          }
        }
        frames.sort((a,b)=>a-b);
        return {frames:frames.length,median:frames[Math.floor(frames.length*.5)],
          p95:frames[Math.floor(frames.length*.95)],over32:frames.filter(value=>value>32).length};
      });
      t.diagnostic((baseline?'S3-B':'S3-C')+' '+JSON.stringify(result));
      assert.deepEqual(errors,[]);
    }finally{await context.close();}
  }
});
