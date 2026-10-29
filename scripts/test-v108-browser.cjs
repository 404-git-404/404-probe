// R33: actual dashboard and native dialog in local Chromium; HTTP is loopback only.
const {test,before,after}=require('node:test'),assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {v5Card,load,C}=require('./lib/r07-harness.cjs');
let chromium;try{({chromium}=require(process.env.M02_PLAYWRIGHT||'C:/Users/ufoli/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright'));}catch(_){/* optional local browser dependency */}
const chromiumPath=process.env.M02_CHROMIUM||'C:/Users/ufoli/AppData/Local/ms-playwright/chromium-1228/chrome-win64/chrome.exe';
const available=Boolean(chromium&&fs.existsSync(chromiumPath)),browserOptions={skip:!available&&'Playwright/Chromium not available'};
const root=path.join(__dirname,'../web/static'),ID='a'.repeat(32),M=60000,DAY=86400000;
const dashboardFixture = a => ({items:[{...a,accumulated_traffic:a.traffic,traffic:{known:true,stale:false,quota_state:'current'},resources:{known:!!a.state,stale:false,cpu_percent:a.state?.cpu_percent,ram_percent:a.state?.ram_percent,disk_percent:a.state?.disk_percent,collected_at:a.state?.collected_at,max_age_ms:600000},network:{known:true,active:false,monitored:true},renewal:{type:'upcoming',days:40},search_fields:[a.name,a.state?.hostname||''],group_id:null,tag_ids:[]}],revision:1,groups:[],tags:[],exchange_rates:[{currency:'USD',units_per_usd:'1'}],expenses:{monthly_usd:'0.00',yearly_usd:'0.00'},server_now:Date.now()});
let browser,server,base,scenario='normal',posts=[],requests=[];
const now=()=>Date.now();
function config(){const v=load(scenario==='real'?'v4-saved-off':'v4-running');if(scenario==='real')return v;v.slots.forEach((s,i)=>{s.kind='builtin';s.region=['上海','广州','北京'][i];s.isp=C.slots[i];s.slot_enabled=true;s.ipv4_tcp=true;s.ipv4_icmp=true;});v.lanes.forEach((l,i)=>{if(l.family==='ipv4'){l.status=scenario==='special'&&l.slot==='unicom'&&l.protocol==='tcp'?'inactive':'active';l.target_id||=(i+1).toString(16).padStart(32,'0');l.slot_revision='2';}});return v;}
function agent(){const n=now();const a={agent_id:ID,name:'DediRock-LAX-1',created_at:n-52*DAY,online:true,last_seen:n,revoked:false,disabled_at:null,country_code:'US',country_source:'manual',version:'v1.0.7',upgrade_capable:true,
 state:{collected_at:n,uptime:52*86400,os:'linux',platform:'debian',platform_version:'13.6',hostname:'DediRock-LAX-1',cpu_percent:scenario==='special'?12.4:1.3,ram_percent:scenario==='special'?83.2:15.9,disk_percent:scenario==='special'?92.1:19.3,cpu_cores:1,load1:.09,load5:.06,load15:.01,cpu_steal_percent:0,ram_used:234*2**20,ram_total:1.44*2**30,swap_used:0,swap_total:512*2**20,disk_used:4.49*2**30,disk_total:24.5*2**30,disk_read_rate:12*1024,disk_write_rate:48*1024,disk_busy_percent:1,rx_rate:369,tx_rate:265},
 traffic:{rx_total:79.3e9,tx_total:79.1e9,started_at:new Date(2026,8,30).getTime()},usage_24h:{rx:1.3e9,tx:.9e9,coverage_ms:DAY},
 plan:scenario==='special'?{renewal_date:'2026-10-30'}:{currency:'USD',renewal_price:'6.85',renewal_price_period:'yearly',renewal_date:'2026-12-01',traffic_mode:'sum',cycle_kind:'monthly',cycle_count:1,cycle_anchor:'2026-10-01',cycle_start:n-9*DAY,cycle_end:n+21*DAY,cycle_rx_bytes:20.1e9,cycle_tx_bytes:16.5e9,usage_bytes:36.6e9,quota_bytes:5e12,quota_value:'5',quota_unit:'TB',bandwidth_value:'1',bandwidth_unit:'Gbps'},
 outbounds:{configured:true,available:true,status:'connected',order_source:'config',selectors:[{name:'出站主组',current:'直连',choices:['直连','WARP','自定义']},{name:'出站备用组',current:'备用',choices:['备用','备用二']}]}};
 if(scenario==='single-outbound')a.outbounds.selectors.splice(1);
 if(scenario==='long-outbound')for(const s of a.outbounds.selectors){s.current='lax-cn-hy2-'+ 'very-long-outbound-tag-'.repeat(12);s.choices=[s.current,...Array.from({length:100},(_,i)=>'出站-'+i)];}
 a.state.os='Debian 13.6';a.public_egress_check={enabled:true};a.state.public_egress={ipv4:{status:'available',checked_at:n,valid_until:n+DAY,ip:'203.0.113.9'},ipv6:{status:'unavailable',checked_at:n}};a.google_status={supported:true,checked_at:n,next_due_at:n+DAY,result:{youtube:{status:'not_cn',region:'US'}}};
 if(scenario==='real'){const sample=JSON.parse(fs.readFileSync(process.env.R33_REAL_SAMPLE,'utf8'));a.name='本机实测（Windows）';a.state=sample;a.last_seen=sample.collected_at;a.country_code='';a.country_source='';a.plan={};a.traffic={};a.usage_24h=null;a.outbounds={configured:false};a.public_egress_check={enabled:false};a.google_status={supported:false};}
 return a;}
