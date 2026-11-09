/* Dashboard-only row projection. No per-device reads or mutation transport. */
(function(root){
 'use strict';
 const finite=v=>typeof v==='number'&&Number.isFinite(v),M=root.DashboardModel,C=root.Card;
 function price(plan){
  if(!plan)return '未设套餐';
  const renewal=plan.renewal_price!==''&&plan.renewal_price!=null,value=renewal?plan.renewal_price:plan.purchase_price,period=renewal?plan.renewal_price_period:plan.purchase_price_period;
  if(value==null||value==='')return '未设价格';if(Number(value)===0)return '免费';
  const symbol={CNY:'¥',USD:'$',EUR:'€',GBP:'£',JPY:'¥',HKD:'HK$'}[plan.currency]||((plan.currency||'')+' ');
  return symbol+value+({monthly:' / 月',quarterly:' / 季',semiannual:' / 半年',yearly:' / 年',once:' / 一次'}[period]||'');
 }
 function renewal(a){const r=a.renewal;if(!a.plan)return '—';if(r?.type==='overdue')return `逾期 ${Math.abs(r.days||0)} 天`;if(finite(r?.days))return `余 ${r.days} 天`;if(r?.type==='invalid')return '日期无效';return !a.plan.renewal_date&&!a.plan.expiry_date?'长期':'未设日期';}
 function latency(a,slot){
  const s=a.network_slots?.find(s=>s.slot===slot),state=s?.state||'no_data';
  return {state,title:s?.target_name||'',text:state==='ok'||state==='stale'?`${finite(s.latest_ms)?Number(s.latest_ms.toFixed(1))+' ms':'—'}${state==='stale'?' · 过期':''}`:({no_target:'未设目标',unsupported:'不支持',no_data:'暂无数据'}[state]||'暂无数据')};
 }
 function projection(a,{slot=1,server,now=Date.now(),csrf='',blocked=false}={}){
  const lifecycle=M.lifecycle(a),r=a.resources||{},old=M.resourcesOld(a,now);
  const name=a.name||a.state?.hostname||'未命名 VPS',uptime=lifecycle==='paused'?'暂停':lifecycle==='offline'?`离线 ${Math.floor(Math.max(0,now-(a.last_seen||now))/60000)} 分钟`:finite(a.state?.uptime)?`${Math.floor(a.state.uptime/86400)} 天 ${Math.floor(a.state.uptime%86400/3600)} 时`:'等待上报';
  const os=a.state?.os||'',system=/debian/i.test(os)?'Deb':/alpine/i.test(os)?'Alp':/ubuntu/i.test(os)?'Ubu':os.split(' ')[0]||'—';
  const metrics=['cpu_percent','ram_percent','disk_percent'].map(k=>{const v=a.state?.collected_at>=r.collected_at&&finite(a.state[k])?a.state[k]:r[k],tone=C.ringTone(v,old);return {tone,fill:tone==='unknown'?0:Math.max(0,Math.min(100,v)),text:tone==='unknown'?'—':Number(v.toFixed(1))+'%'};});
  const p=a.plan,t=a.dashboard_traffic||{},trafficOld=t.stale||t.quota_state==='expired'||(p?.traffic_mode&&!t.known),used=p?.usage_bytes,quota=p?.quota_bytes;
  let traffic={text:'无流量套餐',fill:0,tone:'unknown'};
  if(p?.traffic_mode){const percent=finite(used)&&finite(quota)&&quota>0?used/quota*100:null;
   traffic={text:trafficOld?'数据陈旧':t.quota_state==='pending'?'周期尚未开始':quota==null?`${C.sig3(used)} / 不限流量`:!finite(used)?'暂无用量':`${C.sig3(used)} / ${C.sig3(quota)} · ${percent==null?'—':Number(percent.toFixed(1))+'%'}`,fill:trafficOld?0:Math.max(0,Math.min(100,percent||0)),tone:trafficOld?'unknown':C.ringTone(percent)};
  }
  const youtube=root.YouTubeStatus.display(a,now);
  return {name,uptime,lifecycle,country:a.country_code||'',system,os,metrics,tx:'↑ '+(old?'—':C.sig3(a.state?.tx_rate,true)),rx:'↓ '+(old?'—':C.sig3(a.state?.rx_rate,true)),traffic,latency:latency(a,slot),youtube:{text:youtube.code||'—',tone:youtube.manual?'crit':youtube.premium==='unavailable'?'crit':youtube.tone==='observed'?'ok':'unknown',title:youtube.description},price:price(p),renewal:renewal(a),soon:a.renewal?.type==='due_soon'||a.renewal?.type==='overdue',upgrade:M.flags(a,server,now).upgrade,outbounds:(a.outbounds?.selectors||[]).map(s=>[s.name,s.current]),disabled:root.Selector.reason(a,csrf)||blocked};
 }
 function create({host,document:doc=root.document,selector,onOpen=()=>{},onCollapse=()=>{},onSort=()=>{},onSlot=()=>{},csrf=()=>'',blocked=()=>false}={}){
  const groups=new Map(),rows=new Map();let lastLayout='',headerKey='',namesKey='';
  const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;},text=(n,s)=>{if(n.textContent!==s)n.textContent=s;};
  host.classList.add('banner-scroll');host.tabIndex=0;host.setAttribute('aria-label','设备列表，可横向滚动');
  const toolbar=doc.getElementById('dashboard-toolbar'),size=()=>host.style.setProperty('--toolbar-height',(toolbar?.getBoundingClientRect().height||64)+'px');
  const resize=new root.ResizeObserver(size);if(toolbar)resize.observe(toolbar);size();
  host.addEventListener('scroll',()=>host.classList.toggle('is-scrolled',host.scrollLeft>0),{passive:true});
  const grid=el('div','banner-grid'),header=el('div','banner-header banner-columns');grid.setAttribute('role','presentation');header.setAttribute('role','row');host.append(grid);const sticky=el('div','banner-sticky'),headScroll=el('div','banner-head-scroll');headScroll.append(header);sticky.append(headScroll);const table=el('div','banner-table');table.setAttribute('role','table');table.setAttribute('aria-label','设备列表');host.before(table);table.append(sticky,host);const sync=()=>{headScroll.scrollLeft=host.scrollLeft;sticky.classList.toggle('is-scrolled',host.scrollLeft>0);};host.addEventListener('scroll',sync,{passive:true});headScroll.addEventListener('scroll',()=>{if(host.scrollLeft!==headScroll.scrollLeft)host.scrollLeft=headScroll.scrollLeft;},{passive:true});
  const columns=[['name','设备'],['system','系统'],['cpu','CPU'],['memory','内存'],['disk','磁盘'],['rate','实时速率'],['traffic','周期流量'],['latency','延迟'],['outbound','出站'],['youtube','YouTube'],['price','价格 / 续费'],['upgrade','']];
  for(const [key,label]of columns){const cell=el('div','banner-cell');cell.setAttribute('role','columnheader');cell.dataset.column=key;
   if(key==='latency'){const sort=el('button','banner-sort','延迟');sort.type='button';sort.dataset.sort=key;sort.addEventListener('click',()=>onSort(key));const picker=el('details','banner-slot-picker'),summary=el('summary','banner-slot','槽 1 ▾'),options=el('div','banner-slot-options');summary.setAttribute('aria-label','延迟槽');for(let i=1;i<=3;i++){const o=el('button','',`槽 ${i}`);o.type='button';o.dataset.slot=i;o.addEventListener('click',()=>{onSlot(i);picker.open=false;summary.focus();});options.append(o);}picker.addEventListener('keydown',e=>{if(e.key==='Escape'){picker.open=false;summary.focus();e.stopPropagation();}});picker.addEventListener('toggle',()=>{if(picker.open){const at=summary.getBoundingClientRect();options.style.left=Math.max(8,Math.min(at.left,root.innerWidth-144))+'px';options.style.top=(at.bottom+4)+'px';}});host.addEventListener('scroll',()=>{picker.open=false;},{passive:true});picker.append(summary,options);cell.append(sort,picker);}
   else if(['name','system','cpu','memory','disk','rate','traffic','price','upgrade'].includes(key)){const b=el('button','banner-sort',label);b.type='button';b.dataset.sort=key;b.setAttribute('aria-label',label?label+'排序':'待升级排序');b.addEventListener('click',()=>onSort(key));cell.append(b);}
   else cell.textContent=label;header.append(cell);
  }
  function bar(){const box=el('div','banner-metric'),track=el('div','banner-track'),fill=el('i'),label=el('small');track.append(fill);box.append(track,label);return {box,fill,label};}
  function row(a){
   const node=el('div','banner-row banner-columns');node.dataset.agentId=a.agent_id;node.setAttribute('role','row');
   const cells=columns.map(([key])=>{const n=el('div','banner-cell');n.dataset.column=key;n.setAttribute('role','cell');node.append(n);return n;}),identity=el('div','banner-identity'),dot=el('i','banner-dot'),flag=el('span','banner-flag'),names=el('div','banner-names'),name=el('button','banner-name'),uptime=el('small');name.type='button';name.addEventListener('click',()=>onOpen(a.agent_id,name));names.append(name,uptime);identity.append(dot,flag,names);cells[0].append(identity);
   const system=el('span','banner-os');cells[1].append(system);const metrics=[bar(),bar(),bar()];metrics.forEach((m,i)=>cells[2+i].append(m.box));const tx=el('span'),rx=el('small');cells[5].append(tx,rx);const traffic=bar();cells[6].append(traffic.box);const youtube=el('span','banner-youtube');cells[9].append(youtube);const cost=el('span'),renew=el('small');cells[10].append(cost,renew);
   node.addEventListener('click',e=>{if(e.target.closest('button,a,input,select')||root.getSelection?.()?.toString())return;onOpen(a.agent_id,name);});
   function update(v,agent){
    node.dataset.lifecycle=v.lifecycle;node.setAttribute('aria-label',v.name);text(name,v.name);name.title=v.name;name.setAttribute('aria-label','查看 '+v.name+' 详情');text(uptime,v.uptime);if(flag.dataset.code!==v.country){flag.dataset.code=v.country;flag.replaceChildren(root.countryMark(v.country,agent.country_source));}dot.dataset.state=v.lifecycle;dot.title=C.statusHint(agent);if(system.dataset.os!==v.os){system.dataset.os=v.os;system.replaceChildren(root.systemLogo(v.os));}system.title=v.os;
    const setBar=(m,v)=>{m.box.dataset.tone=v.tone;m.fill.style.width=v.fill+'%';text(m.label,v.text);};metrics.forEach((m,i)=>setBar(m,v.metrics[i]));text(tx,v.tx);text(rx,v.rx);setBar(traffic,v.traffic);text(cells[7],v.latency.text);cells[7].title=v.latency.title;cells[7].dataset.state=v.latency.state;
    const trigger=selector.compactTrigger(agent);if(trigger.parentNode!==cells[8])cells[8].append(trigger);text(youtube,v.youtube.text);youtube.dataset.tone=v.youtube.tone;youtube.removeAttribute('title');text(cost,v.price);text(renew,v.renewal);renew.classList.toggle('due-soon',v.soon);if(v.upgrade){if(!cells[11].firstElementChild){const upgrade=el('button','banner-upgrade-link','待升级');upgrade.type='button';upgrade.addEventListener('click',()=>onOpen(a.agent_id,upgrade,'management'));cells[11].replaceChildren(upgrade);}}else cells[11].replaceChildren();cells[11].classList.toggle('banner-upgrade',v.upgrade);
   }
   return {node,update,key:''};
  }
  function render(model,prefs,{dirty=null,server,deadline=Infinity}={}){
   const names=model.list.map(a=>a.name||a.state?.hostname||'未命名 VPS'),nk=JSON.stringify(names);
   if(nk!==namesKey){namesKey=nk;const context=doc.createElement('canvas').getContext('2d'),fontSize=parseFloat(root.getComputedStyle(host).fontSize);context.font=`600 ${fontSize*13/12}px system-ui`;const longest=Math.max(0,...names.map(n=>context.measureText(n).width));grid.style.setProperty('--device-width',Math.min(14*13/12,Math.max(8,(longest+62)/fontSize))+'em');}
   sticky.style.setProperty('--toolbar-height',(toolbar?.getBoundingClientRect().height||64)+'px');sticky.style.setProperty('--device-width',grid.style.getPropertyValue('--device-width'));sync();const deferred=new Set(),visible=new Set(),live=new Set(model.list.map(a=>a.agent_id));let updates=0;
   const hk=JSON.stringify([prefs.sort,prefs.sortDir,prefs.latencySlot]);if(hk!==headerKey){headerKey=hk;text(header.querySelector('.banner-slot'),`槽 ${prefs.latencySlot||1} ▾`);for(const b of header.querySelectorAll('[data-slot]'))b.setAttribute('aria-pressed',String(Number(b.dataset.slot)===(prefs.latencySlot||1)));for(const b of header.querySelectorAll('[data-sort]')){const active=b.dataset.sort===prefs.sort;b.dataset.direction=active?(prefs.sortDir||'asc'):'';b.setAttribute('aria-label',(columns.find(c=>c[0]===b.dataset.sort)?.[1]||'待升级')+'排序'+(active?(prefs.sortDir==='desc'?'，降序':'，升序'):''));}}
   const layout=JSON.stringify(model.sections.map(g=>[g.id,prefs.collapsed.includes(g.id),g.items.map(a=>a.agent_id)])),changed=layout!==lastLayout;
   let position=grid.firstElementChild;
   for(const group of model.sections){let gv=groups.get(group.id);if(!gv){const node=el('section','banner-group'),head=el('button','banner-group-header'),body=el('div','banner-group-body');head.type='button';const label=el('span');head.append(label);head.addEventListener('click',()=>onCollapse(group.id));node.setAttribute('role','rowgroup');body.setAttribute('role','presentation');const heading=el('div'),cell=el('div');heading.setAttribute('role','row');cell.setAttribute('role','cell');cell.setAttribute('aria-colspan','12');cell.append(head);heading.append(cell);node.append(heading,body);gv={node,head,body,label};groups.set(group.id,gv);}const collapsed=prefs.collapsed.includes(group.id);text(gv.label,`${collapsed?'▸':'▾'} ${group.name} ${group.items.length} 台`);gv.head.parentNode.hidden=prefs.sort!=='group';gv.head.setAttribute('aria-expanded',String(!collapsed));gv.body.hidden=collapsed;
    if(changed){if(gv.node!==position)grid.insertBefore(gv.node,position);position=gv.node.nextElementSibling;}if(collapsed)continue;
    let rowPosition=gv.body.firstElementChild;
    for(const a of group.items){const id=a.agent_id;visible.add(id);let rv=rows.get(id);if(!rv||dirty===null||dirty.has(id)){
      if(updates&&performance.now()>=deadline){deferred.add(id);if(!rv)continue;}else{const v=projection(a,{slot:prefs.latencySlot||1,server,csrf:csrf(),blocked:blocked(id)}),key=JSON.stringify(v);updates++;if(!rv){rv=row(a);rows.set(id,rv);}if(key!==rv.key){rv.update(v,a);rv.key=key;}}
     }if(changed||rv.node.parentNode!==gv.body){if(rv.node!==rowPosition)gv.body.insertBefore(rv.node,rowPosition);}rowPosition=rv.node.nextElementSibling;
    }
   }
   for(const [id,rv]of rows)if(!visible.has(id)){rv.node.remove();if(!live.has(id))rows.delete(id);}
   for(const [id,gv]of groups)if(!model.sections.some(g=>g.id===id)){gv.node.remove();groups.delete(id);}
   if(!deferred.size)lastLayout=layout;return deferred;
  }
  return {render};
 }
 root.BannerView={create,price,renewal,latency,projection};
})(globalThis);
