// Paint evidence: retain every frame, classify against the target CommitLoad.
// The maintainer's acceptance boundary excludes only pre-commit browser frames.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {launch}=require('./lib/v11-panel-fixture.cjs'),{start}=require('./lib/v110-dashboard-fixture.cjs');
const out=path.resolve(process.argv[2]),baseline=path.resolve(process.argv[3]),current=path.resolve(__dirname,'..');
fs.mkdirSync(out,{recursive:true});
(async()=>{const browser=await launch(),fixture=await start({n:1}),summary=[];
 try{for(const [label,source]of [['before',baseline],['after',current]])for(let round=1;round<=3;round++){
  const context=await browser.newContext({viewport:{width:1100,height:780},colorScheme:'light'}),page=await context.newPage();
  await page.addInitScript(()=>{localStorage.setItem('404-probe.theme.v1','dark');for(const name of ['pageswap','pagereveal'])addEventListener(name,e=>performance.mark('qa:'+name+':'+Boolean(e.viewTransition)));});
  await context.route('**/*',async route=>{const u=new URL(route.request().url());if(u.origin!==fixture.base)return route.abort();if(u.pathname.startsWith('/api/'))return route.continue();
   let file=path.join(source,'web/static',u.pathname==='/'?'index.html':u.pathname.slice(1)),body;
   if(u.pathname==='/login')body=fs.readFileSync(path.join(source,'internal/server/web_auth.go'),'utf8').match(/Parse\(`([\s\S]*?)`\)\)/)[1].replace(/\{\{if .*?\}\}[\s\S]*?\{\{end\}\}/g,'').replace('{{.CSRFToken}}','synthetic-only');
   else{if(!fs.existsSync(file))return route.fulfill({status:404});body=fs.readFileSync(file);}
   if(u.pathname==='/style.css')await new Promise(r=>setTimeout(r,180));
   return route.fulfill({body,contentType:file.endsWith('.css')?'text/css':file.endsWith('.js')?'text/javascript':file.endsWith('.svg')?'image/svg+xml':'text/html'});
  });
  const cdp=await context.newCDPSession(page);
  for(const [action,target]of [['open','/'],['reload','/'],['jobs','/jobs.html'],['schedules','/schedules.html'],['history','/history.html'],['login','/login']]){
   const stem=label+'-'+action+'-'+round;
   const events=[];const onData=({value})=>events.push(...value);cdp.on('Tracing.dataCollected',onData);
   await cdp.send('Tracing.start',{categories:'devtools.timeline,disabled-by-default-devtools.screenshot,blink.user_timing,loading',transferMode:'ReportEvents'});
   // Arm the recorder on the existing document before requesting navigation.
   // Preserve its image as well: an asynchronous tracing-start readback of a
   // new tab's already-drawn blank canvas must not race the new CommitLoad.
   const initial=await cdp.send('Page.captureScreenshot',{format:'png'});
   fs.writeFileSync(path.join(out,stem+'-before-navigation.png'),Buffer.from(initial.data,'base64'));
   await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
   if(action==='reload')await page.reload();else if(action==='open')await page.goto(fixture.base+target);else{
    // Use document-initiated navigation so the browser can run the real cross-
    // document transition; address-bar automation does not exercise that path.
    await Promise.all([page.waitForURL(fixture.base+target,{waitUntil:'load'}),page.evaluate(target=>{const a=document.createElement('a');a.href=target;document.body.append(a);a.click();},target)]);
   }
   await page.waitForTimeout(350);const fact=await page.evaluate(()=>({theme:document.documentElement.dataset.theme,scheme:getComputedStyle(document.documentElement).colorScheme,meta:document.querySelector('meta[name="color-scheme"]')?.content||null,root:getComputedStyle(document.documentElement).backgroundColor,body:getComputedStyle(document.body).backgroundColor,navigationTransition:[...document.styleSheets].filter(s=>s.href?.endsWith('/style.css')).some(s=>[...s.cssRules].some(r=>r.cssText.startsWith('@view-transition')&&r.navigation==='auto'))}));
   const done=new Promise(r=>cdp.once('Tracing.tracingComplete',r));await cdp.send('Tracing.end');await done;cdp.off('Tracing.dataCollected',onData);
   fs.writeFileSync(path.join(out,stem+'-trace.json'),JSON.stringify({traceEvents:events}));
   const commit=events.find(e=>e.name==='CommitLoad'&&e.args?.data?.isMainFrame&&e.args.data.url===fixture.base+target);assert(commit,'missing target CommitLoad: '+stem);
   const frames=events.filter(e=>e.name==='Screenshot'&&e.args?.snapshot);const decoder=await context.newPage();const samples=[];
   for(const [i,frame]of frames.entries()){
    const file=stem+'-frame-'+i+'.jpg';fs.writeFileSync(path.join(out,file),Buffer.from(frame.args.snapshot,'base64'));
    const pixel=await decoder.evaluate(async data=>{const img=new Image();img.src='data:image/jpeg;base64,'+data;await img.decode();const canvas=document.createElement('canvas');canvas.width=img.width;canvas.height=img.height;const c=canvas.getContext('2d');c.drawImage(img,0,0);return [...c.getImageData(Math.floor(img.width*.8),Math.floor(img.height*.92),1,1).data].slice(0,3);},frame.args.snapshot);
    samples.push({file,ts:frame.ts,phase:frame.ts<commit.ts?'before_commit':'after_commit',pixel,white:pixel.every(v=>v>240),dark:pixel.every(v=>v<64)});
   }await decoder.close();await page.screenshot({path:path.join(out,stem+'-settled.png')});
   const pre=samples.filter(f=>f.phase==='before_commit'),post=samples.filter(f=>f.phase==='after_commit');
   const row={label,action,round,...fact,commit_ts:commit.ts,pre_commit_frames:pre.length,pre_commit_white_frames:pre.filter(f=>f.white).length,post_commit_frames:post.length,post_commit_white_frames:post.filter(f=>f.white).length,post_commit_dark_frames:post.filter(f=>f.dark).length,transition_marks:events.filter(e=>/^qa:(pageswap|pagereveal):/.test(e.name)).map(e=>({name:e.name,ts:e.ts})),paint_events:events.filter(e=>e.name==='Paint').length,frames:samples};summary.push(row);
   assert.equal(fact.theme,'dark');assert.equal(fact.body,'rgb(14, 16, 18)');if(label==='after'){assert(post.length>0);assert(post.every(f=>!f.white),'white frame after target CommitLoad: '+stem);assert(post.every(f=>f.dark),'non-dark frame after target CommitLoad: '+stem);assert.equal(fact.meta,'dark');assert.equal(fact.scheme,'dark');assert.equal(fact.root,'rgb(14, 16, 18)');assert(fact.navigationTransition,'missing navigation:auto on '+target);}
   console.log(JSON.stringify({label,action,round,pre_commit_white:row.pre_commit_white_frames,post_commit:post.length,post_commit_dark:row.post_commit_dark_frames,transition_marks:row.transition_marks.map(m=>m.name)}));
  }await context.close();
 }}finally{fixture.close();await browser.close();fs.writeFileSync(path.join(out,'summary.json'),JSON.stringify(summary,null,2));}
 assert.equal(summary.length,36);
})().catch(e=>{console.error(e);process.exitCode=1;});
