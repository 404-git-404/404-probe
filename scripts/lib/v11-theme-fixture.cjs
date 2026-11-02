// Theme QA uses production pages and loopback-only synthetic responses.
const fs=require('node:fs'),path=require('node:path');
const {start,ID}=require('./v110-dashboard-fixture.cjs');
const {load,C,v5Card}=require('./r07-harness.cjs');
const NOW=Date.UTC(2026,10,2,12),repo=path.resolve(__dirname,'../..');
function resources(id,hours=24){
 const end=NOW,from=end-hours*3600000,interval=hours>24?3600000:60000;
 const length=Math.min(121,Math.floor((end-from)/interval)+1),points=Array.from({length},(_,i)=>({timestamp:end-(length-1-i)*interval,cpu:10+(i%30),ram_percent:40+i%15,swap_percent:0,disk_percent:30,load1:.1,load5:.2,load15:.3,rx_rate:20000+i%17*2000,tx_rate:10000+i%13*1000}));
 return {agent_id:id,server_now:NOW,from,to:end,bucket_from:from,bucket_to:end,interval_ms:interval,source:hours>24?'hourly_metrics':'minute_metrics',aggregation:'arithmetic_mean',retention_days:30,coverage:'partial',point_limit:1441,oldest_timestamp:points[0]?.timestamp,last_timestamp:points.at(-1)?.timestamp,points};
}
async function open(browser,{source=repo,width=1440,height=1100,theme='light',n=8,prepare=null}={}){
 const previous=Date.now;Date.now=()=>NOW;const f=await start({n});Date.now=previous;
 f.state.items[0].outbounds.selectors.push({name:'Global',current:'出口 B',choices:['出口 A','出口 B']});
 if(f.state.items[6]){f.state.items[6].plan=null;f.state.items[6].renewal={type:'unset'};}
 const raw=load('v4-running');raw.slots.forEach((s,i)=>{s.kind='custom';s.region=['上海','广州','北京'][i];s.isp=['电信','联通','移动'][i];});
 const adapted=C.adapt(raw),targets=adapted.targets;
 const context=await browser.newContext({viewport:{width,height},deviceScaleFactor:1,locale:'zh-CN',timezoneId:'America/Los_Angeles',colorScheme:'light'}),page=await context.newPage(),errors=[];
 page.setDefaultTimeout(12000);page.on('pageerror',e=>errors.push(e.message));
 await page.clock.setFixedTime(NOW);
 if(theme)await page.addInitScript(theme=>{if(localStorage.getItem('404-probe.theme.v1')===null)localStorage.setItem('404-probe.theme.v1',theme);},theme);
 const staticRoot=path.join(source,'web/static');
 await context.route('**/*',async r=>{
  const u=new URL(r.request().url());if(u.origin!==f.base)return r.abort();
  const json=data=>r.fulfill({json:data});
  if(u.pathname==='/login'){
   const go=fs.readFileSync(path.join(source,'internal/server/web_auth.go'),'utf8');
   const html=go.match(/Parse\(`([\s\S]*?)`\)\)/)[1].replace(/\{\{if .*?\}\}[\s\S]*?\{\{end\}\}/g,'').replace('{{.CSRFToken}}','synthetic-only');
   return r.fulfill({contentType:'text/html',body:html});
  }
  if(!u.pathname.startsWith('/api/')){
   const p=path.resolve(staticRoot,'.'+(u.pathname==='/'?'/index.html':u.pathname));
   if(!p.startsWith(staticRoot+path.sep)||!fs.existsSync(p))return r.fulfill({status:404});
   return r.fulfill({body:fs.readFileSync(p),contentType:p.endsWith('.js')?'text/javascript':p.endsWith('.css')?'text/css':p.endsWith('.svg')?'image/svg+xml':p.endsWith('.png')?'image/png':'text/html'});
  }
  if(u.pathname==='/api/v1/web/version')return json({version:'v1.0.10',upgrade_eligible:true,one_click_install:true});
  if(u.pathname==='/api/v1/web/agents')return json({items:f.state.items,next_cursor:''});
  if(u.pathname==='/api/v1/web/jobs')return json({items:[{job_id:ID(800),agent_id:ID(0),probe_type:'tcp_connect',operation_status:'success',created_at:NOW-60000,attempt:1,result_summary:{success:true,duration_ms:21}}],next_cursor:''});
  if(u.pathname.startsWith('/api/v1/web/schedules')){const item={schedule_id:ID(801),agent_id:ID(0),name:'合成 TCP 检测',probe_type:'tcp_connect',enabled:true,interval_seconds:300,timeout_ms:5000,next_run_at:NOW+300000,updated_at:NOW,config:{host:'example.invalid',port:443}};return json(u.pathname.endsWith('/schedules')?{items:[item],next_cursor:''}:item);}
  if(u.pathname.endsWith('/resources/history'))return json(resources(u.pathname.split('/')[5],Number(u.searchParams.get('hours')||24)));
  if(/\/agents\/[^/]+\/history$/.test(u.pathname))return json(resources(ID(0)));
  if(u.pathname.endsWith('/v4/config'))return json(raw);
  if(u.pathname.endsWith('/v5/card'))return json(v5Card(raw,u.searchParams.get('family')||'ipv4',NOW));
  if(u.pathname.endsWith('/v3/history')){
   const value=load(u.searchParams.get('view')==='1day'?'history-1day-tcp':'history-card-tcp'),target=targets.find(t=>t.id===u.searchParams.get('target_id'))||targets[0];
   // Keep bucket boundaries aligned while bringing historical fixture data to our clock.
   const offset=Math.floor((NOW-value.now)/60000)*60000;
   const shift=x=>Array.isArray(x)?x.map(shift):x&&typeof x==='object'?Object.fromEntries(Object.entries(x).map(([k,v])=>[k,shift(v)])):typeof x==='number'&&x>1500000000000&&x<2000000000000?x+offset:x;
   const answer=shift(value);answer.target={...answer.target,...target};
   for(const group of answer.groups||[]){const measured=group.points.find(p=>p.p50_ms!==null);if(measured)group.points=group.points.map((p,i)=>({...measured,start:p.start,end:p.end,slot_revision_min:target.slot_revision,slot_revision_max:target.slot_revision,p50_ms:20+i%7,p95_ms:30+i%9,p50_5m_ms:20+i%7}));}
   return json(answer);
  }
  if(u.pathname.endsWith('/renewal')||u.pathname.endsWith('/renewals'))return json({items:[],undo:null});
  if(u.pathname.endsWith('/public-egress-check'))return json({});
  if(u.pathname==='/api/v1/web/dashboard'||u.pathname==='/api/v1/web/organization/catalog'){
   const answer=f.catalog();answer.server_now=NOW;return json(answer);
  }
  return r.continue();
 });
 if(prepare)await prepare(page,context);
 await page.goto(f.base);await page.waitForFunction(n=>typeof dashboardData!=='undefined'&&dashboardData.ready()&&agents.size===n,n);
 if(n)await page.waitForFunction(n=>agentCards.size===n,n);
 await page.waitForFunction(()=>!document.querySelector('#overview-money').textContent.includes('正在'));
 return {page,context,f,errors,raw,targets,async close(){await context.close();f.close();}};
}
async function scene(page,name){
 const id=ID(0);
 if(name==='Home-A')return;
 if(name==='Home-B'){await page.evaluate(()=>{for(const c of agentCards.values())c.setFaces('BBB');});return;}
 if(name.startsWith('Details-')){await page.evaluate(({id,tab})=>deviceDetails.open(id,tab),{id,tab:name.slice(8)});if(name==='Details-resources')await page.waitForSelector('.resource-canvas canvas');return;}
 if(name==='Selector'){await page.locator(`[data-agent-id="${id}"] .selector-trigger`).click();await page.waitForSelector('.selector-dialog[open]');return;}
 if(name==='Confirm'){await page.evaluate(()=>{void DialogShell.confirm({title:'暂停监控',message:'暂停后这台机器将不再接收监控数据，可以随时恢复。',confirmText:'暂停'});});return;}
 if(name==='Legacy-Selector'){await page.evaluate(()=>{const n=document.createElement('section');n.className='selector-popover';n.setAttribute('role','dialog');n.innerHTML='<div class="selector-popover-heading"><strong>出站选择</strong><button type="button">关闭</button></div><p class="selector-popover-status">Global：出口 B</p><select aria-label="出站"><option>出口 B（当前）</option><option>出口 A</option></select>';document.body.append(n);n.style.left='calc(50% - 160px)';n.style.top='40%';n.style.width='320px';});return;}
 if(name==='Upgrade'){await page.evaluate(id=>openUpgradeAgentDialog(agents.get(id)),id);return;}
 if(name==='Revoke'){await page.evaluate(id=>openRemoveAgentDialog(agents.get(id)),id);return;}
 if(name==='Plan'||name==='Quota'){await page.evaluate(({id,mode})=>openPlanDialog(agents.get(id),null,mode),{id,mode:name==='Quota'?'quota':'plan'});return;}
 if(name==='Renewal'){await page.evaluate(id=>planRenewal.open(id),id);return;}
 if(name==='Assign'){await page.evaluate(id=>organizationDialogs.openAssignment(id),id);await page.waitForFunction(()=>document.querySelector('#organization-group')?.options.length>1);return;}
 if(name==='Groups'||name==='Tags'){await page.evaluate(kind=>organizationDialogs.openManage(kind),name==='Groups'?'groups':'tags');await page.waitForSelector('.organization-manage-row');return;}
 if(name==='Expense'){await page.locator('#overview-expense').click();await page.waitForFunction(()=>document.querySelector('#expense-monthly').textContent.startsWith('$'));return;}
 if(name==='Filter'){await page.locator('#panel-more-toggle').click();return;}
 if(name==='Quality-Settings'||name==='Quality-Copy'){
  await page.evaluate(async id=>{await networkQuality.ensureHistoryTargets(id);networkQuality.openSettings(id);},id);await page.waitForSelector('#network-quality-settings');
  if(name==='Quality-Copy')await page.locator('#network-quality-settings').getByRole('button',{name:'从其他设备复制',exact:false}).click();return;
 }
 if(name==='Quality-History'){
  await page.evaluate(async id=>{await networkQuality.ensureHistoryTargets(id);const t=networkQuality.historyTargets(id)[0];networkQuality.openHistoryFor(id,t.id,t.family,'latency');},id);await page.waitForSelector('#network-quality-history canvas');return;
 }
 if(name.startsWith('Add-')){
  await page.locator('#add-agent').click();
  const panel={'Add-Choice':'add-agent-choice','Add-OneClick':'oneclick-pane','Add-Command':'oneclick-result','Add-Manual':'add-agent-create','Add-Created':'add-agent-result'}[name];
  if(panel!=='add-agent-choice')await page.evaluate(panel=>{
   for(const id of ['add-agent-choice','oneclick-pane','oneclick-result','add-agent-create','add-agent-result'])document.getElementById(id).classList.toggle('hidden',id!==panel);
   document.querySelector('#oneclick-command').textContent='curl -fsSL https://example.invalid/install.sh | sh';document.querySelector('#created-enrollment').textContent='synthetic-example';document.querySelector('#created-install-command').textContent='sudo 404-probe-install agent';
   document.querySelector('#oneclick-countdown').textContent='30:00';
  },panel);return;
 }
 if(name==='Empty'){await page.evaluate(()=>{agents.clear();render();});return;}
 const url=name==='Login'?'/login':name==='History'?'/history.html?id='+id:name==='Jobs'?'/jobs.html':'/schedules.html';
 await page.goto(new URL(url,page.url()).href);
 if(name==='History')await page.waitForFunction(()=>document.querySelector('#title').textContent!=='Agent 历史');
 if(name==='Jobs')await page.waitForSelector('.job-card');
 if(name==='Schedules')await page.waitForSelector('.schedule-card');
}
module.exports={open,scene,resources,NOW,ID};
