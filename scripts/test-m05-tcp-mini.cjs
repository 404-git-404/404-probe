const {test}=require('node:test'),assert=require('node:assert/strict');
const L=require('../web/static/latency-mini.js');
const M=60000,NOW=1792080030000,FIRST=Math.floor(NOW/M)*M-1439*M;
const LAST=1439; // in-progress minute (end = NOW)
function fixture(protocol='tcp',slot='telecom'){
 const target={id:`t-${protocol}-${slot}`,slot,family:'ipv4',protocol,region:'test',slot_revision:'7',status:'active'};
 const points=Array.from({length:1440},(_,i)=>({start:FIRST+i*M,end:Math.min(FIRST+(i+1)*M,NOW),count:0,valid_rtts:0,not_executed:0,sent:0,received:0,p50_ms:null,p95_ms:null,completeness:'no_data',execution_coverage:'unverified',categories:{}}));
 for(let i=1430;i<1436;i++)Object.assign(points[i],{count:2,valid_rtts:2,p50_ms:30+i-1430,p95_ms:40+i-1430,completeness:'complete',config_revision_min:'9',config_revision_max:'9',slot_revision_min:'7',slot_revision_max:'7',categories:{success:2}});
 const h={target,view:'1day',resolution_ms:M,now:NOW,window_start:FIRST,algorithm:'weighted-grid-rtt-adjacent-v3',histogram_version:1,grid_ms:.25,percentile_max_error_ms:.125,groups:[{semantics:'staged_probe_v2',metric:protocol==='tcp'?'pure_connect':'echo_rtt',points}]};return {target,h,points};
}
const set=(points,i,p50,p95=p50)=>Object.assign(points[i],{count:2,valid_rtts:2,p50_ms:p50,p95_ms:p95,completeness:'complete',config_revision_min:'9',config_revision_max:'9',slot_revision_min:'7',slot_revision_max:'7',categories:{success:2}});

test('fixed piecewise ruler: 0-300ms is the lower 80%, 300-1000ms the top 20%',()=>{
 for(const [v,r]of [[0,0],[150,.4],[300,.8],[650,.9],[1000,1],[1001,1],[60000,1]])assert.ok(Math.abs(L.ratio(v)-r)<1e-12,`${v}`);
 for(const gone of ['axis','tcpAxis','tcpLevel'])assert.equal(L[gone],undefined);
});
test('normalize plots P50 and keeps P95 for the tooltip',()=>{
 const {h,target}=fixture();const p=L.normalize(h,target).points[1430];assert.equal(p.value,30);assert.equal(p.p95,40);
});
test('p50_ms is validated like p95_ms',()=>{
 const {h,target,points}=fixture();points[1430].p50_ms=-1;assert.throws(()=>L.normalize(h,target));points[1430].p50_ms=Infinity;assert.throws(()=>L.normalize(h,target));points[1430].p50_ms='3';assert.throws(()=>L.normalize(h,target));delete points[1430].p50_ms;assert.throws(()=>L.normalize(h,target));points[1430].p50_ms=null;assert.equal(L.normalize(h,target).points[1430].value,null);
});
test('TCP known_gap retains only valid identity-safe P50, with original categories',()=>{
 const {h,target,points}=fixture();Object.assign(points[1432],{execution_coverage:'known_gap',not_executed:1,completeness:'partial',categories:{success:1,timeout:1}});let p=L.normalize(h,target).points[1432];assert.equal(p.value,32);assert.equal(p.partial,true);assert.equal(p.valid,2);assert.equal(p.count,2);assert.equal(p.notExecuted,1);assert.deepEqual(p.categories,{success:1,timeout:1});
 points[1432].config_revision_min='8';assert.equal(L.normalize(h,target).points[1432].value,null);points[1432].config_revision_min='9';points[1432].slot_revision_max='8';assert.equal(L.normalize(h,target).points[1432].value,null);points[1432].slot_revision_max='7';points[1432].valid_rtts=0;assert.equal(L.normalize(h,target).points[1432].value,null);
});
test('success plus failure, null, unexecuted, partial zero and sub-ms retain truthful P50',()=>{
 const {h,target,points}=fixture();Object.assign(points[1430],{valid_rtts:1,p50_ms:.4,p95_ms:.4,categories:{success:1,timeout:1}});Object.assign(points[1431],{execution_coverage:'known_gap',p50_ms:0,p95_ms:0});Object.assign(points[1432],{p50_ms:null,p95_ms:null,valid_rtts:0,categories:{timeout:2}});Object.assign(points[1433],{p50_ms:null,p95_ms:null,valid_rtts:0,not_executed:2,execution_coverage:'known_gap'});const n=L.normalize(h,target);assert.equal(n.points[1430].value,.4);assert.equal(n.points[1431].value,0);assert.equal(n.points[1431].partial,true);assert.equal(n.points[1432].value,null);assert.equal(n.points[1433].value,null);
});
test('old semantics and mismatched API remain rejected',()=>{
 const {h,target}=fixture();h.groups[0].semantics='single_probe_v1';assert.equal(L.normalize(h,target).supported,false);h.target={...target,family:'ipv6'};assert.throws(()=>L.normalize(h,target));h.target=target;h.algorithm='unknown';assert.throws(()=>L.normalize(h,target));
});
test('ICMP preserves the known_gap mask and has no partial field',()=>{
 const {h,target,points}=fixture('icmp');points[1431].execution_coverage='known_gap';const n=L.normalize(h,target);assert.equal(n.points[1431].value,null);assert.equal(Object.hasOwn(n.points[1430],'partial'),false);assert.equal(n.points[1430].value,30);
});
test('recent(): median of the last five completed minutes, skipping null/partial/in-progress, "—" below three',()=>{
 const run=(values,extra={})=>{const {h,target,points}=fixture();for(const p of points)Object.assign(p,{p50_ms:null,p95_ms:null,valid_rtts:0,count:0,completeness:'no_data'});for(const [i,v]of values)set(points,i,v);extra.fn?.(points);return L.recent(L.normalize(h,target));};
 assert.equal(run([[1434,100],[1435,120],[1436,140],[1437,160],[1438,180],[LAST,9999]]),140);
 assert.equal(run([[1434,100],[1435,300],[1436,140],[1437,160],[1438,180]]),160);
 assert.equal(run([[1435,100],[1436,200],[1437,300],[1438,400]]),250,'even count averages the middle pair');
 assert.equal(run([[1434,100],[1436,140],[1438,180]]),140,'three usable of five is enough');
 assert.equal(run([[1434,100],[1436,140]]),null,'two usable values');
 assert.equal(run([[1434,100],[1435,120],[1436,140],[1437,160],[1438,180]],{fn:p=>{Object.assign(p[1436],{execution_coverage:'known_gap',completeness:'partial'});Object.assign(p[1437],{p50_ms:null,p95_ms:null,valid_rtts:0});Object.assign(p[1435],{p50_ms:null,p95_ms:null,valid_rtts:0});}}),null,'partial and null minutes are skipped');
 assert.equal(run([[1420,100],[1421,100],[1422,100]]),null,'only the last five minutes count');
});

