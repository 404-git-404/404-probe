// Loopback synthetic data only. Run once for each exact source checkout.
const fs=require('node:fs'),path=require('node:path');
const source=path.resolve(process.env.V112_SOURCE||path.join(__dirname,'..')),out=path.resolve(process.env.V112_SCREENS),side=process.env.V112_SIDE||'after';
const {launch,open,ID}=require(path.join(source,'scripts/lib/v11-panel-fixture.cjs'));
const scenes=['F1-cards','F1-list','F2-network','F2-latency','F2-name','F2-egress','F2-youtube','F3-hover','F3-details','F4-result'];
(async()=>{fs.mkdirSync(out,{recursive:true});const browser=await launch(),manifest=[];try{for(const theme of ['light','dark']){
 const x=await open(browser,{width:2048,height:1152,deviceScaleFactor:1.25,n:6,theme}),p=x.page;
 try{
 const now=Date.now();x.f.state.items.forEach((a,i)=>{a.name='Synthetic-'+String(i+1).padStart(3,'0');a.state.hostname='synthetic-'+i;a.search_fields=[a.name];a.online=true;a.disabled_at=null;a.public_egress_check.enabled=true;a.state.public_egress={ipv4:{status:'available',ip:'203.0.113.42',checked_at:now,valid_until:now+86400000}};a.country_code='US';a.google_status={supported:true,checked_at:now,next_due_at:now+86400000,result:{youtube:{status:'not_cn',region:'US'}}};});
 const [a,b]=x.f.state.items;a.plan.country_code_override='MO';a.country_code='MO';a.country_source='manual';b.google_status.result.youtube={status:'cn',region:'CN'};
 await p.evaluate(()=>dashboardData.reload());await p.waitForFunction(()=>!renderFrame);await p.waitForTimeout(250);
 const card=p.locator(`.agent-card[data-agent-id="${ID(0)}"]`);
 async function shot(scene){const file=`${side}-${theme}-${scene}.png`;await p.screenshot({path:path.join(out,file)});manifest.push({side,theme,scene,file});}
 await shot('F1-cards');await p.locator('[data-view=list]').click();await p.waitForFunction(()=>!renderFrame);await shot('F1-list');await p.locator('[data-view=cards]').click();await p.waitForFunction(()=>!renderFrame);
 for(const [scene,selector]of [['F2-network','.nq7-b[data-i="10"]'],['F2-latency','.lm-plot'],['F2-name','h2'],['F2-egress','[data-family=ipv4]'],['F2-youtube','.check-result']]){if(scene==='F2-latency')await card.locator('.nq7-title').click();await card.locator(selector).first().hover();await p.waitForTimeout(150);await shot(scene);await p.mouse.move(0,0);}
 await p.locator('[data-view=list]').click();await p.waitForFunction(()=>!renderFrame);const cell=p.locator(`.banner-row[data-agent-id="${ID(0)}"] [data-column=upgrade]`);const box=await cell.boundingBox();await p.mouse.move(box.x+3,box.y+3);await shot('F3-hover');await cell.locator('button').click();await p.locator('[data-upgrade-region]').waitFor();await shot('F3-details');
 // Only detail returns the operation, exactly like the real Server.
 await p.route('**/api/v1/web/dashboard*',async route=>{const response=await route.fetch(),data=await response.json();for(const row of data.items)delete row.country_code_lookup_operation;await route.fulfill({response,json:data});});
 await p.route('**/country-code/refresh',async route=>{a.country_code_lookup_operation={operation_id:'synthetic-operation',status:'requested',updated_at:Date.now()};await route.fulfill({status:202,json:a.country_code_lookup_operation});});
 await p.locator('.country-lookup-controls button').click();await p.locator('#action-confirm .primary-action').click();await p.waitForFunction(()=>document.querySelector('.country-lookup-controls button').textContent==='识别中…');a.country_code_lookup_operation={...a.country_code_lookup_operation,status:'succeeded',result_code:'JP'};await p.waitForTimeout(3000);await shot('F4-result');
 if(x.errors.length)throw Error(JSON.stringify(x.errors));
 }finally{await x.close();}
 }fs.writeFileSync(path.join(out,side+'-manifest.json'),JSON.stringify(manifest,null,2));
 const html='<!doctype html><meta charset="utf-8"><title>v1.1.2 修复对照</title><style>body{font:16px system-ui;background:#eee;margin:24px}.pair{display:flex;gap:12px}figure{width:50%;margin:0}img{width:100%}figcaption{padding:8px}</style><h1>v1.1.2 改前 / 改后</h1><p>改前 4e0e7b987；回环合成数据。视口 2048×1152，缩放 1.25，原图 2560×1440。F1：Synthetic-001 自动 US / 手动 MO，Synthetic-002 检测 CN。F3 在单元格左上边缘悬停；F4 结果返回后等待 3 秒。原生 title 弹层不在截图内。</p>'+['light','dark'].flatMap(theme=>scenes.map(scene=>`<section><h2>${theme} · ${scene}</h2><div class="pair">${['before','after'].map(s=>`<figure><a href="${s}-${theme}-${scene}.png"><img src="${s}-${theme}-${scene}.png"></a><figcaption>${s==='before'?'改前':'改后'}</figcaption></figure>`).join('')}</div></section>`)).join('');fs.writeFileSync(path.join(out,'index.html'),html);
 }finally{await browser.close();}})().catch(e=>{console.error(e);process.exitCode=1;});
