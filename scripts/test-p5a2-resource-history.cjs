const {test}=require('node:test'),assert=require('node:assert/strict');
const H=require('../web/static/resource-history.js'),C=require('../web/static/network-quality-core.js'),S=require('../web/static/agent-state.js');
const id='a'.repeat(32),now=1700000040000;
function dto(){const p={timestamp:now-60000,cpu:0,ram_percent:50,swap_percent:0,disk_percent:20,load1:1,load5:2,load15:3,rx_rate:1000000,tx_rate:0,rx_total:9007199254740992,secret:'not copied'};
 return {agent_id:id,server_now:now,from:now-3600000,to:now,bucket_from:now-3600000,bucket_to:now,interval_ms:60000,source:'minute_metrics',aggregation:'arithmetic_mean',retention_days:30,coverage:'partial',point_limit:1441,oldest_timestamp:p.timestamp,last_timestamp:p.timestamp,points:[p]};}
const read=(fetcher,{signal,timeoutMs=1000}={})=>C.transportLease(tracked=>S.fetchJSON('/resources/history',{fetcher:tracked,signal,timeoutMs}),H.boundedFetcher(fetcher));
test('P5a2 real Response stream parsed under physical lease; known DTO fields and zero kept, totals not treated as exact',async()=>{
 const lease=read(async()=>new Response(JSON.stringify(dto())));const v=H.validate(await lease.result,id);await lease.settled;
 assert.equal(v.points[0].cpu,0);assert.equal(v.points[0].tx_rate,0);assert.equal(v.points[0].rx_total,undefined);assert.equal(v.points[0].secret,undefined);
 assert.equal(H.columns(v).data[1][0],1000000);assert.equal(H.columns(v,'cpu').data[1][0],0);
});
test('P5a2 actual standard stream incrementally rejects byte overflow and cancels/releases reader',async()=>{
 let canceled=0;const body=new ReadableStream({start(c){c.enqueue(new Uint8Array(H.MAX_BYTES));c.enqueue(new Uint8Array(1));},cancel(){canceled++;}});
 const lease=read(async()=>new Response(body));await assert.rejects(lease.result,/1MiB/);await lease.settled;assert.equal(canceled,1);assert.equal(body.locked,false);
});
test('P5a2 actual hanging stream timeout/cancel settles physical lease and returns connection slot',async()=>{
 for(const manual of [false,true]){
  let canceled=0;const body=new ReadableStream({pull(){},cancel(){canceled++;}}),controller=new AbortController();
  const lease=read(async()=>new Response(body),{signal:controller.signal,timeoutMs:20});if(manual)setImmediate(()=>controller.abort());
  await assert.rejects(lease.result,e=>e.name===(manual?'AbortError':'TimeoutError'));await lease.settled;assert.equal(canceled,1);assert.equal(body.locked,false);
 }
});
test('P5a2 physical owner cannot release during hung stream cancellation; no shared core changes',async()=>{
 let releaseCancel,started=false;const body=new ReadableStream({pull(){},cancel(){return new Promise(r=>releaseCancel=r);}}),controller=new AbortController();
 const q=C.coordinator(),owner={};const result=q.request('resources',owner,signal=>{started=true;return read(async()=>new Response(body),{signal,timeoutMs:1000});});
 for(let i=0;i<10;i++)await new Promise(r=>setImmediate(r));assert.ok(started);q.release(owner);await assert.rejects(result,e=>e.name==='AbortError');
 assert.equal(q.snapshot().active,1);assert.equal(body.locked,true);releaseCancel();for(let i=0;i<10;i++)await new Promise(r=>setImmediate(r));assert.equal(q.snapshot().active,0);assert.equal(body.locked,false);q.close();controller.abort();
});
test('P5a2 errors/status/auth, invalid JSON/UTF8 and no unchecked json fallback',async()=>{
 for(const body of ['not json',new Uint8Array([0xff])]){const lease=read(async()=>new Response(body));await assert.rejects(lease.result);await lease.settled;}
 const lease=read(async()=>({ok:true,status:200,json:()=>dto()}));await assert.rejects(lease.result,/ReadableStream/);await lease.settled;
 const bad=read(async()=>new Response('{}',{status:500}));await assert.rejects(bad.result,e=>e.status===500);await bad.settled;
});
test('P5a2 actual DTO rejects wrong identities/query/bounds/unsafe timestamps/nonfinite fields/point order',()=>{
 for(const mutate of [v=>v.agent_id='b'.repeat(32),v=>v.source='SSE',v=>v.coverage='complete',v=>v.interval_ms=10000,v=>v.point_limit=2000,v=>v.points[0].cpu=NaN,v=>v.points[0].cpu=null,v=>v.points[0].timestamp++,v=>v.points.push(v.points[0]),v=>v.server_now=Number.MAX_SAFE_INTEGER+1,v=>v.bucket_to++,v=>v.from--,v=>v.oldest_timestamp=null]) {const v=dto();mutate(v);assert.throws(()=>H.validate(v,id));}
 const custom=dto();custom.from=now-120000;custom.bucket_from=custom.from;assert.ok(H.validate(custom,id,{from:custom.from,to:now}));assert.throws(()=>H.validate(custom,id,{from:custom.from+1,to:now}));
});
test('P5a2 missing-minute separators bounded by points; no edge extrapolation, zero never becomes gap',()=>{
 const v=H.validate(dto(),id);v.points.unshift({...v.points[0],timestamp:now-240000});const a=H.columns(v);
 assert.equal(a.data[0].length,3);assert.equal(a.points[1],null);assert.equal(a.data[1][1],null);assert.equal(a.data[2][2],0);
 assert.equal(a.data[0][0],(now-240000)/1000);assert.equal(a.data[0].at(-1),(now-60000)/1000);assert.equal(H.columns({points:[]}).data[0].length,0);
 assert.throws(()=>H.range('bad','bad',now));assert.throws(()=>H.range(now-86400001,now,now));assert.deepEqual(H.range(now-3600000,now,now),{from:now-3600000,to:now});
});
