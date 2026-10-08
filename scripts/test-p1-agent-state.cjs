// Deterministic tests execute the production store/reconciler and actual dashboard callbacks.
const nodeTest = require('node:test');
const test = (name, fn) => nodeTest(name, {timeout: 5000}, fn);
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const State = require('../web/static/agent-state.js');
const A = 'a'.repeat(32), B = 'b'.repeat(32);
const dto = (id = A, cpu = 10, name = 'original') => ({agent_id:id, name, created_at:100, revoked:false,
  disabled_at:null, online:true, last_seen:100, version:'v1.0.0', upgrade_capable:true,
  state:{cpu_percent:cpu, collected_at:100, uptime:10}, management:{country_code_lookup:true},
  country_code:'US', country_source:'manual', plan:{currency:'USD', quota_value:'5'}, outbounds:{configured:true, selectors:[]}});
const deferred = () => { let resolve, reject; const promise = new Promise((a,b) => {resolve=a; reject=b;}); return {promise,resolve,reject}; };
const tick = () => new Promise(resolve => setImmediate(resolve));
async function flush(n = 4) { while(n--) await tick(); }
function seed(store, value = dto()) { store.commitRead(store.read(value.agent_id),value); }
function response(value, status = 200) { return {status,ok:status>=200 && status<300,statusText:'test failure',json:async()=>value}; }
class Element {
  get value(){return this._value ?? '';}
  set value(value){this._value=String(value);}
  constructor() { this.listeners=new Map(); this.value=''; this.checked=false; this.disabled=false; this.dataset={}; this.open=false;
    this.classList={add(){},remove(){},toggle(){}}; this.textContent=''; this.style={}; this.children=[]; }
  getBoundingClientRect(){return{left:100,top:80,bottom:680,width:560,height:600};}
  contains(node){return this===node||(this.children||[]).some(child=>child.contains?.(node));}
  addEventListener(type, handler) { const all=this.listeners.get(type)||[]; all.push(handler); this.listeners.set(type,all); }
  removeEventListener(type, handler) {this.listeners.set(type,(this.listeners.get(type)||[]).filter(fn=>fn!==handler));}
  remove() {this.removed=true;}
  async emit(type) { for(const f of this.listeners.get(type)||[]) await f({preventDefault(){}}); }
  reset() {} focus() {} setCustomValidity(value) {this.validityMessage=value;} reportValidity() {}
  showModal() {this.open=true;} close() {this.open=false; for(const f of this.listeners.get('close')||[]) f();}
  setAttribute() {} querySelector(){return new Element();} querySelectorAll(){return [];} replaceChildren(...children){this.children=children;} append(...children){this.children ||= [];this.children.push(...children);}
}
function dashboard(file = 'app.js', readTimeout = 1000) {
  const nodes=new Map(), requests=[], sources=[], redirects=[], windowListeners=new Map();
  let route=null;
  const getNode = selector => {if(!nodes.has(selector)) nodes.set(selector,new Element()); return nodes.get(selector);};
  const sandbox={AgentState:{...State,fetchJSON:(url,options)=>State.fetchJSON(url,{...options,timeoutMs:readTimeout}),
    // Keep the production reconciler, but resolve the intentionally stubbed renderer
    // at call time rather than capture the original DOM renderer during startup.
    reconcile:(store,read,changed,failed)=>State.reconcile(store,read,()=>vm.runInContext(file==='app.js'?'render()':'renderCurrentAgent()',sandbox),failed)},AbortController,TextEncoder,console,URLSearchParams,Intl,Date,Map,Set,Number,Object,JSON,
    location:{search:'?id='+A,assign:p=>redirects.push(p),reload:()=>redirects.push('reload')},navigator:{clipboard:{writeText:async()=>{}}},
    document:{querySelector:getNode,querySelectorAll:()=>[],activeElement:null,createElement:()=>new Element(),addEventListener(){},removeEventListener(){}},
    setInterval:()=>0,clearInterval(){},setTimeout,clearTimeout,queueMicrotask,addEventListener(type,f){windowListeners.set(type,f);},confirm:()=>true,
    crypto:{randomUUID:()=> 'c'.repeat(32),getRandomValues:a=>require('node:crypto').webcrypto.getRandomValues(a)},fetch:(url,options={})=>{
      const d=deferred(), record={url,options,...d}; requests.push(record);
      if(route) {const value=route(url,options); if(value !== undefined) Promise.resolve(value).then(d.resolve,d.reject);}
      return d.promise;
    },EventSource:class {constructor(){this.listeners=new Map();sources.push(this);}close(){this.closed=true;}addEventListener(type,f){this.listeners.set(type,f);}}};
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static/management.js'),'utf8'),sandbox,{filename:'management.js'});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static/selector.js'),'utf8'),sandbox,{filename:'selector.js'});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static/network-quality-core.js'),'utf8'),sandbox,{filename:'network-quality-core.js'});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static/network-quality.js'),'utf8'),sandbox,{filename:'network-quality.js'});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static/traffic-chart.js'),'utf8'),sandbox,{filename:'traffic-chart.js'});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static/card-metrics.js'),'utf8'),sandbox,{filename:'card-metrics.js'});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static/resource-history.js'),'utf8'),sandbox,{filename:'resource-history.js'});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static/device-details.js'),'utf8'),sandbox,{filename:'device-details.js'});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static/plan-editor.js'),'utf8'),sandbox,{filename:'plan-editor.js'});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static/plan-renewal.js'),'utf8'),sandbox,{filename:'plan-renewal.js'});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static/overview.js'),'utf8'),sandbox,{filename:'overview.js'});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static/plan-units.js'),'utf8'),sandbox,{filename:'plan-units.js'});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/static',file),'utf8'),sandbox,{filename:file});
  if(file === 'app.js') vm.runInContext("agentState.endRefresh(agentState.beginRefresh()); var actualDashboardRender=render; render = () => {}; mutationCSRFToken = 'test-token';",sandbox);
  else vm.runInContext("loadController.abort(); renderCurrentAgent = () => {currentAgent=agentState.agents.get(id);}; renderOutbounds=()=>{};renderAgentRuntime=()=>{};renderGoogleStatus=()=>{}; mutationCSRFToken='test-token';",sandbox);
  return {nodes,requests,redirects,sources,setConfirm:f=>sandbox.confirm=f,windowEvent:(type,value={})=>windowListeners.get(type)(value),setRoute:f=>route=f,
    eval:code=>vm.runInContext(code,sandbox), state:vm.runInContext('agentState',sandbox),
    event:value=>sources[0].listeners.get('agent')({data:JSON.stringify(value)}),
    request:fragment=>requests.findLast(r=> /^\/[a-z]{32}$/.test(fragment) ? r.url.endsWith(fragment) : r.url.includes(fragment))};
}

test('production store: slow detail cannot overwrite newer partial even equal/rolled-back timestamps',()=>{
  const s=State.create(); seed(s); const old=s.read(A);
  s.event({agent_id:A,name:'queued old name',online:true,last_seen:100,state:{cpu_percent:93,collected_at:1,uptime:1}});
  s.commitRead(old,dto(A,2)); assert.equal(s.agents.get(A).state.cpu_percent,93); assert.equal(s.agents.get(A).name,'original');
});
test('production store: telemetry does not starve independent configuration reads',()=>{
  const s=State.create(); seed(s); const read=s.read(A);
  for(let i=0;i<30;i++) s.event({agent_id:A,state:{cpu_percent:i,collected_at:100}});
  s.commitRead(read,{...dto(A,0,'other client'),plan:{currency:'EUR'},country_code:'CA'});
  assert.equal(s.agents.get(A).state.cpu_percent,29); assert.equal(s.agents.get(A).name,'other client');
  assert.equal(s.agents.get(A).plan.currency,'EUR'); assert.equal(s.agents.get(A).country_code,'CA');
});
test('production store: name/plan/country barriers partition writes, fail releases without old restore',()=>{
  const s=State.create(); seed(s); const old=s.read(A);
  const name=s.beginMutation(A,['name']), plan=s.beginMutation(A,['plan','country']);
  assert.equal(s.beginMutation(A,['name']),null);
  s.event({agent_id:A,state:{cpu_percent:83,collected_at:100}});
  s.finishMutation(name,{name:'saved'}); s.finishMutation(plan,{plan:{currency:'EUR'},country_code:'DE'});
  s.commitRead(old,dto(A,1,'old')); assert.equal(s.agents.get(A).name,'saved');
  assert.equal(s.agents.get(A).plan.currency,'EUR'); assert.equal(s.agents.get(A).country_code,'DE');
  const failed=s.beginMutation(A,['plan']); s.finishMutation(failed);
  s.commitRead(s.read(A),{plan:{currency:'GBP'}}); assert.equal(s.agents.get(A).plan.currency,'GBP');
  assert.equal(s.agents.get(A).state.cpu_percent,83);
});
test('production store: pagination preserves new entity and overlap invalidates old cycle',()=>{
  const s=State.create(); seed(s); const one=s.beginRefresh(); seed(s,dto(B,40));
  s.commitList(one,[{agent_id:A,name:'list'}]); assert.ok(s.agents.has(B));
  const two=s.beginRefresh(); assert.equal(one.controller.signal.aborted,true);
  assert.equal(s.commitList(one,[{agent_id:A,name:'stale'}]),false);
  s.commitList(two,[{agent_id:A,name:'current'},{agent_id:B,name:'new'}]); assert.equal(s.agents.get(A).name,'current');
});
test('production store: delete blocks old reads/list/events, fail retains current, tombstone retires safely',()=>{
  const s=State.create(); seed(s); const old=s.read(A), list=s.beginRefresh(), barrier=s.beginDelete(A);
  assert.equal(s.event({agent_id:A,state:{cpu_percent:2}}),false);
  assert.equal(s.commitRead(old,dto(A)),false); s.commitList(list,[dto(A)]);
  assert.equal(s.agents.get(A).state.cpu_percent,10);
  s.finishDelete(barrier,false); s.commitRead(s.read(A),dto(A,77));
  assert.equal(s.agents.get(A).state.cpu_percent,77);
  const late=s.read(A), removed=s.beginDelete(A); s.finishDelete(removed,true);
  s.commitList(s.beginRefresh(),[]); assert.equal(s.revokedAgentIDs.size,0);
  assert.equal(s.commitRead(late,dto(A)),false); assert.equal(s.event({agent_id:A,state:{cpu_percent:2}}),false);
  seed(s,dto(B)); assert.ok(s.agents.has(B)); assert.equal(s.agents.has(A),false);
});

