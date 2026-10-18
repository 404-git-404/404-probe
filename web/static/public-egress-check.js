/* Public IP detection: one model (view), one IP masking rule (maskIP) and one panel (createPanel), shared
   by the device-details dialog and the history page. The panel never reveals an address unless the
   eye button was pressed in this page load; the choice lives in memory only. */
(function(root){
  'use strict';
  const FAMILIES=[['ipv4','IPv4'],['ipv6','IPv6']];

  // IPv4 keeps two octets (203.0.*.*), IPv6 keeps two groups (2001:db8:…). Anything that is not a
  // valid address is never echoed back.
  function parseV6(text){
    if(typeof text!=='string'||text.length>45||!/^[0-9a-fA-F:.]+$/.test(text))return null;
    let t=text;
    if(t.includes('.')){
      const at=t.lastIndexOf(':'),m=/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(t.slice(at+1));
      if(at<0||!m)return null;
      const n=m.slice(1).map(Number);if(n.some((x,i)=>x>255||(m[i+1].length>1&&m[i+1][0]==='0')))return null;
      t=t.slice(0,at+1)+((n[0]<<8)|n[1]).toString(16)+':'+((n[2]<<8)|n[3]).toString(16);
    }
    const halves=t.split('::');if(halves.length>2)return null;
    const part=s=>s===''?[]:s.split(':'),head=part(halves[0]),rest=halves.length===2?part(halves[1]):[];
    if(head.concat(rest).some(g=>!/^[0-9a-fA-F]{1,4}$/.test(g)))return null;
    const total=head.length+rest.length;
    if(halves.length===1?total!==8:total>7)return null;
    return [...head,...Array(8-total).fill('0'),...rest].map(g=>parseInt(g,16));
  }
  function maskIP(text){
    if(typeof text!=='string')return '—';
    const v4=/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(text);
    if(v4){
      const n=v4.slice(1).map(Number);
      if(n.some((x,i)=>x>255||(v4[i+1].length>1&&v4[i+1][0]==='0')))return '—';
      return `${n[0]}.${n[1]}.*.*`;
    }
    const groups=parseV6(text);
    return groups?`${groups[0].toString(16)}:${groups[1].toString(16)}:…`:'—';
  }
  const validIP=text=>maskIP(text)!=='—';

  const sameDay=(a,b)=>new Date(a).toDateString()===new Date(b).toDateString();
  // HH:MM in the browser's local time, with the date when it is not today.
  function clock(ms,now){
    const time=new Date(ms).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',hour12:false});
    return sameDay(ms,now)?time:new Date(ms).toLocaleDateString([],{month:'2-digit',day:'2-digit'})+' '+time;
  }

  // One address row: text for display and, only when there is an address, the full value for the copy button.
  function row(name,o,now,reveal){
    const show=ip=>reveal?ip:maskIP(ip);
    if(!o||!o.checked_at)return {text:`${name}：尚未检测`,ip:''};
    const hasIP=o.status==='available'&&validIP(o.ip);
    const fresh=hasIP&&!o.stale&&(!o.valid_until||o.valid_until>now);
    if(fresh)return {text:`${name}：${show(o.ip)}`,ip:o.ip};
    if(hasIP)return {text:`${name}：旧数据 ${show(o.ip)}`,ip:o.ip};
    return {text:`${name}：${o.status==='unavailable'?'不可用':'未知'}`,ip:''};
  }
  function lastSuccess(name,o,now,reveal){
    const last=o?.last_success;
    if(!last||!validIP(last.ip)||(o.status==='available'&&o.ip===last.ip&&!o.stale))return null;
    return {text:`最后成功 ${name}：${reveal?last.ip:maskIP(last.ip)}（${new Date(last.checked_at).toLocaleString()}）`,ip:last.ip};
  }

  function view(agent,now=Date.now(),{reveal=false}={}){
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
    const rows={ipv4:null,ipv6:null,last_ipv4:null,last_ipv6:null};
    const lines=[];
    if(!enabled){lines.push('公网 IP 检测：未启用');}
    else{
      lines.push('公网 IP 检测：已启用');
      for(const [key,name] of FAMILIES){
        rows[key]=row(name,egress?.[key],now,reveal);lines.push(rows[key].text);
        const last=lastSuccess(name,egress?.[key],now,reveal);if(last){rows['last_'+key]=last;lines.push(last.text);}
      }
      const checked=Math.max(egress?.ipv4?.checked_at||0,egress?.ipv6?.checked_at||0);
      lines.push(checked?`上次检测：${new Date(checked).toLocaleString()}`:'上次检测：尚无记录');
      lines.push(c.next_check_at>0?`下次检测：${c.next_check_at<=now?'即将开始':new Date(c.next_check_at).toLocaleString()}`:'下次检测：等待调度');
    }
    const hasAddress=Object.values(rows).some(r=>r&&r.ip);
    return {state,enabled,switchDisabled:revoked,buttonDisabled:state!=='ready',buttonLabel:label,note,lines,rows,hasAddress,
      cooldownUntil,nextAllowed:cooldownUntil||0,
      times:{last:Math.max(egress?.ipv4?.checked_at||0,egress?.ipv6?.checked_at||0),next:enabled&&c.next_check_at>0?c.next_check_at:0}};
  }

  // The panel keeps one persistent set of nodes: updates change text and flags in place, so focus never
  // moves and a scrolling dialog is never rebuilt.
  function createPanel({document:doc=root.document,agentId,csrf=()=>'',fetcher=(...a)=>root.fetch(...a),begin=()=>({}),finish=()=>true,
      unauthorized=()=>root.location.assign('/login'),clipboard=()=>root.navigator?.clipboard,now=()=>Date.now(),
      setTimer=(f,ms)=>root.setTimeout(f,ms),clearTimer=t=>root.clearTimeout(t),idPrefix='egress'}={}){
    const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
    const node=el('section','public-egress-panel');
    const head=el('div','pe-head');head.append(el('strong','','公网 IP 检测'));
    const eye=el('button','pe-eye','显示完整 IP');eye.type='button';eye.dataset.focusKey=`${idPrefix}-eye`;eye.setAttribute('aria-pressed','false');
    const check=el('button','pe-check','立即检测');check.type='button';check.dataset.focusKey=`${idPrefix}-check`;
    head.append(eye,check);
    const switchLabel=el('label','pe-switch');const toggle=el('input');toggle.type='checkbox';toggle.dataset.focusKey=`${idPrefix}-switch`;
    switchLabel.append(toggle,el('span','',' 启用公网 IP 检测（默认关闭）'));
    const note=el('p','pe-note','');note.setAttribute('aria-live','polite');
    const lines=el('div','pe-lines'),status=el('div','pe-status');
    const rowNodes={};
    for(const key of ['ipv4','ipv6','last_ipv4','last_ipv6']){
      const r=el('div','pe-row');r.dataset.row=key;const text=el('span','pe-text');
      const copy=el('button','pe-copy','复制');copy.type='button';copy.dataset.focusKey=`${idPrefix}-copy-${key}`;copy.setAttribute('aria-label','复制完整 IP');
      r.append(text,copy);r.hidden=true;lines.append(r);rowNodes[key]={r,text,copy,ip:''};
    }
    const times=el('div','pe-times');
    const feedback=el('p','pe-feedback','');feedback.setAttribute('aria-live','polite');
    node.append(head,switchLabel,note,lines,status,times,feedback);

    let agent=null,revealed=false,busy=false,copyTimer=null,currentId=agentId||null;
    const say=text=>{if(feedback.textContent!==text)feedback.textContent=text;};
    function render(){
      const model=view(agent,now(),{reveal:revealed});
      if(toggle.checked!==model.enabled)toggle.checked=model.enabled;
      toggle.disabled=!csrf()||!agent||model.switchDisabled||busy;
      check.disabled=!csrf()||!agent||model.buttonDisabled||busy;
      check.textContent=model.buttonLabel;
      if(note.textContent!==model.note)note.textContent=model.note;
      for(const key of Object.keys(rowNodes)){
        const item=model.rows[key],n=rowNodes[key];n.r.hidden=!item;n.ip='';
        if(item){n.ip=item.ip;if(n.text.textContent!==item.text)n.text.textContent=item.text;}
        n.copy.hidden=!item||!item.ip;
      }
      eye.hidden=!model.hasAddress;
      eye.setAttribute('aria-pressed',String(revealed));
      const eyeText=revealed?'隐藏 IP':'显示完整 IP';if(eye.textContent!==eyeText)eye.textContent=eyeText;
      const timeText=model.enabled?[model.times.last?`上次检测：${new Date(model.times.last).toLocaleString()}`:'上次检测：尚无记录',
        model.times.next?`下次检测：${model.times.next<=now()?'即将开始':new Date(model.times.next).toLocaleString()}`:'下次检测：等待调度'].join(' · '):'';
      if(times.textContent!==timeText)times.textContent=timeText;
      times.hidden=!timeText;
    }
    // Revealed addresses live in memory only and go back to the half-hidden form on any change of context.
    function conceal(){revealed=false;if(copyTimer!==null){clearTimer(copyTimer);copyTimer=null;}say('');render();}
    function update(next){
      if(next?.agent_id&&currentId&&next.agent_id!==currentId){revealed=false;say('');}
      if(next?.agent_id)currentId=next.agent_id;
      agent=next;render();
    }
    eye.addEventListener('click',()=>{revealed=!revealed;render();});
    for(const key of Object.keys(rowNodes)){
      rowNodes[key].copy.addEventListener('click',async()=>{
        const ip=rowNodes[key].ip;if(!ip)return;
        try{await clipboard().writeText(ip);say('已复制');}catch(error){say('复制失败，请手动选择');}
        if(copyTimer!==null)clearTimer(copyTimer);copyTimer=setTimer(()=>{copyTimer=null;say('');},1500);
      });
    }
    async function post(path,body){
      const response=await fetcher(`/api/v1/web/agents/${encodeURIComponent(currentId)}${path}`,{method:'POST',cache:'no-store',
        headers:{'Content-Type':'application/json','X-CSRF-Token':csrf()},body});
      if(response.status===401){unauthorized();return null;}
      let payload=null;try{payload=await response.json();}catch(error){/* keep null */}
      if(!response.ok){const error=new Error(payload?.error?.message||'请求失败');error.code=payload?.error?.code;error.nextAllowedAt=payload?.next_allowed_at;throw error;}
      return payload;
    }
    toggle.addEventListener('change',async()=>{
      if(!agent||!csrf()||busy)return;
      const wanted=toggle.checked,mutation=begin();
      if(!mutation){toggle.checked=!wanted;return;}
      busy=true;say('');render();
      try{
        const result=await post('/public-egress',JSON.stringify({enabled:wanted}));
        if(result){const {created,...patch}=result;finish(mutation,{public_egress_check:patch});
          if(wanted&&!created)say(patch.cooldown_until?'冷却期内重新打开不会立即检测，将按计划检测。':'');}
      }catch(error){say(error.message||'无法修改公网 IP 检测开关');}
      finally{busy=false;finish(mutation);render();}
    });
    check.addEventListener('click',async()=>{
      if(!agent||!csrf()||busy||check.disabled)return;
      const mutation=begin();if(!mutation)return;
      busy=true;say('');render();
      try{
        if(await post('/public-egress/check','{}'))finish(mutation,{public_egress_check:{...agent.public_egress_check,pending:true}});
      }catch(error){
        if(error.code==='cooldown'&&error.nextAllowedAt)finish(mutation,{public_egress_check:{...agent.public_egress_check,cooldown_until:error.nextAllowedAt}});
        else say(error.message||'检测请求失败');
      }finally{busy=false;finish(mutation);render();}
    });
    render();
    return {node,update,conceal,elements:{toggle,check,eye,note,feedback,rows:rowNodes,times,status},get revealed(){return revealed;}};
  }

  const api={view,maskIP,clock,createPanel};
  if(typeof module!=='undefined'&&module.exports)module.exports=api;else root.PublicEgressCheck=api;
})(globalThis);
