'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),Overview=require('../web/static/overview.js'),Metrics=require('../web/static/card-metrics.js');
const now=100000,deadline=plan=>({tone:plan.tone||'',label:plan.label||'正常日期'}),map=rows=>new Map(rows.map((row,i)=>[String(i),row]));
const agent=(patch={})=>({online:true,state:{collected_at:now,rx_rate:0,tx_rate:1000},...patch});
const plan=(patch={})=>({traffic_mode:'rx',usage_bytes:0,cycle_start:now-1,cycle_end:now+1,renewal_date:'2026-10-03',...patch});
const aggregate=rows=>Overview.aggregate(map(rows),{now,deadline});
test('P5c online is non-paused/non-revoked registration, independent of valid samples',()=>{
 const s=aggregate([agent(),agent({state:null}),agent({online:false}),agent({disabled_at:1}),agent({revoked:true})]);
 assert.deepEqual([s.registered,s.online,s.paused,s.rx.count,s.rx.value],[4,2,1,1,0]);
 assert.equal(aggregate([]).registered,0);
});
test('P5c rate freshness uses exact original 30s / future5s boundary, no coercion of null or missing',()=>{
 const rows=[0,30000,30001,-5000,-5001].map(age=>agent({state:{collected_at:now-age,rx_rate:10,tx_rate:null}}));
 rows.push(agent({state:{collected_at:now,stale:true,rx_rate:100}}),agent({state:{collected_at:NaN,rx_rate:100}}),agent({state:{collected_at:now,rx_rate:-1,tx_rate:0}}),agent({state:{collected_at:now,rx_rate:'10',tx_rate:Infinity}}));
 const s=aggregate(rows);assert.deepEqual(s.rx,{value:30,count:3,overflow:false});assert.deepEqual(s.tx,{value:0,count:1,overflow:false});assert.equal(s.online,9);
});
test('P5c offline paused revoked and missing states never contribute stale rates',()=>{
 const s=aggregate([agent({online:false}),agent({disabled_at:1}),agent({revoked:true}),agent({state:null})]);assert.equal(s.rx.count,0);assert.equal(s.tx.count,0);
});
test('P5c current heterogeneous direction cycles include observed/calibrated legal zero, not machine totals',()=>{
 const s=aggregate(['rx','tx','sum'].map((mode,i)=>agent({plan:plan({traffic_mode:mode,usage_bytes:i*1000,usage_status:i===0?'calibrated':'partial',cycle_start:now-100-i,cycle_end:now+100+i}),traffic:{rx_total:1e9}})));
 assert.deepEqual(s.usage,{value:3000,count:3,overflow:false});assert.equal(s.partial,2);assert.equal(s.calibrated,1);
});
test('P5c invalid or old/not-started cycles are excluded; exact half-open bounds and zero start allowed',()=>{
 const plans=[null,plan({traffic_mode:''}),plan({cycle_start:null}),plan({cycle_end:null}),plan({cycle_start:now+1}),plan({cycle_end:now}),plan({usage_bytes:null}),plan({usage_bytes:-1}),plan({usage_bytes:NaN}),plan({usage_bytes:Infinity}),plan({usage_bytes:'0'}),plan({cycle_start:0,cycle_end:now+1})];
 const s=aggregate(plans.map(p=>agent({plan:p})));assert.equal(s.usage.count,1);assert.equal(s.usage.value,0);assert.equal(s.usageExcluded,11);
});
test('P5c sums flag overflow instead of rendering Infinity',()=>{
 const s=aggregate([agent({state:{collected_at:now,rx_rate:Number.MAX_VALUE},plan:plan({usage_bytes:Number.MAX_VALUE})}),agent({state:{collected_at:now,rx_rate:Number.MAX_VALUE},plan:plan({usage_bytes:Number.MAX_VALUE})})]);assert.equal(s.rx.overflow,true);assert.equal(s.usage.overflow,true);
});
test('P5c selected primary card date / original tone only, once price does not exclude expiry',()=>{
 const seen=[];const s=Overview.aggregate(map([agent({plan:plan({tone:'danger',expiry_date:'2030-01-01'})}),agent({plan:plan({renewal_date:'',expiry_date:'2026-10-05',tone:'warn',renewal_price_period:'once'})}),agent({plan:plan()}),agent({plan:plan({renewal_date:'invalid',tone:'danger'})}),agent({plan:plan({label:'NaN 天后续费',tone:'warn'})}),agent({plan:null})]),{now,deadline:p=>{seen.push(p.renewal_date||p.expiry_date);return deadline(p);}});
 assert.deepEqual([s.due,s.soon,s.unset],[1,1,3]);assert.deepEqual(seen,['2026-10-03','2026-10-05','2026-10-03','2026-10-03']);
});
function ui(saved=null,blocked=false){const nodes=new Map(),writes=[];const doc={querySelector:id=>{if(!nodes.has(id))nodes.set(id,{open:true,textContent:'',listeners:{},addEventListener(t,f){this.listeners[t]=f;}});return nodes.get(id);}};
 const api=Overview.create({doc,size:Metrics.size,deadline,storage:()=>{if(blocked)throw Error('storage denied');return {getItem:()=>saved,setItem:(key,value)=>{writes.push([key,value]);saved=value;}};}});
 return {api,nodes,writes,toggle(open){const n=nodes.get('#overview');n.open=open;n.listeners.toggle();},value:()=>saved};}
