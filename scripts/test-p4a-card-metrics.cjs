const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const C=require('../web/static/card-metrics.js');
const at=1790900000000;
const dto=(seconds=0,extra={})=>({agent_id:'a'.repeat(32),online:true,revoked:false,disabled_at:null,state:{collected_at:at+seconds*1000,uptime:100+seconds,cpu_percent:90,disk_busy_percent:87,stale:false,...extra}});
test('actual Web timestamp/uptime observations require 30 seconds, duplicates cannot age a window',()=>{
 const observer=C.createObserver();for(const t of [0,10,20]){const a=dto(t);assert.equal(observer.observe(a,a.state.collected_at).cpu,'warning');for(let i=0;i<10;i++)assert.equal(observer.observe(a,a.state.collected_at+1000).cpu,'warning');}
 const last=dto(30),result=observer.observe(last,last.state.collected_at);assert.equal(result.cpu,'critical');assert.equal(result.io,'critical');assert.equal(observer.size(),1);
});
test('gaps/backwards sample/uptime jumps/regression and low observations restart sustained windows',()=>{
 for(const bad of [dto(41),dto(19),dto(21,{uptime:1}),dto(21,{uptime:1000}),dto(21,{cpu_percent:70,disk_busy_percent:40})]) {
  const observer=C.createObserver();for(const t of [0,10,20])observer.observe(dto(t),at+t*1000);
  const b=observer.observe(bad,bad.state.collected_at);assert.notEqual(b.cpu,'critical');assert.notEqual(b.io,'critical');
  const next=dto((bad.state.collected_at-at)/1000+5);assert.equal(observer.observe(next,next.state.collected_at).cpu,'warning');
 }
});
test('missing/null/nonfinite values never become healthy zero; actual zero remains healthy',()=>{
 for(const value of [null,undefined,NaN,Infinity,-1,'0']){assert.equal(C.percent(value),null);assert.equal(C.pct(value),'—');assert.equal(C.tone(value),'unknown');}
 assert.equal(C.percent(101),100);assert.equal(C.tone(0),'healthy');
 const observer=C.createObserver();assert.equal(observer.observe(dto(0,{cpu_percent:null,disk_busy_percent:undefined}),at).cpu,'unknown');
 assert.equal(observer.observe(dto(1,{cpu_percent:0,disk_busy_percent:0}),at+1000).io,'healthy');
 assert.equal(C.tone(70),'warning');assert.equal(C.tone(90),'critical');
});
test('offline/paused/revoked/stale/old/future/missing timestamp/uptime reset and deletion prunes',()=>{
 for(const mutate of [a=>a.online=false,a=>a.disabled_at=1,a=>a.revoked=true,a=>a.state.stale=true,a=>a.state.collected_at=null,a=>a.state.collected_at=at+6000,a=>a.state.uptime=null]) {
  const observer=C.createObserver();for(const t of [0,10,20,30])observer.observe(dto(t),at+t*1000);
  const a=dto();mutate(a);assert.notEqual(observer.observe(a,at).cpu,'critical');
  assert.equal(observer.observe(dto(1),at+1000).cpu,'warning');
 }
 const observer=C.createObserver();assert.equal(observer.observe(dto(),at+30001).stale,true);observer.observe(dto(),at);observer.sync([]);assert.equal(observer.size(),0);
 observer.observe(dto(),at);observer.sync(['a'.repeat(32)]);assert.equal(observer.size(),1);assert.equal(C.createObserver().observe(dto(30),at+30000).cpu,'warning');
});
test('binary machine capacity versus decimal network bytes/rate and honest OS identity/version',()=>{
 assert.equal(C.size(1000000,true),'1.00 MB/s');assert.equal(C.size(1073741824,false,true),'1.00 GiB');assert.equal(C.size(1000),'1.00 KB');assert.equal(C.size(0),'0 B');assert.equal(C.size(null),'—');
 assert.deepEqual(C.os('debian 12.7'),{text:'Debian 12.7',icon:'debian'});assert.deepEqual(C.os('Alpine 3.20.2'),{text:'Alpine Linux 3.20.2',icon:'alpine'});
 assert.deepEqual(C.os('alpine linux 3.21.0'),{text:'Alpine Linux 3.21.0',icon:'alpine'});assert.deepEqual(C.os('unknown-fork 1.2'),{text:'unknown-fork 1.2',icon:'generic'});
 assert.equal(C.os('').text,'等待首次上报');assert.equal(C.os('alpinejs 3').icon,'generic');
});
test('price uses actual currency and value with symbol, never exchange conversion',()=>{
 assert.equal(C.price({currency:'USD',purchase_price:'9.99',purchase_price_period:'monthly'}),'USD $9.99/月');
 assert.equal(C.price({currency:'EUR',renewal_price:'10',renewal_price_period:'yearly'}),'EUR €10.00/年');assert.equal(C.price(null),'未设价格');assert.equal(C.price({currency:'USD',purchase_price:'0'}),'USD $0.00');
});
class Element {constructor(){this.children=[];this.attributes={};}setAttribute(k,v){this.attributes[k]=v;}append(...children){this.children.push(...children);}}
test('production tile DOM uses finite neutral ring/accessible old label and seven actual stamp components',()=>{
 const sandbox={document:{createElement:()=>new Element()},Intl,Date};vm.createContext(sandbox);vm.runInContext(fs.readFileSync(require('node:path').join(__dirname,'../web/static/card-metrics.js'),'utf8'),sandbox);
 const cards=sandbox.CardMetrics;
 const old=cards.tile('cpu','CPU',90,'2 核','unknown','steal —',true);assert.match(old.children[0].innerHTML,/stroke-dasharray="0 100"/);assert.match(old.attributes['aria-label'],/90.0%.*旧数据/);
 assert.match(cards.tile('cpu','CPU',Infinity,'','unknown').children[0].innerHTML,/stroke-dasharray="0 100"/);
 const tiers=['SSS','SS','S','A','B','C','D'];for(const tier of tiers){const stamp=cards.stamp(tier);assert.match(stamp.attributes['aria-label'],/非真实评分/);assert.match(stamp.innerHTML,new RegExp(`<strong>${tier}</strong>`));}
 assert.equal(cards.stamp(null),null);assert.equal(cards.stamp('OFFLINE'),null);assert.equal(cards.stamp('<img>'),null);
});
