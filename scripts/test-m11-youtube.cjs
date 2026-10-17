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
test('M11: Premium availability colours only valid results, by the official list',()=>{
 const premium=(region,extra={},base={})=>YouTube.view({...agent({status:region==='CN'?'cn':'not_cn',region},extra),...base},now);
 assert.equal(premium('US').premium,'available');assert.equal(premium('US').tone,'observed');
 assert.equal(premium('PR').premium,'available');
 for(const region of ['CN','MO','RU']){const v=premium(region);assert.equal(v.premium,'unavailable');assert.match(v.description,/Premium 不可用地区（依据 YouTube 官方名单，核对日期 \d{4}-\d{2}-\d{2}）/);}
 assert.match(premium('US').description,/Premium 可用地区/);assert.match(premium('US').description,/未验证视频播放或 Premium 解锁/);assert.match(premium('CN').description,/不代表出口 IP 归属/);
});
test('M11: stale, offline, pending, unknown and unsupported stay neutral',()=>{
 const stale=YouTube.view(agent({status:'cn',region:'CN'},{stale:true}),now);
 assert.equal(stale.label,'过期·CN');assert.equal(stale.premium,null);assert.equal(stale.tone,'stale');assert.doesNotMatch(stale.description,/Premium (不)?可用地区/);
 const offline=YouTube.view({...agent({status:'not_cn',region:'US'}),online:false},now);assert.equal(offline.premium,null);assert.equal(offline.tone,'stale');
 assert.equal(YouTube.view(agent({status:'unknown',error:{category:'timeout'}}),now).premium,null);
 assert.equal(YouTube.view(agent({status:'not_cn',region:'ZZ'}),now).premium,null);
 assert.equal(YouTube.view(agent({status:'not_cn',region:'US'},{pending:true}),now).premium,null);
 assert.equal(YouTube.view(agent({status:'not_cn',region:'US'},{supported:false}),now).premium,null);
});
test('M11: every Premium list entry is a valid region code',()=>{
 const source=require('node:fs').readFileSync(require.resolve('../web/static/youtube-status.js'),'utf8');
 const list=source.match(/premiumRegions=new Set\(`([^`]*)`/)[1].trim().split(/\s+/);
 assert.equal(new Set(list).size,list.length);
 for(const code of list)assert.equal(YouTube.view(agent({status:'not_cn',region:code}),now).known,true,code);
 assert.match(source,/support\.google\.com\/youtube\/answer\/6307365/);
});
