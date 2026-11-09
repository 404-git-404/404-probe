// Native mouse delivery audit under real loopback SSE. Other actions are blocked
// at capture only for this transport audit; theme and quick filters execute.
const path=require('node:path');
async function audit({source=path.resolve(__dirname,'../..'),onResult=()=>{}}={}){
 const {launch,open,ID}=require(path.join(source,'scripts/lib/v11-panel-fixture.cjs'));
 const b=await launch();let f,timer;const results=[];
 try{
 f=await open(b,{n:60,width:2048,height:1152});const p=f.page;
 const now=Date.now();for(const a of f.f.state.items){a.name='Synthetic-'+a.agent_id.slice(-3);a.public_egress_check.enabled=true;a.state.public_egress={ipv4:{status:'available',ip:'203.0.113.42',checked_at:now,valid_until:now+86400000},ipv6:{status:'available',ip:'2001:db8::42',checked_at:now,valid_until:now+86400000}};}
 await p.evaluate(()=>dashboardData.reload());await p.waitForFunction(()=>!renderFrame);
 let seq=0;timer=setInterval(()=>{for(let j=0;j<3;j++){const i=(seq++)%60;f.f.publish('agent',{agent_id:ID(i),online:true,state:{...f.f.state.items[i].state,cpu_percent:seq%99,collected_at:Date.now()}});}f.f.publish('agent',{agent_id:ID(59),online:seq%2===0,state:f.f.state.items[59].state});},70);
 await p.evaluate(()=>{window.__clickAudit={hits:0};document.addEventListener('click',e=>{const n=e.target.closest('[data-audit]');if(!n)return;__clickAudit.hits++;if(n.dataset.audit==='delivery'){e.preventDefault();e.stopImmediatePropagation();}},true);});
 async function run(name,selector,kind='delivery'){
  if(process.env.V111_CLICK_ONLY&&!process.env.V111_CLICK_ONLY.split(',').includes(name))return;
  const n=p.locator(selector).first();if(!await n.count()||!await n.isVisible())throw Error('missing audit control: '+name+' '+selector);
  await n.evaluate((n,kind)=>n.dataset.audit=kind,kind);let hits=0,effects=0;
  for(let i=0;i<50;i++){
   await n.scrollIntoViewIfNeeded();const r=await n.boundingBox(),before=await p.evaluate(()=>({hits:__clickAudit.hits,theme:document.documentElement.dataset.theme,quick:JSON.stringify(dashboardUI.preferences.value.quick)}));
   // Alternate center and padded area to cover the button, not just its glyph.
   await p.mouse.move(r.x+r.width*(i%2?.5:.25),r.y+r.height*.5);await p.mouse.down();await p.waitForTimeout(50+(i%3)*50);await p.mouse.up();
   const after=await p.evaluate(()=>({hits:__clickAudit.hits,theme:document.documentElement.dataset.theme,quick:JSON.stringify(dashboardUI.preferences.value.quick)}));hits+=after.hits-before.hits;effects+=kind==='theme'?after.theme!==before.theme:kind==='quick'?after.quick!==before.quick:0;
   if(selector==='#dashboard-sort')await p.keyboard.press('Escape');
  }
  await n.evaluate(n=>delete n.dataset.audit);const result={name,selector,attempts:50,hits,...(kind!=='delivery'?{effects}:{})};results.push(result);onResult(result);
 }
 await run('theme','[data-theme-toggle]','theme');await run('quick','#dashboard-quick [data-quick=network]','quick');await p.evaluate(()=>dashboardUI.clear());
 for(const [name,selector]of [['settings','#panel-settings'],['logout','#logout'],['add','#add-agent'],['view cards','[data-view=cards]'],['view list','[data-view=list]'],['group','#dashboard-groups .dashboard-option'],['tag filter','#dashboard-tags .dashboard-option'],['details','.c7-detail'],['edit name','.agent-name-display button'],['edit plan','.edit-plan'],['outbound','.selector-trigger'],['IPv4','.public-egress [data-family=ipv4]'],['IPv6','.public-egress [data-family=ipv6]'],['traffic','.c7-quota'],['renew','.renew-plan'],['sort','#dashboard-sort']])await run(name,selector);
 await p.locator('[data-view=list]').click();await p.waitForFunction(()=>!renderFrame);
 for(const [name,selector]of [['row name','.banner-name'],['row outbound','.selector-compact'],['row upgrade','.banner-upgrade-link']])await run(name,selector);
 // Select a tag to create the removable toolbar chip.
 await p.locator('#dashboard-tags .dashboard-option').first().click();await p.locator('.dashboard-chip').first().waitFor();await run('tag chip','.dashboard-chip');
 return {results,events:seq,errors:f.errors};
 }finally{clearInterval(timer);await f?.close();await b.close();}
}
module.exports={audit};
if(require.main===module)audit({source:process.env.V111_CLICK_SOURCE||undefined,onResult:r=>console.log(JSON.stringify(r))}).then(r=>console.log('CLICK_AUDIT '+JSON.stringify(r))).catch(e=>{console.error(e);process.exitCode=1;});
