const {test}=require('node:test'),assert=require('node:assert/strict');
const L=require('../web/static/latency-mini.js');
const M=60000,NOW=1792080030000,FIRST=Math.floor(NOW/M)*M-1439*M;
const LAST=1439; // in-progress minute (end = NOW)
function fixture(protocol='tcp',slot='telecom'){
 const target={id:`t-${protocol}-${slot}`,slot,family:'ipv4',protocol,slot_revision:'7',status:'active',lane_status:'active',condition:''};
 const points=Array.from({length:1440},(_,i)=>({start:FIRST+i*M,end:Math.min(FIRST+(i+1)*M,NOW),count:0,valid_rtts:0,not_executed:0,sent:0,received:0,p50_ms:null,p50_5m_ms:null,p95_ms:null,completeness:'no_data',execution_coverage:'unverified',categories:{}}));
 for(let i=1430;i<1436;i++)Object.assign(points[i],{count:2,valid_rtts:2,p50_ms:30+i-1430,p50_5m_ms:20+i-1430,p95_ms:40+i-1430,completeness:'complete',config_revision_min:'9',config_revision_max:'9',slot_revision_min:'7',slot_revision_max:'7',categories:{success:2}});
 const h={target,view:'1day',resolution_ms:M,now:NOW,window_start:FIRST,algorithm:'weighted-grid-rtt-adjacent-v3',histogram_version:1,grid_ms:.25,percentile_max_error_ms:.125,groups:[{semantics:'staged_probe_v2',metric:protocol==='tcp'?'pure_connect':'echo_rtt',points}]};return {target,h,points};
}
const set=(points,i,five,p95=five,own=five)=>Object.assign(points[i],{count:2,valid_rtts:2,p50_5m_ms:five,p50_ms:own,p95_ms:p95,completeness:'complete',config_revision_min:'9',config_revision_max:'9',slot_revision_min:'7',slot_revision_max:'7',categories:{success:2}});

