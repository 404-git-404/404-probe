const {test,before,after}=require('node:test'),assert=require('node:assert/strict');
const {chromium}=require(process.env.M02_PLAYWRIGHT||'C:/Users/ufoli/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');
const {open,scene}=require('./lib/v11-theme-fixture.cjs');
let browser;
before(async()=>{browser=await chromium.launch({headless:true,executablePath:process.env.M02_CHROMIUM||'C:/Users/ufoli/AppData/Local/ms-playwright/chromium-1228/chrome-win64/chrome.exe'});});
after(async()=>browser?.close());
async function ui(run,options){const f=await open(browser,options);try{await run(f);assert.deepEqual(f.errors,[]);}finally{await f.close();}}
const theme=p=>p.evaluate(()=>({mode:ProbeTheme.get(),resolved:document.documentElement.dataset.theme}));
const waitTheme=(p,value)=>p.waitForFunction(v=>document.documentElement.dataset.theme===v,value);
test('three modes cycle in order and survive navigation/reload with accessible current labels',()=>ui(async({page:p})=>{
 const button=p.locator('[data-theme-toggle]');
 for(const [mode,resolved,label] of [['light','light','浅色'],['dark','dark','深色'],['system','light','跟随系统'],['light','light','浅色']]){
  assert.deepEqual(await theme(p),{mode,resolved});assert.equal(await button.getAttribute('aria-label'),'主题：'+label);assert.equal(await button.getAttribute('title'),'主题：'+label);await button.click();
 }
 assert.deepEqual(await theme(p),{mode:'dark',resolved:'dark'});await p.reload();await waitTheme(p,'dark');assert.equal(await p.evaluate(()=>localStorage.getItem('404-probe.theme.v1')),'dark');
 for(const path of ['/history.html?id='+'1'.padStart(32,'0'),'/jobs.html','/schedules.html','/']){await p.goto(new URL(path,p.url()).href);assert.equal(await p.locator('[data-theme-toggle]').count(),1);assert.deepEqual(await theme(p),{mode:'dark',resolved:'dark'});assert(await p.evaluate(()=>[...document.styleSheets].filter(s=>s.href?.endsWith('/style.css')).some(s=>[...s.cssRules].some(r=>r.cssText.startsWith('@view-transition')&&r.navigation==='auto'))));}
}));
test('default system mode follows OS changes while explicit modes ignore them',()=>ui(async({page:p})=>{
 assert.deepEqual(await theme(p),{mode:'system',resolved:'light'});await p.emulateMedia({colorScheme:'dark'});await waitTheme(p,'dark');
 await p.evaluate(()=>ProbeTheme.set('light'));await p.emulateMedia({colorScheme:'light'});await p.emulateMedia({colorScheme:'dark'});assert.deepEqual(await theme(p),{mode:'light',resolved:'light'});
 await p.evaluate(()=>ProbeTheme.set('system'));await waitTheme(p,'dark');await p.emulateMedia({colorScheme:'light'});await waitTheme(p,'light');
},{theme:null}));
test('skipped cross-document transitions keep dark pages and produce no unhandled ready rejection',()=>ui(async({page:p})=>{
 for(const path of ['/jobs.html','/schedules.html','/history.html','/']){
  await Promise.all([p.waitForURL(new URL(path,p.url()).href,{waitUntil:'load'}),p.evaluate(path=>{const a=document.createElement('a');a.href=path;document.body.append(a);a.click();},path)]);
  assert.equal(await p.evaluate(()=>document.documentElement.dataset.theme),'dark');
 }
 assert(await p.evaluate(()=>Number(localStorage.getItem('qa-transition-skips')))>0);
 await p.waitForTimeout(100);
},{theme:'dark',prepare:async p=>p.addInitScript(()=>{
 for(const name of ['pageswap','pagereveal'])addEventListener(name,event=>{if(event.viewTransition){localStorage.setItem('qa-transition-skips',String(Number(localStorage.getItem('qa-transition-skips')||0)+1));event.viewTransition.skipTransition();}});
})}));
test('login POST opts out for that submission and restores auto on the destination',()=>ui(async({page:p})=>{
 await scene(p,'Login');await p.locator('#password').fill('synthetic-theme-test');
 await Promise.all([p.waitForURL(new URL('/',p.url()).href,{waitUntil:'load'}),p.locator('button[type=submit]').click()]);
 assert.equal(await p.evaluate(()=>localStorage.getItem('qa-login-navigation')),'none');
 assert.equal(await p.evaluate(()=>document.documentElement.dataset.theme),'dark');
 assert(await p.evaluate(()=>[...document.styleSheets].filter(s=>s.href?.endsWith('/style.css')).some(s=>[...s.cssRules].some(r=>r.cssText.startsWith('@view-transition')&&r.navigation==='auto'))));
},{theme:'dark',prepare:async p=>{
 await p.route('**/login',r=>r.request().method()==='POST'?r.fulfill({status:303,headers:{Location:'/'},body:''}):r.fallback());
 await p.addInitScript(()=>addEventListener('submit',()=>{const sheet=[...document.styleSheets].find(s=>s.href?.endsWith('/style.css'));localStorage.setItem('qa-login-navigation',[...sheet.cssRules].filter(r=>r.cssText.startsWith('@view-transition')).at(-1)?.navigation||'');}));
}}));
test('saved dark resolves while document is loading and the first styled frame is dark on a light OS',()=>ui(async({page:p})=>{
 const first=await p.evaluate(()=>({boot:window.__themeBoot,frame:window.__themeFrame}));
 assert.equal(first.boot.theme,'dark');assert.equal(first.boot.ready,'loading');assert.equal(first.boot.meta,'dark');assert.equal(first.boot.scheme,'dark');assert.equal(first.frame,'rgb(14, 16, 18)');
 assert.equal(await p.evaluate(()=>getComputedStyle(document.documentElement).colorScheme),'dark');
 await scene(p,'Login');assert.equal(await p.locator('[data-theme-toggle]').count(),0);assert.equal(await p.evaluate(()=>document.documentElement.dataset.theme),'dark');
 assert.equal(await p.evaluate(()=>getComputedStyle(document.body).backgroundColor),'rgb(14, 16, 18)');assert(await p.evaluate(()=>[...document.styleSheets].filter(s=>s.href?.endsWith('/style.css')).some(s=>[...s.cssRules].some(r=>r.cssText.startsWith('@view-transition')&&r.navigation==='auto'))));
},{theme:'dark',prepare:async p=>p.addInitScript(()=>{
 const observer=new MutationObserver(()=>{if(document.documentElement?.dataset.theme&&!window.__themeBoot){window.__themeBoot={theme:document.documentElement.dataset.theme,ready:document.readyState,meta:document.querySelector('meta[name="color-scheme"]')?.content,scheme:document.documentElement.style.colorScheme};observer.disconnect();}});observer.observe(document,{subtree:true,attributes:true,attributeFilter:['data-theme']});
 const frame=()=>{if(document.body){window.__themeFrame=getComputedStyle(document.body).backgroundColor;}else requestAnimationFrame(frame);};requestAnimationFrame(frame);
})}));
test('resource uPlots redraw using the new palette without refetching history or losing the active tab',()=>ui(async({page:p})=>{
 await scene(p,'Details-resources');const canvas=p.locator('.resource-canvas canvas').first();const before=await canvas.evaluate(n=>n.toDataURL());
 let reads=0;p.on('request',r=>{if(r.url().includes('/resources/history'))reads++;});await p.evaluate(()=>ProbeTheme.set('dark'));await waitTheme(p,'dark');
 assert.notEqual(await p.locator('.resource-canvas canvas').first().evaluate(n=>n.toDataURL()),before);assert.equal(reads,0);assert.equal(await p.evaluate(()=>deviceDetails.snapshot().tab),'resources');
}));
test('latency SVG, legend and traffic SVG recolor immediately from the same event',()=>ui(async({page:p})=>{
 await scene(p,'Home-B');await p.waitForSelector('.lm-plot svg [data-grid]',{state:'attached'});
 const before=await p.locator('.lm-plot svg [data-grid]').first().getAttribute('stroke');
 const dot=await p.locator('.lm-dot').first().evaluate(n=>getComputedStyle(n).backgroundColor);
 await p.evaluate(()=>ProbeTheme.set('dark'));
 assert.notEqual(await p.locator('.lm-plot svg [data-grid]').first().getAttribute('stroke'),before);
 assert.notEqual(await p.locator('.lm-dot').first().evaluate(n=>getComputedStyle(n).backgroundColor),dot);
 assert.equal(await p.locator('.tc7-rx').first().evaluate(n=>getComputedStyle(n).stroke),'rgb(85, 184, 157)');
}));
test('network history and legacy history canvas redraw on a live theme change',()=>ui(async({page:p})=>{
 await scene(p,'Quality-History');let before=await p.locator('#network-quality-history canvas').first().evaluate(n=>n.toDataURL());await p.evaluate(()=>ProbeTheme.set('dark'));
 assert.notEqual(await p.locator('#network-quality-history canvas').first().evaluate(n=>n.toDataURL()),before);
 await scene(p,'History');await p.waitForTimeout(100);before=await p.locator('#cpu').evaluate(n=>n.toDataURL());await p.evaluate(()=>ProbeTheme.set('light'));
 assert.notEqual(await p.locator('#cpu').evaluate(n=>n.toDataURL()),before);
}));
test('manual light overrides OS dark in quality settings, native controls and backdrop',()=>ui(async({page:p})=>{
 await p.emulateMedia({colorScheme:'dark'});await scene(p,'Quality-Settings');
 assert.equal(await p.locator('.nq-dialog').evaluate(n=>getComputedStyle(n).colorScheme),'light');
 assert.equal(await p.locator('.nq-dialog').evaluate(n=>getComputedStyle(n).backgroundColor),'rgb(255, 253, 250)');
 await p.evaluate(()=>ProbeTheme.set('dark'));assert.equal(await p.locator('.nq-dialog').evaluate(n=>getComputedStyle(n).colorScheme),'dark');
 assert.match(await p.locator('#network-quality-settings').evaluate(n=>getComputedStyle(n,'::backdrop').backgroundColor),/^rgba\(0, 0, 0,/);
}));