function summary(){const v=v5Card(config(),'ipv4',now());if(scenario==='real')return v;
 const seq=scenario==='special'?['nnggggggggggyrrrrrrr','','rrrrrrrrrrrrrrrrrrrr']:['gggggggggggggggggggg','ggggggyggggggyyggggg','ggggggggygyrrrrrggyg'];
 v.lanes.forEach((l,i)=>{if(!seq[i]){l.tcp={status:'inactive'};l.icmp={status:'active'};return;}
  l.tcp.status='active';l.tcp.blocks=Array.from(seq[i],(ch,k)=>({start:v.window_start+k*180000,state:{g:'green',y:'yellow',r:'red',n:'gray'}[ch],judged:ch==='n'?0:6,slow:scenario==='special'&&i===2?0:ch==='r'?3:ch==='y'?1:0,failed:scenario==='special'?(i===2?6:k>12&&k<19?2:0):(i===2&&k===13?3:0),unjudged:0,max_ms:ch==='r'?1840:250,in_progress:k===19,partial:false}));
  if(scenario==='special'&&i===0)l.tcp.blocks.slice(2,10).forEach(b=>b.judged=5);
  if(scenario==='normal'&&i===2)l.tcp.blocks.slice(0,10).forEach(b=>b.judged=4);
 });return v;}
function resources(q=new URLSearchParams()){const n=now(),end=q.has('to')?Number(q.get('to')):n,from=q.has('from')?Number(q.get('from')):end-(Number(q.get('hours'))||24)*3600000,start=Math.floor(from/M)*M,last=Math.floor(end/M)*M,points=[];
 for(let t=start;t<=last;t+=M){const i=Math.floor(t/M),k=i/15;points.push({timestamp:t,cpu:1+Math.abs(Math.sin(k))*3,ram_percent:15.9,swap_percent:0,disk_percent:19.3,load1:.09,load5:.06,load15:.01,rx_rate:500+Math.abs(Math.sin(k*.37))*5000,tx_rate:250+Math.abs(Math.cos(k*.41))*1500,rx_total:i*1000000,tx_total:i*700000});}
 return {agent_id:ID,server_now:n,from,to:end,bucket_from:start,bucket_to:last,interval_ms:M,source:'minute_metrics',aggregation:'arithmetic_mean',retention_days:30,coverage:'partial',point_limit:1441,oldest_timestamp:points[0]?.timestamp??null,last_timestamp:points.at(-1)?.timestamp??null,points};}