test('fixed square-root ruler: ratio = sqrt(v/1000) up to 1000ms, then clamped',()=>{
 for(const [v,r]of [[-5,0],[0,0],[100,.316],[250,.5],[1000,1],[1500,1],[60000,1]])assert.ok(Math.abs(L.ratio(v)-r)<.001,`${v}`);
 for(const gone of ['axis','tcpAxis','tcpLevel'])assert.equal(L[gone],undefined);
});
test('normalize plots the 5-minute P50 and keeps the minute P50 and P95 for the tooltip',()=>{
 const {h,target}=fixture();const p=L.normalize(h,target).points[1430];assert.equal(p.value,20);assert.equal(p.p50,30);assert.equal(p.p95,40);
});
test('p50_5m_ms and p50_ms are validated like p95_ms',()=>{
 for(const key of ['p50_5m_ms','p50_ms']){const {h,target,points}=fixture();for(const bad of [-1,Infinity,'3'])assert.throws(()=>{points[1430][key]=bad;L.normalize(h,target);});delete points[1430][key];assert.throws(()=>L.normalize(h,target));points[1430][key]=null;L.normalize(h,target);}
 {const {h,target,points}=fixture();points[1430].p50_5m_ms=null;const p=L.normalize(h,target).points[1430];assert.equal(p.value,null);assert.match(p.reason,/5 分钟窗口内有效 RTT 不足/);}
});
test('TCP known_gap retains only valid identity-safe P50, with original categories',()=>{
 const {h,target,points}=fixture();Object.assign(points[1432],{execution_coverage:'known_gap',not_executed:1,completeness:'partial',categories:{success:1,timeout:1}});let p=L.normalize(h,target).points[1432];assert.equal(p.value,22);assert.equal(p.partial,true);assert.equal(p.valid,2);assert.equal(p.count,2);assert.equal(p.notExecuted,1);assert.deepEqual(p.categories,{success:1,timeout:1});
 points[1432].config_revision_min='8';assert.equal(L.normalize(h,target).points[1432].value,null);points[1432].config_revision_min='9';points[1432].slot_revision_max='8';assert.equal(L.normalize(h,target).points[1432].value,null);points[1432].slot_revision_max='7';points[1432].valid_rtts=0;assert.equal(L.normalize(h,target).points[1432].value,null);
});
test('success plus failure, null, unexecuted, partial zero and sub-ms retain truthful P50',()=>{
 const {h,target,points}=fixture();Object.assign(points[1430],{valid_rtts:1,p50_5m_ms:.4,p95_ms:.4,categories:{success:1,timeout:1}});Object.assign(points[1431],{execution_coverage:'known_gap',p50_5m_ms:0,p95_ms:0});Object.assign(points[1432],{p50_5m_ms:null,p95_ms:null,valid_rtts:0,categories:{timeout:2}});Object.assign(points[1433],{p50_5m_ms:null,p95_ms:null,valid_rtts:0,not_executed:2,execution_coverage:'known_gap'});const n=L.normalize(h,target);assert.equal(n.points[1430].value,.4);assert.equal(n.points[1431].value,0);assert.equal(n.points[1431].partial,true);assert.equal(n.points[1432].value,null);assert.equal(n.points[1433].value,null);
});
test('old semantics and mismatched API remain rejected',()=>{
 const {h,target}=fixture();h.groups[0].semantics='single_probe_v1';assert.equal(L.normalize(h,target).supported,false);h.target={...target,family:'ipv6'};assert.throws(()=>L.normalize(h,target));h.target=target;h.algorithm='unknown';assert.throws(()=>L.normalize(h,target));
});
test('ICMP preserves the known_gap mask and has no partial field',()=>{
 const {h,target,points}=fixture('icmp');points[1431].execution_coverage='known_gap';const n=L.normalize(h,target);assert.equal(n.points[1431].value,null);assert.equal(Object.hasOwn(n.points[1430],'partial'),false);assert.equal(n.points[1430].value,20);
});
test('recent(): the 5-minute P50 of the latest completed minute; null when that minute has none, is partial, or is still open',()=>{
 const run=(values,extra={})=>{const {h,target,points}=fixture();for(const p of points)Object.assign(p,{p50_ms:null,p50_5m_ms:null,p95_ms:null,valid_rtts:0,count:0,completeness:'no_data'});for(const [i,v]of values)set(points,i,v);extra.fn?.(points);return L.recent(L.normalize(h,target));};
 assert.equal(run([[1434,100],[1435,120],[1436,140],[1437,160],[1438,180],[LAST,9999]]),180,'latest completed minute, not the open one');
 assert.equal(run([[1438,172]],{fn:p=>Object.assign(p[1438],{p50_ms:1575})}),172,'not the minute P50');
 assert.equal(run([[1436,140],[1437,160]]),null,'latest completed minute (1438) has no value: name only, no look-back');
 assert.equal(run([[1437,160],[1438,180]],{fn:p=>Object.assign(p[1438],{execution_coverage:'known_gap',completeness:'partial'})}),null,'partial minute');
 assert.equal(run([[1438,180]],{fn:p=>Object.assign(p[1438],{p50_5m_ms:null})}),null);
 assert.equal(run([[LAST,50]]),null);
});