test('production store: absence polling cannot retire an in-flight deletion barrier',()=>{
  for(const success of [true,false]) {
    const s=State.create(); seed(s); const barrier=s.beginDelete(A);
    s.commitList(s.beginRefresh(),[]);
    assert.equal(s.blocked(A),true); assert.equal(s.revokedAgentIDs.has(A),true);
    assert.equal(s.commitRead(s.read(A),dto(A,1)),false);
    assert.equal(s.finishDelete(barrier,success),true);
    if(success) { s.commitList(s.beginRefresh(),[]); assert.equal(s.revokedAgentIDs.size,0); }
    else { assert.equal(s.blocked(A),false); assert.equal(s.commitRead(s.read(A),dto(A,89)),true); }
  }
});
test('production store: old 404 cannot remove entity after newer state; upgrade is independent',()=>{
  const s=State.create(); seed(s); const old=s.read(A), upgrade=s.read(A,null,['upgrade']);
  s.event({agent_id:A,state:{cpu_percent:70}}); assert.equal(s.missing(old),false);
  s.commitRead(upgrade,{upgrade:{status:'succeeded'}}); assert.equal(s.agents.get(A).state.cpu_percent,70);
  assert.equal(s.missing(s.read(A)),true);
});

test('production store: older 404 cannot invalidate a newer uncompleted detail read',()=>{
  const s=State.create(); seed(s); const older=s.read(A), newer=s.read(A);
  assert.equal(s.missing(older),false); assert.equal(s.commitRead(newer,dto(A,88)),true);
  assert.equal(s.agents.get(A).state.cpu_percent,88);
});

test('actual pages: navigation closes SSE and bfcache restores a fresh page',()=>{
  for(const file of ['app.js','history.js']) {
    const h=dashboard(file); h.windowEvent('pagehide'); assert.equal(h.sources[0].closed,true);
    h.windowEvent('pageshow',{persisted:false}); assert.deepEqual(h.redirects,[]);
    h.windowEvent('pageshow',{persisted:true}); assert.deepEqual(h.redirects,['reload']);
  }
});
test('production reconciler: multiple full notifications in flight preserve exactly one dirty tail',async()=>{
  const s=State.create(); seed(s); const calls=[];
  const r=State.reconcile(s,()=>{const d=deferred();calls.push(d);return d.promise;},()=>{});
  r.notify(A); await flush(); r.notify(A); r.notify(A); r.notify(A);
  assert.equal(calls.length,1); calls[0].resolve(true); await flush(); assert.equal(calls.length,2);
  calls[1].resolve(true); await flush(); assert.equal(calls.length,2); assert.equal(r.size(),0); r.close();
});
test('production reconciler: pending-write dirty resumes on both success and failure; no immediate failure loop',async()=>{
  const s=State.create(); seed(s); const calls=[], errors=[];
  const r=State.reconcile(s,()=>{const d=deferred();calls.push(d);return d.promise;},()=>{},e=>errors.push(e));
  for(const success of [true,false]) {
    const m=s.beginMutation(A,['plan']);r.notify(A);r.notify(A);const before=calls.length; await flush();
    assert.equal(calls.length,before);s.finishMutation(m,success?{plan:{currency:'EUR'}}:{});r.notify(A);await flush();
    assert.equal(calls.length,before+1);calls.at(-1).resolve(true);await flush();
  }
  r.notify(A);await flush();calls.at(-1).reject(new Error('503'));await flush();const count=calls.length;
  await flush();assert.equal(calls.length,count);assert.equal(errors.length,1);
  r.retry();await flush();assert.equal(calls.length,count+1);calls.at(-1).resolve(true);await flush();r.close();
});
test('production reconciler: exact bounded concurrency, delete and navigation stop queued tail',async()=>{
  const s=State.create(); const calls=[];
  const r=State.reconcile(s,(id,signal)=>{const d=deferred();calls.push({id,signal,...d});return d.promise;},()=>{});
  for(let i=0;i<12;i++){const id=String(i);seed(s,{...dto(),agent_id:id});r.notify(id);}
  await flush();assert.equal(calls.length,8);
  const first=calls[0];r.notify(first.id);s.beginDelete(first.id);r.cancel(first.id);
  assert.equal(first.signal.aborted,true);first.resolve(true);await flush();
  assert.equal(calls.filter(c=>c.id===first.id).length,1);
  r.close();for(const c of calls)c.resolve(true);await flush();assert.equal(r.size(),0);
});
test('actual dashboard detail commits immediately, partial wins, slow upgrade never gates state',async()=>{
  const app=dashboard();seed(app.state);const work=app.eval('hydrateDetails([...agents.values()])');
  await flush();app.event({agent_id:A,online:true,last_seen:100,state:{cpu_percent:91,collected_at:0}});
  app.request('/'+A).resolve(response(dto(A,1,'detail name')));await flush();
  assert.equal(app.state.agents.get(A).state.cpu_percent,91);assert.equal(app.state.agents.get(A).name,'detail name');
  app.request('/upgrade').resolve(response({status:'succeeded'}));await work;
  assert.equal(app.state.agents.get(A).state.cpu_percent,91);
});
test('actual dashboard refresh overlap and pagination preserve SSE-created valid entity',async()=>{
  const app=dashboard();seed(app.state);
  app.setRoute(url=>url.endsWith('/upgrade')?response(null,204):url.includes('/agents/'+B)?response(dto(B,50)):url.includes('/agents/'+A)?response(dto()):undefined);
  const first=app.eval('refresh()');const page=app.request('?limit=100');
  app.event({agent_id:B,online:true,last_seen:100,state:{cpu_percent:50,collected_at:100}});await flush();
  page.resolve(response({items:[{agent_id:A,name:'original'}],next_cursor:'next'}));await flush();
  app.request('cursor=next').resolve(response({items:[],next_cursor:''}));await first;assert.ok(app.state.agents.has(B));
  const old=app.eval('refresh()'), oldPage=app.request('?limit=100'), recent=app.eval('refresh()'), newPage=app.request('?limit=100');
  assert.equal(oldPage.options.signal.aborted,true);
  newPage.resolve(response({items:[{agent_id:A,name:'original'},{agent_id:B,name:'new'}]}));await recent;
  oldPage.resolve(response({items:[{agent_id:A,name:'late'}]}));await old;
  assert.ok(app.state.agents.has(B));assert.notEqual(app.state.agents.get(A).name,'late');
});
test('actual name save survives stale HTTP/full SSE; other-client full notification is reflected',async()=>{
  const app=dashboard();seed(app.state);app.eval(`editingAgentNames.add('${A}');agentNameDrafts.set('${A}','saved');agentNameEditGeneration.set('${A}',1)`);
  const stale=app.eval(`readAgentDetail('${A}')`),old=app.request('/'+A);
  const ctxInput=app.nodes.get('#agents');ctxInput.value='saved';
  app.eval(`saveAgentName(agents.get('${A}'), document.querySelector('#agents'), document.querySelector('#add-agent'))`);
  const write=app.request('/name');write.resolve(response({name:'saved'}));await flush();
  app.request('/'+A).resolve(response(dto(A,55,'saved')));await flush();
  old.resolve(response(dto(A,1,'old')));await stale;assert.equal(app.state.agents.get(A).name,'saved');
  app.event(dto(A,1,'queued old full'));await flush();app.request('/'+A).resolve(response(dto(A,55,'saved')));await flush();
  assert.equal(app.state.agents.get(A).name,'saved');
  app.event({...dto(A,56,'other client'),plan:{currency:'EUR'}});await flush();
  app.request('/'+A).resolve(response({...dto(A,56,'other client'),plan:{currency:'EUR'}}));await flush();
  assert.equal(app.state.agents.get(A).name,'other client');assert.equal(app.state.agents.get(A).plan.currency,'EUR');
});
test('actual dashboard single entity failure/404/abort do not block others, 401 still redirects',async()=>{
  const app=dashboard();seed(app.state);seed(app.state,dto(B));
  app.setRoute(url=>url.endsWith('/upgrade')?response(null,204):url.includes('/'+B)?response(dto(B,88)):undefined);
  const hydrate=app.eval('hydrateDetails([...agents.values()])');await flush();app.request('/'+A).reject(new Error('503'));await hydrate;
  assert.equal(app.state.agents.get(B).state.cpu_percent,88);
  const old=app.eval(`readAgentDetail('${A}')`);app.event({agent_id:A,state:{cpu_percent:90}});
  app.request('/'+A).resolve(response({},404));await old;assert.ok(app.state.agents.has(A));
  const missing=app.eval(`readAgentDetail('${A}')`);app.request('/'+A).resolve(response({},404));await missing;assert.equal(app.state.agents.has(A),false);
  const aborted=app.eval(`globalThis.detailController=new AbortController(); readAgentDetail('${B}',detailController.signal)`);
  app.eval('detailController.abort()'); await flush();
  app.request('/'+B).resolve(response(dto(B,1))); assert.equal(await aborted,false);
  assert.equal(app.state.agents.get(B).state.cpu_percent,88);
  app.setRoute(()=>response({},401));
  // readJSON retains authentication redirect even when a caller is no longer interested.
  await assert.rejects(app.eval("readJSON('/auth')"));assert.deepEqual(app.redirects,['/login']);
});

