(function(root){
 'use strict';
 const State=root.AgentState||(typeof require==='function'?require('./agent-state.js'):null);
 const KEY='404-probe.dashboard.v1',QUICK={offline:'离线',disk:'磁盘异常',network:'网络异常',resources:'资源吃紧',traffic:'流量将满',renewal:'7 天内续费',health:'需要关注',upgrade:'待升级'};
 const STATUS={online:'在线',offline:'离线',paused:'暂停',unknown:'状态未知'};
 // Client display window: 10s telemetry throttle, 15s calibration, read latency
 // and margin. Two refresh cycles tolerate one failed read; the Server's 5s
 // computation TTL and resource max_age_ms remain independent.
 const NETWORK_SUMMARY_FRESH_MS=30000;
 const SORTS={group:'分组',name:'名称',system:'系统',cpu:'CPU',memory:'内存',disk:'磁盘',rate:'实时速率',traffic:'周期流量 %',latency:'延迟',price:'价格',renewal:'续费',upgrade:'待升级'};
 const defaults=()=>({version:1,search:'',groups:[],tags:[],countries:[],statuses:[],quick:[],sort:'group',collapsed:[],expensePeriod:'monthly',panelCollapsed:false,viewMode:'cards',sortDir:'asc',latencySlot:1});
 function preferences(storage=()=>root.localStorage){
  let value=defaults();
  try{const saved=JSON.parse(storage().getItem(KEY));if(saved?.version===1){for(const k of ['groups','tags','countries','statuses','quick','collapsed'])if(Array.isArray(saved[k]))value[k]=[...new Set(saved[k].filter(x=>typeof x==='string'))];if(typeof saved.search==='string')value.search=saved.search.slice(0,256);if(['group','name','renewal'].includes(saved.sort))value.sort=saved.sort;for(const k of ['panelCollapsed'])value[k]=saved[k]===true;if(saved.expensePeriod==='yearly')value.expensePeriod='yearly';}}catch{}
  try{const saved=JSON.parse(storage().getItem(KEY));if(saved?.version===1){if(['cards','list','network'].includes(saved.viewMode))value.viewMode=saved.viewMode;if(Object.hasOwn(SORTS,saved.sort))value.sort=saved.sort;if(saved.sortDir==='desc')value.sortDir='desc';if([1,2,3].includes(saved.latencySlot))value.latencySlot=saved.latencySlot;}}catch{}
  const save=()=>{try{storage().setItem(KEY,JSON.stringify(value));}catch{}};
  return {value,save,validate(catalog){let changed=false;for(const [key,allowed]of [['groups',new Set(['__ungrouped',...catalog.groups.map(g=>g.id)])],['tags',new Set(catalog.tags.map(t=>t.id))],['collapsed',new Set(['__ungrouped',...catalog.groups.map(g=>g.id)])],['statuses',new Set(Object.keys(STATUS))],['quick',new Set(Object.keys(QUICK))]]){const filtered=value[key].filter(id=>allowed.has(id));if(filtered.length!==value[key].length){value[key]=filtered;changed=true;}}if(changed)save();}};
 }
 function lifecycle(a){return a.revoked?'revoked':a.disabled_at!=null?'paused':a.online?'online':a.state||a.last_seen?'offline':'unknown';}
 const diskFaultReasons={write_eio:['写入失败','写入失败（I/O 错误）'],read_eio:['读回失败','读回失败（I/O 错误）'],read_only:['变为只读','文件系统变为只读'],stalled:['I/O 卡住','I/O 卡住'],ext4_errors:['文件系统报错','ext4 记录到新错误']};
 const diskHints={full:'磁盘空间或 inode 已满',scsi_errors:'SCSI 错误计数增加',slow:'探测偏慢',unwritable:'数据目录不可写（配置问题）'};
 function diskReport(a,now=Date.now()){
  const h=a.state?.host_health;
  if(!h?.disk||h.stale||a.state.stale||lifecycle(a)!=='online'||(a.resources&&resourcesOld(a,now)))return null;
  if(h.disk.status==='ok'&&(!(h.disk.checked_at>0)||now-h.disk.checked_at>150000||h.disk.checked_at-now>60000))return null;
  return h;
 }
 function diskTone(a,now=Date.now()){
  const h=diskReport(a,now);
  if(h?.disk.status==='error')return 'crit';
  return h?.disk.status==='ok'&&h.last_fault_at>0&&h.last_fault_at<=now&&now-h.last_fault_at<=7*86400000?'warn':'';
 }
 function diskFaults(disk,{short=false,now=Date.now()}={}){
  const result=[];
  for(const [reason,[brief,full]] of Object.entries(diskFaultReasons)){
   if(!disk?.reasons?.includes(reason))continue;
   if(short)return [brief];
   const seconds=Math.floor(Math.max(now-(disk.stalled_since||now),disk.probe_ms||0,30000)/1000);
   result.push(reason==='stalled'?`I/O 卡住（已超过 ${seconds} 秒无响应）`:full);
  }
  return result;
 }
 function diskTime(at){
  const d=new Date(at),pad=n=>String(n).padStart(2,'0');
  return `${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
 }
 const savedPlans=new WeakSet();
 function savedPlan(plan){if(plan&&typeof plan==='object')savedPlans.add(plan);}
 // A local plan write arrives before its next summary. Until the matching
 // summary arrives, project the saved date rather than the preceding date's days.
 function renewal(a,now=Date.now()){
  const p=a.plan,r=a.renewal||{};if(!p||!savedPlans.has(p))return r;
  const date=p.renewal_date||p.expiry_date||'',timezone=p.timezone||'UTC';
  if(r.date===date&&(r.timezone||'UTC')===timezone)return r;
  const value={date,timezone,field:p.renewal_date?'renewal_date':p.expiry_date?'expiry_date':''};
  if(!date)return {...value,type:'missing'};
  const management=root.Management||(typeof require==='function'?require('./management.js'):null),today=management?.planDate(now,timezone);
  if(timezone==='Local'||!today||!management.validPlanDate(date))return {...value,type:'invalid'};
  const days=Math.round((Date.parse(date)-Date.parse(today))/86400000);
  return {...value,days,type:days<0?'overdue':days<=7?'due_soon':'upcoming'};
 }
 function flags(a,server,now=Date.now()){
  const r=a.resources,t=a.dashboard_traffic||{},p=a.plan,n=a.network;
  const fresh=!!r?.known&&!r.stale&&(!r.max_age_ms||!r.collected_at||now-r.collected_at<=r.max_age_ms);
  const networkFresh=a.dashboard_received_at?now-a.dashboard_received_at<=NETWORK_SUMMARY_FRESH_MS:!n?.expires_at||now<=n.expires_at;
  const networkKnown=!!n?.known&&typeof n.active==='boolean'&&(!n.monitored||networkFresh);
  const resource=fresh&&[r.cpu_percent,r.ram_percent,r.disk_percent].some(v=>typeof v==='number'&&Number.isFinite(v)&&v>=90);
  const traffic=t.quota_state==='current'&&typeof p?.quota_bytes==='number'&&p.quota_bytes>0&&typeof p?.usage_bytes==='number'&&p.usage_bytes/p.quota_bytes>=.8;
  const network=networkKnown&&n.active===true;
  const disk=diskTone(a,now)==='crit';
  const order=State.compareStableVersions(a.version,server?.version);
  const nonformal=order===null&&/^v\d+\.\d+\.\d+-(?:beta|rc)\.[1-9]\d*$/.test(a.version||'')&&State.compareReleaseVersions(a.version,a.version)===0;
  return {offline:lifecycle(a)==='offline',disk,network,resources:resource,traffic,renewal:renewal(a,now).type==='due_soon',health:disk||network||resource||traffic,upgrade:order===-1,versionNonformal:nonformal,versionUnknown:order===null&&!nonformal,insufficient:!!(!fresh||!networkKnown||(p?.traffic_mode&&(!t.known||t.stale||t.quota_state==='expired')))};
 }
 function aggregate(agents,server,now=Date.now()){
  const s={registered:0,online:0,offline:0,paused:0,unknown:0,health:0,insufficient:0,upgrade:0,versionUnknown:0,versionNonformal:0,renewal:0,overdue:0,renewalInvalid:0,nearest:null,quick:Object.fromEntries(Object.keys(QUICK).map(k=>[k,0]))};
  for(const a of agents.values()){
   if(a.revoked)continue;s.registered++;s[lifecycle(a)]++;
   const f=flags(a,server,now);for(const k of Object.keys(QUICK))if(f[k])s.quick[k]++;
   for(const k of ['health','insufficient','upgrade','versionUnknown','versionNonformal','renewal'])if(f[k])s[k]++;
   const due=renewal(a,now);if(due.type==='overdue')s.overdue++;if(due.type==='invalid')s.renewalInvalid++;
   const days=due.days;if(Number.isInteger(days)&&days>=0&&(!s.nearest||days<s.nearest.renewal.days||(days===s.nearest.renewal.days&&a.agent_id<s.nearest.agent_id)))s.nearest={...a,renewal:due};
  }return s;
 }
 function active(p){return !!(p.search.trim()||['groups','tags','countries','statuses','quick'].some(k=>p[k].length));}
 function matches(a,p,server,now){
  if(a.revoked)return false;
  if(p.search.trim()&&!(a.search_fields||[]).some(text=>typeof text==='string'&&text.toLocaleLowerCase().includes(p.search.trim().toLocaleLowerCase())))return false;
  if(p.groups.length&&!p.groups.includes(a.group_id||'__ungrouped'))return false;
  if(p.tags.length&&!p.tags.some(id=>(a.tag_ids||[]).includes(id)))return false;
  if(p.countries.length&&!p.countries.includes(a.country_code||'unknown'))return false;
  if(p.statuses.length&&!p.statuses.includes(lifecycle(a)))return false;
  const f=flags(a,server,now);return p.quick.every(k=>f[k]);
 }
 const collator=new Intl.Collator('zh-CN');
 const byName=(a,b)=>collator.compare(a.name||'',b.name||'')||a.agent_id.localeCompare(b.agent_id);
 function resourcesOld(a,now=Date.now()){const r=a.resources;return !r?.known||r.stale||!!(r.max_age_ms&&r.collected_at&&now-r.collected_at>r.max_age_ms)||lifecycle(a)!=='online';}
 // Same monthly USD basis as estimateExpenses; keep decimal ordering exact.
 function monthlyUSD(plan,rates=[]){
  if(!plan)return null;
  const renewal=plan.renewal_price!=null&&plan.renewal_price!=='',price=renewal?plan.renewal_price:plan.purchase_price,period=renewal?plan.renewal_price_period:plan.purchase_price_period,months={monthly:1,quarterly:3,semiannual:6,yearly:12}[period];
  const rate=plan.currency==='USD'?'1':rates.find(r=>r.currency===plan.currency)?.units_per_usd;
  if(!months||! /^(0|[1-9][0-9]{0,11})(\.[0-9]{1,3})?$/.test(price??'')||! /^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$/.test(rate??'')||!/[1-9]/.test(rate))return null;
  const decimal=s=>{const [a,b='']=String(s).split('.');return {n:BigInt(a+b),d:10n**BigInt(b.length)};},v=decimal(price),r=decimal(rate);
  return {n:v.n*r.d,d:v.d*r.n*BigInt(months)};
 }
 function sortValue(a,key,p,server,catalog,now){
  if(key==='system')return a.state?.os||null;
  if(['cpu','memory','disk'].includes(key)){const field={cpu:'cpu_percent',memory:'ram_percent',disk:'disk_percent'}[key],r=a.resources;return !resourcesOld(a,now)?(a.state?.collected_at>=r.collected_at&&Number.isFinite(a.state?.[field])?a.state[field]:r[field]):null;}
  if(key==='rate')return !resourcesOld(a,now)&&typeof a.state?.rx_rate==='number'&&typeof a.state?.tx_rate==='number'?a.state.rx_rate+a.state.tx_rate:null;
  if(key==='traffic')return a.dashboard_traffic?.known&&!a.dashboard_traffic.stale&&a.dashboard_traffic.quota_state==='current'&&a.plan?.quota_bytes>0?100*a.plan.usage_bytes/a.plan.quota_bytes:null;
  if(key==='latency'){const s=a.network_slots?.find(s=>s.slot===(p.latencySlot||1));return s?.state==='ok'?s.latest_ms:null;}
  if(key==='price')return monthlyUSD(a.plan,catalog?.exchange_rates);
  if(key==='renewal')return renewal(a,now).days??null;
  if(key==='upgrade')return flags(a,server).upgrade?1:0;
  return a.name||'';
 }
 function comparator(p,server,catalog,now=Date.now()){return (a,b)=>{const x=sortValue(a,p.sort,p,server,catalog,now),y=sortValue(b,p.sort,p,server,catalog,now),valid=v=>v!=null&&(typeof v!=='number'||Number.isFinite(v));if(!valid(x)||!valid(y))return valid(x)?-1:valid(y)?1:byName(a,b);const delta=p.sort==='price'?x.n*y.d-y.n*x.d:null,c=delta!==null?(delta<0n?-1:delta>0n?1:0):typeof x==='string'?collator.compare(x,y):x-y;return c*(p.sortDir==='desc'?-1:1)||byName(a,b);};}
 // Full reads, SSE and calibration contain no order signal. Keep ids, not DTOs.
 function createOrder(){let key='',order=new Map();return {apply(model,catalog,p){
  const next=JSON.stringify([p.sort,p.sortDir,p.sort==='latency'?p.latencySlot:null,p.search,p.groups,p.tags,p.countries,p.statuses,p.quick,catalog.groups.map(g=>[g.id,g.name,g.sort_order])]);
  if(next!==key){key=next;order=new Map();}
  const liveGroups=new Set();for(const g of model.sections){liveGroups.add(g.id);const ids=order.get(g.id);
   // The common live update already has the frozen ids in the same order.
   // Keep its fresh DTOs without allocating reconciliation maps and arrays.
   if(ids?.length===g.items.length&&g.items.every((a,i)=>a.agent_id===ids[i]))continue;
   const byID=new Map(g.items.map(a=>[a.agent_id,a])),prior=(ids||[]).filter(id=>byID.has(id)),known=new Set(prior);for(const a of g.items)if(!known.has(a.agent_id)){known.add(a.agent_id);prior.push(a.agent_id);}order.set(g.id,prior);g.items=prior.map(id=>byID.get(id));}
  for(const id of order.keys())if(!liveGroups.has(id))order.delete(id);return model;
 },reset(){key='';order=new Map();}};}
 function select(agents,catalog,p,server,now=Date.now()){
  const groupIDs=new Set(catalog.groups.map(g=>g.id));
  const list=[...agents.values()].map(a=>a.group_id&&!groupIDs.has(a.group_id)?{...a,group_id:null}:a).filter(a=>matches(a,p,server,now)),groups=[...catalog.groups].sort((a,b)=>a.sort_order-b.sort_order||a.id.localeCompare(b.id));
  list.sort(comparator(p,server,catalog,now));
  const sections=p.sort==='group'?[...groups.map(g=>({id:g.id,name:g.name,items:list.filter(a=>a.group_id===g.id)})),{id:'__ungrouped',name:'未分组',items:list.filter(a=>!a.group_id)}].filter(g=>g.items.length):[{id:'__all',name:'全部设备',items:list}];
  return {list,sections,summary:aggregate(agents,server,now)};
 }
 const api={KEY,QUICK,STATUS,SORTS,NETWORK_SUMMARY_FRESH_MS,defaults,preferences,lifecycle,resourcesOld,savedPlan,renewal,diskReport,diskTone,diskFaults,diskHints,diskTime,flags,aggregate,active,matches,select,createOrder,comparator};if(typeof module==='object'&&module.exports)module.exports=api;else root.DashboardModel=api;
})(globalThis);