// ---------- rendering through the real create()/render() with a minimal DOM ----------
class El{
 constructor(tag,ns){this.tagName=tag;this.namespace=ns;this.children=[];this.attributes={};this.dataset={};this.style={setProperty(k,v){this[k]=v;}};this.listeners={};this.className='';this.ownText='';this.isConnected=true;this.hidden=false;}
 set textContent(v){this.ownText=String(v);this.children=[];}get textContent(){return this.ownText+this.children.map(c=>c.textContent).join('');}
 append(...c){this.children.push(...c);}replaceChildren(...c){this.ownText='';this.children=c;}
 setAttribute(k,v){this.attributes[k]=String(v);}getAttribute(k){return this.attributes[k];}
 addEventListener(t,f){(this.listeners[t]||(this.listeners[t]=[])).push(f);}removeEventListener(){}
 dispatch(t,e={}){for(const f of this.listeners[t]||[])f({preventDefault(){},stopPropagation(){},...e});}
 getBoundingClientRect(){return this.rect||{width:0,height:0,left:0,top:0,right:0,bottom:0};}
 remove(){this.isConnected=false;}getContext(){return {measureText:t=>({width:t.length*5.5})};}
 hasPointerCapture(){return false;}setPointerCapture(){}releasePointerCapture(){}
 all(){return this.children.flatMap(c=>[c,...c.all()]);}
}
const fakeDoc={hidden:false,createElement:t=>new El(t),createElementNS:(ns,t)=>new El(t,ns),addEventListener(){},removeEventListener(){}};
const IDS={telecom:1,unicom:2,mobile:3};
const gate={marks:null},clock={ms:0};
async function card({height=80,protocol='tcp',series,marks=[],id='a'}){
 gate.marks=null;clock.ms=0;
 // series: {slot: points => void}; slots absent from `series` get empty history.
 const histories={};
 for(const proto of ['tcp','icmp'])for(const slot of Object.keys(IDS)){const f=fixture(proto,slot);for(const p of f.points)Object.assign(p,{count:0,valid_rtts:0,p50_ms:null,p95_ms:null,completeness:'no_data'});if(proto===protocol)series[slot]?.(f.points);histories[f.target.id]=f.h;}
 const targets=['tcp','icmp'].flatMap(proto=>Object.keys(IDS).map(slot=>fixture(proto,slot).target));
 const lanes=new Map(targets.map(t=>[`${t.slot}/ipv4/${t.protocol}`,{slot:t.slot,family:'ipv4',protocol:t.protocol,status:'active',condition:''}]));
 const config={revision:'1',settings_revision:'1',supported:true,enabled:true,targets,lanes,slots:Object.keys(IDS).map(slot=>({slot,kind:'builtin',region:'',isp:slot,host:'example.com',port:443}))};
 const requests=[],state={};
 state.fetchJSON=async url=>{
  requests.push(url);
  if(url.includes('/v5/marks')){const answer={family:'ipv4',eval_end:NOW-30000,from:NOW-86400000,to:NOW-30000,lanes:Object.keys(IDS).map(slot=>({slot,runs:marks.filter(m=>m.slot===slot).map(({start,end,state})=>({start,end,state}))}))};
   if(gate.marks)await gate.marks; // the answer is made when the request is made, and arrives later
   return answer;}
  const tid=decodeURIComponent(/target_id=([^&]+)/.exec(url)[1]);return histories[tid];};
 const api=L.create({document:fakeDoc,state,interval:()=>0,clear(){},now:()=>NOW+clock.ms,monotonicNow:()=>0,unauthorized(){}});
 const node=api.mount({agent_id:id,online:true});
 const plot=node.all().find(n=>n.className==='lm-plot');plot.rect={width:300,height,left:0,top:0,right:300,bottom:80};node.rect={width:300,height:200,left:0,top:0,right:300,bottom:200};globalThis.innerHeight=1000;globalThis.innerWidth=1000;
 const flush=async()=>{for(let i=0;i<30;i++){await new Promise(r=>setImmediate(r));api.tick();}};
 api.bindConfig(id,config);api.setVisible(id,true);await flush();
 if(protocol==='icmp'){node.all().find(n=>n.tagName==='button'&&n.textContent==='ICMP').dispatch('click');await flush();}
 const svg=()=>plot.children[0].all();
 const nums=()=>Object.fromEntries(node.all().filter(n=>n.className.startsWith('lm-num')).map((n,i)=>[Object.keys(IDS)[i],n]));
 const strip=()=>node.all().find(n=>n.className==='lm7-marks').children[0]?.all().filter(n=>n.tagName==='rect'&&n.attributes['data-mark'])||[];
 const tags=()=>node.all().find(n=>n.className==='lm7-marks').children[0]?.all().filter(n=>n.attributes['data-tag'])||[];
 return {api,node,plot,svg,nums,strip,tags,config,marks,gate,clock,requests,flush,legend:node.all().filter(n=>n.tagName==='button'&&/^(电信|联通|移动)/.test(n.textContent))};
}
const solid=c=>c.svg().filter(n=>n.tagName==='path'&&n.attributes['stroke-width']==='1.3');
const dashed=c=>c.svg().filter(n=>n.tagName==='path'&&n.attributes['stroke-dasharray']);
const firstY=d=>Number(/^M[\d.]+,([\d.]+)/.exec(d)[1]);
const yOf=(v,ph=78)=>ph-Math.min(1,L.ratio(v))*(ph-4)-1; // plot height 80 => drawing height 78
const line=(points,from,to,fn)=>{for(let i=from;i<=to;i++)set(points,i,fn(i-from));};
const caption=c=>c.node.all().find(n=>n.className==='lm7-caption').textContent;

