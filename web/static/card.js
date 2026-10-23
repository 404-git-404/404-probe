/* The agent card (R28): three modules (machine, traffic, network), each with a short face A and a detailed face B.
   Pure view models first (tested without a DOM), then the small DOM builders the page assembles the card from. */
(function(root) {
  'use strict';
  const CM=root.CardMetrics||(typeof require==='function'?require('./card-metrics.js'):null);
  const finite=v=>typeof v==='number'&&Number.isFinite(v);
  const DAY=86400000,FACE_KEY=id=>'404-probe:card-face:v1:'+id,FACE_PREFIX='404-probe:card-face:v1:';
  const HINT='点击空白处切换 简要 / 详细';
  const MODULES=['machine','traffic','network'];

  // ---- which face is shown ---------------------------------------------------------------------
  // Three characters, one per module (A = short, B = detailed). Anything else, or a storage that throws, is "AAA".
  const parseFaces=value=>typeof value==='string'&&/^[ABab]{3}$/.test(value)?value.toUpperCase():'AAA';
  // The header button: while any module shows A, all go to B; when none does, all go back to A.
  const flipAll=faces=>faces.includes('A')?'BBB':'AAA';
  function createFaceStore({storage=()=>root.localStorage}={}) {
    const memory=new Map();
    const read=id=>{
      if(memory.has(id))return memory.get(id);
      let value=null;try{value=storage()?.getItem(FACE_KEY(id));}catch(_){/* private window, blocked storage */}
      const faces=parseFaces(value);memory.set(id,faces);return faces;
    };
    const write=(id,faces)=>{memory.set(id,faces);try{storage()?.setItem(FACE_KEY(id),faces);}catch(_){/* kept in memory for this page only */}};
    // Devices that are gone take their key with them.
    function sync(ids) {
      const live=new Set(ids);
      if(!live.size)return; // no list yet (or all gone at once): never wipe the choices on that
      for(const id of [...memory.keys()])if(!live.has(id)){memory.delete(id);try{storage()?.removeItem(FACE_KEY(id));}catch(_){/* nothing to clean */}}
      try{
        const store=storage(),stale=[];
        for(let i=0;i<(store?.length||0);i++){const key=store.key(i);if(key?.startsWith(FACE_PREFIX)&&!live.has(key.slice(FACE_PREFIX.length)))stale.push(key);}
        for(const key of stale)store.removeItem(key);
      }catch(_){/* cleaning is best effort */}
    }
    return {read,write,sync};
  }

  // ---- formats ---------------------------------------------------------------------------------
  // Three significant digits, so a changing rate keeps a stable width: 369 B/s, 1.23 KB/s, 12.3 KB/s, 123 KB/s.
  function sig3(value,rate=false) {
    if(!finite(value)||value<0)return '—';
    const units=['B','KB','MB','GB','TB','PB','EB'];
    let n=value,i=0;
    while(n>=999.5&&i<units.length-1){n/=1000;i++;}
    let text;
    if(i===0)text=String(Math.round(n));
    else if(n>=99.95)text=n.toFixed(0);
    else if(n>=9.995)text=n.toFixed(1);
    else text=n.toFixed(2);
    return `${text} ${units[i]}${rate?'/s':''}`;
  }
  const sizeBinary=(v,rate=false)=>CM.size(v,rate,true);
  // Ring colour by value: normal, from 80 yellow, from 90 red. Old or unknown data is grey and empty.
  function ringTone(value,old=false) {
    const n=CM.percent(value);
    if(old||n==null)return 'unknown';
    return n>=90?'crit':n>=80?'warn':'ok';
  }
  const RING_R=24,RING_C=2*Math.PI*RING_R;
  const ringDash=(value,old=false)=>{const n=CM.percent(value);return `${(old||n==null?0:n/100*RING_C).toFixed(1)} ${RING_C.toFixed(1)}`;};

  // ---- machine ---------------------------------------------------------------------------------
  function ipRow(agent) {
    const lifecycle=Boolean(!agent.online||agent.disabled_at||agent.revoked||agent.state?.stale),egress=agent.state?.public_egress,mask=root.PublicEgressCheck?.maskIP||(v=>v);
    const parts=[];let any=false;
    for(const [family,name] of [['ipv4','IPv4'],['ipv6','IPv6']]) {
      const o=egress?.[family];
      if(!o?.checked_at){parts.push(`${name} —`);continue;}
      any=true;
      const has=o.status==='available'&&typeof o.ip==='string'&&o.ip.length>0,fresh=has&&!lifecycle&&!o.stale&&(!o.valid_until||o.valid_until>Date.now());
      parts.push(`${name} ${has?(fresh?mask(o.ip):'旧 '+mask(o.ip)):o.status==='unavailable'?'不可用':'未知'}`);
    }
    if(!any)return agent.public_egress_check?.enabled?'公网 IP 尚未检测到':'未检测（公网 IP 检测未启用）';
    return parts.join(' · ');
  }
  // observed: the sustained-load observation of card-metrics (cpu / io: 'critical' when it lasted); old: data is not current.
  function machineView(agent,observed={},old=false) {
    const s=agent.state||{},has=Boolean(agent.state),known=v=>finite(v)?v.toFixed(2):'—';
    const hotCPU=!old&&observed.cpu==='critical',hotIO=!old&&observed.io==='critical';
    const values=[s.cpu_percent,s.ram_percent,s.disk_percent];
    const rings=['cpu','memory','disk'].map((kind,i)=>{
      const value=has?values[i]:null,tone=ringTone(value,old||!has),name=['CPU','RAM','DISK'][i];
      const note=[kind==='cpu'&&hotCPU?'CPU 持续高负载':'',kind==='disk'&&hotIO?'磁盘 I/O 持续繁忙':'',old&&has?'旧数据，不是当前负载':''].filter(Boolean).join(' · ');
      return {kind,name,tone,dash:ringDash(value,old||!has),text:CM.pct(value),old:old&&has,title:`${name} ${CM.pct(value)}${note?' · '+note:''}`};
    });
    const flag=(on)=>on?'! ':'';
    const rows=[
      {key:'CPU',value:`${flag(hotCPU)}${s.cpu_cores?s.cpu_cores+' 核':'核心数未知'} · Load ${known(s.load1)} / ${known(s.load5)} / ${known(s.load15)} · Steal ${CM.pct(s.cpu_steal_percent)}`,hot:hotCPU},
      {key:'内存',value:`${flag(ringTone(s.ram_percent,old)==='warn'||ringTone(s.ram_percent,old)==='crit')}${CM.size(s.ram_used,false,true)} / ${CM.size(s.ram_total,false,true)} · Swap ${CM.size(s.swap_used,false,true)} / ${CM.size(s.swap_total,false,true)}`},
      {key:'磁盘',value:`${flag(hotIO||['warn','crit'].includes(ringTone(s.disk_percent,old)))}${CM.size(s.disk_used,false,true)} / ${CM.size(s.disk_total,false,true)} · 读 ${sizeBinary(s.disk_read_rate,true)} 写 ${sizeBinary(s.disk_write_rate,true)} · 繁忙 ${CM.pct(s.disk_busy_percent)}`,hot:hotIO},
      {key:'IP',value:ipRow(agent)},
    ];
    return {rings,rows:rows.map(r=>({...r,value:old&&has&&r.key!=='IP'?r.value+' · 旧数据':r.value}))};
  }

  // ---- traffic ---------------------------------------------------------------------------------
  const hasCycle=plan=>Boolean(plan&&plan.traffic_mode&&plan.cycle_kind&&plan.cycle_kind!=='none');
  function cycleDays(plan,now) {
    if(!finite(plan.cycle_start)||!finite(plan.cycle_end)||plan.cycle_end<=plan.cycle_start)return null;
    const total=Math.max(1,Math.round((plan.cycle_end-plan.cycle_start)/DAY)),day=Math.min(total,Math.max(1,Math.floor((now-plan.cycle_start)/DAY)+1));
    return {day,total,elapsed:Math.max(0,Math.min(1,(now-plan.cycle_start)/(plan.cycle_end-plan.cycle_start)))};
  }
  function resetText(plan) {
    if(!hasCycle(plan))return '未设置周期';
    const [year,month,day]=String(plan.cycle_anchor||'').split('-').map(Number),count=plan.cycle_count||1;
    if(plan.cycle_kind==='days')return count===1?'每天重置':`每 ${count} 天重置`;
    if(plan.cycle_kind==='yearly'&&month&&day)return count===1?`每年 ${month} 月 ${day} 日重置`:`每 ${count} 年重置（${month} 月 ${day} 日）`;
    if(plan.cycle_kind==='monthly'&&day)return count===1?`每月 ${day} 日重置`:`每 ${count} 个月重置（${day} 日）`;
    return '按套餐周期重置';
  }
  const bandwidthText=plan=>plan?.bandwidth_value?`${plan.bandwidth_value} ${plan.bandwidth_unit}`:'未设带宽';
  const dateText=ms=>{if(!finite(ms))return '';const d=new Date(ms);return `${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}`;};
  // The first cycle after the upgrade may hold only part of the direction split: say since when.
  function basisMark(plan) {
    const since=dateText(plan.cycle_dir_since);
    if(plan.cycle_dir_basis==='backfill')return {mark:'≈',title:`本周期的分方向数值由分钟数据回填，精度约一分钟，合计已与周期用量对齐${since?'（自 '+since+' 起）':''}`};
    if(plan.cycle_dir_basis==='since')return {mark:'*',title:`${since?'自 '+since+' 起统计':'从校准或升级后起统计'}；此前的用量没有分方向记录`};
    return {mark:'',title:''};
  }
  function trafficView(agent,{old=false,now=Date.now(),quotaDisplay=null}={}) {
    const s=agent.state||{},plan=agent.plan||{},traffic=agent.traffic||{},cycle=hasCycle(plan);
    const rate=value=>old||!finite(value)?'—':sig3(value,true);
    const down=cycle?plan.cycle_rx_bytes:traffic.rx_total,up=cycle?plan.cycle_tx_bytes:traffic.tx_total;
    const mark=cycle?basisMark(plan):{mark:'',title:''};
    const view={
      rx:`↓ ${rate(s.rx_rate)}`,tx:`↑ ${rate(s.tx_rate)}`,label:cycle?'周期':'累计',
      down:finite(down)?sig3(down):'—',up:finite(up)?sig3(up):'—',mark:mark.mark,markTitle:mark.title,
      bandwidth:bandwidthText(plan),oldMetrics:old,
    };
    if(!cycle)return {...view,bar:{kind:'none',text:'未设置流量配额 · 点此编辑套餐'}};
    const days=cycleDays(plan,now),dayText=days?`第 ${days.day}/${days.total} 天`:'周期尚未开始',right=`${dayText} · ${view.bandwidth}`;
    if(plan.quota_bytes==null)return {...view,bar:{kind:'nocap',left:'未设配额',right,time:days?days.elapsed:null}};
    const used=Number(plan.usage_bytes||0),quota=Number(plan.quota_bytes),percent=quota>0?used/quota*100:0;
    const quotaText=quotaDisplay?`${quotaDisplay.value} ${quotaDisplay.unit}`:sig3(quota);
    return {...view,bar:{kind:'bar',left:`已用 ${sig3(used)} / ${quotaText} · `,percent:percent.toFixed(1)+'%',tone:percent>=90?'crit':percent>=80?'warn':'ok',fill:Math.max(0,Math.min(100,percent)),right,time:days?days.elapsed:null,
      title:plan.usage_status==='calibrated'?'人工校准值，不补齐历史':'仅统计可观测增量，存在缺测'}};
  }
  function trafficDetail(agent) {
    const plan=agent.plan||{},traffic=agent.traffic||{},u=agent.usage_24h;
    let day24='近 24h　—';
    if(u&&finite(u.rx)&&finite(u.tx)){
      const hours=finite(u.coverage_ms)?u.coverage_ms/3600000:24,label=hours>=23.9?'近 24h':`近 ${Math.max(1,Math.round(hours))} 小时`;
      day24=`${label}　↓ ${sig3(u.rx)}　↑ ${sig3(u.tx)}`;
    }
    const since=finite(traffic.started_at)?`自 ${dateText(traffic.started_at)} 起`:'自接入起';
    return {day24,reset:resetText(plan),total:finite(traffic.rx_total)?`设备累计　↓ ${sig3(traffic.rx_total)}　↑ ${sig3(traffic.tx_total)}`:'设备累计　—',since};
  }

  // ---- one tap: blank area flips, controls and selections do not --------------------------------
  const INTERACTIVE='button,input,select,textarea,a,summary,label,[contenteditable],output,[role=tooltip],[role=slider],[data-no-flip],.metric-meaning';
  function attachFlip(node,{toggle,selection=()=>root.getSelection?.()?.toString()||''}) {
    let gesture=null,moved=false;
    node.addEventListener('pointerdown',e=>{gesture={id:e.pointerId,x:e.clientX,y:e.clientY};moved=false;},true);
    node.addEventListener('pointermove',e=>{if(gesture?.id===e.pointerId&&Math.hypot(e.clientX-gesture.x,e.clientY-gesture.y)>5)moved=true;},true);
    node.addEventListener('pointercancel',()=>{moved=true;gesture=null;},true);
    node.addEventListener('pointerup',()=>{gesture=null;},true);
    node.addEventListener('click',e=>{
      const dragged=moved;moved=false;
      if(dragged&&e.detail!==0)return;
      if(e.target?.closest?.(INTERACTIVE)||selection())return;
      toggle();
    });
  }

  // ---- DOM ---------------------------------------------------------------------------------------
  const NS='http://www.w3.org/2000/svg';
  const make=doc=>(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
  const hide=(node,hidden)=>{node.hidden=hidden;node.inert=hidden;};
  // A module owns two face nodes inside its host; switching only toggles hidden / inert, never the height.
  function createModule({document:doc=root.document,key,label,host,a,b,onFace=()=>{}}) {
    host.classList.add('c7-mod');host.dataset.module=key;host.title=HINT;host.setAttribute('aria-label',label);
    for(const [node,face] of [[a,'A'],[b,'B']]){node.classList.add('c7-face');node.dataset.face=face;if(node.parentNode!==host)host.append(node);}
    host.tabIndex=-1;
    let face=null;
    function set(next,{notify=true}={}) {
      if(next!=='A'&&next!=='B')return face;
      const focused=host.contains(doc.activeElement)?doc.activeElement:null;
      face=next;host.dataset.face=next;hide(a,next!=='A');hide(b,next!=='B');
      // A focused control that went away hands the focus to the module, not to the page.
      if(focused&&(a.contains(focused)||b.contains(focused))&&(focused.closest('[hidden]')))host.focus?.({preventScroll:true});
      if(notify)onFace(next);
      return face;
    }
    return {node:host,key,get face(){return face;},set,toggle(){return set(face==='A'?'B':'A');}};
  }
  function ringNode(doc,name) {
    const el=make(doc),root_=el('div','c7-ring'),box=el('div','c7-ring-art'),svg=doc.createElementNS(NS,'svg');
    svg.setAttribute('width','58');svg.setAttribute('height','58');svg.setAttribute('viewBox','0 0 58 58');svg.setAttribute('aria-hidden','true');
    const circle=cls=>{const c=doc.createElementNS(NS,'circle');for(const [k,v] of Object.entries({cx:29,cy:29,r:RING_R,fill:'none','stroke-width':8}))c.setAttribute(k,String(v));c.setAttribute('class',cls);return c;};
    const track=circle('c7-ring-track'),value=circle('c7-ring-value');value.setAttribute('stroke-linecap','round');svg.append(track,value);
    const label=el('span','c7-ring-name',name),text=el('span','c7-ring-pct');
    box.append(svg,label);root_.append(box,text);
    return {node:root_,update(ring){root_.dataset.tone=ring.tone;value.setAttribute('stroke-dasharray',ring.dash);if(text.textContent!==ring.text)text.textContent=ring.text;root_.dataset.old=String(ring.old);root_.title=ring.title;}};
  }
  function machineFaces(doc) {
    const el=make(doc),a=el('div','c7-machine-a'),b=el('div','c7-machine-b');
    const rings=['CPU','RAM','DISK'].map(name=>ringNode(doc,name));a.append(...rings.map(r=>r.node));
    const rows=['CPU','内存','磁盘','IP'].map(key=>{const row=el('div','c7-info'),k=el('span','c7-info-k',key),v=el('span','c7-info-v');row.append(k,v);b.append(row);return {row,v};});
    return {a,b,update(model){model.rings.forEach((ring,i)=>rings[i].update(ring));model.rows.forEach((r,i)=>{const {row,v}=rows[i];if(v.textContent!==r.value)v.textContent=r.value;row.title=`${r.key} ${r.value}`;row.dataset.hot=String(!!r.hot);});}};
  }
  function trafficFaces(doc,{chart,onEditPlan}) {
    const el=make(doc),a=el('div','c7-traffic-a'),b=el('div','c7-traffic-b');
    const line=el('div','c7-dirs'),sides=[0,1].map(i=>{const side=el('div','c7-dir'),rate=el('span',`c7-rate ${i?'c7-tx':'c7-rx'}`),cyc=el('span','c7-cyc'),label=el('span','c7-cyc-label'),amount=el('span','c7-cyc-amount'),mark=el('sup','c7-cyc-mark');cyc.append(label,amount,mark);side.append(rate,cyc);return {side,rate,label,amount,mark};});
    line.append(sides[0].side,el('span','c7-sep'),sides[1].side);
    const bar=el('div','c7-quota'),fill=el('span','c7-quota-fill'),tick=el('span','c7-quota-tick'),text=el('span','c7-quota-text'),left=el('span','c7-quota-left'),pctNode=el('span','c7-quota-pct'),right=el('span','c7-quota-right');
    text.append(left,pctNode,right);bar.append(fill,tick,text);
    const empty=el('button','c7-quota-empty');empty.type='button';empty.dataset.focusKey='traffic-plan';empty.addEventListener('click',()=>onEditPlan(empty));
    a.append(line,bar,empty);
    const spark=chart,d24=el('div','c7-row'),d24l=el('span'),d24r=el('span','c7-muted'),tot=el('div','c7-row'),totl=el('span'),totr=el('span','c7-muted');
    d24.append(d24l,d24r);tot.append(totl,totr);b.append(spark,d24,tot);
    return {a,b,emptyButton:empty,update(view,detail){
      sides.forEach((s,i)=>{const rate=i?view.tx:view.rx;if(s.rate.textContent!==rate)s.rate.textContent=rate;s.label.textContent=view.label+' ';s.amount.textContent=i?view.up:view.down;s.mark.textContent=view.mark;s.mark.hidden=!view.mark;
        s.side.title=view.mark?view.markTitle:(view.oldMetrics?'旧数据，速率不是当前值':'');s.side.dataset.old=String(view.oldMetrics);});
      const kind=view.bar.kind;
      bar.hidden=kind==='none';empty.hidden=kind!=='none';
      if(kind==='none'){empty.textContent=view.bar.text;}
      else{
        bar.dataset.kind=kind;bar.dataset.tone=view.bar.tone||'ok';fill.style.width=kind==='bar'?view.bar.fill+'%':'0%';fill.hidden=kind!=='bar';
        tick.hidden=view.bar.time==null;if(view.bar.time!=null)tick.style.left=(view.bar.time*100).toFixed(1)+'%';
        left.textContent=kind==='bar'?view.bar.left:view.bar.left;pctNode.textContent=kind==='bar'?view.bar.percent:'';right.textContent=view.bar.right;bar.title=view.bar.title||'';
      }
      d24l.textContent=detail.day24;d24r.textContent=detail.reset;totl.textContent=detail.total;totr.textContent=detail.since;
    }};
  }

  function agoText(ms,now=Date.now()){
    if(!finite(ms)||ms<=0)return '从未上报';
    const minutes=Math.floor(Math.max(0,now-ms)/60000);
    if(minutes<1)return '刚刚';if(minutes<60)return `${minutes} 分钟前`;
    const hours=Math.floor(minutes/60);return hours<48?`${hours} 小时前`:`${Math.floor(hours/24)} 天前`;
  }
  const api={agoText,HINT,MODULES,parseFaces,flipAll,createFaceStore,sig3,ringTone,ringDash,machineView,trafficView,trafficDetail,resetText,hasCycle,cycleDays,basisMark,ipRow,attachFlip,createModule,machineFaces,trafficFaces,INTERACTIVE};
  if(typeof module==='object'&&module.exports)module.exports=api;else root.Card=api;
})(globalThis);
