// Real loopback Server/isolated SQLite supplies auth and embedded assets. Browser routes supply synthetic telemetry/jobs.
// No real credentials or outbound HTTP. V109_SERVER points to a locally built executable.
'use strict';
const fs=require('node:fs'),path=require('node:path'),os=require('node:os'),cp=require('node:child_process'),http=require('node:http'),crypto=require('node:crypto'),assert=require('node:assert/strict');
const {chromium}=require(process.env.M02_PLAYWRIGHT||'C:/Users/ufoli/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');
const {load,v5Card}=require('./lib/r07-harness.cjs');
const exe=process.env.V109_SERVER,out=process.env.V109_SCREENS;
assert(exe&&out,'Set V109_SERVER and V109_SCREENS');fs.mkdirSync(out,{recursive:true});
const tmp=fs.mkdtempSync(path.join(os.tmpdir(),'v109-ui-')),db=path.join(tmp,'isolated.sqlite'),hash=path.join(tmp,'password.hash'),password=crypto.randomBytes(24).toString('hex'),ID='a'.repeat(32),DAY=86400000,M=60000;
let server,browser,mode='success',gate,job=null,jobNumber=0,posts=[],errors=[],keyboard=[];
const delay=ms=>new Promise(r=>setTimeout(r,ms));
const sampleTime=Date.now(),region='US',name='DediRock-LAX-1 · 多出站组与超长设备名称验证 · 生产式合成样本';
function agent(){return {agent_id:ID,name,created_at:sampleTime-60*DAY,online:true,last_seen:Date.now()-3000,version:'v1.0.7',upgrade_capable:true,upgrade_v2:true,revoked:false,disabled_at:null,country_code:region,country_source:'automatic',management:{country_code_lookup:true},
 state:{os:'Debian 13.6',arch:'amd64',hostname:'dedirock-synthetic-long-hostname',cpu_percent:1.2,cpu_cores:1,cpu_steal_percent:.2,load1:.05,load5:.03,load15:.01,ram_used:206*2**20,ram_total:1.44*2**30,ram_percent:14,swap_total:1.5*2**30,swap_used:0,disk_total:24.5*2**30,disk_used:4.49*2**30,disk_percent:18,disk_read_rate:0,disk_write_rate:38.8*1024,disk_busy_percent:0,rx_rate:1750,tx_rate:1140,uptime:54*86400,collected_at:sampleTime,public_egress:{ipv4:{status:'available',ip:'203.0.113.9',checked_at:sampleTime-DAY,valid_until:sampleTime+6*DAY},ipv6:{status:'available',ip:'2001:db8:abcd:1234:5678:90ab:cdef:1234',checked_at:sampleTime-DAY,valid_until:sampleTime+6*DAY}}},
 traffic:{rx_total:79.3e9,tx_total:79.1e9,started_at:sampleTime-27*DAY},public_egress_check:{enabled:true,supported:true,pending:false,next_check_at:sampleTime+6*DAY},google_status:{supported:true,pending:false,checked_at:sampleTime-DAY,result:{youtube:{status:'not_cn',region:'US'}}},
 plan:{currency:'USD',renewal_price:'6.85',renewal_price_period:'yearly',renewal_date:'2026-11-28',timezone:'America/Los_Angeles',bandwidth_value:'1',bandwidth_unit:'Gbps',traffic_mode:'sum',quota_bytes:5e12,quota_value:'5',quota_unit:'TB',cycle_kind:'monthly',cycle_count:1,cycle_anchor:'2026-09-28',cycle_start:sampleTime-27*DAY,cycle_end:sampleTime+4*DAY,cycle_rx_bytes:20e9,cycle_tx_bytes:16.6e9,usage_bytes:36.6e9,usage_status:'partial'},
 security:{supported:true,status:'partial',updated_at:sampleTime,current:{status:'partial',reason:'source_limit',window_start:sampleTime-DAY,window_end:sampleTime,collected_at:sampleTime-60000,total_events:729,tracked_sources:154,dropped_sources:54,sources:Array.from({length:100},(_,i)=>({ip:'198.51.100.'+(i+1),count:i<8?[291,21,20,16,16,13,11,11][i]:3,first_seen:sampleTime-DAY+i*M,last_seen:sampleTime-60000}))}},
 outbounds:{configured:true,available:true,status:'connected',stale:false,order_source:'config',selectors:[{name:'YouTube',current:'LAX-默认线路',choices:['LAX-默认线路','LAX-备用线路']},{name:'漏网 · 很长的出站分组名称用于换行和键盘验证',current:'自动出口',choices:['自动出口','备用出口']},{name:'其他服务',current:'DIRECT',choices:['DIRECT','PROXY']}]}};}
