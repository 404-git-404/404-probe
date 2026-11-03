// Loopback-only synthetic UI fixture. Every credential below is a test sentinel.
const {start,ID}=require('./v110-dashboard-fixture.cjs');
const {chromium}=require(process.env.M02_PLAYWRIGHT||'C:/Users/ufoli/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');
const launch=()=>chromium.launch({headless:true,executablePath:process.env.M02_CHROMIUM||'C:/Users/ufoli/AppData/Local/ms-playwright/chromium-1228/chrome-win64/chrome.exe'});
async function open(browser,{width=1920,height=1080,theme='light',n=28,preferences=null,deviceScaleFactor=1}={}){
 let settingsState;const f=await start({n,handle:({req,url,body,json,fail})=>{if(url.pathname!=='/api/v1/web/settings/update-check')return false;settingsState.writes.push({headers:req.headers,body});if(settingsState.failSettings||req.headers['x-csrf-token']!=='synthetic-csrf'||req.headers['sec-fetch-site']!=='same-origin'||req.headers.origin!==f.base)fail('forbidden',403);else{settingsState.update={...settingsState.update,enabled:body.enabled,newer:body.enabled};json(settingsState.update);}return true;}}),context=await browser.newContext({viewport:{width,height},deviceScaleFactor,locale:'zh-CN',permissions:['clipboard-read','clipboard-write']}),page=await context.newPage(),errors=[];
 const state={update:{enabled:true,newer:true,latest:'v1.1.1',checked_at:1},failSettings:false,enrollments:0,revokes:0,writes:[],token:'S'.repeat(43),manual:'synthetic-manual-credential'};
 settingsState=state;page.setDefaultTimeout(10000);page.on('pageerror',e=>errors.push(e.message));
 await page.addInitScript(({theme,preferences})=>{localStorage.setItem('404-probe.theme.v1',theme);if(preferences&&!localStorage.getItem('404-probe.dashboard.v1'))localStorage.setItem('404-probe.dashboard.v1',JSON.stringify({version:1,...preferences}));},{theme,preferences});
 await context.route('**/*',async r=>{
  const u=new URL(r.request().url()),method=r.request().method();if(u.origin!==f.base)return r.abort();
  if(u.pathname==='/api/v1/web/version')return r.fulfill({json:{version:'v1.0.10',upgrade_eligible:true,one_click_install:true,update:state.update}});
  if(u.pathname==='/api/v1/web/agent-enrollments'){
   state.enrollments++;state.name=r.request().postDataJSON().name;
   state.command=`curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.10/install.sh | PROBE_404_ROLE=agent PROBE_404_SERVER='${f.base}' PROBE_404_ENROLL_TOKEN='${state.token}' PROBE_404_AGENT_NAME='${state.name}' bash`;
   return r.fulfill({json:{enrollment_id:'e'.repeat(32),command:state.command,expires_at:(await page.evaluate(()=>Date.now()))+30*60000}});
  }
  if(u.pathname.endsWith('/revoke')){state.revokes++;return r.fulfill({json:{revoked:true}});}
  if(u.pathname==='/api/v1/web/agents'&&method==='POST')return r.fulfill({json:{agent_id:ID(999),enrollment_value:state.manual,install_available:true,install_command:`curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.10/install.sh | bash`}});
  return r.continue();
 });
 await page.goto(f.base);await page.waitForFunction(n=>typeof dashboardData!=='undefined'&&dashboardData.ready()&&agents.size===n&&!renderFrame&&document.querySelector('#dashboard-count').textContent.includes(' / '),n);
 await page.waitForFunction(()=>!document.querySelector('#add-agent').disabled);
 return {page,context,f,state,errors,async close(){await context.close();f.close();}};
}
module.exports={launch,open,ID};
