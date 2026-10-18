/* Public IP detection panel model: one place decides the switch, the button and the texts. No requests. */
(function(root){
  'use strict';
  const family=(name,o,now)=>{
    if(!o||!o.checked_at)return `${name}：尚未检测`;
    const fresh=o.status==='available'&&o.ip&&!o.stale&&(!o.valid_until||o.valid_until>now);
    if(fresh)return `${name}：${o.ip}`;
    return `${name}：${o.status==='available'&&o.ip?'旧数据 '+o.ip:o.status==='unavailable'?'不可用':'未知'}`;
  };
  const sameDay=(a,b)=>new Date(a).toDateString()===new Date(b).toDateString();
  // HH:MM in the browser's local time, with the date when it is not today.
  function clock(ms,now){
    const time=new Date(ms).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',hour12:false});
    return sameDay(ms,now)?time:new Date(ms).toLocaleDateString([],{month:'2-digit',day:'2-digit'})+' '+time;
  }
  function view(agent,now=Date.now()){
    const c=agent?.public_egress_check||{};
    const egress=agent?.state?.public_egress;
    const enabled=Boolean(c.enabled);
    const revoked=Boolean(agent?.revoked),paused=Boolean(agent?.disabled_at),offline=agent?.online===false;
    const cooldownUntil=c.cooldown_until>now?c.cooldown_until:0;
    let state='ready',note='',label='立即检测';
    if(!enabled){state='off';note='未启用';}
    else if(revoked){state='revoked';note='Agent 已撤销';}
    else if(paused){state='paused';note='Agent 已暂停';}
    else if(!c.supported){state='unsupported';note='Agent 版本不支持，升级 Agent 后可用';}
    else if(offline){state='offline';note='Agent 离线';}
    else if(c.pending){state='pending';note='检测中';label='检测中…';}
    else if(cooldownUntil){state='cooldown';note=`可在 ${clock(cooldownUntil,now)} 后再次检测`;}
    const lines=[];
    if(!enabled){lines.push('公网 IP 检测：未启用');}
    else{
      lines.push('公网 IP 检测：已启用');
      lines.push(family('IPv4',egress?.ipv4,now),family('IPv6',egress?.ipv6,now));
      const checked=Math.max(egress?.ipv4?.checked_at||0,egress?.ipv6?.checked_at||0);
      lines.push(checked?`上次检测：${new Date(checked).toLocaleString()}`:'上次检测：尚无记录');
      lines.push(c.next_check_at>0?`下次检测：${c.next_check_at<=now?'即将开始':new Date(c.next_check_at).toLocaleString()}`:'下次检测：等待调度');
    }
    return {state,enabled,switchDisabled:revoked,buttonDisabled:state!=='ready',buttonLabel:label,note,lines,
      cooldownUntil,nextAllowed:cooldownUntil||0};
  }
  const api={view,clock};if(typeof module!=='undefined'&&module.exports)module.exports=api;else root.PublicEgressCheck=api;
})(globalThis);
