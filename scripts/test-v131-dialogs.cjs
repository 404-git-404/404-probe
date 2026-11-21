// Every browser request is restricted by the shared fixture to loopback synthetic data.
'use strict';
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {launch,open,ID}=require('./lib/v11-panel-fixture.cjs');
const SCREENS=process.env.V131_SCREENS;
const testCases=[
 ['编辑套餐','#plan-dialog',`openPlanDialog(agents.get('${ID(0)}'))`,'#plan-renewal-price'],
 ['流量配额','#plan-dialog',`openPlanDialog(agents.get('${ID(0)}'),null,'quota')`,'#plan-traffic-enabled'],
 ['续费','#renewal-dialog',`planRenewal.open('${ID(0)}')`],
 ['详情','#device-details',`deviceDetails.open('${ID(0)}')`],
 ['分组和标签','#organization-dialog',`organizationDialogs.openAssignment('${ID(0)}')`,'#organization-tag-input'],
 ['管理分组和标签','#organization-manage-dialog',`organizationDialogs.openManage('groups')`,'#organization-create-name'],
 ['支出与汇率','#expense-dialog',`expenseDialog.open()`,'#expense-new-currency'],
 ['网络质量设置','#network-quality-settings',`networkQuality.openSettings('${ID(0)}')`,'[data-nq-field=threshold]'],
 ['出口选择','#selector-popover',`document.querySelector('.selector-trigger').click()`,'[data-selector-kind=choice]'],
 ['添加 Agent','#add-agent-dialog',`document.querySelector('#add-agent').click()`,'#add-agent-choice-name'],
 ['升级 Agent','#upgrade-agent-dialog',`openUpgradeAgentDialog(agents.get('${ID(0)}'))`,'#upgrade-release'],
 ['撤销 Agent','#remove-agent-dialog',`openRemoveAgentDialog(agents.get('${ID(0)}'))`,'#remove-agent-input'],
 ['操作确认','#action-confirm',`void DialogShell.confirm({title:'合成操作确认',message:'合成数据'})`],
];
const waitClosed=(p,s)=>p.waitForFunction(s=>!document.querySelector(s)?.open,s);
async function outside(p,d){const r=await d.boundingBox();assert(r);const v=p.viewportSize();await p.mouse.click(r.x>5?r.x-5:Math.min(v.width-1,r.x+r.width+5),r.y>5?r.y-5:Math.min(v.height-1,r.y+r.height/2));}
async function prompt(p){await p.getByRole('dialog',{name:'放弃修改？',exact:true}).waitFor();assert.equal(await p.locator('#action-confirm .shell-content').textContent(),'改动还没保存，关掉就没了。');}
async function keep(p){await p.getByRole('button',{name:'继续编辑',exact:true}).click();await p.locator('#action-confirm').waitFor({state:'detached'});}
async function drop(p,s){await p.getByRole('button',{name:'放弃修改',exact:true}).click();await waitClosed(p,s);await p.locator('#action-confirm').waitFor({state:'detached'});}
async function changed(p,input){if(await input.getAttribute('type')==='checkbox'){const old=await input.isChecked();await input.setChecked(!old);return {old,next:!old,current:()=>input.isChecked(),revert:()=>input.setChecked(old)};}const type=await input.evaluate(n=>n.tagName);const old=await input.inputValue();
 if(type==='SELECT'){const choices=await input.locator('option:not(:disabled)').evaluateAll(ns=>ns.map(n=>n.value));const next=choices.find(v=>v!==old);assert.notEqual(next,undefined);await input.selectOption(next);return {old,next,current:()=>input.inputValue(),revert:()=>input.selectOption(old)};}
 const next=old==='500'?'501':old+'7';await input.fill(next);return {old,next,current:()=>input.inputValue(),revert:()=>input.fill(old)};
}
async function run(theme,browser){
 const f=await open(browser,{n:8,theme,handle:async({req,url,body,json})=>{
  if(req.method==='PUT'&&/\/agents\/[a-f0-9]{32}\/plan$/.test(url.pathname)){const a=f.f.state.items.find(a=>url.pathname.includes(a.agent_id));a.plan={...a.plan,...body,agent_id:a.agent_id};json(a.plan);return true;}
  if(url.pathname.endsWith('/upgrade-targets')){json({targets:[{version:'v1.0.10',supported:true,channel:'stable'},{version:'v1.0.11-beta.1',supported:true,channel:'beta'}]});return true;}
  if(url.pathname.endsWith('/renewal/preview')){json({eligible:true,field:'renewal_date',from_date:'2026-11-24',to_date:'2027-11-24',unchanged_field:'expiry_date',unchanged_date:'',period:'yearly',timezone:'UTC',anchor_source:'current_date',expected_revision:'a'.repeat(32),new_overdue:false,due_today:false});return true;}
  return false;
 }});
 try{const p=f.page;for(const [i,a]of f.f.state.items.entries()){a.name='Synthetic-'+String(i+1).padStart(3,'0');a.plan.renewal_url=i%2?'':'https://billing.example.invalid/account';}await p.evaluate(()=>dashboardData.reload());await p.waitForFunction(()=>!renderFrame);
 const shot=async name=>{if(SCREENS){fs.mkdirSync(SCREENS,{recursive:true});await p.screenshot({path:path.join(SCREENS,theme+'-'+name+'.png')});}};
 for(const [name,selector,expression,field]of testCases){
  const launch=async()=>{await p.evaluate(code=>eval(code),expression);await p.locator(selector).waitFor({state:'visible'});if(selector==='#expense-dialog')await p.waitForFunction(()=>document.querySelector('#expense-monthly').textContent.startsWith('$'));};
  await launch();const d=p.locator(selector);if(name!=='详情')assert.equal(await d.locator('.shell-head').count(),1,name+' single header');assert.equal(await d.locator('.shell-head button,.details-header button,.nq-dialog-head button').evaluateAll(ns=>ns.filter(n=>/^[×✕✖]$/.test(n.textContent.trim())).length),0,name+' has no close icon');
  if(name==='详情')await shot('details');if(name==='续费'){await p.locator('#renewal-dates').waitFor();await shot('renewal-url');assert.equal(await p.locator('#renewal-go').getAttribute('title'),'billing.example.invalid · 新标签页打开');assert.equal(await p.locator('#renewal-go').getAttribute('rel'),'noopener noreferrer');assert.equal(await p.locator('#renewal-go').getAttribute('target'),'_blank');await p.locator('#renewal-go').focus();await p.keyboard.press('Tab');assert.equal(await p.evaluate(()=>document.activeElement.id),'renewal-dismiss');await p.keyboard.press('Tab');assert.equal(await p.evaluate(()=>document.activeElement.id),'renewal-confirm');}
  await p.keyboard.press('Escape');await waitClosed(p,selector);await launch();await outside(p,d);await waitClosed(p,selector);
  if(field){await launch();const input=d.locator(field).first();await input.waitFor({state:'visible'});await input.waitFor({state:'attached'});assert.equal(await input.isEnabled(),true,name+' editable field');const value=await changed(p,input);
   await outside(p,d);await prompt(p);if(name==='编辑套餐')await shot('discard-plan');await keep(p);assert.equal(await value.current(),value.next,name+' keep preserves draft');
   await p.keyboard.press('Escape');await prompt(p);await p.keyboard.press('Escape');await p.locator('#action-confirm').waitFor({state:'detached'});assert.equal(await value.current(),value.next);
   await p.keyboard.press('Escape');await prompt(p);await outside(p,p.locator('#action-confirm'));await p.locator('#action-confirm').waitFor({state:'detached'});assert.equal(await value.current(),value.next);
   await value.revert();await p.keyboard.press('Escape');await waitClosed(p,selector);
   await launch();await changed(p,d.locator(field).first());await p.keyboard.press('Escape');await prompt(p);await drop(p,selector);
   await launch();await changed(p,d.locator(field).first());const cancel=d.getByRole('button',{name:/^(取消|关闭)$/}).last();if(await cancel.count()){await cancel.click();await waitClosed(p,selector);assert.equal(await p.locator('#action-confirm[open]').count(),0,name+' explicit cancel does not prompt');}else {await p.keyboard.press('Escape');await prompt(p);await drop(p,selector);}
  }
  console.log('PASS '+theme+' '+name+' close, Esc, backdrop'+(field?', value comparison, keep, discard, cancel':''));
 }
 await p.evaluate(id=>planRenewal.open(id),ID(1));await p.locator('#renewal-dates').waitFor();assert.equal(await p.locator('#renewal-go').isVisible(),false);assert.equal(await p.getByText('还没填续费网址',{exact:false}).count(),0);await shot('renewal-no-url');await p.keyboard.press('Escape');
 // Details' custom range is a draft until applied; abandoning it must not survive reopening.
 const details= p.locator('#device-details');
 const openCustom=async()=>{await p.evaluate(id=>deviceDetails.open(id,'resources'),ID(0));if(!await details.locator('.details-custom').isVisible())await details.getByRole('button',{name:'自定义时间…',exact:true}).click();};
 const from=details.getByLabel('自定义开始时间'),to=details.getByLabel('自定义结束时间');
 const dates=await p.evaluate(()=>{const format=ms=>{const d=new Date(ms-Date.now()%1000);const two=v=>String(v).padStart(2,'0');return `${d.getFullYear()}-${two(d.getMonth()+1)}-${two(d.getDate())}T${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;};return {from:format(Date.now()-3600000),to:format(Date.now()-60000)};});
 await openCustom();await from.fill(dates.from);await p.keyboard.press('Escape');await prompt(p);await keep(p);assert.equal(await from.inputValue(),dates.from);await from.fill('');await p.keyboard.press('Escape');await waitClosed(p,'#device-details');
 await openCustom();await from.fill(dates.from);await outside(p,details);await prompt(p);await drop(p,'#device-details');await openCustom();assert.equal(await from.inputValue(),'');await from.fill(dates.from);await to.fill(dates.to);await details.getByRole('button',{name:'应用自定义范围',exact:true}).click();assert.equal(await details.locator('.details-range-error').textContent(),'');await p.keyboard.press('Escape');await waitClosed(p,'#device-details');assert.equal(await p.locator('#action-confirm[open]').count(),0);console.log('PASS '+theme+' details custom dates: keep, revert, discard clears draft, applied range closes without prompt');
 // A real pending save must neither dismiss nor show a discard prompt; success closes directly.
 let releaseSave,saveStarted;const readySave=new Promise(r=>saveStarted=r),saveGate=new Promise(r=>releaseSave=r);
 const saveRoute=async route=>{if(route.request().method()!=='PUT')return route.continue();saveStarted();await saveGate;await route.continue();};
 await p.route('**/api/v1/web/agents/*/plan',saveRoute);await p.evaluate(id=>openPlanDialog(agents.get(id)),ID(0));await p.locator('#plan-renewal-price').fill('99');await p.locator('#plan-save').click();await readySave;
 await p.keyboard.press('Escape');assert.equal(await p.locator('#plan-dialog').evaluate(n=>n.open),true);await outside(p,p.locator('#plan-dialog'));assert.equal(await p.locator('#plan-dialog').evaluate(n=>n.open),true);assert.equal(await p.locator('#action-confirm[open]').count(),0);releaseSave();await waitClosed(p,'#plan-dialog');assert.equal(await p.locator('#action-confirm[open]').count(),0);await p.unroute('**/api/v1/web/agents/*/plan',saveRoute);console.log('PASS '+theme+' pending real save cannot dismiss; successful save does not prompt');
 // The nested source picker follows the same actual-value and discard rules.
 await p.evaluate(id=>networkQuality.openSettings(id),ID(0));
 const openPicker=async()=>{await p.locator('[data-nq-field=copy]').click();await p.getByRole('dialog',{name:'从其他设备复制',exact:true}).waitFor();};
 await openPicker();const picker=p.getByRole('dialog',{name:'从其他设备复制',exact:true});const source=picker.locator('select');const draft=await changed(p,source);
 await outside(p,picker);await prompt(p);await keep(p);assert.equal(await draft.current(),draft.next);await draft.revert();await p.keyboard.press('Escape');await picker.waitFor({state:'detached'});
 await openPicker();await changed(p,picker.locator('select'));await p.keyboard.press('Escape');await prompt(p);await drop(p,'dialog[aria-label="从其他设备复制"]');
 await openPicker();await changed(p,picker.locator('select'));await picker.getByRole('button',{name:'取消',exact:true}).click();await picker.waitFor({state:'detached'});assert.equal(await p.locator('#action-confirm[open]').count(),0);
 await openPicker();await outside(p,picker);await picker.waitFor({state:'detached'});await p.locator('[data-nq-field=cancel]').click();console.log('PASS '+theme+' copy picker value comparison, keep, revert, discard, cancel and backdrop');
 // Nested copy picker and mobile shell are separate native dialogs.
 await p.evaluate(id=>networkQuality.openSettings(id),ID(0));await p.locator('[data-nq-field=copy]').click();const copy=p.getByRole('dialog',{name:'从其他设备复制',exact:true});await copy.waitFor();assert.equal(await copy.locator('button').filter({hasText:/^[×✕]$/}).count(),0);await p.keyboard.press('Escape');await copy.waitFor({state:'detached'});await p.keyboard.press('Escape');
 await p.setViewportSize({width:390,height:844});await p.locator('#panel-mobile-open').click();await p.keyboard.press('Escape');await waitClosed(p,'#panel-mobile-dialog');await p.locator('#panel-mobile-open').click();await outside(p,p.locator('#panel-mobile-dialog'));await waitClosed(p,'#panel-mobile-dialog');console.log('PASS '+theme+' copy picker and mobile panel');
 assert.deepEqual(f.errors,[]);
 }finally{await f.close();}
}
(async()=>{const b=await launch();try{for(const theme of ['light','dark'])await run(theme,b);}finally{await b.close();}})().catch(e=>{console.error(e);process.exitCode=1;});
