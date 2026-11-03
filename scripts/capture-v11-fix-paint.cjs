// Paint evidence uses the same slow stylesheet in both sources, saved dark and a light OS.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {launch}=require('./lib/v11-panel-fixture.cjs'),{start}=require('./lib/v110-dashboard-fixture.cjs');
const out=path.resolve(process.argv[2]),baseline=path.resolve(process.argv[3]),current=path.resolve(__dirname,'..');
fs.mkdirSync(out,{recursive:true});
(async()=>{const browser=await launch(),fixture=await start({n:1}),summary=[];
 try{for(const [label,source]of [['before',baseline],['after',current]]){
  const context=await browser.newContext({viewport:{width:1100,height:780},colorScheme:'light'}),page=await context.newPage();
  await page.addInitScript(()=>localStorage.setItem('404-probe.theme.v1','dark'));
  await context.route('**/*',async route=>{const u=new URL(route.request().url());if(u.origin!==fixture.base)return route.abort();if(u.pathname.startsWith('/api/'))return route.continue();
   let file=path.join(source,'web/static',u.pathname==='/'?'index.html':u.pathname.slice(1)),body;
   if(u.pathname==='/login')body=fs.readFileSync(path.join(source,'internal/server/web_auth.go'),'utf8').match(/Parse\(`([\s\S]*?)`\)\)/)[1].replace(/\{\{if .*?\}\}[\s\S]*?\{\{end\}\}/g,'').replace('{{.CSRFToken}}','synthetic-only');
   else{if(!fs.existsSync(file))return route.fulfill({status:404});body=fs.readFileSync(file);}
   if(u.pathname==='/style.css')await new Promise(r=>setTimeout(r,180));
   return route.fulfill({body,contentType:file.endsWith('.css')?'text/css':file.endsWith('.js')?'text/javascript':file.endsWith('.svg')?'image/svg+xml':'text/html'});
  });
  const cdp=await context.newCDPSession(page);
  for(const [action,target]of [['open','/'],['reload','/'],['jobs','/jobs.html'],['schedules','/schedules.html'],['history','/history.html'],['login','/login']]){
   const events=[];const onData=({value})=>events.push(...value);cdp.on('Tracing.dataCollected',onData);
   await cdp.send('Tracing.start',{categories:'devtools.timeline,disabled-by-default-devtools.screenshot,blink.user_timing,loading',transferMode:'ReportEvents'});
   if(action==='reload')await page.reload();else await page.goto(fixture.base+target);
   await page.waitForTimeout(150);const fact=await page.evaluate(()=>({theme:document.documentElement.dataset.theme,scheme:getComputedStyle(document.documentElement).colorScheme,meta:document.querySelector('meta[name="color-scheme"]')?.content||null,root:getComputedStyle(document.documentElement).backgroundColor,body:getComputedStyle(document.body).backgroundColor}));
   const done=new Promise(r=>cdp.once('Tracing.tracingComplete',r));await cdp.send('Tracing.end');await done;cdp.off('Tracing.dataCollected',onData);
   const stem=label+'-'+action;fs.writeFileSync(path.join(out,stem+'-trace.json'),JSON.stringify({traceEvents:events}));
   const frames=events.filter(e=>e.name==='Screenshot'&&e.args?.snapshot);const decoder=await context.newPage();const samples=[];
   for(const [i,frame]of frames.entries()){
    const file=stem+'-frame-'+i+'.jpg';fs.writeFileSync(path.join(out,file),Buffer.from(frame.args.snapshot,'base64'));
    const pixel=await decoder.evaluate(async data=>{const img=new Image();img.src='data:image/jpeg;base64,'+data;await img.decode();const canvas=document.createElement('canvas');canvas.width=img.width;canvas.height=img.height;const c=canvas.getContext('2d');c.drawImage(img,0,0);return [...c.getImageData(Math.floor(img.width*.8),Math.floor(img.height*.92),1,1).data].slice(0,3);},frame.args.snapshot);
    samples.push({file,ts:frame.ts,pixel,white:pixel.every(v=>v>240)});
   }await decoder.close();await page.screenshot({path:path.join(out,stem+'-settled.png')});
   const row={label,action,...fact,paint_events:events.filter(e=>e.name==='Paint').length,frames:samples};summary.push(row);
   assert.equal(fact.theme,'dark');assert.equal(fact.body,'rgb(14, 16, 18)');if(label==='after'){assert(samples.length>0);assert(samples.every(f=>!f.white),'white frame after fix: '+stem);assert.equal(fact.meta,'dark');assert.equal(fact.scheme,'dark');assert.equal(fact.root,'rgb(14, 16, 18)');}
  }await context.close();
 }}finally{fixture.close();await browser.close();fs.writeFileSync(path.join(out,'summary.json'),JSON.stringify(summary,null,2));}
 console.log(JSON.stringify(summary.map(({label,action,paint_events,frames})=>({label,action,paint_events,frames:frames.map(f=>({white:f.white,pixel:f.pixel}))})),null,2));
})().catch(e=>{console.error(e);process.exitCode=1;});
