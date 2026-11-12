const {test}=require('node:test'),assert=require('node:assert/strict');
const {launch,open,ID}=require('./lib/v11-panel-fixture.cjs');
const {audit}=require('./lib/v111-click-audit.cjs');
async function ui(fn){const b=await launch(),f=await open(b,{n:60,width:2048,height:1152});try{await fn(f);assert.deepEqual(f.errors,[]);}finally{await f.close();await b.close();}}
test('S1 YouTube observation ignores egress override and keeps CN red in both palettes; S3 native options are opaque and readable',()=>ui(async f=>{
 const p=f.page,now=Date.now(),[a,b]=f.f.state.items;f.f.state.youtube_ad_free_regions=['CN','MO'];a.plan.country_code_override='MO';a.google_status={supported:true,checked_at:now,result:{youtube:{status:'not_cn',region:'US',premium:'available'}}};b.plan.country_code_override='';b.google_status={supported:true,checked_at:now,result:{youtube:{status:'cn',region:'CN',premium:'unavailable'}}};
 await p.evaluate(()=>dashboardData.reload());await p.waitForFunction(()=>!renderFrame);
 const card=i=>p.locator(`.agent-card[data-agent-id="${ID(i)}"]`),styles=n=>n.evaluate(n=>{const s=getComputedStyle(n);return {color:s.color,border:s.borderTopColor,background:s.backgroundColor,padding:s.padding,borderWidth:s.borderTopWidth,radius:s.borderRadius};});
 for(const theme of ['light','dark']){await p.evaluate(t=>ProbeTheme.set(t),theme);const manual=card(0).locator('.check-result'),cn=card(1).locator('.check-result');assert.equal(await manual.textContent(),'地区 US');assert.equal(await manual.getAttribute('data-region-tone'),'ok');assert.equal(await cn.getAttribute('data-region-tone'),'crit');assert.notDeepEqual(await styles(manual),await styles(cn));
 const colors=await p.locator('select option').evaluateAll(nodes=>nodes.map(n=>{const s=getComputedStyle(n);return [s.color,s.backgroundColor];}));assert(colors.length>0);
 const lum=c=>{const rgb=c.match(/[\d.]+/g).slice(0,3).map(Number).map(v=>{v/=255;return v<=.04045?v/12.92:((v+.055)/1.055)**2.4;});return rgb[0]*.2126+rgb[1]*.7152+rgb[2]*.0722;};
 for(const [color,bg]of colors){assert(!bg.startsWith('rgba('),'option must be opaque: '+bg);assert((Math.max(lum(color),lum(bg))+.05)/(Math.min(lum(color),lum(bg))+.05)>=4.5);}
 console.log('OPTION_COLORS '+theme+' '+JSON.stringify([...new Set(colors.map(JSON.stringify))]));
 const cardColor=(await styles(manual)).background;await p.locator('[data-view=list]').click();await p.waitForFunction(()=>!renderFrame);const listColor=await p.locator(`.banner-row[data-agent-id="${ID(0)}"] .banner-youtube`).evaluate(n=>getComputedStyle(n).color);for(const color of [cardColor,listColor]){const [r,g,b]=color.match(/\d+/g).map(Number);assert(g>r&&g>b,'available YouTube region stays green: '+color);}await p.locator('[data-view=cards]').click();await p.waitForFunction(()=>!renderFrame);
 }
 assert.equal(await p.locator('.agent-card .banner-dot').count(),0);
}));
test('S2 toolbar resize and tag changes immediately move sticky header without a later render',()=>ui(async f=>{
 const p=f.page;await p.locator('[data-view=list]').click();await p.evaluate(()=>scrollTo(0,600));
 const check=async()=>{await p.waitForTimeout(100);const r=await p.evaluate(()=>({toolbar:document.querySelector('#dashboard-toolbar').getBoundingClientRect().bottom,sticky:document.querySelector('.banner-sticky').getBoundingClientRect().top}));assert(Math.abs(r.toolbar-r.sticky)<1,JSON.stringify(r));};
 for(const width of [1300,820,1300]){await p.setViewportSize({width,height:900});await check();}
 f.f.state.tags.push(...Array.from({length:16},(_,i)=>({id:ID(500+i),name:'长标签名称 '+i})));f.f.state.revision++;await p.evaluate(()=>dashboardData.reload());await p.waitForFunction(()=>!renderFrame);await check();
 await p.evaluate(()=>{document.querySelector('#dashboard-chips').style.minHeight='120px';});await check();await p.evaluate(()=>document.querySelector('#dashboard-chips').style.minHeight='0px');await check();
}));
test('B10 each control receives 50 mouse clicks under continuous SSE; theme and quick filters act every time',async()=>{
 const result=await audit({onResult:r=>console.log('CONTROL '+JSON.stringify(r))});assert.deepEqual(result.errors,[]);for(const r of result.results){assert.equal(r.hits,50,r.name);if('effects'in r)assert.equal(r.effects,50,r.name);}assert(result.events>50);
});
