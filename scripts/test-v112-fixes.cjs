const {test,before,after}=require('node:test'),assert=require('node:assert/strict');
const {launch,open,ID}=require('./lib/v11-panel-fixture.cjs');
const Y=require('../web/static/youtube-status.js'),M=require('../web/static/management.js');
let browser,currentFixture;before(async()=>browser=await launch());after(async()=>browser?.close());
const settle=p=>p.waitForFunction(()=>!renderFrame);
async function ui(run,options={}){const f=await open(browser,{n:2,width:2048,height:1152,deviceScaleFactor:1.25,...options});try{currentFixture=f;await run(f);assert.deepEqual(f.errors,[]);}finally{await f.close();}}
function contrast(a,b){const lum=c=>c.match(/[\d.]+/g).slice(0,3).map(Number).map(x=>{x/=255;return x<=.04045?x/12.92:((x+.055)/1.055)**2.4;}).reduce((s,x,i)=>s+x*[.2126,.7152,.0722][i],0),x=lum(a),y=lum(b);return (Math.max(x,y)+.05)/(Math.min(x,y)+.05);}
module.exports={contrast};
test('F1 detected YouTube region ignores egress override; plan and quota saves preserve override',()=>{
 for(const manual of ['', 'MO','CN']){const a={online:true,plan:{country_code_override:manual},google_status:{supported:true,checked_at:Date.now(),result:{youtube:{status:'not_cn',region:'US'}}}};assert.equal(Y.display(a).code,'US');assert.equal(Y.display(a).premium,'available');a.google_status.result.youtube={status:'cn',region:'CN'};assert.equal(Y.display(a).premium,'unavailable');
 for(const section of ['plan','clear','quota']){const payload=M.planSectionPayload(a.plan,section,{country_code_override:'JP'},{enabled:false});assert.equal(payload.country_code_override,manual);}}
});
test('F1 card/list share detection and CN stays unavailable in both themes',()=>ui(async f=>{
 const [a,b]=f.f.state.items;a.plan.country_code_override='MO';a.country_code='MO';b.google_status.result.youtube={status:'cn',region:'CN'};await f.page.evaluate(()=>dashboardData.reload());
 for(const theme of ['light','dark']){await f.page.evaluate(t=>ProbeTheme.set(t),theme);for(const view of ['cards','list']){await f.page.locator(`[data-view=${view}]`).click();await settle(f.page);const tags=i=>f.page.locator(`[data-agent-id="${ID(i)}"] ${view==='cards'?'.check-result':'.banner-youtube'}`);assert.equal(await tags(0).textContent(),view==='cards'?'地区 US':'US');assert.equal(await tags(1).getAttribute(view==='cards'?'data-premium':'data-tone'),view==='cards'?'unavailable':'crit');}}
}));

