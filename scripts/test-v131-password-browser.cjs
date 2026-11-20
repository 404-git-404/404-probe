const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {launch,open}=require('./lib/v11-panel-fixture.cjs');
(async()=>{const b=await launch();try{for(const theme of ['light','dark']){
 const posts=[];const f=await open(b,{n:1,theme,handle:async({req,url,body,json})=>{
 if(url.pathname.endsWith('/youtube-ad-free-regions')){json({regions:[],defaults:[]});return true;}
 if(!url.pathname.endsWith('/settings/password'))return false;
 assert.deepEqual(Object.keys(body).sort(),['confirm_password','current_password','new_password']);posts.push(body.new_password.length);
 if([...body.new_password].length<12)json({error:{code:'too_short',characters:[...body.new_password].length}},400);else json({});return true;
 }});
 try{const p=f.page;await p.goto(f.f.base+'/settings.html');await p.locator('#pw-save').waitFor({state:'visible'});await p.clock.install();
 const user=p.locator('#password-form [autocomplete=username]');assert.equal(await user.inputValue(),'404-probe');assert.equal(await user.getAttribute('name'),null);assert.equal(await user.isVisible(),false);assert.equal(await user.getAttribute('tabindex'),'-1');assert.equal(await user.getAttribute('aria-hidden'),'true');
 for(const [id,ac]of [['current','current-password'],['new','new-password'],['confirm','new-password']])assert.equal(await p.locator('#pw-'+id).getAttribute('autocomplete'),ac);
 await p.locator('#password').scrollIntoViewIfNeeded();
 const shot=async name=>{if(process.env.V131_SCREENS){fs.mkdirSync(process.env.V131_SCREENS,{recursive:true});await p.screenshot({path:path.join(process.env.V131_SCREENS,theme+'-password-'+name+'.png')});}};
 assert.equal(await p.locator('#pw-new-msg').textContent(),'至少 12 个字符');await shot('hint-12');
 await p.locator('#pw-current').fill('synthetic-current');for(const field of ['new','confirm'])await p.locator('#pw-'+field).fill('a'.repeat(11));await p.locator('#pw-save').click();await p.waitForFunction(()=>document.querySelector('#pw-new-msg').textContent==='太短了：至少 12 个字符，现在 11 个');await shot('error-11');
 for(const field of ['new','confirm'])await p.locator('#pw-'+field).fill('密'.repeat(12));await p.locator('#pw-save').click();await p.waitForFunction(()=>document.querySelector('#pw-banner').textContent==='密码已修改。所有浏览器都已退出，3 秒后跳到登录页。');await shot('accepted-12');await shot('success');assert.equal(await p.locator('#pw-save').isDisabled(),true);
 await p.route('**/login',r=>r.fulfill({contentType:'text/html',body:'<title>合成登录</title>'}));await p.clock.fastForward(2999);assert(new URL(p.url()).pathname==='/settings.html');await p.clock.fastForward(1);await p.waitForURL('**/login');assert.deepEqual(posts,[11,12]);assert.deepEqual(f.errors,[]);console.log('PASS '+theme+' password DOM, exact request keys, 11/12 code points, banner and 3-second redirect');
 }finally{await f.close();}
}}finally{await b.close();}})().catch(e=>{console.error(e);process.exitCode=1;});
