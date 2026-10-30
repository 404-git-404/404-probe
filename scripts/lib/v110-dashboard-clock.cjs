// Deterministic integration clock for the real state/data/model modules.
const path=require('node:path');
async function simulate({milliseconds=90000,partialLatency=1500,failReads=false,source=path.resolve(__dirname,'../..')}={}){
 const State=require(path.join(source,'web/static/agent-state.js')),Data=require(path.join(source,'web/static/dashboard-data.js')),Model=require(path.join(source,'web/static/dashboard-model.js'));
 let time=1,serial=0;const original=Date.now;Date.now=()=>time;
 const ids=Array.from({length:200},(_,i)=>i.toString(16).padStart(32,'0')),state=State.create(),timers=new Map(),frames=[],pending=[],calls=[],samples=[];
 const row=id=>({agent_id:id,name:id,online:true,version:'v1.0.9',tag_ids:[],resources:{known:true,stale:false,cpu_percent:5,ram_percent:5,disk_percent:5,collected_at:time,max_age_ms:30000},network:{known:true,active:true,monitored:true,expires_at:time+5000},state:{cpu_percent:5},traffic:{quota_state:'unconfigured'},accumulated_traffic:{rx_total:1}});
 const tick=()=>new Promise(setImmediate);
 const api=Data.create({state,now:()=>time,frame:f=>frames.push(f),setTimer:(f,ms)=>{timers.set(++serial,{f,at:time+ms});return serial;},clearTimer:id=>timers.delete(id),read:url=>new Promise((resolve,reject)=>{
  const partial=url.includes('?ids='),list=partial?url.split('ids=')[1].split(','):ids;calls.push({at:time,partial,ids:list});pending.push({at:time+(partial?partialLatency:30),done:()=>failReads&&time>31?reject(Error('unreachable')):resolve({items:list.map(row),revision:1,groups:[],tags:[],exchange_rates:[]})});
 })});
 async function pump(){while(frames.length){frames.shift()();await tick();await tick();}}
 async function step(target){
  for(let count=0;;count++){
   if(count>10000)throw Error('timer loop did not settle');
   let key=null,at=Infinity;for(const [k,v]of timers)if(v.at<=target&&v.at<at){key=k;at=v.at;}
   let index=-1,readAt=Infinity;pending.forEach((v,i)=>{if(v.at<=target&&v.at<readAt){index=i;readAt=v.at;}});
   if(key===null&&index<0)break;
   if(index>=0&&readAt<=at){time=readAt;pending.splice(index,1)[0].done();await tick();await tick();}
   else{time=at;const timer=timers.get(key);timers.delete(key);timer.f();}
   await pump();
  }
  time=target;await pump();
 }
 try{
  const first=api.refresh();await step(31);await first;
  for(let t=50;t<=milliseconds;t+=50){await step(t);const id=ids[Math.floor(t/50)%200];state.event({agent_id:id,state:{cpu_percent:5}});api.telemetry(id);if(t%15000===0)void api.refresh();await pump();const s=Model.aggregate(state.agents,{version:'v1.0.10'},time);samples.push({at:t,network:s.quick.network,insufficient:s.insufficient,health:s.health});}
  return {samples,calls};
 }finally{api.close();Date.now=original;}
}
module.exports={simulate};
