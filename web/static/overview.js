(function(root){
 'use strict';
 const M=root.DashboardModel||(typeof require==='function'?require('./dashboard-model.js'):null);
 function create({doc=root.document,preferences,onfilter=()=>{},onexpense=()=>{}}={}){
  const get=id=>doc.getElementById('overview-'+id),shell=doc.getElementById('overview'),p=preferences?.value||M.defaults();
  const save=()=>preferences?.save();shell.open=!p.overviewClosed;
  shell.addEventListener('toggle',()=>{p.overviewClosed=!shell.open;get('action').textContent=shell.open?'收起 ▴':'展开 ▾';save();});
  for(const [id,key]of [['upgrade','upgrade'],['health','health'],['renewal','renewal']])get(id).addEventListener('click',()=>onfilter(key));
  get('expense').addEventListener('click',e=>onexpense(e.currentTarget));
  get('period').value=p.expensePeriod;let last=null;
  get('period').addEventListener('change',()=>{p.expensePeriod=get('period').value;save();money();});
  function money(){get('money').textContent=last?'$'+(p.expensePeriod==='yearly'?last.yearly_usd:last.monthly_usd)+' / '+(p.expensePeriod==='yearly'?'年':'月'):'—';}
  function update(agents,ready,{summary=null,catalog={},server={},simple=false}={}){
   shell.classList.toggle('overview-simple',simple);get('scope').textContent=ready?'全部 '+summary.registered+' 台':'正在读取';
   doc.getElementById('dashboard-expand').hidden=!simple;
   if(!ready){for(const id of ['online','health','renewal'])get(id).textContent='等待读取';get('health-note').textContent='资料读取完成后显示健康汇总';return;}
   const s=summary||M.aggregate(agents,server);shell.classList.toggle('has-attention',!!(s.health||s.insufficient));get('online').textContent=`${s.online} / ${s.registered}`;
   get('offline').textContent=`离线 ${s.offline} · 暂停 ${s.paused}`;get('upgrade').textContent=`待升级 ${s.upgrade} 台`;get('upgrade').disabled=!s.upgrade;get('upgrade').hidden=simple&&!s.upgrade;
   get('versions').textContent=[s.versionUnknown?`版本无法识别 ${s.versionUnknown} 台`:'',s.unknown?`状态未知 ${s.unknown} 台`:''].filter(Boolean).join(' · ');
   get('health').textContent=s.health?`${s.health} 台需要关注`:s.insufficient?'暂无已知异常':s.registered?'✓ 全部正常':'尚无设备';
   get('health').classList.toggle('attention',s.health>0);get('health').disabled=!s.health;
   get('health-note').textContent=`网络异常 ${s.quick.network} · 资源吃紧 ${s.quick.resources} · 流量将满 ${s.quick.traffic}`;
   get('unknown').textContent=s.insufficient?`资料不足 ${s.insufficient} 台` : '';
   get('renewal').textContent=`${s.renewal} 台 7 天内续费`;get('renewal').disabled=!s.renewal;get('overdue').textContent=`已到期 ${s.overdue}`;
   get('nearest').textContent=s.nearest?`最近：${s.nearest.name} · ${s.nearest.renewal.date}（${s.nearest.renewal.days===0?'今天':s.nearest.renewal.days+' 天后'}）`:s.renewalInvalid?'续费日期或时区需要检查':'尚未设置待续费日期';
   last=catalog.expenses||null;money();get('action').textContent=shell.open?'收起 ▴':'展开 ▾';
  }
  return {update};
 }
 const api={create,aggregate:M.aggregate,KEY:M.KEY};if(typeof module==='object'&&module.exports)module.exports=api;else root.Overview=api;
})(globalThis);
