'use strict';
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {start,ID}=require('./lib/v110-dashboard-fixture.cjs');
const {load,v5Card}=require('./lib/r07-harness.cjs');

const HOUR=3600000, DAY=24*HOUR, TZ=8*HOUR;
const now=Date.UTC(2026,10,22,12,0);
const slots=['telecom','unicom','mobile'];
const carriers=['电信','联通','移动','IIJ','NTT','PCCW','HE','Telia'];
const regions=['上海','广州','北京','东京','大阪','香港','洛杉矶','法兰克福'];
const base=load('v4-running');
const object=(i,other=false)=>({
  ...base.slots[0],slot:slots[i]||'obj-'+String(i).padStart(16,'0'),kind:'custom',
  region:other&&i===1?'深圳':regions[i],isp:carriers[i],host:['ct-sh','cu-gz','cm-bj','jp-kix','jp-nrt','hk','us','de'][i]+'.example.net',
  port:443,slot_enabled:true,show_on_card:i<3
});
const machineName=i=>'Synthetic-'+String(i+1).padStart(3,'0');
const bucket=(i,j,time,family,protocol)=>{
  const hour=((time+TZ)/HOUR)%24;
  const evening=Math.max(0,1-Math.abs(hour-21.5)/3.7);
  const daily=1+Math.sin(Math.floor(time/DAY)*2.67);
  const noise=Math.sin(time/500000+i+j)*5+Math.sin(time/80000+j)*2+Math.cos(time/31000+j)*2;
  const gap=(i===0&&time>now-13.4*HOUR&&time<now-12.6*HOUR)||(i===3&&time>now-10*HOUR)||(i===5&&time>now-2*DAY);
  const congested=i===2&&j===2&&time>now-2.2*HOUR;
  const p50=150+j*16+i*9+(family==='ipv6'?14:0)-(protocol==='icmp'?12:0)+noise+evening*(28+j*7)*daily+(congested?501:0);
  const ratio=Math.max(0,.1+(noise+9)*.07+evening*2*daily+(congested?30:0));
  const old=protocol==='tcp'&&i===2&&time<now-4*HOUR;
  const loss=gap||old?null:protocol==='icmp'?ratio/2:ratio;
  const state=gap?'gray':loss>=20||p50>1000?'red':loss>0||p50>500?'yellow':'green';
  const reason={gray:'no_data',red:'loss_red',yellow:'loss_yellow',green:'normal'}[state];
  return {state,reason,old_tcp:old,samples:gap?0:6,p50_ms:gap?null:p50,p95_ms:gap?null:p50+Math.abs(noise)*4+10,
    max_ms:gap?null:p50+80,loss_count:loss==null?0:Math.ceil(loss/100*6),loss_attempts:loss==null?0:6,
    error_count:0,error_attempts:protocol==='tcp'&&!gap?6:0,error_percent:protocol==='tcp'&&!gap?0:null,
    mean_ms:gap?null:p50+4,jitter_ms:gap?null:Math.abs(noise)+1,
    packet_loss_percent:loss,
    categories:!gap&&ratio>3?{timeout:3,refused:1}:{}};
};
const alerts=i=>i===2?[{
  slot:'mobile',family:'ipv4',protocol:'tcp',target_id:'a'.repeat(32),start:now-2.2*HOUR,end:null,duration_ms:2.2*HOUR,
  worst_p95_ms:1180,worst_failure_percent:null,worst_packet_loss_percent:34,reason:'loss'
},...[2,9,21].map((days,j)=>({
  slot:slots[j],family:'ipv4',protocol:'tcp',target_id:'b'.repeat(32),start:now-days*DAY-HOUR,end:now-days*DAY,
  duration_ms:HOUR,worst_p95_ms:760-j*120,worst_failure_percent:22-j*5,worst_packet_loss_percent:null
}))]:[];
const outDir=process.env.S3B_SCREENSHOTS;
function range(params){
  const lengths={'1h':HOUR,'24h':DAY,'7d':7*DAY,'30d':30*DAY};
  return params.has('start')?[Number(params.get('start')),Number(params.get('end'))]:[now-lengths[params.get('window')],now];
}
function configFor(slots){
  return {...base,slots,lanes:slots.flatMap(o=>base.lanes.slice(0,4).map(l=>({...l,slot:o.slot})))};
}

