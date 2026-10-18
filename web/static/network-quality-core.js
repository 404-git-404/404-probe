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
  // UI02: share only horizontal pixel constraints, never series or scale data.
  function plotAxes() {
    const agents=new Map();let queued=false,closed=false;
    const live=r=>r.node.isConnected&&r.node.getBoundingClientRect().width>0&&r.node.getBoundingClientRect().height>0;
    const width=(rows,r)=>Math.ceil(Math.max(...[...rows.values()].filter(live).map(p=>p.node.getBoundingClientRect().left+p.need))-r.node.getBoundingClientRect().left-1e-6);
    function schedule(){if(queued||closed)return;queued=true;(root.queueMicrotask|| (fn=>Promise.resolve().then(fn)))(()=>{queued=false;if(closed)return;for(const rows of agents.values())for(const r of [...rows.values()])if(live(r)){const next=width(rows,r);if(next!==r.applied){r.applied=next;r.redraw();}}});}
    return {
      fit(id,kind,node,need,redraw){if(closed)return need;let rows=agents.get(id);if(!rows){rows=new Map();agents.set(id,rows);}const r=rows.get(kind)||{};Object.assign(r,{node,need,redraw});rows.set(kind,r);r.applied=width(rows,r);schedule();return r.applied;},
      release(id,kind){const rows=agents.get(id);if(!rows)return;rows.delete(kind);if(!rows.size)agents.delete(id);schedule();},
      close(){closed=true;agents.clear();},
      snapshot:()=>({agents:agents.size,axes:[...agents].flatMap(([id,rows])=>[...rows].map(([kind,r])=>({id,kind,need:r.need,width:r.applied,visible:live(r)})))})
    };
  }
  function draft(config) {
    return {enabled:!!config.enabled,ipv6:!!config.ipv6,choices:slots.map(slot=>{
      const t=(config.targets||[]).find(t=>t.slot===slot&&t.family==='ipv4')||(config.targets||[]).find(t=>t.slot===slot)||{};
      return {slot,source:'catalog',region:t.region||'',protocol:t.protocol||'tcp'};
    })};
  }
  function chooseRegion(value,slot,region) {
    const out=structuredClone(value);
    for(const choice of out.choices) if(slot==='all'||choice.slot===slot) Object.assign(choice,{source:'catalog',region});
    return out;
  }
  function payload(value,revision) {
    if(typeof revision!=='string') throw Error('配置版本缺失，不能保存');
    return {expected_revision:revision,enabled:value.enabled,ipv6:value.ipv6,choices:value.choices.map(c=>({slot:c.slot,source:'catalog',protocol:c.protocol,region:c.region}))};
  }
  const historyKey = (agent,target,family,hours) => JSON.stringify(['history',agent,target,family,hours]);
  function ratio(stats,protocol) {
    const s=stats||{},icmp=protocol==='icmp',modern=finite(s.connect_attempts),denominator=icmp?s.sent:modern?s.connect_attempts:s.attempts;
    const failures=icmp?s.sent-s.received:modern?s.connect_attempts-s.connect_successes:s.failures;
    const value=icmp?s.packet_loss_percent:modern&&denominator>0?100*failures/denominator:s.failure_percent;
    return {label:icmp?'丢包率':modern?'Connect 失败率':'连接失败率',value:denominator>0&&finite(value)?value:null,
      fraction:denominator>0?`${failures} / ${denominator}`:'无实际分母'};
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
    return `${time(block.start)} — ${time(block.end)}${block.in_progress||block.provisional?' · 未最终封存':''}${block.gap?' · 缺测/未执行':''}\nP50 ${ms(block.p50_ms)} · P95 ${ms(block.p95_ms)} · Jitter ${ms(block.jitter_ms??block.delay_variation_ms)} · 配对 ${block.jitter_pairs||0}\n${r.label} ${percent(r.value)} (${r.fraction}) · 样本 ${block.count} · 完整探测 ${block.probe_attempts??block.attempts} · 未执行 ${block.not_executed} · 发送/接收 ${block.sent}/${block.received}\n${block.completeness||'unknown'} · ${block.execution_coverage||'unverified'}\n分类 ${JSON.stringify(block.categories||{})}`;
  }
  function columns(blocks,protocol,kind) {
    const ordered=[...(blocks||[])].sort((a,b)=>a.start-b.start);
    const used=new Set(), valid=ordered.filter(b=>{if(!finite(b.start)||!finite(b.end)||b.end<=b.start||used.has(b.start))return false;used.add(b.start);return true;});
    const x=valid.map(b=>b.start/1000);
    const y=key=>valid.map(b=>b.gap?null:finite(b[key])?b[key]:null);
    return {blocks:valid,data:kind==='latency'?[x,y('p50_ms'),y('p95_ms')]:[x,valid.map(b=>b.gap?null:ratio(b,protocol).value)]};
  }
  function laneDraft(config) {
    return {enabled:!!config.enabled,ipv6:!!config.ipv6,region:config.region_id||'',choices:slots.flatMap(slot=>['ipv4','ipv6'].flatMap(family=>['tcp','icmp'].map(protocol=>{
      const lane=(config.card_lanes||[]).find(t=>t.slot===slot&&t.family===family&&t.protocol===protocol);
      return lane?{slot,family,protocol,enabled:!!lane.enabled,source:'catalog',region:lane.region||config.region_id||''}:{slot,family,protocol,enabled:false,source:'catalog',region:config.region_id||''};
    })))};
  }
  function lanePayload(value,revision) {
    if(typeof revision!=='string')throw Error('配置版本缺失');
    if(!value.region&&value.enabled)throw Error('请选择三网共用地区');
    return {expected_revision:revision,enabled:value.enabled,ipv6:value.ipv6,region_id:value.region,lanes:value.choices.map(c=>({
      slot:c.slot,family:c.family,protocol:c.protocol,enabled:!!c.enabled,source:'catalog',region:value.region
    }))};
  }
  function cardGroup(h,target) {
    const metric=target?.protocol==='tcp'?'pure_connect':'echo_rtt';
    return h?.current?.find(g=>g.semantics==='staged_probe_v2'&&g.metric===metric);
  }
  // Why a card has nothing to measure, decided from the Server's card config alone. "Not selected" is
  // a configuration state (the Agent is fine); "unsupported" is only for an Agent without the capability.
  function configNotice(config) {
    if(!config)return {key:'waiting',text:'等待地区配置'};
    if(config.state==='region_selection_required')return {key:'region',text:'未选择地区',detail:'尚未选择地区；选择地区后才会开始检测'};
    if(config.supported!==true)return {key:'unsupported',text:'Agent 不支持',detail:'Agent 没有上报 staged_probe_v2 网络质量能力'};
    return null;
  }
  // One lane (family + protocol) that the Agent reports it cannot run: ICMP without CAP_NET_RAW, for example.
  // TCP keeps working; the lane says why it is unavailable instead of showing "unknown".
  function laneNotice(config,family,protocol) {
    const lane=config?.capabilities?.lanes?.find(l=>l.family===family&&l.protocol===protocol);
    if(!lane||lane.supported!==false)return null;
    if(lane.reason==='platform_excluded')return {key:'lane',text:'不可用',detail:'不可用（当前平台不支持网络质量检测）'};
    if(protocol==='icmp')return {key:'lane',text:'不可用',detail:'不可用（当前环境无 ICMP 权限）'};
    return {key:'lane',text:'不可用',detail:`不可用（当前环境不支持 ${String(protocol).toUpperCase()}）`};
  }
  function cardCurrent(h,target,{old=false,revision,elapsed=0}={}) {
    const g=cardGroup(h,target),s=g?.latest_probe,d=s?.details;
    if(target?.status==='not_scheduled_legacy'||target?.status==='unsupported')return {text:'不支持',detail:`目标状态 ${target.status}；Agent 不支持 staged_probe_v2；不使用旧 DNS-inclusive 语义`};
    if(target?.status==='paused')return {text:'暂停',detail:'目标状态 paused；已完成历史独立保留'};
    if(!target?.id||target.status==='unavailable')return {text:'未知',detail:'没有对应地址族/协议的可用目标'};
    if(target.status!=='active')return {text:'未知',detail:`目标状态 ${target.status||'unknown'}；没有可信当前检测状态`};
    if(!s||!d)return {text:'未知',detail:'没有 staged_probe_v2 的可信当前样本；不使用旧 DNS-inclusive 语义'};
    const age=(h.now-s.started_at)+Math.max(0,elapsed),stale=old||g.stale||age>g.freshness_budget_ms;
    const detail=`${time(s.started_at)} · ${s.outcome} · 配置 ${s.config_revision} / 槽位 ${s.slot_revision}`;
    if(stale)return {text:'过期',detail:`当前样本过期；${detail}`};
    if(s.target_id!==target.id||s.slot_revision!==target.slot_revision||revision&&s.config_revision!==revision)return {text:'未知',detail:`当前配置不匹配；${detail}`};
    if(target.protocol==='tcp') {
      if(s.outcome==='success'&&d.probe_complete&&d.connect_complete&&finite(d.connect_ms))return {text:`${d.connect_ms.toFixed(d.connect_ms>=1000?0:1)}ms`,detail:`最新纯 Connect RTT（不含 DNS）${d.connect_ms.toFixed(3)}ms；${detail}`};
      return {text:d.probe_complete?'失败':'未知',detail:`当前探测 ${s.outcome}，不显示较早成功值；${detail}`};
    }
    if(d.window_complete&&d.requested===3&&s.sent===3&&Number.isInteger(s.received)&&s.received>=0&&s.received<=3) {
      const loss=100*(s.sent-s.received)/s.sent;
      return {text:`${loss===0||loss===100?loss:loss.toFixed(1)}%`,detail:`最新完整三 Echo：发送 ${s.sent} / 接收 ${s.received} · 丢包 ${percent(loss)}；${detail}`};
    }
    return {text:'未知',detail:`无可信完整三 Echo 分母：${s.outcome}；${detail}`};
  }
  function cardPoints(h,target) {
    const metric=target?.protocol==='tcp'?'pure_connect':'echo_rtt';
    const points=h?.groups?.find(g=>g.semantics==='staged_probe_v2'&&g.metric===metric)?.points||[];
    if(!finite(h?.now)||h.resolution_ms!==300000)return [];
    const first=Math.floor(h.now/300000)*300000-11*300000;
    return Array.from({length:12},(_,i)=>points.find(p=>p.start===first+i*300000&&p.end>p.start)||{start:first+i*300000,end:Math.min(first+(i+1)*300000,h.now),count:0,completeness:'no_data',categories:{}});
  }
  function cardBlock(block,target) {
    const known=block.execution_coverage==='known_gap',mixed=block.count>0&&(block.config_revision_min!==block.config_revision_max||block.slot_revision_min!==block.slot_revision_max||block.slot_revision_min!==target.slot_revision);
    const valid=block.count>0&&!mixed&&!known;
    const loss=block.sent>0&&Number.isInteger(block.sent)&&Number.isInteger(block.received)&&block.received>=0&&block.received<=block.sent?100*(block.sent-block.received)/block.sent:null;
    const v=target.protocol==='tcp'?block.p95_ms:loss;
    const tone=!valid||!finite(v)?'neutral':target.protocol==='tcp'?(v<150?'low':v<=250?'mid':'high'):v===0?'low':'loss';
    const reason=mixed?'配置/槽位边界不混合':known?'known_gap（缺测/限流，不算失败）':!block.count?'unknown（无观测）':block.completeness==='partial'?'partial（仅已观测部分）':block.completeness;
    const text=`${time(block.start)} — ${time(block.end)}${block.end<block.start+300000?' · 当前未完成桶':''}\n${target.protocol.toUpperCase()} · ${target.family} · target ${target.id||'无'}\n${target.protocol==='tcp'?`纯 Connect P95 ${ms(block.p95_ms)}（量化误差≤0.125ms）；有效 RTT ${block.valid_rtts||0}`:`丢包 ${percent(loss)}；发送 ${block.sent||0} / 接收 ${block.received||0}`}\n样本 ${block.count||0} · ${reason||'未知'} · 未执行 ${block.not_executed||0}\n配置 ${block.config_revision_min||'无'}–${block.config_revision_max||'无'} / 槽位 ${block.slot_revision_min||'无'}–${block.slot_revision_max||'无'}\n分类 ${JSON.stringify(block.categories||{})}`;
    return {tone,text,mark:tone==='neutral'?'·':tone==='loss'?'!':block.completeness==='partial'?'·':''};
  }
  const api={slots,names,finite,ms,time,percent,coordinator,transportLease,plotAxes,draft,chooseRegion,payload,historyKey,ratio,current,blockText,columns,laneDraft,lanePayload,cardGroup,configNotice,laneNotice,cardCurrent,cardPoints,cardBlock};
  if(typeof module==='object'&&module.exports)module.exports=api;else root.NetworkQualityCore=api;
})(globalThis);
