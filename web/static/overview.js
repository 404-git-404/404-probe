(function(root,factory){const api=factory();if(typeof module==='object'&&module.exports)module.exports=api;else root.Overview=api;})(typeof globalThis==='object'?globalThis:this,function(){
  'use strict';
  const KEY='404-probe.overview.v1';
  const valid=n=>typeof n==='number'&&Number.isFinite(n)&&n>=0;
  const total=()=>({value:0,count:0,overflow:false});
  function add(sum,n){sum.value+=n;sum.count++;sum.overflow=!Number.isFinite(sum.value);}
  // One pass over the accepted DTO snapshot; no I/O, sampling, calendar or billing calculation.
  function aggregate(agents,{now=Date.now(),deadline}={}){
    const result={registered:0,online:0,paused:0,rx:total(),tx:total(),usage:total(),usageExcluded:0,partial:0,calibrated:0,soon:0,due:0,unset:0};
    for(const agent of agents.values()){
      if(agent.revoked)continue;
      result.registered++;
      if(agent.disabled_at)result.paused++;
      const online=Boolean(agent.online&&!agent.disabled_at);
      if(online)result.online++;
      const state=agent.state;
      if(online&&state&&!state.stale&&valid(state.collected_at)&&state.collected_at<=now+5000&&now-state.collected_at<=30000){
        if(valid(state.rx_rate))add(result.rx,state.rx_rate);
        if(valid(state.tx_rate))add(result.tx,state.tx_rate);
      }
      const plan=agent.plan;
      if(plan&&['rx','tx','sum'].includes(plan.traffic_mode)&&valid(plan.cycle_start)&&valid(plan.cycle_end)&&plan.cycle_start<=now&&now<plan.cycle_end&&valid(plan.usage_bytes)){
        add(result.usage,plan.usage_bytes);
        if(plan.usage_status==='partial')result.partial++;
        if(plan.usage_status==='calibrated')result.calibrated++;
      }else result.usageExcluded++;
      const date=plan?.renewal_date||plan?.expiry_date;
      // The existing card helper owns timezone/date semantics; reject malformed DTO shapes first.
      if(typeof date!=='string'||!/^\d{4}-\d{2}-\d{2}$/.test(date)){result.unset++;continue;}
      const d=deadline(plan);
      if(!d||/NaN|未设/.test(d.label||'')){result.unset++;continue;}
      if(d.tone==='danger')result.due++;
      else if(d.tone==='warn')result.soon++;
    }
    return result;
  }
  function create({doc=document,size,deadline,storage=()=>localStorage}={}){
    const shell=doc.querySelector('#overview'),action=doc.querySelector('#overview-action');
    const node=id=>doc.querySelector('#overview-'+id);
    const nodes=Object.fromEntries(['online','online-note','rates','rates-note','usage','usage-note','dates','dates-note'].map(id=>[id,node(id)]));
    try{shell.open=storage().getItem(KEY)!=='closed';}catch(_){shell.open=true;}
    let lastOpen=shell.open;
    const label=()=>{action.textContent=shell.open?'收起 ▴':'展开 ▾';};label();
    shell.addEventListener('toggle',()=>{
      label();if(shell.open===lastOpen)return;lastOpen=shell.open;
      try{storage().setItem(KEY,shell.open?'open':'closed');}catch(_){/* Optional preference only. */}
    });
    const value=(sum,rate=false)=>sum.overflow?'无法合计':sum.count?size(sum.value,rate):'—';
    function update(agents,ready,now=Date.now()){
      if(!ready){for(const id of ['online','rates','usage','dates'])nodes[id].textContent='等待读取';return null;}
      const s=aggregate(agents,{now,deadline});
      nodes.online.textContent=`${s.online} / ${s.registered}`;
      nodes['online-note'].textContent=`在线 / 已登记设备 · 暂停 ${s.paused}（在线不等同有效样本）`;
      nodes.rates.textContent=`↓ ${value(s.rx,true)} · ↑ ${value(s.tx,true)}`;
      nodes['rates-note'].textContent=`十进制 B/s · 下载贡献 ${s.rx.count}/在线 ${s.online} · 上传贡献 ${s.tx.count}/在线 ${s.online}；随页面刷新，连接状态见顶部`;
      nodes.usage.textContent=value(s.usage);
      nodes['usage-note'].textContent=`计入 ${s.usage.count} · 未计入 ${s.usageExcluded} · 可观测 ${s.partial} / 校准 ${s.calibrated}。各自周期与计费方向；含可观测/校准值，非统一月度总量，不代表完整无缺测。`;
      nodes.dates.textContent=`7日内 ${s.soon} · 已到期/续费日已过 ${s.due}`;
      nodes['dates-note'].textContent=`未设置 ${s.unset} · 每设备仅主卡优先日期；提醒汇总，不执行付款`;
      return s;
    }
    return {update};
  }
  return {aggregate,create,KEY};
});