function history(targetID){const t=C.adapt(config()).targets.find(t=>t.id===targetID);if(!t)return {};
 const n=now(),start=Math.floor(n/M)*M-1439*M,idx=C.slots.indexOf(t.slot);
 const points=Array.from({length:1440},(_,i)=>{const v=[149,258,236][idx]+Math.sin(i*.4)*5+(idx===2&&i>1400&&i<1420?400:0);return {start:start+i*M,end:Math.min(start+(i+1)*M,n),count:2,valid_rtts:2,not_executed:0,sent:6,received:5,p50_5m_ms:v,p50_ms:v,p95_ms:v+10,completeness:'complete',execution_coverage:'unverified',config_revision_min:'2',config_revision_max:'2',slot_revision_min:t.slot_revision,slot_revision_max:t.slot_revision};});
 return {target:t,now:n,window_start:start,view:'1day',resolution_ms:M,algorithm:'weighted-grid-rtt-adjacent-v3',histogram_version:1,grid_ms:.25,percentile_max_error_ms:.125,groups:[{semantics:'staged_probe_v2',metric:t.protocol==='tcp'?'pure_connect':'echo_rtt',points}]};}
before(async()=>{
 if(!available)return;
 server=http.createServer((req,res)=>{const u=new URL(req.url,'http://local');requests.push(req.url);const json=(v,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(v));};
  if(u.pathname==='/api/v1/web/events'){res.writeHead(200,{'Content-Type':'text/event-stream'});res.write(': local fixture\n\n');return;}
  if(req.method==='POST'){let b='';req.on('data',c=>b+=c);req.on('end',()=>{posts.push({path:u.pathname,body:JSON.parse(b)});json({error:{code:'switch_failed',message:'local synthetic rejection'}},409);});return;}
  if(u.pathname.endsWith('/session'))return json({csrf_token:'synthetic-local'});
  if(u.pathname.endsWith('/version'))return json({version:'v1.0.8',upgrade_eligible:true});
  if(u.pathname==='/api/v1/web/dashboard')return json(dashboardFixture(agent()));
  if(u.pathname==='/api/v1/web/agents')return json({items:[agent()]});
  if(u.pathname.endsWith('/jobs'))return json({items:[]});
  if(u.pathname.endsWith('/v4/config'))return json(config());
  if(u.pathname.endsWith('/v5/card'))return json(summary());
  if(u.pathname.endsWith('/resources/history'))return json(resources(u.searchParams));
  if(u.pathname.endsWith('/v3/history'))return json(history(u.searchParams.get('target_id')));
  if(u.pathname===`/api/v1/web/agents/${ID}`)return json(agent());
  if(u.pathname.startsWith('/api/'))return json({},404);
  const file=path.resolve(root,'.'+(u.pathname==='/'?'/index.html':u.pathname));if(!file.startsWith(root+path.sep)||!fs.existsSync(file)){res.writeHead(404);return res.end();}
  res.setHeader('Content-Type',file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.svg')?'image/svg+xml':file.endsWith('.html')?'text/html':'application/octet-stream');res.end(fs.readFileSync(file));
 });await new Promise(r=>server.listen(0,'127.0.0.1',r));base=`http://127.0.0.1:${server.address().port}`;
 browser=await chromium.launch({headless:true,executablePath:chromiumPath});
});
after(async()=>{await browser?.close();if(server){server.closeAllConnections();await new Promise(r=>server.close(r));}});
async function open(s='normal',width=1000){scenario=s;const page=await browser.newPage({viewport:{width,height:800},deviceScaleFactor:2});const errors=[];
 page.on('pageerror',e=>errors.push(e.message));await page.route('**/*',route=>route.request().url().startsWith(base)?route.continue():route.abort());
 await page.goto(base);await page.waitForSelector('.nq7-row');if(s!=='real')await page.waitForFunction(()=>document.querySelector('.c7-rate')?.textContent.includes('369'));await page.waitForTimeout(200);
 // Two design-width cards fit on the page; screenshot a single 400px production card.
 await page.addStyleTag({content:'.agent-grid,.dashboard-card-grid{grid-template-columns:400px;justify-content:center}'});
 return {page,errors};}
