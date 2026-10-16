/* M05: fixed IPv4 RTT history, from the existing Server minute P95 rollup. */
(function(root){
  'use strict';
  const C=root.NetworkQualityCore||(typeof require==='function'?require('./network-quality-core.js'):null);
  const MINUTE=60000,HOUR=3600000,DAY=24*HOUR,TTL=120000;
  const colors={telecom:'#007d83',unicom:'#8653b1',mobile:'#ae7017'};
  const finite=C.finite,integer=v=>Number.isSafeInteger(v)&&v>=0;
  const identity=t=>JSON.stringify([t.id,t.slot,t.family,t.protocol,t.region,t.slot_revision,t.status]);
  const metric=t=>t.protocol==='tcp'?'pure_connect':'echo_rtt';
  const number=v=>{const text=v>=1000?String(Math.round(v)):v>=10?v.toFixed(0):v>=1?v.toFixed(1):v.toFixed(2);return v>0&&Number(text)===0?String(Number(v.toPrecision(2))):text;};
  const stamp=v=>new Date(v).toLocaleString();
  const clock=(v,dates)=>new Date(v).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',hour12:false})+(dates?' '+new Date(v).toLocaleDateString([],{month:'2-digit',day:'2-digit'}):'');
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
        !(p.p95_ms===null||finite(p.p95_ms)&&p.p95_ms>=0)||!['complete','partial','no_data'].includes(p.completeness)||!['known_gap','unverified'].includes(p.execution_coverage))fail();
      const mixed=p.count>0&&(p.config_revision_min!==p.config_revision_max||p.slot_revision_min!==p.slot_revision_max||p.slot_revision_min!==t.slot_revision);
      const reason=p.execution_coverage==='known_gap'?'缺测/未执行':mixed?'配置/槽位边界':!p.valid_rtts?'无有效 RTT':p.completeness==='no_data'?'无观测':'';
      // Partial ICMP uses ONLY actually replied Echo RTTs. No loss-to-RTT conversion.
      const partial=t.protocol==='tcp'&&p.execution_coverage==='known_gap'&&!mixed&&p.valid_rtts>0&&p.count>0&&end>start&&p.completeness!=='no_data'&&finite(p.p95_ms);
      const value=(!reason||partial)&&p.count>0&&end>start&&finite(p.p95_ms)?p.p95_ms:null;
      return {start,end,value,reason,...(t.protocol==='tcp'?{partial}:{}),valid:p.valid_rtts,count:p.count,notExecuted:p.not_executed,sent:p.sent,received:p.received,
        completeness:p.completeness,provisional:!!p.provisional,config:[p.config_revision_min,p.config_revision_max],slot:[p.slot_revision_min,p.slot_revision_max],categories:p.categories||{}};
    });
    return {now:h.now,points,supported:true};
  }
  function axis(values){
    const positive=values.filter(v=>v>0),max=Math.max(0,...values),min=positive.length?Math.min(...positive):0;
    const log=max>0&&min>0&&max/min>100;
    const ceiling=max?max*1.08:1;
    // A disclosed log1p scale keeps zero, sub-ms and isolated long RTTs honest.
    const transform=v=>log?Math.log10(1+v/.125):v;
    return {max:ceiling,log,ratio:v=>transform(v)/transform(ceiling)};
  }
  function tcpLevel(max){
    if(max<=2000)return 2000;
    const p=10**Math.floor(Math.log10(max));
    return [1,2,5,10].map(v=>v*p).find(v=>v>=max);
  }
  function tcpAxis(status){
    const ceiling=tcpLevel(status?.level_ms||2000);
    return {min:5,max:ceiling,log:true,warning:status?.state==='alert',
      ratio:v=>v>0?(Math.log10(v)-Math.log10(5))/(Math.log10(ceiling)-Math.log10(5)):null};
  }
  function create({document:doc=root.document,coordinator=null,axes=null,state=root.AgentState,fetcher=(...args)=>root.fetch(...args),
    unauthorized=()=>root.location.assign('/login'),Observer=root.IntersectionObserver,Resize=root.ResizeObserver,
    interval=root.setInterval,clear=root.clearInterval,now=()=>Date.now(),monotonicNow=()=>root.performance?.now?.()??Date.now()}={}){
    const cards=new Map(),queue=coordinator||C.coordinator({now});let closed=false,timer=null;
    const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
    const btn=(text,fn)=>{const b=el('button','',text);b.type='button';b.addEventListener('click',fn);return b;};
    const inactive=c=>c.agent.revoked||c.agent.removal||c.agent.disabled_at;
    const effective=c=>!closed&&!c.dead&&c.active&&c.visible&&!doc.hidden&&c.node.isConnected&&!inactive(c);
    const targets=c=>C.slots.map(slot=>(c.config?.targets||[]).find(t=>t.slot===slot&&t.family==='ipv4'&&t.protocol===c.protocol&&t.region===c.config.region_id)||{slot,status:'unavailable'});
    const key=(c,t)=>JSON.stringify(['latency-mini',c.id,c.config.revision,c.config.region_id,identity(t)]);
    const selected=c=>targets(c).filter(t=>c.isps.has(t.slot));
    function bounds(c){return {start:c.latest-DAY,end:c.latest};}
    function clamp(c){const b=bounds(c);c.end=Math.max(b.start+c.hours*HOUR,Math.min(b.end,c.end));}
    function view(c){if(c.follow)c.end=c.latest;else clamp(c);return [c.end-c.hours*HOUR,c.end];}
    function invalidate(c,clearData=false){axes?.release(c.id,'C');c.serial++;c.loading.clear();queue.release(c.owner);c.drag=null;c.note.textContent='';if(clearData)c.cache.clear();}
    function note(c,epoch){
      const parts=[`${stamp(view(c)[0])} — ${stamp(c.end)} · ${c.follow?'跟随现在':'历史位置'}\n一分钟 RTT P95 · ${c.protocol==='tcp'?'纯 Connect，不含 DNS':'Echo Reply RTT，仅实际回包'} · 量化误差≤0.125ms`];
      for(const t of selected(c)){
        const entry=c.cache.get(t.id),p=entry?.data?.points.find(p=>epoch>=p.start&&epoch<Math.max(p.start+1,p.end));
        parts.push(`${C.names[t.slot]}：${p?`${stamp(p.start)} — ${stamp(p.end)}\n${p.value===null?p.reason||'无有效 RTT':p.value.toFixed(3)+'ms'}${p.partial?' · 存在有效 RTT，但该分钟覆盖不完整':''} · 有效 RTT ${p.valid} / 观测 ${p.count} · ${p.completeness}${p.provisional?' · 未最终封存':''}\n${c.protocol==='icmp'?`完整窗口分母：发送 ${p.sent} / 回包 ${p.received} · 丢包 ${p.sent?((p.sent-p.received)/p.sent*100).toFixed(1)+'%':'分母未知'} · `:''}未执行 ${p.notExecuted} · 配置 ${p.config.join('—')} / 槽位 ${p.slot.join('—')}\n分类 ${JSON.stringify(p.categories)}`:entry?.error||(!t.id?'没有匹配目标':!entry?.data?.supported?'无 staged_probe_v2 历史':'此时间无桶')}${entry?.error?' · 读取失败，旧历史':''}${c.agent.online===false?' · 离线历史':''}${t.status==='paused'?' · 检测暂停':''}`);
      }
      c.note.textContent=parts.join('\n');
    }
    function render(c){
      const [start,end]=view(c),rect=c.plot.getBoundingClientRect(),w=rect.width;if(!w)return;
      const h=rect.height,ph=h-12,values=[],rows=[];
      for(const t of selected(c)){const entry=c.cache.get(t.id),points=(entry?.data?.points||[]).filter(p=>p.start>=start&&p.start<=end);rows.push({t,entry,points});for(const p of points)if(p.value!==null)values.push(p.value);}
      const scope=JSON.stringify([c.protocol,[...c.isps],c.hours,c.follow,c.config?.revision,c.config?.region_id]);if(scope!==c.axisScope){c.axisScope=scope;c.axisNeed=0;}
      const a=c.protocol==='tcp'?tcpAxis(c.tcpEvidence):axis(values),measure=c.measure||(c.measure=doc.createElement('canvas').getContext('2d'));measure.font='10px Arial';
      const highLabel=number(a.max)+' ms',lowLabel=(a.min||0)+' ms';measure.font=(a.warning?'700 ':'')+'10px Arial';const highWidth=measure.measureText(highLabel).width;measure.font='10px Arial';const need=Math.ceil(Math.max(highWidth,measure.measureText(lowLabel).width)+5+2);
      c.axisNeed=Math.max(c.axisNeed||0,need);const left=axes?axes.fit(c.id,'C',c.plot,c.axisNeed,()=>{if(!closed&&!c.dead&&c.active)render(c);}):c.axisNeed;
      c.axisLeft=left;c.plot.dataset.axisLeft=String(left);c.plot.dataset.axisRequired=String(need);
      const pw=Math.max(1,w-left),x=v=>left+(v-start)/(end-start)*pw,y=v=>ph-a.ratio(v)*(ph-2),ns='http://www.w3.org/2000/svg';
      const svg=doc.createElementNS(ns,'svg');svg.setAttribute('viewBox',`0 0 ${w} ${h}`);svg.setAttribute('aria-hidden','true');
      const add=(tag,attrs,text)=>{const n=doc.createElementNS(ns,tag);for(const [k,v]of Object.entries(attrs))n.setAttribute(k,String(v));if(text!=null)n.textContent=text;svg.append(n);return n;};
      const txt=(x,y,text,anchor='start')=>add('text',{x,y,fill:'#4e5965','font-size':10,'font-family':'Arial,sans-serif','text-anchor':anchor},text);
      add('path',{d:`M${left} 1V${ph}H${w}`,fill:'none',stroke:'#b9aa99','stroke-width':.6});
      const high=txt(left-5,9,highLabel,'end'),low=txt(left-5,ph,lowLabel,'end');high.dataset.axisTick='max';low.dataset.axisTick='min';
      if(c.protocol==='tcp'){
        high.setAttribute('fill',a.warning?'#c42b32':'currentColor');high.setAttribute('font-weight',a.warning?'700':'400');
        const mid=txt(left-5,y(100)+3,'100 ms','end');mid.dataset.axisTick='mid';
      }
      for(const [i,v]of [start,(start+end)/2,end].entries()){const label=clock(v,false);txt(i===0?left:i===2?w:left+pw/2,h-1,label,i===0?'start':i===2?'end':'middle');}
      if(a.log)txt(w-2,9,c.protocol==='tcp'?'log10':'log','end');
      const outside=p=>c.protocol==='tcp'&&p.value!==null&&(p.value<5||p.value>a.max);
      let overflow=0,underflow=0;
      for(const {t,points}of rows){let d='',previous=null;for(const p of points){if(p.value===null||p.partial||outside(p)){previous=null;continue;}d+=(previous&&p.start-previous.start===MINUTE?'L':'M')+`${x(p.start).toFixed(2)},${y(p.value).toFixed(2)}`;previous=p;}
        if(d)add('path',{d,fill:'none',stroke:colors[t.slot],'stroke-width':1.2});
        // Every genuine isolated observation remains a point at its actual timestamp.
        for(let i=0;i<points.length;i++){const p=points[i];
          if(outside(p)){
            const px=x(p.start),py=p.value>a.max?2:ph-2,zero=p.value===0;
            if(p.value>a.max)overflow++;else underflow++;
            const marker=add('path',{d:zero?`M${px},${py-2}l2,2 -2,2 -2,-2Z`:p.value>a.max?`M${px},${py}l-2,4h4Z`:`M${px},${py}l-2,-4h4Z`,fill:p.partial?'none':colors[t.slot],stroke:colors[t.slot],'stroke-width':.6,'data-boundary':zero?'quantized-zero':p.value>a.max?'historical-overflow':'below-5ms','data-start':p.start,'data-partial':p.partial?'known_gap':''});
            const title=doc.createElementNS(ns,'title');title.textContent=`${C.names[t.slot]} ${stamp(p.start)} · ${zero?'量化为0，低于量化分辨率':p.value+' ms'}${p.partial?' · 覆盖不完整':''} · ${p.value>a.max?'可视历史越界（此标记不独立决定今日告警）':'低于5ms轴下界，边界符号不代表5ms'}`;marker.append(title);continue;
          }
          if(p.partial){add('circle',{cx:x(p.start),cy:y(p.value),r:1.5,fill:'none',stroke:colors[t.slot],'stroke-width':1,'data-partial':'known_gap','data-start':p.start});continue;}if(p.value!==null&&(!points[i-1]||points[i-1].value===null||points[i-1].partial||outside(points[i-1])||p.start-points[i-1].start!==MINUTE)&&(!points[i+1]||points[i+1].value===null||points[i+1].partial||outside(points[i+1])||points[i+1].start-p.start!==MINUTE))add('circle',{cx:x(p.start),cy:y(p.value),r:1.6,fill:colors[t.slot]});}
      }
      const message=!c.isps.size?'未选择 ISP':inactive(c)?'设备暂停/撤销':!c.config?'等待地区配置':!c.config.supported?'Agent 不支持':!values.length?(c.loading.size?'读取历史…':rows.some(r=>r.entry?.error)?'历史读取失败':'暂无有效 RTT'):rows.some(r=>r.entry?.error)||c.agent.online===false?'旧历史':'';
      if(message)txt(left+pw/2,14,message,'middle');
      if(c.protocol==='tcp'&&(overflow||underflow))txt(left+3,9,`${overflow?'▲历史越界 ':''}${underflow?'◆0 / ▼<5ms':''}`);
      if(c.protocol==='tcp'&&(!c.tcpEvidence||c.tcpEvidence.state==='unknown'||c.tcpEvidenceError))txt(left+3,ph-3,'告警覆盖不足');
      c.plot.replaceChildren(svg);c.plot.dataset.scale=c.protocol==='tcp'?'log10':a.log?'log1p':'linear';c.plot.dataset.warning=String(!!a.warning);c.plot.dataset.start=String(start);c.plot.dataset.end=String(end);c.plot.dataset.pointCount=String(values.length);
      c.plot.title=`${stamp(start)} — ${stamp(end)} · 一分钟 RTT P95；${c.protocol==='tcp'?`Log10，5ms至${a.max}ms；▲历史越界；◆量化0，▼低于5ms（边界标记）；${c.tcpEvidence?.timezone||'浏览器本地时区'}；${c.tcpEvidence?.coverage||'告警覆盖不足'}；告警来源日 ${c.tcpEvidence?.source_day||'无可证明事件'}`:a.log?'对数 log1p(ms/0.125)，无裁剪':'自动毫秒轴，无裁剪'}；横拖平移，方向键平移，Enter 回到现在；滚轮只滚动页面`;
      c.plot.setAttribute('aria-label',c.plot.title+(message?' · '+message:''));
      c.span.textContent=c.hours+'h';c.span.title='当前可视时间跨度：'+c.hours+'小时';c.slider.setAttribute('aria-valuetext',c.hours+'小时');c.now.setAttribute('aria-pressed',String(c.follow));
      for(const [protocol,b]of c.protocols)b.setAttribute('aria-pressed',String(protocol===c.protocol));
      for(const [slot,b]of c.legend)b.setAttribute('aria-pressed',String(c.isps.has(slot)));
    }
    function request(c){
      if(!effective(c))return;
      if(c.protocol==='tcp'&&!c.evidenceLoading&&(!c.evidenceUpdated||now()-c.evidenceUpdated>=30000)){
        c.evidenceLoading=true;const serial=c.serial,zone=Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC';
        queue.request(JSON.stringify(['tcp-mini-state',c.id,zone]),c.owner,signal=>C.transportLease(tracked=>state.fetchJSON(`/api/v1/web/agents/${encodeURIComponent(c.id)}/network-quality/v3/tcp-mini?timezone=${encodeURIComponent(zone)}`,{signal,fetcher:tracked,unauthorized,timeoutMs:10000}),fetcher)).then(status=>{
          if(c.dead||serial!==c.serial)return;
          if(!status||!['normal','alert','unknown'].includes(status.state)||!finite(status.level_ms)||status.level_ms<2000||!integer(status.now))throw Error('invalid TCP Mini state');
          c.tcpEvidence=status;c.tcpEvidenceError=false;c.evidenceUpdated=now();render(c);
        }).catch(()=>{if(!c.dead&&serial===c.serial){c.tcpEvidenceError=true;c.tcpEvidence={...c.tcpEvidence,state:'unknown'};c.evidenceUpdated=now();render(c);}}).finally(()=>{c.evidenceLoading=false;if(serial!==c.serial&&effective(c))request(c);});
      }
      if(!c.config?.supported)return;
      const selectedTargets=selected(c).filter(t=>t.id&&['active','paused'].includes(t.status)),keep=new Set(selectedTargets.map(t=>key(c,t)));if(c.protocol==='tcp')keep.add(JSON.stringify(['tcp-mini-state',c.id,Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC']));queue.release(c.owner,keep);
      for(const t of selectedTargets){const entry=c.cache.get(t.id);if(c.loading.has(t.id)||entry&&now()-entry.updated<TTL)continue;
        const serial=c.serial;c.loading.add(t.id);
        queue.request(key(c,t),c.owner,signal=>C.transportLease(tracked=>state.fetchJSON(`/api/v1/web/agents/${encodeURIComponent(c.id)}/network-quality/v3/history?target_id=${encodeURIComponent(t.id)}&view=1day`,{signal,fetcher:tracked,unauthorized,timeoutMs:10000}),fetcher)).then(h=>{
          if(serial!==c.serial||!effective(c)||!selected(c).some(p=>identity(p)===identity(t)))return;
          const data=normalize(h,t);c.cache.set(t.id,{data,updated:now(),error:'',response:{serial:c.responseSerial=(c.responseSerial||0)+1,serverNow:data.now,at:monotonicNow()}});c.latest=Math.max(c.latestServer||0,data.now);c.latestServer=c.latest;render(c);
        }).catch(error=>{if(serial===c.serial&&effective(c)&&error.name!=='AbortError'){c.cache.set(t.id,{...c.cache.get(t.id),updated:now(),error:'历史读取失败'});render(c);}})
          .finally(()=>{if(serial===c.serial){c.loading.delete(t.id);render(c);}});
      }
      render(c);
    }
    function setVisible(c,value){if(c.visible===value)return;c.visible=value;if(!value)invalidate(c);else request(c);render(c);}
    const observer=Observer?new Observer(entries=>{for(const e of entries){const c=cards.get(e.target.dataset.latencyAgent);if(c)setVisible(c,e.isIntersecting);}},{rootMargin:'0px'}):null;
    function fallback(){if(!observer)for(const c of cards.values()){const r=c.node.getBoundingClientRect();setVisible(c,c.node.isConnected&&r.width>0&&r.bottom>0&&r.top<(root.innerHeight||0)&&r.right>0&&r.left<(root.innerWidth||0));}}
    function pan(c,delta){c.follow=false;c.end+=delta;clamp(c);c.note.textContent='';render(c);}
    function current(c){c.follow=true;c.end=c.latest;c.note.textContent='';render(c);request(c);}
    function mount(agent){
      let c=cards.get(agent.agent_id);
      if(!c){
        const node=el('section','latency-mini'),top=el('div','lm-top'),legend=el('div','lm-legend'),plot=el('div','lm-plot'),bottom=el('div','lm-bottom'),slider=el('input'),span=el('span','lm-span','1h'),output=el('output','lm-note');
        c={id:agent.agent_id,agent,node,plot,slider,span,note:output,owner:{},config:null,serial:0,dead:false,active:true,visible:false,cache:new Map(),loading:new Set(),protocol:'tcp',isps:new Set(C.slots),hours:1,follow:true,latest:now(),latestServer:null,end:now(),protocols:new Map(),legend:new Map(),drag:null,resize:null};
        node.dataset.latencyAgent=c.id;plot.tabIndex=0;plot.dataset.focusKey='lm-plot';output.id='lm-note-'+c.id;plot.setAttribute('aria-describedby',output.id);
        for(const p of ['tcp','icmp']){const b=btn(p.toUpperCase(),()=>{if(c.protocol===p)return;invalidate(c);c.protocol=p;render(c);request(c);});b.dataset.focusKey='lm-'+p;c.protocols.set(p,b);top.append(b);}
        for(const slot of C.slots){const b=btn('',()=>{invalidate(c);if(c.isps.has(slot))c.isps.delete(slot);else c.isps.add(slot);render(c);request(c);});const dot=el('span','lm-dot');dot.setAttribute('aria-hidden','true');b.append(dot,el('span','',C.names[slot]));b.style.setProperty('--lm-color',colors[slot]);b.setAttribute('aria-label',C.names[slot]+' RTT 曲线');b.dataset.focusKey='lm-'+slot;c.legend.set(slot,b);legend.append(b);}top.append(span,legend);
        slider.type='range';slider.min='1';slider.max='24';slider.step='1';slider.value='24';slider.setAttribute('aria-label','时间范围：左24小时，右1小时');slider.dataset.focusKey='lm-hours';
        slider.addEventListener('input',()=>{const center=c.end-c.hours*HOUR/2;c.hours=25-Number(slider.value);if(!c.follow)c.end=center+c.hours*HOUR/2;clamp(c);c.note.textContent='';render(c);});
        c.now=btn('现在',()=>current(c));c.now.dataset.focusKey='lm-now';bottom.append(el('span','','24h'),slider,el('span','','1h'),c.now);node.append(top,plot,bottom,output);
        plot.addEventListener('pointerdown',e=>{if(e.isPrimary===false||e.button!==0||e.clientX<plot.getBoundingClientRect().left+(c.axisLeft||0))return;c.drag={id:e.pointerId,x:e.clientX,y:e.clientY,end:c.end,hours:c.hours,moved:false,touch:e.pointerType==='touch'};if(!c.drag.touch)plot.setPointerCapture(e.pointerId);});
        plot.addEventListener('pointermove',e=>{const r=plot.getBoundingClientRect(),d=c.drag;if(d&&d.id===e.pointerId){const dx=e.clientX-d.x,dy=e.clientY-d.y;if(!d.moved&&Math.abs(dx)<4)return;if(d.touch&&!d.moved&&Math.abs(dy)>Math.abs(dx)){c.drag=null;return;}d.moved=true;c.follow=false;c.end=d.end-dx/Math.max(1,r.width-(c.axisLeft||0))*d.hours*HOUR;clamp(c);c.note.textContent='';render(c);}else if(e.pointerType!=='touch')note(c,view(c)[0]+Math.max(0,Math.min(1,(e.clientX-r.left-(c.axisLeft||0))/Math.max(1,r.width-(c.axisLeft||0))))*c.hours*HOUR);});
        const endDrag=e=>{if(c.drag?.id!==e.pointerId)return;c.drag=null;if(plot.hasPointerCapture(e.pointerId))plot.releasePointerCapture(e.pointerId);};plot.addEventListener('pointerup',endDrag);plot.addEventListener('pointercancel',endDrag);
        plot.addEventListener('pointerleave',()=>{if(!c.drag&&doc.activeElement!==plot)c.note.textContent='';});plot.addEventListener('blur',()=>c.note.textContent='');
        plot.addEventListener('keydown',e=>{if(['ArrowLeft','ArrowRight','Home','End','Enter'].includes(e.key)){e.preventDefault();if(e.key==='Home'){c.follow=false;c.end=c.latest-DAY+c.hours*HOUR;render(c);}else if(e.key==='End'||e.key==='Enter')current(c);else pan(c,(e.key==='ArrowLeft'?-1:1)*c.hours*HOUR/4);}else if(e.key===' '){e.preventDefault();note(c,c.end-MINUTE);}else if(e.key==='Escape')c.note.textContent='';});
        // Deliberately no wheel handler; native vertical page scrolling is retained.
        cards.set(c.id,c);observer?.observe(node);if(Resize){c.resize=new Resize(()=>render(c));c.resize.observe(plot);}
      }
      const was=inactive(c);c.agent=agent;if(!was&&inactive(c))invalidate(c,true);render(c);return c.node;
    }
    function bindConfig(id,config){const c=cards.get(id);if(!c)return;const signature=JSON.stringify([config.revision,config.region_id,config.supported,config.enabled,config.targets]);if(signature===c.signature)return;invalidate(c,true);c.signature=signature;c.config=config;c.latestServer=null;render(c);request(c);}
    function retire(c){invalidate(c,true);c.dead=true;c.resize?.disconnect();observer?.unobserve(c.node);c.node.remove();cards.delete(c.id);}
    function tick(){if(closed||doc.hidden)return;fallback();queue.pump();for(const c of cards.values())request(c);}
    function visibility(){if(doc.hidden){for(const c of cards.values())invalidate(c);if(timer!==null)clear(timer);timer=null;}else{if(timer===null&&!closed)timer=interval(tick,30000);tick();}}
    doc.addEventListener('visibilitychange',visibility);if(!observer){root.addEventListener?.('scroll',fallback,{passive:true});root.addEventListener?.('resize',fallback,{passive:true});}if(!doc.hidden)timer=interval(tick,30000);
    return {mount,bindConfig,tick,sync(ids){const live=new Set(ids);for(const c of [...cards.values()])if(!live.has(c.id))retire(c);},
      setActive(id,value){const c=cards.get(id);if(!c||c.active===value)return;c.active=value;if(!value)invalidate(c);else{render(c);request(c);}},
      setVisible(id,value){const c=cards.get(id);if(c)setVisible(c,value);},
      close(){if(closed)return;closed=true;if(timer!==null)clear(timer);observer?.disconnect();for(const c of [...cards.values()])retire(c);doc.removeEventListener('visibilitychange',visibility);if(!observer){root.removeEventListener?.('scroll',fallback);root.removeEventListener?.('resize',fallback);}if(!coordinator)queue.close();},
      snapshot:()=>({cards:cards.size,requests:queue.snapshot(),views:[...cards.values()].map(c=>({id:c.id,protocol:c.protocol,isps:[...c.isps],hours:c.hours,follow:c.follow,start:view(c)[0],end:c.end,visible:c.visible,loading:c.loading.size,cachedTargets:c.cache.size,region:c.config?.region_id}))})};
  }
  const api={create,normalize,axis,tcpLevel,tcpAxis};if(typeof module==='object'&&module.exports)module.exports=api;else root.LatencyMini=api;
})(globalThis);
