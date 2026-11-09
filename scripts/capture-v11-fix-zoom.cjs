// Native browser zoom via a temporary QA extension in a dedicated persistent profile.
// This changes chrome.tabs zoom, not CSS zoom, DPR emulation or root font size.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {open}=require('./lib/v11-panel-fixture.cjs');
const {chromium}=require(process.env.M02_PLAYWRIGHT||(()=>{throw Error('Set M02_PLAYWRIGHT to the required local dependency or artifact path');})());
const out=path.resolve(process.argv[2]),evidence=path.resolve(process.argv[3]),ext=path.join(evidence,'zoom-extension'),profile=fs.mkdtempSync(path.join(evidence,'zoom-profile-'));
fs.mkdirSync(ext,{recursive:true});fs.writeFileSync(path.join(ext,'manifest.json'),JSON.stringify({manifest_version:3,name:'Local native zoom QA',version:'1.0',permissions:['tabs'],background:{service_worker:'background.js'}}));fs.writeFileSync(path.join(ext,'background.js'),'chrome.runtime.onInstalled.addListener(()=>{});chrome.runtime.onStartup.addListener(()=>{});');
(async()=>{const context=await chromium.launchPersistentContext(profile,{headless:false,executablePath:process.env.M02_CHROMIUM||(()=>{throw Error('Set M02_CHROMIUM to the required local dependency or artifact path');})(),viewport:{width:1920,height:1000},ignoreDefaultArgs:['--disable-extensions','--hide-scrollbars'],args:[`--disable-extensions-except=${ext}`,`--load-extension=${ext}`,'--window-size=1920,1100']}),facts=[];let fixture;
 try{const worker=context.serviceWorkers()[0]||await context.waitForEvent('serviceworker'),f=await open({newContext:async()=>context},{theme:'dark',preferences:{viewMode:'list'}}),p=f.page,baseline=await p.evaluate(()=>devicePixelRatio);
  fixture=f.f;for(const scale of [.5,2/3,.8,1.25,1.5]){
   const zoom=await worker.evaluate(async({scale,url})=>{const tab=(await chrome.tabs.query({})).find(t=>t.url===url);if(!tab)throw Error('QA tab not found');await chrome.tabs.setZoom(tab.id,scale);return chrome.tabs.getZoom(tab.id);},{scale,url:p.url()});
   await p.waitForFunction(({scale,baseline})=>Math.abs(devicePixelRatio/baseline-scale)<.015,{scale,baseline});await p.waitForTimeout(150);
   const fact=await p.evaluate(()=>({ratio:devicePixelRatio,width:innerWidth,height:innerHeight,overflow:document.documentElement.scrollWidth>innerWidth,buttons:[...document.querySelectorAll('.theme-toggle,#panel-settings,#logout')].map(n=>({id:n.id||n.className,width:n.getBoundingClientRect().width,height:n.getBoundingClientRect().height})),columns:[...document.querySelectorAll('.banner-header>.banner-cell')].map(n=>({column:n.dataset.column,width:n.getBoundingClientRect().width}))}));
   facts.push({...fact,baseline,native_zoom:zoom});await p.screenshot({path:path.join(out,`dark-native-zoom-${Math.round(scale*100)}.png`)});assert(!fact.overflow);for(const v of fact.buttons)assert(Math.abs(v.width-v.height)<1);for(const v of fact.columns.filter(v=>['cpu','memory','disk'].includes(v.column)))assert(v.width<=101);console.log('saved '+Math.round(scale*100));
  }assert.deepEqual(f.errors,[]);
 }finally{fs.writeFileSync(path.join(out,'native-zoom.json'),JSON.stringify(facts,null,2));fixture?.close();await context.close();assert(profile.startsWith(evidence+path.sep)&&path.basename(profile).startsWith('zoom-profile-'));fs.rmSync(profile,{recursive:true,force:true});}
})().catch(e=>{console.error(e);process.exitCode=1;});