test('actual dashboard reconnect requests fresh HTTP baseline, not a frozen prior stream',async()=>{
  const app=dashboard(); seed(app.state);
  app.setRoute(url=>url.includes('?limit=100')?response({items:[{agent_id:A,name:'reconnected'}]}):url.endsWith('/upgrade')?response(null,204):url.endsWith('/'+A)?response(dto(A,91,'reconnected')):undefined);
  app.sources[0].onerror(); assert.equal(app.nodes.get('#stream').textContent,'正在重连');
  app.sources[0].onopen(); await flush(10);
  assert.equal(app.state.agents.get(A).name,'reconnected'); assert.equal(app.state.agents.get(A).state.cpu_percent,91);
  assert.equal(app.nodes.get('#stream').textContent,'实时连接');
});
test('actual dashboard delete fails honestly and restores via fresh read; late success cannot resurrect',async()=>{
  const app=dashboard();seed(app.state);app.eval(`pendingRemoveAgentID='${A}'`);
  const slow=app.eval(`readAgentDetail('${A}')`),slowRequest=app.request('/'+A);
  const deleting=app.nodes.get('#remove-agent-form').emit('submit');
  app.event({agent_id:A,state:{cpu_percent:1}});slowRequest.resolve(response(dto(A,1)));await slow;
  app.request('/revoke').resolve(response({},500));await deleting;await flush();
  assert.ok(app.state.agents.has(A));app.request('/'+A).resolve(response(dto(A,79)));await flush();
  assert.equal(app.state.agents.get(A).state.cpu_percent,79);
  app.setRoute(url=>url.includes('?limit=100')?response({items:[]}):url.includes('/'+A)&&!url.endsWith('/revoke')?response({},404):undefined);
  app.eval(`pendingRemoveAgentID='${A}'`);const success=app.nodes.get('#remove-agent-form').emit('submit');
  app.request('/revoke').resolve(response({agent:{agent_id:A,revoked:true}}));await success;
  app.event({agent_id:A,state:{cpu_percent:1}});await flush();assert.equal(app.state.agents.has(A),false);
  app.setRoute(url=>url.includes('/'+B)?response(dto(B,81)):undefined);
  app.event({agent_id:B,state:{cpu_percent:81}});await flush();assert.ok(app.state.agents.has(B));
});
test('actual history Selector completion cannot roll back concurrent telemetry',async()=>{
  const app=dashboard('history.js');seed(app.state,{...dto(),outbounds:{configured:true,available:true,selectors:[{name:'proxy',current:'old',choices:['old','new']}]}});
  app.eval(`currentAgent=agentState.agents.get('${A}')`);
  let detailReads = 0;
  app.setRoute(url=>url.includes('/jobs/')?response({operation_status:'success',result:{success:true,measurement:{selector_switch:{current:'new',changed:true}}}}):url.includes('/agents/'+A)&&!url.endsWith('/switch')?response({...dto(A,++detailReads === 1 ? 1 : 96),outbounds:{configured:true,selectors:[{name:'proxy',current:'new'}]}}):undefined);
  const work=app.eval("switchOutbound('proxy','new')");app.event({agent_id:A,state:{cpu_percent:96,collected_at:0}});
  app.request('/switch').resolve(response({job_id:'job'}));await work;await flush();
  assert.equal(app.state.agents.get(A).state.cpu_percent,96);assert.equal(app.state.agents.get(A).outbounds.selectors[0].current,'new');
});
test('actual plan save fences old read/country and never closes a newly opened draft',async()=>{
  const app=dashboard();seed(app.state);seed(app.state,dto(B));
  const old=app.eval(`readAgentDetail('${A}')`),oldRequest=app.request('/'+A);
  app.eval(`openPlanDialog(agents.get('${A}'))`);
  app.nodes.get('#plan-currency').value='EUR';app.nodes.get('#plan-country-code').value='DE';
  const work=app.nodes.get('#plan-form').emit('submit'), write=app.request('/plan');
  assert.ok(write.url.includes(A));app.nodes.get('#plan-dialog').close();
  app.eval(`openPlanDialog(agents.get('${B}'))`);app.nodes.get('#plan-currency').value='draft GBP';
  write.resolve(response({currency:'EUR',country_code_override:'DE'}));await work;await flush();
  oldRequest.resolve(response({...dto(A,1),country_code:'US'}));await old;
  assert.equal(app.state.agents.get(A).plan.currency,'EUR');
  assert.equal(app.nodes.get('#plan-dialog').open,true);assert.equal(app.nodes.get('#plan-currency').value,'draft GBP');
  app.request('/'+A).resolve(response({...dto(A,44),country_code:'DE',plan:{currency:'EUR',country_code_override:'DE'}}));await flush();
  assert.equal(app.state.agents.get(A).country_code,'DE');assert.equal(app.state.agents.get(A).state.cpu_percent,44);
});
test('actual name response retains a subsequently typed draft, failure is not success',async()=>{
  const app=dashboard();seed(app.state);
  const input=app.nodes.get('#agents');input.value='submitted';
  app.eval(`editingAgentNames.add('${A}');agentNameDrafts.set('${A}','submitted');agentNameEditGeneration.set('${A}',1)`);
  const work=app.eval(`saveAgentName(agents.get('${A}'),document.querySelector('#agents'),document.querySelector('#add-agent'))`);
  app.eval(`agentNameDrafts.set('${A}','new unsaved draft')`);
  app.request('/name').resolve(response({name:'submitted'}));await work;
  assert.equal(app.eval(`agentNameDrafts.get('${A}')`),'new unsaved draft');
  assert.equal(app.eval(`editingAgentNames.has('${A}')`),true);
  input.value='new unsaved draft'; // the rerendered real input contains this retained draft
  const failed=app.eval(`saveAgentName(agents.get('${A}'),document.querySelector('#agents'),document.querySelector('#add-agent'))`);
  app.request('/name').resolve(response({error:{message:'real write failure'}},500));await failed;
  assert.equal(app.state.agents.get(A).name,'submitted');assert.equal(app.state.busy(A,'name'),false);
  assert.equal(app.eval(`agentNameErrors.get('${A}')`),'real write failure');
});

test('actual plan callback retains input edited during save and isolates old-dialog errors',async()=>{
  const app=dashboard(); seed(app.state); seed(app.state,dto(B));
  app.eval(`openPlanDialog(agents.get('${A}'))`); app.nodes.get('#plan-currency').value='EUR';
  const saving=app.nodes.get('#plan-form').emit('submit');
  app.nodes.get('#plan-currency').value='GBP'; await app.nodes.get('#plan-form').emit('input');
  app.request('/plan').resolve(response({currency:'EUR'})); await saving;
  assert.equal(app.nodes.get('#plan-dialog').open,true); assert.equal(app.nodes.get('#plan-currency').value,'GBP');
  assert.equal(app.state.agents.get(A).plan.currency,'EUR');
  const failed=app.nodes.get('#plan-form').emit('submit');
  app.nodes.get('#plan-dialog').close(); app.eval(`openPlanDialog(agents.get('${B}'))`);
  app.nodes.get('#plan-error').textContent='';
  app.request('/plan').resolve(response({error:{message:'old dialog failure'}},500)); await failed;
  assert.equal(app.nodes.get('#plan-error').textContent,''); assert.equal(app.nodes.get('#plan-dialog').open,true);
});

test('P5b actual full plan payload retains every stored field; calibration is blank on each opening',async()=>{
 const app=dashboard(),plan={traffic_mode:'tx',quota_value:'12.5',quota_unit:'TiB',cycle_kind:'days',cycle_count:15,cycle_anchor:'2026-09-01',timezone:'Asia/Shanghai',bandwidth_value:'500',bandwidth_unit:'Mbps',currency:'EUR',purchase_price:'39.99',purchase_price_period:'once',renewal_price:'4.50',renewal_price_period:'monthly',purchase_date:'2026-01-15',renewal_date:'2026-10-15',expiry_date:'2027-01-15',country_code_override:'JP',calibration_value:'999',calibration_unit:'TB'};
 seed(app.state,{...dto(),plan});app.eval(`openPlanDialog(agents.get('${A}'))`);
 assert.equal(app.nodes.get('#plan-calibration-value').value,'');assert.equal(app.nodes.get('#plan-calibration-unit').value,'GB');
 assert.equal(app.nodes.get('#plan-quota-value').value,'13.7438953472');assert.equal(app.nodes.get('#plan-quota-unit').value,'TB');
 assert.equal(app.nodes.get('#plan-country-code').value,'JP');assert.equal(app.requests.filter(r=>r.url.endsWith('/plan')).length,0);
 const saving=app.nodes.get('#plan-form').emit('submit'),write=app.request('/plan');
 assert.equal(write.options.method,'PUT');assert.equal(write.options.headers['X-CSRF-Token'],'test-token');
 assert.deepEqual(JSON.parse(write.options.body),{...plan,calibration_value:'',calibration_unit:''});write.resolve(response(plan));await saving;
 app.eval(`openPlanDialog(agents.get('${A}'))`);assert.equal(app.nodes.get('#plan-calibration-value').value,'');
 app.nodes.get('#plan-calibration-value').value='2.5';app.nodes.get('#plan-calibration-unit').value='GB';const calibrated=app.nodes.get('#plan-form').emit('submit'),second=app.request('/plan');
 assert.deepEqual(JSON.parse(second.options.body),{...plan,calibration_value:'2.5',calibration_unit:'GB'});second.resolve(response(plan));await calibrated;
});

test('P5b actual disabled traffic and empty optional values preserve original payload clearing rules',async()=>{
 const app=dashboard();seed(app.state);app.eval(`openPlanDialog(agents.get('${A}'))`);
 app.nodes.get('#plan-traffic-enabled').checked=false;for(const id of ['#plan-quota-value','#plan-calibration-value','#plan-bandwidth-value','#plan-purchase-price','#plan-renewal-price'])app.nodes.get(id).value='';
 app.nodes.get('#plan-purchase-period').value='yearly';app.nodes.get('#plan-renewal-period').value='monthly';app.nodes.get('#plan-bandwidth-unit').value='Gbps';app.nodes.get('#plan-calibration-unit').value='TB';
 app.nodes.get('#plan-currency').value=' eur ';app.nodes.get('#plan-country-code').value=' jp ';app.nodes.get('#plan-timezone').value=' UTC ';
 const saving=app.nodes.get('#plan-form').emit('submit'),write=app.request('/plan'),payload=JSON.parse(write.options.body);
 assert.deepEqual(payload,{traffic_mode:'',quota_value:'',quota_unit:'',cycle_kind:'',cycle_count:0,cycle_anchor:'',timezone:'UTC',calibration_value:'',calibration_unit:'',bandwidth_value:'',bandwidth_unit:'',currency:'EUR',purchase_price:'',purchase_price_period:'',renewal_price:'',renewal_price_period:'',purchase_date:'',renewal_date:'',expiry_date:'',country_code_override:'JP'});
 write.resolve(response(payload));await saving;
});

