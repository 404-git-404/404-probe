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
  // ---- R07: user-entered targets (the v4 model) ----------------------------------------------
  const provinces=['河北','山西','辽宁','吉林','黑龙江','江苏','浙江','安徽','福建','江西','山东','河南','湖北','湖南','广东','海南','四川','贵州','云南','陕西','甘肃','青海','内蒙古','广西','西藏','宁夏','新疆','北京','天津','上海','重庆'];
  const isps=slots.map(slot=>[slot,names[slot]]);
  const intervals=Array.from({length:10},(_,i)=>(i+1)*30000);
  const THRESHOLD_MIN=50,THRESHOLD_MAX=3000,THRESHOLD_DEFAULT=500;
  const subItems=[['ipv4_tcp','IPv4 TCP','ipv4','tcp'],['ipv4_icmp','IPv4 ICMP','ipv4','icmp'],['ipv6_tcp','IPv6 TCP','ipv6','tcp'],['ipv6_icmp','IPv6 ICMP','ipv6','icmp']];
  const laneStatuses=['device_inactive','agent_unavailable','excluded','storage_pressure','disabled','unset','inactive','no_ipv6','icmp_unsupported','target_abnormal','active'];
  const isDecimal=v=>typeof v==='string'&&/^(0|[1-9][0-9]*)$/.test(v);
  const bad=what=>{throw Error('v4 配置契约不匹配：'+what);};
  // The Server answers with settings_revision (optimistic lock), config_revision (what samples carry),
  // slots[3] and lanes[12]. Anything else is not adopted.
  function adapt(v4) {
    if(!v4||typeof v4!=='object')bad('响应');
    if(!isDecimal(v4.settings_revision)||!isDecimal(v4.config_revision))bad('版本号');
    if(!Array.isArray(v4.slots)||v4.slots.length!==3||!Array.isArray(v4.lanes)||v4.lanes.length!==12)bad('卡槽/通道数量');
    if(!finite(v4.interval_ms)||!finite(v4.effective_interval_ms)||typeof v4.enabled!=='boolean'||typeof v4.state!=='string'||typeof v4.agent_support!=='string')bad('字段类型');
    const bySlot=new Map();
    v4.slots.forEach((s,i)=>{
      if(s.slot!==slots[i]||!['builtin','custom'].includes(s.kind)||typeof s.region!=='string'||typeof s.isp!=='string'||typeof s.host!=='string'||!Number.isInteger(s.port)||typeof s.slot_enabled!=='boolean'||subItems.some(([k])=>typeof s[k]!=='boolean'))bad('卡槽 '+(i+1));
      bySlot.set(s.slot,s);
    });
    // What the Agent of this session can do with ICMP, per family, as the Server says it. Missing or unrecognised means unknown, never unsupported.
    const level=v=>v==='supported'||v==='unsupported'?v:'unknown',icmpSupport={ipv4:level(v4.icmp_support?.ipv4),ipv6:level(v4.icmp_support?.ipv6)};
    const targets=[],lanes=new Map();
    for(const l of v4.lanes) {
      if(!bySlot.has(l.slot)||!['ipv4','ipv6'].includes(l.family)||!['tcp','icmp'].includes(l.protocol)||!laneStatuses.includes(l.status)||!isDecimal(l.slot_revision)||(l.target_id!==null&&!/^[0-9a-f]{32}$/.test(l.target_id)))bad('通道');
      const key=l.slot+'/'+l.family+'/'+l.protocol;if(lanes.has(key))bad('通道重复');lanes.set(key,l);
      if(l.target_id===null)continue;
      const s=bySlot.get(l.slot),running=l.status==='active'||l.status==='target_abnormal';
      // The page keeps working with the target shape it always had; labels come from the slot, never from the target.
      targets.push({id:l.target_id,slot:l.slot,family:l.family,protocol:l.protocol,slot_revision:l.slot_revision,status:running?'active':'paused',
        lane_status:l.status,condition:l.condition||'',source:'user',host:s.host,port:l.protocol==='icmp'?0:s.port});
    }
    // The Server always sends the threshold (schema 35); an answer without it is read as the default.
    const threshold=Number.isInteger(v4.slow_threshold_ms)&&v4.slow_threshold_ms>=THRESHOLD_MIN&&v4.slow_threshold_ms<=THRESHOLD_MAX?v4.slow_threshold_ms:THRESHOLD_DEFAULT;
    return {version:4,revision:v4.config_revision,settings_revision:v4.settings_revision,enabled:v4.enabled,interval_ms:v4.interval_ms,effective_interval_ms:v4.effective_interval_ms,slow_threshold_ms:threshold,
      state:v4.state,agent_support:v4.agent_support,icmpSupport,supported:v4.agent_support==='user_targets',slots:v4.slots.map(s=>({...s})),lanes,targets};
  }
  const slotNumber=slot=>slots.indexOf(slot)+1;
  const ispText=s=>s?.kind==='builtin'?(names[s.isp]||s.isp):s?.isp;
  // Row label of a slot: "ISP · region" exactly as the user typed or picked it; "卡槽 N（未标注）" when empty.
  function slotLabel(config,slot) {
    const s=config?.slots?.find(x=>x.slot===slot);
    const text=[ispText(s),s?.region].filter(Boolean).join(' · ');
    return text||`卡槽 ${slotNumber(slot)}（未标注）`;
  }
  function slotShort(config,slot) {
    const s=config?.slots?.find(x=>x.slot===slot),text=ispText(s);
    return text?Array.from(text)[0]:String(slotNumber(slot));
  }
  const conditionText={not_found:'域名不存在或没有该地址族的记录',no_address:'没有该地址族的解析结果',unsafe_address:'解析到非公网地址，已拒绝连接'};
  // What a lane shows when it is not measuring: decided by the Server's status, never inferred by the page.
  function statusNotice(status,condition) {
    switch(status) {
      case 'active':return null;
      case 'target_abnormal':return {key:'abnormal',text:'目标异常',detail:`目标异常（从本机看到）：${conditionText[condition]||condition||'地址不可用'}；不计入失败率`,measured:true};
      case 'device_inactive':return {key:'device',text:'设备已撤销/暂停',detail:'设备已撤销或暂停，当前数据不可用'};
      case 'excluded':return {key:'excluded',text:'平台不支持',detail:'当前平台不支持网络质量检测'};
      case 'storage_pressure':return {key:'pressure',text:'存储压力，已暂停',detail:'Server 存储压力下暂停了检测'};
      case 'agent_unavailable':
        if(condition==='upgrade_required')return {key:'upgrade',text:'需升级 Agent',detail:'需升级 Agent 才能使用自定义目标'};
        if(condition==='negotiating')return {key:'negotiating',text:'正在连接…',detail:'Agent 刚重启，正在完成协商，约一个上报周期'};
        return {key:'unsupported',text:'Agent 不支持',detail:'Agent 没有上报网络质量能力'};
      case 'disabled':return {key:'off',text:'已关闭',detail:'检测总开关已关闭'};
      case 'unset':return {key:'unset',text:'未配置目标',detail:'这个卡槽还没有填写目标地址'};
      case 'inactive':return {key:'inactive',text:'未启用',detail:'这个卡槽或子项没有勾选'};
      case 'no_ipv6':return {key:'no_ipv6',text:'本机无 IPv6',detail:'这台机器没有可用的 IPv6 地址；有地址后会自动开始'};
      case 'icmp_unsupported':return {key:'no_icmp',text:'不支持 ICMP',detail:'当前环境没有 ICMP 权限（例如容器内没有 CAP_NET_RAW）；TCP 不受影响'};
      default:return {key:'unknown',text:'未知',detail:status};
    }
  }
  function laneNotice(config,slot,family,protocol) {
    const l=config?.lanes?.get(`${slot}/${family}/${protocol}`);
    if(!l)return {key:'waiting',text:'等待配置',detail:'配置尚未读取'};
    return statusNotice(l.status,l.condition);
  }
  // One sentence for the whole card, or '' when there is nothing to say.
  function cardNotice(config) {
    if(!config)return '配置尚未读取';
    switch(config.state) {
      case 'agent_upgrade_required':return '需升级 Agent 才能使用自定义目标';
      case 'negotiating':return '正在连接…';
      case 'unsupported':return 'Agent 尚未支持网络质量';
      case 'excluded':return '当前平台不支持网络质量检测';
      case 'storage_pressure':return 'Server 存储压力，检测已暂停';
      case 'disabled':return '已关闭';
      case 'unset':return '未配置目标';
      case 'device_inactive':return '设备已撤销/暂停，当前数据不可用';
      case 'capacity_throttled':return `Server 容量限制，实际间隔 ${config.effective_interval_ms/1000} 秒`;
      case 'retention_degraded':return 'Server 容量紧张，历史保留已降级';
      default:return '';
    }
  }
  // ---- the settings dialog ----
  const slotDraft=s=>({slot:s.slot,kind:s.kind,region:s.region,isp:s.isp,host:s.host,port:s.port?String(s.port):'',slot_enabled:s.slot_enabled,...Object.fromEntries(subItems.map(([k])=>[k,s[k]]))});
  const draftOf=config=>({interval_ms:config.interval_ms,slow_threshold_ms:String(config.slow_threshold_ms??THRESHOLD_DEFAULT),slots:config.slots.map(slotDraft)});
  const clone=v=>JSON.parse(JSON.stringify(v));
  const trimmed=d=>({interval_ms:d.interval_ms,slow_threshold_ms:String(d.slow_threshold_ms??THRESHOLD_DEFAULT).trim(),slots:d.slots.map(s=>({...s,host:s.host.trim(),port:s.port.trim()}))});
  const sameContent=(a,b)=>JSON.stringify(trimmed(a))===JSON.stringify(trimmed(b));
  const isDirty=(d,base)=>JSON.stringify(trimmed(d))!==JSON.stringify(trimmed(base));
  // Switching between built-in and custom keeps address, port and ticks and only clears the labels, so no free text is carried into an enumeration.
  function switchKind(s,kind) {return s.kind===kind?s:{...s,kind,region:'',isp:''};}
  // The Server unmaps ::ffff:a.b.c.d before it decides the family, so the page does the same.
  function mappedIPv4(h) {
    const tail=h.toLowerCase().match(/^(.*:)(\d{1,3}(?:\.\d{1,3}){3})$/),head=tail?tail[1]+'0:0':h.toLowerCase();
    const halves=head.split('::');if(halves.length>2)return false;
    const part=s=>s===''?[]:s.split(':'),a=part(halves[0]),b=halves.length===2?part(halves[1]):[];
    const fill=8-a.length-b.length;
    if(halves.length===2?fill<1:a.length!==8)return false;
    const groups=halves.length===2?[...a,...Array(fill).fill('0'),...b]:a;
    if(!groups.every(g=>/^[0-9a-f]{1,4}$/.test(g)))return false;
    return groups.slice(0,5).every(g=>parseInt(g,16)===0)&&parseInt(groups[5],16)===0xffff;
  }
  const literalFamily=host=>{const h=host.trim();return /^\d{1,3}(\.\d{1,3}){3}$/.test(h)?'ipv4':h.includes(':')&&/^[0-9a-fA-F:.]+$/.test(h)?(mappedIPv4(h)?'ipv4':'ipv6'):'';};
  // Immediate hints only; the Server decides. A literal address of one family cannot be ticked for the other.
  function hints(d) {
    const out={};
    for(const s of d.slots) {
      const e={},host=s.host.trim(),lit=literalFamily(host);
      if(host) {
        if(!/^[0-9]+$/.test(s.port.trim())||+s.port<1||+s.port>65535)e.port='invalid_port';
        if(!s.region)e.region='label_required';
        if(!s.isp)e.isp='label_required';
      }
      if(lit==='ipv4')for(const k of ['ipv6_tcp','ipv6_icmp'])if(s[k])e[k]='family_mismatch';
      if(lit==='ipv6')for(const k of ['ipv4_tcp','ipv4_icmp'])if(s[k])e[k]='family_mismatch';
      out[s.slot]=e;
    }
    return out;
  }
  // The threshold is a whole number of milliseconds; the Server's histogram only resolves whole milliseconds.
  function thresholdHint(d) {
    const text=String(d.slow_threshold_ms??THRESHOLD_DEFAULT).trim();
    return /^[0-9]+$/.test(text)&&+text>=THRESHOLD_MIN&&+text<=THRESHOLD_MAX?'':'invalid_threshold';
  }
  const disabledSubs=host=>{const lit=literalFamily(host);return {ipv4_tcp:lit==='ipv6',ipv4_icmp:lit==='ipv6',ipv6_tcp:lit==='ipv4',ipv6_icmp:lit==='ipv4'};};
  const errorText={invalid_host:'地址不合法',reserved_name:'这是保留名称，不能检测',unsafe_address:'地址不是公网地址',family_mismatch:'字面地址与勾选的地址族不符',invalid_port:'端口须为 1–65535',
    invalid_label:'标注不合法（1–32 个字符，不含控制字符和首尾空格）',label_required:'已填地址时必须填写',invalid_interval:'检测频率须为 30–300 秒，每档 30 秒',invalid_threshold:'慢连接门槛须为 50–3000 的整数（毫秒）',invalid_encoding:'含有非法字符',invalid_request:'请求不合法'};
  function savePayload(d,settingsRevision) {
    if(!isDecimal(settingsRevision))throw Error('配置版本缺失，不能保存');
    if(!intervals.includes(d.interval_ms))throw Error('检测频率须为 30–300 秒，每档 30 秒');
    if(thresholdHint(d))throw Error(errorText.invalid_threshold);
    return {expected_revision:settingsRevision,interval_ms:d.interval_ms,slow_threshold_ms:Number(String(d.slow_threshold_ms).trim()),slots:d.slots.map(s=>{
      const host=s.host.trim();
      return {slot:s.slot,kind:s.kind,region:s.region,isp:s.isp,host,port:host?Number(s.port.trim()):0,slot_enabled:!!s.slot_enabled,ipv4_tcp:!!s.ipv4_tcp,ipv4_icmp:!!s.ipv4_icmp,ipv6_tcp:!!s.ipv6_tcp,ipv6_icmp:!!s.ipv6_icmp};
    })};
  }
  function enabledPayload(settingsRevision,enabled) {
    if(!isDecimal(settingsRevision))throw Error('配置版本缺失');
    return {expected_revision:settingsRevision,enabled:!!enabled};
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
  function cardGroup(h,target) {
    const metric=target?.protocol==='tcp'?'pure_connect':'echo_rtt';
    return h?.current?.find(g=>g.semantics==='staged_probe_v2'&&g.metric===metric);
  }
  // ---- R28: the card answer (v5/card). The Server judges every block; these functions check the shape and word it. ----
  const CARD_STATES=['gray','green','yellow','red'],BLOCK_COUNT=20;
  const STATE_TEXT={gray:'无数据或样本不足',green:'正常',yellow:'偶发慢 / 失败',red:'持续（告警）'};
  const wholeCount=v=>Number.isInteger(v)&&v>=0;
  function normalizeCard(v,family) {
    const fail=what=>{throw Error('v5 卡片契约不匹配：'+what);};
    if(!v||typeof v!=='object')fail('响应');
    if(v.family!==family)fail('地址族');
    for(const key of ['now','eval_end','threshold_ms','interval_ms','block_ms','window_start'])if(!finite(v[key]))fail(key);
    if(typeof v.alert_active!=='boolean'||!Array.isArray(v.lanes)||v.lanes.length!==slots.length)fail('卡槽');
    const lanes=v.lanes.map((l,i)=>{
      if(!l||l.slot!==slots[i]||!l.tcp||!l.icmp||!l.right||typeof l.tcp.status!=='string'||typeof l.icmp.status!=='string')fail('卡槽 '+(i+1));
      let blocks=null;
      if(l.tcp.status==='active') {
        if(!Array.isArray(l.tcp.blocks)||l.tcp.blocks.length!==BLOCK_COUNT)fail('方块数量');
        blocks=l.tcp.blocks.map(b=>{
          if(!b||!finite(b.start)||!CARD_STATES.includes(b.state)||![b.judged,b.slow,b.failed,b.unjudged].every(wholeCount)||typeof b.in_progress!=='boolean'||typeof b.partial!=='boolean'||(b.max_ms!=null&&!finite(b.max_ms)))fail('方块');
          return {start:b.start,state:b.state,judged:b.judged,slow:b.slow,failed:b.failed,unjudged:b.unjudged,max_ms:b.max_ms??null,in_progress:b.in_progress,partial:b.partial};
        });
      }
      const r=l.right;
      if(!['icmp_loss','tcp_failure','none'].includes(r.kind)||!['ok','few','none'].includes(r.samples)||!wholeCount(r.numerator)||!wholeCount(r.denominator)||(r.percent!=null&&!finite(r.percent)))fail('右侧数字');
      return {slot:l.slot,tcp:{status:l.tcp.status,condition:l.tcp.condition||'',blocks,red:!!l.tcp.red?.active,dns_failing:!!l.tcp.dns_failing},
        icmp:{status:l.icmp.status,condition:l.icmp.condition||''},right:{kind:r.kind,percent:r.percent??null,numerator:r.numerator,denominator:r.denominator,samples:r.samples}};
    });
    return {now:v.now,eval_end:v.eval_end,family,settings_revision:String(v.settings_revision??''),config_revision:String(v.config_revision??''),
      threshold_ms:v.threshold_ms,interval_ms:v.interval_ms,block_ms:v.block_ms,window_start:v.window_start,lanes,alert_active:v.alert_active};
  }
  // "上海电信" for a built-in slot (region + carrier), what the user typed for a custom one.
  function cardLabel(config,slot) {
    const s=config?.slots?.find(x=>x.slot===slot),region=s?.region||'',carrier=ispText(s)||'';
    const text=s?.kind==='builtin'?region+carrier:[region,carrier].filter(Boolean).join(' ');
    return text||`卡槽 ${slotNumber(slot)}（未标注）`;
  }
  const clock=v=>new Date(v).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',hour12:false});
  const peak=v=>v>=1000?String(Math.round(v)):v>=100?v.toFixed(0):v.toFixed(1);
  // The text of one 3-minute block, for the hover card and for assistive technology. It restates the Server's numbers only.
  function blockView(b,{label='',threshold=500,intervalMS=30000,blockMS=180000}={}) {
    const lines=[`${label?label+' · ':''}${clock(b.start)}–${clock(b.start+blockMS)} · ${STATE_TEXT[b.state]}`];
    if(b.judged>0)lines.push(`本格 ${b.judged} 个样本：慢 ${b.slow} · 失败 ${b.failed}${b.max_ms!=null?` · 最高约 ${peak(b.max_ms)} ms`:''}${b.unjudged?` · 未判断 ${b.unjudged}`:''}`);
    else if(b.state==='gray')lines.push(b.unjudged?`本格没有可判断的样本（${b.unjudged} 个未判断）`:'本格没有可用样本');
    if(b.state==='gray'&&b.judged>0)lines.push('样本不足，不判断');
    if(b.state==='gray'&&intervalMS>=240000)lines.push('检测间隔较长，部分方块没有样本');
    if(b.state==='yellow'||b.state==='red')lines.push(`慢 = 连接时间超过 ${threshold} ms`,'B 面曲线同一时间有对应标记');
    if(b.partial)lines.push('含配置变更或缺测，部分分钟未计入');
    if(b.in_progress)lines.push('数据仍在到达');
    return {state:b.state,text:lines.join('\n')};
  }
  // The number at the right of a lane: ICMP loss when ICMP runs, else the TCP failure rate. TCP never says "loss".
  function rightView(right) {
    const icmp=right.kind==='icmp_loss';
    if(right.kind==='none'||right.samples==='none'||right.percent==null)
      return {text:'—',tone:'muted',title:right.kind==='none'?'没有可显示的数字':icmp?'近 1 小时没有完整的 ICMP 检测':'近 1 小时 TCP 样本太少'};
    const value=right.percent===0?'0%':`${right.percent.toFixed(1)}%`,few=right.samples==='few';
    const title=icmp?`近 1 小时 ${right.denominator} 个 Echo，丢 ${right.numerator} 个`:`近 1 小时 ${right.numerator} / ${right.denominator} 次连接失败`;
    return {text:`${icmp?'丢包':'失败'} ${value}${few?' ·少':''}`,tone:right.percent>=10?'crit':right.percent>=1?'warn':'muted',title:title+(few?'；样本较少，仅供参考':'')};
  }
  // What a lane shows in place of the 20 blocks, or its blocks.
  function laneView(lane) {
    const tcp=lane.tcp,icmp=lane.icmp;
    if(tcp.status==='active') {
      if(tcp.dns_failing)return {kind:'text',key:'dns',text:'解析失败',title:'最近的检测都在解析目标域名时失败；这些样本不计入慢 / 失败的判断'};
      return {kind:'blocks',blocks:tcp.blocks,red:tcp.red};
    }
    if(tcp.status==='inactive'&&icmp.status==='active')return {kind:'text',key:'icmp-only',text:'未测 TCP（只开了 ICMP）',title:'这个卡槽只开了 ICMP，没有测 TCP；右侧是 ICMP 丢包率'};
    const n=statusNotice(tcp.status,tcp.condition);
    return {kind:'text',key:n.key,text:n.text,title:n.detail};
  }
  const api={slots,names,finite,ms,time,percent,coordinator,transportLease,plotAxes,provinces,isps,intervals,subItems,adapt,slotLabel,slotShort,laneNotice,cardNotice,draftOf,slotDraft,clone,isDirty,sameContent,switchKind,hints,disabledSubs,errorText,literalFamily,savePayload,enabledPayload,historyKey,ratio,current,blockText,columns,cardGroup,statusNotice,THRESHOLD_MIN,THRESHOLD_MAX,THRESHOLD_DEFAULT,thresholdHint,cardLabel,normalizeCard,blockView,rightView,laneView,STATE_TEXT};
  if(typeof module==='object'&&module.exports)module.exports=api;else root.NetworkQualityCore=api;
})(globalThis);
