const {start,ID}=require('./v110-dashboard-fixture.cjs');
async function open(browser,{now='2026-03-15T12:00:00Z',browserTimezone='UTC',planTimezone='UTC',anchor='',period=null,theme='light'}={}){
 const writes=[],f=await start({n:1,handle:({req,url,body,fail})=>{
  if(req.method==='PUT'&&url.pathname.endsWith('/plan')){writes.push(body);fail('invalid_billing_timezone');return true;}
  return false;
 }});
 const plan=f.state.items[0].plan;Object.assign(plan,{timezone:planTimezone,cycle_anchor:anchor,cycle_start:period?Date.parse(period.start+'T00:00:00Z'):null,cycle_end:period?Date.parse(period.end+'T00:00:00Z'):null});
 const context=await browser.newContext({viewport:{width:1440,height:1100},locale:'zh-CN',timezoneId:browserTimezone}),page=await context.newPage(),errors=[];
 page.setDefaultTimeout(12000);page.on('pageerror',e=>errors.push(e.message));await page.clock.setFixedTime(new Date(now));
 await page.addInitScript(theme=>{localStorage.setItem('404-probe.theme.v1',theme);localStorage.setItem('404-probe.dashboard.v1',JSON.stringify({version:1,viewMode:'cards'}));},theme);
 await context.route('**/*',r=>new URL(r.request().url()).origin===f.base?r.continue():r.abort());
 await page.goto(f.base);await page.waitForFunction(()=>dashboardData.ready()&&!renderFrame&&document.querySelector('.agent-card'));
 return {page,errors,writes,id:ID(0),async close(){await context.close();f.close();}};
}
module.exports={open};