test('P5b actual legacy rounded and unsafe-integer quotas display decimal but unrelated save keeps old wire',async()=>{
 for(const [value,unit,shown] of [['1','GiB','1.073741824'],['0.001','TiB','0.001099511628'],['8388608.001','GiB','9007199.255814734']]){
  const app=dashboard();seed(app.state,{...dto(),plan:{traffic_mode:'sum',quota_value:value,quota_unit:unit}});app.eval(`openPlanDialog(agents.get('${A}'))`);assert.equal(app.nodes.get('#plan-quota-value').value,shown);assert.equal(app.nodes.get('#plan-quota-unit').value,unit==='GiB'?'GB':'TB');assert.equal(app.nodes.get('#plan-calibration-value').value,'');assert.equal(app.nodes.get('#plan-calibration-unit').value,'GB');
  app.nodes.get('#plan-currency').value='EUR';const saving=app.nodes.get('#plan-form').emit('submit'),write=app.request('/plan'),p=JSON.parse(write.options.body);assert.equal(p.quota_value,value);assert.equal(p.quota_unit,unit);assert.equal(p.currency,'EUR');assert.equal(p.calibration_unit,'');write.resolve(response(p));await saving;
 }
});

test('P5b actual explicit decimal edit saves decimal; precision error creates no mutation or locked buttons',async()=>{
 const app=dashboard();seed(app.state,{...dto(),plan:{traffic_mode:'sum',quota_value:'1',quota_unit:'GiB'}});app.eval(`openPlanDialog(agents.get('${A}'))`);
 app.nodes.get('#plan-quota-value').value='1.073741825';await app.nodes.get('#plan-form').emit('submit');assert.equal(app.requests.filter(r=>r.url.endsWith('/plan')).length,0);assert.match(app.nodes.get('#plan-error').textContent,/最多 3 位/);assert.equal(app.state.busy(A,'plan'),false);assert.equal(app.nodes.get('#plan-save').disabled,false);assert.equal(app.nodes.get('#plan-clear').disabled,false);
 app.nodes.get('#plan-quota-value').value='1.074';app.nodes.get('#plan-calibration-value').value='0.125';const saving=app.nodes.get('#plan-form').emit('submit'),write=app.request('/plan'),p=JSON.parse(write.options.body);assert.equal(p.quota_value,'1.074');assert.equal(p.quota_unit,'GB');assert.equal(p.calibration_value,'0.125');assert.equal(p.calibration_unit,'GB');write.resolve(response(p));await saving;
});

test('P5b actual binary legacy traffic disabled still clears original quota and calibration wire fields',async()=>{
 const app=dashboard();seed(app.state,{...dto(),plan:{traffic_mode:'sum',quota_value:'1',quota_unit:'GiB'}});app.eval(`openPlanDialog(agents.get('${A}'))`);app.nodes.get('#plan-quota-value').value='invalid';app.nodes.get('#plan-calibration-value').value='invalid';app.nodes.get('#plan-traffic-enabled').checked=false;
 const saving=app.nodes.get('#plan-form').emit('submit'),write=app.request('/plan'),p=JSON.parse(write.options.body);for(const key of ['quota_value','quota_unit','calibration_value','calibration_unit','traffic_mode','cycle_kind','cycle_anchor'])assert.equal(p[key],'');assert.equal(p.cycle_count,0);write.resolve(response(p));await saving;
});

test('P5b actual dashboard and legacy history format network decimal but machine binary',()=>{
 const app=dashboard();assert.equal(app.eval('bytes(1000000,true)'),'1.0 MB/s');assert.equal(app.eval('bytes(1073741824,false,true)'),'1.0 GiB');assert.equal(app.eval('bytes(1024,true,true)'),'1.0 KiB/s');
 const history=dashboard('history.js');assert.equal(history.eval('detailBytes(1000000000,false)'),'1.0 GB');assert.equal(history.eval('detailBytes(1073741824)'),'1.0 GiB');assert.equal(history.eval('rate(1000000)'),'1MB/s');
});

test('P5b actual pagehide aborts pending plan and late result/finally cannot render or reopen',async()=>{
 const app=dashboard();seed(app.state);app.eval(`openPlanDialog(agents.get('${A}')); var planRenderCount=0; render=()=>planRenderCount++`);
 const saving=app.nodes.get('#plan-form').emit('submit'),write=app.request('/plan');app.windowEvent('pagehide');assert.equal(write.options.signal.aborted,true);
 write.resolve(response({currency:'EUR'}));await saving;assert.equal(app.eval('planRenderCount'),0);assert.equal(app.nodes.get('#plan-dialog').open,false);assert.equal(app.eval('planEditor.canSubmit()'),false);assert.equal(app.sources[0].closed,true);
});

test('P5b actual old plan finally cannot unlock a new device pending save; duplicate save/clear never writes',async()=>{
 const app=dashboard();seed(app.state);seed(app.state,dto(B));app.eval(`openPlanDialog(agents.get('${A}'))`);
 app.nodes.get('#plan-currency').value='EUR';const old=app.nodes.get('#plan-form').emit('submit'),oldWrite=app.request('/plan');
 await app.nodes.get('#plan-form').emit('submit');await app.nodes.get('#plan-clear').emit('click');assert.equal(app.requests.filter(r=>r.url.endsWith('/plan')).length,1);
 app.nodes.get('#plan-dialog').close();app.eval(`openPlanDialog(agents.get('${B}'))`);app.nodes.get('#plan-currency').value='GBP';
 const current=app.nodes.get('#plan-form').emit('submit'),newWrite=app.request('/plan');assert.notEqual(newWrite,oldWrite);
 oldWrite.resolve(response({currency:'EUR'}));await old;
 assert.equal(app.nodes.get('#plan-save').disabled,true);assert.equal(app.nodes.get('#plan-clear').disabled,true);assert.equal(app.nodes.get('#plan-dialog').open,true);assert.equal(app.nodes.get('#plan-currency').value,'GBP');
 newWrite.resolve(response({currency:'GBP'}));await current;assert.equal(app.nodes.get('#plan-dialog').open,false);assert.equal(app.nodes.get('#plan-save').disabled,false);
});

test('P5b actual clear only explicit same-dialog confirmation writes existing empty PUT, error preserves drafts',async()=>{
 const app=dashboard();seed(app.state);app.eval(`openPlanDialog(agents.get('${A}'))`);app.nodes.get('#plan-currency').value='EUR';
 const before=app.requests.length;await app.nodes.get('#plan-clear').emit('click');assert.equal(app.requests.length,before);assert.equal(app.eval('planEditor.snapshot().confirmation'),'clear');
 await app.nodes.get('#plan-continue').emit('click');assert.equal(app.requests.length,before);assert.equal(app.nodes.get('#plan-currency').value,'EUR');
 await app.nodes.get('#plan-clear').emit('click');const clearing=app.nodes.get('#plan-discard').emit('click'),write=app.request('/plan');
 assert.equal(write.options.method,'PUT');assert.deepEqual(JSON.parse(write.options.body),{});assert.equal(app.nodes.get('#plan-save').disabled,true);
 write.resolve(response({error:{message:'synthetic uncertain failure'}},503));await clearing;assert.equal(app.nodes.get('#plan-dialog').open,true);assert.equal(app.nodes.get('#plan-currency').value,'EUR');assert.match(app.nodes.get('#plan-error').textContent,/synthetic uncertain failure/);assert.equal(app.nodes.get('#plan-save').disabled,false);
});

test('P5b actual close dirty uses current values, country search is not persisted data and reverted input closes clean',async()=>{
 const app=dashboard();seed(app.state);app.eval(`openPlanDialog(agents.get('${A}'))`);
 app.nodes.get('#plan-currency').value='EUR';await app.nodes.get('#plan-cancel').emit('click');assert.equal(app.nodes.get('#plan-dialog').open,true);assert.equal(app.eval('planEditor.snapshot().confirmation'),'close');
 await app.nodes.get('#plan-continue').emit('click');assert.equal(app.nodes.get('#plan-dialog').open,true);
 app.nodes.get('#plan-currency').value='USD';app.nodes.get('#plan-country-search').value='germany';await app.nodes.get('#plan-cancel').emit('click');assert.equal(app.nodes.get('#plan-dialog').open,false);
 app.eval(`openPlanDialog(agents.get('${A}'))`);app.nodes.get('#plan-traffic-enabled').checked=!app.nodes.get('#plan-traffic-enabled').checked;
 await app.nodes.get('#plan-dismiss').emit('click');assert.equal(app.nodes.get('#plan-dialog').open,true);await app.nodes.get('#plan-discard').emit('click');assert.equal(app.nodes.get('#plan-dialog').open,false);assert.equal(app.requests.filter(r=>r.url.endsWith('/plan')).length,0);
});

const omittedDetail = () => {
  const value=dto(); for(const key of ['plan','traffic','country_code','country_source','country_code_lookup_operation','removal']) delete value[key];
  return value;
};