test('curve is drawn from the 5-minute P50, not the minute P50 or P95',async()=>{
 const c2=await card({series:{telecom:p=>{line(p,1420,1428,()=>100);for(let i=1420;i<=1428;i++){p[i].p95_ms=3000;p[i].p50_ms=2500;}}}});
 const d=solid(c2)[0].attributes.d;assert.ok(Math.abs(firstY(d)-yOf(100))<.01,d);
 assert.equal(dashed(c2).length,0,'a P95 of 3000ms must not appear on the curve');
 assert.equal(c2.plot.dataset.scale,'fixed-sqrt');
 assert.match(c2.plot.attributes['aria-label'],/5 分钟滑动 P50（每分钟一个点）；固定平方根刻度 0–1000ms，超过 1000ms 用顶边虚线表示/);
});
test('the same value has the same y on cards with completely different data ranges',async()=>{
 const a=await card({series:{telecom:p=>line(p,1420,1428,i=>i%2?100:120)}});
 const b=await card({id:'b',series:{telecom:p=>line(p,1420,1428,i=>i%2?100:290),unicom:p=>line(p,1420,1428,()=>5)}});
 const ya=firstY(solid(a)[0].attributes.d),yb=solid(b).map(n=>firstY(n.attributes.d));
 assert.ok(Math.abs(ya-yOf(120))<.01);
 assert.ok(Math.abs(yb[0]-yOf(290))<.01);
 const grid=card2=>card2.svg().filter(n=>n.attributes['data-grid']).map(n=>n.attributes['data-grid']+':'+n.attributes.d);
 assert.deepEqual(grid(a),grid(b));assert.deepEqual(grid(a).map(x=>x.split(':')[0]),['100','300','1000']);
 for(const g of a.svg().filter(n=>n.attributes['data-grid'])){assert.equal(g.attributes.stroke,'#e6ddd1');assert.equal(g.attributes['stroke-width'],'0.6');}
 const ticks=card2=>card2.svg().filter(n=>n.dataset.axisTick).map(n=>n.dataset.axisTick+n.ownText);
 assert.deepEqual(ticks(a),ticks(b));assert.deepEqual([...ticks(a)].sort(),['10001000','100100','300300']);
 for(const t of a.svg().filter(n=>n.dataset.axisTick))assert.equal(t.attributes['font-size'],'9');
});
test('a run above 1000ms is one dashed top-edge segment joined to the solid line',async()=>{
 const c=await card({series:{telecom:p=>{line(p,1420,1423,()=>100);line(p,1424,1428,()=>1500);p[1424].p95_ms=3010;line(p,1429,1432,()=>100);}}});
 const d=dashed(c);assert.equal(d.length,1);assert.equal(d[0].attributes['data-minutes'],'5');assert.equal(d[0].attributes['stroke-dasharray'],'3,2');
 assert.match(d[0].attributes.d,/^M[\d.]+,3H[\d.]+$/);
 const path=solid(c)[0].attributes.d;assert.match(path,/L[\d.]+,3/,'solid line reaches the top edge');
 assert.equal(c.svg().filter(n=>n.attributes['data-boundary']==='above-scale').length,1);
});
test('a single minute above 1000ms is a short dashed mark with the real values on hover',async()=>{
 const c=await card({series:{telecom:p=>{set(p,1435,1830,3010,1575);}}});
 const d=dashed(c);assert.equal(d.length,1);assert.equal(d[0].attributes['data-minutes'],'1');
 const m=/^M([\d.]+),3H([\d.]+)$/.exec(d[0].attributes.d);assert.ok(Number(m[2])-Number(m[1])>=3);
 const left=Number(c.plot.dataset.axisLeft),start=Number(c.plot.dataset.start),end=Number(c.plot.dataset.end),epoch=FIRST+1435*M+1000;
 c.plot.dispatch('pointermove',{pointerType:'mouse',pointerId:1,clientX:left+(epoch-start)/(end-start)*(300-left)});
 const note=c.node.all().find(n=>n.tagName==='output').textContent;
 assert.match(note,/电信：5 分钟 P50 1830ms · 本分钟 P50 1575ms · P95 3010ms · 超出刻度/);
});
test('top numbers: latest completed minute 5-minute P50, lowest bold, >1000 red, name only when missing',async()=>{
 const c=await card({series:{
  telecom:p=>{set(p,1437,90);set(p,1438,140,3000,1575);set(p,LAST,9999);},
  unicom:p=>{set(p,1436,200);set(p,1437,200);},
  mobile:p=>{set(p,1438,1700);}}});
 const n=c.nums();
 assert.equal(n.telecom.textContent,'140');assert.equal(n.unicom.textContent,'');assert.equal(n.mobile.textContent,'1700');
 assert.equal(n.telecom.dataset.state,'lowest');assert.match(n.telecom.className,/lm-lowest/);
 assert.equal(n.mobile.dataset.state,'over');assert.match(n.mobile.className,/lm-over/);assert.doesNotMatch(n.mobile.className,/lm-lowest/);
 assert.equal(n.unicom.dataset.state,'none');
 for(const b of c.legend)assert.doesNotMatch(b.textContent,/—/);
 assert.ok(!c.node.all().some(x=>x.className==='lm-unit'||x.textContent==='ms'),'no trailing unit');
 // Moving the range away from "now" does not change the headline numbers.
 const thumb=c.node.all().find(x=>x.dataset.thumb==='end');thumb.dispatch('keydown',{key:'ArrowLeft',shiftKey:true});
 assert.notEqual(c.plot.dataset.end,String(NOW));
 assert.equal(c.nums().telecom.textContent,'140');
});
test('a lone value is not marked lowest; deselecting a line clears its number',async()=>{
 const c=await card({series:{telecom:p=>set(p,1438,140)}});
 let n=c.nums();assert.equal(n.telecom.textContent,'140');assert.doesNotMatch(n.telecom.className,/lm-lowest/);
 const button=c.node.all().find(x=>x.tagName==='button'&&x.textContent.startsWith('联通'));button.dispatch('click');await c.flush();
 assert.equal(c.nums().unicom.textContent,'');
});

