/* Bounded read-only minute history adapter. No timer, persistence or mutation. */
(function(root) {
  'use strict';
  const MAX_BYTES=1<<20,MAX_POINTS=1441,MINUTE=60000,DAY=86400000;
  const fields=['cpu','ram_percent','swap_percent','disk_percent','load1','load5','load15','rx_rate','tx_rate'];
  const metrics={traffic:{label:'下载 / 上传',keys:['rx_rate','tx_rate'],names:['↓ 下载','↑ 上传'],unit:'B/s'},
    cpu:{label:'CPU',keys:['cpu'],names:['CPU'],unit:'%'},memory:{label:'RAM / Swap',keys:['ram_percent','swap_percent'],names:['RAM','Swap'],unit:'%'},
    disk:{label:'磁盘使用',keys:['disk_percent'],names:['磁盘使用'],unit:'%'},load:{label:'Load 1 / 5 / 15',keys:['load1','load5','load15'],names:['Load 1','Load 5','Load 15'],unit:'load'}};
  const abortError=()=>Object.assign(new Error('Resource history read cancelled'),{name:'AbortError'});
  const safe=n=>Number.isSafeInteger(n)&&n>=0&&n<=8640000000000000;
  // Wrapped on the fetcher side of transportLease so the lease observes json()
  // settlement, including cancellation, rather than only response headers.
  function boundedFetcher(fetcher) {
    return async (url,init={})=>{
      const response=await fetcher(url,init),signal=init.signal;
      if(signal?.aborted){await response.body?.cancel?.();throw abortError();}
      let work;
      return {ok:response.ok,status:response.status,statusText:response.statusText,body:response.body,
        json(){return work||=(async()=>{
          if(!response.body||typeof response.body.getReader!=='function')throw Error('ReadableStream body required');
          const reader=response.body.getReader(),chunks=[];let size=0,complete=false,cancellation=null;
          const cancel=()=>{if(!cancellation)cancellation=Promise.resolve().then(()=>reader.cancel()).catch(()=>{});return cancellation;};
          const onabort=()=>{void cancel();};signal?.addEventListener('abort',onabort,{once:true});
          try {
            if(signal?.aborted)throw abortError();
            for(;;){const part=await reader.read();if(signal?.aborted)throw abortError();if(part.done){complete=true;break;}
              if(!(part.value instanceof Uint8Array))throw Error('Invalid response byte chunk');
              size+=part.value.byteLength;if(size>MAX_BYTES)throw Error('Resource history exceeds 1MiB');if(part.value.byteLength)chunks.push(part.value);
            }
            const bytes=new Uint8Array(size);let offset=0;for(const part of chunks){bytes.set(part,offset);offset+=part.byteLength;}
            if(signal?.aborted)throw abortError();
            return JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(bytes));
          } finally {
            signal?.removeEventListener('abort',onabort);
            if(!complete||cancellation)await cancel();
            reader.releaseLock();
          }
        })();}};
    };
  }
  function validate(value,id,query={hours:1}) {
    const fail=()=>{throw Error('资源历史格式无效，未采用');};
    if(!value||value.agent_id!==id||!safe(value.server_now)||!safe(value.from)||!safe(value.to)||value.to<=value.from||value.to-value.from>DAY||
      value.from<value.server_now-30*DAY||value.to>value.server_now||value.bucket_from!==Math.floor(value.from/MINUTE)*MINUTE||value.bucket_to!==Math.floor(value.to/MINUTE)*MINUTE||
      value.interval_ms!==MINUTE||value.source!=='minute_metrics'||value.aggregation!=='arithmetic_mean'||value.retention_days!==30||value.coverage!=='partial'||
      value.point_limit!==MAX_POINTS||!Array.isArray(value.points)||value.points.length>MAX_POINTS)fail();
    if(Object.hasOwn(query,'hours')){if(!Number.isInteger(query.hours)||query.hours<1||query.hours>24||value.to!==value.server_now||value.from!==value.server_now-query.hours*3600000)fail();}
    else if(value.from!==query.from||value.to!==query.to)fail();
    let previous=-1;
    const points=value.points.map(point=>{
      if(!point||!safe(point.timestamp)||point.timestamp%MINUTE!==0||point.timestamp<=previous||point.timestamp<value.bucket_from||point.timestamp>value.bucket_to)fail();
      previous=point.timestamp;const out={timestamp:point.timestamp};
      for(const key of fields){if(typeof point[key]!=='number'||!Number.isFinite(point[key])||point[key]<0)fail();out[key]=point[key];}return out;
    });
    if(value.oldest_timestamp!==(points[0]?.timestamp??null)||value.last_timestamp!==(points.at(-1)?.timestamp??null))fail();
    return {agent_id:id,server_now:value.server_now,from:value.from,to:value.to,bucket_from:value.bucket_from,bucket_to:value.bucket_to,
      interval_ms:MINUTE,source:value.source,aggregation:value.aggregation,coverage:value.coverage,retention_days:30,point_limit:MAX_POINTS,
      oldest_timestamp:value.oldest_timestamp,last_timestamp:value.last_timestamp,points};
  }
  function columns(value,metric='traffic') {
    const spec=metrics[metric];if(!spec)throw Error('Unknown resource metric');
    const points=[],data=Array.from({length:spec.keys.length+1},()=>[]);let previous=null;
    for(const point of value.points){
      if(previous!==null&&point.timestamp-previous>MINUTE){data[0].push((previous+MINUTE)/1000);for(const col of data.slice(1))col.push(null);points.push(null);}
      data[0].push(point.timestamp/1000);spec.keys.forEach((key,i)=>data[i+1].push(point[key]));points.push(point);previous=point.timestamp;
    }
    return {data,points,spec};
  }
  function range(from,to,now=Date.now()) {
    const start=new Date(from).getTime(),end=new Date(to).getTime();
    if(!safe(start)||!safe(end)||end<=start||end-start>DAY||start<now-30*DAY||end>now)throw Error('选择最近30天内、最长24小时的有效起止时间');
    return {from:start,to:end};
  }
  const api={MAX_BYTES,MAX_POINTS,MINUTE,metrics,boundedFetcher,validate,columns,range};
  if(typeof module==='object'&&module.exports)module.exports=api;else root.ResourceHistory=api;
})(globalThis);
