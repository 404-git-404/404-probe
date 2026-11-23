(function(root){
  'use strict';
  const charts=root.NetworkStatusCharts;
  const HOUR=3600000, DAY=24*HOUR, BEIJING=8*HOUR;
  const windows={'1h':HOUR,'24h':DAY,'7d':7*DAY,'30d':30*DAY};
  const labels={normal:'正常',slow:'偏慢或失败多',alert:'告警',no_data:'没数据',offline:'离线',paused:'暂停'};
  const reasonLabels={dns_error:'DNS',dns_timeout:'DNS 超时',not_found:'域名不存在',no_address:'无解析地址',unsafe_address:'非公网地址',icmp_error:'ICMP 错误',timeout:'超时',refused:'拒绝',network_error:'网络错误',
    connect_timeout:'连接超时',connect_refused:'连接被拒绝',icmp_timeout:'回包超时',no_route:'无路由',
    unsupported:'不支持',internal_error:'内部错误',target_abnormal:'目标异常',local_error:'本机错误'};
  const escape=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const number=value=>Number.isFinite(value)?(Math.abs(value)>=100?value.toFixed(0):Number(value.toFixed(1)).toString()):'—';
  const ms=value=>value==null?'—':number(value)+' ms';
  const percent=value=>value==null?'—':number(value)+'%';
  const dateTime=at=>new Date(at+BEIJING).toISOString().slice(5,16).replace('T',' ');
  const inputTime=at=>new Date(at+BEIJING).toISOString().slice(0,16);
  const parseInput=value=>Date.parse(value+'Z')-BEIJING;
  const symbol=status=>'<i class="net-dot" data-tone="'+status+'" aria-hidden="true"></i>';
  const badge=machine=>machine.server_local?'<small class="net-badge">Server</small>':'';
  const pill=(status,text)=>'<span class="net-pill" data-tone="'+status+'">'+escape(text||labels[status])+'</span>';
  const cell=(value,cls='net-number')=>'<td class="'+cls+'">'+value+'</td>';
  const title=(text,aside='')=>'<header class="net-heading"><h2>'+text+'</h2><span class="net-muted">'+aside+'</span></header>';
  const swatch=color=>'<i class="net-swatch" style="--net-line:'+color+'" aria-hidden="true"></i>';
  const columns=headers=>'<thead><tr>'+headers.map(head=>'<th scope="col">'+escape(head)+'</th>').join('')+'</tr></thead>';
  const table=(headers,rows,extra='')=>'<div class="net-table-scroll"><table class="net-table '+extra+'">'+columns(headers)+'<tbody>'+rows+'</tbody></table></div>';
  const endpoint=(id,suffix)=>'/api/v1/web/agents/'+encodeURIComponent(id)+'/network-quality/'+suffix;
  const lossKey=proto=>proto==='icmp'?'packet_loss_percent':'failure_percent';
  const lossName=proto=>proto==='icmp'?'丢包':'失败率';
  const targetLabel=(object,i)=>object.region||object.isp?root.NetworkQualityCore.cardLabel({slots:[object]},object.slot):object.host||'对象 '+(i+1);
  const tone=(status)=>Object.hasOwn(labels,status)?status:'no_data';
  const machineTone=m=>m.status==='online'?(m.current_alerts?'alert':'normal'):tone(m.status);
  function duration(value){
    const minutes=Math.max(0,Math.floor(value/60000));
    const days=Math.floor(minutes/1440),hours=Math.floor(minutes%1440/60);
    if(days)return days+' 天'+(hours?' '+hours+' 小时':'');
    if(hours)return hours+' 小时'+(minutes%60?' '+minutes%60+' 分':'');
    return minutes?minutes+' 分钟':Math.floor(Math.max(0,value)/1000)+' 秒';
  }
  function segment(key,label,options,selected){
    return '<div class="net-segment" role="group" aria-label="'+label+'">'+options.map(([k,v])=>
      '<button type="button" data-net-control="'+key+'" data-net-value="'+k+'" aria-pressed="'+(k===selected)+'">'+v+'</button>').join('')+'</div>';
  }
  function targetText(object,protocol){
    if(!object.host)return '—';
    if(protocol==='icmp'||!object.port)return escape(object.host);
    return escape((object.host.includes(':')?'['+object.host+']':object.host)+':'+object.port);
  }
  function info(label,value){
    return '<div><span>'+label+'</span><strong>'+value+'</strong></div>';
  }
  function create({host,nav,read,select}){
    const state={agent:null,window:'24h',family:'ipv4',protocol:'tcp',metric:'p50_ms',chart:'latency',smooth:false,
      customOpen:false,custom:null,zoom:null,hidden:new Set(),heatSlot:null,heatMetric:'p50_ms'};
    const fleetURL='/api/v1/web/network-quality/fleet';
    const events=new AbortController();
    let pageRead,heatRead,disposeCharts,lastResult,heatResult,heatError='',loadError='',customError='';
    let summaries=[],agents=[],navKey='',clockOffset=0,loading=false;
    const now=()=>Date.now()+clockOffset;
    const heatDays=()=>state.window==='30d'&&!state.custom?30:7;
    const currentRange=()=>state.zoom||state.custom;
    const baseParams=()=>({family:state.family,protocol:state.protocol});
    function timeParams(){
      const range=currentRange();
      return range?{start:range[0],end:range[1]}:{window:state.window};
    }
    const query=params=>new URLSearchParams(params).toString();
    function renderNav(){
      if(!nav)return;
      const fallback=new Map(summaries.map(m=>[m.agent_id,m]));
      const list=agents.length?agents.map(m=>({...fallback.get(m.agent_id),...m})):summaries;
      list.sort((a,b)=>a.name.localeCompare(b.name,'zh-CN'));
      const key=JSON.stringify([state.agent,list.map(m=>[m.agent_id,m.name,m.status,m.server_local,m.current_alerts])]);
      if(key===navKey)return;
      navKey=key;
      nav.innerHTML=list.map(m=>'<button type="button" class="panel-item net-machine-nav" data-net-agent="'+escape(m.agent_id)+'"'+
        (state.agent===m.agent_id?' aria-current="page"':'')+'>'+symbol(machineTone(m))+'<span>'+escape(m.name)+'</span>'+badge(m)+'</button>').join('');
    }
    function updateAgents(map){
      agents=[...map.values()].filter(a=>!a.revoked).map(a=>({agent_id:a.agent_id,name:a.name||a.agent_id,server_local:!!a.server_local,status:root.DashboardModel.lifecycle(a)}));
      renderNav();
    }
    function show(id){
      if(id===state.agent)return;
      state.agent=id;
      state.zoom=null;state.custom=null;state.customOpen=false;state.hidden.clear();state.heatSlot=null;lastResult=null;
      renderNav();
      if(id==null){pageRead?.abort();heatRead?.abort();disposeCharts?.();disposeCharts=null;return;}
      void load();
    }
    function controls(){
      const win=segment('window','时间范围',[['1h','1 小时'],['24h','24 小时'],['7d','7 天'],['30d','30 天'],['custom','自定义…']],state.customOpen?'custom':state.window);
      const family=segment('family','地址族',[['ipv4','IPv4'],['ipv6','IPv6']],state.family);
      const proto=segment('protocol','协议',[['tcp','TCP'],['icmp','ICMP']],state.protocol);
      const detail=state.agent?segment('metric','延迟统计',[['p50_ms','p50'],['p95_ms','p95'],['mean_ms','平均']],state.metric)+
        '<label class="net-smooth"><input type="checkbox" data-net-smooth'+(state.smooth?' checked':'')+'> 平滑</label>':'';
      let custom='';
      if(state.customOpen){
        const at=now(),range=state.custom||[lastResult?.start??at-windows[state.window],lastResult?.end??at];
        custom='<form class="net-custom"><label>从 <input name="start" type="datetime-local" required value="'+inputTime(range[0])+'"></label>'+
          '<label>到 <input name="end" type="datetime-local" required value="'+inputTime(range[1])+'"></label><button type="submit">应用</button>'+
          '<small class="net-muted">北京时间，最多 30 天前。分辨率按起点距今自动选择。</small><span role="alert" class="net-custom-error">'+escape(customError)+'</span></form>';
      }
      return '<div class="net-controls">'+win+family+proto+detail+'</div>'+custom;
    }
    function renderFleet(result){
      const {machines}=result,count=Math.max(0,...machines.map(m=>m.objects.length));
      const objects=Array.from({length:count},(_,i)=>{
        const names=new Set(machines.map(m=>m.objects[i]).filter(Boolean).map(o=>targetLabel(o,i)));
        return names.size===1?[...names][0]:null;
      });
      const loss=lossName(state.protocol),key=lossKey(state.protocol);
      const rows=machines.map(machine=>{
        const lines=new Map(machine.lines.map(line=>[line.slot,line]));
        const quality=objects.map((name,i)=>{
          const object=machine.objects[i],line=object&&lines.get(object.slot);
          if(!line)return cell('—');
          return '<td><div class="net-cell" data-tone="'+tone(line.status)+'"><div>'+
            (name?'':'<small class="net-object-name">'+escape(targetLabel(object,i))+'</small>')+
            '<strong>'+ms(line.stats.p50_ms)+'</strong><small>抖动 '+number(line.stats.jitter_ms)+' · '+loss+' '+percent(line.stats[key])+
            '</small></div>'+charts.spark(line.spark)+'</div></td>';
        }).join('');
        const alert=machine.lines.find(line=>line.alert);
        const object=alert&&machine.objects.find(o=>o.slot===alert.slot);
        const status=machineTone(machine);
        const description=status==='alert'?(alert?'告警 · '+targetLabel(object,machine.objects.indexOf(object))+' · 已持续 '+duration(alert.alert.duration_ms):'告警 · '+machine.current_alerts+' 条'):
          status==='offline'&&machine.offline_since?'离线 '+duration(result.now-machine.offline_since):labels[status];
        const id=escape(machine.agent_id);
        return '<tr data-net-agent="'+id+'"><th scope="row"><button type="button" class="net-machine-link" data-net-agent="'+id+'">'+
          symbol(status)+escape(machine.name)+badge(machine)+'</button></th>'+quality+cell(pill(status,description),'')+cell(machine.alerts_30d||'—')+'</tr>';
      }).join('');
      return title('网络状态','全部 '+machines.length+' 台 · 每格显示所选时间的 p50 延迟')+controls()+
        (machines.length?table(['机器',...objects.map((name,i)=>name||'位置 '+(i+1)),'状态','30 天告警'],rows,'net-fleet'):'<p class="net-empty">尚无活动 Agent。</p>')+
        '<p class="net-note">颜色：正常 / 偏慢或失败多（黄）/ 告警（红）/ 没数据（灰）。点一行进入这台机器。</p>';
    }
    function objectLines(result){
      const machine=result.machines[0],histories=result.histories;
      return machine.objects.map((object,i)=>({object,name:targetLabel(object,i),color:charts.colors[i],hidden:state.hidden.has(object.slot),
        line:machine.lines.find(line=>line.slot===object.slot),history:histories[i],points:histories[i]?.points||[],
        changes:histories[i]?.target_changes||[]}));
    }
    function renderMachine(result){
      const machine=result.machines[0], loss=lossName(state.protocol),key=lossKey(state.protocol),lines=objectLines(result);
      const lastData=machine.last_seen??Math.max(0,...machine.lines.map(line=>line.latest_bucket_at??0));
      const seen=lastData?dateTime(lastData)+'（北京时间）':'—';
      const skew=machine.quality_clock_skewed?' <small class="net-warn" title="Server 接收时间和 Agent 样本结束时间相差超过 1 分钟">Agent 时钟偏差超过 1 分钟</small>':'';
      const strip='<div class="net-strip">'+info('出口地区',escape(machine.country_code||'—'))+
        info('监测对象',lines.length+' 个 · '+(state.family==='ipv6'?'IPv6':'IPv4')+' · '+state.protocol.toUpperCase())+
        info('当前告警',machine.current_alerts?'<span class="net-danger">'+machine.current_alerts+'</span>':'0')+
        info('30 天告警',machine.alerts_30d+' 次')+info('慢门槛',machine.slow_threshold_ms+' ms')+
        info('最近数据',seen+(machine.status==='paused'?'（已暂停）':'')+skew)+
        info('网络评分','<button type="button" class="score-network-badge">数据不足</button>')+'</div>';
      const rows=lines.map(({object,name,color,line,history})=>{
        const stats=line?.stats||{},evening=line?.evening_p50_ms,delta=evening!=null&&stats.p50_ms>0?(evening/stats.p50_ms-1)*100:null;
        const comparison=delta==null?'':' <small'+(delta>25?' class="net-warn"':'')+'>'+(delta>=0?'+':'')+Math.round(delta)+'%</small>';
        const changed=history?.last_target_change_at,days=changed==null?null:Math.floor((result.now-changed)/DAY);
        let status=tone(line?.status),text=status==='alert'?'告警 · 已持续 '+duration(line.alert?.duration_ms||0):labels[status];
        if(machine.status==='paused'){status='paused';text='暂停';}
        return '<tr><th scope="row">'+swatch(color)+escape(name)+(days==null?'':'<small class="net-changed">'+(days<1?'今天':days+' 天前')+'换过目标</small>')+
          '</th>'+cell(targetText(object,state.protocol),'net-address')+cell(number(line?.latest_p50_ms))+cell(number(stats.p50_ms))+
          cell(number(stats.p95_ms))+cell(number(stats.jitter_ms))+cell('<strong>'+number(evening)+'</strong>'+comparison)+
          cell(percent(stats[key]))+cell(pill(status,text),'')+'</tr>';
      }).join('');
      const tableHTML=lines.length?table(['名称','目标','最近','p50','p95','抖动','晚高峰 p50',loss,'状态'],rows):'<p class="net-empty">这台机器还没有监测对象。</p>';
      const note='<p class="net-note">单位 ms。抖动：TCP = 相邻两次连接耗时之差，ICMP = 同一次探测内相邻回包之差，两者不要相比。晚高峰 = 北京时间 20:00–23:00。</p>';
      return title(escape(machine.name)+badge(machine),'网络状态')+controls()+strip+tableHTML+note+
        (lines.length?chartSection(lines,loss)+heatSection(lines):'')+alertSection(result,lines);
    }
    function chips(lines,heat){
      return '<div class="net-chips">'+lines.map(line=>{
        const pressed=heat?state.heatSlot===line.object.slot:!line.hidden;
        return '<button type="button" class="net-chip" aria-pressed="'+pressed+'" data-net-'+(heat?'heat':'legend')+'="'+escape(line.object.slot)+'">'+
          swatch(line.color)+escape(line.name)+'</button>';
      }).join('')+'</div>';
    }
    function chartSection(lines,loss){
      const resolution=lines.find(line=>line.history)?.history.resolution_ms;
      return '<section class="net-panel net-charts"><header class="net-panel-head">'+segment('chart','上图指标',[['latency','延迟'],['jitter','抖动']],state.chart)+chips(lines,false)+
        '<span class="net-spacer"></span><small class="net-muted">分辨率 '+charts.resolutionLabel(resolution)+' · 拖动放大</small>'+
        (state.zoom?'<button type="button" data-net-reset>重置</button>':'')+'</header>'+
        '<div class="net-chart" tabindex="0" role="group" aria-label="延迟或抖动图，方向键查看数据"></div>'+
        '<header class="net-panel-head"><h3>'+loss+'</h3></header><div class="net-chart" tabindex="0" role="group" aria-label="'+loss+'图，方向键查看数据"></div>'+
        '<p class="net-note">竖虚线 = 换过目标；灰色竖条 = 没有数据；悬停看 p50 / p95 / 抖动 / '+loss+' / 失败原因。</p></section>';
    }
    function heatSection(lines){
      return '<section class="net-panel" aria-label="晚高峰热力图"><header class="net-panel-head"><h3>晚高峰 · 时段 × 日期</h3>'+
        chips(lines,true)+segment('heatMetric','热力图指标',[['p50_ms','延迟 p50'],[lossKey(state.protocol),lossName(state.protocol)]],state.heatMetric)+
        '<span class="net-spacer"></span><small class="net-muted">最近 '+heatDays()+' 天 · 北京时间</small></header><div class="net-heat-content">'+heatBody()+'</div></section>';
    }
    function heatBody(){
      if(heatError)return '<p class="net-empty" role="status">热力图读取失败 <button type="button" data-net-heat-retry>重试</button></p>';
      if(!heatResult)return '<p class="net-empty" role="status">正在读取热力图…</p>';
      const key=state.heatMetric,fmt=key==='p50_ms'?ms:percent;
      const values=heatResult.days.flatMap(day=>day.hours.map(h=>h[key])).filter(v=>v!=null);
      const min=values.length?Math.min(...values):null,max=values.length?Math.max(...values):null;
      const evening=hour=>hour>=20&&hour<23?' net-heat-evening':'';
      const hours=Array.from({length:24},(_,i)=>'<span class="net-heat-hour'+evening(i)+'">'+(i%3===0||i===20||i===22?i:'')+'</span>').join('');
      const days=heatResult.days.map((day,i)=>'<span class="net-heat-day">'+escape(day.date.slice(5))+'</span>'+
        day.hours.map((hour,j)=>{
          const value=hour[key],strength=value==null?null:Math.round(100*(value-min)/(max-min||1));
          const hint=day.date+' '+String(j).padStart(2,'0')+':00 · '+(value==null?'没有数据':fmt(value));
          return '<span class="net-heat-cell'+evening(j)+(i===heatResult.days.length-1?' net-heat-last':'')+'" title="'+escape(hint)+'"'+
            (strength==null?' data-empty="true"':' style="--heat:'+strength+'%"')+'></span>';
        }).join('')).join('');
      return '<div class="net-heat-scroll"><div class="net-heat"><span></span>'+hours+days+'</div></div>'+
        '<div class="net-heat-legend"><span>'+fmt(min)+'</span><i aria-hidden="true"></i><span>'+fmt(max)+'</span>'+
        '<span>框出的三列是 20:00–23:00，灰格表示没有数据。</span></div>';
    }
    function alertSection(result,lines){
      const bySlot=new Map(lines.map(line=>[line.object.slot,line])),alerts=result.alerts;
      const rows=alerts.map(alert=>{
        const line=bySlot.get(alert.slot),name=line?.name||({telecom:'电信',unicom:'联通',mobile:'移动'}[alert.slot]||'已删除的对象');
        const worst=[alert.worst_p95_ms==null?'':'p95 '+ms(alert.worst_p95_ms),alert.worst_failure_percent==null?'':'失败 '+percent(alert.worst_failure_percent),
          alert.worst_packet_loss_percent==null?'':'丢包 '+percent(alert.worst_packet_loss_percent)].filter(Boolean).join(' · ');
        return '<tr><th scope="row">'+(line?swatch(line.color):'')+escape(name)+' <small class="net-muted">'+alert.family.toUpperCase().replace('IPV','IPv')+' · '+alert.protocol.toUpperCase()+'</small></th>'+
          cell(dateTime(alert.start))+cell(alert.end==null?pill('alert','进行中'):dateTime(alert.end))+cell(duration(alert.duration_ms))+cell(worst||'—','net-muted')+'</tr>';
      }).join('');
      return '<section class="net-panel net-alerts"><header class="net-panel-head"><h3>告警记录 · 最近 30 天</h3><span class="net-spacer"></span><small class="net-muted">'+
        alerts.length+' 次 · 北京时间</small></header>'+(rows?table(['线路','开始','结束','持续','最差时'],rows):'<p class="net-empty">最近 30 天没有告警。</p>')+'</section>';
    }
    function tooltip(lines,at,marks){
      const key=lossKey(state.protocol),loss=lossName(state.protocol);
      return '<strong>'+dateTime(at)+'（北京时间）</strong>'+(marks.length?'<small class="net-warn">'+marks.map(escape).join('、')+' 换过目标</small>':'')+
        lines.map(line=>{
          const p=line.points.find(p=>p.start<=at&&p.end>at);
          const heading='<b>'+swatch(line.color)+escape(line.name)+'</b>';
          if(!p?.samples)return '<div>'+heading+' <small>没有数据</small></div>';
          const reasons=Object.entries(p.categories||{}).filter(([k,n])=>n>0&&!['success','ok'].includes(k))
            .map(([k,n])=>escape(reasonLabels[k]||k)+' '+n).join(' · ');
          return '<div>'+heading+'<small>p50 '+ms(p.p50_ms)+' · p95 '+ms(p.p95_ms)+' · 抖动 '+ms(p.jitter_ms)+' · '+loss+' '+percent(p[key])+'</small>'+
            (reasons?'<small>'+reasons+'</small>':'')+'</div>';
        }).join('');
    }
    function updateScore(){
      const badge=host.querySelector('.score-network-badge');
      if(badge && lastResult?.machines[0] && root.DeviceScores){
        root.DeviceScores.networkBadge(badge,root.DeviceScores.forAgent(lastResult.machines[0]));
      }
    }
    function repaint(){
      disposeCharts?.();disposeCharts=null;
      if(state.agent==null)return;
      if(loading||loadError||!lastResult){
        const name=agents.find(m=>m.agent_id===state.agent)?.name||summaries.find(m=>m.agent_id===state.agent)?.name;
        host.innerHTML=title(escape(name||'网络状态'),name?'网络状态':'')+controls()+
          (loadError?'<p class="net-empty" role="alert">读取网络状态失败 <button type="button" data-net-retry>重试</button></p>':'<p class="net-empty" role="status">正在读取网络状态…</p>');
        return;
      }
      host.innerHTML=state.agent?renderMachine(lastResult):renderFleet(lastResult);
      if(state.agent)updateScore();
      const boxes=[...host.querySelectorAll('.net-chart')];
      if(boxes.length)disposeCharts=charts.attach(boxes,{start:lastResult.start,end:lastResult.end,lines:objectLines(lastResult),
        metric:state.chart==='jitter'?'jitter_ms':state.metric,lossKey:lossKey(state.protocol),smooth:state.smooth,
        threshold:lastResult.machines[0].slow_threshold_ms,onZoom:range=>{state.zoom=range;void load();},tooltip});
    }
    async function load(){
      pageRead?.abort();heatRead?.abort();
      const request=pageRead=new AbortController(),signal=request.signal,id=state.agent,params={...baseParams(),...timeParams()};
      loading=true;loadError='';heatResult=null;heatError='';repaint();
      try{
        const result=await read(fleetURL+'?'+query({...params,...(id?{agent_id:id}:{view:'card'})}),signal);
        if(request!==pageRead||signal.aborted)return;
        clockOffset=result.now-Date.now();
        if(id){
          const machine=result.machines[0];
          if(!machine)throw Error('找不到这台机器');
          const objects=machine.objects;
          for(const slot of state.hidden)if(!objects.some(o=>o.slot===slot))state.hidden.delete(slot);
          if(state.hidden.size>=objects.length)state.hidden.clear();
          if(!objects.some(o=>o.slot===state.heatSlot))state.heatSlot=objects[0]?.slot||null;
          void loadHeat();
          const range=timeParams();
          const lines=objects.map(o=>read(endpoint(id,'range')+'?'+query({...baseParams(),...range,slot:o.slot}),signal));
          const [alerts,...histories]=await Promise.all([read(endpoint(id,'alerts'),signal),...lines]);
          result.alerts=alerts;result.histories=histories;
        }else{
          summaries=result.machines;renderNav();
        }
        if(request!==pageRead||signal.aborted)return;
        lastResult=result;loading=false;repaint();
      }catch(error){
        if(request!==pageRead||signal.aborted)return;
        loading=false;loadError=error.message||'请求失败';repaint();
      }
    }
    function paintHeat(){
      const heat=host.querySelector('.net-heat-content');
      if(heat)heat.innerHTML=heatBody();
    }
    async function loadHeat(){
      heatRead?.abort();
      if(!state.heatSlot)return;
      const request=heatRead=new AbortController(),signal=request.signal;
      heatResult=null;heatError='';paintHeat();
      try{
        const data=await read(endpoint(state.agent,'heatmap')+'?'+query({...baseParams(),slot:state.heatSlot,days:heatDays()}),signal);
        if(signal.aborted||request!==heatRead)return;
        heatResult=data;paintHeat();
      }catch(error){
        if(signal.aborted||request!==heatRead)return;
        heatError=error.message||'请求失败';paintHeat();
      }
    }
    function handleClick(e){
      const destination=e.target.closest('[data-net-agent]');
      if(destination){select(destination.dataset.netAgent);return;}
      const control=e.target.closest('[data-net-control]');
      if(control){
        const key=control.dataset.netControl,value=control.dataset.netValue;
        if(key==='window'){
          customError='';
          if(value==='custom'){state.customOpen=true;repaint();host.querySelector('.net-custom input')?.focus();return;}
          state.customOpen=false;state.custom=null;state.zoom=null;
        }
        state[key]=value;
        if(key==='protocol'&&state.heatMetric!=='p50_ms')state.heatMetric=lossKey(state.protocol);
        if(['metric','chart','heatMetric'].includes(key)){repaint();return;}
        void load();return;
      }
      const legend=e.target.closest('[data-net-legend]');
      if(legend){
        const slot=legend.dataset.netLegend;
        if(state.hidden.has(slot))state.hidden.delete(slot);
        else if(state.hidden.size+1<lastResult.machines[0].objects.length)state.hidden.add(slot);
        else return;
        repaint();host.querySelector('[data-net-legend="'+slot+'"]')?.focus();return;
      }
      const heat=e.target.closest('[data-net-heat]');
      if(heat){
        state.heatSlot=heat.dataset.netHeat;
        for(const button of host.querySelectorAll('[data-net-heat]'))button.setAttribute('aria-pressed',String(button===heat));
        void loadHeat();return;
      }
      if(e.target.closest('[data-net-heat-retry]')){void loadHeat();return;}
      if(e.target.closest('[data-net-reset]')){state.zoom=null;void load();return;}
      if(e.target.closest('[data-net-retry]'))void load();
    }
    host.addEventListener('click',handleClick,{signal:events.signal});
    host.ownerDocument.addEventListener('device-scores-changed',updateScore,{signal:events.signal});
    nav?.addEventListener('click',handleClick,{signal:events.signal});
    host.addEventListener('change',e=>{
      if(e.target.matches('[data-net-smooth]')){state.smooth=e.target.checked;repaint();host.querySelector('[data-net-smooth]')?.focus();}
    },{signal:events.signal});
    host.addEventListener('submit',e=>{
      if(!e.target.matches('.net-custom'))return;
      e.preventDefault();
      const at=now(),start=parseInput(e.target.elements.start.value),end=parseInput(e.target.elements.end.value);
      if(!Number.isFinite(start)||!Number.isFinite(end)||start>=end||end>at||start<at-30*DAY-60000){
        customError='请选择最近 30 天中有效的起止时间。';
        e.target.querySelector('.net-custom-error').textContent=customError;
        return;
      }
      customError='';state.custom=[Math.max(start,at-30*DAY+3000),end];state.zoom=null;void load();
    },{signal:events.signal});
    return {show,updateAgents,close(){show(null);events.abort();}};
  }
  root.NetworkStatus={create};
})(globalThis);