test('P5a2 actual dashboard reset port keeps uncertain original request across details close, explicit verify never adopts old receipt',async()=>{
 const app=dashboard();seed(app.state,{...dto(),traffic:{rx_total:800,tx_total:400,started_at:100}});
 app.eval(`resetAccumulatedTraffic('${A}')`);const first=app.request('/traffic/reset');assert.ok(first);
 const requestID=JSON.parse(first.options.body).request_id;assert.equal(requestID,'c'.repeat(32));assert.equal(first.options.headers['X-CSRF-Token'],'test-token');
 await app.eval(`resetAccumulatedTraffic('${A}')`);assert.equal(app.requests.filter(r=>r.url.endsWith('/traffic/reset')).length,1);
 first.resolve(response({},500));await flush();assert.equal(app.eval(`trafficResetRequests.get('${A}')`),requestID);
 app.eval('deviceDetails.close()');app.state.commitRead(app.state.read(A),{traffic:{rx_total:2000,tx_total:1000,started_at:200}});
 const verify=app.eval(`resetAccumulatedTraffic('${A}')`),second=app.request('/traffic/reset');assert.equal(JSON.parse(second.options.body).request_id,requestID);
 second.resolve(response({rx_total:0,tx_total:0,started_at:100}));await verify;assert.equal(app.state.agents.get(A).traffic.rx_total,2000);assert.equal(app.eval(`trafficResetRequests.has('${A}')`),false);
 assert.match(app.eval(`trafficResetFeedback.get('${A}')`),/不使用旧回执/);
});
test('P5a2 actual dashboard reset guards offline/stale/paused/revoked/CSRF and rejected confirmation without writes',async()=>{
 const app=dashboard();const valid={...dto(),traffic:{rx_total:800,tx_total:400}};
 for(const change of [{online:false},{disabled_at:1},{revoked:true},{state:{stale:true}},{traffic:null}]){seed(app.state,{...valid,...change});await app.eval(`resetAccumulatedTraffic('${A}')`);}
 seed(app.state,valid);app.eval("mutationCSRFToken=''");await app.eval(`resetAccumulatedTraffic('${A}')`);app.eval("mutationCSRFToken='test-token'");app.setConfirm(()=>false);await app.eval(`resetAccumulatedTraffic('${A}')`);
 assert.equal(app.requests.filter(r=>r.url.endsWith('/traffic/reset')).length,0);
});
test('P5a2 actual dashboard reset navigation cancels, retains totals and clears live operation identities',async()=>{
 const app=dashboard();seed(app.state,{...dto(),traffic:{rx_total:800,tx_total:400}});
 app.eval('globalThis.resetRenders=0;render=()=>{resetRenders++;}');
 const work=app.eval(`resetAccumulatedTraffic('${A}')`),write=app.request('/traffic/reset');app.windowEvent('pagehide');assert.equal(write.options.signal.aborted,true);
 const renderedBeforeSettle=app.eval('resetRenders'),readCount=app.requests.length;
 write.resolve(response({rx_total:0,tx_total:0}));await work;assert.equal(app.state.agents.get(A).traffic.rx_total,800);assert.equal(app.eval('trafficResetRequests.size'),0);assert.equal(app.eval('trafficResetControllers.size'),0);
 assert.equal(app.eval('resetRenders'),renderedBeforeSettle);assert.equal(app.requests.length,readCount);
 assert.equal(app.eval('networkQuality.snapshot().cards'),0);assert.equal(app.eval('trafficCharts.snapshot().cards'),0);assert.equal(app.eval('monitorCoordinator.snapshot().active'),0);
});
test('complete detail clears actual omitempty fields; partial/list/upgrade never clear them',()=>{
  const s=State.create(); seed(s,{...dto(),traffic:{rx_total:1},removal:{status:'failed'},country_code_lookup_operation:{status:'failed'}});
  const list=s.beginRefresh(); s.commitList(list,[{agent_id:A,name:'list'}]); s.endRefresh(list);
  s.event({agent_id:A,state:{cpu_percent:81}}); s.commitRead(s.read(A,null,['upgrade']),{upgrade:null});
  assert.equal(s.agents.get(A).plan.currency,'USD'); assert.equal(s.agents.get(A).country_code,'US');
  s.commitDetail(s.read(A),omittedDetail());
  for(const key of ['plan','traffic','removal','country_code_lookup_operation']) assert.equal(s.agents.get(A)[key],null);
  assert.equal(s.agents.get(A).country_code,''); assert.equal(s.agents.get(A).country_source,'');
});

test('actual dashboard/history complete HTTP omission clears other-client fields, old omission cannot clear local save',async()=>{
  for(const file of ['app.js','history.js']) {
    const app=dashboard(file); seed(app.state); if(file==='history.js') app.eval(`currentAgent=agentState.agents.get('${A}')`);
    app.setRoute(url=>url.endsWith('/'+A)?response(omittedDetail()):undefined);
    app.event(omittedDetail()); await flush();
    assert.equal(app.state.agents.get(A).plan,null); assert.equal(app.state.agents.get(A).country_code,'');
    app.setRoute(()=>undefined);
    const late=app.eval(file==='app.js'?`readAgentDetail('${A}')`:`reconcileAgent('${A}',new AbortController().signal)`);
    const request=app.request('/'+A), write=app.state.beginMutation(A,['plan','country']);
    app.state.finishMutation(write,{plan:{currency:'GBP'},country_code:'GB',country_source:'manual'});
    request.resolve(response(omittedDetail())); await late;
    assert.equal(app.state.agents.get(A).plan.currency,'GBP'); assert.equal(app.state.agents.get(A).country_code,'GB');
  }
});

test('production JSON reader bounds hung fetch and body, and navigation abort settles exactly once',async()=>{
  for(const bodyHung of [false,true]) {
    let requestSignal;
    const fetcher=async(_,options)=>{requestSignal=options.signal;return bodyHung?{ok:true,status:200,json:()=>new Promise(()=>{})}:new Promise(()=>{});};
    await assert.rejects(State.fetchJSON('/hung',{fetcher,timeoutMs:15}),{name:'TimeoutError'});
    assert.equal(requestSignal.aborted,true);
  }
  const controller=new AbortController(); let settlements=0;
  const read=State.fetchJSON('/cancel',{fetcher:()=>new Promise(()=>{}),signal:controller.signal,timeoutMs:30}).catch(error=>{settlements++;throw error;});
  controller.abort(); await assert.rejects(read,{name:'AbortError'});
  await new Promise(resolve=>setTimeout(resolve,40)); assert.equal(settlements,1);
});

test('actual refresh: eight hung fetch/body/upgrade reads release workers and next cycle recovers',async()=>{
  const app=dashboard('app.js',25), ids=Array.from({length:9},(_,i)=>(i+1).toString(16).padStart(32,'0'));
  let recovered=false;
  app.setRoute(url=>{
    if(url.includes('?limit=100')) return response({items:ids.map(id=>dto(id))});
    const id=ids.find(id=>url.includes('/'+id)); if(!id) return undefined;
    if(recovered || id===ids[8]) return url.endsWith('/upgrade')?response(null,204):response(dto(id,77));
    if(ids.indexOf(id)%2) return {ok:true,status:200,json:()=>new Promise(()=>{})};
    return undefined;
  });
  await app.eval('refresh()');
  assert.equal(app.state.refreshing(),false); assert.equal(app.state.agents.get(ids[8]).state.cpu_percent,77);
  recovered=true; await app.eval('refresh()');
  for(const id of ids) assert.equal(app.state.agents.get(id).state.cpu_percent,77);
  assert.equal(app.state.refreshing(),false);
});

test('actual reconciler: hung slots time out without immediate retry, periodic retry and navigation cancel recover',async()=>{
  const app=dashboard('app.js',20), ids=Array.from({length:9},(_,i)=>(i+1).toString(16).padStart(32,'0'));
  for(const id of ids) seed(app.state,dto(id));
  let recovered=false;
  app.setRoute(url=>{const id=ids.find(id=>url.endsWith('/'+id));return id && (recovered || id===ids[8])?response(dto(id,82)):undefined;});
  for(const id of ids) app.event(dto(id));
  await new Promise(resolve=>setTimeout(resolve,55));
  assert.equal(app.state.agents.get(ids[8]).state.cpu_percent,82);
  assert.equal(app.requests.filter(r=>r.url.endsWith('/'+ids[0])).length,1);
  recovered=true; app.eval('reconciliation.retry()'); await flush();
  for(const id of ids) assert.equal(app.state.agents.get(id).state.cpu_percent,82);
  assert.equal(app.eval('reconciliation.size()'),0);
  app.setRoute(()=>undefined); app.event(dto(ids[0])); await flush();
  app.nodes.get('#stream').textContent='live'; app.windowEvent('pagehide'); await flush();
  assert.equal(app.request('/'+ids[0]).options.signal.aborted,true);
  assert.equal(app.eval('reconciliation.size()'),0); assert.equal(app.nodes.get('#stream').textContent,'live');
});

test('actual management failure retains per-entity feedback across resource-release renders',async()=>{
  const app=dashboard(); seed(app.state);
  for(const [group,call,endpoint] of [
    ['google','rerunGoogleStatus','/google-status'],['country','refreshCountryCodeLookup','/country-code/refresh']]) {
    const work=app.eval(`${call}(agents.get('${A}'),document.querySelector('#add-agent'),document.querySelector('#agents'))`);
    app.request(endpoint).resolve(response({error:{message:'real '+group+' failure'}},500)); await work;
    assert.equal(app.state.busy(A,group),false); assert.equal(app.eval(`agentActionErrors.get('${A}/${group}')`),'real '+group+' failure');
    assert.equal(app.state.agents.get(A).state.cpu_percent,10);
  }
});

test('actual history reconnect reflects missed configuration through one entity HTTP read, not full history',async()=>{
  const app=dashboard('history.js'); seed(app.state); app.eval(`currentAgent=agentState.agents.get('${A}')`);
  const before=app.requests.length;
  // The full configuration event was missed while disconnected; no new full event.
  app.sources[0].onopen(); await flush();
  app.event({agent_id:A,name:'old partial name',state:{cpu_percent:95,collected_at:1}});
  app.request('/'+A).resolve(response({...dto(A,1,'reconnected name'),plan:{currency:'EUR'},outbounds:{configured:true,selectors:[{name:'proxy',current:'new'}]}})); await flush();
  assert.equal(app.state.agents.get(A).name,'reconnected name'); assert.equal(app.state.agents.get(A).plan.currency,'EUR');
  assert.equal(app.state.agents.get(A).outbounds.selectors[0].current,'new'); assert.equal(app.state.agents.get(A).state.cpu_percent,95);
  app.event({agent_id:A,name:'old partial name',state:{cpu_percent:96}});
  assert.equal(app.state.agents.get(A).name,'reconnected name'); assert.equal(app.state.agents.get(A).plan.currency,'EUR');
  assert.deepEqual(app.requests.slice(before).map(r=>r.url),['/api/v1/web/agents/'+A]);
});

