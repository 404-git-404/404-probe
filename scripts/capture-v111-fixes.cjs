const fs=require('fs'),path=require('path');
const source=path.resolve(process.argv[2]),label=process.argv[3],out=path.resolve(process.argv[4]);
const {launch,open,ID}=require(path.join(source,'scripts/lib/v11-panel-fixture.cjs'));
const CAPTURE_NOW=Date.parse('2026-11-09T12:02:30Z'),realNow=Date.now;Date.now=()=>CAPTURE_NOW;
const completed=fs.readFileSync(path.join(source,'internal/storage/network_quality_card.go'),'utf8').includes('floorDiv64(evalEnd, QualityBlockMS) - 1');
(async()=>{fs.mkdirSync(out,{recursive:true});const b=await launch(),manifest=[];try{for(const theme of ['light','dark']){
 const x=await open(b,{width:2048,height:1152,deviceScaleFactor:1.25,theme,n:60}),p=x.page;
 const shot=async name=>{await p.waitForFunction(()=>!renderFrame);await p.waitForTimeout(100);const file=`${theme}-${name}-${label}.png`;await p.screenshot({path:path.join(out,file)});manifest.push({file,theme,source,label});};
 try{
 await p.clock.setFixedTime(CAPTURE_NOW);
 // Same synthetic world on both versions: a live unsealed block has no data;
 // the offline device also has genuinely empty completed blocks.
 await p.route('**/v5/card?*',async route=>{
  const response=await route.fetch(),raw=await response.json(),block=raw.block_ms,evalEnd=Math.floor((CAPTURE_NOW-90000)/60000)*60000;
  const last=completed?(Math.floor(evalEnd/block)-1)*block:Math.floor((evalEnd-1)/block)*block;
  raw.now=CAPTURE_NOW;raw.eval_end=evalEnd;raw.window_start=last-19*block;
  const id=new URL(route.request().url()).pathname.match(/agents\/([^/]+)/)?.[1],agent=x.f.state.items.find(a=>a.agent_id===id);
  for(const lane of raw.lanes)if(lane.tcp?.blocks?.length){
   const first=lane.tcp.blocks[0];
   lane.tcp.blocks=Array.from({length:20},(_,i)=>{const start=raw.window_start+i*block,unsealed=start+block>evalEnd,empty=unsealed||(!agent?.online&&start>=agent.last_seen);return {...first,start,state:empty?'gray':'green',judged:empty?0:6,slow:0,failed:0,unjudged:0,max_ms:empty?null:22,in_progress:unsealed,partial:unsealed};});
   const liveLast=Math.floor((evalEnd-1)/block)*block,measured=Array.from({length:20},(_,i)=>liveLast-(19-i)*block).filter(start=>start+block<=evalEnd&&(agent?.online||start<agent.last_seen)).length;
   lane.right={...lane.right,numerator:0,denominator:measured*(lane.right.kind==='icmp_loss'?18:6),percent:0,samples:measured?'ok':'none'};
  }
  await route.fulfill({json:raw});
 });
 await p.route('**/api/v1/web/version',route=>route.fulfill({json:{version:'v1.1.0',upgrade_eligible:true,one_click_install:true,update:x.state.update}}));await p.route('**/upgrade-targets',route=>route.fulfill({json:{targets:[{version:'v1.1.0',supported:true,reason_code:''}]}}));await p.evaluate(()=>{serverBuild.version='v1.1.0';document.querySelector('.panel-brand small').textContent='v1.1.0';});
 const now=Date.now();for(const [i,a]of x.f.state.items.entries()){a.name='Synthetic-'+String(i+1).padStart(3,'0');a.search_fields=[a.name];}
 const a=x.f.state.items[0];a.version='v1.0.9';a.plan.country_code_override='MO';a.country_code='MO';a.country_source='manual';a.public_egress_check={enabled:true,supported:true,next_check_at:now+86400000};a.state.public_egress={version:1,supported:true,ipv4:{status:'available',ip:'203.0.113.42',checked_at:now,valid_until:now+86400000},ipv6:{status:'available',ip:'2001:db8::42',checked_at:now,valid_until:now+86400000}};
 a.outbounds.selectors=[{name:'Alpha',current:'Direct',choices:['Direct','Backup']},{name:'Zeta',current:'Direct',choices:['Direct','Backup']}];a.outbounds.order_source='name';
 await p.reload();await p.waitForFunction(()=>dashboardData.ready()&&agents.size===60&&!renderFrame);await p.waitForFunction(()=>!!document.querySelector('.nq7-b[data-i="19"]')?.tipText);
 await p.evaluate(()=>scrollTo(0,document.documentElement.scrollHeight));await shot('A1-cards-bottom');await shot('A5-footer');await p.evaluate(()=>scrollTo(0,0));
 const card=p.locator(`.agent-card[data-agent-id="${ID(0)}"]`);await card.locator('.public-egress-item').first().hover();await p.waitForTimeout(1000);await shot('B2-ip-hover');await p.mouse.move(0,0);
 await p.locator('.nq7-b[data-i="10"]').first().hover();await shot('B9-hint');await p.mouse.move(0,0);await shot('B9-left');await shot('B1-blocks');
 await card.locator('.selector-trigger').click();await shot('B6-order-retained');await p.keyboard.press('Escape');
 await p.locator('[data-view=list]').click();await p.evaluate(()=>scrollTo(0,500));await shot('A2-sticky');await p.evaluate(()=>scrollTo(0,document.documentElement.scrollHeight));await shot('A1-list-bottom');await p.evaluate(()=>scrollTo(0,0));
 await shot('A7-icons-B7-manual-region');await p.locator('.banner-dot').first().hover();await p.waitForTimeout(1000);await shot('A5-status-hover');
 await p.locator('#dashboard-sort').selectOption('cpu');await shot('A6-global-cpu');
 await p.locator('#dashboard-sort').click();await shot('A4-dropdown');await p.keyboard.press('Escape');
 await p.setViewportSize({width:1100,height:900});await p.locator('#agent-list').evaluate(n=>n.scrollLeft=350);await shot('A2-narrow-horizontal');await p.setViewportSize({width:2048,height:1152});await p.locator('#agent-list').evaluate(n=>n.scrollLeft=0);await p.evaluate(()=>scrollTo(0,0));
 const row=p.locator(`.banner-row[data-agent-id="${ID(0)}"]`);await row.locator('[data-column=upgrade]').click();await shot('A3-upgrade-target');await p.keyboard.press('Escape');
 await p.evaluate(id=>deviceDetails.open(id,'overview'),ID(0));await p.waitForTimeout(300);
 x.f.state.items[0].state.public_egress={ipv4:{checked_at:0,status:'unknown',reason:'not_reported'},ipv6:{checked_at:0,status:'unknown'}};await p.evaluate(()=>dashboardData.reload());await p.waitForTimeout(10050);await shot('B3-address-after-10s');await p.keyboard.press('Escape');
 // Restore the synthetic detail address to inspect copying independently of B3.
 x.f.state.items[0].state.public_egress=a.state.public_egress={version:1,supported:true,ipv4:{status:'available',ip:'203.0.113.42',checked_at:now,valid_until:now+86400000}};
 await p.evaluate(async id=>{await readAgentDetail(id);render(new Set([id]));deviceDetails.open(id,'overview');},ID(0));await p.waitForTimeout(350);await p.locator('#device-details .pe-copy').first().click();await p.waitForTimeout(250);await shot('B4-copy-feedback');await p.locator('#details-tab-management').click();await shot('B5-detection-name');await p.keyboard.press('Escape');
 if(x.errors.length)throw Error(JSON.stringify(x.errors));
 }finally{await x.close();}
 }}finally{Date.now=realNow;await b.close();fs.writeFileSync(path.join(out,label+'-manifest.json'),JSON.stringify({viewport:[2048,1152],deviceScaleFactor:1.25,physical:[2560,1440],narrow:[1100,900],synthetic:true,fixedClock:CAPTURE_NOW,completedBlockServer:completed,files:manifest,nativePopupNote:'Playwright page screenshots do not capture native OS select/title surfaces; native captures are separate.'},null,2));}
})().catch(e=>{console.error(e);process.exitCode=1});
