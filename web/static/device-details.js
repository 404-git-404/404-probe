/* Persistent overview/management nodes. Server fields retain their original meanings. */
(function(root){
 'use strict';
 function create({document:doc=root.document,actions,egress,country,edit,renew,saveCountry,canWrite}){
  const el=(t,c='',text)=>{const n=doc.createElement(t);n.className=c;if(text!=null)n.textContent=text;return n;};
  const overview=el('div','details-overview'),management=el('div','details-management'),metrics=el('div','details-metrics'),grid=el('div','details-grid'),groups=el('div','details-grid');
  overview.append(metrics,grid);management.append(groups);
  const values={};let current=null;
  const value=(key,parent)=>{const n=el('span');values[key]=n;parent.append(n);return n;};
  const button=(label,action,key)=>{const n=el('button','',label);n.type='button';if(key)n.dataset.focusKey=key;n.addEventListener('click',()=>action(n));return n;};
  function section(title,parent=grid){const n=el('section','details-box'),h=el('div','details-box-head');h.append(el('h2','',title));n.append(h);parent.append(n);return {n,h};}
  function fact(box,label,key){const row=el('div','details-fact');row.append(el('span','details-label',label));value(key,row);box.n.append(row);}
  const tiles=['CPU','内存','磁盘','下行','上行'].map((label,i)=>{const n=el('div','details-box details-metric');n.append(el('span','details-label',label));const main=value('metric'+i,n),sub=value('sub'+i,n);main.className='metric-value';sub.className='details-note';metrics.append(n);return n;});
  const plan=section('套餐与续费'),editButton=button('编辑',n=>edit(current,n),'detail-plan');plan.h.append(editButton);
  fact(plan,'价格','price');values.price.className='details-price';fact(plan,'带宽','bandwidth');fact(plan,'下次续费','renewal');const renewal=button('已续费…',n=>renew(current,n),'detail-renewal');values.renewal.parentNode.append(renewal);fact(plan,'到期日','expiry');
  const traffic=section('流量'),quota=button('编辑配额',n=>edit(current,n,'quota'),'detail-quota');traffic.h.append(quota);value('usage',traffic.n);
  const track=el('div','details-progress'),fill=el('span');track.append(fill);traffic.n.append(track);value('cycle',traffic.n).className='details-note';fact(traffic,'设备累计','total');value('coverage',traffic.n).className='details-note';
  const network=section('网络地址');network.n.append(egress.addresses);fact(network,'出口地区','country');
  const system=section('系统');fact(system,'主机名','hostname');fact(system,'配置','config');values.config.className='details-config';fact(system,'Agent','version');fact(system,'最近上报','seen');
  const agentGroup=section('Agent',groups),checks=section('检测与统计',groups),danger=section('危险操作',management);danger.n.classList.add('details-danger');
  function row(box,title,key,control){const n=el('div','details-action-row'),left=el('div');left.append(el('strong','',title));value(key,left).className='details-note';n.append(left);if(control)n.append(control);box.n.append(n);return n;}
  row(agentGroup,'升级','upgrade',actions.upgrade);row(agentGroup,'系统信息','sample');
  const region=el('div','details-region'),select=el('select');select.setAttribute('aria-label','出口地区手动覆盖');select.dataset.focusKey='detail-country';
  const catalog=root.Management.countries();select.append(Object.assign(el('option','','使用自动识别'),{value:''}));for(const c of catalog)select.append(Object.assign(el('option','',c.zh+' '+c.code),{value:c.code}));
  select.addEventListener('change',()=>saveCountry(current,select.value));region.append(select,country);row(agentGroup,'出口地区','countryManage',region);
  const iprow=row(checks,'公网 IP 检测','egress',egress.controls),switchLabel=egress.controls.querySelector('.pe-switch');if(switchLabel){switchLabel.querySelector('span').textContent=' 公网 IP 检测';iprow.children[0].querySelector('strong').hidden=true;iprow.children[0].prepend(switchLabel);}row(checks,'设备累计流量','since',actions.reset);checks.n.append(actions.resetFeedback);
  row(checks,'Google 检测','google',actions.google);checks.n.append(actions.googleFeedback);
  row(danger,'暂停监控','pause',actions.pause);row(danger,'撤销 Agent','revoke',actions.remove);
  const more=el('details','details-more'),summary=el('summary','','更多');more.append(summary,actions.history);management.append(more);
  const time=(v,dateOnly=false)=>{if(!v)return '未知';const d=new Date(v),pad=n=>String(n).padStart(2,'0');return (d.getFullYear()!==new Date().getFullYear()?d.getFullYear()+'-':'')+pad(d.getMonth()+1)+'-'+pad(d.getDate())+(dateOnly?'':' '+pad(d.getHours())+':'+pad(d.getMinutes()));};
  const bytes=(v,binary=false)=>v==null?'—':root.CardMetrics?.size?.(v,binary)||format(v,binary);
  function format(v,binary){let n=Number(v),i=0;const units=binary?['B','KiB','MiB','GiB','TiB']:['B','KB','MB','GB','TB'],base=binary?1024:1000;while(n>=base&&i<4){n/=base;i++;}return Number(n.toFixed(i&&n<10?2:i&&n<100?1:0))+' '+units[i];}
  const countryName=(code)=>catalog.find(c=>c.code===code)?.zh||code||'未知';
  const set=(key,text)=>{if(values[key].textContent!==text)values[key].textContent=text;};
  function update(a,target=null,serverBuild=null){current=a;const s=a.state||{},p=a.plan||{},old=!a.online||a.disabled_at||s.stale,valid=v=>!old&&v!=null;
   const percent=v=>v==null?'—':Number(v).toFixed(Number(v)>=10?0:1)+'%';
   [s.cpu_percent,s.ram_percent,s.disk_percent].forEach((v,i)=>set('metric'+i,valid(v)?percent(v):'—'));
   set('sub0',old?'当前数据不可用':`负载 ${s.load1==null?'—':s.load1.toFixed(2)} · steal ${percent(s.cpu_steal_percent)}`);
   set('sub1',old?'当前数据不可用':`${s.ram_used==null?'—':format(s.ram_used,true)} / ${s.ram_total?format(s.ram_total,true):'—'}`);set('sub2',old?'当前数据不可用':`${s.disk_used==null?'—':format(s.disk_used,true).replace(/ [A-Za-z]+$/,'')} / ${s.disk_total?format(s.disk_total,true):'—'}`);
   [s.rx_rate,s.tx_rate].forEach((v,i)=>{set('metric'+(i+3),valid(v)?format(v,false)+'/s':'—');set('sub'+(i+3),old?'当前数据不可用':'实时速率');});
   set('price',p.renewal_price?`${p.currency||''} ${p.currency==='USD'?'$':''}${p.renewal_price} / ${{monthly:'月',yearly:'年',once:'一次性'}[p.renewal_price_period]||'未设周期'}`:'未设置');
   set('bandwidth',p.bandwidth_value?`${p.bandwidth_value} ${p.bandwidth_unit}`:'未设置');
   let relative='';if(p.renewal_date){try{const today=new Intl.DateTimeFormat('en-CA',{timeZone:p.timezone||'UTC',year:'numeric',month:'2-digit',day:'2-digit'}).format(new Date());const days=Math.round((Date.parse(p.renewal_date)-Date.parse(today))/86400000);if(Number.isFinite(days))relative=days===0?'今天':days>0?`${days} 天后`:`已过 ${-days} 天`;}catch{}}
   values.renewal.replaceChildren(el('strong','',p.renewal_date||'未设置'),el('span','details-note',relative));set('expiry',p.expiry_date||'未设置');
   const view=root.Card.trafficView(a,{old:Boolean(old)}),days=root.Card.cycleDays(p,Date.now());
   values.usage.className='details-usage';values.usage.replaceChildren();const usageLeft=el('span');usageLeft.append(el('strong','',format(p.usage_bytes||0,false)),el('span','',` / ${p.quota_bytes==null?'不限流量':format(p.quota_bytes,false)} 本周期`));values.usage.append(usageLeft);
   if(p.quota_bytes)values.usage.append(el('span','details-note',(Number(p.usage_bytes||0)/p.quota_bytes*100).toFixed(1)+'%'));if(!p.traffic_mode)values.usage.textContent='未开启套餐流量统计';
   track.hidden=!p.traffic_mode||p.quota_bytes==null;fill.style.width=p.quota_bytes?Math.max(0,Math.min(100,Number(p.usage_bytes||0)/p.quota_bytes*100))+'%':'0%';
   set('cycle',`${root.Card.resetText(p)}${days?` · 第 ${days.day}/${days.total} 天`:''}${p.traffic_mode?' · '+({sum:'计下行 + 上行',rx:'只计下行',tx:'只计上行'}[p.traffic_mode]||''):''}`);
   values.total.replaceChildren();if(a.traffic)values.total.append(el('span','details-down','↓ '+format(a.traffic.rx_total,false)),el('span','details-up','↑ '+format(a.traffic.tx_total,false)),el('span','details-note',a.traffic.started_at?'自 '+time(a.traffic.started_at,true):'自接入起'));else set('total','—');
   set('coverage','');values.usage.title=p.usage_status==='calibrated'?'本周期用量已人工对账':'仅统计可观测用量，缺测可能使统计不完整';
   const regionText=p.country_code_override?`已手动设置为 ${countryName(p.country_code_override)} ${p.country_code_override}`:`${a.country_code?countryName(a.country_code)+' '+a.country_code:'未知'} · 自动识别`;
   set('country',regionText);set('countryManage',regionText);if(select!==doc.activeElement)select.value=p.country_code_override||'';select.disabled=!canWrite(a);
   set('hostname',s.hostname||'—');set('config',`${s.cpu_cores?s.cpu_cores+' 核':'核心数未知'} · ${s.ram_total?format(s.ram_total,true):'—'} 内存 · ${s.disk_total?format(s.disk_total,true):'—'} 磁盘 · ${s.swap_total==null?'—':format(s.swap_total,true)} Swap`);
   actions.upgrade.disabled=actions.upgrade.disabled||!target?.supported;
   const healthCode=root.AgentState?.updaterHealthReason(a.updater_health);
   const targetReason=root.AgentState?.upgradeReasonDisplay(target)||target?.reason||'暂时不能升级，请刷新后查看原因';
   const noteCode=healthCode||(targetReason.includes('升级服务排查文档')?target?.reason_code:'');
   const upgrade=target?.supported?`可升级到 ${target.version}`:'';values.version.replaceChildren(el('span','',a.version||'—'));if(upgrade)values.version.append(el('span','details-upgrade-badge',upgrade));
   set('upgrade',(a.version||'—')+(target?' → '+target.version+(target.supported?'（与面板相同）':target.reason_code===noteCode?'':' · '+targetReason):' · '+(a.online?'正在读取可升级版本，请稍候':'机器离线，请先恢复连接再升级')));
   if(noteCode){const note=el('span','details-note');note.style.display='block';root.AgentState.appendUpgradeReason(note,{reason_code:noteCode},serverBuild);values.upgrade.append(note);}
   set('seen',a.last_seen?time(a.last_seen)+':'+String(new Date(a.last_seen).getSeconds()).padStart(2,'0'):'未知');values.config.title=values.config.textContent;values.sample.title=s.collected_at?new Date(s.collected_at).toLocaleString():'';values.since.title=a.traffic?.started_at?new Date(a.traffic.started_at).toLocaleString():'';values.since.title+='；只改变累计显示，不改历史或套餐周期用量';values.seen.title=a.last_seen?new Date(a.last_seen).toLocaleString():'';set('sample','随上报自动更新 · 最近采样 '+time(s.collected_at));set('egress',a.public_egress_check?.next_check_at?'下次 '+time(a.public_egress_check.next_check_at):'按服务端计划检测');
   set('since',a.traffic?.started_at?'从 '+time(a.traffic.started_at,true)+' 起 · 重新开始不影响套餐用量':'重新开始不影响套餐用量');set('google',a.google_status?.checked_at?'上次 '+time(a.google_status.checked_at):'尚未检测');set('pause',a.disabled_at?'已暂停，可以恢复监控':'不再接收数据，可随时恢复');set('revoke','撤销只会让面板不再接收这台机器的数据，保留历史记录。要在机器上彻底卸载，请在那台机器上运行：sudo 404-probe-install uninstall agent');values.revoke.title='运行卸载命令时需确认删除机器上的托管数据';
   [editButton,quota,renewal].forEach(n=>{n.disabled=!canWrite(a);});
  }
  return {overview,management,update};
 }
 const api={create};if(typeof module!=='object'||!module.exports)root.DetailSummary=api;
})(globalThis);

