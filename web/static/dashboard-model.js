(function(root){
 'use strict';
 const State=root.AgentState||(typeof require==='function'?require('./agent-state.js'):null);
 const KEY='404-probe.dashboard.v1',QUICK={offline:'离线',network:'网络异常',resources:'资源吃紧',traffic:'流量将满',renewal:'7 天内续费',health:'需要关注',upgrade:'待升级'};
 const STATUS={online:'在线',offline:'离线',paused:'暂停',unknown:'状态未知'};
 const defaults=()=>({version:1,search:'',groups:[],tags:[],countries:[],statuses:[],quick:[],sort:'group',collapsed:[],overviewClosed:false,expensePeriod:'monthly',expanded:false});
 function preferences(storage=()=>root.localStorage){
  let value=defaults();
  try{const saved=JSON.parse(storage().getItem(KEY));if(saved?.version===1){for(const k of ['groups','tags','countries','statuses','quick','collapsed'])if(Array.isArray(saved[k]))value[k]=[...new Set(saved[k].filter(x=>typeof x==='string'))];if(typeof saved.search==='string')value.search=saved.search.slice(0,256);if(['group','name','renewal'].includes(saved.sort))value.sort=saved.sort;for(const k of ['overviewClosed','expanded'])value[k]=saved[k]===true;if(saved.expensePeriod==='yearly')value.expensePeriod='yearly';}}catch{}
  const save=()=>{try{storage().setItem(KEY,JSON.stringify(value));}catch{}};
  return {value,save,validate(catalog){let changed=false;for(const [key,allowed]of [['groups',new Set(['__ungrouped',...catalog.groups.map(g=>g.id)])],['tags',new Set(catalog.tags.map(t=>t.id))],['collapsed',new Set(['__ungrouped',...catalog.groups.map(g=>g.id)])],['statuses',new Set(Object.keys(STATUS))],['quick',new Set(Object.keys(QUICK))]]){const filtered=value[key].filter(id=>allowed.has(id));if(filtered.length!==value[key].length){value[key]=filtered;changed=true;}}if(changed)save();}};
 }
 function lifecycle(a){return a.revoked?'revoked':a.disabled_at!=null?'paused':a.online?'online':a.state||a.last_seen?'offline':'unknown';}
 function flags(a,server,now=Date.now()){
  const r=a.resources,t=a.dashboard_traffic||{},p=a.plan,n=a.network;
  const fresh=!!r?.known&&!r.stale&&(!r.max_age_ms||!r.collected_at||now-r.collected_at<=r.max_age_ms);
  const networkKnown=!!n?.known&&typeof n.active==='boolean'&&(!n.monitored||!n.expires_at||now<=n.expires_at);
  const resource=fresh&&[r.cpu_percent,r.ram_percent,r.disk_percent].some(v=>typeof v==='number'&&Number.isFinite(v)&&v>=90);
  const traffic=t.quota_state==='current'&&typeof p?.quota_bytes==='number'&&p.quota_bytes>0&&typeof p?.usage_bytes==='number'&&p.usage_bytes/p.quota_bytes>=.8;
  const network=networkKnown&&n.active===true;
  const order=State.compareStableVersions(a.version,server?.version);
  const nonformal=order===null&&/^v\d+\.\d+\.\d+-(?:beta|rc)\.[1-9]\d*$/.test(a.version||'')&&State.compareReleaseVersions(a.version,a.version)===0;
  return {offline:lifecycle(a)==='offline',network,resources:resource,traffic,renewal:a.renewal?.type==='due_soon',health:network||resource||traffic,upgrade:order===-1,versionNonformal:nonformal,versionUnknown:order===null&&!nonformal,insufficient:!!(!fresh||!networkKnown||(p?.traffic_mode&&(!t.known||t.stale||t.quota_state==='expired')))};
 }
 function aggregate(agents,server,now=Date.now()){
  const s={registered:0,online:0,offline:0,paused:0,unknown:0,health:0,insufficient:0,upgrade:0,versionUnknown:0,versionNonformal:0,renewal:0,overdue:0,renewalInvalid:0,nearest:null,quick:Object.fromEntries(Object.keys(QUICK).map(k=>[k,0]))};
  for(const a of agents.values()){
   if(a.revoked)continue;s.registered++;s[lifecycle(a)]++;
   const f=flags(a,server,now);for(const k of Object.keys(QUICK))if(f[k])s.quick[k]++;
   for(const k of ['health','insufficient','upgrade','versionUnknown','versionNonformal','renewal'])if(f[k])s[k]++;
   if(a.renewal?.type==='overdue')s.overdue++;if(a.renewal?.type==='invalid')s.renewalInvalid++;
   const days=a.renewal?.days;if(Number.isInteger(days)&&days>=0&&(!s.nearest||days<s.nearest.renewal.days||(days===s.nearest.renewal.days&&a.agent_id<s.nearest.agent_id)))s.nearest=a;
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
 function select(agents,catalog,p,server,now=Date.now()){
  const groupIDs=new Set(catalog.groups.map(g=>g.id));
  const list=[...agents.values()].map(a=>a.group_id&&!groupIDs.has(a.group_id)?{...a,group_id:null}:a).filter(a=>matches(a,p,server,now)),groups=[...catalog.groups].sort((a,b)=>a.sort_order-b.sort_order||a.id.localeCompare(b.id));
  if(p.sort==='renewal')list.sort((a,b)=>(a.renewal?.days??Infinity)-(b.renewal?.days??Infinity)||byName(a,b));else list.sort(byName);
  const sections=p.sort==='group'?[...groups.map(g=>({id:g.id,name:g.name,items:list.filter(a=>a.group_id===g.id)})),{id:'__ungrouped',name:'未分组',items:list.filter(a=>!a.group_id)}].filter(g=>g.items.length):[{id:'__all',name:'全部设备',items:list}];
  return {list,sections,summary:aggregate(agents,server,now),simple:agents.size<=3&&!catalog.groups.length&&!catalog.tags.length&&!active(p)&&!p.expanded};
 }
 const api={KEY,QUICK,STATUS,defaults,preferences,lifecycle,flags,aggregate,active,matches,select};if(typeof module==='object'&&module.exports)module.exports=api;else root.DashboardModel=api;
})(globalThis);