test('actual history reconnect coalesces pending-write notifications and preserves one in-flight dirty tail',async()=>{
  for(const success of [true,false]) {
    const app=dashboard('history.js'); seed(app.state); app.eval(`currentAgent=agentState.agents.get('${A}')`);
    const before=app.requests.length, write=app.state.beginMutation(A,['outbounds']);
    app.sources[0].onopen(); app.sources[0].onopen(); app.event(dto()); await flush();
    assert.equal(app.requests.length,before); assert.equal(app.eval('reconciliation.size()'),1);
    app.state.finishMutation(write,success?{outbounds:{configured:true,selectors:[]}}:{});
    // Actual mutation finalizers notify the same production queue on either result.
    app.eval(`reconciliation.notify('${A}')`); await flush();
    const first=app.request('/'+A);
    app.sources[0].onopen(); app.sources[0].onopen(); app.sources[0].onopen(); await flush();
    assert.equal(app.requests.length,before+1);
    first.resolve(response(dto(A,71,'first'))); await flush(); assert.equal(app.requests.length,before+2);
    app.request('/'+A).resolve(response(dto(A,72,'tail'))); await flush();
    assert.equal(app.state.agents.get(A).name,'tail'); assert.equal(app.eval('reconciliation.size()'),0);
    assert.equal(app.requests.length,before+2);
  }
});

test('actual page reconnect callbacks after navigation cannot create new reads or queued jobs',async()=>{
  for(const file of ['app.js','history.js']) {
    const app=dashboard(file); seed(app.state); app.windowEvent('pagehide'); await flush();
    const before=app.requests.length; app.sources[0].onopen(); await flush();
    assert.equal(app.requests.length,before); assert.equal(app.eval('reconciliation.size()'),0);
  }
});

test('P2a actual name accepts UTF-8 boundary offline and rejects Chinese overflow/newline/NUL',async()=>{
  const app=dashboard();seed(app.state,{...dto(),online:false});
  const input=app.nodes.get('#agents');
  for(const name of ['中'.repeat(34),'bad\nname','bad\0name','   ']) {
    input.value=name;const before=app.requests.length;
    await app.eval(`saveAgentName(agents.get('${A}'),document.querySelector('#agents'),document.querySelector('#add-agent'))`);
    assert.equal(app.requests.length,before);assert.match(input.validityMessage,/UTF-8/);
  }
  input.value='中'.repeat(33)+'a';
  app.eval(`editingAgentNames.add('${A}');agentNameDrafts.set('${A}',document.querySelector('#agents').value);agentNameEditGeneration.set('${A}',1)`);
  const work=app.eval(`saveAgentName(agents.get('${A}'),document.querySelector('#agents'),document.querySelector('#add-agent'))`);
  const write=app.request('/name');assert.equal(Buffer.byteLength(JSON.parse(write.options.body).name),100);
  write.resolve(response({name:input.value}));await work;assert.equal(app.state.agents.get(A).name,input.value);
});

test('P2a actual name cancel/reopen prevents late error and navigation cancels hung body',async()=>{
  const app=dashboard();seed(app.state);
  app.eval(`editingAgentNames.add('${A}');agentNameDrafts.set('${A}','old');agentNameEditGeneration.set('${A}',1)`);
  app.nodes.get('#agents').value='old';
  const work=app.eval(`saveAgentName(agents.get('${A}'),document.querySelector('#agents'),document.querySelector('#add-agent'))`);
  const group=app.eval(`agentNameControl(agents.get('${A}'),agents.get('${A}').state)`);
  await group.children[2].emit('click');
  app.eval(`editingAgentNames.add('${A}');agentNameDrafts.set('${A}','new draft');agentNameEditGeneration.set('${A}',3)`);
  app.request('/name').resolve(response({error:{message:'late error'}},500));await work;
  assert.equal(app.eval(`agentNameDrafts.get('${A}')`),'new draft');assert.equal(app.eval(`agentNameErrors.has('${A}')`),false);
  app.nodes.get('#agents').value='new draft';
  const hung=app.eval(`saveAgentName(agents.get('${A}'),document.querySelector('#agents'),document.querySelector('#add-agent'))`);
  app.request('/name').resolve({...response(null),json:()=>new Promise(()=>{})});await flush();app.windowEvent('pagehide');await hung;
  assert.equal(app.state.busy(A,'name'),false);assert.equal(app.eval(`agentNameDrafts.get('${A}')`),'new draft');
});

test('P2a actual country picker searches labels/codes, preserves current, clear only changes plan draft',async()=>{
  const app=dashboard();seed(app.state,{...dto(),plan:{currency:'USD',country_code_override:'US',quota_value:'5'}});
  app.eval(`openPlanDialog(agents.get('${A}'))`);
  const search=app.nodes.get('#plan-country-search'), select=app.nodes.get('#plan-country-code');
  for(const query of ['中国','China','CN']) {search.value=query;await search.emit('input');assert.ok(select.children.some(o=>o.value==='CN'));assert.equal(select.value,'US');}
  search.value='no-region-matches';await search.emit('input');assert.equal(select.value,'US');assert.match(app.nodes.get('#plan-country-status').textContent,/无匹配/);
  const before=app.requests.length;await app.nodes.get('#plan-country-auto').emit('click');
  assert.equal(select.value,'');assert.equal(app.requests.length,before);assert.equal(app.state.agents.get(A).plan.country_code_override,'US');
  const save=app.nodes.get('#plan-form').emit('submit');assert.equal(JSON.parse(app.request('/plan').options.body).country_code_override,'');
  assert.equal(JSON.parse(app.request('/plan').options.body).currency,'USD');
  app.request('/plan').resolve(response({currency:'USD',quota_value:'5'}));await save;
  assert.equal(app.state.agents.get(A).plan.currency,'USD');
});

test('P2a actual country accepted immediately blocks duplicate POST and preserves old/manual value',async()=>{
  const app=dashboard();seed(app.state);
  const call=`refreshCountryCodeLookup(agents.get('${A}'),document.querySelector('#add-agent'),document.querySelector('#agents'))`;
  const work=app.eval(call);await app.eval(call);
  assert.equal(app.requests.filter(r=>r.url.endsWith('/country-code/refresh')).length,1);
  app.request('/country-code/refresh').resolve(response({operation_id:'operation-one',status:'requested'},202));await work;
  assert.equal(app.state.agents.get(A).country_code_lookup_operation.status,'requested');assert.equal(app.state.agents.get(A).country_code,'US');
  await app.eval(call);assert.equal(app.requests.filter(r=>r.url.endsWith('/country-code/refresh')).length,1);
  const controls=app.eval(`countryCodeLookupControls(agents.get('${A}'))`);assert.equal(controls.children[0].disabled,true);
  assert.match(controls.children[1].textContent,/已排队/);
});

test('P2a actual country timeout retains old operation and does not create an automatic/new POST',async()=>{
  const app=dashboard('app.js',20);seed(app.state,{...dto(),country_code_lookup_operation:{operation_id:'old',status:'succeeded'}});
  const call=`refreshCountryCodeLookup(agents.get('${A}'),document.querySelector('#add-agent'),document.querySelector('#agents'))`;
  await app.eval(call);app.eval("mutationCSRFToken='test-token'");const controls=app.eval(`countryCodeLookupControls(agents.get('${A}'))`);
  assert.equal(controls.children[0].disabled,false);assert.match(controls.children[0].textContent,/核实/);assert.match(controls.children[1].textContent,/不确定/);
  await app.eval(call);assert.equal(app.requests.filter(r=>r.url.endsWith('/country-code/refresh')).length,1);
  assert.equal(app.state.agents.get(A).country_code,'US');
});

test('P2a actual traffic uncertain retry uses original ID, duplicate disabled, merge only traffic',async()=>{
  const app=dashboard('history.js',20);seed(app.state,{...dto(),traffic:{rx_total:100,tx_total:200,started_at:1}});
  app.eval(`currentAgent=agentState.agents.get('${A}')`);
  const button=app.nodes.get('#traffic-reset');button.disabled=false;
  const first=button.emit('click');await button.emit('click');await first;
  const one=app.request('/traffic/reset');const original=JSON.parse(one.options.body).request_id;
  assert.equal(app.state.agents.get(A).traffic.rx_total,100);assert.equal(app.eval('trafficRequest'),original);
  assert.match(app.nodes.get('#traffic-reset-feedback').textContent,/不确定/);
  button.disabled=false;const second=button.emit('click');const two=app.request('/traffic/reset');assert.equal(JSON.parse(two.options.body).request_id,original);
  two.resolve(response({rx_total:0,tx_total:0,started_at:2}));await second;
  assert.equal(app.state.agents.get(A).traffic.rx_total,100);assert.equal(app.state.agents.get(A).plan.currency,'USD');assert.equal(app.eval('trafficRequest'),null);
  assert.match(app.nodes.get('#traffic-reset-feedback').textContent,/已确认原请求执行/);
});

test('P2a actual traffic cancelled/deleted entity cannot accept late totals or feedback',async()=>{
  for(const action of ['delete','pagehide']) {
    const app=dashboard('history.js');seed(app.state,{...dto(),traffic:{rx_total:100,started_at:1}});app.eval(`currentAgent=agentState.agents.get('${A}')`);
    app.nodes.get('#traffic-reset').disabled=false;const work=app.nodes.get('#traffic-reset').emit('click');const write=app.request('/traffic/reset');
    if(action==='delete') app.eval(`agentState.beginDelete('${A}')`);else app.windowEvent('pagehide');
    write.resolve(response({rx_total:0,started_at:2}));await work;
    assert.equal(app.state.agents.get(A).traffic.rx_total,100);assert.doesNotMatch(app.nodes.get('#traffic-reset-feedback').textContent,/已从当前有效样本/);
  }
});

test('P2a actual late name failure does not annotate subsequently changed input',async()=>{
  const app=dashboard();seed(app.state);app.nodes.get('#agents').value='old submitted';
  app.eval(`editingAgentNames.add('${A}');agentNameDrafts.set('${A}','old submitted');agentNameEditGeneration.set('${A}',1)`);
  const work=app.eval(`saveAgentName(agents.get('${A}'),document.querySelector('#agents'),document.querySelector('#add-agent'))`);
  app.nodes.get('#agents').value='new input';app.eval(`agentNameDrafts.set('${A}','new input')`);
  app.request('/name').resolve(response({error:{message:'old failed'}},500));await work;
  assert.equal(app.eval(`agentNameErrors.has('${A}')`),false);assert.equal(app.eval(`agentNameDrafts.get('${A}')`),'new input');assert.equal(app.state.busy(A,'name'),false);
});

