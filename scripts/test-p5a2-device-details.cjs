const {test}=require('node:test'),assert=require('node:assert/strict');
const D=require('../web/static/device-details.js'),C=require('../web/static/network-quality-core.js'),S=require('../web/static/agent-state.js');
const N=require('../web/static/network-quality.js');
const id='a'.repeat(32),other='b'.repeat(32),now=1700000040000;
const flush=async()=>{for(let i=0;i<12;i++)await new Promise(r=>setImmediate(r));};
function dom(){
 const doc={hidden:false,activeElement:null,listeners:new Map()};
 class Element {
  constructor(tag){this.tagName=tag;this.children=[];this.dataset={};this.attributes={};this.listeners=new Map();this.style={};this.isConnected=true;this.hidden=false;this.open=false;this.value='';this.textContent='';}
  append(...nodes){for(const node of nodes){if(node.parent)node.parent.children=node.parent.children.filter(n=>n!==node);node.parent=this;this.children.push(node);}}
  get firstElementChild(){return this.children[0]||null;}get lastElementChild(){return this.children.at(-1)||null;}
  get parentNode(){return this.parent||null;}get nextElementSibling(){const a=this.parent?.children||[];return a[a.indexOf(this)+1]||null;}
  insertBefore(node,before){if(node===before)return node;if(node.parent)node.parent.children=node.parent.children.filter(n=>n!==node);node.parent=this;node.isConnected=true;const i=before?this.children.indexOf(before):-1;if(i<0)this.children.push(node);else this.children.splice(i,0,node);return node;}
  replaceChildren(...nodes){for(const c of this.children)c.parent=null;this.children=[];this.append(...nodes);}
  remove(){if(this.parent)this.parent.children=this.parent.children.filter(n=>n!==this);this.isConnected=false;}
  setAttribute(k,v){this.attributes[k]=v;}addEventListener(t,f){const a=this.listeners.get(t)||[];a.push(f);this.listeners.set(t,a);}removeEventListener(t,f){this.listeners.set(t,(this.listeners.get(t)||[]).filter(x=>x!==f));}
  dispatch(t,e={}){for(const f of this.listeners.get(t)||[])f({currentTarget:this,target:this,preventDefault(){},...e});}
  focus(){doc.activeElement=this;}contains(n){return n===this||this.children.some(c=>c.contains(n));}showModal(){this.open=true;}close(){this.open=false;this.dispatch('close');}
  getBoundingClientRect(){return {width:600,left:0,right:600};}all(){return this.children.flatMap(c=>[c,...c.all()]);}
  querySelectorAll(s){return this.all().filter(n=>s==='*'||s[0]==='.'&&n.className?.split(' ').includes(s.slice(1))||n.tagName===s||s.startsWith('[data-')&&(()=>{const m=s.match(/\[data-([^=]+)="([^"]+)"\]/);if(!m)return false;return n.dataset[m[1].replace(/-([a-z])/g,(_,c)=>c.toUpperCase())]===m[2];})()||s[0]==='#'&&n.id===s.slice(1));}
  querySelector(s){return this.querySelectorAll(s)[0];}
 }
 doc.createElement=t=>new Element(t);doc.body=new Element('body');const dialog=new Element('dialog');dialog.id='device-details';doc.body.append(dialog);doc.querySelector=s=>s==='#device-details'?dialog:doc.body.querySelector(s);
 doc.addEventListener=(t,f)=>{const a=doc.listeners.get(t)||[];a.push(f);doc.listeners.set(t,a);};doc.removeEventListener=(t,f)=>doc.listeners.set(t,(doc.listeners.get(t)||[]).filter(x=>x!==f));doc.dispatch=t=>{for(const f of doc.listeners.get(t)||[])f();};return{doc,dialog,Element};
}
function dto(agent=id){const point={timestamp:now-60000,cpu:0,ram_percent:50,swap_percent:0,disk_percent:20,load1:1,load5:2,load15:3,rx_rate:1000,tx_rate:0};return{agent_id:agent,server_now:now,from:now-3600000,to:now,bucket_from:now-3600000,bucket_to:now,interval_ms:60000,source:'minute_metrics',aggregation:'arithmetic_mean',retention_days:30,coverage:'partial',point_limit:1441,oldest_timestamp:point.timestamp,last_timestamp:point.timestamp,points:[point]};}
function fixture(fetch){const {doc,dialog,Element}=dom(),agents=new Map([[id,{agent_id:id,name:'设备',detail_loaded:true}],[other,{agent_id:other,name:'另一个',detail_loaded:true}]]),queue=C.coordinator(),requests=[],plots=[];
 let released=0,restored=0;const pane=new Element('section');pane.className='card-detail-panel';
 class Plot{constructor(opts,data,node){this.options=opts;this.data=data;this.node=node;this.over=new Element('div');plots.push(this);}destroy(){this.destroyed=true;}setSize(){}posToIdx(){return 0;}}
 const details=D.create({document:doc,state:S,agents,coordinator:queue,Plot,Resize:null,management:()=>pane,network:()=>({releaseHistory(){released++;},openHistoryFor(){restored++;}}),fetcher:(url,options)=>{requests.push({url,options});return fetch?fetch(url,options):Promise.resolve(new Response(JSON.stringify(dto(url.includes(other)?other:id))));}});
 return{doc,dialog,Element,agents,queue,details,requests,plots,pane,released:()=>released,restored:()=>restored,close(){details.shutdown();queue.close();}};
}
test('OS1b actual security pane distinguishes platform unavailable from setup and other statuses',()=>{
 const f=fixture();try{for(const [status,reason,label]of [['unavailable','platform_unsupported','此平台暂不支持（v1.1）'],['unavailable','setup_required','需要配置'],['failed','platform_unsupported','采集失败']]){
  f.agents.get(id).security={supported:true,status,reason};f.details.open(id,'security');f.details.update(f.agents.get(id));
  const text=f.dialog.querySelector('#details-pane-security').all().map(n=>n.textContent).join(' ');assert.ok(text.includes(label),text);
  if(reason==='platform_unsupported'&&status==='unavailable'){assert.ok(!text.includes('需要配置'));assert.ok(!text.includes('0 条'));}
 }assert.equal(f.requests.length,0);}finally{f.close();}
});

test('P5a2 production stable dialog keeps active custom drafts/caret/tab across 20 metadata updates with no history reads',async()=>{
 const f=fixture();f.details.open(id,'resources');await flush();assert.equal(f.requests.length,1);assert.equal(f.plots.length,1);assert.equal(f.plots[0].data[0].length,1);assert.equal(f.plots[0].options.series[1].points.show,true);
 const custom=f.dialog.querySelector('.details-custom'),from=custom.all().find(n=>n.attributes['aria-label']==='自定义开始时间');from.value='2026-10-03T12:34';from.selectionStart=5;from.focus();
 for(let i=0;i<20;i++)f.details.update({...f.agents.get(id),name:'SSE'+i});await flush();assert.equal(f.details.snapshot().tab,'resources');assert.equal(f.details.snapshot().draft.from,'2026-10-03T12:34');assert.equal(f.doc.activeElement,from);assert.equal(from.selectionStart,5);assert.equal(f.requests.length,1);
 const spec=f.dialog.querySelector('.details-history-controls').all().find(n=>n.tagName==='select');spec.value='cpu';spec.dispatch('change');assert.equal(f.requests.length,1);assert.equal(f.plots[1].data[1][0],0);assert.ok(f.plots[0].destroyed);f.close();
});
test('P5a2 actual detail plot compact axes keep units, large CPU/Load ticks bounded and point values full',async()=>{
 const f=fixture();f.details.open(id,'resources');await flush();
 assert.equal(f.plots[0].options.axes[1].font,'10px system-ui');
 assert.equal(f.plots[0].options.axes[0].size,52);
 assert.deepEqual(f.plots[0].options.axes[1].values(null,[0,250,500]),['0 B/s','250 B/s','500 B/s']);
 const spec=f.dialog.querySelector('.details-history-controls').all().find(n=>n.tagName==='select');
 spec.value='cpu';spec.dispatch('change');assert.deepEqual(f.plots.at(-1).options.axes[1].values(null,[0,50,100,1e12]),['0%','50%','100%','1.0e+12%']);
 spec.value='load';spec.dispatch('change');assert.deepEqual(f.plots.at(-1).options.axes[1].values(null,[1.234567,1e12]),['1.235 load','1.0e+12 load']);
 assert.match(f.dialog.querySelector('.details-point').textContent,/Load 1 1\.00 load/);assert.equal(f.requests.length,1);f.close();
});
test('P5a2 production tab/device/close cancellation destroys plot and fences late real stream data',async()=>{
 let resolve;const f=fixture(()=>new Promise(r=>resolve=r));f.details.open(id,'resources');await flush();assert.equal(f.requests.length,1);
 f.dialog.querySelector('#details-tab-management').dispatch('click');assert.equal(f.requests[0].options.signal.aborted,true);
 resolve(new Response(JSON.stringify(dto())));await flush();assert.equal(f.details.snapshot().tab,'management');assert.equal(f.details.snapshot().points,0);assert.equal(f.plots.length,0);
 f.details.open(other,'resources');await flush();assert.equal(f.requests.length,2);f.details.close();assert.equal(f.requests[1].options.signal.aborted,true);resolve(new Response(JSON.stringify(dto(other))));await flush();assert.equal(f.details.snapshot().open,false);assert.equal(f.details.snapshot().points,0);assert.equal(f.queue.snapshot().active,0);f.close();
});
test('P5a2 production hidden abort retains lease until body settles; visible restores only pending active read',async()=>{
 let cancelCount=0;const f=fixture((_url,options)=>f.requests.length===1?Promise.resolve(new Response(new ReadableStream({pull(){},cancel(){cancelCount++;}}))):Promise.resolve(new Response(JSON.stringify(dto()))));
 f.details.open(id,'resources');await flush();f.doc.hidden=true;f.doc.dispatch('visibilitychange');await flush();assert.equal(cancelCount,1);assert.equal(f.queue.snapshot().active,0);assert.equal(f.plots.length,0);
 f.doc.hidden=false;f.doc.dispatch('visibilitychange');await flush();assert.equal(f.requests.length,2);assert.equal(f.plots.length,1);f.dialog.querySelector('#details-tab-management').dispatch('click');assert.ok(f.plots[0].destroyed);
 f.doc.hidden=true;f.doc.dispatch('visibilitychange');f.doc.hidden=false;f.doc.dispatch('visibilitychange');await flush();assert.equal(f.requests.length,2);f.close();
});
test('P5a2 production network host tab switch releases exact owner, no native dialog stack and unsupported security stays observation',()=>{
 const f=fixture();f.details.open(id,'network',null,{target:'exact-v6',family:'ipv6',kind:'ratio'});assert.equal(f.details.active('network',id),true);assert.equal(f.details.active('network',other),false);assert.equal(f.restored(),0);
 f.dialog.querySelector('#details-tab-security').dispatch('click');assert.equal(f.released(),1);assert.equal(f.details.snapshot().tab,'security');assert.equal(f.requests.length,0);
 f.dialog.querySelector('#details-tab-network').dispatch('click');assert.equal(f.restored(),1);f.agents.delete(id);f.details.sync();assert.equal(f.dialog.open,false);f.close();
});
test('P5a2 production native cancel/close cleanup and exact connected focus return without another device borrowing',()=>{
 const f=fixture(),trigger=new f.Element('button');trigger.dataset.focusKey='manage';f.doc.body.append(trigger);
 f.details.open(id,'management',trigger);f.dialog.dispatch('cancel');assert.equal(f.doc.activeElement,trigger);assert.equal(f.doc.body.style.overflow,undefined);
 f.details.open(id,'management',trigger);f.dialog.close();assert.equal(f.details.snapshot().open,false);assert.equal(f.doc.activeElement,trigger);f.close();
});
test('P5a2 production exact network visible return restores once; inactive/closed/device removal never restores',()=>{
 const f=fixture();f.details.open(id,'network',null,{target:'exact-v6',family:'ipv6',kind:'ratio'});
 f.doc.hidden=true;f.doc.dispatch('visibilitychange');assert.equal(f.released(),1);
 f.doc.hidden=false;f.doc.dispatch('visibilitychange');assert.equal(f.restored(),1);f.doc.dispatch('visibilitychange');assert.equal(f.restored(),1);
 f.doc.hidden=true;f.doc.dispatch('visibilitychange');f.dialog.querySelector('#details-tab-management').dispatch('click');f.doc.hidden=false;f.doc.dispatch('visibilitychange');assert.equal(f.restored(),1);
 f.details.open(id,'network',null,{target:'exact-v6',family:'ipv6',kind:'ratio'});f.doc.hidden=true;f.doc.dispatch('visibilitychange');f.details.close();f.doc.hidden=false;f.doc.dispatch('visibilitychange');assert.equal(f.restored(),1);
 f.details.open(id,'network',null,{target:'exact-v6',family:'ipv6',kind:'ratio'});f.doc.hidden=true;f.doc.dispatch('visibilitychange');f.agents.delete(id);f.details.sync();f.doc.hidden=false;f.doc.dispatch('visibilitychange');assert.equal(f.restored(),1);f.close();
});
test('P5a2 combined actual Details + N3 host exact V6 ratio hidden/visible once, inactive no poll, removed target no borrow',async()=>{
 const {doc,dialog,Element}=dom(),standalone=new Element('dialog');standalone.id='network-quality-history';doc.body.append(standalone);
 const agent={agent_id:id,name:'exact-host',online:true},agents=new Map([[id,agent]]),queue=C.coordinator(),requests=[],plots=[];
 const targets=['ipv4','ipv6'].map((family,i)=>({id:String(i+3).repeat(32),family,slot:'telecom',status:'active',protocol:'tcp',source:'catalog',region:'synthetic'}));
 let cfg={supported:true,enabled:true,ipv6:true,revision:'1',targets},ui,details;
 class Plot{constructor(){this.over=new Element('div');plots.push(this);}destroy(){this.destroyed=true;}setSize(){}}
 const fetcher=async(url,init)=>{requests.push({url,signal:init.signal});let value;
  if(url.endsWith('/catalog'))value={regions:[]};else if(url.endsWith('/config'))value=cfg;
  else{const target=targets.find(t=>t.id===new URL(url,'http://fixture').searchParams.get('target_id'));value={target,now,partial:true,completeness:'unverified',window:{attempts:2,failure_percent:0},blocks:[{start:now-120000,end:now-60000,p50_ms:1,p95_ms:2,attempts:2,failure_percent:0},{start:now-60000,end:now,p50_ms:0,p95_ms:1,attempts:2,failure_percent:0}]};}
  return new Response(JSON.stringify(value));};
 details=D.create({document:doc,state:S,agents,coordinator:queue,Plot,Resize:null,network:()=>ui,fetcher});
 ui=N.create({document:doc,state:S,coordinator:queue,Plot,Resize:null,Observer:null,interval:()=>1,clear:()=>{},fetcher,
  historyHost:{open:(id,trigger,exact)=>details.open(id,'network',trigger,exact),container:()=>details.networkContainer(),active:id=>details.active('network',id),close:()=>details.close()}});
 const node=ui.mount(agent);doc.body.append(node);ui.setVisible(id,true);await flush();ui.setVisible(id,false);await flush();
 assert.equal(ui.openHistoryFor(id,targets[1].id,'ipv6','ratio'),true);await flush();assert.equal(standalone.open,false);assert.equal(ui.snapshot().history.family,'ipv6');assert.equal(ui.snapshot().history.kind,'ratio');assert.ok(plots.length);
 const before=requests.length;doc.hidden=true;doc.dispatch('visibilitychange');await flush();assert.equal(ui.snapshot().history,null);assert.ok(plots.at(-1).destroyed);assert.equal(queue.snapshot().active,0);
 doc.hidden=false;doc.dispatch('visibilitychange');await flush();assert.equal(requests.length,before+1);assert.equal(ui.snapshot().history.family,'ipv6');assert.ok(requests.at(-1).url.includes(targets[1].id));doc.dispatch('visibilitychange');await flush();assert.equal(requests.length,before+1);
 dialog.querySelector('#details-tab-management').dispatch('click');const inactive=requests.length;ui.tick();await flush();assert.equal(requests.length,inactive);assert.equal(ui.snapshot().history,null);
 // Same family requested after its actual config is replaced: no fallback target.
 cfg={...cfg,revision:'2',targets:[targets[0]]};ui.setVisible(id,true); // Existing card config reload belongs to the real controller.
 node.querySelector('.nq-link').dispatch('click');await flush();ui.setVisible(id,false);
 assert.equal(ui.openHistoryFor(id,targets[1].id,'ipv6','ratio'),false);
 dialog.querySelector('#details-tab-network').dispatch('click');await flush();assert.equal(ui.snapshot().history,null);assert.ok(details.networkContainer().children[0].textContent.includes('不可用'));
 details.shutdown();ui.close();queue.close();
});