async function shot(page,name,selector='.agent-card'){if(!process.env.R33_SCREEN_DIR)return;fs.mkdirSync(process.env.R33_SCREEN_DIR,{recursive:true});const opts={path:path.join(process.env.R33_SCREEN_DIR,name+'.png')};if(selector==='body')await page.screenshot(opts);else await page.locator(selector).screenshot(opts);}
test('R33 actual dashboard: A/B heights, filled day plot and full-width traffic plot; screenshots',browserOptions,async()=>{
 const {page,errors}=await open();try{
  await shot(page,'A-normal');await page.click('.c7-flip');
  await page.waitForFunction(()=>document.querySelector('.tc7-spark svg path'));
  const facts=await page.evaluate(()=>{const box=s=>{const r=document.querySelector(s).getBoundingClientRect();return {x:r.x,width:r.width,height:r.height};};const n=box('.lm-plot');return {heights:['machine','traffic','network'].map(m=>box(`[data-module=${m}]`).height),traffic:box('.tc7-plot'),trafficFrame:box('.tc7'),trafficAxes:document.querySelectorAll('.tc7-axis').length,network:{x:n.x+32,width:n.width-32},fill:document.querySelectorAll('.tc7-rx-fill,.tc7-tx-fill').length,marks:document.querySelectorAll('.lm7-marks,.rs-mark').length};});
  assert.deepEqual(facts.heights,[100,120,164]);assert.equal(facts.traffic.x,facts.trafficFrame.x);assert.equal(facts.traffic.width,facts.trafficFrame.width);assert.equal(facts.trafficAxes,0);assert.equal(facts.fill,2);assert.equal(facts.marks,0);
  assert.ok(requests.some(u=>u.includes('resources/history?hours=24')));assert.ok(!requests.some(u=>u.includes('/v5/marks')));
  await shot(page,'B-normal');
  await page.click('[data-focus-key=lm-icmp]');await page.waitForFunction(()=>[...document.querySelectorAll('.lm-loss')].every(n=>n.textContent.includes('丢包 16.7%')));
  assert.equal(await page.locator('.lm-loss:visible').count(),3);
  await shot(page,'B-icmp');
  await page.click('[data-focus-key=lm-tcp]');assert.equal(await page.locator('.lm-loss:visible').count(),0);
  if(process.env.R33_SCREEN_DIR)fs.writeFileSync(path.join(process.env.R33_SCREEN_DIR,'facts.json'),JSON.stringify(facts,null,2));
  assert.deepEqual(errors,[]);
 }finally{await page.close();}
});
test('R33 special states, hover/title and native outbound dialog: draft, per-group submit, focus, backdrop, scroll',browserOptions,async()=>{
 const {page,errors}=await open('special');try{
  await page.waitForFunction(()=>document.querySelectorAll('.nq7-b-red').length>20);
  assert.equal(await page.locator('.nq7-row').nth(1).locator('.nq7-right').textContent(),'—');
  assert.match(await page.locator('.c7-quota-empty').textContent(),/未设置流量配额/);
  assert.equal(await page.locator('.nq7-right').evaluateAll(nodes=>nodes.every(n=>n.scrollWidth<=n.clientWidth)),true,'56px column must fit fractional failure percentages');
  assert.equal(await page.locator('.nq7-right').first().evaluate(n=>{const old=n.textContent;n.textContent='失败 33.3%';const fits=n.scrollWidth<=n.clientWidth;n.textContent=old;return fits;}),true);
  await page.locator('.nq7-row').first().locator('.nq7-b-red').first().hover();
  await page.waitForSelector('.nq7-tip:not([hidden])');assert.match(await page.locator('.nq7-b-red').first().getAttribute('title'),/门槛 500 ms/);
  assert.equal(await page.locator('.nq7-tip').textContent(),await page.locator('.nq7-row').first().locator('.nq7-b-red').first().getAttribute('title'),'hover wording is unchanged');
  assert.equal(await page.locator('.nq7-tip strong').evaluate(n=>getComputedStyle(n).fontWeight),'700');
  assert.equal(await page.locator('.nq7-tip span').evaluate(n=>getComputedStyle(n).fontWeight),'400');
  await shot(page,'A-special-tooltip');await page.mouse.move(0,0);
  await page.click('.selector-trigger');await page.waitForSelector('dialog.selector-dialog[open]');
  assert.equal(await page.locator('.selector-device').textContent(),'DediRock-LAX-1');
  assert.equal(await page.locator('#selector-dialog-title').textContent(),'出站选择');
  assert.equal(await page.locator('.selector-options select').first().inputValue(),'直连');
  assert.equal(await page.locator('.selector-options select').nth(1).inputValue(),'备用');
  assert.equal(await page.locator('.selector-actions button').textContent(),'关闭');
  const switches=page.locator('[data-selector-kind=submit]');assert.equal(await switches.count(),2);
  assert.equal(await switches.first().isDisabled(),true);assert.equal(await switches.nth(1).isDisabled(),true);
  await shot(page,'outbound-dialog','body');
  const before=posts.length;await page.locator('.selector-options select').first().selectOption('WARP');assert.equal(posts.length,before);
  assert.equal(await switches.first().isDisabled(),false);assert.equal(await switches.nth(1).isDisabled(),true);
  await shot(page,'outbound-dialog-draft','body');
  const p1=await page.locator('.selector-dialog').boundingBox();await page.evaluate(()=>{document.body.style.minHeight='2000px';window.scrollTo(0,500);});const p2=await page.locator('.selector-dialog').boundingBox();assert.equal(p1.y,p2.y);
  await page.keyboard.press('Escape');assert.equal(await page.locator('.selector-trigger').evaluate(e=>e===document.activeElement),true);
  await page.click('.selector-trigger');await page.mouse.click(2,2);assert.equal(await page.locator('.selector-dialog').evaluate(e=>e.open),false);
  await page.click('.selector-trigger');await page.locator('.selector-options select').first().selectOption('WARP');await switches.first().click();
  await page.waitForFunction(()=>document.querySelector('.selector-error'));
  assert.equal(posts.length,before+1);assert.equal(posts.at(-1).body.selector,'出站主组');assert.equal(posts.at(-1).body.choice,'WARP');
  assert.deepEqual(errors,[]);
 }finally{await page.close();}
});

