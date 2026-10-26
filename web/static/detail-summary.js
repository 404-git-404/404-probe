/* Persistent overview/management nodes. Server fields retain their original meanings. */
(function(root){
 'use strict';
 function create({document:doc=root.document,actions,egress,country,edit,renew,saveCountry,canWrite}){
  const el=(t,c='',text)=>{const n=doc.createElement(t);n.className=c;if(text!=null)n.textContent=text;return n;};
  const overview=el('div','details-overview'),management=el('div','details-management'),metrics=el('div','details-metrics'),grid=el('div','details-grid'),groups=el('div','details-grid');
  overview.append(metrics,grid);management.append(groups);
  const values={};let current=null;
  const value=(key,parent)=>{const n=el('span');values[key]=n;parent.append(n);return n;};
  const button=(label,action,key)=>{const n=el('button','',label);n.type='button';if(key)n.dataset.focusKey=key;n.addEventListener('click',()=>action(n));return n;};
  function section(title,parent=grid){const n=el('section','details-box'),h=el('div','details-box-head');h.append(el('h2','',title));n.append(h);parent.append(n);return {n,h};}
  function fact(box,label,key){const row=el('div','details-fact');row.append(el('span','details-label',label));value(key,row);box.n.append(row);}
  const tiles=['CPU','内存','磁盘','下行','上行'].map((label,i)=>{const n=el('div','details-box details-metric');n.append(el('span','details-label',label));const main=value('metric'+i,n),sub=value('sub'+i,n);main.className='metric-value';sub.className='details-note';metrics.append(n);return n;});
  const plan=section('套餐与续费'),editButton=button('编辑',n=>edit(current,n),'detail-plan');plan.h.append(editButton);
  fact(plan,'价格','price');fact(plan,'带宽','bandwidth');fact(plan,'下次续费','renewal');const renewal=button('已续费…',n=>renew(current,n),'detail-renewal');plan.n.append(renewal);fact(plan,'到期日','expiry');
  const traffic=section('流量'),quota=button('编辑配额',n=>edit(current,n),'detail-quota');traffic.h.append(quota);value('usage',traffic.n);
  const track=el('div','details-progress'),fill=el('span');track.append(fill);traffic.n.append(track);value('cycle',traffic.n).className='details-note';fact(traffic,'设备累计','total');value('coverage',traffic.n).className='details-note';
  const network=section('网络地址');network.n.append(egress.addresses);fact(network,'出口地区','country');
  const system=section('系统');fact(system,'主机名','hostname');fact(system,'配置','config');fact(system,'Agent','version');fact(system,'最近上报','seen');
  const agentGroup=section('Agent',groups),checks=section('检测与统计',groups),danger=section('危险操作',management);danger.n.classList.add('details-danger');
  function row(box,title,key,control){const n=el('div','details-action-row'),left=el('div');left.append(el('strong','',title));value(key,left).className='details-note';n.append(left);if(control)n.append(control);box.n.append(n);return n;}
  row(agentGroup,'升级','upgrade',actions.upgrade);row(agentGroup,'系统信息','sample');
  const region=el('div','details-region'),select=el('select');select.setAttribute('aria-label','出口地区手动覆盖');select.dataset.focusKey='detail-country';
  const catalog=root.Management.countries();select.append(Object.assign(el('option','','使用自动识别'),{value:''}));for(const c of catalog)select.append(Object.assign(el('option','',c.zh+' '+c.code),{value:c.code}));
  select.addEventListener('change',()=>saveCountry(current,select.value));region.append(select,country);row(agentGroup,'出口地区','countryManage',region);
  row(checks,'公网 IP 检测','egress',egress.controls);row(checks,'设备累计流量','since',actions.reset);checks.n.append(actions.resetFeedback);
  row(checks,'Google 检测','google',actions.google);checks.n.append(actions.googleFeedback);
  row(danger,'暂停监控','pause',actions.pause);row(danger,'撤销 Agent','revoke',actions.remove);
  const more=el('details','details-more'),summary=el('summary','','更多');more.append(summary,actions.history);management.append(more);
  const time=v=>v?new Date(v).toLocaleString():'未知';
  const bytes=(v,binary=false)=>v==null?'—':root.CardMetrics?.size?.(v,binary)||format(v,binary);
  function format(v,binary){let n=Number(v),i=0;const units=binary?['B','KiB','MiB','GiB','TiB']:['B','KB','MB','GB','TB'],base=binary?1024:1000;while(n>=base&&i<4){n/=base;i++;}return n.toFixed(i?2:0)+' '+units[i];}
  const countryName=(code)=>catalog.find(c=>c.code===code)?.zh||code||'未知';
  const set=(key,text)=>{if(values[key].textContent!==text)values[key].textContent=text;};
  function update(a,target=null){current=a;const s=a.state||{},p=a.plan||{},old=!a.online||a.disabled_at||s.stale,valid=v=>!old&&v!=null;
   const percent=v=>v==null?'—':Number(v).toFixed(1)+'%';
   [s.cpu_percent,s.ram_percent,s.disk_percent].forEach((v,i)=>set('metric'+i,valid(v)?percent(v):'—'));
   set('sub0',old?'当前数据不可用':`负载 ${s.load1==null?'—':s.load1.toFixed(2)} · steal ${percent(s.cpu_steal_percent)}`);
   set('sub1',old?'当前数据不可用':`${s.ram_used==null?'—':format(s.ram_used,true)} / ${s.ram_total?format(s.ram_total,true):'—'}`);set('sub2',old?'当前数据不可用':`${s.disk_used==null?'—':format(s.disk_used,true)} / ${s.disk_total?format(s.disk_total,true):'—'}`);
   [s.rx_rate,s.tx_rate].forEach((v,i)=>{set('metric'+(i+3),valid(v)?format(v,false)+'/s':'—');set('sub'+(i+3),old?'当前数据不可用':'实时速率');});
   set('price',p.renewal_price?`${p.currency||''} ${p.renewal_price} / ${{monthly:'月',yearly:'年',once:'一次性'}[p.renewal_price_period]||'未设周期'}`:'未设置');
   set('bandwidth',p.bandwidth_value?`${p.bandwidth_value} ${p.bandwidth_unit}`:'未设置');
   let relative='';if(p.renewal_date){try{const today=new Intl.DateTimeFormat('en-CA',{timeZone:p.timezone||'UTC',year:'numeric',month:'2-digit',day:'2-digit'}).format(new Date());const days=Math.round((Date.parse(p.renewal_date)-Date.parse(today))/86400000);if(Number.isFinite(days))relative=days===0?' · 今天':days>0?` · ${days} 天后`:` · 已过 ${-days} 天`;}catch{}}
   set('renewal',(p.renewal_date||'未设置')+relative);set('expiry',p.expiry_date||'未设置');
   const view=root.Card.trafficView(a,{old:Boolean(old)}),days=root.Card.cycleDays(p,Date.now());
   set('usage',p.traffic_mode?`${format(p.usage_bytes||0,false)} / ${p.quota_bytes==null?'不限流量':format(p.quota_bytes,false)}${p.quota_bytes?` · ${(Number(p.usage_bytes||0)/p.quota_bytes*100).toFixed(1)}%`:''} 本周期`:'未开启套餐流量统计');
   track.hidden=!p.traffic_mode||p.quota_bytes==null;fill.style.width=p.quota_bytes?Math.max(0,Math.min(100,Number(p.usage_bytes||0)/p.quota_bytes*100))+'%':'0%';
   set('cycle',`${root.Card.resetText(p)}${days?` · 第 ${days.day}/${days.total} 天`:''}${p.traffic_mode?' · '+({sum:'计下行 + 上行',rx:'只计下行',tx:'只计上行'}[p.traffic_mode]||''):''}`);
   set('total',a.traffic?`↓ ${format(a.traffic.rx_total,false)}　↑ ${format(a.traffic.tx_total,false)} · ${a.traffic.started_at?'自 '+time(a.traffic.started_at):'自接入起'}`:'—');set('coverage',p.traffic_mode?(p.usage_status==='calibrated'?'本周期用量已人工对账':'仅统计可观测用量，缺测可能使统计不完整'):'');
   const regionText=p.country_code_override?`已手动设置为 ${countryName(p.country_code_override)} ${p.country_code_override}`:`自动识别为 ${a.country_code?countryName(a.country_code)+' '+a.country_code:'未知'}`;
   set('country',regionText);set('countryManage',regionText);if(select!==doc.activeElement)select.value=p.country_code_override||'';select.disabled=!canWrite(a);
   set('hostname',s.hostname||'—');set('config',`${s.cpu_cores?s.cpu_cores+' 核':'核心数未知'} · ${s.ram_total?format(s.ram_total,true):'—'} 内存 · ${s.disk_total?format(s.disk_total,true):'—'} 磁盘 · ${s.swap_total==null?'—':format(s.swap_total,true)} Swap`);
   actions.upgrade.disabled=actions.upgrade.disabled||!target?.supported;
   const upgrade=target?.supported?`可升级到 ${target.version}`:'';set('version',(a.version||'—')+(upgrade?' · '+upgrade:''));set('upgrade',(a.version||'—')+(target?' → '+target.version+(target.supported?'（与 Server 相同）':' · '+(target.reason||'不可升级')):' · '+(a.online?'正在读取目标版本':'离线')));
   set('seen',time(a.last_seen));set('sample','随上报自动更新 · 最近采样 '+time(s.collected_at));set('egress',a.public_egress_check?.next_check_at?'下次 '+time(a.public_egress_check.next_check_at):'按服务端计划检测');
   set('since',a.traffic?.started_at?'从 '+time(a.traffic.started_at)+' 起 · 不影响套餐用量':'重新开始不影响套餐用量');set('google',a.google_status?.checked_at?'上次 '+time(a.google_status.checked_at):'尚未检测');set('pause',a.disabled_at?'已暂停，可以恢复监控':'不再接收数据，可随时恢复');set('revoke','停止接收数据，保留历史记录；Agent 程序不会被卸载');
   [editButton,quota,renewal].forEach(n=>{n.disabled=!canWrite(a);});
  }
  return {overview,management,update};
 }
 const api={create};if(typeof module==='object'&&module.exports)module.exports=api;else root.DetailSummary=api;
})(globalThis);
