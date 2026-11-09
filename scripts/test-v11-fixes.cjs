const {test,before,after}=require('node:test'),assert=require('node:assert/strict');
const M=require('../web/static/dashboard-model.js'),Management=require('../web/static/management.js'),Card=require('../web/static/card.js');
const {launch,open,ID}=require('./lib/v11-panel-fixture.cjs');
let browser;before(async()=>browser=await launch());after(async()=>browser?.close());
const settle=p=>p.waitForFunction(()=>!renderFrame),ids=p=>p.locator('.banner-row').evaluateAll(ns=>ns.map(n=>n.dataset.agentId));
async function ui(fn,options={}){const f=await open(browser,{preferences:{viewMode:'list'},...options});try{await fn(f);assert.deepEqual(f.errors,[]);}finally{await f.close();}}

test('frozen ids retain fresh DTOs through unchanged live order, reordered input, additions and removals',()=>{
 const order=M.createOrder(),p=M.defaults(),catalog={groups:[{id:'g',name:'g',sort_order:0}]};
 const apply=items=>order.apply({list:items,sections:[{id:'g',items}]},catalog,p).sections[0].items;
 apply([{agent_id:'a'},{agent_id:'b'}]);const a={agent_id:'a',resources:{cpu_percent:99}},b={agent_id:'b',resources:{cpu_percent:7}},c={agent_id:'c'};
 assert.equal(apply([a,b])[0],a);assert.equal(apply([b,a])[0],a);
 assert.deepEqual(apply([c,b,a]),[a,b,c]);assert.deepEqual(apply([c,b]),[b,c]);
});

