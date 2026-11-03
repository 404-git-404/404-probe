const {test,before,after}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {launch}=require('./lib/v11-panel-fixture.cjs'),{open}=require('./lib/v11-quota-browser-fixture.cjs'),{bounds}=require('./lib/v11-quota-cases.cjs');
let browser;before(async()=>browser=await launch());after(async()=>browser?.close());
async function ui(options,run){const f=await open(browser,options);try{await run(f);assert.deepEqual(f.errors,[]);}finally{await f.close();}}
const shot=async(p,name)=>{if(process.env.V11_QUOTA_SCREENS){fs.mkdirSync(process.env.V11_QUOTA_SCREENS,{recursive:true});await p.screenshot({path:path.join(process.env.V11_QUOTA_SCREENS,name+'.png')});}};
test('opening and changing monthly day use plan-local today across both browser/plan date boundaries',async()=>{
 for(const [now,browserTimezone,planTimezone,today,day,anchor] of [
  ['2026-03-15T00:30:00Z','Asia/Shanghai','America/Los_Angeles','2026-03-14',15,'2026-02-15'],
  ['2026-03-15T20:30:00Z','America/Los_Angeles','Asia/Shanghai','2026-03-16',16,'2026-03-16']
 ])await ui({now,browserTimezone,planTimezone},async({page:p})=>{
  await p.locator('.c7-quota:visible,.c7-quota-empty:visible').click();assert.equal(await p.locator('#plan-cycle-anchor').inputValue(),today);
  await p.locator('#quota-cycle-day').fill(String(day));const actual=await p.locator('#plan-cycle-anchor').inputValue();assert.equal(actual,anchor);
  const [period]=bounds([{day,now,timezone:planTimezone,anchor:actual}]);assert.equal(period.starts.length,13);assert(period.start<=today&&today<period.end);
 });
});
test('March 15 monthly 31st stays January 31 and shows the Server current February–March cycle',async()=>{
 const [period]=bounds([{day:31,now:'2026-03-15T12:00:00Z',timezone:'UTC',anchor:'2026-01-31'}]);
 assert.equal(period.start,'2026-02-28');assert.equal(period.end,'2026-03-31');
 for(const theme of ['light','dark'])await ui({anchor:'2026-01-31',period,theme},async({page:p})=>{
  await p.locator('.c7-quota:visible,.c7-quota-empty:visible').click();assert.equal(await p.locator('#quota-cycle-day').inputValue(),'31');
  await p.locator('#quota-cycle-day').fill('30');assert.equal(await p.locator('#plan-cycle-anchor').inputValue(),'2026-01-30');
  await p.locator('#quota-cycle-day').fill('31');assert.equal(await p.locator('#plan-cycle-anchor').inputValue(),'2026-01-31');
  assert.match(await p.locator('#quota-cycle-summary').textContent(),/当前周期 02-28 – 03-31/);
  await p.locator('#quota-cycle-options').click();await shot(p,'N1-monthly31-march-'+theme);
 });
});
test('invalid timezone retains the original anchor, shows an adjacent hint and lets Server reject saving',async()=>{
 for(const theme of ['light','dark'])await ui({anchor:'2026-01-31',theme},async({page:p,writes})=>{
  await p.locator('.edit-plan').click();await p.locator('.plan-more summary').click();await p.locator('#plan-timezone').fill('Not/AZone');
  assert.equal(await p.locator('#plan-timezone').getAttribute('aria-invalid'),'true');assert(await p.locator('#plan-timezone-error').isVisible());
  await p.evaluate(()=>setMonthlyQuotaDay());assert.equal(await p.locator('#plan-cycle-anchor').inputValue(),'2026-01-31');
  await p.locator('#plan-timezone-error').scrollIntoViewIfNeeded();await shot(p,'N2-invalid-timezone-'+theme);await p.locator('#plan-save').click();await p.locator('#plan-error:not(.hidden)').waitFor();
  assert.equal(writes.length,1);assert.equal(writes[0].timezone,'Not/AZone');assert.equal(writes[0].cycle_anchor,'2026-01-31');
  await p.locator('#plan-timezone').fill('UTC');assert.equal(await p.locator('#plan-timezone').getAttribute('aria-invalid'),'false');assert(await p.locator('#plan-timezone-error').isHidden());
 });
 await ui({anchor:'2026-01-31',planTimezone:'Not/AZone'},async({page:p})=>{
  await p.locator('.c7-quota:visible,.c7-quota-empty:visible').click();await p.locator('#quota-cycle-day').fill('30');assert.equal(await p.locator('#plan-cycle-anchor').inputValue(),'2026-01-31');
  await p.locator('#quota-cycle-options').click();assert.match(await p.locator('#quota-timezone').textContent(),/时区无效/);
 });
});
