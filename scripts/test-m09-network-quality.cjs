const {test}=require('node:test'),assert=require('node:assert/strict');
const C=require('../web/static/network-quality-core.js');
const target={id:'b'.repeat(32),slot:'telecom',family:'ipv4',protocol:'tcp',slot_revision:'9007199254740993',status:'active'};
const point={start:300000,end:600000,count:3,valid_rtts:3,p95_ms:149,config_revision_min:'2',config_revision_max:'2',slot_revision_min:target.slot_revision,slot_revision_max:target.slot_revision,completeness:'complete',execution_coverage:'unverified',sent:3,received:3};
const sample={target_id:target.id,config_revision:'2',slot_revision:target.slot_revision,started_at:599000,outcome:'success',sent:3,received:3,details:{semantics:'staged_probe_v2',probe_complete:true,connect_complete:true,connect_ms:152,requested:3,window_complete:true}};
const h={now:600000,resolution_ms:300000,current:[{semantics:'staged_probe_v2',metric:'pure_connect',latest_probe:sample,stale:false,freshness_budget_ms:76000}],groups:[{semantics:'staged_probe_v2',metric:'pure_connect',points:[point]}]};
test('TCP P95 strict 150/250 inclusive boundaries and explicit identities',()=>{
 for(const [v,tone]of [[149.999,'low'],[150,'mid'],[250,'mid'],[250.001,'high']])assert.equal(C.cardBlock({...point,p95_ms:v},target).tone,tone);
 assert.match(C.cardBlock(point,target).text,/P95.*149/);
 assert.equal(C.cardBlock({...point,config_revision_max:'3'},target).tone,'neutral');
 assert.equal(C.cardBlock({...point,slot_revision_min:'1',slot_revision_max:'1'},target).tone,'neutral');
 assert.equal(C.cardBlock({...point,execution_coverage:'known_gap'},target).tone,'neutral');
});
test('latest Connect distinct from DNS and historical P95; failure/current stale',()=>{
 assert.equal(C.cardCurrent(h,target,{revision:'2'}).text,'152.0ms');
 assert.equal(C.cardCurrent({...h,current:[{...h.current[0],latest_probe:{...sample,outcome:'refused'}}]},target,{revision:'2'}).text,'失败');
 assert.equal(C.cardCurrent(h,target,{old:true}).text,'过期');
 assert.equal(C.cardCurrent(h,target,{elapsed:80000}).text,'过期');
 assert.equal(C.cardCurrent(h,target,{revision:'3'}).text,'未知');
 const legacy={...h,current:[{...h.current[0],semantics:'single_probe_v1',metric:'legacy_dial_including_dns'}],groups:[{...h.groups[0],semantics:'single_probe_v1',metric:'legacy_dial_including_dns'}]};
 assert.equal(C.cardCurrent(legacy,target).text,'未知');assert(C.cardPoints(legacy,target).every(p=>p.count===0));
});
test('ICMP latest complete three Echo denominator, cancellation cannot fabricate loss',()=>{
 const t={...target,protocol:'icmp'},history={...h,current:[{...h.current[0],metric:'echo_rtt'}]};
 for(const [received,text]of [[3,'0%'],[2,'33.3%'],[1,'66.7%'],[0,'100%']]) {
  const v={...history,current:[{...history.current[0],latest_probe:{...sample,received}}]};assert.equal(C.cardCurrent(v,t).text,text);
 }
 assert.equal(C.cardCurrent({...history,current:[{...history.current[0],latest_probe:{...sample,details:{...sample.details,window_complete:false},outcome:'canceled'}}]},t).text,'未知');
 assert.equal(C.cardBlock(point,t).tone,'low');assert.equal(C.cardBlock({...point,received:2},t).tone,'loss');
 assert.match(C.cardBlock({...point,received:2},t).text,/33.3%.*发送 3 \/ 接收 2/);
 assert.equal(C.cardBlock({...point,sent:0,received:0},t).tone,'neutral');
});
test('historical completed colors survive global stale/offline/request failure',()=>{
 const before=C.cardPoints(h,target).map(p=>C.cardBlock(p,target));
 for(const old of [true,false]) {C.cardCurrent(h,target,{old});assert.deepEqual(C.cardPoints(h,target).map(p=>C.cardBlock(p,target)),before);}
 assert.equal(before.length,12);
});
test('v3 detail ratios use exact Connect denominator; probe attempts remain separate',()=>{
 const r=C.ratio({connect_attempts:4,connect_successes:3,probe_attempts:6,probe_successes:3},'tcp');assert.equal(r.value,25);assert.equal(r.fraction,'1 / 4');
 assert.equal(C.ratio({connect_attempts:0,connect_successes:0},'tcp').value,null);
});
test('M09 R1 legacy unsupported and unknown statuses never imply user pause',()=>{
 for(const protocol of ['tcp','icmp']) {
  for(const status of ['not_scheduled_legacy','unsupported']) {
   const result=C.cardCurrent(h,{...target,protocol,status});
   assert.equal(result.text,'不支持');assert.match(result.detail,new RegExp(status));
   assert.match(result.detail,/不支持 staged_probe_v2/);assert.match(result.detail,/不使用旧 DNS-inclusive/);
  }
  assert.equal(C.cardCurrent(h,{...target,protocol,status:'paused'}).text,'暂停');
  for(const status of ['unavailable','unrecognized',undefined])assert.equal(C.cardCurrent(h,{...target,protocol,status}).text,'未知');
 }
 assert.equal(C.cardCurrent(h,null).text,'未知');
 assert.equal(C.cardCurrent(h,{...target,id:null,status:'not_scheduled_legacy'}).text,'不支持');
 assert.equal(C.cardCurrent(h,target,{revision:'2'}).text,'152.0ms');
 assert.deepEqual(C.cardPoints(h,target).map(p=>C.cardBlock(p,target)),C.cardPoints(h,{...target,status:'not_scheduled_legacy'}).map(p=>C.cardBlock(p,target)));
});
