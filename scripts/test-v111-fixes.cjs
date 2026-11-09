const {test,before,after}=require('node:test'),assert=require('node:assert/strict');
const State=require('../web/static/agent-state.js'),M=require('../web/static/dashboard-model.js');
const {launch,open,ID}=require('./lib/v11-panel-fixture.cjs');
let browser;before(async()=>browser=await launch());after(async()=>browser?.close());
const settle=p=>p.waitForFunction(()=>!renderFrame);
async function ui(fn,opts={}){const f=await open(browser,{width:2048,height:1152,deviceScaleFactor:1.25,...opts});try{await fn(f);assert.deepEqual(f.errors,[]);}finally{await f.close();}}
async function seed(f){
 const a=f.f.state.items[0],now=Date.now();a.name='Synthetic-001';a.version='v1.0.9';a.plan.country_code_override='MO';
 a.public_egress_check={supported:true,enabled:true,next_check_at:now+86400000};a.state.public_egress={version:1,supported:true,ipv4:{status:'available',ip:'203.0.113.42',checked_at:now,valid_until:now+86400000},ipv6:{status:'available',ip:'2001:db8::42',checked_at:now,valid_until:now+86400000}};
 await f.page.evaluate(()=>dashboardData.reload());await settle(f.page);
}
module.exports={seed};
test('B3 checked updates, omission, summary not_reported and explicit no-address remain distinct',()=>{
 const s=State.create(),id='a',commit=patch=>s.commitRead(s.read(id),{agent_id:id,...patch});
 const ip='203.0.113.42';commit({state:{public_egress:{ipv4:{checked_at:1,status:'available',ip}}}});
 s.event({agent_id:id,state:{cpu_percent:11}});assert.equal(s.agents.get(id).state.public_egress.ipv4.ip,ip);
 commit({state:{public_egress:{ipv4:{checked_at:0,status:'unknown',reason:'not_reported'}}}});assert.equal(s.agents.get(id).state.public_egress.ipv4.ip,ip);
 commit({state:{public_egress:{ipv4:{checked_at:2,status:'unknown',reason:'timeout',ip:''}}}});assert.equal(s.agents.get(id).state.public_egress.ipv4.ip,ip);
 commit({state:{public_egress:{ipv4:{checked_at:2,status:'available',ip:'203.0.113.43'}}}});assert.equal(s.agents.get(id).state.public_egress.ipv4.ip,'203.0.113.43');
 commit({state:{public_egress:{ipv4:{checked_at:3,status:'unavailable',ip:''}}}});assert.equal(s.agents.get(id).state.public_egress.ipv4.status,'unavailable');assert.equal(s.agents.get(id).state.public_egress.ipv4.ip,'');
});
test('A1 A2 sidebar stays at bottom; one vertical scroll; aligned sticky header and horizontal separator',()=>ui(async({page:p})=>{
 await p.locator('[data-view=list]').click();await settle(p);await p.evaluate(()=>scrollTo(0,document.documentElement.scrollHeight));await p.waitForTimeout(80);
 assert(Math.abs((await p.locator('#dashboard-panel').boundingBox()).y)<1);
 const g=await p.evaluate(()=>{const h=document.querySelector('#agent-list'),t=document.querySelector('.banner-header'),bar=document.querySelector('#dashboard-toolbar');return {scroll:h.scrollHeight-h.clientHeight,header:t.getBoundingClientRect().top,toolbar:bar.getBoundingClientRect().bottom};});assert.equal(g.scroll,0);assert(Math.abs(g.header-g.toolbar)<2);
 await p.evaluate(()=>scrollTo(0,0));await p.setViewportSize({width:1100,height:900});await p.locator('#agent-list').evaluate(n=>n.scrollLeft=250);await p.waitForTimeout(50);
 const cols=await p.evaluate(()=>{const head=document.querySelector('.banner-header [data-column=cpu]'),row=document.querySelector('.banner-row [data-column=cpu]');return [head.getBoundingClientRect().left,row.getBoundingClientRect().left,getComputedStyle(document.querySelector('.banner-row .banner-cell')).boxShadow];});assert(Math.abs(cols[0]-cols[1])<2);assert.notEqual(cols[2],'none');
 await p.locator('[data-view=cards]').click();await p.evaluate(()=>scrollTo(0,document.documentElement.scrollHeight));assert(Math.abs((await p.locator('#dashboard-panel').boundingBox()).y)<1);
},{n:60}));
test('A3 A4 A5 A7 B7 B8 upgrade target, native theme, shared icons, status meaning and manual region',()=>ui(async f=>{
 await seed(f);const p=f.page;await p.locator('[data-view=list]').click();await settle(p);const row=p.locator(`[data-agent-id="${ID(0)}"]`);
 assert.equal(await row.locator('.banner-youtube').textContent(),'MO');assert.equal(await row.locator('.banner-youtube').getAttribute('data-tone'),'crit');assert.equal(await row.locator('.banner-youtube').getAttribute('title'),null);
 assert.match(await row.locator('.country-flag').getAttribute('src'),/us.svg$/);assert.match(await row.locator('.banner-os img').getAttribute('src'),/debian.svg$/);assert.match(await row.locator('.banner-dot').getAttribute('title'),/状态：正常/);
 assert.equal(await p.locator('.status-legend,body>footer').count(),0);assert.equal(await p.locator('#dashboard-sort').evaluate(n=>getComputedStyle(n).colorScheme),'dark');
 await row.locator('.banner-upgrade-link').click();assert.equal(await p.locator('#details-tab-management').getAttribute('aria-selected'),'true');assert(await p.locator('[data-upgrade-region]').isVisible());assert((await p.locator('#details-pane-management').textContent()).includes('YouTube 地区检测'));
},{theme:'dark'}));
test('A6 all non-group sorts share global sections and freeze telemetry ids',()=>{
 const now=Date.now(),agents=new Map([['a',{agent_id:'a',name:'a',group_id:'g'}],['b',{agent_id:'b',name:'b'}]]),catalog={groups:[{id:'g',name:'g',sort_order:0}]};
 for(const sort of Object.keys(M.SORTS)){const p={...M.defaults(),sort,viewMode:'list'},v=M.select(agents,catalog,p,{},now);assert.equal(v.sections.length,sort==='group'?2:1);if(sort!=='group')assert.equal(v.sections[0].id,'__all');}
});
test('B2 B3 B4 masked badges, no raw card attributes, detail survives ten seconds and visible copy feedback',()=>ui(async f=>{
 await seed(f);const p=f.page,card=p.locator(`.agent-card[data-agent-id="${ID(0)}"]`);assert.match(await card.locator('[data-family=ipv4]').getAttribute('title'),/203.0.\*.\*/);
 const attrs=await card.evaluate(n=>[n,...n.querySelectorAll('*')].flatMap(e=>[...e.attributes].map(a=>a.value)).join('\n'));assert(!attrs.includes('203.0.113.42'));assert(!attrs.includes('2001:db8::42'));
 await p.evaluate(id=>deviceDetails.open(id,'overview'),ID(0));await p.waitForFunction(()=>document.querySelector('#device-details .pe-text')?.textContent.includes('203.0.*.*'));
 f.f.state.items[0].state.public_egress={ipv4:{checked_at:0,status:'unknown',reason:'not_reported'},ipv6:{checked_at:0,status:'unknown'}};
 await p.evaluate(()=>dashboardData.reload());await p.waitForTimeout(10050);assert.match(await p.locator('#device-details .pe-text').first().textContent(),/203.0.\*.\*/);
 const copy=p.locator('#device-details .pe-copy').first();await copy.click();await p.waitForFunction(()=>document.querySelector('#device-details .pe-copy')?.textContent==='已复制');assert.equal(await copy.textContent(),'已复制');await p.waitForTimeout(1600);assert.equal(await copy.textContent(),'复制');
 await p.evaluate(()=>{navigator.clipboard.writeText=()=>Promise.reject(Error('synthetic failure'));});await copy.click();await p.waitForFunction(()=>document.querySelector('#device-details .pe-copy')?.textContent==='复制失败');assert.equal(await copy.textContent(),'复制失败');
}));
test('B9 one custom hint, leave, scroll and telemetry dismiss it',()=>ui(async f=>{
 const p=f.page,cell=p.locator('.nq7-b[data-i="10"]').first();await cell.scrollIntoViewIfNeeded();await cell.hover();assert(await p.locator('.nq7-tip:visible').count());assert.equal(await cell.getAttribute('title'),null);assert(await cell.getAttribute('aria-label'));
 await p.mouse.move(0,0);assert.equal(await p.locator('.nq7-tip:visible').count(),0);await cell.hover();await p.evaluate(()=>window.dispatchEvent(new Event('scroll')));assert.equal(await p.locator('.nq7-tip:visible').count(),0);
 await cell.hover();f.f.publish('agent',{agent_id:ID(0),online:true,state:{collected_at:Date.now(),cpu_percent:23}});await p.waitForFunction(()=>document.querySelectorAll('.nq7-tip:not([hidden])').length===0);
}));
