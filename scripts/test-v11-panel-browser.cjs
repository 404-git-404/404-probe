const {test,before,after}=require('node:test'),assert=require('node:assert/strict');
const {launch,open}=require('./lib/v11-panel-fixture.cjs');
let browser;before(async()=>browser=await launch());after(async()=>browser?.close());
async function ui(run,options){const f=await open(browser,options);try{await run(f);assert.deepEqual(f.errors,[]);}finally{await f.close();}}
test('manual collapse persists and Ctrl K expands the icon rail',()=>ui(async({page:p})=>{
 await p.locator('#panel-collapse').click();
 assert.equal(await p.locator('.panel-collapsed').count(),1);
 await p.reload();
 await p.waitForFunction(()=>document.querySelector('.panel-collapsed'));
 assert.equal(await p.locator('#dashboard-network svg:visible').count(),1);
 assert.equal(await p.locator('#dashboard-network span:visible').count(),0);
 await p.keyboard.press('Control+k');
 assert.equal(await p.locator('.panel-collapsed').count(),0);
 assert.equal(await p.evaluate(()=>document.activeElement.id),'dashboard-search');
}));
test('short windows scroll navigation independently and retain multiple expanded drawers',()=>ui(async({page:p})=>{
 await p.setViewportSize({width:1920,height:500});
 for(const id of ['dashboard-countries','dashboard-statuses','dashboard-settings']) await p.locator('#'+id+' > summary').click();
 assert(await p.locator('.panel-scroll').evaluate(n=>n.scrollHeight>n.clientHeight));
 const bottom=await p.locator('.panel-bottom').boundingBox();
 await p.locator('.panel-scroll').evaluate(n=>n.scrollTop=n.scrollHeight);
 assert.deepEqual(await p.locator('.panel-bottom').boundingBox(),bottom);
 await p.reload();
 await p.waitForFunction(()=>dashboardData.ready());
 for(const id of ['dashboard-groups','dashboard-countries','dashboard-statuses','dashboard-settings']) assert(await p.locator('#'+id).evaluate(n=>n.open));
 await p.locator('#manage-groups').click();
 assert(await p.locator('#organization-manage-dialog').isVisible());
}));
test('390px overlay closes on backdrop, left swipe and Escape, preserving stacked scroll locking',()=>ui(async({page:p})=>{
 const opener=p.locator('#panel-mobile-open'), dialog=p.locator('#panel-mobile-dialog');
 assert.equal(await p.locator('#dashboard-panel').isVisible(),false);
 await opener.click();
 assert(await dialog.isVisible());
 assert.equal(await p.evaluate(()=>document.body.style.overflow),'hidden');
 assert((await dialog.boundingBox()).width<=390*.75);
 assert.equal(await dialog.locator('#panel-mobile-close').count(),0);
 await p.mouse.click(380,250);
 assert.equal(await dialog.isVisible(),false);
 await opener.click();
 await dialog.evaluate(n=>{
  for(const [type,x] of [['touchstart',215],['touchend',85]]){
   const touch=new Touch({identifier:1,target:n,clientX:x,clientY:120});
   n.dispatchEvent(new TouchEvent(type,{touches:type==='touchstart'?[touch]:[],changedTouches:[touch],bubbles:true}));
  }
 });
 assert.equal(await dialog.isVisible(),false);
 assert.equal(await p.evaluate(()=>document.activeElement.id),'panel-mobile-open');
 await opener.click();
 await p.keyboard.press('Shift+Tab');
 assert(await dialog.evaluate(n=>n.contains(document.activeElement)));
 await p.evaluate(()=>{const d=document.createElement('dialog');document.body.append(d);DialogShell.attach(d).open();});
 await p.keyboard.press('Escape');
 assert.equal(await p.evaluate(()=>document.body.style.overflow),'hidden');
 await p.keyboard.press('Escape');
 assert.equal(await p.evaluate(()=>document.body.style.overflow),'');
},{width:390,height:844}));
test('group, tag, country and status filters intersect and Devices row clears all filters',()=>ui(async({page:p})=>{
 const online=await p.locator('#overview-online').textContent();
 for(const [key,label] of [['groups','常用'],['tags','GIA'],['countries','美国'],['statuses','在线']]){
  const host=p.locator('#dashboard-'+key);
  if(key!=='tags'&&!await host.evaluate(n=>n.open)) await host.locator('summary').click();
  const option=host.locator('.dashboard-option').filter({hasText:label}).first();
  if(['groups','tags'].includes(key)) await option.click();
  else await option.locator('input').check();
 }
 await p.waitForFunction(()=>document.querySelector('#dashboard-devices-count').textContent==='1 / 28');
 assert.equal(await p.locator('#dashboard-devices > summary').getAttribute('aria-pressed'),'false');
 assert.equal(await p.locator('#overview-online').textContent(),online);
 await p.locator('#dashboard-countries summary').click();
 assert.equal(await p.locator('#dashboard-countries .panel-summary').textContent(),'美国');
 await p.locator('#dashboard-devices > summary').click();
 await p.waitForFunction(()=>document.querySelector('#dashboard-devices-count').textContent==='28');
 assert.equal(await p.locator('#dashboard-devices > summary').getAttribute('aria-pressed'),'true');
 assert.equal(await p.locator('#dashboard-tags-clear').getAttribute('aria-pressed'),'true');
}));
test('release notice requires true newer and canonical stable version, never uses supplied URLs or cache age',()=>ui(async({page:p,state})=>{
 assert.equal(await p.locator('#panel-update').isVisible(),true);assert.equal(await p.locator('#panel-update').getAttribute('href'),'https://github.com/404-git-404/404-probe/releases/tag/v1.1.1');assert.equal(await p.locator('#panel-update').getAttribute('rel'),'noopener noreferrer');
 for(const latest of ['v1.1.1-rc.1','v01.2.3','javascript:alert(1)','v1.2.3/../../','v1.2.3\n']){await p.evaluate(latest=>dashboardUI.updateVersion({update:{newer:true,latest,html_url:'https://example.invalid/'}}),latest);assert.equal(await p.locator('#panel-update').isVisible(),false);}
 await p.evaluate(()=>dashboardUI.updateVersion({update:{newer:'true',latest:'v1.2.3'}}));assert.equal(await p.locator('#panel-update').isVisible(),false);
 await p.evaluate(()=>dashboardUI.updateVersion({update:{newer:true,latest:'v1.2.3',checked_at:1}}));assert(await p.locator('#panel-update').isVisible());await p.locator('#panel-collapse').click();assert.equal(await p.locator('#panel-update').isVisible(),false);
}));
test('settings page persists the version checkbox immediately with session/CSRF, and explains 403',()=>ui(async({page:p,state})=>{
 state.update={enabled:false,newer:false,latest:'',checked_at:0};await p.locator('#dashboard-settings > summary').click();await p.locator('[data-settings=update]').click();assert.equal(new URL(p.url()).hash,'#update');
 await p.waitForFunction(()=>!document.querySelector('#update-check-enabled').disabled);
 state.failSettings=true;await p.locator('#update-check-enabled').check();await p.waitForFunction(()=>document.querySelector('#update-check-error').textContent.includes('CSRF'));
 assert.deepEqual(state.writes[0].body,{enabled:true});assert.equal(state.writes[0].headers['x-csrf-token'],'synthetic-csrf');assert.equal(state.writes[0].headers['sec-fetch-site'],'same-origin');assert.equal(state.writes[0].headers.origin,new URL(p.url()).origin);
 state.failSettings=false;await p.locator('#update-check-enabled').check();await p.locator('#update-saved').waitFor();assert.equal(state.update.enabled,true);
}));
test('theme control switches in place after opening settings from the icon rail',()=>ui(async({page:p})=>{
 await p.locator('#panel-collapse').click();await p.locator('#dashboard-settings > summary').click();await p.locator('[data-panel-theme=dark]').click();assert.equal(await p.locator('html').getAttribute('data-theme'),'dark');assert.equal(await p.locator('[data-panel-theme=dark]').getAttribute('aria-pressed'),'true');await p.locator('[data-panel-theme=light]').click();assert.equal(await p.evaluate(()=>ProbeTheme.get()),'light');
}));
async function command(p){await p.locator('#add-agent').click();await p.locator('#add-agent-choice-name').fill('单次名称 · 测试');await p.locator('#add-agent-next').click();await p.waitForSelector('#oneclick-result:not(.hidden)');}
test('one-click has one name input, visible address, masked multiline command and exact clipboard, then revoke',()=>ui(async({page:p,state})=>{
 await command(p);assert.equal(state.name,'单次名称 · 测试');assert.equal(state.enrollments,1);assert.equal(await p.locator('#add-agent-dialog input:not([type=hidden]):visible').count(),0);assert.equal(await p.locator('[data-agent-server]').textContent(),new URL(p.url()).origin);
 const displayed=await p.locator('#oneclick-command').textContent();assert(!displayed.includes(state.token));assert(displayed.includes('••••'));assert(displayed.includes('\n'));assert.equal(await p.locator('#oneclick-command strong').count(),4);
 await p.locator('#oneclick-copy').click();assert.equal(await p.evaluate(()=>navigator.clipboard.readText()),state.command);assert.equal(await p.locator('#oneclick-copy').textContent(),'已复制');await p.locator('#oneclick-revoke').click();await p.waitForFunction(()=>document.querySelector('#oneclick-countdown').textContent==='已作废');assert.equal(state.revokes,1);assert.equal(await p.locator('#oneclick-command').textContent(),'');await p.locator('#oneclick-done').click();assert.equal(await p.locator('#oneclick-command').textContent(),'');
}));
test('countdown expires and regenerates without changing the name; closing clears all command access',()=>ui(async({page:p,state})=>{
 await p.clock.install();await command(p);await p.clock.fastForward(30*60000+1000);assert.match(await p.locator('#oneclick-countdown').textContent(),/已过期/);assert.equal(await p.locator('#oneclick-copy').isDisabled(),true);await p.locator('#oneclick-regenerate').click();await p.waitForFunction(()=>!document.querySelector('#oneclick-copy').disabled);assert.equal(state.enrollments,2);assert.equal(state.name,'单次名称 · 测试');await p.keyboard.press('Escape');assert.equal(await p.locator('#oneclick-command').textContent(),'');
}));
test('manual installation has two copy steps, masked revealable credential, exact clipboard and clear-on-close',()=>ui(async({page:p,state})=>{
 await p.locator('#add-agent').click();await p.locator('#add-agent-choice-name').fill('手动一次名称');await p.locator('#add-agent-choose-manual').click();await p.locator('#add-agent-next').click();await p.waitForSelector('#add-agent-result:not(.hidden)');assert(!(await p.locator('#created-enrollment').textContent()).includes(state.manual));await p.locator('#show-enrollment').click();assert.equal(await p.locator('#created-enrollment').textContent(),state.manual);await p.locator('[data-copy-target=created-enrollment]').click();assert.equal(await p.evaluate(()=>navigator.clipboard.readText()),state.manual);assert.match(await p.locator('#add-agent-result').textContent(),/剪贴板.*清除/);await p.locator('#add-agent-done').click();assert.equal(await p.locator('#created-enrollment').textContent(),'');assert.equal(await p.evaluate(()=>manualCredential),'');
}));
test('management button text retains 4.5:1 contrast at rest, hover, focus and disabled in both themes',async()=>{
 const {open:themeOpen,scene}=require('./lib/v11-theme-fixture.cjs');
 for(const theme of ['light','dark']){
  const f=await themeOpen(browser,{theme});try{
   const p=f.page;await scene(p,'Details-management');
   for(const b of await p.locator('#device-details button:visible').all()){
    const wasDisabled=await b.isDisabled();
    for(const state of ['rest','hover','focus','disabled']){
     await p.mouse.move(0,0);await b.evaluate((n,{state,wasDisabled})=>{n.disabled=state==='disabled'||wasDisabled;if(state!=='focus')n.blur();},{state,wasDisabled});
     if(state==='hover')await b.hover();if(state==='focus'&&!wasDisabled)await b.focus();
     const result=await b.evaluate(n=>{
      const rgba=s=>s.match(/[\d.]+/g)?.map(Number)||[0,0,0,0];
      const blend=(a,b)=>a.slice(0,3).map((v,i)=>v*(a[3]??1)+b[i]*(1-(a[3]??1)));
      const ancestors=[];for(let e=n;e;e=e.parentElement)ancestors.unshift(e);
      let bg=[255,255,255];for(const e of ancestors)bg=blend(rgba(getComputedStyle(e).backgroundColor),bg);
      const c=getComputedStyle(n),fg=blend(rgba(c.color),bg),opacity=ancestors.reduce((o,e)=>o*Number(getComputedStyle(e).opacity),1),visibleFg=fg.map((v,i)=>v*opacity+bg[i]*(1-opacity));
      const luminance=c=>c.map(x=>{x/=255;return x<=.04045?x/12.92:((x+.055)/1.055)**2.4;}).reduce((a,v,i)=>a+v*[.2126,.7152,.0722][i],0);
      const l=[luminance(visibleFg),luminance(bg)].sort((a,b)=>b-a);return {text:n.textContent,ratio:(l[0]+.05)/(l[1]+.05),color:c.color,bg:c.backgroundColor,opacity};
     });assert(result.ratio>=4.5,JSON.stringify({theme,state,...result}));
    }await b.evaluate((n,v)=>n.disabled=v,wasDisabled);
   }
   assert.deepEqual(f.errors,[]);
  }finally{await f.close();}
 }
});
test('unsupported Alpine upgrade row suppresses the contradictory same-version explanation',async()=>{
 const {open:themeOpen,scene}=require('./lib/v11-theme-fixture.cjs');const f=await themeOpen(browser);
 try{const p=f.page;f.f.state.items[0].version='v1.0.10';f.f.state.items[0].updater_health={status:'unsupported'};await p.evaluate(()=>dashboardData.reload());await p.waitForFunction(()=>[...agents.values()][0].version==='v1.0.10');await scene(p,'Details-management');
  const row=p.locator('.details-action-row').filter({hasText:'暂不支持从面板升级'});await row.waitFor();const text=await row.textContent();assert(!text.includes('无需升级'));assert(!text.includes('以后更新面板'));assert(text.includes('Alpine/OpenRC'));assert.equal(await row.locator('.upgrade-agent').isDisabled(),true);assert.deepEqual(f.errors,[]);
 }finally{await f.close();}
});
