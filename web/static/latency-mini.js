/* Face B: one day of one-minute RTT history (fixed square-root scale) and
   a two-handle range. The Server owns the samples; the page only draws them. */
(function(root){
  'use strict';
  const C=root.NetworkQualityCore||(typeof require==='function'?require('./network-quality-core.js'):null);
  const RS=root.RangeSlider||(typeof require==='function'?require('./range-slider.js'):null);
  const MINUTE=60000,HOUR=3600000,DAY=24*HOUR,TTL=120000,MIN_SPAN=10*MINUTE;
  const colors={telecom:'#3D6FA8',unicom:'#8A5BB0',mobile:'#4F5B62'};
  const finite=C.finite,integer=v=>Number.isSafeInteger(v)&&v>=0;
  const identity=t=>JSON.stringify([t.id,t.slot,t.family,t.protocol,t.slot_revision,t.status]);
  const metric=t=>t.protocol==='tcp'?'pure_connect':'echo_rtt';
  const number=v=>{const text=v>=1000?String(Math.round(v)):v>=10?v.toFixed(0):v>=1?v.toFixed(1):v.toFixed(2);return v>0&&Number(text)===0?String(Number(v.toPrecision(2))):text;};
  const stamp=v=>new Date(v).toLocaleString();
  const clock=v=>new Date(v).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',hour12:false});
  function rangeText(start,end){
    if(end-start>=DAY-MINUTE)return '近 24 小时';
    return new Date(start).toDateString()===new Date(end).toDateString()?`${clock(start)} – ${clock(end)}`:`${stamp(start)} – ${stamp(end)}`;
  }
  function normalize(h,t){
    const fail=()=>{throw Error('RTT 历史契约不匹配，未采用');};
    if(!h||!h.target||identity(h.target)!==identity(t)||h.view!=='1day'||h.resolution_ms!==MINUTE||!integer(h.now)||!integer(h.window_start)||
      h.window_start!==Math.floor(h.now/MINUTE)*MINUTE-1439*MINUTE||h.algorithm!=='weighted-grid-rtt-adjacent-v3'||h.histogram_version!==1||h.grid_ms!==.25||h.percentile_max_error_ms!==.125||!Array.isArray(h.groups))fail();
    const g=h.groups.find(g=>g.semantics==='staged_probe_v2'&&g.metric===metric(t));
    if(!g)return {now:h.now,points:[],supported:false};
    if(!Array.isArray(g.points)||g.points.length!==1440)fail();
    const points=g.points.map((p,i)=>{
      const start=h.window_start+i*MINUTE,end=Math.min(start+MINUTE,h.now);
      if(!p||p.start!==start||p.end!==end||!integer(p.count)||!integer(p.valid_rtts)||!integer(p.not_executed)||!integer(p.sent)||!integer(p.received)||p.received>p.sent||
        !(p.p50_5m_ms===null||finite(p.p50_5m_ms)&&p.p50_5m_ms>=0)||!(p.p50_ms===null||finite(p.p50_ms)&&p.p50_ms>=0)||!(p.p95_ms===null||finite(p.p95_ms)&&p.p95_ms>=0)||!['complete','partial','no_data'].includes(p.completeness)||!['known_gap','unverified'].includes(p.execution_coverage))fail();
      const mixed=p.count>0&&(p.config_revision_min!==p.config_revision_max||p.slot_revision_min!==p.slot_revision_max||p.slot_revision_min!==t.slot_revision);
      const reason=p.execution_coverage==='known_gap'?'缺测/未执行':mixed?'配置/槽位边界':!p.valid_rtts?'无有效 RTT':p.completeness==='no_data'?'无观测':'';
      // Partial ICMP uses ONLY actually replied Echo RTTs. No loss-to-RTT conversion.
      const partial=t.protocol==='tcp'&&p.execution_coverage==='known_gap'&&!mixed&&p.valid_rtts>0&&p.count>0&&end>start&&p.completeness!=='no_data'&&finite(p.p50_5m_ms);
      const value=(!reason||partial)&&p.count>0&&end>start&&finite(p.p50_5m_ms)?p.p50_5m_ms:null;
      const shown=value===null&&!reason&&p.valid_rtts>0&&p.count>0?'5 分钟窗口内有效 RTT 不足 3 个':reason;
      return {start,end,value,p50:finite(p.p50_ms)?p.p50_ms:null,p95:finite(p.p95_ms)?p.p95_ms:null,reason:shown,...(t.protocol==='tcp'?{partial}:{}),valid:p.valid_rtts,count:p.count,notExecuted:p.not_executed,sent:p.sent,received:p.received,
        completeness:p.completeness,provisional:!!p.provisional,config:[p.config_revision_min,p.config_revision_max],slot:[p.slot_revision_min,p.slot_revision_max],categories:p.categories||{}};
    });
    return {now:h.now,points,supported:true};
  }
  // The answer of v5/marks: runs of yellow / red blocks per lane. `to` may be earlier than asked (the Server stops at its evaluation cut-off).
  function normalizeMarks(v,family){
    const fail=()=>{throw Error('v5 标记契约不匹配，未采用');};
    if(!v||v.family!==family||![v.eval_end,v.from,v.to].every(finite)||!Array.isArray(v.lanes))fail();
    const runs=[];
    for(const lane of v.lanes){
      if(!lane||!C.slots.includes(lane.slot)||!Array.isArray(lane.runs))fail();
      for(const r of lane.runs){if(!r||!finite(r.start)||!finite(r.end)||r.end<=r.start||!['yellow','red'].includes(r.state))fail();runs.push({slot:lane.slot,start:r.start,end:r.end,state:r.state});}
    }
    return {family,from:v.from,to:v.to,evalEnd:v.eval_end,runs,slots:v.lanes.map(l=>l.slot)};
  }
  // Fixed ruler: sqrt(v/1000) over 0-1000ms, identical on every card. Above 1000ms is off the ruler and is drawn as a dashed run on the top edge.
  const CEIL=1000,DASH='3,2',GRID=[100,300,1000];
  function ratio(v){return v<=0?0:v>=CEIL?1:Math.sqrt(v/CEIL);}
  // Headline number: the 5-minute rolling P50 of the latest completed minute; null (name only) when that minute has none.
  function recent(data){
    const last=(data?.points||[]).filter(p=>p.end-p.start===MINUTE).at(-1);
    return last&&last.value!==null&&!last.partial?last.value:null;
  }
  const plain=v=>String(Number(v.toFixed(3)));
  const ms=v=>v===null?'—':plain(v)+'ms';
  const figures=p=>`5 分钟 P50 ${ms(p.value)} · 本分钟 P50 ${ms(p.p50)} · P95 ${ms(p.p95)}`;
  // Worst colour wins where runs of different lanes overlap.
  function mergeRuns(runs){
    const edges=[...new Set(runs.flatMap(r=>[r.start,r.end]))].sort((a,b)=>a-b),out=[];
    for(let i=0;i+1<edges.length;i++){
      const a=edges[i],b=edges[i+1],covering=runs.filter(r=>r.start<=a&&r.end>=b);
      if(!covering.length)continue;
      const state=covering.some(r=>r.state==='red')?'red':'yellow',last=out.at(-1);
      if(last&&last.end===a&&last.state===state)last.end=b;else out.push({start:a,end:b,state});
    }
    return out;
  }
  function create({document:doc=root.document,coordinator=null,state=root.AgentState,fetcher=(...args)=>root.fetch(...args),
    unauthorized=()=>root.location.assign('/login'),Observer=root.IntersectionObserver,Resize=root.ResizeObserver,
    interval=root.setInterval,clear=root.clearInterval,now=()=>Date.now(),monotonicNow=()=>root.performance?.now?.()??Date.now(),
    family=()=>'ipv4',setFamily=()=>{},openSettings=()=>{},raf=undefined}={}){
    const cards=new Map(),queue=coordinator||C.coordinator({now});let closed=false,timer=null;
    const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
    const btn=(text,fn,cls='')=>{const b=el('button',cls,text);b.type='button';b.addEventListener('click',fn);return b;};
    const inactive=c=>c.agent.revoked||c.agent.removal||c.agent.disabled_at;
    const effective=c=>!closed&&!c.dead&&c.active&&c.visible&&!doc.hidden&&c.node.isConnected&&!inactive(c);
    const targets=c=>C.slots.map(slot=>(c.config?.targets||[]).find(t=>t.slot===slot&&t.family===family(c.id)&&t.protocol===c.protocol)||{slot,status:'unavailable'});
    const key=(c,t)=>JSON.stringify(['latency-mini',c.id,c.config.revision,identity(t)]);
    const selected=c=>targets(c).filter(t=>c.isps.has(t.slot));
    const view=c=>({start:c.range.start,end:c.range.end});
    const label=(c,slot)=>C.cardLabel(c.config,slot);
    function invalidate(c,clearData=false){c.serial++;c.loading.clear();queue.release(c.owner);c.cursor=null;hideTip(c);if(clearData){c.cache.clear();}}
    function hideTip(c){c.tip.hidden=true;c.note.textContent='';}
    // The hover card restates the minute under the pointer, per lane.
    function tipText(c,epoch){
      const parts=[`${stamp(Math.floor(epoch/MINUTE)*MINUTE)} · 5 分钟滑动 P50 / 本分钟 P50、P95`];
      for(const t of selected(c)){
        const entry=c.cache.get(t.id),p=entry?.data?.points.find(p=>epoch>=p.start&&epoch<Math.max(p.start+1,p.end));
        const text=p?(p.value===null?p.reason||'无有效 RTT':figures(p)+(p.value>CEIL?' · 超出刻度':'')+(p.partial?' · 该分钟覆盖不完整':'')):entry?.error||(!t.id?'没有匹配目标':!entry?.data?.supported?'无 staged_probe_v2 历史':'此时间无桶');
        parts.push(`${label(c,t.slot)}：${text}${t.status==='paused'?' · 检测暂停':''}`);
      }
      return parts.join('\n');
    }
    function showTip(c,epoch,x){
      const text=tipText(c,epoch);c.note.textContent=text;c.tip.textContent=text;c.tip.hidden=false;
      const box=c.node.getBoundingClientRect(),plot=c.plot.getBoundingClientRect(),width=Math.min(250,Math.max(140,box.width-8));
      c.tip.style.width=width+'px';c.tip.style.left=Math.max(0,Math.min(box.width-width,x-box.left-width/2))+'px';c.tip.style.top=(plot.bottom-box.top+4)+'px';
    }
    function render(c){
      const {start,end}=view(c),rect=c.plot.getBoundingClientRect(),w=rect.width;if(!w)return;
      const h=rect.height,ph=h-2,values=[],rows=[],left=32;
      for(const t of selected(c)){const entry=c.cache.get(t.id),points=(entry?.data?.points||[]).filter(p=>p.start>=start&&p.start<=end);rows.push({t,entry,points});for(const p of points)if(p.value!==null)values.push(p.value);}
      const pw=Math.max(1,w-left),x=v=>left+(v-start)/(end-start)*pw,y=v=>ph-ratio(v)*(ph-4)-1,topY=y(CEIL),mw=pw/((end-start)/MINUTE),ns='http://www.w3.org/2000/svg';
      const svg=doc.createElementNS(ns,'svg');svg.setAttribute('viewBox',`0 0 ${w} ${h}`);svg.setAttribute('aria-hidden','true');
      const add=(tag,attrs,text)=>{const n=doc.createElementNS(ns,tag);for(const [k,v]of Object.entries(attrs))n.setAttribute(k,String(v));if(text!=null)n.textContent=text;svg.append(n);return n;};
      for(const v of GRID)add('path',{d:`M${left} ${y(v).toFixed(2)}H${w}`,fill:'none',stroke:'#e6ddd1','stroke-width':.6,'data-grid':v});
      add('path',{d:`M${left} 1V${ph}H${w}`,fill:'none',stroke:'#b9aa99','stroke-width':.6});
      for(const v of GRID)add('text',{x:left-4,y:Math.max(8,y(v)+3),fill:'#5E564D','font-size':9,'font-family':'Arial,sans-serif','text-anchor':'end'},String(v)).dataset.axisTick=String(v);
      for(const {t,points}of rows){
        const color=colors[t.slot],name=label(c,t.slot);let d='',prev=null,run=null;
        // Minutes above the ruler are one dashed run on the top edge; the solid line joins it from, and back to, the scale.
        const flush=()=>{if(!run)return;const x1=x(run.first.start),x2=x(run.last.start)+Math.max(mw,3);
          const dash=add('path',{d:`M${x1.toFixed(2)},${topY}H${x2.toFixed(2)}`,fill:'none',stroke:color,'stroke-width':1.4,'stroke-dasharray':DASH,'data-boundary':'above-scale','data-start':run.first.start,'data-minutes':run.n});
          const tip=doc.createElementNS(ns,'title');tip.textContent=run.n===1?`${name} ${stamp(run.first.start)} · ${figures(run.first)} · 超出刻度`:`${name} ${stamp(run.first.start)} — ${stamp(run.last.end)} · ${run.n} 分钟超出刻度，最高 P50 ${plain(run.max)}ms`;dash.append(tip);run=null;};
        for(let i=0;i<points.length;i++){const p=points[i];
          if(p.value===null){flush();prev=null;continue;}
          const over=p.value>CEIL,consec=prev&&p.start-prev.start===MINUTE;
          if(p.partial&&!over){flush();add('circle',{cx:x(p.start),cy:y(p.value),r:1.5,fill:'none',stroke:color,'stroke-width':1,'data-partial':'known_gap','data-start':p.start});prev=null;continue;}
          if(over){
            if(consec&&prev.over){run.last=p;run.n++;run.max=Math.max(run.max,p.value);d+=`M${x(p.start).toFixed(2)},${topY}`;}
            else{flush();run={first:p,last:p,n:1,max:p.value};d+=`${consec?'L':'M'}${x(p.start).toFixed(2)},${topY}`;}
          }else{
            flush();d+=`${consec?'L':'M'}${x(p.start).toFixed(2)},${y(p.value).toFixed(2)}`;
            const next=points[i+1];
            if(!consec&&!(next&&next.value!==null&&!(next.partial&&next.value<=CEIL)&&next.start-p.start===MINUTE))add('circle',{cx:x(p.start),cy:y(p.value),r:1.6,fill:color});
          }
          prev={start:p.start,over};
        }
        flush();if(d.includes('L'))add('path',{d,fill:'none',stroke:color,'stroke-width':1.3});
      }
      if(c.cursor!==null&&c.cursor>=start&&c.cursor<=end)add('path',{d:`M${x(c.cursor).toFixed(2)} 1V${ph}`,fill:'none',stroke:'#2A2622','stroke-width':.8,'data-cursor':'true'});
      const lanes=[...c.isps].map(slot=>C.laneNotice(c.config,slot,family(c.id),c.protocol)),stopped=lanes.filter(n=>n&&!n.measured),allStopped=c.config&&lanes.length>0&&stopped.length===lanes.length,
        message=!c.isps.size?'未选择卡槽':inactive(c)?'设备暂停/撤销':!c.config?'等待配置':allStopped?stopped[0].text:!values.length?(c.loading.size?'读取历史…':rows.some(r=>r.entry?.error)?'历史读取失败':'暂无有效 RTT'):rows.some(r=>r.entry?.error)||c.agent.online===false?'旧历史':'';
      if(message)add('text',{x:left+pw/2,y:14,fill:'#5E564D','font-size':10,'font-family':'Arial,sans-serif','text-anchor':'middle'},message);
      c.plot.replaceChildren(svg);
      c.plot.dataset.scale='fixed-sqrt';c.plot.dataset.start=String(start);c.plot.dataset.end=String(end);c.plot.dataset.pointCount=String(values.length);c.plot.dataset.axisLeft=String(left);
      c.plot.title='';
      c.plot.setAttribute('aria-label',`${stamp(start)} — ${stamp(end)} · 5 分钟滑动 P50（每分钟一个点）；固定平方根刻度 0–1000ms，超过 1000ms 用顶边虚线表示；左右方向键逐分钟查看${message?' · '+message:''}`);
      const caption=`${rangeText(start,end)} · ${c.follow?'跟随现在':'历史位置'}`;
      c.slider.element.title=`近 24 小时，拖动两端选择范围（当前：${caption}）；P50 线，固定平方根刻度 0–1000 ms`;
      c.node.dataset.protocol=c.protocol;
      for(const [protocol,b]of c.protocols)b.setAttribute('aria-pressed',String(protocol===c.protocol));
      for(const [f,b]of c.families)b.setAttribute('aria-pressed',String(f===family(c.id)));
      for(const [slot,b]of c.legend)b.setAttribute('aria-pressed',String(c.isps.has(slot)));
      for(const [slot,nodes]of c.legendNodes){const full=label(c,slot);nodes.nameNode.textContent=full;nodes.b.title=full;nodes.b.setAttribute('aria-label',full+' RTT 曲线');}
      // Headline numbers follow "now", not the selected range.
      const latest=new Map();for(const t of selected(c)){const v=recent(c.cache.get(t.id)?.data);if(v!==null)latest.set(t.slot,v);}
      const lowest=latest.size>1?Math.min(...latest.values()):null;
      for(const [slot,n] of c.nums){
        const on=c.isps.has(slot),v=latest.get(slot);
        const points=rows.find(r=>r.t.slot===slot)?.points||[],sent=points.reduce((n,p)=>n+p.sent,0),received=points.reduce((n,p)=>n+p.received,0);
        n.textContent=v===undefined?'':number(v);
        const loss=c.legendNodes.get(slot).loss;
        loss.hidden=c.protocol!=='icmp';loss.textContent=sent?`丢包 ${Number(((sent-received)/sent*100).toFixed(1))}%`:'丢包 —';
        loss.title=`当前框选范围 ${sent} 个 Echo，丢 ${sent-received} 个；无观测不计入分母`;
        n.className='lm-num'+(v!==undefined&&v===lowest?' lm-lowest':'')+(v!==undefined&&v>CEIL?' lm-over':'');
        n.dataset.state=!on?'off':v===undefined?'none':v>CEIL?'over':v===lowest?'lowest':'value';
      }
    }
    function request(c){
      if(!effective(c))return;
      if(!c.config?.supported){render(c);return;}
      const selectedTargets=selected(c).filter(t=>t.id&&['active','paused'].includes(t.status)),keep=new Set(selectedTargets.map(t=>key(c,t)));queue.release(c.owner,keep);
      for(const t of selectedTargets){const entry=c.cache.get(t.id);if(c.loading.has(t.id)||entry&&now()-entry.updated<TTL)continue;
        const serial=c.serial;c.loading.add(t.id);
        queue.request(key(c,t),c.owner,signal=>C.transportLease(tracked=>state.fetchJSON(`/api/v1/web/agents/${encodeURIComponent(c.id)}/network-quality/v3/history?target_id=${encodeURIComponent(t.id)}&view=1day`,{signal,fetcher:tracked,unauthorized,timeoutMs:10000}),fetcher)).then(h=>{
          if(serial!==c.serial||!effective(c)||!selected(c).some(p=>identity(p)===identity(t)))return;
          const data=normalize(h,t);c.cache.set(t.id,{data,updated:now(),error:'',response:{serial:c.responseSerial=(c.responseSerial||0)+1,serverNow:data.now,at:monotonicNow()}});
          c.latest=Math.max(c.latestServer||0,data.now);c.latestServer=c.latest;c.slider.setBounds(c.latest-DAY,c.latest);syncRange(c);render(c);
        }).catch(error=>{if(serial===c.serial&&effective(c)&&error.name!=='AbortError'){c.cache.set(t.id,{...c.cache.get(t.id),updated:now(),error:'历史读取失败'});render(c);}})
          .finally(()=>{if(serial===c.serial){c.loading.delete(t.id);render(c);}});
      }
      render(c);
    }
    // The range follows the slider; time moving on is the slider's business (setBounds keeps a following range on "now").
    function syncRange(c){const r=c.slider.getRange();c.range={start:r.start,end:r.end};c.follow=r.follow;}
    function setVisible(c,value){if(c.visible===value)return;c.visible=value;if(!value)invalidate(c);else request(c);render(c);}
    const observer=Observer?new Observer(entries=>{for(const e of entries){const c=cards.get(e.target.dataset.latencyAgent);if(c)setVisible(c,e.isIntersecting);}},{rootMargin:'0px'}):null;
    function fallback(){if(!observer)for(const c of cards.values()){const r=c.node.getBoundingClientRect();setVisible(c,c.node.isConnected&&r.width>0&&r.bottom>0&&r.top<(root.innerHeight||0)&&r.right>0&&r.left<(root.innerWidth||0));}}
    function segment(items,current,onPick,dataKey){
      const group=el('span','lm7-seg'),map=new Map();
      for(const [value,text] of items){const b=btn(text,()=>onPick(value));b.dataset.focusKey=dataKey+value;map.set(value,b);group.append(b);}
      return {group,map};
    }
    function mount(agent){
      let c=cards.get(agent.agent_id);
      if(!c){
        const node=el('section','latency-mini'),head=el('div','lm7-head'),legend=el('div','lm-legend'),plot=el('div','lm-plot'),
          rangeRow=el('div','lm7-range'),output=el('output','lm-note'),tip=el('div','lm7-tip');
        c={id:agent.agent_id,agent,node,plot,note:output,tip,owner:{},config:null,serial:0,dead:false,active:true,visible:false,nums:new Map(),cache:new Map(),loading:new Set(),
          protocol:'tcp',isps:new Set(C.slots),follow:true,latest:now(),latestServer:null,protocols:new Map(),families:new Map(),legend:new Map(),legendNodes:new Map(),cursor:null,resize:null};
        const t0=now();
        c.slider=RS.create({document:doc,min:t0-DAY,max:t0,start:t0-HOUR,end:t0,minSpan:MIN_SPAN,step:MINUTE,follow:true,labels:{start:'曲线开始时间',end:'曲线结束时间'},format:stamp,...(raf?{raf}:{}),
          onChange:(range,info)=>{c.range={start:range.start,end:range.end};c.follow=info.follow;c.cursor=null;hideTip(c);render(c);}});
        c.range={start:t0-HOUR,end:t0};
        node.dataset.latencyAgent=c.id;plot.tabIndex=0;plot.dataset.focusKey='lm-plot';output.id='lm-note-'+c.id;plot.setAttribute('aria-describedby',output.id);output.setAttribute('aria-live','polite');
        tip.hidden=true;tip.setAttribute('role','tooltip');
        const title=el('strong','lm7-title','网络质量');
        const proto=segment([['tcp','TCP'],['icmp','ICMP']],c.protocol,p=>{if(c.protocol===p)return;invalidate(c);c.protocol=p;render(c);request(c);},'lm-');c.protocols=proto.map;
        const fam=segment([['ipv4','IPv4'],['ipv6','IPv6']],'',f=>{if(family(c.id)!==f)setFamily(c.id,f);},'lm-family-');c.families=fam.map;
        const settings=btn('设置',event=>openSettings(c.id,event.currentTarget),'lm7-btn');settings.dataset.focusKey='lm-settings';
        head.append(title,proto.group,fam.group,settings);
        for(const slot of C.slots){const b=btn('',()=>{invalidate(c);if(c.isps.has(slot))c.isps.delete(slot);else c.isps.add(slot);render(c);request(c);});const dot=el('span','lm-dot');dot.setAttribute('aria-hidden','true');dot.style.background=colors[slot];const num=el('span','lm-num'),loss=el('span','lm-loss');loss.hidden=true;c.nums.set(slot,num);const nameNode=el('span','lm-name',C.names[slot]);c.legendNodes.set(slot,{b,nameNode,loss});b.title=C.names[slot];b.append(dot,nameNode,num,loss);b.style.setProperty('--lm-color',colors[slot]);b.setAttribute('aria-label',C.names[slot]+' RTT 曲线');b.dataset.focusKey='lm-'+slot;c.legend.set(slot,b);legend.append(b);}
        const span=el('span','lm7-span','24h');rangeRow.append(span,c.slider.element);
        head.title='每分钟一个点的 5 分钟滑动 P50；缺测断线；超过 1000 ms 用顶边虚线表示';
        node.append(head,legend,plot,rangeRow,output,tip);
        plot.addEventListener('pointermove',e=>{const r=plot.getBoundingClientRect(),left=32,{start,end}=view(c),at=start+Math.max(0,Math.min(1,(e.clientX-r.left-left)/Math.max(1,r.width-left)))*(end-start);c.cursor=Math.floor(at/MINUTE)*MINUTE;render(c);showTip(c,c.cursor,e.clientX);});
        plot.addEventListener('pointerleave',()=>{if(doc.activeElement!==plot){c.cursor=null;hideTip(c);render(c);}});
        plot.addEventListener('blur',()=>{c.cursor=null;hideTip(c);render(c);});
        plot.addEventListener('keydown',e=>{
          const {start,end}=view(c);
          if(['ArrowLeft','ArrowRight','Home','End'].includes(e.key)){e.preventDefault();const base=c.cursor??Math.floor(end/MINUTE)*MINUTE,step=(e.shiftKey?10:1)*MINUTE;
            c.cursor=Math.max(start,Math.min(end-MINUTE,e.key==='Home'?start:e.key==='End'?end-MINUTE:base+(e.key==='ArrowLeft'?-step:step)));render(c);showTip(c,c.cursor,plot.getBoundingClientRect().left+32+(c.cursor-start)/(end-start)*Math.max(1,plot.getBoundingClientRect().width-32));}
          else if(e.key==='Escape'){c.cursor=null;hideTip(c);render(c);}
        });
        // Deliberately no wheel handler; native vertical page scrolling is retained.
        cards.set(c.id,c);observer?.observe(node);if(Resize){c.resize=new Resize(()=>render(c));c.resize.observe(plot);}
      }
      const was=inactive(c);c.agent=agent;if(!was&&inactive(c))invalidate(c,true);render(c);return c.node;
    }
    function bindConfig(id,config){const c=cards.get(id);if(!c)return;
      const signature=JSON.stringify([config.revision,config.supported,config.enabled,config.targets.map(t=>[t.id,t.slot,t.family,t.protocol,t.slot_revision,t.status,t.lane_status,t.condition])]);if(signature===c.signature){c.config=config;render(c);return;}invalidate(c,true);
      c.signature=signature;c.config=config;c.latestServer=null;render(c);request(c);}
    // The page's address family changed (the same choice drives face A): the old curves belong to the other family.
    function familyChanged(id){const c=cards.get(id);if(!c)return;invalidate(c,true);render(c);request(c);}
    function retire(c){invalidate(c,true);c.dead=true;c.slider.destroy();c.resize?.disconnect();observer?.unobserve(c.node);c.node.remove();cards.delete(c.id);}
    function tick(){if(closed||doc.hidden)return;fallback();queue.pump();for(const c of cards.values())request(c);}
    function visibility(){if(doc.hidden){for(const c of cards.values())invalidate(c);if(timer!==null)clear(timer);timer=null;}else{if(timer===null&&!closed)timer=interval(tick,30000);tick();}}
    doc.addEventListener('visibilitychange',visibility);if(!observer){root.addEventListener?.('scroll',fallback,{passive:true});root.addEventListener?.('resize',fallback,{passive:true});}if(!doc.hidden)timer=interval(tick,30000);
    return {mount,bindConfig,familyChanged,tick,sync(ids){const live=new Set(ids);for(const c of [...cards.values()])if(!live.has(c.id))retire(c);},
      setActive(id,value){const c=cards.get(id);if(!c||c.active===value)return;c.active=value;if(!value)invalidate(c);else{render(c);request(c);}},
      setVisible(id,value){const c=cards.get(id);if(c)setVisible(c,value);},
      close(){if(closed)return;closed=true;if(timer!==null)clear(timer);observer?.disconnect();for(const c of [...cards.values()])retire(c);doc.removeEventListener('visibilitychange',visibility);if(!observer){root.removeEventListener?.('scroll',fallback);root.removeEventListener?.('resize',fallback);}if(!coordinator)queue.close();},
      snapshot:()=>({cards:cards.size,requests:queue.snapshot(),views:[...cards.values()].map(c=>({id:c.id,protocol:c.protocol,family:family(c.id),isps:[...c.isps],follow:c.follow,start:c.range.start,end:c.range.end,visible:c.visible,loading:c.loading.size,cachedTargets:c.cache.size}))})};
  }
  const api={rangeText,create,normalize,normalizeMarks,mergeRuns,ratio,recent};if(typeof module==='object'&&module.exports)module.exports=api;else root.LatencyMini=api;
})(globalThis);