test('P2a actual old reset receipt cannot roll back later reset/sample; detail confirms current',async()=>{
  const app=dashboard('history.js');seed(app.state,{...dto(),traffic:{rx_total:45,tx_total:67,started_at:300}});
  app.eval(`currentAgent=agentState.agents.get('${A}');trafficRequest='${'d'.repeat(32)}'`);
  app.nodes.get('#traffic-reset').disabled=false;const work=app.nodes.get('#traffic-reset').emit('click');
  app.request('/traffic/reset').resolve(response({rx_total:0,tx_total:0,started_at:100}));await work;await flush();
  assert.equal(app.state.agents.get(A).traffic.rx_total,45);assert.equal(app.state.agents.get(A).traffic.started_at,300);
  app.request('/'+A).resolve(response({...dto(),traffic:{rx_total:50,tx_total:70,started_at:300}}));await flush();
  assert.equal(app.state.agents.get(A).traffic.rx_total,50);assert.equal(app.state.agents.get(A).traffic.started_at,300);
});

test('P2a actual uncertain country no-record verification offers explicit new operation, accepted remains disabled',async()=>{
  for(const accepted of [false,true]) {
    const app=dashboard();seed(app.state);let confirmations=0;app.setConfirm(message=>{confirmations++;assert.match(message,/前请求结果仍不确定/);return false;});
    app.eval(`uncertainCountryRequests.set('${A}','')`);
    const work=app.eval(`verifyCountryRequest(agents.get('${A}'),document.querySelector('#add-agent'),document.querySelector('#agents'))`);
    app.request('/'+A).resolve(response({...dto(),country_code_lookup_operation:accepted?{operation_id:'new',status:'requested'}:undefined}));await work;
    assert.equal(app.requests.filter(r=>r.options.method==='POST').length,0);
    const controls=app.eval(`countryCodeLookupControls(agents.get('${A}'))`);
    if(accepted){assert.equal(confirmations,0);assert.equal(controls.children[0].disabled,true);assert.match(controls.children[1].textContent,/已排队/);}
    else {assert.equal(confirmations,1);assert.equal(controls.children[0].disabled,false);assert.match(controls.children[1].textContent,/无法断言失败/);}
  }
});

const renewalPreview=(field='renewal_date',revision='1'.repeat(32))=>({eligible:true,field,from_date:field==='renewal_date'?'2026-01-31':'2027-01-31',to_date:field==='renewal_date'?'2026-02-28':'2027-02-28',unchanged_field:field==='renewal_date'?'expiry_date':'renewal_date',unchanged_date:field==='renewal_date'?'2027-01-31':'2026-01-31',period:'monthly',timezone:'UTC',anchor_source:'purchase_date',expected_revision:revision,new_overdue:false,due_today:false});
const renewalPlan={agent_id:A,renewal_date:'2026-01-31',expiry_date:'2027-01-31',renewal_price_period:'monthly',timezone:'UTC',currency:'USD'};
async function readyRenewal(app){seed(app.state,{...dto(),plan:renewalPlan});app.eval(`openPlanRenewal(agents.get('${A}'))`);app.request('/renewal/preview').resolve(response(renewalPreview()));await flush();}

