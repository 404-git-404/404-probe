const fs=require('fs'),path=require('path');
const source=path.resolve(process.env.V111_SOURCE||path.join(__dirname,'..')),out=path.resolve(process.env.V111_SCREENS||(()=>{throw Error('Set V111_SCREENS to screenshot output directory');})()),side=process.env.V111_SIDE||'after';
const {launch,open,ID}=require(path.join(source,'scripts/lib/v11-panel-fixture.cjs'));
(async()=>{fs.mkdirSync(out,{recursive:true});const browser=await launch(),manifest=[];try{for(const theme of ['light','dark']){
 const x=await open(browser,{width:2048,height:1152,deviceScaleFactor:1.25,n:60,theme}),p=x.page;
 try{const now=Date.now();x.f.state.items.forEach((a,i)=>{a.name='Synthetic-'+String(i+1).padStart(3,'0');a.state.hostname='synthetic-'+i;a.search_fields=[a.name];a.online=true;a.disabled_at=null;a.public_egress_check.enabled=true;a.state.public_egress={ipv4:{status:'available',ip:'203.0.113.42',checked_at:now,valid_until:now+86400000},...(i===1?{ipv6:{status:'available',ip:'2001:db8::42',checked_at:now,valid_until:now+86400000}}:{})};if(i===2)a.state.public_egress={};});
 const [a,b]=x.f.state.items;a.plan.country_code_override='MO';a.country_code='MO';a.country_source='manual';a.google_status={supported:true,checked_at:now,result:{youtube:{status:'not_cn',region:'US',premium:'available'}}};b.plan.country_code_override='';b.google_status={supported:true,checked_at:now,result:{youtube:{status:'cn',region:'CN',premium:'unavailable'}}};
 await p.evaluate(()=>dashboardData.reload());await p.waitForFunction(()=>!renderFrame);await p.waitForTimeout(200);
 async function shot(scene){const file=`${side}-${theme}-${scene}.png`;await p.screenshot({path:path.join(out,file)});manifest.push({side,theme,scene,file});}
 await shot('S1-S4-card-header');
 await p.locator('[data-view=list]').click();await p.waitForFunction(()=>!renderFrame);
 x.f.state.tags.push(...Array.from({length:16},(_,i)=>({id:ID(500+i),name:'长标签名称 '+i})));x.f.state.revision++;await p.evaluate(()=>dashboardData.reload());await p.waitForFunction(()=>!renderFrame);
 await p.evaluate(()=>{const p=dashboardUI.preferences.value;p.tags=dashboardData.catalog().tags.map(t=>t.id);render();});await p.waitForFunction(()=>!renderFrame);await p.evaluate(()=>scrollTo(0,600));await p.waitForTimeout(250);await shot('S2-sticky-tags');
 console.log(side+' '+theme+' '+JSON.stringify(await p.evaluate(()=>({toolbar:document.querySelector('#dashboard-toolbar').getBoundingClientRect().bottom,sticky:document.querySelector('.banner-sticky').getBoundingClientRect().top}))));
 if(x.errors.length)throw Error(JSON.stringify(x.errors));
 }finally{await x.close();}
 }fs.writeFileSync(path.join(out,side+'-manifest.json'),JSON.stringify(manifest,null,2));
 const html='<!doctype html><meta charset="utf-8"><title>v1.1.1 修复2 对照</title><style>body{font:16px system-ui;background:#eee;margin:24px}section{margin-bottom:32px}.pair{display:flex;gap:12px}.pair figure{width:50%;margin:0}img{width:100%}figcaption{padding:8px}a{color:#1459a5}</style><h1>v1.1.1 修复2：改前 / 改后</h1><p>改前 cd481f9；2048×1152 视口 ×1.25 = 2560×1440。S1 比较首排 MO / CN；S4 首排依次仅 V4 / 双族 / 无 IP；卡片头部检查圆点。S3 原生菜单请人工核对，computed style 见报告。</p>'+['light','dark'].flatMap(theme=>['S1-S4-card-header','S2-sticky-tags'].map(scene=>`<section><h2>${theme} · ${scene}</h2><div class="pair">${['before','after'].map(side=>`<figure><a href="${side}-${theme}-${scene}.png"><img src="${side}-${theme}-${scene}.png"></a><figcaption>${side==='before'?'改前 cd481f9':'改后'}</figcaption></figure>`).join('')}</div></section>`)).join('');fs.writeFileSync(path.join(out,'index.html'),html);
 }finally{await browser.close();}})().catch(e=>{console.error(e);process.exitCode=1;});
