// Test exported production models plus actual module DOM/event callbacks, not a UI reimplementation.
const {test}=require('node:test'),assert=require('node:assert/strict');
const C=require('../web/static/network-quality-core.js'),UI=require('../web/static/network-quality.js');
const State=require('../web/static/agent-state.js');global.AgentState=State;
const Management=require('../web/static/management.js');
const flush=async()=>{for(let i=0;i<12;i++)await new Promise(resolve=>setImmediate(resolve));};
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};};
const id='a'.repeat(32),targetID='b'.repeat(32),revision='9223372036854775806';
const config=()=>({version:1,revision,supported:true,enabled:true,ipv6:true,targets:C.slots.flatMap((slot,i)=>['ipv4','ipv6'].map(family=>({id:i===0?targetID:String(i).repeat(32),slot,family,slot_revision:revision,source:'catalog',region:'北京',protocol:i===1?'icmp':'tcp',host:'synthetic.invalid',port:i===1?0:443,status:family==='ipv6'&&i===2?'unavailable':'active'})))});
const stats={count:2,attempts:2,successes:2,failures:0,not_executed:0,sent:2,received:2,failure_percent:0,packet_loss_percent:0,p50_ms:0,p95_ms:30,delay_variation_ms:30,categories:{success:2}};
const history=(target,overrides={})=>({target,now:1800000,latest_success:{started_at:1799900,latency_ms:0},latest_failure:null,stale:false,partial:false,gap_reason:'unknown_gap',completeness:'unverified',recent_5min:stats,window:stats,blocks:Array.from({length:60},(_,i)=>({...stats,start:i*60000,end:(i+1)*60000,gap:i%4===0,in_progress:i===59})),recent_blocks:Array.from({length:20},(_,i)=>({...stats,start:i*60000,end:(i+1)*60000,gap:i%4===0})),...overrides});
function dom(){
 const doc={hidden:false,activeElement:null,listeners:new Map()};
 class Element{
  constructor(tag){this.tagName=tag.toUpperCase();this.children=[];this.dataset={};this.attrs={};this.style={};this.listeners=new Map();this.isConnected=true;this.value='';this.rect={top:10,bottom:100,left:10,right:400,width:390};}
  setAttribute(k,v){this.attrs[k]=v;}append(...nodes){for(const n of nodes){if(n.parent)n.parent.children=n.parent.children.filter(x=>x!==n);n.parent=this;this.children.push(n);}}
  get firstElementChild(){return this.children[0]||null;}get lastElementChild(){return this.children.at(-1)||null;}
  get parentNode(){return this.parent||null;}get nextElementSibling(){const a=this.parent?.children||[];return a[a.indexOf(this)+1]||null;}
  insertBefore(node,before){if(node===before)return node;if(node.parent)node.parent.children=node.parent.children.filter(n=>n!==node);node.parent=this;node.isConnected=true;const i=before?this.children.indexOf(before):-1;if(i<0)this.children.push(node);else this.children.splice(i,0,node);return node;}
  contains(n){return n===this||this.children.some(c=>c.contains(n));}
  replaceChildren(...nodes){if(this.contains(doc.activeElement))doc.activeElement=doc.body;this.children=[];this.append(...nodes);}
  remove(){if(this.parent)this.parent.children=this.parent.children.filter(n=>n!==this);this.isConnected=false;}
  addEventListener(t,f){const a=this.listeners.get(t)||[];a.push(f);this.listeners.set(t,a);}removeEventListener(t,f){this.listeners.set(t,(this.listeners.get(t)||[]).filter(x=>x!==f));}
  dispatch(t,event={}){for(const f of this.listeners.get(t)||[])f({target:this,currentTarget:this,preventDefault(){},...event});}
  click(){this.focus();this.dispatch('click');}focus(){doc.activeElement=this;}setSelectionRange(a,b){this.selectionStart=a;this.selectionEnd=b;}
  showModal(){this.open=true;this.querySelector('button')?.focus();}close(){this.open=false;this.dispatch('close');}getBoundingClientRect(){return this.rect;}
  querySelectorAll(selector){const all=this.children.flatMap(n=>[n,...n.querySelectorAll('*')]);return all.filter(n=>selector==='*'||selector.split(',').some(s=>s===n.tagName.toLowerCase()||s.startsWith('.')&&n.className?.split(' ').includes(s.slice(1))||s.startsWith('[data-')&&(()=>{const m=s.match(/\[data-([^=]+)="([^"]+)"\]/);if(!m)return false;const k=m[1].replace(/-([a-z])/g,(_,c)=>c.toUpperCase());return n.dataset[k]===m[2];})()));}
  querySelector(s){return this.querySelectorAll(s)[0];}
 }
 doc.body=new Element('body');doc.createElement=t=>new Element(t);const dialog=new Element('dialog');doc.body.append(dialog);doc.querySelector=()=>dialog;
 doc.addEventListener=(t,f)=>{const a=doc.listeners.get(t)||[];a.push(f);doc.listeners.set(t,a);};doc.removeEventListener=(t,f)=>doc.listeners.set(t,(doc.listeners.get(t)||[]).filter(x=>x!==f));
 doc.dispatch=(t,event={})=>{for(const f of doc.listeners.get(t)||[])f(event);};return {doc,dialog};
}
function fixture(options={}){
 const {doc,dialog}=dom(),requests=[],writes=[],plots=[];let cfg=config(),putStatus=200,late=null,one=false;
 const regions=[{id:'北京',label:'北京',province:'北京',available:{telecom:{ipv4:true,ipv6:true},unicom:{ipv4:true,ipv6:false},mobile:{ipv4:false,ipv6:false}}},{id:'上海',label:'上海',province:'上海',available:{}}];
 const fetcher=async(url,init)=>{
  requests.push({url,method:init.method||'GET'});if(options.transport)return options.transport(url,init);
  let value,status=200;
  if(url.endsWith('/catalog'))value={regions};
  else if(url.endsWith('/config')){if(init.method==='PUT'){writes.push(JSON.parse(init.body));if(options.writeDelay)await options.writeDelay.promise;status=putStatus;value=cfg;}else value=cfg;}
  else {const query=new URL(url,'http://local').searchParams;const t=cfg.targets.find(t=>t.id===query.get('target_id')&&t.family===(options.family||'ipv4'))||cfg.targets[0];value=history(t,options.stale?{stale:true}:{});
   if(one)value.blocks=value.blocks.map((b,i)=>({...b,gap:i!==12}));if(late){const l=late;late=null;await l.promise;}}
  return {ok:status===200,status,statusText:String(status),json:async()=>value};
 };
 class Plot{constructor(opts,data){this.opts=opts;this.data=data;plots.push(this);}destroy(){this.destroyed=true;}setSize(){}}
 const ui=UI.create({document:doc,fetcher,csrf:()=> 'synthetic',state:State,management:Management,Plot,Observer:null,Resize:null,interval:()=>1,clear:()=>{}});
 const agent={agent_id:id,name:'合成设备'},node=ui.mount(agent);doc.body.append(node);
 const visible=()=>ui.setVisible(id,true);
 const panel=()=>doc.body.children.find(n=>n.className==='nq-popover');
 const byText=(node,text)=>node.querySelectorAll('button').find(n=>n.textContent===text);
 return {ui,doc,dialog,node,agent,requests,writes,plots,visible,panel,byText,config:()=>cfg,setConfig:value=>cfg=value,setStatus:v=>putStatus=v,setLate:l=>late=l,setOne:()=>one=true};
}
test('N3 production adapters: zero != no denominator; latest failure/stale/paused; exact fraction and minute times',()=>{
 assert.equal(C.ms(0),'0.0 ms');assert.equal(C.ms(null),'—');assert.equal(C.ratio({...stats,attempts:0},'tcp').value,null);
 assert.equal(C.ratio({...stats,sent:0},'icmp').value,null);assert.equal(C.ratio({...stats,attempts:8,failures:2,failure_percent:25},'tcp').fraction,'2 / 8');
 const t=config().targets[0];assert.equal(C.current(history(t),t).text,'0.0 ms');assert.equal(C.current(history(t,{stale:true}),t).text,'旧数据');
 assert.equal(C.current(history(t,{latest_failure:{started_at:1800000,outcome:'timeout'}}),t).text,'最近失败');assert.equal(C.current(history(t),{...t,status:'paused'}).text,'已暂停');
 assert.match(C.blockText({...stats,start:60000,end:120000,in_progress:true,gap:true},'tcp'),/进行中.*缺测/);
});
test('N3 production draft mode has no side effect, independent region and manual protocol retain exact string revision',()=>{
 const d=C.draft(config()),s=C.chooseRegion(d,'unicom','上海');assert.equal(s.choices[0].region,'北京');assert.equal(s.choices[2].region,'北京');assert.equal(d.choices[1].region,'北京');
 assert.ok(C.chooseRegion(s,'all','上海').choices.every(c=>c.region==='上海'));
 s.choices[0]={slot:'telecom',source:'manual',protocol:'icmp',host:'203.0.113.8',port:443};assert.equal(C.payload(s,revision).choices[0].port,0);assert.equal(C.payload(s,revision).expected_revision,revision);
});
test('N3 actual plot adapter sorts unique real seconds, equal length finite/null and gap null',()=>{
 const adapted=C.columns([{...stats,start:120000,end:180000,gap:false},{...stats,start:0,end:60000,gap:true},{...stats,start:120000,end:180000},{...stats,start:60000,end:120000,p95_ms:Infinity}], 'tcp','latency');
 assert.deepEqual(adapted.data[0],[0,60,120]);assert.deepEqual(adapted.data[1],[null,0,0]);assert.deepEqual(adapted.data[2],[null,null,30]);
 assert.ok(adapted.data.every(c=>c.length===3));assert.notEqual(C.historyKey(id,targetID,'ipv4',1),C.historyKey(id,targetID,'ipv6',1));
});
test('N3 actual coordinator: single key, shared owners and bounded queued intent',async()=>{
 const q=C.coordinator(),pending=Array.from({length:6},deferred),owner={},other={};let calls=0;
 const promises=pending.map((p,i)=>q.request(String(i),owner,()=>{calls++;return p.promise;}));for(const p of promises)p.catch(()=>{});assert.equal(q.snapshot().active,4);assert.equal(calls,4);
 assert.equal(q.request('0',other,()=>{throw Error('duplicate');}),promises[0]);q.release(owner,new Set(['0','4','5']));
 pending[1].resolve(1);pending[2].resolve(2);pending[3].resolve(3);await flush();assert.ok(q.snapshot().active<=4);
 for(const p of pending)p.resolve(0);await Promise.allSettled(promises);q.close();
});
test('N3 real helper lease: 4 aborted but unresolved fetches block fifth; latest queued intent starts only after actual exit',async()=>{
 const q=C.coordinator(),transports=Array.from({length:5},deferred),owners=Array.from({length:5},()=>({}));let started=0;
 const request=i=>q.request(String(i),owners[i],signal=>C.transportLease(fetcher=>State.fetchJSON('/local',{signal,fetcher}),()=>{started++;return transports[i].promise;}));
 const promises=[0,1,2,3].map(request);for(const p of promises)p.catch(()=>{});await flush();assert.equal(started,4);
 owners.slice(0,4).forEach(o=>q.release(o));await Promise.allSettled(promises);assert.equal(q.snapshot().active,4);
 const fifth=request(4);await flush();assert.equal(started,4);
 transports[0].resolve({ok:true,status:200,body:{cancel:async()=>{}},json:async()=>({})});await flush();assert.equal(started,5);
 for(let i=1;i<5;i++)transports[i].resolve({ok:true,status:200,json:async()=>({i})});assert.deepEqual(await fifth,{i:4});await flush();q.close();
});
test('N3 real helper lease: hung JSON timeout rejects UI but holds transport/body slot',async()=>{
 const q=C.coordinator({limit:1}),body=deferred(),o={};let calls=0;
 const first=q.request('first',o,signal=>C.transportLease(fetcher=>State.fetchJSON('/local',{signal,fetcher,timeoutMs:15}),async()=>({ok:true,status:200,json:()=>body.promise})));
 await assert.rejects(first,{name:'TimeoutError'});const second=q.request('second',o,()=>{calls++;return 'done';});await flush();assert.equal(calls,0);assert.equal(q.snapshot().active,1);
 body.resolve({});assert.equal(await second,'done');q.close();
});
test('N3 coordinator bounded failure backoff no tight loop and retired key cleanup',async()=>{
 let at=100,calls=0;const q=C.coordinator({now:()=>at}),o={};const run=()=>{calls++;throw Error('503');};
 await assert.rejects(q.request('x',o,run));await flush();await assert.rejects(q.request('x',o,run),{name:'BackoffError'});assert.equal(calls,1);
 at+=30000;await assert.rejects(q.request('x',o,run));await flush();assert.equal(calls,2);q.release(o);assert.equal(q.snapshot().keys,0);q.close();
});
test('N3 actual card callbacks: visible first, current family three targets, no config on metadata refresh, hidden stops',async(t)=>{
 const f=fixture();assert.equal(f.requests.length,0);f.visible();await flush();assert.equal(f.requests.filter(r=>r.url.includes('/history')).length,3);
 const reads=f.requests.filter(r=>r.url.endsWith('/config')).length;assert.equal(f.ui.mount({...f.agent,name:'新名字'}),f.node);await flush();assert.equal(f.requests.filter(r=>r.url.endsWith('/config')).length,reads);
 f.byText(f.node,'V6').click();await flush();assert.equal(f.requests.filter(r=>r.url.includes('/history')).length,5);
 f.doc.hidden=true;f.doc.dispatch('visibilitychange');const count=f.requests.length;f.ui.tick();await flush();assert.equal(f.requests.length,count);
 t.diagnostic(JSON.stringify({initialVisibleHistory:3,switchedFamilyHistoryTotal:5,hiddenExtraRequests:f.requests.length-count,configReadsAcrossMetadataRefresh:reads}));f.ui.close();
});
test('N3 no IntersectionObserver fallback does not read offscreen cards',async()=>{
 const oldW=global.innerWidth,oldH=global.innerHeight;global.innerWidth=800;global.innerHeight=600;const f=fixture();f.node.rect.top=900;f.node.rect.bottom=1000;f.ui.tick();await flush();assert.equal(f.requests.length,0);
 f.node.rect.top=10;f.node.rect.bottom=100;f.ui.tick();await flush();assert.equal(f.requests.filter(r=>r.url.includes('/history')).length,3);f.ui.close();global.innerWidth=oldW;global.innerHeight=oldH;
});
test('N3 actual queued config leaves viewport before starting: zero hidden reads and re-visible recovers',async()=>{
 const first=Array.from({length:4},deferred);let starts=0;
 const response=url=>({ok:true,status:200,json:async()=>url.endsWith('/catalog')?{regions:[]}:{...config(),enabled:false}});
 const f=fixture({transport:(url)=>{const index=starts++;return index<4?first[index].promise:Promise.resolve(response(url));}});
 f.visible();const others=['c','d','e'].map(char=>char.repeat(32));
 for(const agent_id of others){f.doc.body.append(f.ui.mount({agent_id,name:agent_id}));f.ui.setVisible(agent_id,true);}
 await flush();assert.equal(starts,4);const hidden=others[2];f.ui.setVisible(hidden,false);
 for(let i=0;i<4;i++)first[i].resolve(response(f.requests[i].url));await flush();
 assert.equal(f.requests.filter(r=>r.url.includes(hidden)&&r.url.endsWith('/config')).length,0);
 f.ui.setVisible(hidden,true);await flush();assert.equal(f.requests.filter(r=>r.url.includes(hidden)&&r.url.endsWith('/config')).length,1);f.ui.close();
});
test('N3 actual explicit panel config survives viewport departure and does not cancel a user PUT',async()=>{
 const delay=deferred(),f=fixture({writeDelay:delay});f.visible();await flush();f.byText(f.node,'地区选择').click();await flush();
 f.panel().querySelector('form').dispatch('submit');await flush();assert.equal(f.writes.length,1);f.ui.setVisible(id,false);await flush();assert.ok(f.ui.snapshot().panel);
 delay.resolve();await flush();assert.equal(f.ui.snapshot().panel,null);assert.equal(f.writes.length,1);f.ui.close();
});
test('N3 actual stale card neutralizes both strips and visibly labels ratio old',async()=>{
 const f=fixture({stale:true});f.visible();await flush();assert.equal(f.node.querySelectorAll('.nq-low').length,0);assert.equal(f.node.querySelectorAll('.nq-neutral').length,120);
 assert.ok(f.node.querySelectorAll('button').some(b=>b.textContent?.includes('旧数据')));f.ui.close();
});
test('N3 actual panel mode/draft survives mount and search focus; explicit dirty close guard',async()=>{
 const f=fixture();f.visible();await flush();f.byText(f.node,'地区选择').click();await flush();let p=f.panel();const selects=p.querySelectorAll('select');
 selects[0].value='separate';selects[0].dispatch('change');const before=f.ui.snapshot().panel.draft;assert.equal(f.ui.snapshot().panel.dirty,false);
 p=f.panel();p.querySelectorAll('select')[2].value='上海';p.querySelectorAll('select')[2].dispatch('change');assert.equal(f.ui.snapshot().panel.draft.choices[0].region,'上海');assert.equal(f.ui.snapshot().panel.draft.choices[1].region,before.choices[1].region);
 const search=f.panel().querySelector('[data-nq-field="search"]');search.focus();search.value='上海';search.selectionStart=1;search.dispatch('input');assert.equal(f.doc.activeElement.dataset.nqField,'search');assert.equal(f.doc.activeElement.selectionStart,1);
 f.ui.mount({...f.agent,name:'更新'});assert.equal(f.ui.snapshot().panel.draft.choices[0].region,'上海');f.doc.dispatch('keydown',{key:'Escape',preventDefault(){}});assert.ok(f.ui.snapshot().panel);f.byText(f.panel(),'放弃更改').click();assert.equal(f.ui.snapshot().panel,null);assert.equal(f.doc.activeElement.dataset.focusKey,'nq-region');assert.equal(f.writes.length,0);f.ui.close();
});
test('N3 actual CAS409 performs one PUT, reads latest but protects draft until explicit rebase',async()=>{
 const f=fixture();f.visible();await flush();f.byText(f.node,'地区选择').click();await flush();f.setStatus(409);const p=f.panel(),checkbox=p.querySelector('input');checkbox.checked=false;checkbox.dispatch('change');
 f.setConfig({...config(),revision:'9223372036854775807'});f.panel().querySelector('form').dispatch('submit');await flush();const s=f.ui.snapshot().panel;
 assert.equal(f.writes.length,1);assert.equal(s.base,revision);assert.equal(s.draft.enabled,false);assert.equal(s.conflict,true);
 f.ui.tick();await flush();assert.equal(f.writes.length,1);f.byText(f.panel(),'采用最新版本，保留草稿').click();assert.equal(f.ui.snapshot().panel.base,'9223372036854775807');assert.equal(f.ui.snapshot().panel.draft.enabled,false);f.ui.close();
});
test('N3 actual manual Host/Port callbacks preserve other slots and ICMP writes zero port',async()=>{
 const f=fixture();f.visible();await flush();f.byText(f.node,'地区选择').click();await flush();
 let input=f.panel().querySelector('[data-nq-field="mobile-source"]');input.value='manual';input.dispatch('change');
 input=f.panel().querySelector('[data-nq-field="mobile-host"]');input.value='203.0.113.8';input.dispatch('input');
 input=f.panel().querySelector('[data-nq-field="mobile-port"]');input.value='4443';input.dispatch('input');
 input=f.panel().querySelector('[data-nq-field="mobile-protocol"]');input.value='icmp';input.dispatch('change');
 assert.equal(f.ui.snapshot().panel.draft.choices[2].host,'203.0.113.8');f.panel().querySelector('form').dispatch('submit');await flush();
 assert.deepEqual(f.writes[0].choices[2],{slot:'mobile',source:'manual',protocol:'icmp',host:'203.0.113.8',port:0});assert.equal(f.writes[0].choices[0].region,'北京');assert.equal(f.writes[0].choices[1].region,'北京');f.ui.close();
});
test('N3 actual unsupported/disabled V6 has no automatic enable, PUT or family fallback',async()=>{
 const f=fixture();f.setConfig({...config(),supported:false,enabled:false,ipv6:false,targets:[]});f.visible();await flush();
 f.byText(f.node,'V6').click();await flush();assert.equal(f.requests.filter(r=>r.url.includes('/history')).length,0);assert.equal(f.writes.length,0);
 f.byText(f.node,'地区选择').click();await flush();assert.equal(f.byText(f.panel(),'保存').disabled,true);assert.equal(f.ui.snapshot().panel.draft.enabled,false);f.ui.close();
});
test('N3 actual save503 uncertain no auto PUT; success stays closed after refresh',async()=>{
 const f=fixture();f.visible();await flush();f.byText(f.node,'地区选择').click();await flush();f.setStatus(503);f.panel().querySelector('form').dispatch('submit');await flush();assert.equal(f.ui.snapshot().panel.uncertain,true);assert.equal(f.writes.length,1);
 f.ui.tick();await flush();assert.equal(f.writes.length,1);f.byText(f.panel(),'重新读取可信配置').click();await flush();f.byText(f.panel(),'采用最新版本，保留草稿').click();f.setStatus(200);f.panel().querySelector('form').dispatch('submit');await flush();assert.equal(f.ui.snapshot().panel,null);f.ui.mount(f.agent);await flush();assert.equal(f.ui.snapshot().panel,null);assert.equal(f.writes.length,2);f.ui.close();
});
test('N3 actual history plot spanGaps false, separate ratio axis and hour switch, close disposes/restores focus',async()=>{
 const f=fixture();f.visible();await flush();f.node.querySelector('[data-focus-key="nq-telecom-latency"]').click();await flush();assert.equal(f.ui.snapshot().history.hours,1);assert.equal(f.plots.at(-1).opts.series[1].spanGaps,false);
 const old=f.plots.at(-1);f.byText(f.dialog,'24 小时').click();await flush();assert.equal(old.destroyed,true);assert.equal(f.ui.snapshot().history.hours,24);assert.match(f.requests.at(-1).url,/hours=24/);
 f.byText(f.dialog,'连接失败率').click();await flush();assert.deepEqual(f.plots.at(-1).opts.scales.y.range(),[0,100]);const plot=f.plots.at(-1);f.byText(f.dialog,'关闭').click();assert.equal(plot.destroyed,true);assert.equal(f.ui.snapshot().history,null);assert.equal(f.doc.activeElement.dataset.focusKey,'nq-telecom-latency');f.ui.close();
});
test('N3 actual history rejects single measured bucket and ignores late closed response',async()=>{
 const f=fixture();f.visible();await flush();f.setOne();f.node.querySelector('[data-focus-key="nq-telecom-latency"]').click();await flush();assert.equal(f.plots.length,0);assert.match(f.dialog.querySelector('.nq-empty').textContent,/一个真实测量/);
 const late=deferred();f.setLate(late);f.byText(f.dialog,'24 小时').click();await flush();f.byText(f.dialog,'关闭').click();late.resolve();await flush();assert.equal(f.ui.snapshot().history,null);assert.equal(f.dialog.open,false);assert.equal(f.plots.length,0);f.ui.close();
});
