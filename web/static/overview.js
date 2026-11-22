(function(root){
  'use strict';
  const M=root.DashboardModel||(typeof require==='function'?require('./dashboard-model.js'):null);
  function create({doc=root.document,preferences,onfilter=()=>{},onexpense=()=>{}}={}){
    const get=id=>doc.getElementById('overview-'+id),p=preferences?.value||M.defaults();
    const filters=['upgrade','health','renewal'];
    for(const id of filters) get(id).addEventListener('click',()=>onfilter(id));
    get('expense').addEventListener('click',event=>onexpense(event.currentTarget));
    const periods=[...get('period').querySelectorAll('button')];
    let expenses=null, painted='';
    function money(){
      get('money').textContent=expenses&&expenses.included_count!==0
        ?'$'+(p.expensePeriod==='yearly'?expenses.yearly_usd+' / 年':expenses.monthly_usd+' / 月'):'—';
      for(const button of periods) button.setAttribute('aria-pressed',String(button.dataset.period===p.expensePeriod));
    }
    for(const button of periods) button.addEventListener('click',()=>{
      p.expensePeriod=button.dataset.period;
      preferences?.save();
      money();
    });
    money();
    function update(agents,ready,{summary=null,catalog={},server={}}={}){
      if(!ready) return;
      const s=summary||M.aggregate(agents,server);
      const signature=JSON.stringify([s.registered,s.online,s.offline,s.paused,s.upgrade,s.health,s.insufficient,s.renewal,p.quick,catalog.expenses]);
      if(signature===painted) return;
      painted=signature;
      get('scope').textContent='全部 '+s.registered+' 台';
      get('online').textContent=s.online+' / '+s.registered;
      get('offline').textContent='离线 '+s.offline+' · 暂停 '+s.paused;
      get('upgrade').textContent='待升级 '+s.upgrade+' 台';
      get('health-stat').textContent=s.health?'⚠ '+s.health+' 台需要关注':s.insufficient?'暂无已知异常':s.registered?'✓ 全部正常':'尚无设备';
      get('renewal-stat').querySelector('strong').textContent=s.renewal;
      for(const id of ['health','renewal']) get(id+'-stat').classList.toggle('attention',s[id]>0);
      for(const id of filters){
        get(id).disabled=!s[id];
        get(id).setAttribute('aria-pressed',String(p.quick.includes(id)));
      }
      expenses=catalog.expenses||null;
      money();
    }
    return {update};
  }
  const api={create,aggregate:M.aggregate,KEY:M.KEY};
  if(typeof module==='object'&&module.exports) module.exports=api;
  else root.Overview=api;
})(globalThis);