// ---------- rendering through the real create()/render() with a minimal DOM ----------
class El{
 constructor(tag,ns){this.tagName=tag;this.namespace=ns;this.children=[];this.attributes={};this.dataset={};this.style={setProperty(){}};this.listeners={};this.className='';this.ownText='';this.isConnected=true;this.hidden=false;}
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
async function card({protocol='tcp',series,status={state:'normal',level_ms:2000,now:NOW},id='a'}){
 // series: {slot: points => void}; slots absent from `series` get empty history.
 const histories={};
 for(const proto of ['tcp','icmp'])for(const slot of Object.keys(IDS)){const f=fixture(proto,slot);for(const p of f.points)Object.assign(p,{count:0,valid_rtts:0,p50_ms:null,p95_ms:null,completeness:'no_data'});if(proto===protocol)series[slot]?.(f.points);histories[f.target.id]=f.h;}
 const config={revision:'1',region_id:'test',supported:true,enabled:true,targets:['tcp','icmp'].flatMap(proto=>Object.keys(IDS).map(slot=>fixture(proto,slot).target))};
 const state={};
 state.fetchJSON=async url=>{
  if(url.includes('/tcp-mini'))return status;
  const tid=decodeURIComponent(/target_id=([^&]+)/.exec(url)[1]);return histories[tid];};
 const api=L.create({document:fakeDoc,state,interval:()=>0,clear(){},now:()=>NOW,monotonicNow:()=>0,unauthorized(){}});
 const node=api.mount({agent_id:id,online:true});
 const plot=node.all().find(n=>n.className==='lm-plot');plot.rect={width:300,height:80,left:0,top:0,right:300,bottom:80};node.rect={width:300,height:200,left:0,top:0,right:300,bottom:200};globalThis.innerHeight=1000;globalThis.innerWidth=1000;
 const flush=async()=>{for(let i=0;i<30;i++){await new Promise(r=>setImmediate(r));api.tick();}};
 api.bindConfig(id,config);api.setVisible(id,true);await flush();
 if(protocol==='icmp'){node.all().find(n=>n.tagName==='button'&&n.textContent==='ICMP').dispatch('click');await flush();}
 const svg=()=>plot.children[0].all();
 const nums=()=>Object.fromEntries(node.all().filter(n=>n.className.startsWith('lm-num')).map((n,i)=>[Object.keys(IDS)[i],n]));
 return {api,node,plot,svg,nums,flush,unit:node.all().find(n=>n.className==='lm-unit')};
}
const solid=c=>c.svg().filter(n=>n.tagName==='path'&&n.attributes['stroke-width']==='1.2');
const dashed=c=>c.svg().filter(n=>n.tagName==='path'&&n.attributes['stroke-dasharray']);
const firstY=d=>Number(/^M[\d.]+,([\d.]+)/.exec(d)[1]);
const yOf=v=>68-Math.min(1,L.ratio(v))*66; // plot height 80 => drawing height 68
const line=(points,from,to,fn)=>{for(let i=from;i<=to;i++)set(points,i,fn(i-from));};

test('curve is drawn from P50, not P95',async()=>{
 const c2=await card({series:{telecom:p=>{line(p,1420,1428,()=>100);for(let i=1420;i<=1428;i++)p[i].p95_ms=3000;}}});
 const d=solid(c2)[0].attributes.d;assert.ok(Math.abs(firstY(d)-yOf(100))<.01,d);
 assert.equal(dashed(c2).length,0,'a P95 of 3000ms must not appear on the curve');
 assert.equal(c2.plot.dataset.scale,'fixed-piecewise');
 assert.match(c2.plot.title,/一分钟 RTT P50；固定刻度：0–300ms 占 80%，300–1000ms 占 20%，超过 1000ms 用顶边虚线表示/);
});
test('the same value has the same y on cards with completely different data ranges',async()=>{
 const a=await card({series:{telecom:p=>line(p,1420,1428,i=>i%2?100:120)}});
 const b=await card({id:'b',series:{telecom:p=>line(p,1420,1428,i=>i%2?100:290),unicom:p=>line(p,1420,1428,()=>5)}});
 const ya=firstY(solid(a)[0].attributes.d),yb=solid(b).map(n=>firstY(n.attributes.d));
 assert.ok(Math.abs(ya-yOf(120))<.01);
 // card b starts with 290 at index 1420 and 100 at 1421: both live on the same ruler.
 assert.ok(Math.abs(yb[0]-yOf(290))<.01);
 const grid=card2=>card2.svg().filter(n=>n.attributes['data-grid']).map(n=>n.attributes['data-grid']+':'+n.attributes.d);
 assert.deepEqual(grid(a),grid(b));assert.deepEqual(grid(a).map(x=>x.split(':')[0]),['100','200','300']);
 const ticks=card2=>card2.svg().filter(n=>n.dataset.axisTick).map(n=>n.dataset.axisTick+n.ownText);
 assert.deepEqual(ticks(a),ticks(b));assert.ok(ticks(a).includes('1000'+'1000 ms')&&ticks(a).includes('300300')&&ticks(a).includes('100100'));
});
test('a run above 1000ms is one dashed top-edge segment joined to the solid line',async()=>{
 const c=await card({series:{telecom:p=>{line(p,1420,1423,()=>100);line(p,1424,1428,()=>1500);p[1424].p95_ms=3010;line(p,1429,1432,()=>100);}}});
 const d=dashed(c);assert.equal(d.length,1);assert.equal(d[0].attributes['data-minutes'],'5');assert.equal(d[0].attributes['stroke-dasharray'],'3,2');
 assert.match(d[0].attributes.d,/^M[\d.]+,2H[\d.]+$/);
 const path=solid(c)[0].attributes.d;assert.match(path,/L[\d.]+,2/,'solid line reaches the top edge');
 assert.equal(c.svg().filter(n=>n.attributes['data-boundary']==='above-scale').length,1);
 assert.equal(c.svg().filter(n=>n.tagName==='text'&&/越界/.test(n.ownText)).length,0,'the old ▲历史越界 label is gone');
});
test('a single minute above 1000ms is a short dashed mark with the real values on hover',async()=>{
 const c=await card({series:{telecom:p=>{set(p,1435,1830,3010);}}});
 const d=dashed(c);assert.equal(d.length,1);assert.equal(d[0].attributes['data-minutes'],'1');
 const m=/^M([\d.]+),2H([\d.]+)$/.exec(d[0].attributes.d);assert.ok(Number(m[2])-Number(m[1])>=3);
 const left=Number(c.plot.dataset.axisLeft),start=Number(c.plot.dataset.start),end=Number(c.plot.dataset.end),epoch=FIRST+1435*M+1000;
 c.plot.dispatch('pointermove',{pointerType:'mouse',pointerId:1,clientX:left+(epoch-start)/(end-start)*(300-left)});
 const note=c.node.all().find(n=>n.tagName==='output').textContent;
 assert.match(note,/电信：.*\nP50 1830ms · P95 3010ms · 超出刻度/);
});
test('top numbers: median of the last five minutes, lowest bold, >1000 red, "—" below three values',async()=>{
 const c=await card({series:{
  telecom:p=>{[100,120,140,160,180].forEach((v,k)=>set(p,1434+k,v));set(p,LAST,9999);},
  unicom:p=>{set(p,1434,200);set(p,1438,200);},
  mobile:p=>{[1500,1600,1700,1800,1900].forEach((v,k)=>set(p,1434+k,v));}}});
 const n=c.nums();
 assert.equal(n.telecom.textContent,'140');assert.equal(n.unicom.textContent,'—');assert.equal(n.mobile.textContent,'1700');
 assert.equal(n.telecom.dataset.state,'lowest');assert.match(n.telecom.className,/lm-lowest/);
 assert.equal(n.mobile.dataset.state,'over');assert.match(n.mobile.className,/lm-over/);assert.doesNotMatch(n.mobile.className,/lm-lowest/);
 assert.equal(n.unicom.dataset.state,'none');assert.equal(c.unit.textContent,'ms');
 // Panning away from "now" does not change the headline numbers.
 c.plot.dispatch('keydown',{key:'ArrowLeft'});c.plot.dispatch('keydown',{key:'ArrowLeft'});
 assert.equal(c.plot.dataset.end!==String(NOW),true);
 assert.equal(c.nums().telecom.textContent,'140');
});
test('a lone value is not marked lowest; deselecting a line clears its number',async()=>{
 const c=await card({series:{telecom:p=>[100,120,140,160,180].forEach((v,k)=>set(p,1434+k,v))}});
 let n=c.nums();assert.equal(n.telecom.textContent,'140');assert.doesNotMatch(n.telecom.className,/lm-lowest/);
 const button=c.node.all().find(x=>x.tagName==='button'&&x.textContent.startsWith('联通'));button.dispatch('click');await c.flush();
 assert.equal(c.nums().unicom.textContent,'');
});
test('TCP alert shows a red 告警 mark with the evidence tooltip; normal hides it; unknown keeps the coverage hint',async()=>{
 const mark=c=>c.svg().filter(n=>n.dataset.alert==='true');
 const alert=await card({series:{telecom:p=>line(p,1420,1428,()=>100)},status:{state:'alert',level_ms:5000,now:NOW,coverage:'覆盖完整',source_day:'2026-10-16'}});
 assert.equal(mark(alert).length,1);assert.equal(mark(alert)[0].ownText,'告警');assert.equal(mark(alert)[0].attributes.fill,'#c42b32');assert.match(mark(alert)[0].textContent,/告警来源日 2026-10-16/);
 assert.equal(alert.plot.dataset.warning,'true');assert.match(alert.plot.title,/告警来源日 2026-10-16/);
 assert.equal(alert.svg().filter(n=>n.tagName==='text'&&/log/.test(n.ownText)).length,0);
 const normal=await card({series:{telecom:p=>line(p,1420,1428,()=>100)}});
 assert.equal(mark(normal).length,0);assert.equal(normal.plot.dataset.warning,'false');
 const unknown=await card({series:{},status:{state:'unknown',level_ms:2000,now:NOW}});
 assert.equal(mark(unknown).length,0);assert.ok(unknown.svg().some(n=>n.ownText==='告警覆盖不足'));
 // the old ceiling number turning red is gone: the top label no longer depends on the alert level.
 assert.equal(alert.svg().find(n=>n.dataset.axisTick==='1000').attributes.fill,'#4e5965');
});
test('ICMP uses the same fixed scale, ruler and top numbers, and never shows the TCP alert',async()=>{
 const t=await card({series:{telecom:p=>line(p,1420,1428,()=>150)},status:{state:'alert',level_ms:5000,now:NOW}});
 const i=await card({id:'i',protocol:'icmp',series:{telecom:p=>{line(p,1420,1428,()=>150);line(p,1430,1432,()=>2000);}},status:{state:'alert',level_ms:5000,now:NOW}});
 assert.equal(i.plot.dataset.scale,'fixed-piecewise');
 assert.ok(Math.abs(firstY(solid(i)[0].attributes.d)-firstY(solid(t)[0].attributes.d))<.01);
 assert.ok(Math.abs(firstY(solid(i)[0].attributes.d)-yOf(150))<.01);
 assert.equal(dashed(i).length,1);
 const ticks=card=>card.svg().filter(n=>n.dataset.axisTick).map(n=>n.dataset.axisTick);
 assert.deepEqual(ticks(i),ticks(t));
 assert.equal(i.svg().filter(n=>n.dataset.alert==='true').length,0);
 assert.doesNotMatch(i.plot.title,/告警来源日/);
});
