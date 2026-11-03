// A dedicated visible Chromium window. Native Ctrl +/- is supplied by Computer Use.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {open}=require('./lib/v11-panel-fixture.cjs');
const {chromium}=require(process.env.M02_PLAYWRIGHT||'C:/Users/ufoli/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');
const out=path.resolve(process.argv[2]);fs.mkdirSync(out,{recursive:true});
(async()=>{const b=await chromium.launch({headless:false,executablePath:process.env.M02_CHROMIUM||'C:/Users/ufoli/AppData/Local/ms-playwright/chromium-1228/chrome-win64/chrome.exe',args:['--window-size=1920,1100']}),f=await open(b,{theme:'dark',width:1920,height:1000,preferences:{viewMode:'list'}}),p=f.page,seen=new Set(),facts=[];
 try{await p.evaluate(()=>document.title='404-probe · Fix Native Zoom QA');const baseline=await p.evaluate(()=>devicePixelRatio);
 for(let i=0;i<1200&&seen.size<5;i++){
  const fact=await p.evaluate(()=>({ratio:devicePixelRatio,width:innerWidth,height:innerHeight,overflow:document.documentElement.scrollWidth>innerWidth,buttons:[...document.querySelectorAll('.theme-toggle,#panel-settings,#logout')].map(n=>({id:n.id||n.className,width:n.getBoundingClientRect().width,height:n.getBoundingClientRect().height})),columns:[...document.querySelectorAll('.banner-header>.banner-cell')].map(n=>({column:n.dataset.column,width:n.getBoundingClientRect().width}))}));
  for(const scale of [.5,2/3,.8,1.25,1.5])if(!seen.has(scale)&&Math.abs(fact.ratio/baseline-scale)<.015){const label=Math.round(scale*100);await p.waitForTimeout(250);await p.screenshot({path:path.join(out,`dark-native-zoom-${label}.png`)});facts.push({...fact,baseline,native_zoom:scale});fs.writeFileSync(path.join(out,'native-zoom.json'),JSON.stringify(facts,null,2));seen.add(scale);console.log('saved '+label);assert(!fact.overflow);for(const v of fact.buttons)assert(Math.abs(v.width-v.height)<1);for(const v of fact.columns.filter(v=>['cpu','memory','disk'].includes(v.column)))assert(v.width<110);}
  await new Promise(r=>setTimeout(r,300));
 }
 assert.equal(seen.size,5);assert.deepEqual(f.errors,[]);
 }finally{await f.close();await b.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
