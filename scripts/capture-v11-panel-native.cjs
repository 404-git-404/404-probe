// Manual fallback when Windows UI automation cannot bind the QA window.
// Run: node scripts/capture-v11-panel-native.cjs
// In the named QA window use Chrome's own menu to select 125%, then 150%.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {chromium}=require(process.env.M02_PLAYWRIGHT||(()=>{throw Error('Set M02_PLAYWRIGHT to the required local dependency or artifact path');})());
const {open}=require('./lib/v11-panel-fixture.cjs');
const out=path.resolve(process.env.V11_PANEL_SCREENS||'../release-records/v1.1.0/v11-CX-面板-screens');
(async()=>{
 const browser=await chromium.launch({headless:false,executablePath:process.env.M02_CHROMIUM||(()=>{throw Error('Set M02_CHROMIUM to the required local dependency or artifact path');})(),args:['--window-size=1920,1160']});
 const f=await open(browser,{theme:'dark'}),p=f.page,seen=new Set();fs.mkdirSync(out,{recursive:true});
 await p.evaluate(()=>document.title='404-probe · Native Zoom QA');
 console.log('Use Chrome menu in Native Zoom QA: 125%, then 150%. Captures save automatically. Close the window to cancel.');
 try{
  const baseline=await p.evaluate(()=>devicePixelRatio);
  for(let i=0;i<600&&seen.size<2;i++){
   const fact=await p.evaluate(()=>({ratio:devicePixelRatio,width:innerWidth,height:innerHeight,collapsed:document.querySelector('.dashboard-app').classList.contains('panel-collapsed'),overflow:document.documentElement.scrollWidth>innerWidth}));
   for(const scale of [1.25,1.5])if(!seen.has(scale)&&Math.abs(fact.ratio/baseline-scale)<.01){assert(!fact.overflow);const label=Math.round(scale*100);await p.screenshot({path:path.join(out,`dark-native-zoom-${label}.png`)});fs.writeFileSync(path.join(out,`native-zoom-${label}.json`),JSON.stringify({...fact,baseline,native_zoom:scale},null,2)+'\n');seen.add(scale);console.log('Saved '+label+'%');}
   await new Promise(r=>setTimeout(r,500));
  }
  if(seen.size<2)throw Error('Native zoom verification incomplete');assert.deepEqual(f.errors,[]);
 }finally{await f.close();await browser.close();}
})().catch(e=>{console.error(e.message);process.exitCode=1;});
