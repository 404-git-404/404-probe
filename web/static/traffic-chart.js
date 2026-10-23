/* Face B of the traffic module: the last hour of the per-minute mean rates (resources/history?hours=1) as two thin lines and
   the peak. Nothing is invented: a missing minute breaks the line, and no data says so. */
(function(root) {
  'use strict';
  const C=root.NetworkQualityCore||(typeof require==='function'?require('./network-quality-core.js'):null);
  // resource-history.js is loaded after this file on the page, so it is looked up when it is first needed.
  const history=()=>root.ResourceHistory||(typeof require==='function'?require('./resource-history.js'):null);
  const HOUR=3600000,MINUTE=60000,REFRESH_MS=55000,W=200,HEIGHT=24;
  const NS='http:'+'//www.w3.org/2000/svg'; // an XML namespace name, not a request
  function bytes(value) {
    if(value===null||value===undefined||!Number.isFinite(value))return '—';
    const units=['B/s','KB/s','MB/s','GB/s','TB/s','PB/s'];let n=value,i=0;
    while(n>=1000&&i<units.length-1){n/=1000;i++;}
    return `${n.toFixed(i?(n>=100?0:n>=10?1:2):0)} ${units[i]}`;
  }
  // Two series over one hour on one scale; a gap of more than a minute breaks the line.
  function paths(points,from,to) {
    const peak=points.reduce((m,p)=>Math.max(m,p.rx_rate,p.tx_rate),0),top=Math.max(peak,1);
    const x=t=>((t-from)/(to-from))*W,y=v=>HEIGHT-1-(v/top)*(HEIGHT-4);
    const build=key=>{let d='',prev=null;for(const p of points){d+=`${prev!==null&&p.timestamp-prev===MINUTE?'L':'M'}${x(p.timestamp).toFixed(1)},${y(p[key]).toFixed(1)}`;prev=p.timestamp;}return d;};
    return {peak,rx:build('rx_rate'),tx:build('tx_rate')};
  }
  function create({document:doc=root.document,coordinator=null,state=root.AgentState,fetcher=(...args)=>root.fetch(...args),
    unauthorized=()=>root.location.assign('/login'),Observer=root.IntersectionObserver,
    interval=root.setInterval,clear=root.clearInterval,now=()=>Date.now()}={}) {
    const queue=coordinator||C.coordinator({now}),cards=new Map();let closed=false,timer=null;
    const el=(tag,cls,text)=>{const n=doc.createElement(tag);n.className=cls;if(text!=null)n.textContent=text;return n;};
    const inactive=a=>a.revoked?'revoked':a.removal?'removing':a.disabled_at?'paused':'';
    const effective=c=>!closed&&!c.dead&&c.active&&c.visible&&!doc.hidden&&c.node.isConnected&&!inactive(c.agent);
    const geometry=c=>{const r=c.node.getBoundingClientRect();return c.node.isConnected&&r.width>0&&r.bottom>0&&r.top<(root.innerHeight||0)&&r.right>0&&r.left<(root.innerWidth||0);};
    function draw(c) {
      const lifecycle=inactive(c.agent),v=c.data;
      c.svg.replaceChildren();
      const text=lifecycle?'设备暂停/撤销':!v?c.error||(c.loading?'正在读取…':'等待数据'):!v.points.length?'近 1 小时没有分钟数据':'';
      c.status.textContent=text;c.status.hidden=!text;c.svg.parentNode.hidden=!!text;
      if(!v||lifecycle||!v.points.length){c.peak.textContent='峰值 —';c.peak.title='峰值（分钟均值）';return;}
      const {peak,rx,tx}=paths(v.points,v.to-HOUR,v.to);
      for(const [d,cls] of [[tx,'tc7-tx'],[rx,'tc7-rx']]){const p=doc.createElementNS(NS,'path');p.setAttribute('d',d);p.setAttribute('class',cls);p.setAttribute('fill','none');p.setAttribute('vector-effect','non-scaling-stroke');c.svg.append(p);}
      c.peak.textContent=`峰值 ${bytes(peak)}`;c.peak.title=`近 1 小时分钟均值的峰值 ${bytes(peak)}（不是瞬时峰值）${c.error?'；读取失败，显示上次结果':''}`;
      c.svg.parentNode.title=`近 1 小时 ↓ ${bytes(v.points.at(-1).rx_rate)} ↑ ${bytes(v.points.at(-1).tx_rate)}（最近一分钟均值） · 每点为一分钟算术均值`;
    }
    function release(c,keep=false) {c.serial++;c.loading=false;queue.release(c.owner);if(!keep){c.data=null;c.updated=0;c.error='';}}
    async function refresh(c) {
      if(!effective(c)||c.loading)return;
      if(c.data&&now()-c.updated<REFRESH_MS){draw(c);return;}
      c.loading=true;const serial=c.serial,id=c.id;
      try{
        const value=await queue.request(JSON.stringify(['traffic-hour',id]),c.owner,signal=>C.transportLease(tracked=>state.fetchJSON(
          `/api/v1/web/agents/${encodeURIComponent(id)}/resources/history?hours=1`,{signal,fetcher:tracked,unauthorized,timeoutMs:10000}),history().boundedFetcher(fetcher)));
        if(serial!==c.serial||!effective(c))return;
        c.data=history().validate(value,id,{hours:1});c.updated=now();c.error='';draw(c);
      }catch(error){if(serial===c.serial&&effective(c)&&error.name!=='AbortError'){c.updated=now();c.error='流量读取失败';draw(c);}}
      finally{if(serial===c.serial){c.loading=false;draw(c);}}
    }
    function visible(c,value) {
      if(c.visible===value)return;c.visible=value;
      if(!value)release(c,true);else refresh(c);
      draw(c);
    }
    const observer=Observer?new Observer(entries=>{for(const e of entries){const c=cards.get(e.target.dataset.trafficAgent);if(c)visible(c,e.isIntersecting);}},{rootMargin:'0px'}):null;
    function fallback() {if(!observer&&!doc.hidden)for(const c of cards.values())visible(c,geometry(c));}
    function mount(agent) {
      let c=cards.get(agent.agent_id);
      if(!c) {
        const node=el('div','tc7'),label=el('span','tc7-label','近 1h'),box=el('span','tc7-spark'),svg=doc.createElementNS(NS,'svg'),status=el('small','tc7-status'),peak=el('span','tc7-peak','峰值 —');
        svg.setAttribute('viewBox',`0 0 ${W} ${HEIGHT}`);svg.setAttribute('preserveAspectRatio','none');svg.setAttribute('aria-hidden','true');box.append(svg);
        status.setAttribute('role','status');node.dataset.trafficAgent=agent.agent_id;node.append(label,box,status,peak);
        c={id:agent.agent_id,agent,node,svg,status,peak,owner:{},active:false,visible:false,serial:0,loading:false,dead:false,data:null,updated:0,error:''};
        cards.set(c.id,c);observer?.observe(node);
      }
      c.agent=agent;
      if(inactive(agent))release(c);
      draw(c);return c.node;
    }
    function retire(c) {release(c);c.dead=true;observer?.unobserve(c.node);c.node.remove();cards.delete(c.id);}
    function sync(ids) {const live=new Set(ids);for(const c of [...cards.values()])if(!live.has(c.id))retire(c);}
    function tick() {if(closed||doc.hidden)return;fallback();queue.pump();for(const c of cards.values())refresh(c);}
    function visibility() {
      if(doc.hidden){for(const c of cards.values())release(c,true);if(timer!==null){clear(timer);timer=null;}}
      else{if(timer===null&&!closed)timer=interval(tick,30000);tick();}
    }
    doc.addEventListener('visibilitychange',visibility);
    if(!observer){root.addEventListener?.('scroll',fallback,{passive:true});root.addEventListener?.('resize',fallback,{passive:true});}
    if(!doc.hidden)timer=interval(tick,30000);
    return {mount,sync,tick,setActive(id,value){const c=cards.get(id);if(!c||c.active===value)return;c.active=value;if(!value)release(c,true);else refresh(c);},setVisible(id,value){const c=cards.get(id);if(c)visible(c,value);},
      close(){if(closed)return;closed=true;if(timer!==null)clear(timer);timer=null;observer?.disconnect();for(const c of [...cards.values()])retire(c);
        doc.removeEventListener('visibilitychange',visibility);if(!observer){root.removeEventListener?.('scroll',fallback);root.removeEventListener?.('resize',fallback);}if(!coordinator)queue.close();},
      snapshot:()=>({cards:cards.size,requests:queue.snapshot(),timer:timer!==null,observations:[...cards.values()].map(c=>({id:c.id,active:c.active,visible:c.visible,loading:c.loading,points:c.data?.points.length||0}))})};
  }
  const api={create,bytes,paths};
  if(typeof module==='object'&&module.exports)module.exports=api;else root.TrafficChart=api;
})(globalThis);
