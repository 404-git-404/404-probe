/* Bounded snapshots of accepted Server observations; no SSE samples or persistence. */
(function(root) {
  'use strict';
  const C=root.NetworkQualityCore||(typeof require==='function'?require('./network-quality-core.js'):null);
  const WINDOW=1800000,LIMIT=181;
  const reasons={first_observation:'首次观测，速率未知',continuity_reset:'计数或身份重置，速率未知',gap:'上报间隔缺测',invalid_rate:'无有效速率',clock_reset:'接收时钟重置'};
  const statuses={online:'真实接收观测',offline:'离线 · 保留的历史观测',no_data:'等待真实观测',paused:'设备已暂停',revoked:'设备已撤销',removing:'设备移除中'};
  const safe=value=>Number.isSafeInteger(value)&&value>=0&&value<=8640000000000000;
  const opaque=value=>typeof value==='string'&&value.length>0&&value.length<=128;
  const rate=value=>value===null||(typeof value==='number'&&Number.isFinite(value)&&value>=0);
  function validate(value,id) {
    const fail=()=>{throw Error('流量快照格式无效，未采用');};
    if(!value||value.agent_id!==id||!opaque(value.generation)||!safe(value.server_now)||value.window_ms!==WINDOW||value.point_limit!==LIMIT||
      value.nominal_interval_ms!==10000||value.source!=='accepted_server_observations'||value.retention!=='memory_only'||value.coverage!=='partial'||
      !Object.hasOwn(statuses,value.status)||typeof value.truncated!=='boolean'||!Array.isArray(value.points)||value.points.length>LIMIT)fail();
    const expected={online:'',no_data:'no_retained_observations',paused:'agent_paused',revoked:'agent_revoked',removing:'agent_removing'};
    if(value.status==='offline'?!['last_known_observations','no_retained_observations'].includes(value.reason):value.reason!==expected[value.status])fail();
    if((['no_data','paused','revoked','removing'].includes(value.status)&&value.points.length)||(value.status==='online'&&!value.points.length))fail();
    let previous=-1;const orders=new Set();
    const points=value.points.map(p=>{
      if(!p||!safe(p.received_at)||!safe(p.collected_at)||p.received_at<=previous||p.received_at<value.server_now-WINDOW||p.received_at>value.server_now||
        !opaque(p.order)||orders.has(p.order)||!rate(p.rx_rate)||!rate(p.tx_rate)||typeof p.break_before!=='boolean')fail();
      const missing=p.rx_rate===null||p.tx_rate===null;
      if(missing?(p.rx_rate!==null||p.tx_rate!==null||!p.break_before||!Object.hasOwn(reasons,p.reason)):(p.break_before||p.reason!==''))fail();
      if(previous>=0&&p.received_at-previous>30000&&!p.break_before)fail();
      previous=p.received_at;orders.add(p.order);
      return {received_at:p.received_at,collected_at:p.collected_at,order:p.order,rx_rate:p.rx_rate,tx_rate:p.tx_rate,break_before:p.break_before,reason:p.reason};
    });
    if(value.oldest_received_at!==(points[0]?.received_at??null)||value.last_received_at!==(points.at(-1)?.received_at??null))fail();
    if(value.status==='offline'&&value.reason!==(points.length?'last_known_observations':'no_retained_observations'))fail();
    return {agent_id:id,generation:value.generation,server_now:value.server_now,window_ms:WINDOW,point_limit:LIMIT,nominal_interval_ms:10000,
      source:value.source,retention:value.retention,status:value.status,reason:value.reason,coverage:value.coverage,truncated:value.truncated,
      oldest_received_at:value.oldest_received_at,last_received_at:value.last_received_at,points};
  }
  function bytes(value) {
    if(value===null)return '—';
    const units=['B/s','KB/s','MB/s','GB/s','TB/s','PB/s'];let n=value,i=0;
    while(n>=1000&&i<units.length-1){n/=1000;i++;}
    return `${n.toFixed(i?2:0)} ${units[i]}`;
  }
  const time=value=>new Date(value).toLocaleString();
  const detail=p=>p?`接收 ${time(p.received_at)} · 序号 ${p.order} · ↓ ${bytes(p.rx_rate)} · ↑ ${bytes(p.tx_rate)}${p.reason?' · '+reasons[p.reason]:''}`:'';
  const columns=v=>[v.points.map(p=>p.received_at/1000),v.points.map(p=>p.rx_rate),v.points.map(p=>p.tx_rate)];
  function create({document:doc=root.document,coordinator=null,state=root.AgentState,fetcher=(...args)=>root.fetch(...args),
    unauthorized=()=>root.location.assign('/login'),Plot=root.uPlot,Observer=root.IntersectionObserver,Resize=root.ResizeObserver,
    interval=root.setInterval,clear=root.clearInterval,now=()=>Date.now(),timeoutMs=10000,openHistory=null}={}) {
    const queue=coordinator||C.coordinator({now}),cards=new Map();let closed=false,timer=null;
    const el=(tag,cls,text)=>{const n=doc.createElement(tag);n.className=cls;if(text!=null)n.textContent=text;return n;};
    const inactive=a=>a.revoked?'revoked':a.removal?'removing':a.disabled_at?'paused':'';
    const effective=c=>!closed&&!c.dead&&c.visible&&!doc.hidden&&c.node.isConnected&&!inactive(c.agent);
    const geometry=c=>{const r=c.node.getBoundingClientRect();return c.node.isConnected&&r.width>0&&r.bottom>0&&r.top<(root.innerHeight||0)&&r.right>0&&r.left<(root.innerWidth||0);};
    function destroyPlot(c){c.resize?.disconnect();c.resize=null;if(c.tap)c.plot?.over?.removeEventListener('pointerdown',c.tap);c.tap=null;c.plot?.destroy();c.plot=null;c.chart.replaceChildren();}
    function release(c){c.serial++;c.loading=false;queue.release(c.owner);destroyPlot(c);c.data=null;c.error='';c.index=0;c.output.textContent='';}
    function summary(c){
      const lifecycle=inactive(c.agent),v=c.data;
      if(lifecycle){c.status.textContent=statuses[lifecycle]+' · 当前数据不可用';return;}
      if(!v){c.status.textContent=c.error||(!c.visible?'进入视口后读取真实观测':c.loading?'正在读取…':'等待真实观测');return;}
      c.status.textContent=`${c.error?'读取失败 · 保留上次可信历史（非当前） · ':''}${!c.agent.online&&v.points.length?statuses.offline:statuses[v.status]}${v.truncated?' · 数量上限截断':''}${v.points.length?` · 实际覆盖 ${time(v.oldest_received_at)} — ${time(v.last_received_at)}`:''}`;
    }
    function select(c,index){
      c.index=Math.max(0,Math.min((c.data?.points.length||1)-1,index));
      c.output.textContent=detail(c.data?.points[c.index]);
    }
    function renderPlot(c){
      if(!effective(c)||!c.data)return;
      const rect=c.chart.getBoundingClientRect(),width=Math.floor(rect.width);
      if(width<=0)return;
      const height=(root.innerWidth||rect.width)>620?180:150,v=c.data;
      const measured=v.points.filter(p=>p.rx_rate!==null||p.tx_rate!==null).length;
      if(!measured||!Plot){destroyPlot(c);c.chart.append(el('p','traffic-empty',!Plot?'本地图表库未加载':'暂无有效速率 · 不补点'));return;}
      // A singleton is an honest marker, never a fabricated line segment.
      const data=columns(v);
      const yrange=(_u,min,max)=>[0,Math.max(1,Number.isFinite(max)?max*1.1:1)];
      if(c.plot){c.plot.setData(data);c.plot.setSize({width,height});return;}
      c.chart.replaceChildren();
      c.plot=new Plot({width,height,legend:{show:false},cursor:{drag:{x:false,y:false}},
        scales:{x:{time:true,range:()=>[(c.data.server_now-WINDOW)/1000,c.data.server_now/1000]},y:{range:yrange}},
        axes:[{stroke:'#69727a',size:32,font:'10px system-ui',grid:{stroke:'#e7e1d7'}},{stroke:'#69727a',size:75,font:'10px system-ui',grid:{stroke:'#e7e1d7'},values:(_u,ticks)=>ticks.map(v=>bytes(v).replace('.00 ', ' '))}],
        series:[{},...['↓ 下载','↑ 上传'].map((label,i)=>({label,stroke:i?'#9270AD':'#168577',width:1.5,spanGaps:false,points:{show:true,size:3}}))],
        hooks:{setCursor:[u=>{if(effective(c)&&c.data)select(c,u.cursor.idx??c.index);}]}
      },data,c.chart);
      // uPlot's pinned mouse cursor remains unchanged. Touch/pen has a bounded
      // explicit point selection, without a gesture framework or fake samples.
      c.tap=e=>{if(!effective(c)||!['touch','pen'].includes(e.pointerType)||e.isPrimary===false)return;
        const rect=c.plot.over.getBoundingClientRect();if(rect.width<=0||e.clientX<rect.left||e.clientX>rect.right)return;
        const index=c.plot.posToIdx(e.clientX-rect.left);if(Number.isInteger(index))select(c,index);};
      c.plot.over?.addEventListener('pointerdown',c.tap);
      if(Resize){c.resize=new Resize(()=>{if(c.plot&&effective(c))renderPlot(c);});c.resize.observe(c.chart);}
    }
    async function refresh(c){
      if(!effective(c)||c.loading)return;
      c.loading=true;const serial=c.serial;summary(c);
      try{
        const value=await queue.request(JSON.stringify(['traffic',c.id]),c.owner,signal=>C.transportLease(tracked=>state.fetchJSON(
          `/api/v1/web/agents/${encodeURIComponent(c.id)}/traffic/recent`,{signal,fetcher:tracked,unauthorized,timeoutMs}),fetcher));
        if(serial!==c.serial||!effective(c))return;
        const trusted=validate(value,c.id);
        // Every successful read replaces the whole bounded generation, never merges.
        c.data=trusted;c.error='';c.index=Math.max(0,trusted.points.length-1);renderPlot(c);select(c,c.index);summary(c);
      }catch(error){if(serial===c.serial&&effective(c)&&error.name!=='AbortError'){c.error='流量读取失败';summary(c);}}
      finally{if(serial===c.serial){c.loading=false;summary(c);}}
    }
    function visible(c,value){
      if(c.visible===value)return;c.visible=value;
      if(!value)release(c);else refresh(c);summary(c);
    }
    const observer=Observer?new Observer(entries=>{for(const e of entries){const c=cards.get(e.target.dataset.trafficAgent);if(c)visible(c,e.isIntersecting);}},{rootMargin:'0px'}):null;
    function fallback(){if(!observer&&!doc.hidden)for(const c of cards.values())visible(c,geometry(c));}
    function mount(agent){
      let c=cards.get(agent.agent_id);
      if(!c){
        const node=el('section','traffic-chart'),chart=el('div','traffic-plot'),head=el('div','traffic-head'),status=el('small','traffic-status'),output=el('output','traffic-detail');
        node.dataset.trafficAgent=agent.agent_id;head.append(el('span','traffic-title','流量 · 最多最近 30 分钟'),el('span','traffic-legend','↓ 下载 / ↑ 上传'));
        head.title='Server 内存中的已接受接收观测；名义上报间隔 10 秒（实际可配置），可见时每 30 秒读取快照。接收时间为横轴，缺测断线；实际保留覆盖可能更短。速率为十进制 B/s。重启后等待新观测。';
        output.tabIndex=0;output.dataset.focusKey='traffic-point';output.setAttribute('aria-label','流量点详情；左右方向键浏览真实观测');
        node.append(head,chart,status,output);
        c={id:agent.agent_id,agent,node,chart,status,output,owner:{},visible:false,serial:0,loading:false,dead:false,data:null,error:'',index:0,plot:null,resize:null,tap:null};
        c.key=e=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();if(!c.data?.points.length)return;
          if(e.key==='ArrowLeft'||e.key==='ArrowRight')select(c,c.index+(e.key==='ArrowLeft'?-1:1));else select(c,e.key==='Home'?0:c.data.points.length-1);};
        output.addEventListener('keydown',c.key);cards.set(c.id,c);observer?.observe(node);
        if(openHistory){const historyButton=el('button','traffic-history','分钟历史');historyButton.type='button';historyButton.dataset.focusKey='traffic-history';historyButton.addEventListener('click',()=>openHistory(c.id,historyButton));head.append(historyButton);
          chart.addEventListener('click',()=>openHistory(c.id,historyButton));}
      }
      const before=inactive(c.agent);c.agent=agent;const after=inactive(agent);
      if(after&&after!==before)release(c);
      // Resource/SSE remounts do not schedule reads; visible return/timer own them.
      summary(c);return c.node;
    }
    function retire(c){release(c);c.dead=true;observer?.unobserve(c.node);c.output.removeEventListener('keydown',c.key);c.node.remove();cards.delete(c.id);}
    function sync(ids){const live=new Set(ids);for(const c of [...cards.values()])if(!live.has(c.id))retire(c);}
    function tick(){if(closed||doc.hidden)return;fallback();queue.pump();for(const c of cards.values())refresh(c);}
    function visibility(){
      if(doc.hidden){for(const c of cards.values()){release(c);summary(c);}if(timer!==null){clear(timer);timer=null;}}
      else{if(timer===null&&!closed)timer=interval(tick,30000);tick();}
    }
    doc.addEventListener('visibilitychange',visibility);
    if(!observer){root.addEventListener?.('scroll',fallback,{passive:true});root.addEventListener?.('resize',fallback,{passive:true});}
    if(!doc.hidden)timer=interval(tick,30000);
    return {mount,sync,tick,setVisible(id,value){const c=cards.get(id);if(c)visible(c,value);},
      close(){if(closed)return;closed=true;if(timer!==null)clear(timer);timer=null;observer?.disconnect();for(const c of [...cards.values()])retire(c);
        doc.removeEventListener('visibilitychange',visibility);if(!observer){root.removeEventListener?.('scroll',fallback);root.removeEventListener?.('resize',fallback);}if(!coordinator)queue.close();},
      snapshot:()=>({cards:cards.size,requests:queue.snapshot(),timer:timer!==null,observations:[...cards.values()].map(c=>({id:c.id,visible:c.visible,loading:c.loading,points:c.data?.points.length||0,generation:c.data?.generation||null,plot:!!c.plot}))})};
  }
  const api={create,validate,columns,bytes,detail};
  if(typeof module==='object'&&module.exports)module.exports=api;else root.TrafficChart=api;
})(globalThis);
