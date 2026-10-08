/* Network quality: bounded request ownership and adapters for the existing Server DTO. */
(function(root) {
  'use strict';
  const slots = ['telecom','unicom','mobile'];
  const names = {telecom:'电信',unicom:'联通',mobile:'移动'};
  const finite = value => typeof value === 'number' && Number.isFinite(value);
  const ms = value => finite(value) ? `${value.toFixed(1)} ms` : '—';
  const time = value => finite(value) ? new Date(value).toLocaleString() : '未知时间';
  const aborted = () => Object.assign(new Error('质量请求已取消'), {name:'AbortError'});
  function coordinator({limit=4, now=()=>Date.now()}={}) {
    const jobs = new Map(); let active=0, closed=false;
    function pump() {
      if(closed) return;
      for(const [key,job] of jobs) {
        if(active>=limit) break;
        if(job.flight || !job.pending || !job.owners.size || now()<job.retryAt) continue;
        const intent=job.pending; job.pending=null;
        const controller=new AbortController(); job.flight={controller,intent}; active++;
        let operation;
        try {operation=intent.work(controller.signal);}catch(error){operation=Promise.reject(error);}
        const result=operation?.result||Promise.resolve(operation);
        const settled=operation?.settled||result;
        const delivered=Promise.resolve(result).then(value=>{
          if(controller.signal.aborted) throw aborted();
          job.failures=0;job.retryAt=0;intent.resolve(value);
        }).catch(error=>{
          if(error.name!=='AbortError') {job.failures=Math.min(4,job.failures+1);job.retryAt=now()+Math.min(300000,30000*2**(job.failures-1));}
          intent.reject(error);
        });
        Promise.allSettled([delivered,settled]).finally(()=>{
          active--;job.flight=null;
          if(!job.owners.size && !job.pending) jobs.delete(key);
          pump();
        });
      }
    }
    function request(key,owner,work) {
      if(closed) return Promise.reject(aborted());
      const job=jobs.get(key)||{owners:new Set(),pending:null,flight:null,retryAt:0,failures:0};
      jobs.set(key,job);job.owners.add(owner);
      // Busy notifications share the current immutable operation, never stack timers.
      if(job.flight && !job.flight.controller.signal.aborted) return job.flight.intent.promise;
      if(job.pending) return job.pending.promise;
      if(now()<job.retryAt) return Promise.reject(Object.assign(new Error('读取失败，等待退避重试'),{name:'BackoffError'}));
      let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no;});
      job.pending={work,resolve,reject,promise};pump();return promise;
    }
    function release(owner,keep=new Set()) {
      for(const [key,job] of jobs) {
        if(keep.has(key)) continue;
        job.owners.delete(owner);
        if(!job.owners.size) {
          if(job.pending){job.pending.reject(aborted());job.pending=null;}
          job.flight?.controller.abort();
          // Cancelled-but-unsettled work still occupies its physical slot.
          if(!job.flight) jobs.delete(key);
        }
      }
      pump();
    }
    return {request,release,pump,close(){closed=true;for(const job of jobs.values()){job.pending?.reject(aborted());job.flight?.controller.abort();}jobs.clear();},
      snapshot:()=>({active,keys:jobs.size,queued:[...jobs.values()].filter(j=>j.pending).length})};
  }
  // Reuse the established JSON/CSRF helpers while retaining the actual fetch/body
  // lease. Their Promise.race timeout is a UI deadline, not proof of transport exit.
  function transportLease(run,fetcher) {
    let releaseOuter,physical=Promise.resolve(),body=null;
    const outerDone=new Promise(resolve=>{releaseOuter=resolve;});
    const tracked=(...args)=>{
      const transport=Promise.resolve().then(()=>fetcher(...args));
      physical=transport.then(async response=>{
        await outerDone;
        if(body)await body.catch(()=>{});
        else await response.body?.cancel?.();
      }).catch(()=>{});
      return transport.then(response=>({ok:response.ok,status:response.status,statusText:response.statusText,
        json(){body=Promise.resolve().then(()=>response.json());return body;}}));
    };
    let result;try{result=Promise.resolve(run(tracked));}catch(error){result=Promise.reject(error);}
    const settled=result.then(releaseOuter,releaseOuter).then(()=>physical);
    return {result,settled};
  }
  function draft(config) {
    return {enabled:!!config.enabled,ipv6:!!config.ipv6,choices:slots.map(slot=>{
      const t=(config.targets||[]).find(t=>t.slot===slot&&t.family==='ipv4')||(config.targets||[]).find(t=>t.slot===slot)||{};
      return {slot,source:t.source||'catalog',region:t.region||'',protocol:t.protocol||'tcp',host:t.source==='manual'?t.host||'':'',port:t.source==='manual'?t.port||0:0};
    })};
  }
  function chooseRegion(value,slot,region) {
    const out=structuredClone(value);
    for(const choice of out.choices) if(slot==='all'||choice.slot===slot) Object.assign(choice,{source:'catalog',region,host:'',port:0});
    return out;
  }
  function payload(value,revision) {
    if(typeof revision!=='string') throw Error('配置版本缺失，不能保存');
    return {expected_revision:revision,enabled:value.enabled,ipv6:value.ipv6,choices:value.choices.map(c=>
      c.source==='manual'?{slot:c.slot,source:c.source,protocol:c.protocol,host:c.host.trim(),port:c.protocol==='icmp'?0:c.port}:
        {slot:c.slot,source:'catalog',protocol:c.protocol,region:c.region})};
  }
  const historyKey = (agent,target,family,hours) => JSON.stringify(['history',agent,target,family,hours]);
  function ratio(stats,protocol) {
    const s=stats||{},icmp=protocol==='icmp',denominator=icmp?s.sent:s.attempts;
    const value=icmp?s.packet_loss_percent:s.failure_percent;
    return {label:icmp?'丢包率':'连接失败率',value:denominator>0&&finite(value)?value:null,
      fraction:denominator>0?`${icmp?s.sent-s.received:s.failures} / ${denominator}`:'无实际分母'};
  }
  const percent = value => finite(value)?`${value.toFixed(1)}%`:'—';
  function current(history,target) {
    if(!target?.id||target.status==='unavailable')return {text:'暂无节点',detail:'未配置可执行的当前地址族端点',neutral:true};
    if(target.status!=='active')return {text:'已暂停',detail:'历史不代表当前检测',neutral:true};
    if(!history)return {text:'—',detail:'尚无可信质量结果',neutral:true};
    const success=history.latest_success,failure=history.latest_failure;
    const last=success?`上次成功 ${ms(success.latency_ms)} · ${time(success.started_at)}`:'尚无成功样本';
    if(failure&&(!success||failure.started_at>=success.started_at))return {text:'最近失败',detail:`${failure.outcome} · ${time(failure.started_at)}；${last}${history.stale?'；旧数据':''}`,neutral:true};
    if(history.stale)return {text:'旧数据',detail:last,neutral:true};
    return {text:ms(success?.latency_ms),detail:last,neutral:!finite(success?.latency_ms)};
  }
  function blockText(block,protocol) {
    const r=ratio(block,protocol);
    return `${time(block.start)} — ${time(block.end)}${block.in_progress?' · 进行中':''}${block.gap?' · 缺测/未执行':''}\nP50 ${ms(block.p50_ms)} · P95 ${ms(block.p95_ms)} · 延迟波动 ${ms(block.delay_variation_ms)}\n${r.label} ${percent(r.value)} (${r.fraction}) · 样本 ${block.count} · 尝试 ${block.attempts} · 未执行 ${block.not_executed} · 发送/接收 ${block.sent}/${block.received}\n分类 ${JSON.stringify(block.categories||{})}`;
  }
  function columns(blocks,protocol,kind) {
    const ordered=[...(blocks||[])].sort((a,b)=>a.start-b.start);
    const used=new Set(), valid=ordered.filter(b=>{if(!finite(b.start)||!finite(b.end)||b.end<=b.start||used.has(b.start))return false;used.add(b.start);return true;});
    const x=valid.map(b=>b.start/1000);
    const y=key=>valid.map(b=>b.gap?null:finite(b[key])?b[key]:null);
    return {blocks:valid,data:kind==='latency'?[x,y('p50_ms'),y('p95_ms')]:[x,valid.map(b=>b.gap?null:ratio(b,protocol).value)]};
  }
  const api={slots,names,finite,ms,time,percent,coordinator,transportLease,draft,chooseRegion,payload,historyKey,ratio,current,blockText,columns};
  if(typeof module==='object'&&module.exports)module.exports=api;else root.NetworkQualityCore=api;
})(globalThis);