test('400px card: long outbound names never truncate YouTube region; many choices keep two compact rows',browserOptions,async()=>{
 const {page,errors}=await open('long-outbound');try{
  assert.equal(await page.locator('.selector-trigger').textContent(),'出站选择 ⌄');
  assert.match(await page.locator('.selector-trigger').getAttribute('title'),/very-long-outbound-tag/);
  assert.equal(await page.locator('.selector-trigger svg').getAttribute('stroke'),'currentColor');
  const layout=await page.evaluate(()=>{const card=document.querySelector('.agent-card').getBoundingClientRect(),label=document.querySelector('.c7-foot1 .check-result'),r=label.getBoundingClientRect(),b=document.querySelector('.selector-trigger').getBoundingClientRect();return {cardWidth:card.width,text:label.textContent,fits:label.scrollWidth<=label.clientWidth,labelRight:r.right,buttonLeft:b.left,buttonRight:b.right,cardRight:card.right};});
  assert.equal(layout.cardWidth,400);assert.match(layout.text,/地区 US/);assert.equal(layout.fits,true);assert.ok(layout.labelRight<=layout.buttonLeft);assert.ok(layout.buttonRight<=layout.cardRight);
  await shot(page,'400-long-outbound');await page.click('.selector-trigger');
  assert.equal(await page.locator('.selector-row').count(),2);assert.equal(await page.locator('.selector-row select option').count(),202);assert.equal(await page.locator('input[type=radio]').count(),0);
  const dialogRect=await page.locator('.selector-dialog').boundingBox();assert.equal(dialogRect.width,560);assert.ok(dialogRect.height<500);
  for(let i=0;i<2;i++)await page.locator('.selector-row select').nth(i).selectOption('出站-3');
  const before=posts.length;await page.locator('[data-selector-kind=submit]').nth(1).click();await page.waitForSelector('.selector-error');
  assert.equal(posts.length,before+1);assert.equal(posts.at(-1).body.selector,'出站备用组');assert.equal(posts.at(-1).body.choice,'出站-3');
  assert.equal(await page.locator('.selector-row select').first().inputValue(),'出站-3');assert.deepEqual(errors,[]);
 }finally{await page.close();}
});

test('one outbound group uses the same labelled select row and disabled initial switch',browserOptions,async()=>{
 const {page,errors}=await open('single-outbound');try{
  await page.click('.selector-trigger');assert.equal(await page.locator('.selector-row').count(),1);
  assert.equal(await page.getByLabel('出站主组',{exact:true}).inputValue(),'直连');assert.equal(await page.locator('[data-selector-kind=submit]').isDisabled(),true);
  assert.equal(await page.locator('.selector-actions button').textContent(),'关闭');assert.deepEqual(errors,[]);
 }finally{await page.close();}
});
test('R33 real local measurements screenshot (optional sample path)',{skip:!available||!process.env.R33_REAL_SAMPLE},async()=>{
 const {page,errors}=await open('real');try{await shot(page,'real-local');assert.deepEqual(errors,[]);}finally{await page.close();}
});