/* One native dialog, independent of card remounts. History never follows SSE. */
(function(root) {
  'use strict';
  const H=root.ResourceHistory||(typeof require==='function'?require('./resource-history.js'):null);
  const C=root.NetworkQualityCore||(typeof require==='function'?require('./network-quality-core.js'):null);
  function create({document:doc=root.document,state=root.AgentState,agents,coordinator,fetcher=(...args)=>root.fetch(...args),
    unauthorized=()=>root.location.assign('/login'),Plot=root.uPlot,Resize=root.ResizeObserver,
    management=()=>null,overview=()=>null,upgradeTargets=()=>{},resourceFacts=()=>[],identity=()=>null,hydrate=()=>{},conceal=()=>{}}={}) {
    const dialog=doc.querySelector('#device-details'),owner={},panes={},tabs={},shell=root.DialogShell?.attach(dialog,{decorate:false});
    let current=null,tab='overview',origin=null,scroll=null,overflow='',serial=0,loading=false,pending=false,dead=false,opened=false;
    let query={hours:1},data=null,error='',metric='traffic',plot=null,resize=null,tap=null,plots=[],selected=0;
    const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
    const button=(text,action,cls='')=>{const n=el('button',cls,text);n.type='button';n.addEventListener('click',action);return n;};
    const header=el('header','details-header'),title=el('h2','','设备详情'),machine=el('div','details-machine');title.id='device-details-title';
    const heading=el('div','details-heading'),closeButton=button('×',()=>close(),'details-close');closeButton.setAttribute('aria-label','关闭');header.append(heading,closeButton);heading.append(title,machine);
    const tablist=el('div','details-tabs');tablist.setAttribute('role','tablist');tablist.setAttribute('aria-label','设备详情类别');
    const body=el('div','details-body');
    for(const [key,label]of [['overview','概览'],['resources','资源'],['security','安全'],['management','管理']]){
      const b=button(label,()=>activate(key));b.id='details-tab-'+key;b.setAttribute('role','tab');b.setAttribute('aria-controls','details-pane-'+key);tabs[key]=b;tablist.append(b);
      const pane=el('section','details-pane');pane.id='details-pane-'+key;pane.setAttribute('role','tabpanel');pane.setAttribute('aria-labelledby',b.id);panes[key]=pane;body.append(pane);
      b.addEventListener('keydown',e=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();const keys=Object.keys(tabs),i=keys.indexOf(key),next=e.key==='Home'?0:e.key==='End'?keys.length-1:(i+(e.key==='ArrowRight'?1:keys.length-1))%keys.length;activate(keys[next]);tabs[keys[next]].focus();});
    }
    dialog.replaceChildren(header,tablist,body);
    const controls=el('div','details-history-controls'),status=el('p','details-history-status'),note=el('p','details-note','按每分钟平均值统计；缺数据处断开，边缘分钟可能不完整。'),chart=el('div','details-resource-plot'),output=el('output','details-point');
    const hoursButtons=new Map();for(const hours of [1,6,24,168]){const b=button(hours===168?'7 天':hours+' 小时',()=>apply({hours}));hoursButtons.set(hours,b);controls.append(b);}

    const metricSelect=el('select');metricSelect.setAttribute('aria-label','历史指标');for(const [key,spec]of Object.entries(H.metrics)){const option=el('option','',spec.label);option.value=key;metricSelect.append(option);}metricSelect.value=metric;metricSelect.addEventListener('change',()=>{metric=metricSelect.value;renderPlot();});metricSelect.hidden=true;
    const custom=el('form','details-custom'),from=el('input'),to=el('input'),rangeError=el('span','details-range-error');
    from.type=to.type='datetime-local';from.step=to.step='1';from.setAttribute('aria-label','自定义开始时间');to.setAttribute('aria-label','自定义结束时间');
    const label=(text,input)=>{const n=el('label','',text);n.append(input);return n;};const applyButton=el('button','','应用自定义范围');applyButton.type='submit';custom.append(label('开始',from),label('结束',to),applyButton,rangeError);
    custom.addEventListener('submit',e=>{e.preventDefault();try{apply(H.range(from.value,to.value));rangeError.textContent='';}catch(err){rangeError.textContent=err.message;}});
    output.tabIndex=0;output.setAttribute('aria-label','分钟观测详情，左右方向键浏览');output.setAttribute('aria-live','polite');
    output.addEventListener('keydown',e=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();select(e.key==='Home'?0:e.key==='End'?(data?.points.length||1)-1:selected+(e.key==='ArrowLeft'?-1:1));});
    const resourceInfo=el('div','details-resource-facts');panes.resources.append(controls,custom,chart,resourceInfo,output);note.hidden=true;custom.hidden=true;controls.append(button('自定义时间…',()=>{custom.hidden=!custom.hidden;if(!custom.hidden)from.focus();}));controls.append(status);
    function renderResourceFacts(){const a=agents.get(current),s=a?.state||{},size=n=>n==null?'—':Number((n/2**30).toFixed(2))+' GiB',rate=n=>n==null?'—':n===0?'0 B/s':Number((n/1024).toFixed(1))+' KiB/s';const rows=s.disk_total?[["磁盘",`已用 ${size(s.disk_used)} / ${size(s.disk_total)}（${Number(s.disk_percent||0).toFixed(0)}%）　　读 ${rate(s.disk_read_rate)} · 写 ${rate(s.disk_write_rate)} · 最忙 ${Number(s.disk_busy_percent||0).toFixed(1)}%`]]:resourceFacts(a);resourceInfo.hidden=!rows.length;for(let i=0;i<rows.length;i++){let row=resourceInfo.children[i];if(!row){row=el('div');row.append(el('strong'),el('span'));resourceInfo.append(row);}if(row.children[0].textContent!==rows[i][0])row.children[0].textContent=rows[i][0];if(row.children[1].textContent!==rows[i][1])row.children[1].textContent=rows[i][1];}while(resourceInfo.children.length>rows.length)resourceInfo.lastElementChild.remove();}
    function active(key,id=current){return !dead&&dialog.open&&!doc.hidden&&tab===key&&current===id&&agents.has(id);}
    function destroyPlot(){resize?.disconnect();resize=null;for(const p of plots)p.destroy();plots=[];plot=null;chart.replaceChildren();output.textContent='';}
    function release(){serial++;loading=false;coordinator.release(owner);destroyPlot();}
    function leave(){if(tab==='resources'){pending=loading;release();}}
    let titleIdentity = null, securityIdentity = null;
    function updateTitle(){const agent=agents.get(current),s=agent?.state||{};machine.replaceChildren();const mark=identity(agent);if(mark)machine.append(mark);
      const group=el('div','details-identity'),line=el('div','details-title-line'),name=el('strong','',agent?.name||s.hostname||'未命名设备');
      const lifecycle=agent?.revoked?'已撤销':agent?.disabled_at?'已暂停':agent?.online?'在线':'离线';
      name.title=name.textContent;line.append(name,el('span','details-status status-'+(agent?.revoked?'revoked':agent?.disabled_at?'paused':agent?.online?'online':'offline'),'● '+lifecycle+(agent?.last_seen?' · '+Math.max(0,Math.floor((Date.now()-agent.last_seen)/1000))+' 秒前上报':'')));
      group.append(line,el('span','details-note',[s.os||'系统未知',s.arch||'架构未知','Agent '+(agent?.version||'未知'),s.uptime==null?'运行时长未知':`运行 ${Math.floor(s.uptime/86400)} 天${!agent?.online?'（旧数据）':''}`].join(' · ')));machine.append(group);title.hidden=true;
    }
    function renderOverview(){const node=overview(agents.get(current));if(node&&node.parentNode!==panes.overview)panes.overview.replaceChildren(node);}
    function update(agent){if(!dialog.open||agent?.agent_id!==current)return;updateTitle();if(tab==='overview')renderOverview();if(tab==='management')renderManagement();if(tab==='resources')renderResourceFacts();if(tab==='security')renderSecurity();}
    function renderManagement(){const pane=panes.management,focused=pane.contains(doc.activeElement)?doc.activeElement?.dataset?.focusKey:null;
      const node=management(agents.get(current));if(node){node.hidden=false;if(node.parentNode!==pane)pane.replaceChildren(node);if(focused)pane.querySelector(`[data-focus-key="${focused}"]`)?.focus({preventScroll:true});}}
    const shortTime=(v,seconds=false)=>{if(!v)return '未知';const d=new Date(v),p=n=>String(n).padStart(2,'0');return (d.getFullYear()!==new Date().getFullYear()?d.getFullYear()+'-':'')+p(d.getMonth()+1)+'-'+p(d.getDate())+' '+p(d.getHours())+':'+p(d.getMinutes())+(seconds?':'+p(d.getSeconds()):'');};
    function renderSecurity(){const security=agents.get(current)?.security,pane=panes.security,signature=JSON.stringify([current,security]);if(signature===securityIdentity)return;securityIdentity=signature;pane.replaceChildren();
      const date=shortTime,batch=security?.current,box=el('section','details-security-conclusion'),copy=el('div','security-conclusion-copy');box.append(copy);pane.append(box);
      const reasons={setup_required:'需要配置',sandbox_unavailable:'当前环境无法运行安全观察',collector_start_failed:'本地安全采集服务启动失败',platform_unsupported:'此平台暂不支持安全观察',no_sing_box_logs:'未发现 sing-box 日志'};
      if(!security||!security.supported||!batch){copy.title=security?.reason||'';copy.append(el('strong','',!security?'安全资料尚未加载':!security.supported?'当前 Agent 不支持安全观察':security.status==='failed'?'采集失败':reasons[security.reason]||'等待首次本地审计'),el('p','','暂无当前观察，无法形成次数结论'));if(!security)pane.append(button('读取设备资料',()=>hydrate(current)));return;}
      const sources=[...(batch.sources||[])].sort((a,b)=>b.count-a.count).slice(0,100),valid=batch.status!=='failed'&&Number.isFinite(batch.total_events)&&Number.isFinite(batch.tracked_sources);
      const conclusion=el('strong','',valid?`最近一次检查（${date(batch.window_start)} – ${date(batch.window_end)}），${batch.tracked_sources} 个来源向 Reality 发起无效连接，共 ${batch.total_events} 次`:'本次检查失败，无法形成完整统计结论');conclusion.title=`实际观察窗口：${new Date(batch.window_start).toLocaleString()} – ${new Date(batch.window_end).toLocaleString()}`;copy.append(conclusion,el('p','','来自 sing-box 日志里被 Reality 判为无效的 TCP 连接。SSH 登录失败暂未统计。'));
      if(valid){for(const [count,label]of [[batch.total_events,'尝试次数'],[batch.tracked_sources,'来源']]){const n=el('div','security-count');n.append(el('strong','',count),el('span','',label));box.append(n);}}
      const heading=el('div','details-security-heading'),info=el('span','details-note',`检查于 ${date(batch.collected_at)} · 接收于 ${date(security.updated_at)} · 只读观察 ⓘ`);info.title=`检查：${new Date(batch.collected_at).toLocaleString()}；接收：${new Date(security.updated_at).toLocaleString()}。只读本地日志，不阻断连接。`;heading.append(el('h2','details-section-title','尝试最多的来源'),info);pane.append(heading);
      const warnings=[];if(batch.tracked_sources>100||batch.dropped_sources)warnings.push('只记录了次数最多的 100 个来源');else if(batch.status==='partial')warnings.push('本次记录不完整');if(security.stale)warnings.push('当前显示的是旧数据');if(security.delivery_gap)warnings.push('部分时段未收到记录，不能判断期间没有事件');if(warnings.length){const warning=el('p','details-security-warning',warnings.join(' · '));warning.title=[batch.reason,security.reason].filter(Boolean).join(' · ');pane.append(warning);}
      const table=el('div','details-security-table'),head=el('div','details-security-row details-security-head');for(const text of ['来源 IP','次数','占比','时间段'])head.append(el('span','',text));table.append(head);
      const rows=[];for(const [index,source]of sources.entries()){const row=el('div','details-security-row'),proportion=valid&&batch.total_events>0?source.count/batch.total_events*100:null,bar=el('div','security-share'),track=el('span','security-track'),fill=el('span');fill.style.width=proportion==null?'0%':Math.min(100,proportion)+'%';track.append(fill);bar.append(track,el('span','',proportion==null?'未知':proportion.toFixed(1)+'%'));
        row.append(el('code','',source.ip),el('strong','',source.count+' 次'),bar,el('span','details-note',`${date(source.first_seen)} – ${date(source.last_seen)}`));row.hidden=index>=8;rows.push(row);table.append(row);
      }
      pane.append(table);if(sources.length>8){let expanded=false;const toggle=button(`显示全部已记录来源（${sources.length} 个）`,()=>{expanded=!expanded;rows.forEach((n,i)=>{n.hidden=!expanded&&i>=8;});toggle.textContent=expanded?'收起来源':`显示全部已记录来源（${sources.length} 个）`;toggle.setAttribute('aria-expanded',String(expanded));});toggle.setAttribute('aria-expanded','false');pane.append(toggle);}
      if(!sources.length)pane.append(el('p','',valid&&batch.status==='complete'?'本窗口未观察到匹配事件':'没有可展示的完整来源数据'));
    }
    function activate(key){if(!Object.hasOwn(panes,key))return;if(tab!==key){leave();body.scrollTop=0;}tab=key;
      for(const [name,pane]of Object.entries(panes)){pane.hidden=name!==tab;tabs[name].setAttribute('aria-selected',String(name===tab));tabs[name].tabIndex=name===tab?0:-1;}
      if(!dialog.open)return;
      if(tab==='overview'){renderOverview();upgradeTargets(current);}else if(tab==='management'){renderManagement();upgradeTargets(current);}else if(tab==='security'){renderSecurity();if(!agents.get(current)?.detail_loaded)hydrate(current);}
      else if(tab==='resources'){pending=false;renderResourceFacts();renderPlot();request();}

    }
    function open(id,key='overview',trigger=null){if(dead||!agents.has(id))return;
      if(current!==id){conceal();leave();release();data=null;error='';query={hours:1};metric='traffic';metricSelect.value=metric;from.value=to.value='';rangeError.textContent='';current=id;}
      if(trigger||current!==origin?.id)origin={node:trigger,id,key:trigger?.dataset?.focusKey||'manage'};
      const opening=!dialog.open;if(opening){opened=true;if(shell)shell.open(trigger);else{scroll=[root.scrollX||0,root.scrollY||0];overflow=doc.body.style.overflow;doc.body.style.overflow='hidden';dialog.showModal();}}
      updateTitle();activate(key);if(opening)closeButton.focus({preventScroll:true});
    }
    function close({restoreFocus=true}={}){if(!opened)return;opened=false;conceal();leave();release();pending=false;data=null;error='';if(dialog.open)dialog.close();if(!shell)doc.body.style.overflow=overflow;
      if(!shell&&restoreFocus&&origin&&agents.has(origin.id)){const trigger=origin.node?.isConnected?origin.node:doc.querySelector(`.agent-card[data-agent-id="${origin.id}"] [data-focus-key="${origin.key}"]`);trigger?.focus({preventScroll:true});}
      if(!shell&&scroll)root.scrollTo?.(...scroll);}
    function apply(next){release();query=next;data=null;error='';pending=false;renderPlot();request();}
    function statusText(){for(const [hours,b]of hoursButtons)b.setAttribute('aria-pressed',String(query.hours===hours));
      if(!data){status.textContent=error|| (loading?'正在读取…':'暂无历史数据');return;}
      status.textContent=`${shortTime(data.from)} – ${shortTime(data.to)} · ⓘ 说明`;
      status.title=`${error?'读取失败，显示上次可信历史。':''}请求：${new Date(data.from).toLocaleString()} – ${new Date(data.to).toLocaleString()}。实际覆盖：${data.points.length?new Date(data.oldest_timestamp).toLocaleString()+' – '+new Date(data.last_timestamp).toLocaleString():'无数据'}。部分记录，按每分钟平均值（一分钟算术均值）；缺数据处断开，边缘分钟可能不完整。服务器时间：${new Date(data.server_now).toLocaleString()}。流量合计按累计值差计算。`;}
    async function request(){if(!active('resources')||loading)return;loading=true;pending=false;const generation=serial,id=current,wanted={...query};statusText();
      try{const value=await H.load(wanted,async part=>{
        if(generation!==serial||!active('resources',id))throw Object.assign(new Error('cancelled'),{name:'AbortError'});
        const params=new URLSearchParams(Object.hasOwn(part,'hours')?{hours:String(part.hours)}:{from:String(part.from),to:String(part.to)});
        const raw=await coordinator.request(JSON.stringify(['resources',id,part]),owner,signal=>C.transportLease(tracked=>state.fetchJSON(`/api/v1/web/agents/${encodeURIComponent(id)}/resources/history?${params}`,{signal,fetcher:tracked,timeoutMs:10000,unauthorized}),H.boundedFetcher(fetcher)));
        return H.validate(raw,id,part);
      });
        if(generation!==serial||!active('resources',id))return;data=value;error='';selected=Math.max(0,data.points.length-1);renderPlot();
      }catch(err){if(generation===serial&&active('resources',id)&&err.name!=='AbortError'){error='读取失败，保留上次结果';statusText();}}
      finally{if(generation===serial){loading=false;statusText();}}
    }
    function select(index){selected=Math.max(0,Math.min((data?.points.length||1)-1,index));const point=data?.points[selected];output.textContent=point?`${shortTime(point.timestamp,true)} · CPU ${point.cpu.toFixed(2)}% · 内存 ${point.ram_percent.toFixed(2)}% · ↓ ${root.TrafficChart?.bytes?.(point.rx_rate)||point.rx_rate+' B/s'} · ↑ ${root.TrafficChart?.bytes?.(point.tx_rate)||point.tx_rate+' B/s'}`:'';}
    function renderPlot(){destroyPlot();statusText();if(!active('resources')||!data)return;
      if(!data.points.length||!Plot){chart.append(el('p','',!data.points.length?'暂无历史数据':'本地图表库未加载'));return;}
      const stats=H.statistics(data),peak=v=>{const parts=(root.TrafficChart?.bytes?.(v)||v+' B/s').replace(/\/s$/,'').split(' ');return v==null?'—':Number(parts[0])+' '+parts.slice(1).join(' ');},fmt=v=>v==null?'—':root.TrafficChart?.bytes?.(v)?.replace(/\/s$/,'')||v+' B',percent=v=>v?`平均 ${v.mean.toFixed(v.mean>=10?0:1)}% · 最高 ${v.max.toFixed(v.max>=10?0:1)}%`:'暂无数据';
      for(const key of ['cpu','memory','traffic']){
        const box=el('section','details-box resource-chart'),heading=el('div','details-box-head'),summary=el('span','details-note'),canvas=el('div','resource-canvas');
        heading.append(el('h2','',key==='cpu'?'CPU':key==='memory'?'内存':'流量'));heading.append(summary);box.append(heading,canvas);chart.append(box);
        summary.textContent=key==='cpu'?percent(stats.cpu):key==='memory'?percent(stats.memory)+(agents.get(current)?.state?.ram_total?'（'+Number((agents.get(current).state.ram_total/2**30).toFixed(2))+' GiB）':''):`↓ ${fmt(stats.rx_total)}　↑ ${fmt(stats.tx_total)}　峰值 ↓ ${peak(stats.rx?.max)}/s · ↑ ${peak(stats.tx?.max)}/s`;
        if(key==='traffic'){box.classList.add('resource-wide');summary.replaceChildren(el('span','details-down','↓ '+fmt(stats.rx_total)),el('span','details-up','↑ '+fmt(stats.tx_total)),el('span','',`峰值 ↓ ${peak(stats.rx?.max)}/s · ↑ ${peak(stats.tx?.max)}/s`));}
        const adapted=H.columns(data,key);if(key==='memory'){adapted.data=adapted.data.slice(0,2);adapted.spec={...adapted.spec,names:['内存']};}const spec=adapted.spec,width=()=>Math.max(210,Math.floor(canvas.getBoundingClientRect().width));
        const p=new Plot({width:width(),height:key==='traffic'?150:110,legend:{show:false},cursor:{drag:{x:false,y:false}},padding:[0,0,0,0],scales:{x:{time:true},y:{range:(_u,min,max)=>[0,key==='cpu'?Math.max(20,max*1.1):key==='memory'?Math.max(50,max*1.1):Math.max(1,max*1.05)]}},
          axes:[{show:false},{show:true,size:0,grid:{stroke:'#E6DDD1',width:.8},ticks:{show:false},splits:(_u,_i,min,max)=>key==='traffic'?[0,max/2,max*.75]:[0,max/2],values:()=>[]}],
          series:[{},...spec.names.map((label,i)=>({label,stroke:key==='memory'?'#3D6FA8':['#1F7A6C','#7A4FA0'][i],fill:key==='memory'?'#3D6FA818':i?'#7A4FA016':'#1F7A6C20',width:1.5,spanGaps:false,points:{show:false}}))],
          hooks:{setCursor:[u=>{const point=adapted.points[u.cursor.idx];if(point&&active('resources'))select(data.points.indexOf(point));}]}},adapted.data,canvas);
        p.over?.addEventListener('pointerdown',e=>{if(!active('resources')||e.isPrimary===false)return;const r=p.over.getBoundingClientRect(),point=adapted.points[p.posToIdx(e.clientX-r.left)];if(point)select(data.points.indexOf(point));});
        const labels=el('div','resource-axis-labels');for(let i=0;i<5;i++){const v=data.from+(data.to-data.from)*i/4,d=new Date(query.hours===24?Math.round(v/3600000)*3600000:v),pad=n=>String(n).padStart(2,'0');labels.append(el('span','',i===4?'现在':query.hours===168?pad(d.getMonth()+1)+'-'+pad(d.getDate()):(i===0&&query.hours===24?'昨天 ':'')+pad(d.getHours())+':'+pad(d.getMinutes())));}box.append(labels);plots.push(p);if(!plot)plot=p;
        if(Resize){if(!resize)resize=new Resize(()=>{if(active('resources'))plots.forEach(u=>{const n=u.root?.parentNode;if(n)u.setSize({width:Math.max(210,Math.floor(n.getBoundingClientRect().width)),height:u===plots.at(-1)?150:110});});});resize.observe(canvas);}
      }
      select(selected);
    }
    function visibility(){if(doc.hidden){if(tab==='resources'){pending=loading;release();}}else if(active('resources')&&pending){pending=false;request();}else if(active('resources'))renderPlot();}
    // Native close events are queued: an older event must not close a reopened modal.
    const cancel=e=>{e.preventDefault();close();},closed=()=>{if(!dialog.open)close();};
    let backdropPress=false;
    const outside=e=>{const r=dialog.getBoundingClientRect();return e.target===dialog&&(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom);};
    const pointerDown=e=>{backdropPress=e.button===0&&e.isPrimary!==false&&outside(e);};
    const pointerCancel=()=>{backdropPress=false;};
    const backdropClick=e=>{const dismiss=backdropPress&&outside(e);backdropPress=false;if(dismiss)close();};
    dialog.addEventListener('pointerdown',pointerDown);dialog.addEventListener('pointercancel',pointerCancel);dialog.addEventListener('click',backdropClick);
    dialog.addEventListener('cancel',cancel);dialog.addEventListener('close',closed);doc.addEventListener('visibilitychange',visibility);
    return {open,close,update,active,
      sync(){if(current&&!agents.has(current)){close({restoreFocus:false});current=null;data=null;}},
      shutdown(){close({restoreFocus:false});dead=true;release();doc.removeEventListener('visibilitychange',visibility);dialog.removeEventListener('cancel',cancel);dialog.removeEventListener('close',closed);dialog.removeEventListener('pointerdown',pointerDown);dialog.removeEventListener('pointercancel',pointerCancel);dialog.removeEventListener('click',backdropClick);current=null;data=null;},
      snapshot:()=>({id:current,tab,open:dialog.open,loading,points:data?.points.length||0,plot:!!plot,query:{...query},draft:{from:from.value,to:to.value}})};
  }
  const api={create};if(typeof module==='object'&&module.exports)module.exports=api;else root.DeviceDetails=api;
})(globalThis);
