/* One native dialog, independent of card remounts. History never follows SSE. */
(function(root) {
  'use strict';
  const H=root.ResourceHistory||(typeof require==='function'?require('./resource-history.js'):null);
  const C=root.NetworkQualityCore||(typeof require==='function'?require('./network-quality-core.js'):null);
  function create({document:doc=root.document,state=root.AgentState,agents,coordinator,fetcher=(...args)=>root.fetch(...args),
    unauthorized=()=>root.location.assign('/login'),Plot=root.uPlot,Resize=root.ResizeObserver,
    management=()=>null,identity=()=>null,hydrate=()=>{},network=()=>null}={}) {
    const dialog=doc.querySelector('#device-details'),owner={},panes={},tabs={};
    let current=null,tab='management',origin=null,scroll=null,overflow='',serial=0,loading=false,pending=false,dead=false,opened=false;
    let query={hours:1},data=null,error='',metric='traffic',plot=null,resize=null,tap=null,selected=0,networkIdentity=null,networkPending=false;
    const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
    const button=(text,action,cls='')=>{const n=el('button',cls,text);n.type='button';n.addEventListener('click',action);return n;};
    const header=el('header','details-header'),title=el('h2','','设备详情'),machine=el('div','details-machine');title.id='device-details-title';
    const heading=el('div','details-heading'),closeButton=button('关闭',()=>close(),'details-close');header.append(heading,closeButton);heading.append(title,machine);
    const tablist=el('div','details-tabs');tablist.setAttribute('role','tablist');tablist.setAttribute('aria-label','设备详情类别');
    const body=el('div','details-body');
    for(const [key,label]of [['management','设备与管理'],['resources','资源与流量'],['network','网络质量'],['security','安全观察']]){
      const b=button(label,()=>activate(key));b.id='details-tab-'+key;b.setAttribute('role','tab');b.setAttribute('aria-controls','details-pane-'+key);tabs[key]=b;tablist.append(b);
      const pane=el('section','details-pane');pane.id='details-pane-'+key;pane.setAttribute('role','tabpanel');pane.setAttribute('aria-labelledby',b.id);panes[key]=pane;body.append(pane);
      b.addEventListener('keydown',e=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();const keys=Object.keys(tabs),i=keys.indexOf(key),next=e.key==='Home'?0:e.key==='End'?3:(i+(e.key==='ArrowRight'?1:3))%4;activate(keys[next]);tabs[keys[next]].focus();});
    }
    dialog.replaceChildren(header,tablist,body);
    const controls=el('div','details-history-controls'),status=el('p','details-history-status'),note=el('p','details-note','持久化一分钟算术均值 · 当前及边缘分钟可能不完整；包含原首次/重置零速率，不证明完整或健康。与卡片近期内存观测不同。'),chart=el('div','details-resource-plot'),output=el('output','details-point');
    const hoursButtons=new Map();for(const hours of [1,6,24]){const b=button(hours+' 小时',()=>apply({hours}));hoursButtons.set(hours,b);controls.append(b);}
    controls.append(button('刷新',()=>request()));
    const metricSelect=el('select');metricSelect.setAttribute('aria-label','历史指标');for(const [key,spec]of Object.entries(H.metrics)){const option=el('option','',spec.label);option.value=key;metricSelect.append(option);}metricSelect.value=metric;metricSelect.addEventListener('change',()=>{metric=metricSelect.value;renderPlot();});controls.append(metricSelect);
    const custom=el('form','details-custom'),from=el('input'),to=el('input'),rangeError=el('span','details-range-error');
    from.type=to.type='datetime-local';from.step=to.step='1';from.setAttribute('aria-label','自定义开始时间');to.setAttribute('aria-label','自定义结束时间');
    const label=(text,input)=>{const n=el('label','',text);n.append(input);return n;};const applyButton=el('button','','应用自定义范围');applyButton.type='submit';custom.append(label('开始',from),label('结束',to),applyButton,rangeError);
    custom.addEventListener('submit',e=>{e.preventDefault();try{apply(H.range(from.value,to.value));rangeError.textContent='';}catch(err){rangeError.textContent=err.message;}});
    output.tabIndex=0;output.setAttribute('aria-label','分钟观测详情，左右方向键浏览');output.setAttribute('aria-live','polite');
    output.addEventListener('keydown',e=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();select(e.key==='Home'?0:e.key==='End'?(data?.points.length||1)-1:selected+(e.key==='ArrowLeft'?-1:1));});
    panes.resources.append(controls,custom,note,status,chart,output);
    function active(key,id=current){return !dead&&dialog.open&&!doc.hidden&&tab===key&&current===id&&agents.has(id);}
    function destroyPlot(){resize?.disconnect();resize=null;if(tap)plot?.over?.removeEventListener('pointerdown',tap);tap=null;plot?.destroy();plot=null;chart.replaceChildren();output.textContent='';}
    function release(){serial++;loading=false;coordinator.release(owner);destroyPlot();}
    function leave(){if(tab==='resources'){pending=loading;release();}if(tab==='network'){networkPending=false;network()?.releaseHistory();}}
    function restoreNetwork(){if(!active('network')||!networkIdentity)return;
      if(network()?.openHistoryFor(current,networkIdentity.target,networkIdentity.family,networkIdentity.kind,origin?.node)===false)
        panes.network.replaceChildren(el('p','','原指定网络目标已不可用；请从卡片选择当前目标，不借用其他地址族数据。'));}
    let titleIdentity = null, securityIdentity = null;
    function updateTitle(){const agent=agents.get(current), signature=JSON.stringify([current,agent?.country_code,agent?.country_source,agent?.name,agent?.state?.hostname]);if(signature===titleIdentity)return;titleIdentity=signature;machine.replaceChildren();const mark=identity(agent);if(mark)machine.append(mark);machine.append(el('span','',agent?.name||agent?.state?.hostname||'未命名设备'));}
    function update(agent){if(!dialog.open||agent?.agent_id!==current)return;updateTitle();if(tab==='management')renderManagement();if(tab==='security')renderSecurity();}
    function renderManagement(){const pane=panes.management,focused=pane.contains(doc.activeElement)?doc.activeElement?.dataset?.focusKey:null;
      const node=management(agents.get(current));if(node){node.hidden=false;if(node.parentNode!==pane)pane.replaceChildren(node);if(focused)pane.querySelector(`[data-focus-key="${focused}"]`)?.focus({preventScroll:true});}}
    function renderSecurity(){const security=agents.get(current)?.security,pane=panes.security,signature=JSON.stringify([current,security]);if(signature===securityIdentity)return;securityIdentity=signature;pane.replaceChildren();
      if(!security){pane.append(el('p','','安全资料尚未加载'),button('读取设备资料',()=>hydrate(current)));return;}
      if(!security.supported){pane.append(el('p','','当前 Agent 不支持安全观察（需要 v0.9 Agent）'));return;}
      const labels={unavailable:'需要配置',no_data:'等待首次本地审计',complete:'完整',partial:'部分结果',failed:'采集失败'};
      const statusLabel=security.status==='unavailable'&&security.reason==='platform_unsupported'?'此平台暂不支持（v1.1）':labels[security.status]||security.status;
      pane.append(el('p','',`${statusLabel}${security.stale?' · 旧数据':''}${security.reason?' · '+security.reason:''}`),el('p','details-note',`资料更新：${security.updated_at?new Date(security.updated_at).toLocaleString():'未知'} · 仅展示当前观察，不触发扫描`));
      const batch=security.current;if(!batch){pane.append(el('p','','暂无当前观察'));return;}
      pane.append(el('p','',`实际审计窗口：${new Date(batch.window_start).toLocaleString()} — ${new Date(batch.window_end).toLocaleString()} · ${batch.total_events} 条观察 · ${batch.tracked_sources} 个来源${security.delivery_gap?' · delivery gap（上次接收 '+new Date(security.previous_collected_at).toLocaleString()+'）':''}`));
      for(const source of (batch.sources||[]).slice(0,256)){const row=el('div','details-security-source');row.append(el('code','',source.ip),el('strong','',(source.classifications||[]).slice(0,16).join(' + ')),el('span','',`${source.count} 次 · ${new Date(source.first_seen).toLocaleString()} — ${new Date(source.last_seen).toLocaleString()}`));pane.append(row);}
      if(!(batch.sources||[]).length)pane.append(el('p','',batch.status==='complete'?'本窗口未观察到匹配事件':'没有可展示的完整来源数据'));
    }
    function activate(key,{restoreNetwork:shouldRestoreNetwork=true}={}){if(!Object.hasOwn(panes,key))return;if(tab!==key)leave();tab=key;
      for(const [name,pane]of Object.entries(panes)){pane.hidden=name!==tab;tabs[name].setAttribute('aria-selected',String(name===tab));tabs[name].tabIndex=name===tab?0:-1;}
      if(!dialog.open)return;
      if(tab==='management')renderManagement();else if(tab==='security'){renderSecurity();if(!agents.get(current)?.detail_loaded)hydrate(current);}
      else if(tab==='resources'){pending=false;renderPlot();request();}
      else if(shouldRestoreNetwork&&networkIdentity)restoreNetwork();
      else if(!networkIdentity)panes.network.replaceChildren(el('p','','请从设备卡片的指定网络/地址族延迟或比例进入历史。'));
    }
    function open(id,key='management',trigger=null,exact=null){if(dead||!agents.has(id))return;
      if(current!==id){leave();release();data=null;error='';query={hours:1};metric='traffic';metricSelect.value=metric;from.value=to.value='';rangeError.textContent='';networkIdentity=null;current=id;}
      if(trigger||current!==origin?.id)origin={node:trigger,id,key:trigger?.dataset?.focusKey||'manage'};
      if(!dialog.open){opened=true;scroll=[root.scrollX||0,root.scrollY||0];overflow=doc.body.style.overflow;doc.body.style.overflow='hidden';dialog.showModal();}
      if(exact)networkIdentity={...exact};updateTitle();activate(key,{restoreNetwork:!exact});closeButton.focus({preventScroll:true});
    }
    function close({restoreFocus=true}={}){if(!opened)return;opened=false;leave();release();pending=false;data=null;error='';if(dialog.open)dialog.close();doc.body.style.overflow=overflow;
      if(restoreFocus&&origin&&agents.has(origin.id)){const trigger=origin.node?.isConnected?origin.node:doc.querySelector(`.agent-card[data-agent-id="${origin.id}"] [data-focus-key="${origin.key}"]`);trigger?.focus({preventScroll:true});}
      if(scroll)root.scrollTo?.(...scroll);}
    function apply(next){release();query=next;data=null;error='';pending=false;renderPlot();request();}
    function statusText(){for(const [hours,b]of hoursButtons)b.setAttribute('aria-pressed',String(query.hours===hours));
      if(!data){status.textContent=error|| (loading?'正在读取指定分钟窗口…':'暂无分钟数据');return;}
      status.textContent=`${error?'读取失败 · 上次可信历史（非当前） · ':''}请求 ${new Date(data.from).toLocaleString()} — ${new Date(data.to).toLocaleString()} · 实际覆盖 ${data.points.length?new Date(data.oldest_timestamp).toLocaleString()+' — '+new Date(data.last_timestamp).toLocaleString():'无数据'} · 部分记录 · 一分钟算术均值 · 服务器时间 ${new Date(data.server_now).toLocaleString()}`;}
    async function request(){if(!active('resources')||loading)return;loading=true;pending=false;const generation=serial,id=current,wanted={...query};statusText();
      const params=new URLSearchParams(Object.hasOwn(wanted,'hours')?{hours:String(wanted.hours)}:{from:String(wanted.from),to:String(wanted.to)});
      try{const value=await coordinator.request(JSON.stringify(['resources',id,wanted]),owner,signal=>C.transportLease(tracked=>state.fetchJSON(`/api/v1/web/agents/${encodeURIComponent(id)}/resources/history?${params}`,{signal,fetcher:tracked,timeoutMs:10000,unauthorized}),H.boundedFetcher(fetcher)));
        if(generation!==serial||!active('resources',id))return;data=H.validate(value,id,wanted);error='';selected=Math.max(0,data.points.length-1);renderPlot();
      }catch(err){if(generation===serial&&active('resources',id)&&err.name!=='AbortError'){error='资源历史读取失败；未采用新响应';statusText();}}
      finally{if(generation===serial){loading=false;statusText();}}
    }
    function select(index){selected=Math.max(0,Math.min((data?.points.length||1)-1,index));const point=data?.points[selected],spec=H.metrics[metric];output.textContent=point?`${new Date(point.timestamp).toLocaleString()} · 一分钟算术均值 · ${spec.keys.map((key,i)=>spec.names[i]+' '+(spec.unit==='B/s'?root.TrafficChart?.bytes?.(point[key])||point[key]+' B/s':point[key].toFixed(2)+(spec.unit==='%'?'%':' load'))).join(' · ')}`:'';}
    function renderPlot(){destroyPlot();statusText();if(!active('resources')||!data)return;const adapted=H.columns(data,metric),measured=data.points.length;
      if(!measured||!Plot){chart.append(el('p','',!measured?'暂无可绘制分钟数据；不补零':'本地图表库未加载'));return;}
      const width=()=>Math.max(240,Math.floor(chart.getBoundingClientRect().width)),spec=adapted.spec;
      plot=new Plot({width:width(),height:300,legend:{show:true},cursor:{drag:{x:false,y:false}},scales:{x:{time:true},y:{auto:true}},
        axes:[{stroke:'#69727a',font:'10px system-ui',size:52,grid:{stroke:'#e7e1d7'}},{stroke:'#69727a',font:'10px system-ui',size:76,grid:{stroke:'#e7e1d7'},values:(_u,ticks)=>ticks.map(v=>spec.unit==='B/s'?(root.TrafficChart?.bytes?.(v)||v+' B/s').replace('.00 ',' '):(Math.abs(v)>=10000?v.toExponential(1):Number(v.toPrecision(4)))+(spec.unit==='%'?'%':' load'))}],
        series:[{},...spec.names.map((label,i)=>({label,stroke:['#168577','#9270ad','#bb8a37'][i],width:2,spanGaps:false,points:{show:true,size:4}}))],
        hooks:{setCursor:[u=>{const p=adapted.points[u.cursor.idx];if(p&&active('resources'))select(data.points.indexOf(p));}]}},adapted.data,chart);
      tap=e=>{if(!active('resources')||!['touch','pen'].includes(e.pointerType)||e.isPrimary===false)return;const rect=plot.over.getBoundingClientRect();if(rect.width<=0||e.clientX<rect.left||e.clientX>rect.right)return;const p=adapted.points[plot.posToIdx(e.clientX-rect.left)];if(p)select(data.points.indexOf(p));};plot.over?.addEventListener('pointerdown',tap);
      if(Resize){resize=new Resize(()=>{if(active('resources')&&plot)plot.setSize({width:width(),height:300});});resize.observe(chart);}select(selected);
    }
    function visibility(){if(doc.hidden){if(tab==='resources'){pending=loading;release();}if(dialog.open&&tab==='network'){networkPending=Boolean(networkIdentity);network()?.releaseHistory();}}
      else if(active('network')&&networkPending){networkPending=false;restoreNetwork();}
      else if(active('resources')&&pending){pending=false;request();}else if(active('resources'))renderPlot();}
    const cancel=e=>{e.preventDefault();close();},closed=()=>close();
    let backdropPress=false;
    const outside=e=>{const r=dialog.getBoundingClientRect();return e.target===dialog&&(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom);};
    const pointerDown=e=>{backdropPress=e.button===0&&e.isPrimary!==false&&outside(e);};
    const pointerCancel=()=>{backdropPress=false;};
    const backdropClick=e=>{const dismiss=backdropPress&&outside(e);backdropPress=false;if(dismiss)close();};
    dialog.addEventListener('pointerdown',pointerDown);dialog.addEventListener('pointercancel',pointerCancel);dialog.addEventListener('click',backdropClick);
    dialog.addEventListener('cancel',cancel);dialog.addEventListener('close',closed);doc.addEventListener('visibilitychange',visibility);
    return {open,close,update,active,networkContainer:()=>panes.network,
      sync(){if(current&&!agents.has(current)){close({restoreFocus:false});current=null;networkIdentity=null;data=null;}},
      shutdown(){close({restoreFocus:false});dead=true;release();network()?.releaseHistory();doc.removeEventListener('visibilitychange',visibility);dialog.removeEventListener('cancel',cancel);dialog.removeEventListener('close',closed);dialog.removeEventListener('pointerdown',pointerDown);dialog.removeEventListener('pointercancel',pointerCancel);dialog.removeEventListener('click',backdropClick);current=null;data=null;},
      snapshot:()=>({id:current,tab,open:dialog.open,loading,points:data?.points.length||0,plot:!!plot,query:{...query},draft:{from:from.value,to:to.value}})};
  }
  const api={create};if(typeof module==='object'&&module.exports)module.exports=api;else root.DeviceDetails=api;
})(globalThis);