test('P5c pending is not read zero; ready zero shows 0/0 and unknown samples',()=>{
 const u=ui();u.api.update(map([]),false,now);assert.equal(u.nodes.get('#overview-online').textContent,'等待读取');u.api.update(map([]),true,now);assert.equal(u.nodes.get('#overview-online').textContent,'0 / 0');assert.equal(u.nodes.get('#overview-rates').textContent,'↓ — · ↑ —');assert.equal(u.nodes.get('#overview-usage').textContent,'—');
});
test('P5c optional preference default and damaged/denied storage fallback expanded; no startup write',()=>{
 for(const saved of [null,'bad','false','open']){const u=ui(saved);assert.equal(u.nodes.get('#overview').open,true);assert.equal(u.writes.length,0);u.toggle(true);assert.equal(u.writes.length,0);}
 const denied=ui('closed',true);assert.equal(denied.nodes.get('#overview').open,true);assert.doesNotThrow(()=>denied.toggle(false));
 const closed=ui('closed');assert.equal(closed.nodes.get('#overview').open,false);closed.toggle(false);assert.equal(closed.writes.length,0);
});
test('P5c native toggle saves only versioned fixed preference and survives next initialization',()=>{
 const u=ui();u.toggle(false);assert.deepEqual(u.writes,[[Overview.KEY,'closed']]);assert.equal(ui(u.value()).nodes.get('#overview').open,false);u.toggle(true);assert.equal(u.value(),'open');assert.equal(u.nodes.get('#overview-action').textContent,'收起 ▴');
});
test('P5c update preserves shell/summary/open/focus sentinel through twenty snapshots; zero stays known, overflow explicit',()=>{
 const u=ui(),shell=u.nodes.get('#overview');shell.focusSentinel='summary';u.toggle(false);for(let i=0;i<20;i++)u.api.update(map([agent({plan:plan()})]),true,now);
 assert.equal(u.nodes.get('#overview'),shell);assert.equal(shell.open,false);assert.equal(shell.focusSentinel,'summary');assert.equal(u.writes.length,1);assert.match(u.nodes.get('#overview-rates').textContent,/0 B\/s.*1.00 KB\/s/);assert.equal(u.nodes.get('#overview-usage').textContent,'0 B');assert.match(u.nodes.get('#overview-usage-note').textContent,/非统一月度总量/);
 u.api.update(map([agent({state:{collected_at:now,rx_rate:Number.MAX_VALUE}}),agent({state:{collected_at:now,rx_rate:Number.MAX_VALUE}})]),true,now);assert.match(u.nodes.get('#overview-rates').textContent,/无法合计/);assert.doesNotMatch(u.nodes.get('#overview-rates').textContent,/Infinity/);
});