test('ICMP uses the same fixed scale, ruler and top numbers, and draws no slow / failed marks',async()=>{
 const marks=[{slot:'telecom',start:NOW-3600000,end:NOW-3000000,state:'red'}];
 const t=await card({series:{telecom:p=>line(p,1420,1428,()=>150)},marks});
 const i=await card({id:'i',protocol:'icmp',series:{telecom:p=>{line(p,1420,1428,()=>150);line(p,1430,1432,()=>2000);}},marks});
 assert.equal(i.plot.dataset.scale,'fixed-sqrt');
 assert.ok(Math.abs(firstY(solid(i)[0].attributes.d)-firstY(solid(t)[0].attributes.d))<.01);
 assert.equal(dashed(i).length,1);
 assert.equal(t.strip().length>0,true);assert.equal(i.strip().length,0,'the ICMP view draws curves only');
});

test('each lane has its own thin mark strip in the state colours of face A, with a tag in the lane curve colour',async()=>{
 const marks=[{slot:'telecom',start:NOW-3600000,end:NOW-3000000,state:'red'},{slot:'mobile',start:NOW-1800000,end:NOW-1200000,state:'yellow'},{slot:'mobile',start:NOW-900000,end:NOW-720000,state:'red'}];
 const c=await card({series:{telecom:p=>line(p,1420,1428,()=>100)},marks});
 const rects=c.strip();
 assert.equal(rects.length,3);
 const by=slot=>rects.filter(r=>r.attributes['data-slot']===slot);
 assert.equal(by('telecom').length,1);assert.equal(by('unicom').length,0);assert.equal(by('mobile').length,2);
 assert.deepEqual([by('telecom')[0].attributes.y,by('mobile')[0].attributes.y],['0','6'],'one row per lane, always the same row');
 assert.equal(by('telecom')[0].attributes.fill,'#B8433D','red is the red of the blocks');
 assert.equal(by('mobile').find(r=>r.attributes['data-mark']==='yellow').attributes.fill,'#E3B54F');assert.equal(by('mobile').find(r=>r.attributes['data-mark']==='red').attributes.fill,'#B8433D');
 assert.deepEqual(c.tags().map(t=>[t.attributes['data-tag'],t.attributes.fill,t.attributes.y]),[['telecom','#3D6FA8','0'],['unicom','#8A5BB0','3'],['mobile','#4F5B62','6']],'one tag per running lane, in its curve colour');
 assert.ok(rects.every(r=>r.attributes.height==='2'));
 assert.ok(c.requests.some(u=>/v5\/marks\?family=ipv4&from=\d+&to=\d+/.test(u)));
});

