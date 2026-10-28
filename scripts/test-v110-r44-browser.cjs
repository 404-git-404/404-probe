// Actual traffic DOM/CSS, deterministic local data, no network or installed service.
const {test}=require('node:test'),assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),os=require('node:os'),cp=require('node:child_process');
const {chromium}=require(process.env.M02_PLAYWRIGHT||path.join(os.homedir(),'.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright'));
const executablePath=process.env.M02_CHROMIUM||path.join(os.homedir(),'AppData/Local/ms-playwright/chromium-1228/chrome-win64/chrome.exe');
const root=path.join(__dirname,'../web/static'),out=process.env.V110_R44_SCREENS;
const before=name=>cp.execFileSync('git',['show','v1.0.9:web/static/'+name],{cwd:root,encoding:'utf8'});
const read=name=>fs.readFileSync(path.join(root,name),'utf8');
test('R44 actual 1440/390 layout: full plot, peak/hover, independent scale, missing-minute gaps',async()=>{
 assert(out,'Set V110_R44_SCREENS');fs.mkdirSync(out,{recursive:true});
 const browser=await chromium.launch({headless:true,executablePath}),results=[];
 try{
  for(const width of [1440,390]){
   const shots=[];
   for(const changed of [false,true]){
    const page=await browser.newPage({viewport:{width,height:width===390?844:600},locale:'zh-CN',timezoneId:'America/Los_Angeles'}),errors=[];
    page.on('pageerror',e=>errors.push(e.message));await page.route('**/*',r=>r.abort());
    await page.setContent(`<meta charset="utf-8"><style>${changed?read('card-v7.css'):before('card-v7.css')}
      *{box-sizing:border-box}body{margin:0;padding:16px;background:#f4efe7;color:#2a2622;font:14px system-ui,sans-serif}h1{font-size:20px;margin:0 0 8px}p{margin:0 0 16px;color:#6e655b}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(290px,1fr));gap:16px}.c7{min-width:0}.sample-head{padding:12px;font-weight:700}.sample-traffic{height:160px;margin:0 12px 14px;padding:12px;border:1px solid #e6ded2;border-radius:10px}.c7-traffic-b{height:100%}
      </style><h1>流量 B 面 · ${changed?'改后':'改前 v1.0.9'}</h1><p>同一份本机合成数据 · 峰值按每分钟均值 · 缺测断线</p><main class="grid"></main>`);
    for(const file of ['agent-state.js','network-quality-core.js','resource-history.js'])await page.addScriptTag({content:read(file)});
    await page.addScriptTag({content:changed?read('traffic-chart.js'):before('traffic-chart.js')});
    await page.evaluate(()=>{
      const now=Date.parse('2026-10-27T18:00:00Z'),M=60000,DAY=86400000;
      const histories=new Map();
      for(let card=0;card<3;card++){
        const id=String(card+1).repeat(32),scale=card===0?1:.02,points=[];
        if(card<2)for(let i=0;i<1440;i++){
          if(i>650&&i<680)continue;
          points.push({timestamp:now-DAY+(i+1)*M,cpu:1,ram_percent:15,swap_percent:0,disk_percent:20,load1:0,load5:0,load15:0,rx_rate:scale*(100+5700*Math.abs(Math.sin(i*.037))),tx_rate:scale*(50+2500*Math.abs(Math.cos(i*.024)))});
        }
        histories.set(id,{agent_id:id,server_now:now,from:now-DAY,to:now,bucket_from:now-DAY,bucket_to:now,interval_ms:M,source:'minute_metrics',aggregation:'arithmetic_mean',retention_days:30,coverage:'partial',point_limit:1441,oldest_timestamp:points[0]?.timestamp??null,last_timestamp:points.at(-1)?.timestamp??null,points});
        const cardNode=document.createElement('article');cardNode.className='c7';
        const head=document.createElement('div');head.className='sample-head';head.textContent=['US · 合成机器 A','DE · 合成机器 B（低流量）','JP · 合成机器 C（暂无数据）'][card];
        const module=document.createElement('section');module.className='sample-traffic';const face=document.createElement('div');face.className='c7-traffic-b';module.append(face);cardNode.append(head,module);document.querySelector('.grid').append(cardNode);
        const api=TrafficChart.create({document,Observer:null,interval:()=>0,clear(){},now:()=>now,state:{fetchJSON:async url=>histories.get(url.split('/')[5])},fetcher:async()=>({})});
        const node=api.mount({agent_id:id,online:true});face.append(node);api.setActive(id,true);api.setVisible(id,true);
      }
    });
    await page.waitForFunction(()=>document.querySelector('.tc7-peak')?.textContent.includes('/s'));
    await page.evaluate(()=>document.fonts.ready);
    const facts=await page.evaluate(()=>{
      const cards=[...document.querySelectorAll('.tc7')];
      return {axes:document.querySelectorAll('.tc7-axis').length,overflow:document.documentElement.scrollWidth>innerWidth,
       empty:(()=>{const c=cards[2],status=c.querySelector('.tc7-status'),label=c.querySelector('.tc7-label'),peak=c.querySelector('.tc7-peak'),plot=c.querySelector('.tc7-plot');const rect=n=>{const r=n.getBoundingClientRect();return {top:r.top,bottom:r.bottom,height:r.height}};return {status:rect(status),label:rect(label),peak:rect(peak),plot:rect(plot),text:status.textContent,labelText:label.textContent,clipped:status.scrollHeight>status.clientHeight};})(),
       cards:cards.slice(0,2).map(c=>{const rect=n=>{const r=n.getBoundingClientRect();return {left:r.left,right:r.right,width:r.width,top:r.top,bottom:r.bottom,height:r.height}};return {root:rect(c),plot:rect(c.querySelector('.tc7-plot')),label:rect(c.querySelector('.tc7-label')),peakRect:rect(c.querySelector('.tc7-peak')),peak:c.querySelector('.tc7-peak').textContent,line:c.querySelector('.tc7-rx').getAttribute('d')};})};
    });
    assert.equal(facts.overflow,false);assert.deepEqual(errors,[]);
    if(changed){
      assert.equal(facts.axes,0);assert.equal(facts.empty.labelText,'近 24h');assert.match(facts.empty.text,/没有分钟数据/);assert.equal(facts.empty.clipped,false);assert.ok(facts.empty.status.top>=Math.max(facts.empty.label.bottom,facts.empty.peak.bottom)+2,'empty status is below the header labels');assert.ok(Math.abs((facts.empty.status.top+facts.empty.status.bottom-facts.empty.plot.top-facts.empty.plot.bottom)/2)<20,'empty status is near the plot centre');
      for(const c of facts.cards){assert.ok(Math.abs(c.plot.width-c.root.width)<1);assert.ok(c.label.right<=c.peakRect.left+1,'label and peak do not overlap');assert.match(c.peak,/峰值 .*\/s/);assert.ok((c.line.match(/M/g)||[]).length>=2,'missing minutes split lines');}
      assert.equal(facts.cards[0].line,facts.cards[1].line,'each card independently rescales the same proportional data');
      await page.locator('.tc7-spark').first().hover({position:{x:20,y:25}});
      assert.match(await page.locator('.tc7-spark').first().getAttribute('title'),/下行 .*\/s · 上行 .*\/s/);
    }else assert.equal(facts.axes,3);
    const file=path.join(out,`${width}-${changed?'after':'before'}.png`);await page.screenshot({path:file,fullPage:true});shots.push(file);results.push({width,changed,...facts});await page.close();
   }
   const pair=await browser.newPage({viewport:{width:width*2+24,height:width===390?900:650}});
   await pair.setContent(`<style>body{margin:0;display:flex;gap:24px;background:#d9cdbd}img{width:${width}px;height:auto;align-self:flex-start}</style>`+shots.map(file=>`<img src="data:image/png;base64,${fs.readFileSync(file).toString('base64')}">`).join(''));
   await pair.locator('img').last().waitFor();await pair.evaluate(()=>Promise.all([...document.images].map(i=>i.decode())));await pair.screenshot({path:path.join(out,`${width}-before-after.png`),fullPage:true});await pair.close();
  }
  fs.writeFileSync(path.join(out,'geometry.json'),JSON.stringify(results,null,2)+'\n');
 }finally{await browser.close();}
});
