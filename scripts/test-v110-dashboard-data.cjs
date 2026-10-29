const {test}=require('node:test'),assert=require('node:assert/strict');
const State=require('../web/static/agent-state.js'),Data=require('../web/static/dashboard-data.js');
const A='a'.repeat(32),B='b'.repeat(32),tick=()=>new Promise(setImmediate);
const row=id=>({agent_id:id,name:id,online:true,revoked:false,group_id:null,tag_ids:[],state:{cpu_percent:1},traffic:{quota_state:'current'},accumulated_traffic:{rx_total:12},plan:{renewal_price:'1'}});
const response=items=>({items,revision:1,groups:[],tags:[],exchange_rates:[],expenses:{monthly_usd:'1.00',yearly_usd:'12.00'}});
function fixture(){const state=State.create(),frames=[],calls=[],pending=[];let next=response([row(A),row(B)]);const api=Data.create({state,frame:f=>frames.push(f),read:async url=>{calls.push(url);if(next)return next;return new Promise((resolve,reject)=>pending.push({resolve,reject}));}});return {state,api,frames,calls,pending,set(v){next=v;},async frame(){frames.shift()?.();await tick();}};}
test('dashboard starts with one batch and same-frame identities share one ids request',async()=>{
 const f=fixture();await f.api.refresh();assert.deepEqual(f.calls,['/api/v1/web/dashboard']);assert.equal(f.state.agents.get(A).traffic.rx_total,12);
 f.set(null);for(let i=0;i<600;i++)f.api.notify(i%2?A:B);assert.equal(f.frames.length,1);await f.frame();assert.equal(f.calls.length,2);assert.ok(f.calls[1].includes(A)&&f.calls[1].includes(B));f.pending.shift().resolve(response([row(A),row(B)]));await tick();f.api.close();
});
test('an older dashboard answer cannot cross a plan mutation or newer accepted runtime event',async()=>{
 const f=fixture();await f.api.refresh();f.set(null);f.api.notify(A);await f.frame();
 const mutation=f.state.beginMutation(A,['plan']);f.state.finishMutation(mutation,{plan:{renewal_price:'9'}});f.state.event({agent_id:A,state:{cpu_percent:88}});
 f.pending.shift().resolve(response([row(A)]));await tick();assert.equal(f.state.agents.get(A).plan.renewal_price,'9');assert.equal(f.state.agents.get(A).state.cpu_percent,88);f.api.close();
});
test('404 falls back once to a full read; absent devices retire while unrelated devices remain',async()=>{
 const f=fixture();await f.api.refresh();f.set(null);f.api.notify(A);await f.frame();f.pending.shift().reject(Object.assign(Error(),{status:404}));await tick();f.set(response([row(B)]));await f.frame();await tick();assert.equal(f.calls.at(-1),'/api/v1/web/dashboard');assert.equal(f.state.agents.has(A),false);assert.equal(f.state.agents.has(B),true);f.api.close();
});
test('failed partial waits for calibration instead of retrying in an animation loop',async()=>{
 const f=fixture();await f.api.refresh();f.set(null);f.api.notify(A);await f.frame();f.pending.shift().reject(Error('unavailable'));await tick();assert.equal(f.frames.length,0);assert.equal(f.calls.length,2);f.api.close();
});
test('metadata revoke uses the lifetime barrier and cannot be resurrected by an old response',async()=>{
 const f=fixture();await f.api.refresh();f.set(null);f.api.notify(A);await f.frame();f.api.metadata({agent_id:A,revoked:true,kind:'revoke'});f.pending.shift().resolve(response([row(A)]));await tick();assert.equal(f.state.agents.has(A),false);f.api.close();
});
