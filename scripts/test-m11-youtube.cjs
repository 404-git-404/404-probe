const test=require('node:test'),assert=require('node:assert/strict');
const YouTube=require('../web/static/youtube-status.js');
const now=1800000000000;
const agent=(youtube,extra={})=>({online:true,google_status:{supported:true,pending:false,checked_at:now-1000,next_due_at:now+86400000,result:{youtube},...extra}});

test('M11: a page region makes no playback, entitlement or IP geolocation claim',()=>{
 for(const region of ['US','JP','HK','DE','CW']){
  const view=YouTube.view(agent({status:'not_cn',region}),now);
  assert.equal(view.label,'地区 '+region);assert.equal(view.tone,'observed');
  assert.doesNotMatch(view.label,/正常|解锁|送中/);assert.match(view.description,/未验证视频播放或 Premium 解锁/);assert.match(view.description,/不代表出站 IP 归属/);
 }
});
test('M11: latest unknown cannot expose a current region or green normal tone',()=>{
 for(const category of ['challenge_page','region_conflict','timeout','rate_limited','tls_error']){
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
 for(const region of ['MO','RU']){const v=premium(region);assert.equal(v.premium,'unavailable');assert.match(v.description,/Premium 不可用地区（依据 YouTube 官方名单，核对日期 \d{4}-\d{2}-\d{2}）/);}
 assert.match(premium('US').description,/Premium 可用地区/);assert.match(premium('US').description,/未验证视频播放或 Premium 解锁/);assert.match(premium('CN').description,/不代表出站 IP 归属/);
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

test('L2: an EU consent page shows a green 地区 EU with Premium available and the explanation',()=>{
 const view=YouTube.view(agent({status:'unknown',error:{category:'consent_page'}}),now);
 assert.equal(view.label,'地区 EU');assert.equal(view.tone,'observed');assert.equal(view.premium,'available');
 assert.equal(view.known,false,'no country is claimed');
 assert.ok(view.description.includes('遇到欧盟隐私同意页，具体国家未知；欧盟/欧洲经济区、英国、瑞士均在 Premium 可用名单内'));
 assert.doesNotMatch(view.description,/Premium 不可用地区/);
 assert.match(view.description,/不代表出站 IP 归属/);
});
test('L2: a consent page that is stale, offline, pending, unsupported or not yet checked stays neutral',()=>{
 const consent={status:'unknown',error:{category:'consent_page'}};
 for(const view of [YouTube.view(agent(consent,{stale:true}),now),YouTube.view({...agent(consent),online:false},now),YouTube.view({...agent(consent),disabled_at:1},now),YouTube.view(agent(consent,{next_due_at:now-1}),now)]){
  assert.equal(view.tone,'stale');assert.equal(view.premium,null);assert.match(view.label,/^(旧|过期)·未知$/);
 }
 for(const extra of [{pending:true},{supported:false},{result:null}]){
  const view=YouTube.view(agent(consent,extra),now);assert.notEqual(view.label,'地区 EU');assert.equal(view.premium,null);
 }
});
test('L2: other unknown categories never turn green, and a real region still wins over consent wording',()=>{
 for(const category of ['challenge_page','invalid_configuration','missing_evidence','unexpected_page','timeout','unrecognized_response']){
  const view=YouTube.view(agent({status:'unknown',error:{category}}),now);assert.equal(view.label,'未知');assert.equal(view.premium,null);assert.equal(view.tone,'unknown');
 }
 const view=YouTube.view(agent({status:'not_cn',region:'IT',error:{category:'consent_page'}}),now);assert.equal(view.label,'地区 IT');
});
test('L2: the card and the history page both take the label and premium colour from view()',()=>{
 const fs=require('node:fs'),read=f=>fs.readFileSync(require.resolve('../web/static/'+f),'utf8');
 assert.ok(read('app.js').includes('YouTubeStatus.view(agent)')&&read('app.js').includes('value.label,value.tone,value.premium'));
 assert.ok(read('history.js').includes('observation.premium'));
});

// R23: 送中 means Google treats the IP as being in China; the page region is only a default there.
const chinaAgent=(extra={},base={})=>({...agent({status:'cn',sent_to_china:true,region:'US',...extra}),...base});
test('R23: 送中 is shown as the region CN in the style of every region without Premium; the details stay in the tooltip',()=>{
 const withNotice=YouTube.view(chinaAgent({premium_unavailable_notice:true}),now);
 assert.equal(withNotice.label,'地区 CN');assert.doesNotMatch(withNotice.label,/送中/);assert.equal(withNotice.tone,'observed');assert.equal(withNotice.premium,'unavailable');assert.equal(withNotice.known,false);
 assert.match(withNotice.description,/页面提示 Premium 不可用/);assert.match(withNotice.description,/页面地区 US（送中时不可信）/);assert.match(withNotice.description,/Google 把本机 IP 当作中国/);
 // The same view as an ordinary region the official list does not contain (AL, RU, BS).
 for(const region of ['AL','RU','BS']){const plain=YouTube.view(agent({status:'not_cn',region,sent_to_china:false}),now);assert.equal(plain.tone,withNotice.tone);assert.equal(plain.premium,withNotice.premium);}
 for(const extra of [{premium_unavailable_notice:false},{}]){
  const view=YouTube.view(chinaAgent(extra),now);
  assert.equal(view.label,'地区 CN');assert.equal(view.tone,'observed');assert.equal(view.premium,'unavailable');assert.match(view.description,/Premium 不可用地区（CN 不在 YouTube 官方名单内）/);assert.doesNotMatch(view.description,/页面提示 Premium 不可用/);
 }
});
test('R23: 送中 without a usable page region shows no region footnote',()=>{
 for(const region of ['CN','']){
  const view=YouTube.view(chinaAgent({region}),now);
  assert.equal(view.label,'地区 CN');assert.equal(view.premium,'unavailable');assert.doesNotMatch(view.description,/页面地区 [A-Z]{2}（送中时不可信）/);
 }
});
test('R23: stale, offline, pending and unsupported 送中 results stay neutral',()=>{
 assert.equal(YouTube.view(chinaAgent({premium_unavailable_notice:true},{google_status:{...chinaAgent({premium_unavailable_notice:true}).google_status,stale:true}}),now).label,'过期·CN');
 const offline=YouTube.view(chinaAgent({premium_unavailable_notice:true},{online:false}),now);
 assert.equal(offline.label,'旧·CN');assert.equal(offline.tone,'stale');assert.equal(offline.premium,null);
 const pending=chinaAgent({premium_unavailable_notice:true});pending.google_status.pending=true;assert.equal(YouTube.view(pending,now).label,'检测中');assert.equal(YouTube.view(pending,now).premium,null);
 const unsupported=chinaAgent({premium_unavailable_notice:true});unsupported.google_status.supported=false;assert.equal(YouTube.view(unsupported,now).premium,null);
});
test('R23: an unverified China signal is unknown with its own reason and no Premium verdict',()=>{
 const view=YouTube.view(agent({status:'unknown',error:{category:'china_signal_unverified'}}),now);
 assert.equal(view.label,'未知');assert.equal(view.tone,'unknown');assert.equal(view.premium,null);assert.equal(view.known,false);assert.match(view.reason,/页面出现中国区信号但无法确认/);
});
test('R23: a normal region is still judged by the official list and never shows 送中',()=>{
 const view=YouTube.view(agent({status:'not_cn',region:'US',sent_to_china:false}),now);
 assert.equal(view.label,'地区 US');assert.equal(view.premium,'available');assert.doesNotMatch(view.description,/送中/);
});
