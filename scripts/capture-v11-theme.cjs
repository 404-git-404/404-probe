// Invoked via V11_THEME=compare|screens scripts/capture-v110-dashboard-compare.cjs.
const fs=require('node:fs'),path=require('node:path');
const {chromium}=require(process.env.M02_PLAYWRIGHT||(()=>{throw Error('Set M02_PLAYWRIGHT to the required local dependency or artifact path');})());
const {open,scene}=require('./lib/v11-theme-fixture.cjs');
const scenes=['Home-A','Home-B','Details-overview','Details-resources','Details-security','Details-management','Selector','Legacy-Selector','Confirm','Upgrade','Revoke','Plan','Quota','Renewal','Assign','Groups','Tags','Expense','Filter','Quality-Settings','Quality-Copy','Quality-History','Add-Choice','Add-OneClick','Add-Command','Add-Manual','Add-Created','Empty','History','Jobs','Schedules','Login'];
(async()=>{
 const mode=process.env.V11_THEME||'compare',out=path.resolve(process.env.V11_OUTPUT||'../release-records/v1.1.0/v11-CX-深色-evidence/pixel'),baseline=path.resolve(process.env.V11_BASELINE||'../release-records/v1.1.0/v11-CX-深色-evidence/baseline');fs.mkdirSync(out,{recursive:true});
 const browser=await chromium.launch({headless:true,executablePath:process.env.M02_CHROMIUM||(()=>{throw Error('Set M02_CHROMIUM to the required local dependency or artifact path');})()}),results=[];
 try{
 for(const width of [1440,390])for(const name of scenes)for(const variant of mode==='compare'?['baseline','actual']:['light','dark']){
  if(mode!=='compare'&&width===390&&(variant!=='dark'||!['Home-A','Selector'].includes(name)))continue;
  if(process.env.V11_SCENES&&!process.env.V11_SCENES.split(',').includes(name))continue;
  const f=await open(browser,{...(variant==='baseline'?{source:baseline}:{}),width,theme:variant==='dark'?'dark':'light'});
  try{
   await scene(f.page,name);await f.page.evaluate(()=>document.fonts.ready);await f.page.waitForTimeout(400);
   if(!['Login','History','Jobs','Schedules'].includes(name))await f.page.waitForFunction(()=>{const s=monitorCoordinator.snapshot();return !s.active&&!s.queued;});
   await f.page.evaluate(()=>{document.activeElement?.blur();});
   if(f.errors.length)throw Error(f.errors.join('\n'));
   const file=`${name}-${width}-${variant}.png`;await f.page.screenshot({path:path.join(out,file),animations:'disabled',fullPage:mode!=='compare'&&name.startsWith('Home')});
   results.push({name,width,variant,file,errors:f.errors});console.log(file);
  }finally{await f.close();}
 }
 }finally{await browser.close();fs.writeFileSync(path.join(out,'captures.json'),JSON.stringify(results,null,2)+'\n');}
})().catch(e=>{console.error(e);process.exitCode=1;});