test('R39 browser network views, charts and eight-object settings',async t=>{
  const {chromium}=require(process.env.M02_PLAYWRIGHT);
  const browser=await chromium.launch({headless:true,executablePath:process.env.M02_CHROMIUM});
  let config=configFor([object(0),object(1),object(2)]);
  let revision=10, lastSave, fleetSize=8, detailCount=5;
  const fixture=await start({n:8,handle({req,url,json,body}){
    const params=url.searchParams,family=params.get('family'),protocol=params.get('protocol');
    const [start,end]=range(params);
    if(url.pathname==='/api/v1/web/network-quality/fleet'){
      const id=params.get('agent_id'),indices=id?[parseInt(id,16)-1]:Array.from({length:fleetSize},(_,i)=>i);
      json({start,end,now,machines:indices.map(i=>{
        const objects=i===0&&id?Array.from({length:detailCount},(_,j)=>object(j)):[object(0),object(1,i===4),object(2)];
        if(i===1){
          objects[0].ipv4_tcp=false;
          objects[0].ipv6_tcp=true;
          objects[1].ipv4_tcp=true;
          objects[1].ipv4_icmp=false;
          objects[2].ipv4_tcp=true;
        }
        return {agent_id:ID(i),name:machineName(i),server_local:i===0,country_code:['HK','US','DE','US','US','JP','MO','JP'][i],
          status:i===3?'offline':i===5?'paused':'online',last_seen:now-(i===5?2*DAY:i===3?10*60000:8000),
          offline_since:i===3?now-10*60000:null,slow_threshold_ms:500,alerts_30d:alerts(i).length,current_alerts:i===2?1:0,
          quality_clock_skewed:i===0,quality_clock_offset_ms:i===0?66000:null,objects,
          lines:objects.map((o,j)=>{
            const missing=i===1&&j===2&&family==='ipv4'&&protocol==='tcp';
            const stats=bucket(missing?5:i,j,end-HOUR,family,protocol);
            const alert=protocol==='tcp'&&family==='ipv4'&&j===2?alerts(i)[0]||null:null;
            return {slot:o.slot,family,protocol,stats,evening_p50_ms:stats.p50_ms&&stats.p50_ms*1.23,latest_p50_ms:stats.p50_ms,
              latest_bucket_at:now-60000,status:i===3||i===5||!o[family+'_'+protocol]||missing?'no_data':alert?'alert':i===4&&j===1?'slow':'normal',alert,
              spark:Array.from({length:48},(_,k)=>({start:start+(end-start)*k/48,p50_ms:bucket(i,j,start+(end-start)*k/48,family,protocol).p50_ms}))};
          })};
      })});
      return true;
    }
    const match=url.pathname.match(/^\/api\/v1\/web\/agents\/([a-f0-9]{32})\/network-quality\/(.+)$/);
    if(!match)return false;
    const i=parseInt(match[1],16)-1,suffix=match[2],slot=params.get('slot');
    const j=slots.includes(slot)?slots.indexOf(slot):Number(slot?.slice(4)||0);
    if(suffix==='range'){
      const resolution=now-start<=DAY?60000:now-start<=7*DAY?300000:HOUR,points=[];
      for(let t=Math.floor(start/resolution)*resolution;t<end;t+=resolution)points.push({...bucket(i,j,t,family,protocol),start:t,end:t+resolution});
      const change=i===0&&j===1?now-3*DAY:null;
      const semantics=i===2&&j===2&&protocol==='tcp'?now-4*HOUR:null;
      json({slot,start,end,resolution_ms:resolution,points,last_target_change_at:change,
        semantics_change_at:semantics>=start&&semantics<end?semantics:null,
        target_changes:change&&start<=change&&change<end?[{at:change,from_target_id:'old',to_target_id:'new'}]:[],gaps:[]});
      return true;
    }
    if(suffix==='heatmap'){
      const days=Number(params.get('days')),midnight=Math.floor((now+TZ)/DAY)*DAY-TZ;
      json({slot,timezone:'Asia/Shanghai',days:Array.from({length:days},(_,d)=>{
        const day=midnight-(days-1-d)*DAY;
        return {date:new Date(day+TZ).toISOString().slice(0,10),hours:Array.from({length:24},(_,hour)=>{
          const t=day+hour*HOUR;
          return {hour,start:t,...(t<=now?bucket(i,j,t,family,protocol):{samples:0})};
        })};
      })});
      return true;
    }
    if(suffix==='alerts'){json(alerts(i));return true;}
    if(i===0&&suffix==='v5/card'){
      const card=v5Card(config,params.get('family')||'ipv4',now);
      const tcp=card.lanes[0].tcp;
      tcp.alert_runs=[{start:card.window_start+13*180000,end:card.window_start+18*180000}];
      tcp.semantics_change_at=card.window_start+4*180000+60000;
      for(const [index,b] of tcp.blocks.entries()){
        const state=index===1?'gray':index===8?'yellow':index===13||index===15?'red':'green';
        const loss=state==='red'?2:state==='yellow'?1:0;
        const old=index<5;
        b.state=state;
        b.metrics={samples:state==='gray'?0:6,p50_ms:state==='gray'?null:state==='red'?1024:24,
          max_ms:state==='gray'?null:state==='red'?1350:85,old_tcp:old,
          packet_loss_percent:old?null:loss*100/6,loss_count:loss,loss_attempts:old?0:6,
          error_count:0,error_attempts:6,error_percent:0,
          reason:{green:'normal',yellow:'loss_yellow',red:'latency_red',gray:'no_data'}[state]};
      }
      json(card);return true;
    }
    if(i===0&&suffix==='v4/config'){
      if(req.method==='PUT'){
        lastSave=body;
        config={...configFor(body.slots.map((s,j)=>({...s,slot:s.slot.startsWith('new-')?'obj-'+String(j).padStart(16,'0'):s.slot}))),
          settings_revision:String(++revision),interval_ms:body.interval_ms};
      }
      json(config);return true;
    }
    return false;
  }});
  for(const [i,row] of fixture.state.items.entries()){
    row.name=machineName(i);row.server_local=i===0;row.online=i!==3&&i!==5;row.disabled_at=i===5?now-2*DAY:null;
  }
  const errors=[];
  async function open(theme,width=1470){
    const context=await browser.newContext({viewport:{width,height:1000},locale:'zh-CN',timezoneId:'America/Los_Angeles'});
    await context.route('**/*',route=>new URL(route.request().url()).origin===fixture.base?route.continue():route.abort());
    const page=await context.newPage();
    page.setDefaultTimeout(12000);
    page.on('pageerror',error=>errors.push(error.message));
    await page.goto(fixture.base);
    await page.waitForFunction(()=>typeof dashboardData!=='undefined'&&dashboardData.ready()&&agents.size===8);
    await page.evaluate(theme=>window.ProbeTheme.set(theme),theme);
    return {page,context};
  }
  const fleet=page=>page.locator('.net-fleet tbody tr').first().waitFor();
  const charts=async page=>{
    await page.locator('.net-chart svg').last().waitFor();
    await page.locator('.net-heat-cell').first().waitFor();
  };
  const control=(page,key,value)=>page.locator('[data-net-control="'+key+'"][data-net-value="'+value+'"]');
  async function capture(page,name,selector){
    if(!outDir)return;
    fs.mkdirSync(outDir,{recursive:true});
    await page.evaluate(()=>document.fonts.ready);
    const options={path:path.join(outDir,name+'.png'),animations:'disabled'};
    if(selector)await page.locator(selector).screenshot(options);
    else await page.screenshot({...options,fullPage:true});
  }
  async function brush(page,index){
    const box=await page.locator('.net-chart').nth(index).boundingBox();
    await page.mouse.move(box.x+box.width*.2,box.y+35);
    await page.mouse.down();
    await page.mouse.move(box.x+box.width*.65,box.y+36,{steps:6});
    await page.mouse.up();
    await page.locator('[data-net-reset]').waitFor();
    await charts(page);
  }
  try{
    for(const theme of ['dark','light']){
      const {page,context}=await open(theme);
      try{
        const qualityCard=page.locator('.agent-card[data-agent-id="'+ID(0)+'"] .network-quality');
        await qualityCard.locator('.nq7-b-yellow').first().waitFor();
        for(const state of ['green','yellow','red','gray']){
          assert(await qualityCard.locator('.nq7-b-'+state).count());
        }
        assert.equal(await qualityCard.locator('.nq7-alert-run').count(),5);
        assert.equal(await qualityCard.locator('.nq7-sem-change').count(),1);
        await qualityCard.locator('.nq7-b-red').first().hover();
        assert.match(await page.locator('.nq7-tip:visible').textContent(),/延迟中位数 1024 ms · 丢包 33\.3% · 告警 3 分钟/);
        if(outDir){
          fs.mkdirSync(outDir,{recursive:true});
          const card=await qualityCard.boundingBox(),tip=await page.locator('.nq7-tip:visible').boundingBox();
          const x=Math.floor(Math.min(card.x,tip.x))-4,y=Math.floor(Math.min(card.y,tip.y))-4;
          const width=Math.ceil(Math.max(card.x+card.width,tip.x+tip.width)-x)+4;
          const height=Math.ceil(Math.max(card.y+card.height,tip.y+tip.height)-y)+4;
          await page.screenshot({path:path.join(outDir,theme+'-card-tooltip.png'),clip:{x,y,width,height}});
        }
        await page.locator('#dashboard-network').click();await fleet(page);
        assert.equal(await page.locator('#dashboard-network-machines button').count(),8);
        assert.equal(await page.locator('.net-fleet thead th').nth(2).textContent(),'位置 2');
        const scope=() => page.getByText(/告警只按 TCP 判定/);
        assert.match(await scope().textContent(),/旧口径仍显示延迟/);
        const fleetObjects=page.locator('.net-fleet tbody tr').nth(1).locator('.net-cell');
        assert.match(await fleetObjects.nth(0).textContent(),/未启用/);
        assert.equal(await fleetObjects.nth(0).locator('.net-spark').count(),1);
        assert.match(await fleetObjects.nth(2).textContent(),/没数据/);
        await capture(page,theme+'-fleet');
        await page.locator('.net-fleet tbody tr').nth(1).click();await charts(page);
        assert.match(await scope().textContent(),/旧口径仍显示延迟/);
        const objectStatuses=page.locator('.net-table tbody .net-pill');
        assert.equal(await objectStatuses.nth(0).textContent(),'未启用');
        assert.equal(await objectStatuses.nth(2).textContent(),'没数据');
        await control(page,'family','ipv6').click();await charts(page);
        assert.equal(await objectStatuses.nth(0).textContent(),'正常');
        await control(page,'family','ipv4').click();await charts(page);
        await control(page,'protocol','icmp').click();await charts(page);
        assert.equal(await objectStatuses.nth(1).textContent(),'未启用');
        await control(page,'protocol','tcp').click();await charts(page);
        await page.locator('#dashboard-network').click();await fleet(page);
        await page.locator('.net-fleet tbody tr').first().click();await charts(page);
        assert.equal(await page.locator('.net-charts .net-chip').count(),5);
        assert.match(await page.locator('.net-strip').textContent(),/Agent 时钟偏差超过 1 分钟/);
        await capture(page,theme+'-machine-24h');
        await control(page,'window','7d').click();await charts(page);
        assert.equal(await page.locator('.net-chart .net-change').count(),2);
        await capture(page,theme+'-machine-7d');
        await page.locator('.net-chart').first().hover({position:{x:125,y:50}});
        assert.equal(await page.locator('.net-cross[visibility=visible]').count(),2);
        assert.match(await page.locator('.net-tooltip:visible').textContent(),/p95/);
        await brush(page,1);
        await capture(page,theme+'-machine-zoom');
        await page.locator('[data-net-reset]').click();await charts(page);
        await control(page,'window','custom').click();
        await page.locator('.net-custom [name=start]').fill('2026-11-20T20:00');
        await page.locator('.net-custom [name=end]').fill('2026-11-22T10:00');
        await page.locator('.net-custom button').click();await charts(page);
        await capture(page,theme+'-machine-custom');
        await page.locator('[data-net-legend]').nth(1).click();
        assert.equal(await page.locator('.net-chart').first().locator('.net-series').count(),4);
        await capture(page,theme+'-machine-hidden');
        await control(page,'protocol','icmp').click();await charts(page);
        assert.equal(await page.locator('.net-charts h3').textContent(),'丢包率');
        await capture(page,theme+'-machine-icmp');
        await control(page,'family','ipv6').click();await charts(page);
        assert.match(await page.locator('.net-strip').textContent(),/IPv6 · ICMP/);
        await capture(page,theme+'-machine-ipv6');
        await control(page,'window','30d').click();await charts(page);
        assert.equal(await page.locator('.net-heat-cell').count(),720);
        await page.locator('[data-net-heat]').nth(2).click();await page.locator('.net-heat-cell').first().waitFor();
        await control(page,'heatMetric','packet_loss_percent').click();
        await capture(page,theme+'-heatmap','#network-placeholder section[aria-label="晚高峰热力图"]');
        await page.locator('#dashboard-network-machines button').nth(2).click();await charts(page);
        assert.equal(await page.locator('.net-alerts tbody tr').count(),4);
        await capture(page,theme+'-alerts','.net-alerts');
        await control(page,'family','ipv4').click();await charts(page);
        await control(page,'protocol','tcp').click();await charts(page);
        await control(page,'window','24h').click();await charts(page);
        assert.equal(await page.locator('.net-chart .net-semantics').count(),2);
        assert.equal(await page.locator('.net-chart .net-chart-alert').count(),2);
        assert.match(await page.locator('.net-alerts').textContent(),/丢包/);
        await capture(page,theme+'-machine-loss');
      }finally{await context.close();}
    }
    for(const theme of ['dark','light']){
    const {page,context}=await open(theme,390);
    try{
      await page.locator('#panel-mobile-open').click();await page.locator('#dashboard-network').click();await fleet(page);
      await page.keyboard.press('Escape');
      await capture(page,theme+'-mobile-390-fleet');
      await page.locator('#panel-mobile-open').click();
      await page.locator('#dashboard-network-machines button').first().click();await charts(page);
      assert(await page.evaluate(()=>document.documentElement.scrollWidth===innerWidth),'page stays within 390 px');
      assert(await page.locator('.net-chart').first().evaluate(n=>n.clientWidth>320),'chart fills mobile width');
      await capture(page,theme+'-mobile-390-machine');
    }finally{await context.close();}
    }
    const settings=await open('light');
    try{
      const {page}=settings;
      const card=page.locator('.agent-card[data-agent-id="'+ID(0)+'"]');
      await card.locator('.nq-link').first().click();
      const dialog=page.locator('#network-quality-settings');
      await dialog.locator('[data-nq-field="save-bottom"]:enabled').waitFor();
      for(let i=0;i<5;i++){
        await dialog.locator('[data-nq-field=add]').click();
        const row=dialog.locator('.nq-slot:visible').last();
        await row.getByRole('textbox',{name:'地区',exact:true}).fill(regions[i+3]);
        await row.locator('input[aria-label="运营商"]').fill(carriers[i+3]);
        await row.getByRole('textbox',{name:'目标地址',exact:true}).fill('synthetic-'+i+'.example.net');
        assert(await row.getByRole('checkbox',{name:'在卡片显示'}).isDisabled());
      }
      assert.equal(await dialog.locator('.nq-slot:visible').count(),8);
      assert.equal(await dialog.locator('[data-nq-field=add]').isVisible(),false);
      await dialog.getByRole('checkbox',{name:'在卡片显示'}).nth(2).uncheck();
      const replacement=dialog.getByRole('checkbox',{name:'在卡片显示'}).nth(3);
      await replacement.check();
      await dialog.getByRole('button',{name:'上移监测对象'}).nth(3).click();
      await dialog.locator('[data-nq-field=save-bottom]').click();
      await page.waitForFunction(()=>document.querySelector('#network-quality-settings [role=status]:not([hidden])')&&
        document.querySelector('#network-quality-settings p.nq-status[role="status"]')?.textContent.includes('已保存'));
      assert.equal(lastSave.slots.length,8);
      assert.equal(lastSave.slots[2].host,'synthetic-0.example.net');
      assert.equal(lastSave.slots.filter(slot=>slot.show_on_card).length,3);
      assert.equal(await card.locator('[data-lane]').count(),3);
      await dialog.getByRole('button',{name:'删除监测对象'}).last().click();
      const saved=page.waitForResponse(r=>r.url().endsWith('/network-quality/v4/config')&&r.request().method()==='PUT');
      await dialog.locator('[data-nq-field=save-bottom]').click();
      await saved;
      await page.waitForFunction(()=>document.querySelectorAll('#network-quality-settings .nq-slot:not([hidden])').length===7);
      assert.equal(lastSave.slots.length,7);
    }finally{await settings.context.close();}
    fleetSize=200;detailCount=8;
    const performanceView=await open('light');
    try{
      const {page}=performanceView;
      const timings={};
      async function time(label,step,ready){
        const start=performance.now();
        await step();
        await ready();
        (timings[label]??=[]).push(performance.now()-start);
      }
      const fleetReady=()=>page.waitForFunction(()=>document.querySelectorAll('.net-fleet tbody tr').length===200);
      await time('24h fleet',()=>page.locator('#dashboard-network').click(),fleetReady);
      for(let i=0;i<3;i++){
        await time('30d fleet',()=>control(page,'window','30d').click(),fleetReady);
        await time('30d eight-object page',()=>page.locator('.net-fleet tbody tr').first().click(),()=>charts(page));
        assert.equal(await page.locator('.net-charts .net-chip').count(),8);
        await page.locator('#dashboard-network').click();await fleetReady();
        await time('24h fleet',()=>control(page,'window','24h').click(),fleetReady);
      }
      for(const [label,values] of Object.entries(timings)){
        const max=Math.max(...values);
        t.diagnostic(label+' synthetic browser max '+Math.round(max)+' ms');
        assert(max<(label==='24h fleet'?1000:2000),label+' browser timing exceeds its budget');
      }
    }finally{await performanceView.context.close();}
    assert.deepEqual(errors,[]);
  }finally{
    fixture.close();
    await browser.close();
  }
});
