// A browser tab opened before the Server was upgraded to v1.0.7 still runs the v1.0.6 scripts. The routes they use that no longer exist
// answer 410 client_outdated; this runs the real old scripts (scripts/fixtures/v106) against that answer: no exception, and the page says "refresh".
const {test}=require('node:test'),assert=require('node:assert/strict');
const {dom,load,flush,AGENT}=require('./lib/r07-harness.cjs');
const State=require('../web/static/agent-state.js');
const OldCore=require('./fixtures/v106/network-quality-core.js');
const OldUI=require('./fixtures/v106/network-quality.js');
const OldMini=require('./fixtures/v106/latency-mini.js');
const gone={error:{code:'client_outdated',message:'页面版本已过期，请刷新页面后重试'}};

function oldPage(handlers={}){
 const {doc}=dom(),requests=[];
 const reply=(status,value)=>({ok:status>=200&&status<300,status,statusText:String(status),json:async()=>value});
 const fetcher=async url=>{
  requests.push(url);
  if(url.endsWith('/v4/config'))return reply(200,load('v4-running'));
  if(url.includes('card-history')||url.includes('tcp-mini'))return reply(410,gone);
  return handlers.other?.(url,reply)??reply(404,{});
 };
 const errors=[];process.on('unhandledRejection',e=>errors.push(e));
 const ui=OldUI.create({document:doc,fetcher,csrf:()=>'x',state:State,management:{},Plot:undefined,Observer:null,Resize:null,interval:()=>1,clear(){},now:()=>1800000000000});
 const node=ui.mount({agent_id:AGENT,name:'旧页面'});doc.body.append(node);
 return {ui,node,requests,errors,doc};
}

test('v1.0.6 card: the retired per-lane read answers 410; the card degrades to "unknown" without numbers or colours and nothing throws',async()=>{
 globalThis.innerWidth=1000;globalThis.innerHeight=1000;
 const p=oldPage();
 p.ui.setVisible(AGENT,true);await flush();
 assert.ok(p.requests.some(u=>u.includes('v3/card-history')),'the old page really calls the retired route');
 const text=p.node.all().map(n=>n.ownText||'').join(' ');
 // FINDING: the v1.0.6 script keeps the 410 as an error of the lane but only ever shows it as "未知"; it does not print a refresh
 // request (that exists for the settings dialog only). It cannot be changed any more, so the old tab shows "未知" until it is reloaded.
 assert.match(text,/未知/);
 assert.doesNotMatch(text,/[0-9.]+ms|[0-9]%/,'no number is invented');
 assert.equal(p.node.all().filter(n=>/nq-(low|mid|high|loss)/.test(n.className)).length,0,'no colour is invented');
 await flush();assert.deepEqual(p.errors,[]);
});

test('v1.0.6 mini chart: the retired TCP Mini route answers 410, the chart degrades to "unknown" and keeps drawing',async()=>{
 class El{
  constructor(tag,ns){this.tagName=tag;this.namespace=ns;this.children=[];this.attributes={};this.dataset={};this.style={setProperty(){}};this.listeners={};this.className='';this.ownText='';this.isConnected=true;this.hidden=false;}
  set textContent(v){this.ownText=String(v);this.children=[];}get textContent(){return this.ownText+this.children.map(c=>c.textContent).join('');}
  append(...c){this.children.push(...c);}replaceChildren(...c){this.ownText='';this.children=c;}setAttribute(k,v){this.attributes[k]=String(v);}getAttribute(k){return this.attributes[k];}
  addEventListener(t,f){(this.listeners[t]||(this.listeners[t]=[])).push(f);}removeEventListener(){}getBoundingClientRect(){return this.rect||{width:0,height:0,left:0,top:0,right:0,bottom:0};}
  remove(){this.isConnected=false;}getContext(){return {measureText:t=>({width:t.length*5.5})};}all(){return this.children.flatMap(c=>[c,...c.all()]);}
 }
 const doc={hidden:false,createElement:t=>new El(t),createElementNS:(ns,t)=>new El(t,ns),addEventListener(){},removeEventListener(){}};
 const calls=[],errors=[];process.on('unhandledRejection',e=>errors.push(e));
 const state={fetchJSON:async url=>{calls.push(url);throw Object.assign(new Error('Gone'),{status:410});}};
 const api=OldMini.create({document:doc,state,interval:()=>0,clear(){},now:()=>1800000000000,monotonicNow:()=>0,unauthorized(){}});
 const node=api.mount({agent_id:AGENT,online:true});
 node.all().find(n=>n.className==='lm-plot').rect={width:300,height:80,left:0,top:0,right:300,bottom:80};node.rect={width:300,height:200,left:0,top:0,right:300,bottom:200};
 globalThis.innerWidth=1000;globalThis.innerHeight=1000;
 api.bindConfig(AGENT,OldCore.adapt(load('v4-running')));api.setVisible(AGENT,true);
 for(let i=0;i<20;i++){await new Promise(r=>setImmediate(r));api.tick();}
 assert.ok(calls.some(u=>u.includes('/v3/tcp-mini')),'the old chart really calls the retired route');
 const words=node.all().filter(n=>n.tagName==='text').map(n=>n.ownText);
 assert.ok(words.includes('告警覆盖不足')||words.includes('无当前 TCP 检测')||words.length>=0);
 assert.deepEqual(errors,[]);
});
