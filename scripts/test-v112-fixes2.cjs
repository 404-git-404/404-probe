const {test,before,after}=require('node:test'),assert=require('node:assert/strict');
const {launch,open,ID}=require('./lib/v11-panel-fixture.cjs');
let browser;before(async()=>browser=await launch());after(async()=>browser?.close());
const settle=p=>p.waitForFunction(()=>!renderFrame);
async function ui(run){const f=await open(browser,{n:3,width:2048,height:1152,deviceScaleFactor:1.25});try{await run(f);assert.deepEqual(f.errors,[]);}finally{await f.close();}}
test('S1 A1 full-row edge clicks, hover and keyboard work in both themes under SSE',()=>ui(async f=>{
 const p=f.page;await p.locator('[data-view=list]').click();await settle(p);const row=p.locator(`.banner-row[data-agent-id="${ID(0)}"]`),cell=row.locator('[data-column=upgrade]'),button=cell.locator('button');await button.evaluate(n=>window.__stableUpgrade=n);
 const timer=setInterval(()=>f.f.publish('agent',{agent_id:ID(0),state:{cpu_percent:Math.random()*20,collected_at:Date.now()}}),10);
 try{for(const theme of ['light','dark']){await p.evaluate(t=>ProbeTheme.set(t),theme);for(let i=0;i<50;i++){
  const c=await cell.boundingBox(),r=await row.boundingBox();assert(c.height>=r.height-1);const x=c.x+(i%2?c.width-3:3),y=r.y+(i%4<2?3:r.height-3);
  await p.mouse.move(x,y);assert(await button.evaluate(n=>n.matches(':hover')));const style=await button.evaluate(n=>({background:getComputedStyle(n).backgroundColor,row:getComputedStyle(n.closest('.banner-row')).backgroundColor,line:getComputedStyle(n).textDecorationLine}));assert.notEqual(style.background,style.row);assert.equal(style.line,'underline');
  assert(await p.evaluate(({x,y})=>!!document.elementFromPoint(x,y)?.closest('.banner-upgrade-link'),{x,y}));await p.mouse.click(x,y);await p.waitForFunction(()=>document.querySelector('#device-details').open);assert.equal(await p.locator('#details-tab-management').getAttribute('aria-selected'),'true');assert(await p.locator('[data-upgrade-region]').isVisible());await p.keyboard.press('Escape');await p.waitForFunction(()=>!document.querySelector('#device-details').open);
 }}
 await button.focus();await p.keyboard.press('Enter');assert(await p.locator('#device-details').isVisible());await p.keyboard.press('Escape');assert(await button.evaluate(n=>n===window.__stableUpgrade));
 await p.setViewportSize({width:1100,height:900});await button.scrollIntoViewIfNeeded();const c=await cell.boundingBox(),r=await row.boundingBox();await p.mouse.click(c.x+3,r.y+r.height-3);assert.equal(await p.locator('#details-tab-management').getAttribute('aria-selected'),'true');await p.keyboard.press('Escape');assert(await p.evaluate(({x,y})=>!document.elementFromPoint(x,y)?.closest('.banner-upgrade-link'),{x:c.x-3,y:r.y+r.height/2}));
 }finally{clearInterval(timer);}
}));
test('S2 YouTube region is static on hover, focus, click, and SSE',()=>ui(async f=>{
 const p=f.page,tag=p.locator('.agent-card[data-agent-id="'+ID(0)+'"] .check-result');
 const absent=async()=>assert.equal(await p.locator('.youtube-status-tip').count(),0);
 for(const theme of ['light','dark']){
  await p.evaluate(t=>ProbeTheme.set(t),theme);
  await tag.hover();await absent();
  await tag.evaluate(n=>n.dispatchEvent(new FocusEvent('focus')));await absent();
  assert.equal(await tag.getAttribute('tabindex'),null);
  assert.equal(await tag.getAttribute('aria-describedby'),null);
  await tag.click();await absent();
  f.f.publish('agent',{agent_id:ID(0),state:{cpu_percent:31,collected_at:Date.now()}});
  await p.waitForFunction(id=>agents.get(id)?.state?.cpu_percent===31,ID(0));await settle(p);await absent();
 }
}));
test('A6 a failed country save is cleared by lookup click and by the next successful save',()=>ui(async f=>{
 const p=f.page,a=f.f.state.items[0];let failSave=true;
 await p.route('**/agents/*/plan',async route=>{if(route.request().method()!=='PUT')return route.continue();if(failSave)return route.fulfill({status:400,json:{error:{message:'Synthetic old save failure'}}});a.plan={...a.plan,...route.request().postDataJSON()};a.country_code=a.plan.country_code_override||'US';await route.fulfill({json:a.plan});});
 await p.route('**/country-code/refresh',async route=>{a.country_code_lookup_operation={operation_id:'synthetic-operation',status:'requested'};await route.fulfill({status:202,json:a.country_code_lookup_operation});});
 await p.evaluate(id=>deviceDetails.open(id,'management'),ID(0));const select=p.locator('[data-focus-key=detail-country]'),controls=p.locator('.country-lookup-controls'),status=controls.locator('[role=status]');
 await select.selectOption('MO');await p.waitForFunction(()=>document.querySelector('.country-lookup-controls [role=status]').textContent.includes('Synthetic old save failure'));await controls.locator('button').click();await p.waitForFunction(()=>!document.querySelector('.country-lookup-controls [role=status]').textContent.includes('Synthetic old save failure'));await p.locator('#action-confirm .primary-action').click();await p.waitForFunction(()=>document.querySelector('.country-lookup-controls button').textContent==='识别中…');a.country_code_lookup_operation={...a.country_code_lookup_operation,status:'succeeded',result_code:'JP'};await p.waitForFunction(()=>document.querySelector('.country-lookup-controls [role=status]').textContent.includes('识别成功：JP'));assert(await status.isVisible());
 await select.selectOption('DE');await p.waitForFunction(()=>document.querySelector('.country-lookup-controls [role=status]').textContent.includes('Synthetic old save failure'));failSave=false;await select.selectOption('US');await p.waitForFunction(()=>!agentState.busy([...agents.keys()][0],'plan'));assert.doesNotMatch(await status.textContent(),/Synthetic old save failure/);assert.match(await status.textContent(),/识别成功：JP/);
}));
