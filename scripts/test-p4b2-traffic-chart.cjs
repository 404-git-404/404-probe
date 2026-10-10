// Actual traffic/N3 modules, established fetchJSON, coordinator and transportLease.
const {test}=require('node:test'),assert=require('node:assert/strict');
const T=require('../web/static/traffic-chart.js'),C=require('../web/static/network-quality-core.js');
const N=require('../web/static/network-quality.js'),State=require('../web/static/agent-state.js'),Management=require('../web/static/management.js');
const id='a'.repeat(32),at=1790990000000;
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};};
const flush=async()=>{for(let i=0;i<12;i++)await new Promise(r=>setImmediate(r));};
const response=v=>({ok:true,status:200,json:async()=>v});
function dto(overrides={}){
 const points=Array.from({length:181},(_,i)=>({received_at:at-1800000+i*10000,collected_at:at-1800000+i*10000-5000,order:String(9223372036854775627n+BigInt(i)),
  rx_rate:i===0||i===80?null:i===30?0:20000+i*100,tx_rate:i===0||i===80?null:i===30?0:7000+i*50,break_before:i===0||i===80,reason:i===0?'first_observation':i===80?'gap':''}));
 return {agent_id:id,generation:'opaque-server-generation',server_now:at,window_ms:1800000,point_limit:181,nominal_interval_ms:10000,
  source:'accepted_server_observations',retention:'memory_only',status:'online',reason:'',coverage:'partial',truncated:false,
  oldest_received_at:points[0].received_at,last_received_at:points.at(-1).received_at,points,...overrides};
}
function dom(){
 const doc={hidden:false,listeners:new Map(),activeElement:null};
 class Element {
  constructor(tag){this.tagName=tag.toUpperCase();this.children=[];this.dataset={};this.attrs={};this.style={};this.listeners=new Map();this.isConnected=true;this.rect={top:20,bottom:200,left:0,right:500,width:500};}
  get firstElementChild(){return this.children[0]||null;}get lastElementChild(){return this.children.at(-1)||null;}
  get parentNode(){return this.parent||null;}get nextElementSibling(){const a=this.parent?.children||[];return a[a.indexOf(this)+1]||null;}
  insertBefore(node,before){if(node===before)return node;if(node.parent)node.parent.children=node.parent.children.filter(n=>n!==node);node.parent=this;const i=before?this.children.indexOf(before):-1;if(i<0)this.children.push(node);else this.children.splice(i,0,node);return node;}
  setAttribute(k,v){this.attrs[k]=v;}append(...nodes){for(const n of nodes){if(n.parent)n.parent.children=n.parent.children.filter(c=>c!==n);n.parent=this;this.children.push(n);}}
  replaceChildren(...nodes){this.children=[];this.append(...nodes);}contains(n){return n===this||this.children.some(c=>c.contains(n));}
  remove(){this.isConnected=false;if(this.parent)this.parent.children=this.parent.children.filter(c=>c!==this);}
  addEventListener(t,f){const a=this.listeners.get(t)||[];a.push(f);this.listeners.set(t,a);}removeEventListener(t,f){this.listeners.set(t,(this.listeners.get(t)||[]).filter(x=>x!==f));}
  dispatch(t,e={}){for(const f of this.listeners.get(t)||[])f({target:this,currentTarget:this,preventDefault(){},...e});}focus(){doc.activeElement=this;}
  getBoundingClientRect(){return this.rect;}querySelectorAll(s){return this.children.flatMap(c=>[c,...c.querySelectorAll('*')]).filter(c=>s==='*'||s===c.tagName.toLowerCase()||s==='.'+c.className);}
  querySelector(s){return this.querySelectorAll(s)[0];}
 }
 doc.createElement=t=>new Element(t);doc.body=new Element('body');const dialog=new Element('dialog');doc.body.append(dialog);doc.querySelector=()=>dialog;
 doc.addEventListener=(t,f)=>{const a=doc.listeners.get(t)||[];a.push(f);doc.listeners.set(t,a);};doc.removeEventListener=(t,f)=>doc.listeners.set(t,(doc.listeners.get(t)||[]).filter(x=>x!==f));
 doc.dispatch=t=>{for(const f of doc.listeners.get(t)||[])f();};return {doc,Element};
}
function fixture(options={}){
 const {doc,Element}=dom(),requests=[],plots=[],observers=[],resizes=[],timers=new Map();let clock=at,serial=0,value=dto();
 const queue=options.queue||C.coordinator({now:()=>clock});
 class Plot {constructor(opts,data,node){assert.ok(node.isConnected);this.opts=opts;this.data=data;this.node=node;this.over=new Element('div');this.over.rect={left:75,right:475,width:400};plots.push(this);}posToIdx(x){return Math.round(x/400*(this.data[0].length-1));}setData(data){this.data=data;this.updates=(this.updates||0)+1;}setSize(size){this.size=size;}destroy(){this.destroyed=true;}}
 class Observer {constructor(fn,opts){this.fn=fn;this.opts=opts;this.nodes=new Set();observers.push(this);}observe(n){this.nodes.add(n);}unobserve(n){this.nodes.delete(n);}disconnect(){this.nodes.clear();}}
 class Resize {constructor(fn){this.fn=fn;resizes.push(this);}observe(n){this.node=n;}disconnect(){this.disconnected=true;}}
 const fetcher=(url,init)=>{requests.push({url,init});return options.transport?options.transport(url,init):Promise.resolve(response(value));};
 const ui=T.create({document:doc,state:State,coordinator:queue,fetcher,Plot,Observer:options.fallback?null:Observer,Resize,now:()=>clock,timeoutMs:options.timeoutMs||10000,
  interval:(fn,ms)=>{assert.equal(ms,30000);timers.set(++serial,fn);return serial;},clear:n=>timers.delete(n)});
 const agent={agent_id:id,name:'Synthetic',online:true},node=ui.mount(agent);doc.body.append(node);
 return {ui,doc,Element,queue,requests,plots,observers,resizes,timers,node,agent,fetcher,visible:(value=true)=>observers[0]?.fn([{target:node,isIntersecting:value}]),
  set:value2=>value=value2,clock:value2=>clock=value2};
}
test('P4b2 actual DTO adapter preserves received ms, opaque huge order, null gap and real idle zero; bounded whole replacement',()=>{
 const v=T.validate(dto(),id),data=T.columns(v);assert.equal(v.points.length,181);assert.equal(data[0][0],(at-1800000)/1000);assert.equal(data[1][0],null);assert.equal(data[2][80],null);assert.equal(data[1][30],0);
 assert.equal(v.points.at(-1).order,'9223372036854775807');assert.match(T.detail(v.points[80]),/缺测/);assert.equal(T.bytes(1000000),'1.00 MB/s');assert.equal(T.bytes(0),'0 B/s');assert.equal(T.bytes(null),'—');
 const original=dto();const adapted=T.validate(original,id);adapted.points[1].rx_rate=10;assert.notEqual(original.points[1].rx_rate,10);
 original.session_id='not-exposed';original.arbitrary_unbounded={payload:['ignored']};const narrow=T.validate(original,id);assert.equal(narrow.session_id,undefined);assert.equal(narrow.arbitrary_unbounded,undefined);
});
test('P4b2 malformed DTO fails locally: contracts, clocks, duplicate order, hidden identities, null/zero and gaps',()=>{
 const variants=[v=>v.agent_id='b'.repeat(32),v=>v.generation=1,v=>v.source='SSE',v=>v.window_ms=1,v=>v.point_limit=200,v=>v.retention='db',v=>v.points.push(v.points[0]),v=>v.server_now=NaN,
  v=>v.points[1].received_at=v.points[0].received_at,v=>v.points[1].received_at=at+1,v=>v.points[0].received_at-=1,v=>v.points[1].order=Number(v.points[1].order),v=>v.points[1].order=v.points[0].order,
  v=>v.points[1].rx_rate=-1,v=>v.points[1].rx_rate=Infinity,v=>v.points[0].rx_rate=0,v=>v.points[1].break_before=true,v=>v.points[1].reason='<img>',v=>v.last_received_at--,
  v=>{v.points.splice(10,4);},v=>v.status='paused',v=>v.reason='invented'];
 for(const mutate of variants){const v=dto();mutate(v);assert.throws(()=>T.validate(v,id));}
});
test('P4b2 production mount twice/no SSE reads, private plot update/resize, keyboard tooltip, offline old/no-data restart',async()=>{
 const f=fixture();assert.equal(f.requests.length,0);assert.equal(f.ui.mount(f.agent),f.node);assert.equal(f.timers.size,1);assert.equal(f.observers[0].opts.rootMargin,'0px');
 f.visible();await flush();assert.equal(f.requests.length,1);assert.equal(f.plots.length,1);const p=f.plots[0];assert.equal(p.opts.height,72);assert.ok(p.opts.axes.every(a=>a.show===false));assert.equal(p.opts.series[1].spanGaps,false);assert.equal(p.opts.series[1].stroke,'#25A994');assert.equal(p.opts.series[2].stroke,'#9978CB');
 assert.deepEqual(p.opts.scales.x.range(),[(at-1800000)/1000,at/1000]);assert.deepEqual(p.opts.scales.y.range(null,0,0),[0,1]);
 const out=f.node.querySelector('.traffic-detail');out.dispatch('keydown',{key:'Home'});assert.match(out.textContent,/首次观测/);out.dispatch('keydown',{key:'ArrowRight'});assert.match(out.title,/9223372036854775628/);
 p.opts.hooks.setCursor[0]({cursor:{idx:30}});assert.match(out.textContent,/0 B\/s/);
 for(let i=0;i<20;i++)f.ui.mount({...f.agent,name:'metadata'+i});await flush();assert.equal(f.requests.length,1);
 f.node.querySelector('.traffic-plot').rect.width=320;f.resizes[0].fn();assert.equal(p.size.width,320);assert.equal(f.plots.length,1);
 f.set(dto({truncated:true,generation:'new-generation'}));f.ui.tick();await flush();assert.equal(f.ui.snapshot().observations[0].generation,'new-generation');assert.match(f.node.querySelector('.traffic-status').title,/截断/);
 f.ui.mount({...f.agent,online:false});assert.match(f.node.querySelector('.traffic-status').textContent,/离线.*历史/);
 f.set(dto({status:'no_data',reason:'no_retained_observations',generation:'restart',points:[],oldest_received_at:null,last_received_at:null}));f.ui.tick();await flush();assert.equal(f.ui.snapshot().observations[0].points,0);assert.ok(p.destroyed);assert.match(f.node.querySelector('.traffic-status').textContent,/等待真实/);f.ui.close();f.queue.close();
});
test('P4b2 actual singleton is marker not segment; null-only empty; zero/detached dimensions do not instantiate plot',async()=>{
 const f=fixture();const point=dto().points[30];f.set(dto({points:[point],oldest_received_at:point.received_at,last_received_at:point.received_at}));f.node.querySelector('.traffic-plot').rect.width=0;
 f.visible();await flush();assert.equal(f.plots.length,0);f.node.querySelector('.traffic-plot').rect.width=320;f.ui.tick();await flush();assert.equal(f.plots[0].data[0].length,1);assert.equal(f.plots[0].opts.series[1].points.show,true);
 f.node.isConnected=false;f.ui.tick();await flush();assert.equal(f.requests.length,2);f.ui.close();f.queue.close();
});
test('P4b2 actual offscreen withdrawal clears plot/arrays and late response cannot resurrect; return fetches Server snapshot',async()=>{
 const delayed=deferred();let calls=0;const f=fixture({transport:()=>++calls===1?Promise.resolve(response(dto())):delayed.promise});f.visible();await flush();f.ui.tick();await flush();f.visible(false);
 assert.ok(f.plots[0].destroyed);assert.ok(f.resizes[0].disconnected);assert.equal(f.ui.snapshot().observations[0].points,0);delayed.resolve(response(dto()));await flush();assert.equal(f.ui.snapshot().observations[0].points,0);
 f.visible();await flush();assert.equal(calls,3);assert.equal(f.ui.snapshot().observations[0].points,181);f.ui.close();f.queue.close();
});
test('P4b2 actual pause/revoke/removal releases accepted and pending observations; repeated metadata does not poll',async()=>{
 for(const lifecycle of [{disabled_at:at},{revoked:true},{removal:{status:'failed'}}]){
  const delayed=deferred();const f=fixture({transport:()=>delayed.promise});f.visible();await flush();f.ui.mount({...f.agent,...lifecycle});delayed.resolve(response(dto()));await flush();assert.equal(f.ui.snapshot().observations[0].points,0);
  assert.match(f.node.querySelector('.traffic-status').textContent,/当前数据不可用/);f.ui.tick();await flush();assert.equal(f.requests.length,1);
  f.ui.mount(f.agent);f.ui.tick();await flush();assert.equal(f.requests.length,2);assert.equal(f.ui.snapshot().observations[0].points,181);f.ui.close();f.queue.close();
 }
});
test('P4b2 actual delete/recreate same key fences old response and closes all resources',async()=>{
 const delayed=deferred();const f=fixture({transport:()=>delayed.promise});f.visible();await flush();f.ui.sync([]);const newNode=f.ui.mount(f.agent);f.doc.body.append(newNode);f.ui.setVisible(id,true);await flush();assert.equal(f.requests.length,1);
 delayed.resolve(response(dto()));await flush();assert.equal(f.requests.length,2);assert.equal(f.ui.snapshot().observations[0].points,181);assert.equal(f.node.isConnected,false);
 f.ui.close();f.ui.close();assert.equal(f.timers.size,0);assert.equal(f.doc.listeners.get('visibilitychange').length,0);assert.equal(f.observers[0].nodes.size,0);assert.ok(f.plots.every(p=>p.destroyed));f.queue.close();
});
test('P4b2 actual hidden/resume coalesces per-key while aborted transport stays physical; hidden timer stopped',async(t)=>{
 const delayed=deferred();const f=fixture({transport:()=>delayed.promise});f.visible();await flush();f.doc.hidden=true;f.doc.dispatch('visibilitychange');assert.equal(f.timers.size,0);f.ui.tick();await flush();assert.equal(f.queue.snapshot().active,1);
 f.doc.hidden=false;f.doc.dispatch('visibilitychange');for(let i=0;i<20;i++)f.ui.tick();await flush();assert.equal(f.requests.length,1);assert.equal(f.queue.snapshot().queued,1);
 delayed.resolve(response(dto()));await flush();assert.equal(f.requests.length,2);assert.equal(f.ui.snapshot().observations[0].points,181);assert.equal(f.timers.size,1);
 t.diagnostic(JSON.stringify({actualDocumentHiddenCallback:true,abortedUnsettledPhysical:1,resumeQueuedPerKey:1,startsAfterPhysicalSettlement:2}));f.ui.close();f.queue.close();
});
test('P4b2 actual fallback geometry is not all-visible, error keeps trusted data and existing backoff',async()=>{
 const oldW=global.innerWidth,oldH=global.innerHeight;global.innerWidth=800;global.innerHeight=600;
 let fail=false;const f=fixture({fallback:true,transport:()=>fail?Promise.reject(Error('503')):Promise.resolve(response(dto()))});f.node.rect.top=900;f.node.rect.bottom=1000;f.ui.tick();await flush();assert.equal(f.requests.length,0);
 f.node.rect.top=10;f.node.rect.bottom=200;f.ui.tick();await flush();assert.equal(f.requests.length,1);fail=true;f.ui.tick();await flush();assert.equal(f.ui.snapshot().observations[0].points,181);assert.match(f.node.querySelector('.traffic-status').textContent,/非当前/);
 f.ui.tick();await flush();assert.equal(f.requests.length,2);f.clock(at+30000);fail=false;f.ui.tick();await flush();assert.equal(f.requests.length,3);f.ui.close();f.queue.close();global.innerWidth=oldW;global.innerHeight=oldH;
});
test('P4b2 malformed new generation never replaces trusted plot or creates plausible data',async()=>{
 const f=fixture();f.visible();await flush();const invalid=dto({generation:'malformed-new'});invalid.points[1].rx_rate='0';f.set(invalid);f.ui.tick();await flush();assert.equal(f.ui.snapshot().observations[0].generation,'opaque-server-generation');assert.match(f.node.querySelector('.traffic-status').textContent,/非当前/);assert.equal(f.plots[0].data[1][1],20100);f.ui.close();f.queue.close();
});
test('P4b2 actual bounded touch/pen callback selects nonlast null and zero, ignores secondary/offscreen, removes listener',async()=>{
 const f=fixture();f.visible();await flush();const p=f.plots[0],out=f.node.querySelector('.traffic-detail');assert.equal(out.dataset.focusKey,'traffic-point');
 p.over.dispatch('pointerdown',{pointerType:'touch',isPrimary:true,clientX:75+400*80/180});assert.match(out.textContent,/缺测/);assert.match(out.textContent,/↓ — · ↑ —/);
 p.over.dispatch('pointerdown',{pointerType:'pen',isPrimary:true,clientX:75+400*30/180});assert.match(out.textContent,/0 B\/s/);const old=out.textContent;
 p.over.dispatch('pointerdown',{pointerType:'touch',isPrimary:false,clientX:300});assert.equal(out.textContent,old);p.over.dispatch('pointerdown',{pointerType:'touch',isPrimary:true,clientX:500});assert.equal(out.textContent,old);
 f.visible(false);assert.equal(p.over.listeners.get('pointerdown').length,0);p.over.dispatch('pointerdown',{pointerType:'touch',clientX:300});assert.equal(out.textContent,'');f.ui.close();f.queue.close();
});

