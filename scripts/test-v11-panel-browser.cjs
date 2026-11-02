const {test,before,after}=require('node:test'),assert=require('node:assert/strict');
const {launch,open}=require('./lib/v11-panel-fixture.cjs');
let browser;before(async()=>browser=await launch());after(async()=>browser?.close());
async function ui(run,options){const f=await open(browser,options);try{await run(f);assert.deepEqual(f.errors,[]);}finally{await f.close();}}
test('panel collapse persists; automatic narrow collapse and a manual expansion lasts until breakpoint change',()=>ui(async({page:p})=>{
 await p.locator('#panel-collapse').click();assert.equal(await p.locator('.panel-collapsed').count(),1);await p.reload();await p.waitForFunction(()=>typeof dashboardUI!=='undefined');assert.equal(await p.locator('.panel-collapsed').count(),1);
 await p.locator('#panel-collapse').click();await p.setViewportSize({width:1100,height:1080});await p.waitForFunction(()=>document.querySelector('.panel-collapsed'));await p.locator('#panel-collapse').click();assert.equal(await p.locator('.panel-collapsed').count(),0);
 await p.setViewportSize({width:1440,height:1080});await p.setViewportSize({width:1100,height:1080});await p.waitForFunction(()=>document.querySelector('.panel-collapsed'));
 for(const id of ['add-agent','stream','logout','server-version','dashboard-search','dashboard-sort','dashboard-chips','dashboard-clear','dashboard-count'])assert.equal(await p.locator('#'+id).count(),1,id);
 for(const b of await p.locator('.dashboard-panel button:visible').all())assert(await b.getAttribute('aria-label')||await b.textContent());
}));
test('three height tiers fold tags, groups, and attention under More using CSS',()=>ui(async({page:p})=>{
 for(const [height,hidden]of [[1000,['#dashboard-tags']],[800,['#dashboard-tags','#dashboard-groups']],[600,['#dashboard-tags','#dashboard-groups','#panel-attention']]]){
  await p.setViewportSize({width:1920,height});for(const id of hidden)assert.equal(await p.locator(id).isVisible(),false,id);await p.locator('#panel-more-toggle').click();for(const id of hidden)assert.equal(await p.locator(id).isVisible(),true,id);await p.locator('#panel-more-toggle').click();
 }
}));
test('mobile overlay traps focus, closes on Escape/selection, and shares stacked scroll locking',()=>ui(async({page:p})=>{
 assert.equal(await p.locator('#dashboard-panel').isVisible(),false);await p.locator('#panel-mobile-open').click();assert.equal(await p.locator('#panel-mobile-dialog').isVisible(),true);assert.equal(await p.evaluate(()=>document.body.style.overflow),'hidden');
 await p.locator('#panel-mobile-close').focus();await p.keyboard.press('Shift+Tab');assert(await p.locator('#panel-mobile-dialog').evaluate(n=>n.contains(document.activeElement)));await p.keyboard.press('Escape');assert.equal(await p.locator('#panel-mobile-dialog').isVisible(),false);assert.equal(await p.evaluate(()=>document.activeElement.id),'panel-mobile-open');assert.equal(await p.evaluate(()=>document.body.style.overflow),'');
 await p.locator('#panel-mobile-open').click();await p.locator('[data-quick=offline]').click();assert.equal(await p.locator('#panel-mobile-dialog').isVisible(),false);await p.waitForFunction(()=>document.querySelector('#dashboard-count').textContent==='显示 1 / 28 台');
 await p.locator('#panel-mobile-open').click();await p.locator('#add-agent').click();assert.equal(await p.locator('#add-agent-dialog').isVisible(),true);assert.equal(await p.locator('#panel-mobile-dialog').isVisible(),false);assert.equal(await p.evaluate(()=>document.body.style.overflow),'hidden');await p.keyboard.press('Escape');assert.equal(await p.evaluate(()=>document.body.style.overflow),'');
 // A second modal can also sit above an already-open overlay without unlocking it.
 await p.locator('#panel-mobile-open').click();await p.evaluate(()=>{const d=document.createElement('dialog');d.id='stack-probe';document.body.append(d);DialogShell.attach(d).open();});await p.keyboard.press('Escape');assert.equal(await p.evaluate(()=>document.body.style.overflow),'hidden');await p.keyboard.press('Escape');assert.equal(await p.evaluate(()=>document.body.style.overflow),'');
},{width:390,height:844}));
test('panel group/tag/country/status and attention selections drive chips and count without changing totals',()=>ui(async({page:p})=>{
 const online=await p.locator('#overview-online').textContent();
 for(const [key,label]of [['groups','常用'],['tags','GIA'],['countries','美国'],['statuses','在线']]){
  if(['countries','statuses'].includes(key)&&await p.locator('#panel-more-toggle').getAttribute('aria-expanded')==='false')await p.locator('#panel-more-toggle').click();
  const host=p.locator('#dashboard-'+key);if(!await host.evaluate(n=>n.open))await host.locator('summary').click();await host.locator('.dashboard-option').filter({hasText:label}).locator('input').first().check();
 }
 await p.waitForFunction(()=>document.querySelectorAll('.dashboard-chip').length===4);await p.waitForFunction(()=>document.querySelector('#dashboard-count').textContent==='显示 1 / 28 台');assert.equal(await p.locator('#overview-online').textContent(),online);await p.locator('#dashboard-clear').click();await p.waitForFunction(()=>document.querySelector('#dashboard-count').textContent==='显示 28 / 28 台');
}));
test('release notice requires true newer and canonical stable version, never uses supplied URLs or cache age',()=>ui(async({page:p,state})=>{
 assert.equal(await p.locator('#panel-update').isVisible(),true);assert.equal(await p.locator('#panel-update').getAttribute('href'),'https://github.com/404-git-404/404-probe/releases/tag/v1.1.1');assert.equal(await p.locator('#panel-update').getAttribute('rel'),'noopener noreferrer');
 for(const latest of ['v1.1.1-rc.1','v01.2.3','javascript:alert(1)','v1.2.3/../../','v1.2.3\n']){await p.evaluate(latest=>dashboardUI.updateVersion({update:{newer:true,latest,html_url:'https://example.invalid/'}}),latest);assert.equal(await p.locator('#panel-update').isVisible(),false);}
 await p.evaluate(()=>dashboardUI.updateVersion({update:{newer:'true',latest:'v1.2.3'}}));assert.equal(await p.locator('#panel-update').isVisible(),false);
 await p.evaluate(()=>dashboardUI.updateVersion({update:{newer:true,latest:'v1.2.3',checked_at:1}}));await p.locator('#panel-collapse').click();assert.equal(await p.locator('#panel-update').isVisible(),true);assert.equal(await p.locator('#panel-update b').textContent(),'⬆');
}));
test('settings opens on disabled/corrupt state, saves explicit boolean with session/CSRF, and explains 403',()=>ui(async({page:p,state})=>{
 state.update={enabled:false,newer:false,latest:'',checked_at:0};await p.locator('#panel-settings').click();await p.waitForFunction(()=>!document.querySelector('#update-check-enabled').checked);state.failSettings=true;await p.locator('#update-check-enabled').check();await p.locator('#update-check-save').click();await p.waitForFunction(()=>document.querySelector('#update-check-error').textContent.includes('CSRF'));assert.equal(await p.locator('#panel-settings-dialog').isVisible(),true);
 assert.deepEqual(state.writes[0].body,{enabled:true});assert.equal(state.writes[0].headers['x-csrf-token'],'synthetic-csrf');assert.equal(state.writes[0].headers['sec-fetch-site'],'same-origin');assert.equal(state.writes[0].headers.origin,new URL(p.url()).origin);
 state.failSettings=false;await p.locator('#update-check-save').click();await p.waitForFunction(()=>!document.querySelector('#panel-settings-dialog').open);assert.equal(state.update.enabled,true);
}));
test('theme control works inside the panel in both expanded and icon modes',()=>ui(async({page:p})=>{
 await p.locator('[data-theme-toggle]').click();assert.equal(await p.locator('html').getAttribute('data-theme'),'dark');await p.locator('#panel-collapse').click();await p.locator('[data-theme-toggle]').click();assert.equal(await p.evaluate(()=>ProbeTheme.get()),'system');
}));
async function command(p){await p.locator('#add-agent').click();await p.locator('#add-agent-choice-name').fill('单次名称 · 测试');await p.locator('#add-agent-next').click();await p.waitForSelector('#oneclick-result:not(.hidden)');}
test('one-click has one name input, visible address, masked multiline command and exact clipboard, then revoke',()=>ui(async({page:p,state})=>{
 await command(p);assert.equal(state.name,'单次名称 · 测试');assert.equal(state.enrollments,1);assert.equal(await p.locator('#add-agent-dialog input:not([type=hidden]):visible').count(),0);assert.equal(await p.locator('[data-agent-server]').textContent(),new URL(p.url()).origin);
 const displayed=await p.locator('#oneclick-command').textContent();assert(!displayed.includes(state.token));assert(displayed.includes('••••'));assert(displayed.includes('\n'));assert.equal(await p.locator('#oneclick-command strong').count(),4);
 await p.locator('#oneclick-copy').click();assert.equal(await p.evaluate(()=>navigator.clipboard.readText()),state.command);assert.equal(await p.locator('#oneclick-copy').textContent(),'✓ 已复制');await p.locator('#oneclick-revoke').click();await p.waitForFunction(()=>document.querySelector('#oneclick-countdown').textContent==='已作废');assert.equal(state.revokes,1);assert.equal(await p.locator('#oneclick-command').textContent(),'');await p.locator('#oneclick-done').click();assert.equal(await p.locator('#oneclick-command').textContent(),'');
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
