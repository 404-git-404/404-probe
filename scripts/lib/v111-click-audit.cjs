// Native mouse delivery audit under real loopback SSE. Other actions are blocked
// at capture only for this transport audit; theme and filters execute.
const path=require('node:path');
async function audit({source=path.resolve(__dirname,'../..'),onResult=()=>{}}={}){
 const {launch,open,ID}=require(path.join(source,'scripts/lib/v11-panel-fixture.cjs'));
 const b=await launch();let f,timer;const results=[];
 try{
 f=await open(b,{n:60,width:2048,height:1152});const p=f.page;
 const now=Date.now();for(const a of f.f.state.items){a.name='Synthetic-'+a.agent_id.slice(-3);a.public_egress_check.enabled=true;a.state.public_egress={ipv4:{status:'available',ip:'203.0.113.42',checked_at:now,valid_until:now+86400000},ipv6:{status:'available',ip:'2001:db8::42',checked_at:now,valid_until:now+86400000}};}
 // Keep one genuinely offline device so the offline filter stays enabled.
 f.f.state.items[58].online=false;
 await p.evaluate(()=>dashboardData.reload());await p.waitForFunction(()=>!renderFrame);
 let seq=0;timer=setInterval(()=>{for(let j=0;j<3;j++){const i=(seq++)%60;f.f.publish('agent',{agent_id:ID(i),online:i!==58,state:{...f.f.state.items[i].state,cpu_percent:seq%99,collected_at:Date.now()}});}f.f.publish('agent',{agent_id:ID(59),online:seq%2===0,state:f.f.state.items[59].state});},70);
 await p.evaluate(()=>{window.__clickAudit={hits:0};document.addEventListener('click',e=>{const n=e.target.closest('[data-audit]');if(!n)return;__clickAudit.hits++;if(n.dataset.audit==='delivery'){e.preventDefault();e.stopImmediatePropagation();}},true);});
 const tag=p.locator('#dashboard-tags .dashboard-options .dashboard-option').first();
 const snapshot=()=>p.evaluate(()=>({hits:__clickAudit.hits,theme:document.documentElement.dataset.theme,
  quick:JSON.stringify(dashboardUI.preferences.value.quick),tags:JSON.stringify(dashboardUI.preferences.value.tags)}));
 async function run(name,selector,kind='delivery'){
  if(process.env.V111_CLICK_ONLY&&!((process.env.V111_CLICK_ONLY==='panel extras'&&name.startsWith('panel:'))||(process.env.V111_CLICK_ONLY==='offline'&&name.includes('离线')))&&!process.env.V111_CLICK_ONLY.split(',').includes(name))return;
  const controls=kind==='theme'?p.locator(selector):p.locator(selector).first();
  if(await controls.count()!==(kind==='theme'?2:1)||!await controls.first().isVisible())throw Error('missing audit control: '+name+' '+selector);
  await controls.evaluateAll((nodes,kind)=>nodes.forEach(node=>node.dataset.audit=kind),kind);
  // Measure the marked controls even if SSE changes which card is first.
  const marked=`[data-audit="${kind}"]`;
  const n=p.locator(kind==='theme'?marked+'[aria-pressed=false]':marked);
  let hits=0,effects=0;
  for(let i=0;i<50;i++){
   if(kind==='tag-clear')await tag.click();
   await n.scrollIntoViewIfNeeded();const r=await n.boundingBox(),before=await snapshot();
   // Alternate center and padded area to cover the button, not just its glyph.
   // Let the browser settle on the hit target before holding the real mouse down.
   await n.hover({position:{x:r.width*(i%2?.5:.25),y:r.height*.5}});
   await p.mouse.down();await p.waitForTimeout(50+(i%3)*50);await p.mouse.up();
   const after=await snapshot();hits+=after.hits-before.hits;
   if(kind==='theme')effects+=after.theme!==before.theme;
   if(kind==='quick'||kind==='renewal')effects+=after.quick!==before.quick;
   if(kind==='tag-clear')effects+=before.tags!=='[]'&&after.tags==='[]';
   if(kind==='renewal'){await p.evaluate(()=>dashboardUI.clear());await p.waitForFunction(()=>!renderFrame);}
   if(selector==='#dashboard-sort')await p.keyboard.press('Escape');
  }
  await p.locator(marked).evaluateAll(nodes=>nodes.forEach(node=>delete node.dataset.audit));
  const result={name,selector,attempts:50,hits,...(kind!=='delivery'?{effects}:{})};results.push(result);onResult(result);
 }
 // Audit both IP badges on a known online card in the initial viewport.
 const egress=`.agent-card[data-agent-id="${ID(0)}"] .public-egress`;
 await run('IPv4',egress+' [data-family=ipv4]');
 await run('IPv6',egress+' [data-family=ipv6]');
 await p.locator('#dashboard-settings:not([open]) > summary').click();
 await run('theme','#dashboard-settings [data-panel-theme]','theme');await run('quick','#overview-health','quick');await p.evaluate(()=>dashboardUI.clear());
 for(const [name,selector]of [['settings','#dashboard-settings > summary'],['logout','#logout'],['add','#add-agent'],['view cards','[data-view=cards]'],['view list','[data-view=list]'],['group','#dashboard-groups .dashboard-option'],['tag filter','#dashboard-tags .dashboard-options .dashboard-option'],['details','.score-stamp'],['edit name','.agent-name-display button'],['edit plan','.edit-plan'],['outbound','.selector-trigger'],['traffic','.c7-quota'],['renew','.renew-plan'],['sort','#dashboard-sort']])await run(name,selector);
 // Audit every other enabled sidebar button as well, including the collapsed
 // panel, more filters, health/renewal summaries, expenses and management entry points.
 const known='[data-panel-theme],#logout,#overview-health,#dashboard-groups .dashboard-option';
 const extras=await p.locator('#dashboard-panel button').evaluateAll((nodes,known)=>nodes.filter(n=>!n.matches(known)&&!n.disabled&&n.getClientRects().length).map((n,i)=>{n.dataset.extraAudit=String(i);return {selector:`[data-extra-audit="${i}"]`,kind:n.id==='overview-renewal'?'renewal':'delivery',name:'panel: '+(n.getAttribute('aria-label')||n.getAttribute('title')||n.textContent).trim()};}),known);
 for(const extra of extras)await run(extra.name,extra.selector,extra.kind);
 await p.locator('[data-view=list]').click();await p.waitForFunction(()=>!renderFrame);
 for(const [name,selector]of [['row name','.banner-name'],['row outbound','.selector-compact'],['row upgrade','.banner-upgrade-link']])await run(name,selector);
 await run('clear tags','#dashboard-tags-clear','tag-clear');
 return {results,events:seq,errors:f.errors};
 }finally{clearInterval(timer);await f?.close();await b.close();}
}
module.exports={audit};
if(require.main===module)audit({source:process.env.V111_CLICK_SOURCE||undefined,onResult:r=>console.log(JSON.stringify(r))}).then(r=>console.log('CLICK_AUDIT '+JSON.stringify(r))).catch(e=>{console.error(e);process.exitCode=1;});
