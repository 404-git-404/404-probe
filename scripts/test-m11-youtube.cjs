const test=require('node:test'),assert=require('node:assert/strict');
const YouTube=require('../web/static/youtube-status.js');
const now=1800000000000;
const agent=(youtube,extra={})=>({online:true,google_status:{supported:true,pending:false,checked_at:now-1000,next_due_at:now+86400000,result:{youtube},...extra}});

test('M11: a page region makes no playback, entitlement or IP geolocation claim',()=>{
 for(const region of ['US','JP','HK','CN','DE','CW']){
  const view=YouTube.view(agent({status:region==='CN'?'cn':'not_cn',region}),now);
  assert.equal(view.label,'地区 '+region);assert.equal(view.tone,'observed');
  assert.doesNotMatch(view.label,/正常|解锁|送中/);assert.match(view.description,/未验证视频播放或 Premium 解锁/);assert.match(view.description,/不代表出口 IP 归属/);
 }
});
test('M11: latest unknown cannot expose a current region or green normal tone',()=>{
 for(const category of ['consent_page','challenge_page','region_conflict','timeout','rate_limited','tls_error']){
  const view=YouTube.view(agent({status:'unknown',error:{category}}),now);assert.equal(view.label,'未知');assert.equal(view.known,false);assert.equal(view.tone,'unknown');assert.ok(view.reason.length>0);
 }
 assert.equal(YouTube.view(agent({status:'not_cn',region:'ZZ'}),now).known,false);
});
test('M11: missing, pending, stale, offline, paused and unsupported remain distinct',()=>{
 const yt={status:'not_cn',region:'US'};
 assert.equal(YouTube.view(agent(yt,{result:null}),now).label,'尚未检测');
 assert.equal(YouTube.view(agent(yt,{pending:true}),now).label,'检测中');
 assert.equal(YouTube.view(agent(yt,{supported:false}),now).label,'不支持');
 assert.equal(YouTube.view(agent(yt,{stale:true}),now).label,'过期·US');
 assert.equal(YouTube.view({...agent(yt),online:false},now).label,'旧·US');
 assert.equal(YouTube.view({...agent(yt),disabled_at:1},now).tone,'stale');
 assert.equal(YouTube.view(agent(yt,{next_due_at:now-1}),now).label,'过期·US');
 assert.equal(YouTube.view(agent(yt,{next_due_at:undefined,checked_at:now-86400001}),now).label,'过期·US');
});
test('M11: expired work has an expiry timestamp, never a claimed completed network test',()=>{
 const view=YouTube.view(agent({status:'unknown',error:{category:'job_expired'}}),now);
 assert.equal(view.label,'未知');assert.match(view.description,/作业到期时间/);assert.match(view.description,/不能据此确认已执行网络检测/);
});
