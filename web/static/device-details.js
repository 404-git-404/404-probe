/* One native dialog, independent of card remounts. History never follows SSE. */
(function(root) {
  'use strict';
  const H=root.ResourceHistory||(typeof require==='function'?require('./resource-history.js'):null);
  const C=root.NetworkQualityCore||(typeof require==='function'?require('./network-quality-core.js'):null);
  function create({document:doc=root.document,state=root.AgentState,agents,coordinator,fetcher=(...args)=>root.fetch(...args),
    unauthorized=()=>root.location.assign('/login'),Plot=root.uPlot,Resize=root.ResizeObserver,
    management=()=>null,overview=()=>null,upgradeTargets=()=>{},resourceFacts=()=>[],identity=()=>null,hydrate=()=>{},network=()=>null,conceal=()=>{}}={}) {
    const dialog=doc.querySelector('#device-details'),owner={},panes={},tabs={};
    let current=null,tab='overview',origin=null,scroll=null,overflow='',serial=0,loading=false,pending=false,dead=false,opened=false;
    let query={hours:1},data=null,error='',metric='traffic',plot=null,resize=null,tap=null,plots=[],selected=0,networkIdentity=null,networkPending=false;
    const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
    const button=(text,action,cls='')=>{const n=el('button',cls,text);n.type='button';n.addEventListener('click',action);return n;};
    const header=el('header','details-header'),title=el('h2','','设备详情'),machine=el('div','details-machine');title.id='device-details-title';
    const heading=el('div','details-heading'),closeButton=button('关闭',()=>close(),'details-close');header.append(heading,closeButton);heading.append(title,machine);
    const tablist=el('div','details-tabs');tablist.setAttribute('role','tablist');tablist.setAttribute('aria-label','设备详情类别');
    const body=el('div','details-body');
    for(const [key,label]of [['overview','概览'],['resources','资源'],['security','安全'],['management','管理'],['network','网络质量']]){
      const b=button(label,()=>activate(key));b.id='details-tab-'+key;b.setAttribute('role','tab');b.setAttribute('aria-controls','details-pane-'+key);tabs[key]=b;if(key!=='network')tablist.append(b);
      const pane=el('section','details-pane');pane.id='details-pane-'+key;pane.setAttribute('role','tabpanel');pane.setAttribute('aria-labelledby',b.id);panes[key]=pane;body.append(pane);
      b.addEventListener('keydown',e=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();const keys=Object.keys(tabs),i=keys.indexOf(key),next=e.key==='Home'?0:e.key==='End'?keys.length-1:(i+(e.key==='ArrowRight'?1:keys.length-1))%keys.length;activate(keys[next]);tabs[keys[next]].focus();});
    }
    dialog.replaceChildren(header,tablist,body);
    const networkControls=el('div','details-history-controls'),networkTarget=el('select'),networkChart=el('div'),networkHint=el('span','details-note');
    networkTarget.setAttribute('aria-label','网络质量历史目标');networkTarget.dataset.focusKey='details-network-target';networkControls.append(el('span','','历史目标'),networkTarget,networkHint);panes.network.append(networkControls,networkChart);
    function populateNetworkTargets(){const available=network()?.historyTargets(current)||[],signature=JSON.stringify(available.map(t=>[t.id,t.label]));if(networkTarget.dataset.signature!==signature){networkTarget.replaceChildren();for(const t of available){const option=el('option','',t.label);option.value=t.id;networkTarget.append(option);}networkTarget.dataset.signature=signature;}
      if(networkIdentity&&!available.some(t=>t.id===networkIdentity.target&&t.family===networkIdentity.family)){network()?.releaseHistory();networkIdentity=null;}
      if(!networkIdentity){const first=available.find(t=>t.slot==='telecom'&&t.family==='ipv4'&&t.protocol==='tcp')||available[0];if(first)networkIdentity={target:first.id,family:first.family,kind:'latency'};}
      networkTarget.disabled=!available.length;networkTarget.value=networkIdentity?.target||'';networkHint.textContent=available.length?'现有单目标历史，独立于卡片显示模式':'暂无有效目标；地区配置加载或 Agent 不支持';return available;}
    networkTarget.addEventListener('change',()=>{const chosen=(network()?.historyTargets(current)||[]).find(t=>t.id===networkTarget.value);if(!chosen)return;networkIdentity={target:chosen.id,family:chosen.family,kind:'latency'};restoreNetwork();});
    const controls=el('div','details-history-controls'),status=el('p','details-history-status'),note=el('p','details-note','按每分钟平均值统计；缺数据处断开，边缘分钟可能不完整。'),chart=el('div','details-resource-plot'),output=el('output','details-point');
    const hoursButtons=new Map();for(const hours of [1,6,24,168]){const b=button(hours===168?'7 天':hours+' 小时',()=>apply({hours}));hoursButtons.set(hours,b);controls.append(b);}
    controls.append(button('刷新',()=>request()));
    const metricSelect=el('select');metricSelect.setAttribute('aria-label','历史指标');for(const [key,spec]of Object.entries(H.metrics)){const option=el('option','',spec.label);option.value=key;metricSelect.append(option);}metricSelect.value=metric;metricSelect.addEventListener('change',()=>{metric=metricSelect.value;renderPlot();});metricSelect.hidden=true;
    const custom=el('form','details-custom'),from=el('input'),to=el('input'),rangeError=el('span','details-range-error');
    from.type=to.type='datetime-local';from.step=to.step='1';from.setAttribute('aria-label','自定义开始时间');to.setAttribute('aria-label','自定义结束时间');
    const label=(text,input)=>{const n=el('label','',text);n.append(input);return n;};const applyButton=el('button','','应用自定义范围');applyButton.type='submit';custom.append(label('开始',from),label('结束',to),applyButton,rangeError);
    custom.addEventListener('submit',e=>{e.preventDefault();try{apply(H.range(from.value,to.value));rangeError.textContent='';}catch(err){rangeError.textContent=err.message;}});
    output.tabIndex=0;output.setAttribute('aria-label','分钟观测详情，左右方向键浏览');output.setAttribute('aria-live','polite');
    output.addEventListener('keydown',e=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();select(e.key==='Home'?0:e.key==='End'?(data?.points.length||1)-1:selected+(e.key==='ArrowLeft'?-1:1));});
    const resourceInfo=el('div','details-resource-facts');panes.resources.append(controls,custom,note,status,chart,resourceInfo,output);custom.hidden=true;controls.append(button('自定义时间…',()=>{custom.hidden=!custom.hidden;if(!custom.hidden)from.focus();}));
    function renderResourceFacts(){const rows=resourceFacts(agents.get(current));resourceInfo.hidden=!rows.length;for(let i=0;i<rows.length;i++){let row=resourceInfo.children[i];if(!row){row=el('div');row.append(el('strong'),el('span'));resourceInfo.append(row);}if(row.children[0].textContent!==rows[i][0])row.children[0].textContent=rows[i][0];if(row.children[1].textContent!==rows[i][1])row.children[1].textContent=rows[i][1];}while(resourceInfo.children.length>rows.length)resourceInfo.lastElementChild.remove();}
    function active(key,id=current){return !dead&&dialog.open&&!doc.hidden&&tab===key&&current===id&&agents.has(id);}
    function destroyPlot(){resize?.disconnect();resize=null;for(const p of plots)p.destroy();plots=[];plot=null;chart.replaceChildren();output.textContent='';}
    function release(){serial++;loading=false;coordinator.release(owner);destroyPlot();}
    function leave(){if(tab==='resources'){pending=loading;release();}if(tab==='network'){networkPending=false;network()?.releaseHistory();}}
    function restoreNetwork(){if(!active('network')||!networkIdentity)return;
      if(network()?.openHistoryFor(current,networkIdentity.target,networkIdentity.family,networkIdentity.kind,origin?.node)===false)
        networkChart.replaceChildren(el('p','','原指定目标已不可用；请在此选择当前有效目标，不借用其他地址族数据。'));}
    let titleIdentity = null, securityIdentity = null;
    function updateTitle(){const agent=agents.get(current),s=agent?.state||{};machine.replaceChildren();const mark=identity(agent);if(mark)machine.append(mark);
      const group=el('div','details-identity'),line=el('div','details-title-line'),name=el('strong','',agent?.name||s.hostname||'未命名设备');
      const lifecycle=agent?.revoked?'已撤销':agent?.disabled_at?'已暂停':agent?.online?'在线':'离线';
      line.append(name,el('span','details-status',lifecycle+(agent?.last_seen?' · '+Math.max(0,Math.floor((Date.now()-agent.last_seen)/1000))+' 秒前上报':'')));
      group.append(line,el('span','details-note',[s.os||'系统未知',s.arch||'架构未知','Agent '+(agent?.version||'未知'),s.uptime==null?'运行时长未知':`运行 ${Math.floor(s.uptime/86400)} 天${!agent?.online?'（旧数据）':''}`].join(' · ')));machine.append(group);title.hidden=true;
    }
    function renderOverview(){const node=overview(agents.get(current));if(node&&node.parentNode!==panes.overview)panes.overview.replaceChildren(node);}
    function update(agent){if(!dialog.open||agent?.agent_id!==current)return;updateTitle();if(tab==='overview')renderOverview();if(tab==='management')renderManagement();if(tab==='resources')renderResourceFacts();if(tab==='security')renderSecurity();}
    function renderManagement(){const pane=panes.management,focused=pane.contains(doc.activeElement)?doc.activeElement?.dataset?.focusKey:null;
      const node=management(agents.get(current));if(node){node.hidden=false;if(node.parentNode!==pane)pane.replaceChildren(node);if(focused)pane.querySelector(`[data-focus-key="${focused}"]`)?.focus({preventScroll:true});}}
    function renderSecurity(){const security=agents.get(current)?.security,pane=panes.security,signature=JSON.stringify([current,security]);if(signature===securityIdentity)return;securityIdentity=signature;pane.replaceChildren();
      if(!security){pane.append(el('p','','安全资料尚未加载'),button('读取设备资料',()=>hydrate(current)));return;}
      if(!security.supported){pane.append(el('p','','当前 Agent 不支持安全观察（需要 v0.9 Agent）'));return;}
      const labels={unavailable:'需要配置',no_data:'等待首次本地审计',complete:'完整',partial:'部分结果',failed:'采集失败'};
      const unavailableReasons={sandbox_unavailable:'systemd 沙箱不可用（容器或受限环境），安全观察无法运行',collector_start_failed:'本地安全采集服务启动失败'};
      const unavailableText=security.status==='unavailable'?unavailableReasons[security.reason]:'';
      const neutral=security.status==='no_data'&&security.reason==='no_sing_box_logs'?'未发现 sing-box 日志（本机可能未安装或未运行 sing-box）':'';
      const statusLabel=neutral||(unavailableText?`不可用：${unavailableText}`:security.status==='unavailable'&&security.reason==='platform_unsupported'?'此平台暂不支持（v1.1）':labels[security.status]||security.status);
      pane.append(el('p',neutral?'details-note':'',`${statusLabel}${security.stale?' · 旧数据':''}${security.reason&&!unavailableText&&!neutral?' · '+security.reason:''}`),el('p','details-note',`资料更新：${security.updated_at?new Date(security.updated_at).toLocaleString():'未知'} · 仅展示当前观察，不触发扫描`));
      const batch=security.current;if(!batch){pane.append(el('p','','暂无当前观察'));return;}
      pane.append(el('p','',`实际审计窗口：${new Date(batch.window_start).toLocaleString()} — ${new Date(batch.window_end).toLocaleString()} · ${batch.total_events} 条观察 · ${batch.tracked_sources} 个来源${security.delivery_gap?' · delivery gap（上次接收 '+new Date(security.previous_collected_at).toLocaleString()+'）':''}`));
      for(const source of (batch.sources||[]).slice(0,256)){const row=el('div','details-security-source');row.append(el('code','',source.ip),el('strong','',(source.classifications||[]).slice(0,16).join(' + ')),el('span','',`${source.count} 次 · ${new Date(source.first_seen).toLocaleString()} — ${new Date(source.last_seen).toLocaleString()}`));pane.append(row);}
      if(!(batch.sources||[]).length)pane.append(el('p','',batch.status==='complete'?'本窗口未观察到匹配事件':'没有可展示的完整来源数据'));
    }
    function activate(key,{restoreNetwork:shouldRestoreNetwork=true}={}){if(!Object.hasOwn(panes,key))return;if(tab!==key)leave();tab=key;
      for(const [name,pane]of Object.entries(panes)){pane.hidden=name!==tab;tabs[name].setAttribute('aria-selected',String(name===tab));tabs[name].tabIndex=name===tab?0:-1;}
      if(!dialog.open)return;
      if(tab==='overview'){renderOverview();upgradeTargets(current);}else if(tab==='management'){renderManagement();upgradeTargets(current);}else if(tab==='security'){renderSecurity();if(!agents.get(current)?.detail_loaded)hydrate(current);}
      else if(tab==='resources'){pending=false;renderResourceFacts();renderPlot();request();}
      else {network()?.ensureHistoryTargets?.(current);populateNetworkTargets();if(shouldRestoreNetwork&&networkIdentity)restoreNetwork();else if(!networkIdentity)networkChart.replaceChildren(el('p','','暂无有效网络历史目标；请先在网络质量卡片的“设置”里填写并保存目标地址。'));}
    }
    function open(id,key='overview',trigger=null,exact=null){if(dead||!agents.has(id))return;
      if(current!==id){conceal();leave();release();data=null;error='';query={hours:1};metric='traffic';metricSelect.value=metric;from.value=to.value='';rangeError.textContent='';networkIdentity=null;current=id;}
      if(trigger||current!==origin?.id)origin={node:trigger,id,key:trigger?.dataset?.focusKey||'manage'};
      const opening=!dialog.open;if(opening){opened=true;scroll=[root.scrollX||0,root.scrollY||0];overflow=doc.body.style.overflow;doc.body.style.overflow='hidden';dialog.showModal();}
      if(exact)networkIdentity={...exact};updateTitle();activate(key,{restoreNetwork:!exact});if(opening)closeButton.focus({preventScroll:true});
    }
    function close({restoreFocus=true}={}){if(!opened)return;opened=false;conceal();leave();release();pending=false;data=null;error='';if(dialog.open)dialog.close();doc.body.style.overflow=overflow;
      if(restoreFocus&&origin&&agents.has(origin.id)){const trigger=origin.node?.isConnected?origin.node:doc.querySelector(`.agent-card[data-agent-id="${origin.id}"] [data-focus-key="${origin.key}"]`);trigger?.focus({preventScroll:true});}
      if(scroll)root.scrollTo?.(...scroll);}
    function apply(next){release();query=next;data=null;error='';pending=false;renderPlot();request();}
    function statusText(){for(const [hours,b]of hoursButtons)b.setAttribute('aria-pressed',String(query.hours===hours));
      if(!data){status.textContent=error|| (loading?'正在读取指定分钟窗口…':'暂无分钟数据');return;}
      status.textContent=`${error?'读取失败 · 上次可信历史（非当前） · ':''}请求 ${new Date(data.from).toLocaleString()} — ${new Date(data.to).toLocaleString()} · 实际覆盖 ${data.points.length?new Date(data.oldest_timestamp).toLocaleString()+' — '+new Date(data.last_timestamp).toLocaleString():'无数据'} · 部分记录 · 一分钟算术均值 · 服务器时间 ${new Date(data.server_now).toLocaleString()}`;}
    async function request(){if(!active('resources')||loading)return;loading=true;pending=false;const generation=serial,id=current,wanted={...query};statusText();
      try{const value=await H.load(wanted,async part=>{
        if(generation!==serial||!active('resources',id))throw Object.assign(new Error('cancelled'),{name:'AbortError'});
        const params=new URLSearchParams(Object.hasOwn(part,'hours')?{hours:String(part.hours)}:{from:String(part.from),to:String(part.to)});
        const raw=await coordinator.request(JSON.stringify(['resources',id,part]),owner,signal=>C.transportLease(tracked=>state.fetchJSON(`/api/v1/web/agents/${encodeURIComponent(id)}/resources/history?${params}`,{signal,fetcher:tracked,timeoutMs:10000,unauthorized}),H.boundedFetcher(fetcher)));
        return H.validate(raw,id,part);
      });
        if(generation!==serial||!active('resources',id))return;data=value;error='';selected=Math.max(0,data.points.length-1);renderPlot();
      }catch(err){if(generation===serial&&active('resources',id)&&err.name!=='AbortError'){error='资源历史读取失败；未采用新响应';statusText();}}
      finally{if(generation===serial){loading=false;statusText();}}
    }
    function select(index){selected=Math.max(0,Math.min((data?.points.length||1)-1,index));const point=data?.points[selected];output.textContent=point?`${new Date(point.timestamp).toLocaleString()} · 按每分钟平均值 · CPU ${point.cpu.toFixed(2)}% · 内存 ${point.ram_percent.toFixed(2)}% · ↓ ${root.TrafficChart?.bytes?.(point.rx_rate)||point.rx_rate+' B/s'} · ↑ ${root.TrafficChart?.bytes?.(point.tx_rate)||point.tx_rate+' B/s'}`:'';}
    function renderPlot(){destroyPlot();statusText();if(!active('resources')||!data)return;
      if(!data.points.length||!Plot){chart.append(el('p','',!data.points.length?'暂无可绘制分钟数据；不补零':'本地图表库未加载'));return;}
      const stats=H.statistics(data),fmt=v=>v==null?'—':root.TrafficChart?.bytes?.(v)||v+' B',percent=v=>v?`平均 ${v.mean.toFixed(1)}% · 最高 ${v.max.toFixed(1)}%（按每分钟平均值）`:'暂无数据';
      for(const key of ['cpu','memory','traffic']){
        const box=el('section','details-box resource-chart'),heading=el('div','details-box-head'),summary=el('span','details-note'),canvas=el('div','resource-canvas');
        heading.append(el('h2','',key==='cpu'?'CPU':key==='memory'?'内存':'流量'));box.append(heading,summary,canvas);chart.append(box);
        summary.textContent=key==='cpu'?percent(stats.cpu):key==='memory'?percent(stats.memory):`区间累计差 ↓ ${fmt(stats.rx_total)} · ↑ ${fmt(stats.tx_total)} · 峰值 ↓ ${fmt(stats.rx?.max)}/s · ↑ ${fmt(stats.tx?.max)}/s（按每分钟平均值）`;
        if(key==='traffic'){box.classList.add('resource-wide');box.append(el('p','details-note',`合计实际覆盖 ${new Date(stats.from).toLocaleString()} — ${new Date(stats.to).toLocaleString()}；按累计值差计算，非完整起止区间承诺。`));}
        const adapted=H.columns(data,key),spec=adapted.spec,width=()=>Math.max(210,Math.floor(canvas.getBoundingClientRect().width));
        const p=new Plot({width:width(),height:key==='traffic'?190:170,legend:{show:key==='traffic'},cursor:{drag:{x:false,y:false}},scales:{x:{time:true},y:{auto:true}},
          axes:[{stroke:'#6E655B',font:'10px system-ui',size:28,grid:{stroke:'#E6DDD1'}},{stroke:'#6E655B',font:'10px system-ui',size:62,grid:{stroke:'#E6DDD1'},values:(_u,ticks)=>ticks.map(v=>spec.unit==='B/s'?(root.TrafficChart?.bytes?.(v)||v+' B/s'):Number(v.toFixed(1))+'%')}],
          series:[{},...spec.names.map((label,i)=>({label,stroke:key==='memory'?'#3D6FA8':['#1F7A6C','#7A4FA0'][i],fill:key==='memory'?'#3D6FA818':i?'#7A4FA016':'#1F7A6C20',width:1.5,spanGaps:false,points:{show:false}}))],
          hooks:{setCursor:[u=>{const point=adapted.points[u.cursor.idx];if(point&&active('resources'))select(data.points.indexOf(point));}]}},adapted.data,canvas);
        p.over?.addEventListener('pointerdown',e=>{if(!active('resources')||e.isPrimary===false)return;const r=p.over.getBoundingClientRect(),point=adapted.points[p.posToIdx(e.clientX-r.left)];if(point)select(data.points.indexOf(point));});
        plots.push(p);if(!plot)plot=p;
        if(Resize){if(!resize)resize=new Resize(()=>{if(active('resources'))plots.forEach(u=>{const n=u.root?.parentNode;if(n)u.setSize({width:Math.max(210,Math.floor(n.getBoundingClientRect().width)),height:u===plots.at(-1)?190:170});});});resize.observe(canvas);}
      }
      select(selected);
    }
    function visibility(){if(doc.hidden){if(tab==='resources'){pending=loading;release();}if(dialog.open&&tab==='network'){networkPending=Boolean(networkIdentity);network()?.releaseHistory();}}
      else if(active('network')){network()?.ensureHistoryTargets?.(current);if(networkPending){networkPending=false;restoreNetwork();}}
      else if(active('resources')&&pending){pending=false;request();}else if(active('resources'))renderPlot();}
    // Native close events are queued: an older event must not close a reopened modal.
    const cancel=e=>{e.preventDefault();close();},closed=()=>{if(!dialog.open)close();};
    let backdropPress=false;
    const outside=e=>{const r=dialog.getBoundingClientRect();return e.target===dialog&&(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom);};
    const pointerDown=e=>{backdropPress=e.button===0&&e.isPrimary!==false&&outside(e);};
    const pointerCancel=()=>{backdropPress=false;};
    const backdropClick=e=>{const dismiss=backdropPress&&outside(e);backdropPress=false;if(dismiss)close();};
    dialog.addEventListener('pointerdown',pointerDown);dialog.addEventListener('pointercancel',pointerCancel);dialog.addEventListener('click',backdropClick);
    dialog.addEventListener('cancel',cancel);dialog.addEventListener('close',closed);doc.addEventListener('visibilitychange',visibility);
    return {open,close,update,active,networkContainer:()=>networkChart,
      networkChanged(id){if(active('network',id)){populateNetworkTargets();restoreNetwork();}},
      sync(){if(current&&!agents.has(current)){close({restoreFocus:false});current=null;networkIdentity=null;data=null;}},
      shutdown(){close({restoreFocus:false});dead=true;release();network()?.releaseHistory();doc.removeEventListener('visibilitychange',visibility);dialog.removeEventListener('cancel',cancel);dialog.removeEventListener('close',closed);dialog.removeEventListener('pointerdown',pointerDown);dialog.removeEventListener('pointercancel',pointerCancel);dialog.removeEventListener('click',backdropClick);current=null;data=null;},
      snapshot:()=>({id:current,tab,open:dialog.open,loading,points:data?.points.length||0,plot:!!plot,query:{...query},draft:{from:from.value,to:to.value}})};
  }
  const api={create};if(typeof module==='object'&&module.exports)module.exports=api;else root.DeviceDetails=api;
})(globalThis);