test('monthly anchor covers before/today/after, 31st, short months without changing the selected day, January rollover and plan timezone',()=>{
 for(const [today,day,want]of [['2026-11-02',1,'2026-11-01'],['2026-11-02',2,'2026-11-02'],['2026-11-02',30,'2026-10-30'],['2026-03-02',31,'2026-01-31'],['2024-03-02',31,'2024-01-31'],['2026-02-28',28,'2026-02-28'],['2026-02-28',31,'2026-01-31'],['2026-01-02',31,'2025-12-31'],['2026-12-31',31,'2026-12-31']])assert.equal(Management.monthlyQuotaAnchor(day,Date.parse(today+'T12:00:00Z'),'UTC'),want);
 assert.equal(Management.monthlyQuotaAnchor(2,Date.parse('2026-11-02T00:30:00Z'),'America/Los_Angeles'),'2026-10-02');
 for(const day of [0,32,1.5,''])assert.equal(Management.monthlyQuotaAnchor(day),'');
});
test('future quota has a start date on both card faces; absent quota offers settings',()=>{
 const plan={traffic_mode:'sum',cycle_kind:'monthly',cycle_anchor:'2027-02-28',quota_bytes:5e12};
 assert.equal(Card.trafficView({plan}).bar.text,'周期 2 月 28 日开始 · 配额 5.00 TB');assert.equal(Card.resetText(plan),'周期 2 月 28 日开始');
 assert.equal(Card.trafficView({}).bar.text,'未设置流量配额 · 点此设置');
});
test('USD price sort matches expense basis, zero renewal, purchase fallback, nulls and exact decimal ordering',()=>{
 const plans=[{currency:'CNY',renewal_price:'71',renewal_price_period:'yearly'},{currency:'USD',renewal_price:'1',renewal_price_period:'monthly'},{currency:'USD',renewal_price:'0',renewal_price_period:'monthly',purchase_price:'99'},{currency:'EUR',purchase_price:'6',purchase_price_period:'quarterly'},{currency:'JPY',renewal_price:'2',renewal_price_period:'monthly'},{currency:'USD',renewal_price:'1',renewal_price_period:'once'},{}];
 const rows=plans.map((plan,i)=>({agent_id:String(i),name:String(i),plan})),p={...M.defaults(),sort:'price'},catalog={groups:[],tags:[],exchange_rates:[{currency:'CNY',units_per_usd:'7.1'},{currency:'EUR',units_per_usd:'2'}]};
 assert.deepEqual([...rows].sort(M.comparator(p,null,catalog)).map(a=>a.agent_id),['2','0','1','3','4','5','6']);
 p.sortDir='desc';assert.deepEqual([...rows].sort(M.comparator(p,null,catalog)).map(a=>a.agent_id),['1','3','0','2','4','5','6']);
 const tiny=[{agent_id:'a',name:'a',plan:{currency:'AAA',renewal_price:'999999999999.001',renewal_price_period:'monthly'}},{agent_id:'b',name:'b',plan:{currency:'BBB',renewal_price:'999999999999.001',renewal_price_period:'monthly'}}];
 catalog.exchange_rates=[{currency:'AAA',units_per_usd:'999999999999.000000000001'},{currency:'BBB',units_per_usd:'999999999999.000000000002'}];p.sortDir='asc';assert.equal(tiny.sort(M.comparator(p,null,catalog))[0].agent_id,'b');
});
test('resource display/sorting share lifecycle, stale and max-age boundaries; all old values sort last both directions',()=>{
 const now=100000,base={online:true,resources:{known:true,collected_at:90000,max_age_ms:10000,cpu_percent:20},state:{collected_at:90001,cpu_percent:30,rx_rate:10,tx_rate:20}};
 const rows=[{...base,agent_id:'fresh',name:'z'},...['offline','paused','stale','expired'].map((name,i)=>({...base,agent_id:name,name,online:i===0?false:true,disabled_at:i===1?1:null,resources:{...base.resources,cpu_percent:1,stale:i===2,collected_at:i===3?89999:90000}}))];
 for(const sort of ['cpu','rate'])for(const sortDir of ['asc','desc'])assert.equal([...rows].sort(M.comparator({...M.defaults(),sort,sortDir},null,null,now))[0].agent_id,'fresh');
 assert.equal(M.resourcesOld(rows[0],now),false);assert.equal(M.resourcesOld(rows[4],now),true);
});
test('cards and list freeze live ids; latency slot and explicit return to list resort',()=>ui(async({page:p})=>{
 await p.locator('#dashboard-sort').selectOption('latency');await settle(p);const before=await ids(p);
 await p.evaluate(()=>{let i=100;for(const a of agents.values()){a.network_slots[0].latest_ms=i--;a.network_slots[2].latest_ms=i; }render();});await settle(p);assert.deepEqual(await ids(p),before);
 await p.locator('.banner-slot').click();await p.locator('[data-slot="3"]').click();await settle(p);assert.notDeepEqual(await ids(p),before);
 await p.locator('[data-view=cards]').click();await p.locator('#dashboard-sort').selectOption('cpu');await settle(p);const cardIDs=()=>p.locator('.agent-card').evaluateAll(ns=>ns.map(n=>n.dataset.agentId)),frozen=await cardIDs();
 await p.evaluate(()=>{let i=99;for(const a of agents.values()){a.state.cpu_percent=i--;a.resources.cpu_percent=a.state.cpu_percent;}render();});await settle(p);assert.deepEqual(await cardIDs(),frozen);
 await p.locator('[data-view=list]').click();await settle(p);assert.equal((await ids(p))[0],ID(27));
}));
test('table semantics, unique named details buttons, keyboard access, page-sticky header and horizontal scrolling',()=>ui(async({page:p})=>{
 assert.equal(await p.getByRole('table',{name:'设备列表',exact:true}).count(),1);assert.equal(await p.getByRole('columnheader').count(),12);
 const name=p.locator('.banner-name').first();assert.equal(await name.getAttribute('aria-label'),'查看 '+await name.textContent()+' 详情');assert.equal(await p.locator('.banner-row[tabindex],.banner-row[role=button]').count(),0);
 await name.focus();await p.keyboard.press('Enter');assert(await p.locator('#device-details').isVisible());await p.keyboard.press('Escape');assert(await name.evaluate(n=>n===document.activeElement));
 const host=p.locator('#agent-list');assert(await host.evaluate(n=>n.scrollHeight===n.clientHeight&&n.scrollWidth>n.clientWidth));
 assert.equal(await host.evaluate(n=>getComputedStyle(n.querySelector('.banner-cell')).boxShadow),'none');
 await host.evaluate(n=>n.scrollLeft=240);await p.evaluate(()=>scrollTo(0,1000));await p.waitForFunction(()=>document.querySelector('#agent-list').classList.contains('is-scrolled'));
 const geometry=await p.evaluate(()=>({header:document.querySelector('.banner-header').getBoundingClientRect().top,toolbar:document.querySelector('#dashboard-toolbar').getBoundingClientRect().bottom,shadow:getComputedStyle(document.querySelector('#agent-list .banner-cell')).boxShadow}));assert(Math.abs(geometry.toolbar-geometry.header)<2);assert.notEqual(geometry.shadow,'none');
},{width:1100,height:900,n:200}));
test('toolbar tags collapse +N, preserve OR/AND filtering and clear; list and cards share controls',()=>ui(async({page:p,f})=>{
 f.state.tags.push(...Array.from({length:16},(_,i)=>({id:ID(500+i),name:'长标签 '+i})));f.state.revision++;await p.evaluate(()=>dashboardData.reload());await settle(p);await p.waitForFunction(()=>!document.querySelector('#dashboard-tags-more').hidden);
 assert.equal(await p.locator('#dashboard-panel #dashboard-tags').count(),0);assert.equal(await p.locator('#dashboard-toolbar #dashboard-tags').count(),1);
 await p.locator('#dashboard-tags .dashboard-options .dashboard-option').filter({hasText:'GIA'}).click();await settle(p);assert.equal(await p.locator('#dashboard-count').textContent(),'显示 7 / 28 台');
 await p.locator('#dashboard-tags-more summary').click();const extra=p.locator('.dashboard-tag-overflow .dashboard-option').last();await extra.click();await settle(p);assert.equal(await p.locator('#dashboard-count').textContent(),'显示 7 / 28 台');
 await p.locator('#dashboard-tags-more summary').click();await p.locator('#dashboard-groups .dashboard-option').filter({hasText:'常用'}).click();await settle(p);assert.equal(await p.locator('#dashboard-count').textContent(),'显示 3 / 28 台');
 await p.locator('#dashboard-tags-clear').click();await settle(p);assert.deepEqual(await p.evaluate(()=>dashboardUI.preferences.value.tags),[]);assert.equal(await p.locator('#dashboard-count').textContent(),'显示 3 / 28 台');
 await p.locator('[data-view=cards]').click();assert(await p.locator('#dashboard-tags').isVisible());
},{width:1440}));
test('phone list preference renders cards then restores list on tablet; temporary panel expansion leaves saved collapse intact',()=>ui(async({page:p})=>{
 await p.setViewportSize({width:390,height:844});await p.waitForFunction(()=>document.querySelectorAll('#agents .agent-card').length===28&&!renderFrame);assert(await p.locator('#agent-list').isHidden());assert(await p.locator('#phone-view-note').isVisible());assert.equal(await p.evaluate(()=>JSON.parse(localStorage.getItem('404-probe.dashboard.v1')).viewMode),'list');assert(await p.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 await p.setViewportSize({width:760,height:900});await p.waitForFunction(()=>!document.querySelector('#agent-list').hidden&&!renderFrame);assert(await p.locator('#phone-view-note').isHidden());
 await p.setViewportSize({width:1920,height:1000});await p.locator('#panel-collapse').click();await p.setViewportSize({width:1100,height:900});await p.locator('#panel-collapse').click();assert.equal(await p.locator('.panel-collapsed').count(),0);assert.equal(await p.evaluate(()=>JSON.parse(localStorage.getItem('404-probe.dashboard.v1')).panelCollapsed),true);await p.setViewportSize({width:1920,height:1000});await p.waitForFunction(()=>document.querySelector('.panel-collapsed'));assert.equal(await p.locator('.panel-collapsed').count(),1);
}));
test('offline/paused rows dim only nonidentity cells and graph colors differ from body text; rem controls remain circular',()=>ui(async({page:p})=>{
 for(const theme of ['light','dark']){await p.evaluate(t=>ProbeTheme.set(t),theme);const offline=p.locator(`.banner-row[data-agent-id="${ID(3)}"]`);assert.match(await offline.locator('[data-column=name]').textContent(),/离线 10 分钟/);assert.equal(await offline.locator('[data-column=cpu]').evaluate(n=>getComputedStyle(n).opacity),'0.42');assert.equal(await offline.locator('[data-column=name]').evaluate(n=>getComputedStyle(n).opacity),'1');assert.equal(await offline.locator('.banner-dot').evaluate(n=>getComputedStyle(n).backgroundColor),theme==='light'?'rgb(179, 58, 42)':'rgb(234, 107, 87)');}
 for(const percent of [125,150]){await p.evaluate(s=>document.documentElement.style.fontSize=s+'%',percent);for(const id of ['.theme-toggle','#panel-settings','#logout'])assert(await p.locator(id).evaluate(n=>{const r=n.getBoundingClientRect();return Math.abs(r.width-r.height)<1;}));}
}));
test('quota bar always edits quota; plan links to quota; three interval labels and future start are visible',()=>ui(async({page:p,f})=>{
 await p.locator('[data-view=cards]').click();const card=p.locator(`.agent-card[data-agent-id="${ID(0)}"]`);await card.locator('.c7-quota').click();assert.equal(await p.locator('#plan-title').textContent(),'流量配额');
 for(const [kind,label,help]of [['monthly','每隔几个月','每月固定某日清零'],['yearly','每隔几年','按年给流量的套餐'],['days','每隔几天','从起点起每隔 N 天，不按日历月']]){await p.locator('#plan-cycle-kind').selectOption(kind);assert.equal(await p.locator('#quota-cycle-count-label').textContent(),label);assert.equal(await p.locator('#quota-cycle-help').textContent(),help);}
 await p.locator('#plan-cycle-kind').selectOption('monthly');await p.locator('#quota-cycle-day').fill('30');const want=await p.evaluate(()=>Management.monthlyQuotaAnchor(30,Date.now(),'UTC'));assert.equal(await p.locator('#plan-cycle-anchor').inputValue(),want);
 await p.locator('#plan-cancel').click();if(await p.locator('#plan-confirmation').isVisible())await p.locator('#plan-discard').click();
 await card.locator('.edit-plan').click();assert.equal(await p.locator('#plan-title').textContent(),'套餐与续费');await p.locator('#plan-open-quota').click();assert.equal(await p.locator('#plan-title').textContent(),'流量配额');await p.locator('#plan-cancel').click();
 await p.evaluate(id=>{const a=agents.get(id);a.plan={...a.plan,cycle_start:null,cycle_end:null,cycle_anchor:'2027-02-28'};render(new Set([id]));},ID(0));await settle(p);assert.match(await card.locator('.c7-quota-empty').textContent(),/周期 2 月 28 日开始 · 配额 5 TB/);await card.locator('.c7-quota-empty').click();assert.equal(await p.locator('#plan-title').textContent(),'流量配额');
},{preferences:{viewMode:'cards'}}));
