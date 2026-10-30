/* Batch summaries use the same per-resource request/mutation barriers as detail. */
(function(root){
 'use strict';
 const GROUPS=['runtime','name','lifecycle','country','plan','organization','summary','google','outbounds','traffic'];
 function telemetryPhase(id){let hash=2166136261;for(const c of id)hash=Math.imul(hash^c.charCodeAt(0),16777619)>>>0;return hash%10000;}
 function normalize(row){
  const result={...row,dashboard_traffic:row.traffic,traffic:row.accumulated_traffic,dashboard_loaded:true,dashboard_received_at:Date.now()};
  delete result.accumulated_traffic;
  return result;
 }
 function create({state,read,onchange=()=>{},onerror=()=>{},onsuccess=()=>{},frame=root.requestAnimationFrame||((f)=>setTimeout(f,0)),now=Date.now,setTimer=setTimeout,clearTimer=clearTimeout}){
  let catalog={revision:0,groups:[],tags:[],exchange_rates:[],expenses:null},ready=false,closed=false;
  let full=null,partial=null,scheduled=false,wantFull=false;
  const dirty=new Set();
  const telemetryDirty=new Set(),telemetryReady=new Set(),summaryDue=new Map();
  let telemetryTimer=null,lastTelemetryBatch=-Infinity;
  // These are upper bounds, not a periodic-read promise. Full reads renew every
  // device, then deterministic ID phases spread the next eligibility over 10s.
  // Partial replies retain the 10s minimum; metadata continues to bypass it.
  function armTelemetry(){
   if(telemetryTimer!==null){clearTimer(telemetryTimer);telemetryTimer=null;}
   if(closed||full||partial)return;
   const eligible=id=>!state.blocked(id)&&!state.pending(id);
   const times=[...telemetryDirty].filter(eligible).map(id=>summaryDue.get(id)??now()+10000+telemetryPhase(id));
   if([...telemetryReady].some(eligible))times.push(now());
   if(!times.length)return;
   const due=Math.max(lastTelemetryBatch+1000,Math.min(...times));
   telemetryTimer=setTimer(()=>{
    telemetryTimer=null;const time=now();
    for(const id of telemetryDirty)if(time>=(summaryDue.get(id)??Infinity)){telemetryDirty.delete(id);if(!state.blocked(id))telemetryReady.add(id);}
    schedule();
   },Math.max(0,due-now()));
  }
  function readSummary(ids,all=false){for(const id of ids){summaryDue.set(id,now()+10000+(all?telemetryPhase(id):0));telemetryDirty.delete(id);telemetryReady.delete(id);}armTelemetry();onsuccess();}
  function acceptCatalog(data){
   if(data.revision>=catalog.revision)catalog={revision:data.revision,groups:data.groups||[],tags:data.tags||[],exchange_rates:data.exchange_rates||[],expenses:data.expenses,server_now:data.server_now};
  }
  function schedule(){if(closed||scheduled)return;scheduled=true;frame(()=>{scheduled=false;flush();});}
  async function refresh(){
   if(closed)return;if(full)return full;
   if(partial){wantFull=true;await partial;wantFull=false;return refresh();}
   const context=state.beginRefresh();
   full=(async()=>{
    try{
     const data=await read('/api/v1/web/dashboard',context.controller.signal);
     if(!state.validCycle(context)||closed)return;
     const before=new Map(state.agents);
     if(!state.commitList(context,data.items.map(normalize)))return;
     acceptCatalog(data);ready=true;readSummary(state.agents.keys(),true);
     onchange(new Set([...before.keys(),...state.agents.keys()]),{full:true});
    }catch(error){if(!closed&&error.name!=='AbortError')onerror(error);}
    finally{state.endRefresh(context);full=null;armTelemetry();if(dirty.size||wantFull)schedule();}
   })();return full;
  }
  async function flush(){
   if(closed)return;
   if(full||partial)return;
   if(wantFull){wantFull=false;await refresh();return;}
   const allowed=now()>=lastTelemetryBatch+1000;
   const ids=[...new Set([...dirty,...(allowed?telemetryReady:[])])].filter(id=>!state.pending(id)&&!state.blocked(id)).slice(0,200);
   if(!ids.length){armTelemetry();return;}
   const urgent=new Set(ids.filter(id=>dirty.has(id))),telemetry=ids.filter(id=>!urgent.has(id));
   if(telemetry.length)lastTelemetryBatch=now();
   for(const id of ids){dirty.delete(id);telemetryReady.delete(id);}
   const tickets=new Map(ids.map(id=>[id,state.read(id,null,GROUPS)]));
   let failed=false;
   partial=(async()=>{
    try{
     const data=await read('/api/v1/web/dashboard?ids='+ids.map(encodeURIComponent).join(','));
     if(closed)return;
     const changed=new Set();
     for(const row of data.items){const ticket=tickets.get(row.agent_id);if(ticket&&state.commitRead(ticket,normalize(row)))changed.add(row.agent_id);}
     acceptCatalog(data);readSummary(changed);onchange(changed,{full:false});
    }catch(error){
     failed=true;
     if(error.status===404){for(const id of ids)dirty.delete(id);wantFull=true;}
     else{for(const id of ids)if(urgent.has(id))dirty.add(id);else{telemetryDirty.add(id);summaryDue.set(id,now()+10000);}if(!closed)onerror(error);}
    }finally{partial=null;armTelemetry();if(wantFull)schedule();}
   })();await partial;
   if(!failed&&dirty.size&&!wantFull&&ids.some(id=>!state.pending(id)))schedule();
  }
  function notify(id){if(!closed&&id&&!state.blocked(id)){dirty.add(id);schedule();}}
  function metadata(value){
   const id=value.agent_id;
   if(value.revoked&&state.agents.has(id)){const token=state.beginDelete(id);state.finishDelete(token,true);dirty.delete(id);telemetryDirty.delete(id);telemetryReady.delete(id);summaryDue.delete(id);onchange(new Set([id]),{full:false});wantFull=true;schedule();return;}
   if(id){state.invalidate(id,value.kind==='name'?['name','summary']:['lifecycle','summary']);notify(id);}
  }
  return {refresh,async reload(){if(full)await full;if(partial)await partial;wantFull=false;return refresh();},notify,telemetry(id){if(!closed&&id&&!state.blocked(id)){if(!summaryDue.has(id))summaryDue.set(id,now()+10000+telemetryPhase(id));telemetryDirty.add(id);armTelemetry();}},metadata,organization(){for(const id of state.agents.keys())state.invalidate(id,['organization','summary']);wantFull=true;schedule();},retry(){schedule();},
   catalog:()=>catalog,ready:()=>ready,close(){closed=true;dirty.clear();telemetryDirty.clear();telemetryReady.clear();if(telemetryTimer!==null)clearTimer(telemetryTimer);state.beginRefresh().controller.abort();},
   snapshot:()=>({ready,pending:[...dirty],full:!!full,partial:!!partial})};
 }
 const api={create,normalize,GROUPS};if(typeof module==='object'&&module.exports)module.exports=api;else root.DashboardData=api;
})(globalThis);
