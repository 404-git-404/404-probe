// Face B of the traffic module (v1.0.7): the last hour of per-minute mean rates, from resources/history?hours=1.
const {test}=require('node:test'),assert=require('node:assert/strict');
const T=require('../web/static/traffic-chart.js');
const NOW=1792080030000,M=60000;
class El{
 constructor(tag,ns){this.tagName=tag;this.namespace=ns;this.children=[];this.attributes={};this.dataset={};this.style={};this.className='';this.ownText='';this.isConnected=true;this.hidden=false;this.parentNode=null;}
 set textContent(v){this.ownText=String(v);this.children=[];}get textContent(){return this.ownText+this.children.map(c=>c.textContent).join('');}
 append(...c){for(const n of c)n.parentNode=this;this.children.push(...c);}replaceChildren(...c){this.ownText='';for(const n of c)n.parentNode=this;this.children=c;}
 setAttribute(k,v){this.attributes[k]=String(v);}getBoundingClientRect(){return this.rect||{width:300,height:24,left:0,top:0,right:300,bottom:24};}
 remove(){this.isConnected=false;}all(){return this.children.flatMap(c=>[c,...c.all()]);}
}
const doc={hidden:false,createElement:t=>new El(t),createElementNS:(ns,t)=>new El(t,ns),addEventListener(){},removeEventListener(){}};
const ID='a'.repeat(32);
const history=(points,over={})=>({agent_id:ID,server_now:NOW,from:NOW-3600000,to:NOW,bucket_from:Math.floor((NOW-3600000)/M)*M,bucket_to:Math.floor(NOW/M)*M,interval_ms:M,source:'minute_metrics',aggregation:'arithmetic_mean',retention_days:30,coverage:'partial',point_limit:1441,
 oldest_timestamp:points[0]?.timestamp??null,last_timestamp:points.at(-1)?.timestamp??null,points:points.map(p=>({cpu:1,ram_percent:1,swap_percent:0,disk_percent:1,load1:0,load5:0,load15:0,rx_rate:0,tx_rate:0,...p})),...over});
async function mount(answer,agent={agent_id:ID,online:true}){
 const requests=[];
 const state={fetchJSON:async url=>{requests.push(url);if(answer instanceof Error)throw answer;return answer;}};
 const api=T.create({document:doc,state,fetcher:async()=>({}),interval:()=>0,clear(){},now:()=>NOW,unauthorized(){}});
 const node=api.mount(agent);
 api.setActive(ID,true);api.setVisible(ID,true);
 for(let i=0;i<20;i++)await new Promise(r=>setImmediate(r));
 return {api,node,requests,paths:()=>node.all().filter(n=>n.tagName==='path'),peak:()=>node.all().find(n=>n.className==='tc7-peak'),status:()=>node.all().find(n=>n.className==='tc7-status')};
}

test('rates print with a unit and keep their digits',()=>{
 assert.equal(T.bytes(369),'369 B/s');assert.equal(T.bytes(1234),'1.23 KB/s');assert.equal(T.bytes(58000),'58.0 KB/s');assert.equal(T.bytes(null),'—');
});

test('one scale for both lines; a gap of more than a minute breaks the line; the peak is the largest minute mean',()=>{
 const pts=[{timestamp:NOW-3*M,rx_rate:100,tx_rate:50},{timestamp:NOW-2*M,rx_rate:400,tx_rate:50},{timestamp:NOW-M/2*0+0-M*10,rx_rate:10,tx_rate:0}].sort((a,b)=>a.timestamp-b.timestamp);
 const r=T.paths(pts,NOW-3600000,NOW);
 assert.equal(r.peak,400);
 assert.equal((r.rx.match(/M/g)||[]).length,2,'the ten minute gap starts a new segment');assert.equal((r.rx.match(/L/g)||[]).length,1);
});

test('the card reads the last hour once, draws the lines and the peak "(minute mean)"',async()=>{
 const pts=Array.from({length:60},(_,i)=>({timestamp:Math.floor(NOW/M)*M-(59-i)*M,rx_rate:i===30?5800:100,tx_rate:50}));
 const c=await mount(history(pts));
 assert.equal(c.requests.length,1);assert.match(c.requests[0],/\/resources\/history\?hours=1$/);
 assert.equal(c.paths().length,2);
 assert.equal(c.peak().textContent,'峰值 5.80 KB/s');assert.match(c.peak().title,/分钟均值/);
 assert.equal(c.status().hidden,true);
});

test('no data, a failed read and a paused device say so and draw nothing',async()=>{
 const none=await mount(history([]));
 assert.equal(none.paths().length,0);assert.match(none.status().textContent,/没有分钟数据/);assert.equal(none.peak().textContent,'峰值 —');
 const bad=await mount(new Error('boom'));
 assert.equal(bad.paths().length,0);assert.match(bad.status().textContent,/流量读取失败/);
 const paused=await mount(history([{timestamp:NOW-M,rx_rate:1,tx_rate:1}]),{agent_id:ID,online:true,disabled_at:1});
 assert.equal(paused.requests.length,0,'a paused device is not read');assert.match(paused.status().textContent,/暂停/);
});

test('nothing is read while the other face is shown',async()=>{
 const c=await mount(history([{timestamp:NOW-M,rx_rate:1,tx_rate:1}]));
 const before=c.requests.length;
 c.api.setActive(ID,false);c.api.tick();for(let i=0;i<10;i++)await new Promise(r=>setImmediate(r));
 assert.equal(c.requests.length,before);
});
