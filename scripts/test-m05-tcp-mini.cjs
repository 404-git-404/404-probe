const {test}=require('node:test'),assert=require('node:assert/strict');
const L=require('../web/static/latency-mini.js');
const M=60000,NOW=1792080030000,FIRST=Math.floor(NOW/M)*M-1439*M;
function fixture(protocol='tcp'){
 const target={id:'t',slot:'telecom',family:'ipv4',protocol,region:'test',slot_revision:'7',status:'active'};
 const points=Array.from({length:1440},(_,i)=>({start:FIRST+i*M,end:Math.min(FIRST+(i+1)*M,NOW),count:0,valid_rtts:0,not_executed:0,sent:0,received:0,p95_ms:null,completeness:'no_data',execution_coverage:'unverified',categories:{}}));
 for(let i=1430;i<1436;i++)Object.assign(points[i],{count:2,valid_rtts:2,p95_ms:40+i-1430,completeness:'complete',config_revision_min:'9',config_revision_max:'9',slot_revision_min:'7',slot_revision_max:'7',categories:{success:2}});
 const h={target,view:'1day',resolution_ms:M,now:NOW,window_start:FIRST,algorithm:'weighted-grid-rtt-adjacent-v3',histogram_version:1,grid_ms:.25,percentile_max_error_ms:.125,groups:[{semantics:'staged_probe_v2',metric:protocol==='tcp'?'pure_connect':'echo_rtt',points}]};return {target,h,points};
}
test('TCP base and unbounded 2/5/10 steps include rendering-only 7200',()=>{
 for(const [v,n]of [[0,2000],[1999,2000],[2000,2000],[2001,5000],[4700,5000],[5000,5000],[7200,10000],[10001,20000],[20001,50000],[100001,200000]])assert.equal(L.tcpLevel(v),n);
});
test('TCP uses true Log10 and never calls logarithm on quantized zero',()=>{
 const a=L.tcpAxis({state:'normal',level_ms:2000});assert.equal(a.min,5);assert.equal(a.max,2000);assert.equal(a.log,true);assert.equal(a.ratio(0),null);assert.equal(a.ratio(5),0);assert.equal(a.ratio(2000),1);assert.equal(a.ratio(100),(Math.log10(100)-Math.log10(5))/(Math.log10(2000)-Math.log10(5)));
});
test('server persistent status controls warning independently of visible values',()=>{
 const alert=L.tcpAxis({state:'alert',level_ms:5000});assert.equal(alert.warning,true);assert.equal(alert.max,5000);const restored=L.tcpAxis({state:'normal',level_ms:2000});assert.equal(restored.warning,false);assert.equal(restored.max,2000);assert.equal(L.tcpAxis({state:'unknown',level_ms:5000}).warning,false);assert.equal(L.tcpAxis().max,2000);
});
test('TCP known_gap retains only valid identity-safe P95, with original categories',()=>{
 const {h,target,points}=fixture();Object.assign(points[1432],{execution_coverage:'known_gap',not_executed:1,completeness:'partial',categories:{success:1,timeout:1}});let p=L.normalize(h,target).points[1432];assert.equal(p.value,42);assert.equal(p.partial,true);assert.equal(p.valid,2);assert.equal(p.count,2);assert.equal(p.notExecuted,1);assert.deepEqual(p.categories,{success:1,timeout:1});
 points[1432].config_revision_min='8';assert.equal(L.normalize(h,target).points[1432].value,null);points[1432].config_revision_min='9';points[1432].slot_revision_max='8';assert.equal(L.normalize(h,target).points[1432].value,null);points[1432].slot_revision_max='7';points[1432].valid_rtts=0;assert.equal(L.normalize(h,target).points[1432].value,null);
});
test('success plus failure, null, unexecuted, partial zero and sub-ms retain truthful P95',()=>{
 const {h,target,points}=fixture();Object.assign(points[1430],{valid_rtts:1,p95_ms:.4,categories:{success:1,timeout:1}});Object.assign(points[1431],{execution_coverage:'known_gap',p95_ms:0});Object.assign(points[1432],{p95_ms:null,valid_rtts:0,categories:{timeout:2}});Object.assign(points[1433],{p95_ms:null,valid_rtts:0,not_executed:2,execution_coverage:'known_gap'});const n=L.normalize(h,target);assert.equal(n.points[1430].value,.4);assert.equal(n.points[1431].value,0);assert.equal(n.points[1431].partial,true);assert.equal(n.points[1432].value,null);assert.equal(n.points[1433].value,null);
});
test('old semantics and mismatched API remain rejected',()=>{
 const {h,target}=fixture();h.groups[0].semantics='single_probe_v1';assert.equal(L.normalize(h,target).supported,false);h.target={...target,family:'ipv6'};assert.throws(()=>L.normalize(h,target));h.target=target;h.algorithm='unknown';assert.throws(()=>L.normalize(h,target));
});
test('ICMP preserves known_gap mask, fields and original linear/log1p rules',()=>{
 const {h,target,points}=fixture('icmp');points[1431].execution_coverage='known_gap';const n=L.normalize(h,target);assert.equal(n.points[1431].value,null);assert.equal(Object.hasOwn(n.points[1430],'partial'),false);assert.equal(n.points[1430].value,40);
 for(const values of [[0,.4],[10,55],[.25,6000],[]]){const a=L.axis(values),max=Math.max(0,...values),positive=values.filter(v=>v>0),log=max>0&&positive.length>0&&max/Math.min(...positive)>100,ceiling=max?max*1.08:1;assert.equal(a.max,ceiling);assert.equal(a.log,!!log);for(const v of values)assert.equal(a.ratio(v),log?Math.log10(1+v/.125)/Math.log10(1+ceiling/.125):v/ceiling);}
});
