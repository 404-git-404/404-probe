const {test}=require('node:test'),assert=require('node:assert/strict');
const M=require('../web/static/dashboard-model.js'),Overview=require('../web/static/overview.js');
const now=100000,server={version:'v1.0.10'},map=rows=>new Map(rows.map((a,i)=>[a.agent_id||String(i),a]));
const agent=(patch={})=>({agent_id:'a',name:'A',online:true,version:'v1.0.9',revoked:false,disabled_at:null,state:{},resources:{known:true,stale:false,cpu_percent:1,ram_percent:2,disk_percent:3,collected_at:now,max_age_ms:30000},network:{known:true,active:false,monitored:true,expires_at:now+5000},dashboard_traffic:{known:true,stale:false,quota_state:'current'},plan:{quota_bytes:100,usage_bytes:0},renewal:{type:'missing'},search_fields:['A','10.20.*.*'],tag_ids:[],group_id:null,...patch});
const aggregate=rows=>M.aggregate(map(rows.map((a,i)=>({...a,agent_id:String(i)}))),server,now);
test('P5c all active devices count independently of valid samples; paused and revoked never count online',()=>{
 const s=aggregate([agent(),agent({resources:null}),agent({online:false}),agent({disabled_at:1}),agent({revoked:true})]);assert.deepEqual([s.registered,s.online,s.offline,s.paused],[4,2,1,1]);assert.equal(aggregate([]).registered,0);
});
test('P5c >=90 resource threshold requires known fresh finite measurements',()=>{
 const rows=[89.99,90,100].map(cpu=>agent({resources:{known:true,stale:false,cpu_percent:cpu}}));rows.push(agent({resources:{known:false,cpu_percent:99}}),agent({resources:{known:true,stale:true,cpu_percent:99}}),agent({resources:{known:true,stale:false,cpu_percent:'99'}}));assert.equal(aggregate(rows).quick.resources,2);
 assert.equal(M.flags(agent({resources:{known:true,stale:false,cpu_percent:99,collected_at:now-30001,max_age_ms:30000}}),server,now).resources,false);
});
test('P5c missing, expired and null network observations remain unknown; never become healthy',()=>{
 const s=aggregate([agent({network:{known:false,active:null}}),agent({network:{known:true,active:null}}),agent({network:{known:true,active:false,monitored:true,expires_at:now-1}})]);assert.equal(s.health,0);assert.equal(s.insufficient,3);
});

test('P5c a known summary survives the server cache TTL during its throttled refresh window',()=>{
 const a=agent({dashboard_received_at:now,network:{known:true,active:true,monitored:true,expires_at:now+5000}});
 assert.equal(M.flags(a,server,now+10000).network,true);assert.equal(M.flags(a,server,now+11001).network,false);
 assert.equal(M.flags({...a,network:{known:false,active:null}},server,now+1000).network,false);
});
test('P5c traffic >=80% requires current positive quota; partial observations retain the same threshold',()=>{
 const rows=[79,80,100].map(usage=>agent({plan:{quota_bytes:100,usage_bytes:usage,usage_status:'partial'}}));rows.push(agent({plan:{quota_bytes:0,usage_bytes:99}}),agent({plan:{quota_bytes:100,usage_bytes:99},dashboard_traffic:{quota_state:'expired'}}));assert.equal(aggregate(rows).quick.traffic,2);
});
test('P5c health is exactly the deduplicated network/resource/traffic union, independently of offline and renewal',()=>{
 const s=aggregate([agent({network:{known:true,active:true},resources:{known:true,cpu_percent:99},plan:{quota_bytes:100,usage_bytes:99}}),agent({online:false,renewal:{type:'due_soon',days:0}})]);assert.equal(s.health,1);assert.equal(s.quick.offline,1);assert.equal(s.renewal,1);
});
test('P5c formal pending upgrades include unavailable devices and distinguish unknown versions',()=>{
 const s=aggregate([agent({online:false}),agent({disabled_at:1}),agent({version:'v1.0.10'}),agent({version:'v1.0.11'}),agent({version:'dev'})]);assert.equal(s.upgrade,2);assert.equal(s.versionUnknown,1);
});
test('P5c server renewal types separate today/7days, overdue and invalid; nearest uses server days',()=>{
 const rows=[agent({renewal:{type:'due_soon',days:0,date:'2026-10-29'}}),agent({renewal:{type:'due_soon',days:7,date:'2026-11-05'}}),agent({renewal:{type:'overdue',days:-1}}),agent({renewal:{type:'invalid'}})];const s=aggregate(rows);assert.deepEqual([s.renewal,s.overdue,s.renewalInvalid,s.nearest.renewal.days],[2,1,1,0]);
});
test('P5c same dropdown OR, cross-dropdown AND, shortcuts AND; search only searches explicit search_fields',()=>{
 const a=agent({group_id:'g1',tag_ids:['t1'],country_code:'US'}),p=M.defaults();p.groups=['g1','g2'];p.tags=['t0','t1'];p.countries=['US'];assert(M.matches(a,p,server,now));p.statuses=['offline'];assert(!M.matches(a,p,server,now));p.statuses=[];p.search='10.20.30.40';assert(!M.matches(a,p,server,now));p.search='10.20.*.*';assert(M.matches(a,p,server,now));p.quick=['renewal'];assert(!M.matches(a,p,server,now));
});
test('P5c overview uses the complete Map while filters, grouping, few-mode and global sort affect only selection',()=>{
 const data=map([agent({agent_id:'1',name:'B',group_id:'g'}),agent({agent_id:'2',name:'A',renewal:{type:'due_soon',days:1}})]),catalog={groups:[{id:'g',name:'常用',sort_order:0}],tags:[]},p=M.defaults();p.groups=['g'];let view=M.select(data,catalog,p,server,now);assert.equal(view.list.length,1);assert.equal(view.summary.registered,2);p.groups=[];assert.equal(M.select(data,catalog,p,server,now).sections.at(-1).id,'__ungrouped');p.sort='renewal';assert.equal(M.select(data,catalog,p,server,now).list[0].agent_id,'2');assert.equal(M.select(data,{groups:[],tags:[]},p,server,now).simple,true);p.quick=['upgrade'];assert.equal(M.select(data,{groups:[],tags:[]},p,server,now).simple,false);
});
test('P5c preferences are versioned, store IDs and remove deleted catalog IDs; blocked storage is optional',()=>{
 let saved=JSON.stringify({...M.defaults(),groups:['old','g'],tags:['gone'],collapsed:['old','g']});const writes=[],pref=M.preferences(()=>({getItem:()=>saved,setItem:(k,v)=>writes.push([k,v])}));pref.validate({groups:[{id:'g'}],tags:[]});assert.deepEqual(pref.value.groups,['g']);assert.deepEqual(pref.value.tags,[]);assert.deepEqual(pref.value.collapsed,['g']);assert.equal(writes[0][0],M.KEY);assert.doesNotThrow(()=>M.preferences(()=>{throw Error('denied');}).save());
});