// R09 persistent summary and nested lifecycle use actual production modules.
test('R09 overview/management preserve the fixed frame and state labels',browserOptions,async()=>{
 const {page,errors}=await open();try{
  await page.locator('.detail-toggle').click();await page.waitForSelector('#details-pane-overview .details-metrics');
  const before=await page.locator('#device-details').boundingBox();assert.equal(before.width,960);assert.equal(before.height,680);
  await page.locator('#details-tab-management').click();assert.equal((await page.locator('#device-details').boundingBox()).height,680);
  assert.match(await page.locator('#details-pane-management').innerText(),/随上报自动更新/);
  assert.match(await page.locator('#details-pane-management').innerText(),/撤销 Agent/);
  await page.locator('#details-tab-resources').click();await page.waitForSelector('.resource-wide');const stats=await page.locator('.resource-wide .details-note').first().innerText();assert.doesNotMatch(stats,/\/s\/s/);assert.doesNotMatch(stats,/按每分钟平均值/);assert.match(await page.locator('.details-history-status').getAttribute('title'),/按每分钟平均值/);
  await page.keyboard.press('Escape');assert.equal(await page.locator('#device-details').evaluate(n=>n.open),false);assert.deepEqual(errors,[]);
 }finally{await page.close();}
});


test('R44 production homepage at desktop/390 keeps the full-width traffic face readable',browserOptions,async()=>{
 for(const width of [1440,390]){
  const {page,errors}=await open('normal',width);try{
   await page.evaluate(()=>{for(const style of document.querySelectorAll('style'))if(style.textContent.includes('.agent-grid,.dashboard-card-grid{grid-template-columns:400px'))style.remove();});
   await page.click('.c7-flip');await page.waitForFunction(()=>document.querySelector('.tc7-rx'));
   const facts=await page.evaluate(()=>{const rect=s=>{const r=document.querySelector(s).getBoundingClientRect();return {left:r.left,right:r.right,width:r.width};};return {frame:rect('.tc7'),plot:rect('.tc7-plot'),label:rect('.tc7-label'),peak:rect('.tc7-peak'),axes:document.querySelectorAll('.tc7-axis').length,overflow:document.documentElement.scrollWidth>innerWidth};});
   assert.equal(facts.axes,0);assert.ok(Math.abs(facts.frame.width-facts.plot.width)<1);assert.ok(facts.label.right<=facts.peak.left+1);assert.equal(facts.overflow,false);assert.deepEqual(errors,[]);
   if(process.env.R33_SCREEN_DIR){await page.evaluate(()=>document.fonts.ready);await page.screenshot({path:path.join(process.env.R33_SCREEN_DIR,'homepage-after-'+width+'.png'),fullPage:true});}
  }finally{await page.close();}
 }
});


test('updater diagnostics occur once with one version-bound link, including already-latest and legacy fallback',browserOptions,async()=>{
 const {page,errors}=await open();try{
  const rows=await page.evaluate(()=>{
   return [
    {health:{status:'identity_mismatch'},code:'updater_identity_mismatch',version:'v1.0.7'},
    {health:{status:'identity_mismatch'},code:'already_latest',version:'v1.0.8'},
    {health:null,code:'updater_unavailable',version:'v1.0.7'},
   ].map(({health,code,version})=>{
    const div=()=>document.createElement('div'),button=()=>document.createElement('button');
    const summary=DetailSummary.create({actions:{upgrade:button(),reset:button(),resetFeedback:div(),google:button(),googleFeedback:div(),pause:button(),remove:button(),history:button()},egress:{addresses:div(),controls:div()},country:div(),canWrite:()=>false});
    summary.update({agent_id:'a'.repeat(32),name:'fixture',online:true,version,state:{},updater_health:health},{version:'v1.0.8',supported:false,reason_code:code},{version:'v1.0.8'});
    const row=summary.management.querySelector('.details-action-row');
    return {code,text:row.textContent,links:[...row.querySelectorAll('a')].map(a=>a.href)};
   });
  });
  for(const row of rows){assert.equal((row.text.match(/升级按钮已停用/g)||[]).length,1,row.text);assert.deepEqual(row.links,['https://github.com/404-git-404/404-probe/blob/v1.0.8/docs/updater-troubleshooting.md']);}
  assert.match(rows[1].text,/无需升级/);assert.deepEqual(errors,[]);
 }finally{await page.close();}
});