test('the range slider drives the plotted window; the end handle at "now" means following',async()=>{
 const c=await card({series:{telecom:p=>line(p,1420,1428,()=>100)}});
 assert.match(caption(c),/跟随现在/);
 assert.equal(Number(c.plot.dataset.end)-Number(c.plot.dataset.start),3600000,'the last hour by default');
 const end=c.node.all().find(x=>x.dataset.thumb==='end');
 end.dispatch('keydown',{key:'ArrowLeft',shiftKey:true});
 assert.match(caption(c),/历史位置/);
 end.dispatch('keydown',{key:'End'});
 assert.match(caption(c),/跟随现在/);
});

test('B1: a save that only changes the slow threshold invalidates the marks at once; an answer under the old threshold is not adopted',async()=>{
 const marks=[{slot:'telecom',start:NOW-3600000,end:NOW-3000000,state:'red'}];
 const c=await card({series:{telecom:p=>line(p,1420,1428,()=>100)},marks});
 const reads=()=>c.requests.filter(u=>u.includes('/v5/marks')).length;
 assert.equal(c.strip().length,1);const before=reads();
 // the Server now answers with no mark (threshold 3000 ms): same targets, same measurement revision, new settings revision
 marks.length=0;
 c.api.bindConfig('a',{...c.config,settings_revision:'2'});
 assert.equal(c.strip().length,0,'the old mark is gone at once');
 await c.flush();
 assert.ok(reads()>before,'the marks were read again');assert.equal(c.strip().length,0);
 // the same settings revision again changes nothing and reads nothing
 const again=reads();c.api.bindConfig('a',{...c.config,settings_revision:'2'});await c.flush();assert.equal(reads(),again);
});

test('R1: an old marks answer that is still on its way when the threshold changes is not adopted, and the new threshold is really read',async()=>{
 const marks=[{slot:'telecom',start:NOW-3600000,end:NOW-3000000,state:'red'}];
 const c=await card({series:{telecom:p=>line(p,1420,1428,()=>100)},marks});
 const reads=()=>c.requests.filter(u=>u.includes('/v5/marks')).length;
 assert.equal(c.strip().length,1);
 // a periodic refresh starts; its answer (the old red mark, made now) is held back
 let release;gate.marks=new Promise(r=>{release=r;});clock.ms+=61000;c.api.tick();await new Promise(r=>setImmediate(r));
 const held=reads();assert.equal(held,2,'the periodic read is in flight');
 // the threshold is raised and saved: the Server now answers without the mark
 marks.length=0;gate.marks=null;
 c.api.bindConfig('a',{...c.config,settings_revision:'2'});
 assert.equal(c.strip().length,0);
 await new Promise(r=>setImmediate(r));await new Promise(r=>setImmediate(r));
 assert.equal(reads(),held+1,'a new read for the new threshold was really made');
 // now the old answer arrives
 release();for(let i=0;i<10;i++)await new Promise(r=>setImmediate(r));
 assert.equal(c.strip().length,0,'the old red mark does not come back');
 // and it did not refresh the cache stamp: the next refresh is due 60 s after the NEW read, not after the old one
 const before=reads();clock.ms+=30000;c.api.tick();await new Promise(r=>setImmediate(r));assert.equal(reads(),before,'not due yet');
 clock.ms+=31000;c.api.tick();await new Promise(r=>setImmediate(r));assert.equal(reads(),before+1);
});