let current=agent();
function history(query){const now=Date.now(),to=query.has('to')?+query.get('to'):now,from=query.has('from')?+query.get('from'):to-(+query.get('hours')||1)*3600000,lower=Math.floor(from/M)*M,upper=Math.floor(to/M)*M,points=[];
 for(let t=lower;t<=upper;t+=M){const i=Math.floor(t/M);if(i%127<3)continue;points.push({timestamp:t,cpu:1.2+Math.abs(Math.sin(i*.2))*7,ram_percent:14+3*Math.sin(i*.02),swap_percent:0,disk_percent:18,load1:.05,load5:.03,load15:.01,rx_rate:1000+Math.abs(Math.sin(i*.08))*4000,tx_rate:500+Math.abs(Math.cos(i*.1))*2500,rx_total:i*1000000,tx_total:i*700000});}
 return {agent_id:ID,server_now:now,from,to,bucket_from:lower,bucket_to:upper,interval_ms:M,source:'minute_metrics',aggregation:'arithmetic_mean',retention_days:30,coverage:'partial',point_limit:1441,oldest_timestamp:points[0]?.timestamp??null,last_timestamp:points.at(-1)?.timestamp??null,points};}
async function run(){
 fs.writeFileSync(hash,cp.execFileSync(exe,['web','password-hash'],{input:password+'\n',windowsHide:true}));
 const portServer=http.createServer();await new Promise(r=>portServer.listen(0,'127.0.0.1',r));const port=portServer.address().port;await new Promise(r=>portServer.close(r));const base='http://127.0.0.1:'+port;
 server=cp.spawn(exe,['serve','--listen','127.0.0.1:'+port,'--db',db,'--web-password-hash-file',hash,'--web-public-origin',base,'--web-allow-insecure-http'],{windowsHide:true,stdio:'ignore'});
 for(let i=0;;i++){try{await fetch(base+'/login');break;}catch{if(i>=100)throw Error('Server startup timeout');await delay(50);}}
 browser=await chromium.launch({headless:true,executablePath:process.env.M02_CHROMIUM||'C:/Users/ufoli/AppData/Local/ms-playwright/chromium-1228/chrome-win64/chrome.exe'});
 const page=await browser.newPage({viewport:{width:1200,height:900}});page.setDefaultTimeout(10000);page.on('pageerror',e=>errors.push(e.message));
 await page.route('**/*',async route=>{
  const u=new URL(route.request().url());if(u.origin!==base)return route.abort();const pathname=u.pathname,method=route.request().method(),body=route.request().postDataJSON?.();
  const json=(v,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(v)});
  if(!pathname.startsWith('/api/')||pathname.endsWith('/session')||pathname.endsWith('/events'))return route.continue();
  if(method!=='GET')posts.push({path:pathname,method,body});
  if(pathname.endsWith('/version'))return json({version:'v1.0.8',upgrade_eligible:true,one_click_install:true});
  if(pathname==='/api/v1/web/agents')return json({items:[current]});
  if(pathname.endsWith('/upgrade-targets'))return json({targets:[{version:'v1.0.8',channel:'stable',supported:true}]});
  if(pathname.endsWith('/upgrade'))return method==='GET'?json(null):json({status:'requested',target_version:'v1.0.8'});
  if(pathname.endsWith('/v4/config'))return json(load('v4-running'));
  if(pathname.endsWith('/v5/card'))return json(v5Card(load('v4-running'),u.searchParams.get('family')||'ipv4',Date.now()));
  if(pathname.endsWith('/resources/history'))return json(history(u.searchParams));
  if(pathname.endsWith('/traffic/recent'))return json({},404);
  if(pathname.endsWith('/jobs'))return json({items:[]});
  if(pathname.endsWith('/outbounds/switch')){job={id:'b'.repeat(32),selector:body.selector,choice:body.choice};jobNumber++;return json({job_id:job.id});}
  if(pathname.includes('/jobs/')){if(mode==='pending')await new Promise(r=>{gate=r;});if(mode==='failure')return json({operation_status:'failed',result:{success:false,error_category:'switch_failed'}});
   if(mode==='success'){current.outbounds.selectors.find(s=>s.name===job.selector).current=job.choice;}return json({operation_status:'success',result:{success:true,measurement:{selector_switch:{current:job.choice}}}});}
  if(pathname.endsWith('/plan')&&method==='PUT'){current.plan={...current.plan,...body};return json(current.plan);}
  if(pathname===`/api/v1/web/agents/${ID}`)return json(current);
  return json({},404);
 });
 await page.goto(base+'/login');await page.locator('#password').fill(password);await page.locator('button[type=submit]').click();await page.waitForSelector('.detail-toggle');
 const shot=async(file,selector='#device-details')=>{await page.locator(selector).screenshot({path:path.join(out,file+'.png')});};
 const tabs=async key=>{await page.locator('#details-tab-'+key).click();await page.waitForTimeout(80);};
 const openDetails=async()=>{if(!await page.locator('#device-details').evaluate(n=>n.open))await page.locator('.detail-toggle').click();};
 await openDetails();await shot('details-overview');assert.deepEqual(await page.locator('.details-tabs button').allTextContents(),['概览','资源','安全','管理']);
 const rect=await page.locator('#device-details').boundingBox();assert.equal(rect.width,960);assert.equal(rect.height,680);
 await tabs('resources');await page.waitForSelector('.resource-chart');assert.equal(await page.locator('.resource-chart').count(),3);await shot('details-resources');
 await page.getByRole('button',{name:'7 天',exact:true}).click();await page.waitForFunction(()=>deviceDetails.snapshot().points>9000);await shot('details-resources-seven-days');
 await tabs('security');await shot('details-security');await page.getByRole('button',{name:'显示全部已记录来源（100 个）'}).click();assert.equal(await page.locator('.details-security-row:visible').count(),101);
 await tabs('management');await shot('details-management');
 const setState=async patch=>{current={...current,...patch};await page.evaluate(a=>{agents.set(a.agent_id,a);render();},current);};
 for(const [state,patch]of [['online',{online:true,disabled_at:null}],['offline',{online:false}],['paused',{online:true,disabled_at:sampleTime}]]){await setState(patch);await shot('header-'+state,'.details-header');}
 await setState({online:true,disabled_at:null});
 const modalCheck=async(label,open,selector)=>{console.log('checking '+label);await open();const d=page.locator(selector);const expectedWidth=['upgrade','revoke','pause','reset','country'].includes(label)?420:560;assert.equal((await d.boundingBox()).width,expectedWidth,label+' width');assert.equal(await page.evaluate(()=>document.body.style.overflow),'hidden',label+' scroll lock');await d.waitFor({state:'visible'});await shot('dialog-'+label,selector);
  assert.equal(await d.evaluate(n=>n.contains(document.activeElement)),true,label+' initial focus');
  for(let i=0;i<12;i++){await page.keyboard.press('Tab');assert.equal(await d.evaluate(n=>n.contains(document.activeElement)),true,label+' Tab');}
  await page.keyboard.press('Escape');await page.waitForTimeout(40);assert.equal(await page.evaluate(selector=>Boolean(document.querySelector(selector)?.open),selector),false,label+' Esc');
  const focus=await page.evaluate(()=>({tag:document.activeElement?.tagName,visible:!!document.activeElement?.getClientRects().length,inside:document.querySelector('#device-details')?.contains(document.activeElement)}));assert.ok(focus.visible,label+' restored visible focus');const expected={upgrade:'.upgrade-agent',revoke:'.remove-agent',pause:'.agent-state-action',reset:'[data-focus-key=traffic-reset]',country:'.country-lookup-controls button','add-agent':'#add-agent',outbound:'.selector-trigger'}[label];assert.equal(await page.locator(expected).evaluate(n=>n===document.activeElement),true,label+' exact trigger focus');keyboard.push({label,tab:true,esc:true,focus});
  await open();const r=await d.boundingBox();await page.mouse.move(2,2);await page.mouse.down();await page.mouse.move(r.x+r.width/2,r.y+r.height/2);await page.mouse.up();assert.equal(await d.evaluate(n=>n.open),true,label+' mixed gesture');await page.mouse.click(2,2);await page.waitForTimeout(40);assert.equal(await page.evaluate(selector=>Boolean(document.querySelector(selector)?.open),selector),false,label+' backdrop');keyboard.at(-1).backdrop=true;
 };
 await modalCheck('upgrade',()=>page.locator('#details-pane-management .upgrade-agent').click(),'#upgrade-agent-dialog');
 await modalCheck('revoke',()=>page.locator('#details-pane-management .remove-agent').click(),'#remove-agent-dialog');
 await modalCheck('pause',()=>page.locator('#details-pane-management .agent-state-action').click(),'#action-confirm');
 await modalCheck('reset',()=>page.locator('#details-pane-management [data-focus-key=traffic-reset]').click(),'#action-confirm');
 await modalCheck('country',()=>page.locator('#details-pane-management .country-lookup-controls button').click(),'#action-confirm');
 assert.equal(posts.length,0,'merely opening/closing dialogs never writes');
 await page.keyboard.press('Escape');await modalCheck('add-agent',()=>page.locator('#add-agent').click(),'#add-agent-dialog');assert.equal(posts.length,0);
 await openDetails();await tabs('overview');await page.locator('.details-addresses .pe-eye').click();assert.match(await page.locator('.details-addresses').innerText(),/203.0.113.9/);await page.keyboard.press('Escape');await openDetails();assert.doesNotMatch(await page.locator('.details-addresses').innerText(),/203.0.113.9/);
 current.plan={...current.plan,quota_bytes:null,quota_value:'',quota_unit:''};await setState({plan:current.plan});await shot('overview-unlimited');assert.equal(await page.locator('.details-progress').isVisible(),false);
 await page.setViewportSize({width:390,height:844});await shot('mobile-overview');await tabs('management');await shot('mobile-management');assert.equal(await page.locator('.details-close').isVisible(),true);assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=390),true);
 await page.keyboard.press('Escape');await page.setViewportSize({width:1200,height:900});
 await page.locator('.selector-trigger').click();assert.equal(await page.locator('.selector-row').count(),3);await shot('outbound-idle','#selector-popover');
 const choose=page.locator('[data-selector-kind=choice]').first(),submit=page.locator('[data-selector-kind=submit]').first();
 mode='pending';await choose.selectOption('LAX-备用线路');await submit.click();await page.waitForFunction(()=>selectorController.model('a'.repeat(32)).active);await shot('outbound-pending','#selector-popover');
 mode='success';gate?.();await page.getByText('✓ 已切换',{exact:true}).waitFor();await shot('outbound-success','#selector-popover');assert.equal(jobNumber,1);await page.waitForTimeout(3100);assert.equal(await page.getByText('✓ 已切换',{exact:true}).count(),0);
 mode='failure';await choose.selectOption('LAX-默认线路');await submit.click();await page.locator('.selector-error').waitFor();await shot('outbound-failure','#selector-popover');await choose.selectOption('LAX-备用线路');assert.equal(await page.locator('.selector-error').count(),0);
 mode='waiting';await choose.selectOption('LAX-默认线路');await submit.click();await page.getByText('已完成，等待快照确认',{exact:true}).waitFor();await shot('outbound-waiting','#selector-popover');current.outbounds.selectors[0].current='LAX-默认线路';await setState({outbounds:current.outbounds});assert.equal(await page.getByText('已完成，等待快照确认',{exact:true}).count(),0);assert.equal(jobNumber,3);
 await page.keyboard.press('Escape');await modalCheck('outbound',()=>page.locator('.selector-trigger').click(),'#selector-popover');
 const rows=page.locator('.nq7-row');assert.equal(await rows.first().evaluate(n=>n.tagName),'DIV');await rows.first().click();assert.equal(await page.locator('#device-details').evaluate(n=>n.open),false);
 await openDetails();await tabs('management');await page.locator('#details-pane-management .upgrade-agent').click();await page.evaluate(id=>{agents.delete(id);render();},ID);assert.equal(await page.evaluate(()=>document.body.style.overflow),'hidden');await page.keyboard.press('Escape');assert.equal(await page.evaluate(()=>document.body.style.overflow),'');assert.equal(await page.locator('#add-agent').evaluate(n=>n===document.activeElement),true,'removed origin falls back to visible button');
 assert.deepEqual(errors,[]);if(fs.existsSync(path.join(out,'failure.txt')))fs.unlinkSync(path.join(out,'failure.txt'));fs.writeFileSync(path.join(out,'checks.json'),JSON.stringify({boundary:'Real loopback Server + isolated SQLite for auth/assets; browser routes supply synthetic data and mutation receipts',keyboard,posts:posts.map(p=>({path:p.path,method:p.method})),jobNumber,errors},null,2));console.log('R09/R38 browser acceptance passed; '+keyboard.length+' dialog keyboard checks');
}
const watchdog=setTimeout(()=>{console.error('acceptance exceeded 150s');process.exit(2);},150000);
run().catch(e=>{fs.writeFileSync(path.join(out,'failure.txt'),e.stack);console.error(e.stack);process.exitCode=1;}).finally(async()=>{clearTimeout(watchdog);gate?.();await browser?.close();if(server&&server.exitCode===null){server.kill();await new Promise(r=>server.once('exit',r));}fs.rmSync(tmp,{recursive:true,force:true});});
