// Captures only loopback pages backed by synthetic data. No installation runs.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {launch,open}=require('./lib/v11-panel-fixture.cjs');
const out=path.resolve(process.env.V11_PANEL_SCREENS||'../release-records/v1.1.0/v11-CX-面板-screens');
fs.mkdirSync(out,{recursive:true});
async function shot(p,name){await p.screenshot({path:path.join(out,name+'.png')});}
(async()=>{const browser=await launch();try{
 for(const theme of ['light','dark']){
  const f=await open(browser,{theme});const p=f.page;
  try{
   for(const width of [1920,1440,1100,390])for(const height of [600,800,1000]){
    await p.setViewportSize({width,height});await p.waitForFunction(()=>!renderFrame);await shot(p,`${theme}-${width}x${height}`);
   }
   await p.setViewportSize({width:1920,height:1080});await shot(p,theme==='light'?'D1-actual':'D4-actual');
   await p.locator('#panel-collapse').click();await shot(p,`${theme}-update-collapsed`);if(theme==='dark')await shot(p,'D8-actual');await p.locator('#panel-collapse').click();
   await p.locator('#panel-settings').click();await p.waitForSelector('#panel-settings-dialog[open]');await shot(p,`${theme}-settings`);await p.keyboard.press('Escape');
   await p.setViewportSize({width:390,height:844});await shot(p,`${theme}-C1-actual`);await p.locator('#panel-mobile-open').click();await shot(p,`${theme}-C2-actual`);await p.keyboard.press('Escape');
   await p.setViewportSize({width:1440,height:1000});
   await p.clock.install();await p.locator('#add-agent').click();await p.locator('#add-agent-choice-name').fill('Alpine-TYO-1G · 合成长名称');await shot(p,`${theme}-E1-actual`);await p.locator('#add-agent-next').click();await p.waitForSelector('#oneclick-result:not(.hidden)');await shot(p,`${theme}-E2-actual`);await shot(p,`${theme}-E3-alternative`);
   await p.clock.fastForward(30*60000+1000);await p.waitForFunction(()=>document.querySelector('#oneclick-countdown').textContent.includes('已过期'));await shot(p,`${theme}-E4-actual`);await p.keyboard.press('Escape');
   await p.locator('#add-agent').click();await p.locator('#add-agent-choice-name').fill('Manual-Linux');await p.locator('#add-agent-choose-manual').click();await p.locator('#add-agent-next').click();await p.waitForSelector('#add-agent-result:not(.hidden)');await shot(p,`${theme}-E5-actual`);await p.keyboard.press('Escape');
   assert.deepEqual(f.errors,[]);
  }finally{await f.close();}
 }
 // Explicit zoom-equivalent viewport/DPR evidence; native zoom was blocked by the Windows UI tool.
 for(const ratio of [1.25,1.5]){const f=await open(browser,{width:Math.round(1920/ratio),height:Math.round(1080/ratio),theme:'dark',deviceScaleFactor:ratio});try{await shot(f.page,'dark-zoom-equivalent-'+Math.round(ratio*100));}finally{await f.close();}}
 // Standalone pages keep their own headers; the theme fixture provides login and history.
 const {open:themeOpen,scene}=require('./lib/v11-theme-fixture.cjs');const f=await themeOpen(browser,{theme:'light'});
 try{for(const name of ['Jobs','Schedules','History','Login']){await scene(f.page,name);await shot(f.page,'light-'+name);}assert.deepEqual(f.errors,[]);}finally{await f.close();}
 // Side-by-side comparison: original mock on the left, real fixture screenshot on the right.
 const records=path.dirname(out),pairs=[];
 for(const key of ['D1','D4','D8'])pairs.push([path.join(records,'mock-v2-png',fs.readdirSync(path.join(records,'mock-v2-png')).find(n=>n.startsWith(key+'-'))),key+'-actual.png',key+'-compare']);
 for(const key of ['C1','C2']){const ref=fs.readdirSync(path.join(records,'mock-v1-png')).find(n=>n.startsWith(key));if(ref)pairs.push([path.join(records,'mock-v1-png',ref),'light-'+key+'-actual.png',key+'-compare']);}
 for(const [theme,label]of [['light','浅色'],['dark','深色']])for(const key of ['E1','E2','E3','E4','E5'])pairs.push([path.join(records,'mock-add-agent/png',fs.readdirSync(path.join(records,'mock-add-agent/png')).find(n=>n.startsWith(key+'-')&&n.endsWith(label+'.png'))),theme+'-'+key+(key==='E3'?'-alternative.png':'-actual.png'),theme+'-'+key+'-compare']);
 for(const [ref,file,name]of pairs){const a=fs.readFileSync(ref),b=fs.readFileSync(path.join(out,file)),aw=a.readUInt32BE(16),bw=b.readUInt32BE(16),height=Math.max(a.readUInt32BE(20),b.readUInt32BE(20));const p=await browser.newPage({viewport:{width:aw+bw+12,height:height+32}});try{await p.setContent('<body style="margin:0;background:#eee;font:14px system-ui"><div style="display:flex;gap:12px"><section><div style="height:32px">REFERENCE</div><img src="data:image/png;base64,'+a.toString('base64')+'"></section><section><div style="height:32px">ACTUAL</div><img src="data:image/png;base64,'+b.toString('base64')+'"></section></div></body>');await p.evaluate(()=>Promise.all([...document.images].map(n=>n.decode())));await shot(p,name);}finally{await p.close();}}
 fs.writeFileSync(path.join(out,'comparison-index.json'),JSON.stringify(pairs.map(([reference,actual,name])=>({reference,actual,comparison:name+'.png'})),null,2)+'\n');
}finally{await browser.close();}
console.log(out);
})().catch(e=>{console.error(e);process.exitCode=1;});