test('P5c actual ready hook rejects failed/stale list cycle; only committed empty list becomes read zero',async()=>{
 const app=dashboard();app.eval('actualDashboardRender()');assert.equal(app.nodes.get('#overview-online').textContent,'等待读取');
 const failed=app.eval('refresh()');app.request('agents?limit').resolve(response({error:{message:'failed'}},500));await failed;
 assert.equal(app.eval('overviewReady'),false);app.eval('actualDashboardRender()');assert.equal(app.nodes.get('#overview-online').textContent,'等待读取');
 const old=app.eval('refresh()'),oldRequest=app.request('agents?limit');app.eval('agentState.beginRefresh()');oldRequest.resolve(response({items:[],next_cursor:''}));await old;assert.equal(app.eval('overviewReady'),false);
 const current=app.eval('refresh()');app.request('agents?limit').resolve(response({items:[],next_cursor:''}));await current;assert.equal(app.eval('overviewReady'),true);app.eval('actualDashboardRender()');assert.equal(app.nodes.get('#overview-online').textContent,'0 / 0');
});
test('P5c actual app uses exact primary card deadline with timezone; current plan write and deletion change aggregates',()=>{
 const app=dashboard();const today=new Date().toISOString().slice(0,10);seed(app.state,{...dto(),plan:{...renewalPlan,renewal_date:today,expiry_date:'9999-12-31',timezone:'Pacific/Kiritimati'},state:null});
 seed(app.state,{...dto(B),plan:{renewal_date:'',expiry_date:today,timezone:'Pacific/Honolulu',renewal_price_period:'once'},online:false,state:null});
 app.eval('overviewReady=true;actualDashboardRender()');const expected=app.eval("[...agents.values()].reduce((s,a)=>{const t=planDeadline(a.plan).tone;if(t==='danger')s.due++;if(t==='warn')s.soon++;return s;},{due:0,soon:0})");assert.equal(app.nodes.get('#overview-dates').textContent,`7日内 ${expected.soon} · 已到期/续费日已过 ${expected.due}`);
 const mutation=app.state.beginMutation(A,['plan']);app.state.finishMutation(mutation,{plan:{...renewalPlan,renewal_date:'9999-11-30'}});app.eval('actualDashboardRender()');assert.equal(app.eval("Overview.aggregate(agents,{deadline:planDeadline}).due"),app.eval(`planDeadline(agents.get('${B}').plan).tone==='danger'?1:0`));
 const deletion=app.state.beginDelete(B);app.state.finishDelete(deletion,true);app.eval('actualDashboardRender()');assert.equal(app.nodes.get('#overview-online').textContent,'1 / 1');assert.equal(app.nodes.get('#overview-dates').textContent,'7日内 0 · 已到期/续费日已过 0');
});
test('P5c actual render twenty SSE keeps stable native overview, focus and original plan dirty draft without new requests/writes',async()=>{
 const app=dashboard();seed(app.state,{...dto(),state:null});app.eval(`overviewReady=true;render=actualDashboardRender;openPlanDialog(agents.get('${A}'))`);app.nodes.get('#plan-currency').value='GBP';await app.nodes.get('#plan-currency').emit('input');
 app.eval("document.activeElement=document.querySelector('#overview-summary')");const shell=app.nodes.get('#overview'),summary=app.nodes.get('#overview-summary');shell.open=false;await shell.emit('toggle');const before=app.requests.length;
 for(let i=0;i<20;i++)app.event({agent_id:A,online:true,state:{collected_at:Date.now(),rx_rate:0,tx_rate:1000000,cpu_percent:i}});
 assert.equal(app.nodes.get('#overview'),shell);assert.equal(app.nodes.get('#overview-summary'),summary);assert.equal(shell.open,false);assert.equal(app.eval("document.activeElement===document.querySelector('#overview-summary')"),true);assert.equal(app.nodes.get('#plan-currency').value,'GBP');assert.equal(app.nodes.get('#plan-dialog').open,true);assert.equal(app.requests.length,before);assert.match(app.nodes.get('#overview-rates').textContent,/0 B\/s.*1.00 MB\/s/);
});
test('P5c actual renewal merges current_plan not replay receipt into overview and unknown original ID remains across renders',async()=>{
 const app=dashboard();await readyRenewal(app);app.eval('overviewReady=true;render=actualDashboardRender');const applying=app.nodes.get('#renewal-confirm').emit('click');const request=app.request('/renewal/apply'),id=JSON.parse(request.options.body).request_id;request.resolve(response({error:{message:'uncertain'}},503));await applying;await flush();
 for(let i=0;i<20;i++)app.event({agent_id:A,state:{collected_at:Date.now(),rx_rate:0,tx_rate:0,cpu_percent:i}});assert.equal(app.eval(`planRenewal.snapshot().records.find(r=>r.id==='${A}').unknown.dto.request_id`),id);
 const verifying=app.nodes.get('#renewal-verify').emit('click');const verify=app.requests.findLast(r=>r.url.includes('/renewal/apply'));verify.resolve(response(renewalResult(verify,{...renewalPlan,renewal_date:'9999-11-30'},true,B)));await verifying;await flush();assert.equal(app.nodes.get('#overview-dates').textContent,'7日内 0 · 已到期/续费日已过 0');assert.equal(app.state.agents.get(A).plan.renewal_date,'9999-11-30');
});
function renewalResult(request,current={...renewalPlan,renewal_date:'2026-02-28'},replayed=false,undoID=null){const q=JSON.parse(request.options.body),now=Date.now();return {replayed,current_revision:'2'.repeat(32),current_plan:current,operation:{request_id:q.request_id,kind:'apply',field:q.field,from_date:q.from_date,to_date:q.to_date,applied_at:now,undo_until:now+300000},undo:{available:true,operation_id:undoID||q.request_id}};}
test('P5b1c actual card footer has renewal in order; offline, disabled and absent state do not gate button',()=>{
 const app=dashboard();seed(app.state,{...dto(),online:false,state:null,disabled_at:100});app.eval('actualDashboardRender()');
 const card=app.nodes.get('#agents').children[0],actions=card.children.at(-1);assert.equal(actions.children[0].className,'footer-price');assert.equal(actions.children.length,5);assert.deepEqual(Array.from(actions.children.slice(2),x=>x.className),['renew-plan','edit-plan','detail-toggle']);assert.equal(actions.children[2].textContent,'已续费');assert.equal(actions.children[2].disabled,false);
 assert.equal(app.requests.filter(r=>r.url.includes('/renewal/')).length,0);
});
test('P5b1c actual first click and SSE20 are preview-only; explicit confirm once and plan-only merge',async()=>{
 const app=dashboard();await readyRenewal(app);const count=app.requests.length;
 for(let i=0;i<20;i++)app.event({agent_id:A,state:{cpu_percent:i,collected_at:100+i}});await flush();assert.equal(app.requests.length,count);
 const apply=app.eval('planRenewal.confirm()'),req=app.request('/renewal/apply'),q=JSON.parse(req.options.body);assert.equal(req.options.method,'POST');assert.equal(req.options.headers['X-CSRF-Token'],'test-token');assert.equal(q.expected_revision,'1'.repeat(32));assert.equal(q.new_overdue,false);assert.equal(q.due_today,false);assert.match(q.request_id,/^[a-f0-9]{32}$/);await app.eval('planRenewal.confirm()');assert.equal(app.requests.filter(r=>r.url.endsWith('/renewal/apply')).length,1);
 req.resolve(response(renewalResult(req)));await apply;assert.equal(app.state.agents.get(A).state.cpu_percent,19);assert.equal(app.state.agents.get(A).name,'original');assert.equal(app.state.agents.get(A).country_code,'US');assert.equal(app.state.agents.get(A).plan.renewal_date,'2026-02-28');assert.equal(app.eval(`planRenewal.unconfirmed('${A}')`),false);
});
test('P5b1c actual full PUT and clear are blocked by pending and unknown renewal; same-ID verification unblocks',async()=>{
 const app=dashboard();await readyRenewal(app);const apply=app.eval('planRenewal.confirm()'),req=app.request('/renewal/apply');app.eval(`openPlanDialog(agents.get('${A}'))`);assert.equal(app.nodes.get('#plan-dialog').open,false);
 app.eval(`pendingPlanAgentID='${A}'`);await app.nodes.get('#plan-form').emit('submit');await app.eval('clearPlan()');assert.equal(app.requests.filter(r=>r.options.method==='PUT').length,0);
 req.reject(Error('lost reply'));await apply;assert.equal(app.state.busy(A,'plan'),false);assert.equal(app.eval(`planRenewal.unconfirmed('${A}')`),true);
 app.eval(`openPlanDialog(agents.get('${A}'))`);assert.equal(app.nodes.get('#plan-dialog').open,false);app.eval(`planRenewal.open('${A}')`);const verify=app.eval('planRenewal.verify()'),retry=app.request('/renewal/apply');assert.equal(retry.options.body,req.options.body);retry.resolve(response(renewalResult(retry)));await verify;app.eval(`openPlanDialog(agents.get('${A}'))`);assert.equal(app.nodes.get('#plan-dialog').open,true);
});
test('P5b1c actual PUT in flight hands off presentation but renewal cannot write until resource release',async()=>{
 const app=dashboard();seed(app.state,{...dto(),plan:renewalPlan});app.eval(`openPlanDialog(agents.get('${A}'))`);const save=app.nodes.get('#plan-form').emit('submit'),put=app.request('/plan');app.eval(`openPlanRenewal(agents.get('${A}'))`);assert.equal(app.eval('planEditor.snapshot().confirmation'),'switch');await app.nodes.get('#plan-discard').emit('click');assert.equal(app.nodes.get('#renewal-dialog').open,true);assert.equal(app.requests.filter(r=>r.url.includes('/renewal/')).length,0);await app.eval('planRenewal.confirm()');put.resolve(response(renewalPlan));await save;const refresh=app.eval('planRenewal.load()');app.request('/renewal/preview').resolve(response(renewalPreview()));await refresh;assert.equal(app.nodes.get('#renewal-confirm').disabled,false);
});
test('P5b1c actual dirty PlanEditor handoff protects draft; continue/cancel do not preview, discard does',async()=>{
 const app=dashboard();seed(app.state,{...dto(),plan:renewalPlan});app.eval(`openPlanDialog(agents.get('${A}'))`);app.nodes.get('#plan-currency').value='EUR';app.eval(`openPlanRenewal(agents.get('${A}'))`);assert.equal(app.nodes.get('#renewal-dialog').open,false);await app.nodes.get('#plan-continue').emit('click');assert.equal(app.nodes.get('#plan-currency').value,'EUR');assert.equal(app.requests.filter(r=>r.url.includes('/renewal/')).length,0);
 app.eval(`openPlanRenewal(agents.get('${A}'))`);await app.nodes.get('#plan-dialog').emit('cancel');assert.equal(app.nodes.get('#plan-currency').value,'EUR');app.eval(`openPlanRenewal(agents.get('${A}'))`);await app.nodes.get('#plan-discard').emit('click');assert.equal(app.nodes.get('#plan-dialog').open,false);assert.equal(app.nodes.get('#renewal-dialog').open,true);app.request('/renewal/preview').resolve(response(renewalPreview()));await flush();
});
test('P5b1c actual stale field response and replay A with current B cannot roll back dates or guess B undo',async()=>{
 const app=dashboard();await readyRenewal(app);const older=app.eval("planRenewal.load('renewal_date')"),old=app.request('/renewal/preview'),newer=app.eval("planRenewal.load('expiry_date')"),fresh=app.request('/renewal/preview');fresh.resolve(response(renewalPreview('expiry_date')));await newer;old.resolve(response(renewalPreview()));await older;assert.equal(app.eval('planRenewal.snapshot().preview.field'),'expiry_date');const apply=app.eval('planRenewal.confirm()'),req=app.request('/renewal/apply');req.reject(Error('lost'));await apply;const verify=app.eval('planRenewal.verify()'),retry=app.request('/renewal/apply');retry.resolve(response(renewalResult(retry,{...renewalPlan,renewal_date:'2026-04-30'},true,B)));await verify;assert.equal(app.state.agents.get(A).plan.renewal_date,'2026-04-30');assert.equal(app.eval('planRenewal.snapshot().records[0].latest'),null);
});
test('P5b1c actual pagehide and deletion stop renewal late rendering and cannot resurrect records',async()=>{
 for(const mode of ['pagehide','delete']){const app=dashboard();await readyRenewal(app);app.eval('var renewalRenders=0;render=()=>renewalRenders++');const apply=app.eval('planRenewal.confirm()'),req=app.request('/renewal/apply');if(mode==='pagehide')app.windowEvent('pagehide');else app.eval(`var renewalDelete=agentState.beginDelete('${A}');agentState.finishDelete(renewalDelete,true);planRenewal.sync()`);const count=app.eval('renewalRenders');req.resolve(response(renewalResult(req)));await apply;assert.equal(req.options.signal.aborted,true);assert.equal(app.eval('renewalRenders'),count);assert.equal(app.eval('planRenewal.snapshot().records.length'),0);assert.equal(app.nodes.get('#renewal-dialog').open,false);if(mode==='delete')assert.equal(app.state.agents.has(A),false);}
});
test('P5b1c actual undo confirmation shows inverse receipt, no forward preview, independent undo DTO and only returned current plan',async()=>{
 const app=dashboard();await readyRenewal(app);const apply=app.eval('planRenewal.confirm()'),req=app.request('/renewal/apply');req.resolve(response(renewalResult(req)));await apply;const count=app.requests.length;await app.nodes.get('#renewal-undo').emit('click');assert.equal(app.requests.length,count);assert.equal(app.nodes.get('#renewal-from').textContent,'2026-02-28');assert.equal(app.nodes.get('#renewal-to').textContent,'2026-01-31');assert.match(app.nodes.get('#renewal-detail').textContent,/撤销本次登记/);assert.match(app.nodes.get('#renewal-detail').textContent,/到期日期 2027-01-31 保持不变/);assert.equal(app.nodes.get('#renewal-anchor').textContent,'');assert.equal(app.nodes.get('#renewal-warning').textContent,'');assert.equal(app.nodes.get('#renewal-confirm').textContent,'确认撤销此次登记');
 const undo=app.eval('planRenewal.confirm()'),u=app.request('/renewal/undo'),q=JSON.parse(u.options.body),original=JSON.parse(req.options.body);assert.deepEqual(Object.keys(q).sort(),['request_id','operation_id','expected_revision'].sort());assert.notEqual(q.request_id,original.request_id);assert.equal(q.operation_id,original.request_id);u.resolve(response({replayed:false,current_revision:'3'.repeat(32),current_plan:renewalPlan,operation:{request_id:q.request_id,kind:'undo',field:'renewal_date',from_date:'2026-02-28',to_date:'2026-01-31',applied_at:Date.now()},undo:{available:false}}));await undo;assert.equal(app.state.agents.get(A).plan.renewal_date,'2026-01-31');assert.equal(app.state.agents.get(A).country_code,'US');assert.equal(app.eval('planRenewal.snapshot().records[0].latest'),null);
});
test('P5b1c actual year 9999 undo bypasses forward preview; equal-value external PUT CAS409 stays visible without auto repeat',async()=>{
 const app=dashboard();seed(app.state,{...dto(),plan:{...renewalPlan,renewal_date:'9999-11-30'}});app.eval(`openPlanRenewal(agents.get('${A}'))`);app.request('/renewal/preview').resolve(response({...renewalPreview(),from_date:'9999-11-30',to_date:'9999-12-31'}));await flush();const apply=app.eval('planRenewal.confirm()'),req=app.request('/renewal/apply');req.resolve(response(renewalResult(req,{...renewalPlan,renewal_date:'9999-12-31'})));await apply;const count=app.requests.length;await app.nodes.get('#renewal-undo').emit('click');assert.equal(app.requests.length,count);assert.equal(app.nodes.get('#renewal-from').textContent,'9999-12-31');assert.equal(app.nodes.get('#renewal-to').textContent,'9999-11-30');assert.equal(app.nodes.get('#renewal-confirm').disabled,false);const undo=app.eval('planRenewal.confirm()'),u=app.request('/renewal/undo');assert.equal(JSON.parse(u.options.body).expected_revision,'2'.repeat(32));u.resolve(response({error:{message:'synthetic equal-value PUT revision conflict'}},409));await undo;assert.match(app.nodes.get('#renewal-error').textContent,/重新预览/);assert.equal(app.state.agents.get(A).plan.renewal_date,'9999-12-31');assert.equal(app.eval('planRenewal.snapshot().records[0].latest'),null);const after=app.requests.length;await app.eval('planRenewal.confirm()');assert.equal(app.requests.length,after);assert.doesNotMatch(app.nodes.get('#renewal-result').textContent,/撤销已确认/);
});
