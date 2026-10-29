/* Batch summaries use the same per-resource request/mutation barriers as detail. */
(function(root){
 'use strict';
 const GROUPS=['runtime','name','lifecycle','country','plan','organization','summary','google','outbounds','traffic'];
 function normalize(row){
  const result={...row,dashboard_traffic:row.traffic,traffic:row.accumulated_traffic,dashboard_loaded:true};
  delete result.accumulated_traffic;
  return result;
 }
 function create({state,read,onchange=()=>{},onerror=()=>{},frame=root.requestAnimationFrame||((f)=>setTimeout(f,0))}){
  let catalog={revision:0,groups:[],tags:[],exchange_rates:[],expenses:null},ready=false,closed=false;
  let full=null,partial=null,scheduled=false,wantFull=false;
  const dirty=new Set();
  function acceptCatalog(data){
   if(data.revision>=catalog.revision)catalog={revision:data.revision,groups:data.groups||[],tags:data.tags||[],exchange_rates:data.exchange_rates||[],expenses:data.expenses,server_now:data.server_now};
  }
  function schedule(){if(closed||scheduled)return;scheduled=true;frame(()=>{scheduled=false;flush();});}
  async function refresh(){
   if(closed)return;if(full)return full;
   if(partial){wantFull=true;return partial;}
   const context=state.beginRefresh();
   full=(async()=>{
    try{
     const data=await read('/api/v1/web/dashboard',context.controller.signal);
     if(!state.validCycle(context)||closed)return;
     const before=new Map(state.agents);
     if(!state.commitList(context,data.items.map(normalize)))return;
     acceptCatalog(data);ready=true;
     onchange(new Set([...before.keys(),...state.agents.keys()]),{full:true});
    }catch(error){if(!closed&&error.name!=='AbortError')onerror(error);}
    finally{state.endRefresh(context);full=null;if(dirty.size||wantFull)schedule();}
   })();return full;
  }
  async function flush(){
   if(closed)return;
   if(full||partial)return;
   if(wantFull){wantFull=false;await refresh();return;}
   const ids=[...dirty].filter(id=>!state.pending(id)&&!state.blocked(id)).slice(0,200);
   if(!ids.length)return;
   for(const id of ids)dirty.delete(id);
   const tickets=new Map(ids.map(id=>[id,state.read(id,null,GROUPS)]));
   let failed=false;
   partial=(async()=>{
    try{
     const data=await read('/api/v1/web/dashboard?ids='+ids.map(encodeURIComponent).join(','));
     if(closed)return;
     const changed=new Set();
     for(const row of data.items){const ticket=tickets.get(row.agent_id);if(ticket&&state.commitRead(ticket,normalize(row)))changed.add(row.agent_id);}
     acceptCatalog(data);onchange(changed,{full:false});
    }catch(error){
     failed=true;
     if(error.status===404){for(const id of ids)dirty.delete(id);wantFull=true;}
     else{for(const id of ids)dirty.add(id);if(!closed)onerror(error);}
    }finally{partial=null;if(wantFull)schedule();}
   })();await partial;
   if(!failed&&dirty.size&&!wantFull&&ids.some(id=>!state.pending(id)))schedule();
  }
  function notify(id){if(!closed&&id&&!state.blocked(id)){dirty.add(id);schedule();}}
  function metadata(value){
   const id=value.agent_id;
   if(value.revoked&&state.agents.has(id)){const token=state.beginDelete(id);state.finishDelete(token,true);dirty.delete(id);onchange(new Set([id]),{full:false});wantFull=true;schedule();return;}
   if(id){state.invalidate(id,value.kind==='name'?['name','summary']:['lifecycle','summary']);notify(id);}
  }
  return {refresh,notify,metadata,organization(){for(const id of state.agents.keys())state.invalidate(id,['organization','summary']);wantFull=true;schedule();},retry(){schedule();},
   catalog:()=>catalog,ready:()=>ready,close(){closed=true;dirty.clear();state.beginRefresh().controller.abort();},
   snapshot:()=>({ready,pending:[...dirty],full:!!full,partial:!!partial})};
 }
 const api={create,normalize,GROUPS};if(typeof module==='object'&&module.exports)module.exports=api;else root.DashboardData=api;
})(globalThis);