test('M04 round2 horizontal cursor and keyboard keep received time and rx/tx from one accepted observation',async()=>{
 const f=fixture();f.visible();await flush();const p=f.plots[0],out=f.node.querySelector('.traffic-detail'),chart=f.node.querySelector('.traffic-plot');
 const point=dto().points[80];p.opts.hooks.setCursor[0]({...p,cursor:{left:400*80/180,idx:140},posToIdx:p.posToIdx.bind(p)});
 assert.match(out.textContent,/↓ — · ↑ —.*缺测/);assert.ok(out.textContent.includes(new Date(point.received_at).toLocaleString()));assert.ok(out.title.includes('序号 '+point.order));
 chart.dispatch('keydown',{key:'Home'});for(let i=0;i<30;i++)chart.dispatch('keydown',{key:'ArrowRight'});
 assert.match(out.textContent,/↓ 0 B\/s · ↑ 0 B\/s/);assert.ok(out.textContent.includes(new Date(dto().points[30].received_at).toLocaleString()));assert.equal(chart.tabIndex,0);
 const node=f.node,plot=p,selected=out.textContent;f.ui.mount({...f.agent,state:{rx_rate:99}});assert.equal(f.node,node);assert.equal(f.plots[0],plot);assert.equal(out.textContent,selected);f.ui.close();f.queue.close();
});
test('P4b2 mixed actual N3/traffic share physical4; N3 close releases only its owners and never aborts traffic',async(t)=>{
 const held=[],q=C.coordinator(),f=fixture({queue:q,transport:(url,init)=>{const d=deferred();held.push({url,init,...d});return d.promise;}});
 const n=N.create({document:f.doc,state:State,management:Management,coordinator:q,fetcher:f.fetcher,Observer:null,Resize:null,interval:()=>1,clear:()=>{}});
 f.doc.body.append(n.mount(f.agent));n.setVisible(id,true);f.visible();
 for(const char of ['b','c','d']){const aid=char.repeat(32);f.doc.body.append(f.ui.mount({...f.agent,agent_id:aid}));f.ui.setVisible(aid,true);}
 await flush();assert.equal(held.length,4);assert.equal(q.snapshot().active,4);assert.ok(q.snapshot().queued>=1);n.close();await flush();assert.equal(q.snapshot().active,4);const trafficHeld=held.find(r=>r.url.endsWith('/traffic/recent'));assert.equal(trafficHeld.init.signal.aborted,false);
 f.ui.setVisible('d'.repeat(32),false);held.filter(r=>!r.url.endsWith('/traffic/recent')).forEach(r=>r.resolve(response({})));await flush();assert.equal(held.filter(r=>r.url.includes('d'.repeat(32))).length,0);
 for(const r of held)r.resolve(response(dto({agent_id:r.url.split('/')[5]})));await flush();assert.ok(q.snapshot().active<=4);
 const extra=await q.request('still-open',{},()=>42);assert.equal(extra,42);t.diagnostic(JSON.stringify({mixedInitialPhysical:4,maxActiveObserved:4,n3CloseTrafficSignalAborted:false,queuedOffscreenStarts:0}));f.ui.close();q.close();
});
test('P4b2 actual hung body timeout retains physical4 across UI timeout; queued traffic starts after actual body exit',async()=>{
 const bodies=Array.from({length:4},deferred);let starts=0;const f=fixture({timeoutMs:15,transport:()=>{const i=starts++;return i<4?Promise.resolve({ok:true,status:200,json:()=>bodies[i].promise}):Promise.resolve(response(dto({agent_id:'e'.repeat(32)})));}});
 f.visible();for(const char of ['b','c','d','e']){const aid=char.repeat(32);f.doc.body.append(f.ui.mount({...f.agent,agent_id:aid}));f.ui.setVisible(aid,true);}
 await new Promise(r=>setTimeout(r,30));await flush();assert.equal(starts,4);assert.equal(f.queue.snapshot().active,4);bodies[0].resolve(dto());await flush();assert.equal(starts,5);for(const body of bodies)body.resolve(dto());await flush();f.ui.close();f.queue.close();
});
test('P4b2 traffic close does not abort actual N3 catalog/config in shared queue',async()=>{
 const held=[],q=C.coordinator(),f=fixture({queue:q,transport:(url,init)=>{const d=deferred();held.push({url,init,...d});return d.promise;}});
 const n=N.create({document:f.doc,state:State,management:Management,coordinator:q,fetcher:f.fetcher,Observer:null,Resize:null,interval:()=>1,clear:()=>{}});
 f.doc.body.append(n.mount(f.agent));n.setVisible(id,true);f.visible();await flush();f.ui.close();
 for(const r of held.filter(r=>!r.url.endsWith('/traffic/recent')))assert.equal(r.init.signal.aborted,false);
 for(const r of held)r.resolve(response(r.url.endsWith('/catalog')?{regions:[]}:{version:1,revision:'9223372036854775807',supported:true,enabled:false,ipv6:false,targets:[]}));
 await flush();assert.equal(n.snapshot().cards,1);n.close();assert.equal(await q.request('another-owner',{},()=>7),7);q.close();
});
