// Real embedded front end; only same-origin telemetry and explicit operation receipts are synthetic.
'use strict';
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
module.exports=async({page,browser,current,ID,out,openDetails,tabs})=>{
 const capture=async(name,selector)=>{await page.locator(selector).screenshot({path:path.join(out,name+'.png')});};
 await openDetails();await capture('overview-system','#device-details');
 await page.getByRole('button',{name:'编辑',exact:true}).click();await page.locator('#plan-dialog').waitFor();await capture('Dialog-Plan','#plan-dialog');assert.equal(await page.locator('#plan-information').isVisible(),true);await page.keyboard.press('Escape');
 await page.getByRole('button',{name:'编辑配额',exact:true}).click();await capture('Dialog-Quota','#plan-dialog');assert.equal(await page.locator('#quota-information').isVisible(),true);await page.keyboard.press('Escape');
 console.log('Task two plan presentations smoke passed');
};
