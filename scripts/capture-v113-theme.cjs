// Headless-only screenshots: authentic page pixels plus a clearly marked zoomed crop.
const fs=require('node:fs'),path=require('node:path');
const {launch,open}=require('./lib/v11-panel-fixture.cjs');
const out=process.env.V113_SCREENS,evidence=process.env.V113_EVIDENCE;
if(!out||!evidence)throw Error('Set V113_SCREENS and V113_EVIDENCE');
(async()=>{fs.mkdirSync(out,{recursive:true});const b=await launch(),records=[];
 try{for(const theme of ['dark','light']){
 const f=await open(b,{width:2048,height:1152,deviceScaleFactor:1.25,n:3,theme});
 try{f.f.state.items.forEach((a,i)=>{a.name='Synthetic-'+(i+1);a.state.hostname='synthetic-'+i;});await f.page.evaluate(()=>dashboardData.reload());await f.page.waitForFunction(()=>!renderFrame);await f.page.mouse.move(0,0);
 const button=f.page.locator('[data-theme-toggle]');const record={theme,title:await button.getAttribute('title'),ariaLabel:await button.getAttribute('aria-label'),icon:await button.textContent(),deviceScaleFactor:await f.page.evaluate(()=>devicePixelRatio),viewport:{width:2048,height:1152}};if(record.title!==`当前：${theme==='dark'?'深色':'浅色'}`||record.ariaLabel!==record.title)throw Error('Unexpected button labels '+JSON.stringify(record));records.push(record);
 const full=await f.page.screenshot({path:path.join(evidence,`theme-${theme}-raw.png`)}),crop=await f.page.locator('.panel-bottom').screenshot({path:path.join(evidence,`theme-${theme}-crop.png`)});
 const context=await b.newContext({viewport:{width:2048,height:1152},deviceScaleFactor:1.25}),page=await context.newPage();await page.setContent(`<style>body{margin:0;background:${theme==='dark'?'#0e1012':'#f7f4ef'};font-family:system-ui;color:${theme==='dark'?'#e7e4df':'#26221e'}}.full{position:absolute;inset:0;width:2048px;height:1152px}.detail{position:absolute;right:70px;bottom:130px;width:560px;padding:24px;border:2px solid ${theme==='dark'?'#68746d':'#8c8275'};border-radius:12px;background:${theme==='dark'?'#16191c':'#fffdfa'}}h2{font-size:23px;margin:0 0 16px}.detail img{width:560px;display:block}p{font-size:15px;margin:16px 0 0}</style><img class="full" src="data:image/png;base64,${full.toString('base64')}"><section class="detail"><h2>${theme==='dark'?'深色':'浅色'} · 按钮局部放大</h2><img src="data:image/png;base64,${crop.toString('base64')}"><p>原始页面 125% 截图的局部放大；不含原生提示框。</p></section>`);await page.screenshot({path:path.join(out,`theme-${theme}-button.png`)});await context.close();
 }finally{await f.close();}
 }fs.writeFileSync(path.join(evidence,'button-dom-labels.json'),JSON.stringify(records,null,2));fs.writeFileSync(path.join(out,'index.html'),'<!doctype html><meta charset="utf-8"><title>两态主题按钮</title><style>body{font:16px system-ui;margin:24px;background:#eee}img{width:100%;max-width:1280px}</style><h1>深色 / 浅色两态按钮</h1><p>Playwright 无头截图，2560×1440 / 125%。右侧为原图局部放大；无原生提示框，实际 title/aria-label 见报告。</p>'+records.map(r=>`<h2>${r.theme}</h2><a href="theme-${r.theme}-button.png"><img src="theme-${r.theme}-button.png"></a>`).join(''));console.log(JSON.stringify(records));
 }finally{await b.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