test('F1 saving a real plan dialog never promotes automatic egress to a manual override',()=>ui(async f=>{
 const p=f.page,a=f.f.state.items[0];
 for(const override of ['', 'MO']){a.plan.country_code_override=override;a.country_code=override||'US';a.country_source=override?'manual':'automatic';await p.evaluate(()=>dashboardData.reload());await p.evaluate(id=>openPlanDialog(agents.get(id)),ID(0));await p.locator('#plan-renewal-price').fill('8');await p.locator('#plan-save').click();await p.waitForFunction(()=>!document.querySelector('#plan-dialog').open);const request=f.f.requests.filter(r=>r.path.endsWith('/plan')&&r.method==='PUT').at(-1);assert.equal(request.body.country_code_override,override);assert.equal(a.plan.country_code_override,override);}
},{handle:async({url,req,body,json})=>{if(req.method==='PUT'&&url.pathname.endsWith('/plan')){currentFixture.f.state.items[0].plan={...currentFixture.f.state.items[0].plan,...body};json(currentFixture.f.state.items[0].plan);return true;}return false;}}));
test('F2 every custom hover surface uses readable paired colors in both themes',()=>ui(async f=>{
 const p=f.page,now=Date.now();f.f.state.items[0].state.public_egress={ipv4:{status:'available',ip:'203.0.113.42',checked_at:now,valid_until:now+86400000}};await p.evaluate(()=>dashboardData.reload());await settle(p);
 for(const theme of ['light','dark']){await p.evaluate(t=>ProbeTheme.set(t),theme);const colors=await p.evaluate(()=>[...document.querySelectorAll('.nq7-tip,.lm7-tip,.name-tooltip,.public-egress-tip,.youtube-status-tip')].map(n=>{const s=getComputedStyle(n);return {kind:n.className,color:s.color,background:s.backgroundColor};}));assert(colors.length>=8);for(const c of colors)assert(contrast(c.color,c.background)>=4.5,JSON.stringify(c));console.log('TOOLTIP_COLORS '+theme+' '+JSON.stringify(colors));}
 const cell=p.locator('.nq7-b[data-i="10"]').first();await cell.hover();assert.equal(await p.locator('.nq7-tip:visible').count(),1);await p.mouse.move(0,0);assert.equal(await p.locator('.nq7-tip:visible').count(),0);await cell.hover();await p.evaluate(()=>dispatchEvent(new Event('scroll')));assert.equal(await p.locator('.nq7-tip:visible').count(),0);await cell.hover();f.f.publish('agent',{agent_id:ID(0),state:{cpu_percent:24,collected_at:Date.now()}});await p.waitForFunction(()=>!document.querySelector('.nq7-tip:not([hidden])'));
}));
test('F3 entire upgrade cell responds 50 times under SSE and supports Enter without replacing button',()=>ui(async f=>{
 const p=f.page;await p.locator('[data-view=list]').click();await settle(p);const cell=p.locator(`.banner-row[data-agent-id="${ID(0)}"] [data-column=upgrade]`),button=cell.locator('button');await button.evaluate(n=>window.__upgradeButton=n);assert.equal(await button.evaluate(n=>getComputedStyle(n).cursor),'pointer');const timer=setInterval(()=>f.f.publish('agent',{agent_id:ID(0),state:{cpu_percent:Math.random()*20,collected_at:Date.now()}}),10);
 try{for(let i=0;i<50;i++){const box=await cell.boundingBox();await p.mouse.click(box.x+2+(i%2)*(box.width-4),box.y+2+(i%2)*(box.height-4));await p.waitForFunction(()=>document.querySelector('#device-details').open);assert.equal(await p.locator('#details-tab-management').getAttribute('aria-selected'),'true');assert(await p.locator('[data-upgrade-region]').isVisible());await p.keyboard.press('Escape');await p.waitForFunction(()=>!document.querySelector('#device-details').open);}await button.focus();await p.keyboard.press('Enter');assert(await p.locator('#device-details').isVisible());assert(await button.evaluate(n=>n===window.__upgradeButton));}finally{clearInterval(timer);}
}));
test('F4 pending detail-only country operations finish on success/failure/timeout; YouTube resets too',()=>ui(async f=>{
 const p=f.page,a=f.f.state.items[0];
 // Match the product summary contract: operation state is absent from dashboard rows.
 await p.route('**/api/v1/web/dashboard*',async route=>{const response=await route.fetch(),data=await response.json();for(const row of data.items)delete row.country_code_lookup_operation;await route.fulfill({response,json:data});});
 await p.evaluate(id=>deviceDetails.open(id,'management'),ID(0));const control=p.locator('.country-lookup-controls'),button=control.locator('button');
 for(const status of ['succeeded','failed','unknown']){
  await button.click();await p.locator('#action-confirm .primary-action').click();await p.waitForFunction(()=>document.querySelector('.country-lookup-controls button').textContent==='识别中…');
  a.country_code_lookup_operation={...a.country_code_lookup_operation,status,result_code:status==='succeeded'?'JP':'',error_code:status==='failed'?'network_error':''};if(status==='succeeded')a.country_code='JP';
  await p.waitForFunction(()=>document.querySelector('.country-lookup-controls button').textContent==='重新识别');assert.equal(await button.isEnabled(),true);assert(await control.locator('[role=status]').isVisible());assert.match(await control.textContent(),status==='succeeded'?/成功：JP/:status==='failed'?/失败/:/超时/);
 }
 const google=p.locator('[data-focus-key=recheck]');await google.click();await p.waitForFunction(()=>document.querySelector('[data-focus-key=recheck]').textContent==='检测中…');a.google_status.pending=false;a.google_status.result.youtube={status:'not_cn',region:'DE'};await p.waitForFunction(()=>document.querySelector('[data-focus-key=recheck]').textContent==='重新检测');assert(await google.isEnabled());
},{handle:async({url,req,json})=>{
 if(req.method==='POST'&&url.pathname.endsWith('/country-code/refresh')){const operation={operation_id:'synthetic-operation',status:'requested',updated_at:Date.now()};currentFixture.f.state.items[0].country_code_lookup_operation=operation;json(operation,202);return true;}
 if(req.method==='POST'&&url.pathname.endsWith('/google-status')){currentFixture.f.state.items[0].google_status.pending=true;json({},202);return true;}return false;
}}));
