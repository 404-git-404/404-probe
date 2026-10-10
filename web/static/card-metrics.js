(function(root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.CardMetrics = api;
})(typeof globalThis === 'object' ? globalThis : this, function() {
  'use strict';
  const finite = value => typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null;
  const percent = value => finite(value) == null ? null : Math.min(100, value);
  const pct = value => percent(value) == null ? '—' : `${percent(value).toFixed(1)}%`;
  function size(value, rate = false, binary = false) {
    if (finite(value) == null) return '—';
    const base = binary ? 1024 : 1000;
    const units = binary ? ['B','KiB','MiB','GiB','TiB','PiB','EiB'] : ['B','KB','MB','GB','TB','PB','EB'];
    let n = value, index = 0;
    while (n >= base && index < units.length - 1) { n /= base; index++; }
    return `${n.toFixed(index === 0 ? 0 : n >= 100 ? 0 : n >= 10 ? 1 : 2)} ${units[index]}${rate ? '/s' : ''}`;
  }
  function os(value) {
    const text = typeof value === 'string' ? value.trim() : '';
    if (!text) return {text:'等待首次上报', icon:'generic'};
    const match = text.match(/^(debian|alpine(?:\s+linux)?)(?=\s|$)/i);
    if (!match) return {text, icon:'generic'};
    return {text:(match[1].toLowerCase() === 'debian' ? 'Debian' : 'Alpine Linux') + text.slice(match[1].length), icon:match[1].toLowerCase() === 'debian' ? 'debian' : 'alpine'};
  }
  function price(plan) {
    const value = plan?.renewal_price || plan?.purchase_price;
    if (!value) return '未设价格';
    const period = plan.renewal_price ? plan.renewal_price_period : plan.purchase_price_period;
    const currency = plan.currency || '';
    let amount = `${currency} ${value}`.trim();
    if (/^[A-Z]{3}$/.test(currency) && Number.isFinite(Number(value))) {
      try { amount = `${currency} ${new Intl.NumberFormat('en-US',{style:'currency',currency,currencyDisplay:'narrowSymbol'}).format(Number(value))}`; } catch (_) { /* preserve actual currency/value */ }
    }
    return `${amount}${({monthly:'/月',yearly:'/年',once:' 一次性'})[period] || ''}`;
  }
  const tone = (value, warning=70, critical=90) => percent(value) == null ? 'unknown' : value >= critical ? 'critical' : value >= warning ? 'warning' : 'healthy';
  // Two bounded observations per live agent, no timer, request, persisted history or boot-identity claim.
  function createObserver() {
    const records = new Map();
    const sync = ids => {const live = new Set(ids);for(const id of records.keys())if(!live.has(id))records.delete(id);};
    function observe(agent, now = Date.now()) {
      const state = agent.state || {}, at = finite(state.collected_at), up = finite(state.uptime);
      const stale = !agent.state || !agent.online || !!agent.disabled_at || !!agent.revoked || !!state.stale || at == null || at > now + 5000 || now - at > 30000;
      if (stale) {records.delete(agent.agent_id);return {cpu:'unknown',io:'unknown',stale:true};}
      let record = records.get(agent.agent_id);
      if (!record) {record={cpu:null,io:null};records.set(agent.agent_id,record);}
      const result = {stale:false};
      for(const [key,value] of [['cpu',state.cpu_percent],['io',state.disk_busy_percent]]) {
        const previous = record[key];
        if (percent(value) == null || value < 85 || up == null) {record[key]=null;result[key]=tone(value,60,85) === 'critical' ? 'warning' : tone(value,60,85);continue;}
        const delta = previous ? at - previous.at : 0;
        const uptimeDelta = previous ? up - previous.up : 0;
        const continuous = previous && delta >= 0 && delta <= 20000 && uptimeDelta >= 0 && Math.abs(uptimeDelta - delta/1000) <= 2;
        const started = continuous ? previous.started : at;
        record[key] = {at,up,started};
        result[key] = at - started >= 30000 ? 'critical' : 'warning';
      }
      return result;
    }
    return {observe,sync,size:()=>records.size};
  }
  function annotation(kind,value,toneValue,stale=false,ioTone='') {
    const reason=percent(value)==null?'未知':stale?'旧数据':toneValue==='critical'?'高占用':toneValue==='warning'?'接近阈值':'';
    const name=({cpu:'CPU',memory:'RAM',disk:'磁盘容量'})[kind]||'资源';
    const capacity=reason?`${name} ${reason}`:'';
    const io=kind==='disk'&&!stale&&['warning','critical'].includes(ioTone)?'磁盘 I/O 高占用':'';
    return {reason,capacity,io};
  }
  function tile(kind,label,value,detail,toneValue,extra='',stale=false,ioTone='') {
    const el = document.createElement('section');
    el.className = `metric-tile metric-${kind} tone-${toneValue}`;
    const n = percent(value), visible = pct(value);
    const ring = document.createElement('div');ring.className='metric-ring';
    // Only finite clamped numeric values and a fixed icon allowlist enter markup.
    const name = ({cpu:'CPU',memory:'RAM',disk:'DISK'})[kind] || '资源';
    ring.innerHTML = `<svg viewBox="0 0 100 100" aria-hidden="true"><circle class="ring-track" cx="50" cy="50" r="41"/><circle class="ring-value" cx="50" cy="50" r="41" pathLength="100" stroke-dasharray="${!stale && n != null ? n : 0} 100"/></svg><span class="metric-name" aria-hidden="true">${name}</span>`;
    const amount=document.createElement('strong');amount.textContent=visible;
    const title=document.createElement('span');title.className='metric-title';title.textContent=label;
    const note=document.createElement('small');note.textContent=detail;note.title=detail;
    const auxiliary=document.createElement('small');auxiliary.textContent=extra;
    const meaning=document.createElement('small');meaning.className='metric-meaning';
    const description=annotation(kind,value,toneValue,stale,ioTone);
    meaning.textContent=!description.reason?'':description.reason==='未知'?'?':description.reason==='旧数据'?'旧':kind==='disk'?'容':'!';
    meaning.hidden=!description.reason;meaning.title=description.capacity;meaning.dataset.reason=description.capacity;
    meaning.dataset.state=description.reason==='未知'||description.reason==='旧数据'?'neutral':'alert';meaning.setAttribute('aria-label',description.capacity);
    el.tabIndex=0;el.setAttribute('role','button');el.dataset.focusKey=`resource-${kind}`;el.setAttribute('aria-haspopup','dialog');
    el.title=`${name} ${visible} · ${detail}${extra?' · '+extra:''}${description.capacity?' · '+description.capacity:''}${description.io?' · '+description.io:''}；点击查看资源详情`;
    const tooltip=document.createElement('span');tooltip.className='resource-tooltip';tooltip.setAttribute('role','tooltip');tooltip.textContent=el.title;
    el.setAttribute('aria-label',`${label} ${visible}，${description.capacity}，${detail}，${extra}${description.io?'，'+description.io:''}`);
    el.append(ring,amount,title,note,auxiliary,meaning,tooltip);
    if(kind==='disk'){const io=document.createElement('small');io.className='metric-io-alert';io.textContent='I/O';io.hidden=!description.io;io.title=description.io;io.dataset.reason=description.io;io.setAttribute('aria-label',description.io);el.append(io);}
    return el;
  }
  function stamp(tier) {
    if (!['SSS','SS','S','A','B','C','D'].includes(tier)) return null;
    const el=document.createElement('span');el.className=`quality-stamp stamp-${tier.toLowerCase()}`;
    el.setAttribute('aria-label',`合成视觉等级 ${tier}，非真实评分`);
    const points=Array.from({length:64},(_,i)=>{const a=i*Math.PI/32,r=i%2?46:49;return `${50+Math.cos(a)*r},${50+Math.sin(a)*r}`;}).join(' ');
    const ornate=['SSS','SS','S'].includes(tier);
    el.innerHTML=`<svg viewBox="0 0 100 100" aria-hidden="true"><polygon class="stamp-edge" points="${points}"/><circle class="stamp-inner" cx="50" cy="50" r="40"/><circle class="stamp-inset" cx="50" cy="50" r="35"/>${ornate ? '<path class="stamp-laurel" d="M30 72 Q12 47 32 26 M70 72 Q88 47 68 26 M25 62l-9-5m7-3l-8-7m9-2l-6-8m11-1l-4-9m50 35l9-5m-7-3l8-7m-9-2l6-8m-11-1l4-9"/>' : ''}</svg><strong>${tier}</strong>`;
    return el;
  }
  return {finite,percent,pct,size,os,price,tone,createObserver,annotation,tile,stamp};
});