test('P5c corrupt preferences fall back to defaults and first loading never writes storage',()=>{
 for(const saved of ['{broken',null,JSON.stringify({version:99,search:'old'})]){const writes=[];const pref=M.preferences(()=>({getItem:()=>saved,setItem:(...args)=>writes.push(args)}));assert.deepEqual(pref.value,M.defaults());pref.validate({groups:[],tags:[]});assert.deepEqual(writes,[]);}
});
test('P5c beta/rc versions are nonformal; orphan group IDs render and filter as ungrouped',()=>{
 const s=aggregate([agent({version:'v1.0.10-beta.1'}),agent({version:'v1.0.10-rc.2'}),agent({version:'nonsense'})]);assert.equal(s.versionNonformal,2);assert.equal(s.versionUnknown,1);
 const p=M.defaults();p.groups=['__ungrouped'];const v=M.select(map([agent({group_id:'gone'})]),{groups:[],tags:[]},p,server,now);assert.equal(v.list.length,1);assert.equal(v.sections[0].id,'__ungrouped');assert.equal(v.sections[0].items.length,1);
});
function ui(){const nodes=new Map();const doc={getElementById:id=>{if(!nodes.has(id))nodes.set(id,{open:true,textContent:'',value:'',listeners:{},classList:{toggle(){}},addEventListener(t,f){this.listeners[t]=f;}});return nodes.get(id);}};const periods=['monthly','yearly'].map(period=>({dataset:{period},listeners:{},setAttribute(){},addEventListener(t,f){this.listeners[t]=f;}}));doc.getElementById('overview-period').querySelectorAll=()=>periods;const preferences={value:M.defaults(),save(){}};return {nodes,api:Overview.create({doc,preferences}),preferences};}
test('P5c loading never claims healthy; unknown samples are explicit and all-known zero is valid',()=>{
 const u=ui(),data=map([agent({resources:null})]),summary=M.aggregate(data,server,now);u.api.update(data,false,{summary});assert.equal(u.nodes.get('overview-health').textContent,'等待读取');u.api.update(data,true,{summary,catalog:{}});assert.equal(u.nodes.get('overview-health').textContent,'暂无已知异常');assert.match(u.nodes.get('overview-unknown').textContent,/其余 1 台暂无最新数据/);u.api.update(map([agent()]),true,{summary:M.aggregate(map([agent()]),server,now),catalog:{}});assert.equal(u.nodes.get('overview-health').textContent,'✓ 全部正常');
});
test('P5c monthly/yearly strings come from Server; shell and collapse state survive snapshots',()=>{
 const u=ui(),shell=u.nodes.get('overview');shell.open=false;shell.listeners.toggle();assert.equal(u.preferences.value.overviewClosed,true);for(let i=0;i<20;i++)u.api.update(map([agent()]),true,{summary:M.aggregate(map([agent()]),server,now),catalog:{expenses:{monthly_usd:'4.76',yearly_usd:'57.14'}}});assert.equal(shell.open,false);assert.equal(u.nodes.get('overview-money').textContent,'$4.76 / 月');u.nodes.get('overview-period').querySelectorAll()[1].listeners.click();assert.equal(u.nodes.get('overview-money').textContent,'$57.14 / 年');
});
