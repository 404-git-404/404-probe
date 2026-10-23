/* Narrow card/config/history UI; samples remain Server-owned, never persisted by this page. */
(function(root) {
  'use strict';
  const C=root.NetworkQualityCore||(typeof require==='function'?require('./network-quality-core.js'):null);
  const OUTDATED='页面已过期，请刷新',CONFLICT='配置已被其他页面修改',CONFIG_REFRESH_MS=30000,SUMMARY_FRESH_MS=25000;
  function create({document:doc=root.document,fetcher=(...args)=>root.fetch(...args),csrf=()=>'',unauthorized=()=>root.location.assign('/login'),
    state=root.AgentState,management=root.Management,Plot=root.uPlot,Observer=root.IntersectionObserver,Resize=root.ResizeObserver,
    interval=root.setInterval,clear=root.clearInterval,now=()=>Date.now(),coordinator=null,historyHost=null,onConfig=null,onFamily=null}={}) {
    const cards=new Map(),queue=coordinator||C.coordinator({now}),dialogOwner={};
    let closed=false,panel=null,history=null,plot=null,resize=null,timer=null,wasHidden=doc.hidden;
    const dialog=doc.querySelector('#network-quality-history');
    const historyContainer=()=>historyHost?historyHost.container():dialog;
    const historyActive=h=>!doc.hidden&&(!historyHost||historyHost.active(h.card.id));
    // Every user-supplied string (labels, host) reaches the page through textContent, value or title only.
    const el=(tag,className,text)=>{const n=doc.createElement(tag);if(className)n.className=className;if(text!=null)n.textContent=text;return n;};
    const button=(text,action,className='')=>{const b=el('button',className,text);b.type='button';b.addEventListener('click',action);return b;};
    // gate: asked once more at the moment a queued read really starts; a read nobody needs any more is dropped there.
    const read=(key,owner,path,gate=null)=>queue.request(key,owner,signal=>gate&&!gate()?Promise.reject(Object.assign(new Error('读取已取消'),{name:'AbortError'})):C.transportLease(tracked=>state.fetchJSON(path,{signal,fetcher:tracked,unauthorized,timeoutMs:10000}),fetcher));
    const path=id=>`/api/v1/web/agents/${encodeURIComponent(id)}/network-quality/`;
    const geometryVisible=card=>{const r=card.node.getBoundingClientRect();return card.node.isConnected&&r.bottom>0&&r.top<(root.innerHeight||0)&&r.right>0&&r.left<(root.innerWidth||0);};
    function fallbackVisibility(){if(!observer&&!doc.hidden)for(const card of cards.values())setVisible(card,geometryVisible(card));}
    function setVisible(card,value) {
      const changed=card.visible!==value;
      card.visible=value;
      if(!value&&changed){card.generation++;queue.release(card.historyOwner);card.summaryLoading=false;queue.release(card.configOwner);
        if(panel?.card!==card&&!historyHost?.active(card.id)){card.configSerial++;card.reading=false;card.refreshing=null;}}
      else if(value&&changed)refreshCard(card);
    }
    const observer=Observer?new Observer(entries=>{for(const e of entries){const c=cards.get(e.target.dataset.qualityAgent);if(c)setVisible(c,e.isIntersecting);}},{rootMargin:'0px'}):null;
    function effective(card) {return card.visible&&!doc.hidden&&!card.agent.revoked&&!card.agent.removal;}
    // What decides whether a measurement still belongs to the same series: the measurement revision and the
    // targets with their state. Labels, kind, interval text and the settings revision are not part of it.
    const measurement=config=>JSON.stringify([config.revision,config.targets.map(t=>[t.id,t.slot,t.family,t.protocol,t.slot_revision,t.status,t.lane_status,t.condition])]);
    function bindConfig(card,config) {
      const sameSeries=card.config&&measurement(card.config)===measurement(config);
      card.config=config;card.error='';
      if(sameSeries) {
        // Only labels, kind or settings changed. The blocks are judged against the threshold, so a new settings revision reads them again.
        renderCard(card);
        if(card.summary&&card.summary.settings_revision!==config.settings_revision)readSummary(card,true);
        if(history?.card===card)renderHistory();
        onConfig?.(card.id,config);
        return;
      }
      card.generation++;card.summary=null;card.summaryAt=0;card.summaryLoading=false;queue.release(card.historyOwner);
      // An open history is left alone when the target it shows is still there (the Server reports another lane differently,
      // a state changes): closing it would throw away its place in the page. It closes when its target is gone or replaced.
      if(history?.card===card){const kept=config.targets.find(t=>t.id===history.target.id&&t.slot===history.target.slot&&t.family===history.target.family&&t.protocol===history.target.protocol&&t.slot_revision===history.target.slot_revision);if(kept)history.target=kept;else closeHistory();}
      card.previous=config.targets;
      renderCard(card);refreshCard(card);
      onConfig?.(card.id,config);
    }
    const readError=error=>error?.status===410?OUTDATED:error?.message?.startsWith('v4 配置契约')?'配置读取失败：响应不符合契约，保留上次可信状态':'配置读取失败，保留上次可信状态';
    // The v4 state (negotiation, abnormal targets, capacity) changes on the Server without any action of this page, so a visible
    // card reads it again every 30 seconds, the same pace as its bucket reads; failures back off to at most four minutes.
    const configDue=card=>!card.reading&&!card.refreshing&&!(panel?.card===card&&(panel.saving||panel.toggling))&&now()-(card.configAt||0)>=CONFIG_REFRESH_MS*2**Math.min(card.configFails||0,3);
    async function loadConfig(card,force=false,refresh=false) {
      if(closed||card.dead||(!force&&!refresh&&card.config))return;
      // The periodic refresh has its own job and the card's own owner: it dies with the card's visibility, whatever a dialog or an
      // earlier read of the same card still holds. Reads the user asked for keep the dialog's owner.
      const periodic=refresh&&!force,key=JSON.stringify([periodic?'config-refresh':'config',card.id]),owner=force?card.panelReadOwner:periodic?card.configOwner:panel?.card===card?card.panelReadOwner:card.configOwner;
      const gate=periodic?()=>effective(card)&&(!!observer||geometryVisible(card)):null;
      // Two read paths with state of their own. A read the user asked for (first load, opening the dialog, reading back after a
      // write) is numbered: only the newest one is adopted, and it never depends on any other read. The periodic refresh does not
      // take a number of its own; it is adopted only if nothing numbered has started since, and it is dropped without a trace
      // when its gate says nobody needs it.
      const token={};
      if(periodic){if(card.reading||card.refreshing||!gate())return;card.refreshing=token;}
      else{if(card.reading&&!force)return;card.reading=true;++card.configSerial;}
      const serial=card.configSerial;
      try {
        const raw=await read(key,owner,path(card.id)+'v4/config',gate),value=C.adapt(raw);
        if(closed||card.dead||serial!==card.configSerial)return;
        if(JSON.stringify(card.raw)!==JSON.stringify(raw)){card.raw=raw;bindConfig(card,value);}else card.error='';
        card.configAt=now();card.configFails=0;
        if(panel?.card===card)panelAdopt(panel,value);
      }catch(error){if(!card.dead&&serial===card.configSerial&&error.name!=='AbortError'){card.configAt=now();card.configFails=(card.configFails||0)+1;card.error=readError(error);if(panel?.card===card){panel.message=card.error;panel.outdated=error.status===410;renderPanel();}renderCard(card);}}
      finally{if(periodic){if(card.refreshing===token)card.refreshing=null;}else if(serial===card.configSerial)card.reading=false;card.refreshing=null;}
    }
    function targets(card) {
      return C.slots.flatMap(slot=>['tcp','icmp'].map(protocol=>{
        const actual=(card.config?.targets||[]).find(t=>t.slot===slot&&t.family===card.family&&t.protocol===protocol);
        return actual||{slot,family:card.family,protocol,status:'unavailable'};
      }));
    }
    async function refreshCard(card) {
      if(!effective(card)||closed||card.dead)return;
      if(!card.config){loadConfig(card);return;}
      if(configDue(card))loadConfig(card,false,true); // Shared configuration remains live while face B is shown.
      if(card.face==='A')readSummary(card);
    }
    // The v5 answer is the whole of face A: 20 blocks per lane, the numbers at the right and the alert flag, all judged by the
    // Server. The page keeps the last good answer and says so when a read fails.
    function readSummary(card,force=false) {
      if(card.face!=='A'||!effective(card)||card.dead||card.summaryLoading)return;
      if(!force&&card.summary&&now()-card.summaryAt<SUMMARY_FRESH_MS)return;
      const generation=card.generation,family=card.family;card.summaryLoading=true;
      read(JSON.stringify(['card-v5',card.id,family]),card.historyOwner,`${path(card.id)}v5/card?family=${family}`).then(raw=>{
        if(card.dead||generation!==card.generation||card.family!==family)return;
        card.summary=C.normalizeCard(raw,family);card.summaryAt=now();card.summaryError='';renderCard(card);
      }).catch(error=>{
        if(card.dead||generation!==card.generation||error.name==='AbortError'||error.name==='BackoffError')return;
        card.summaryAt=now();
        card.summaryError=error.status===410?OUTDATED:error.message?.startsWith('v5 卡片契约')?'读取失败：响应不符合契约，显示上次结果（非当前）':'读取失败，显示上次结果（非当前）';
        renderCard(card);
      }).finally(()=>{if(generation===card.generation)card.summaryLoading=false;});
    }
    function family(card,value) {
      if(value===card.family)return;
      try{root.localStorage?.setItem('404-probe:nq-family:'+card.id,value);}catch{}
      card.family=value;card.generation++;card.summary=null;card.summaryError='';card.summaryLoading=false;queue.release(card.historyOwner);
      if(history?.card===card)closeHistory();renderCard(card);refreshCard(card);onFamily?.(card.id,value);
    }
    // ---- face A ----------------------------------------------------------------------------------
    function showTip(card,cell) {
      const text=cell?.tipText;
      if(!text){card.tip.hidden=true;return;}
      card.tip.textContent=text;card.tip.hidden=false;
      const box=card.faceA.getBoundingClientRect(),at=cell.getBoundingClientRect(),width=Math.min(220,Math.max(120,box.width-8));
      card.tip.style.width=width+'px';
      card.tip.style.left=Math.max(0,Math.min(box.width-width,at.left-box.left+at.width/2-width/2))+'px';
      card.tip.style.top=(at.bottom-box.top+4)+'px';
    }
    function buildFaceA(card) {
      const faceA=el('div','nq7-face nq7-a');faceA.dataset.face='A';
      const head=el('div','nq7-head'),title=el('span','nq7-title'),sub=el('span','nq7-sub');
      title.append(el('strong','','网络质量'));
      const tabs=el('div','nq7-seg nq-tabs');for(const [v,text]of [['ipv4','IPv4'],['ipv6','IPv6']]){const b=button(text,()=>family(card,v));b.dataset.focusKey=`nq-${v}`;tabs.append(b);}
      const settings=button('设置',event=>openPanel(card,event.currentTarget),'nq7-btn nq-link');settings.dataset.focusKey='nq-settings';
      head.append(title,sub,tabs,settings);
      const content=el('div','nq7-rows');card.rows=new Map();
      const legend=el('div','nq7-legend');
      for(const [state,text] of [['green','正常'],['yellow','偶发'],['red','持续'],['gray','无数据']]){const key=el('span','nq7-key');key.append(el('span',`nq7-b nq7-b-${state}`),el('span','',text));legend.append(key);}
      const note=el('span','nq7-note','每格 3 分钟');legend.append(note);
      legend.title='正常 · 偶发慢或失败（黄）· 持续慢或失败 = 告警（红）· 无数据或样本不足（灰）';
      const tip=el('div','nq7-tip');tip.hidden=true;tip.setAttribute('role','tooltip');
      content.addEventListener('pointerover',event=>{const cell=event.target?.closest?.('.nq7-b[data-i]');if(cell)showTip(card,cell);});
      content.addEventListener('pointerleave',()=>{tip.hidden=true;});
      faceA.append(head,content,legend,tip);
      Object.assign(card,{faceA,head,sub,tabs,content,legend,note,tip});
      card.node.append(faceA);
    }
    function buildRow(card,key) {
      const node=el('button','nq7-row'),label=el('span','nq7-label'),mid=el('span','nq7-mid'),blocks=el('span','nq7-blocks'),text=el('span','nq7-text'),right=el('span','nq7-right');
      node.type='button';node.dataset.slot=key;node.dataset.focusKey=`nq-row-${key}`;node.title='点击进入详情页的这条线路';
      const cells=Array.from({length:20},(_,i)=>{const c=el('span','nq7-b');c.dataset.i=String(i);blocks.append(c);return c;});
      mid.append(blocks,text);node.append(label,mid,right);
      const view={node,label,blocks,text,right,cells,tcp:null,icmp:null};
      node.addEventListener('click',()=>{const t=view.tcp?.id?view.tcp:view.icmp?.id?view.icmp:null;if(t)openHistory(card,t,t.protocol==='tcp'?'latency':'ratio',node);});
      return view;
    }
    // Before the first answer, or when the device is gone: a lane described by the configuration alone.
    function configLane(card,key) {
      const device=card.agent.revoked||card.agent.disabled_at||card.agent.removal;
      const notice=protocol=>device?{status:'device_inactive',condition:''}:(l=>l?{status:l.status,condition:l.condition||''}:{status:'unset',condition:''})(card.config?.lanes?.get(`${key}/${card.family}/${protocol}`));
      return {slot:key,tcp:{...notice('tcp'),blocks:null,red:false,dns_failing:false},icmp:notice('icmp'),right:{kind:'none',percent:null,numerator:0,denominator:0,samples:'none'}};
    }
    function renderCard(card) {
      if(!card.head)buildFaceA(card);
      const device=!!(card.agent.revoked||card.agent.disabled_at||card.agent.removal),summary=device?null:card.summary;
      for(const b of card.tabs.querySelectorAll('button'))b.setAttribute('aria-pressed',String(b.dataset.focusKey===`nq-${card.family}`));
      const threshold=summary?.threshold_ms??card.config?.slow_threshold_ms??C.THRESHOLD_DEFAULT,interval=summary?.interval_ms??card.config?.effective_interval_ms??30000;
      const subText=`TCP · 1h · 门槛 ${threshold} ms`;
      if(card.sub.textContent!==subText)card.sub.textContent=subText;
      card.sub.title=`只看 TCP 线路（当前所选地址族）· 最近 1 小时，每格 3 分钟 · 慢 = 连接时间超过 ${threshold} ms · 检测间隔 ${interval/1000} 秒${interval>=240000?'；检测间隔较长，部分方块没有样本':''}`;
      const live=new Set();let position=card.content.firstElementChild;
      const selected=targets(card);
      for(const key of C.slots) {
        live.add(key);let view=card.rows.get(key);
        if(!view){view=buildRow(card,key);card.rows.set(key,view);}
        view.tcp=selected.find(t=>t.slot===key&&t.protocol==='tcp');view.icmp=selected.find(t=>t.slot===key&&t.protocol==='icmp');
        if(view.node!==position)card.content.insertBefore(view.node,position);position=view.node.nextElementSibling;
        const label=C.cardLabel(card.config,key),lane=summary?.lanes.find(l=>l.slot===key)||configLane(card,key);
        let shown=C.laneView(lane);
        if(shown.kind==='blocks'&&!lane.tcp.blocks)shown={kind:'text',key:'loading',text:'读取中…',title:'正在读取最近 1 小时的判断'};
        if(lane.tcp.status==='active'&&!summary&&!device)shown={kind:'text',key:'loading',text:card.summaryError||'读取中…',title:card.summaryError||'正在读取最近 1 小时的判断'};
        const right=C.rightView(lane.right);
        const set=(node,value)=>{if(node.textContent!==value)node.textContent=value;};
        set(view.label,label);view.label.title=label;
        view.blocks.hidden=shown.kind!=='blocks';view.text.hidden=shown.kind==='blocks';
        view.node.dataset.lane=shown.kind==='blocks'?(shown.red?'red':'blocks'):shown.key;
        if(shown.kind==='blocks') {
          const counts={green:0,yellow:0,red:0,gray:0};
          shown.blocks.forEach((block,i)=>{
            const cell=view.cells[i],cls=`nq7-b nq7-b-${block.state}${block.in_progress?' is-live':''}`;
            if(cell.className!==cls)cell.className=cls;
            cell.tipText=C.blockView(block,{label,threshold,intervalMS:interval,blockMS:summary.block_ms}).text;counts[block.state]++;
          });
          view.text.title='';
          view.node.setAttribute('aria-label',`${label}，TCP 最近 1 小时：正常 ${counts.green} 格，偶发慢或失败 ${counts.yellow} 格，持续告警 ${counts.red} 格，无数据 ${counts.gray} 格；${right.text}`);
        } else {
          set(view.text,shown.text);view.text.title=shown.title;
          view.node.setAttribute('aria-label',`${label}，${shown.text}；${right.text}`);
        }
        set(view.right,right.text);view.right.title=right.title;view.right.dataset.tone=right.tone;
        view.node.disabled=!(view.tcp?.id||view.icmp?.id);
        view.node.dataset.stale=String(!!card.summaryError&&!!summary);
      }
      for(const [key,view]of card.rows)if(!live.has(key)){view.node.remove();card.rows.delete(key);}
      // One line for the whole card: a failed read says so (the old blocks stay, marked as not current).
      let reason=card.summaryError||card.error||C.cardNotice(card.config);
      if(device)reason=card.agent.removal?'设备正在移除，当前数据不可用':'设备已撤销/暂停，当前数据不可用';
      const note=reason||'每格 3 分钟';
      if(card.note.textContent!==note)card.note.textContent=note;
      card.note.title=reason||`每格 3 分钟 · 检测间隔 ${interval/1000} 秒`;card.note.dataset.alert=String(!!reason);
      card.node.dataset.alertActive=String(!!summary?.alert_active);
      const settings=card.head.querySelector('.nq-link');settings.title='网络质量设置：目标地址、检测频率、慢连接门槛、总开关';
    }
    function mount(agent) {
      let card=cards.get(agent.agent_id);
      if(!card){
        const node=el('section','network-quality');node.dataset.qualityAgent=agent.agent_id;
        let saved='ipv4';try{if(root.localStorage?.getItem('404-probe:nq-family:'+agent.agent_id)==='ipv6')saved='ipv6';}catch{}
        card={id:agent.agent_id,agent,node,family:saved,face:'A',config:null,raw:null,previous:[],generation:0,configSerial:0,visible:false,
          summary:null,summaryAt:0,summaryError:'',summaryLoading:false,
          historyOwner:{},configOwner:{},panelReadOwner:{},writeOwner:{},reading:false,dead:false,error:''};
        cards.set(card.id,card);renderCard(card);observer?.observe(node);
      }
      card.agent=agent;
      // A device that is gone or going: what was read before no longer describes it, and an answer still on its way is not adopted.
      if(agent.revoked||agent.removal){queue.release(card.historyOwner);card.generation++;card.summaryLoading=false;card.summary=null;}
      // Metadata/resource refresh never starts a quality config read or resets a draft.
      if(history?.card===card){const machine=historyContainer()?.querySelector('.nq-machine');if(machine)machine.textContent=agent.name||agent.state?.hostname||'未命名设备';}
      renderCard(card);
      return card.node;
    }
    function retire(card) {
      card.dead=true;card.generation++;card.configSerial++;observer?.unobserve(card.node);
      queue.release(card.historyOwner);queue.release(card.configOwner);queue.release(card.panelReadOwner);queue.release(card.writeOwner);
      if(panel?.card===card)closePanel(true);if(history?.card===card)closeHistory();cards.delete(card.id);
    }
    function sync(ids){const live=new Set(ids);for(const card of cards.values())if(!live.has(card.id))retire(card);}

    // ---- the settings dialog -------------------------------------------------------------------
    // Writes keep the Server's error code and per-input details, which the shared helper drops.
    function write(card,suffix,payload) {
      const key=JSON.stringify(['write',card.id]);
      return queue.request(key,card.writeOwner,signal=>C.transportLease(tracked=>state.fetchJSON(path(card.id)+suffix,{signal,fetcher:async(url,options)=>{
        const response=await tracked(url,{...options,method:'PUT',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf()},body:JSON.stringify(payload)});
        if(response.status===401)return response;
        if(!response.ok) {
          let body;try{body=await response.json();}catch(_){/* the status is authoritative */}
          throw Object.assign(new Error(body?.error?.message||response.statusText||'请求失败'),{status:response.status,code:body?.error?.code||'',details:Array.isArray(body?.error?.details)?body.error.details:[]});
        }
        return response;
      },unauthorized,timeoutMs:10000}),fetcher)).finally(()=>queue.release(card.writeOwner));
    }
    const dirty=p=>C.isDirty(p.draft,p.baseDraft);
    // What the keyboard can reach: enabled, shown controls only (a hidden guard's buttons are not among them).
    const focusables=p=>[...p.node.querySelectorAll('button,input,select')].filter(n=>!n.disabled&&!n.hidden&&!n.parentNode?.hidden);
    function openPanel(card,trigger) {
      if(panel&&!closePanel(false))return;
      const config=card.config;
      panel={card,trigger,config,draft:config?C.draftOf(config):{interval_ms:30000,slow_threshold_ms:String(C.THRESHOLD_DEFAULT),slots:C.slots.map(slot=>C.slotDraft({slot,kind:'builtin',region:'',isp:slot,host:'',port:0,slot_enabled:false,ipv4_tcp:false,ipv4_icmp:false,ipv6_tcp:false,ipv6_icmp:false}))},
        baseDraft:null,base:config?.settings_revision,loaded:!!config,saving:false,toggling:false,guard:null,message:'',outdated:false,errors:{},node:el('div','nq-overlay')};
      panel.baseDraft=C.clone(panel.draft);
      panel.node.dataset.nqPanel='open';
      doc.body.append(panel.node);renderPanel();
      const usable=focusables(panel);((panel.enabled&&usable.includes(panel.enabled)?panel.enabled:usable[0])||panel.box).focus({preventScroll:true});
      loadConfig(card,true);
    }
    // What the Server has now. A clean draft follows it. An edited draft is never merged with it: when the content differs the
    // user is asked whether to load the latest (the draft is dropped) or to keep editing. The base revision then stays as it was,
    // so a save of the kept draft is refused again instead of overwriting the other page's change.
    function panelAdopt(p,config) {
      if(panel!==p)return;
      const latest=C.draftOf(config),first=!p.loaded;
      p.loaded=true;p.config=config;
      if(first){p.base=config.settings_revision;p.baseDraft=latest;p.draft=C.clone(latest);renderPanel();return;}
      p.latest=config;
      if(C.sameContent(latest,p.baseDraft))p.base=config.settings_revision; // the content we edit from: only its number moved
      else if(!dirty(p)){p.base=config.settings_revision;p.baseDraft=latest;p.draft=C.clone(latest);p.message='配置已被修改，已载入最新内容';}
      else if(C.sameContent(latest,p.draft)){p.base=config.settings_revision;p.baseDraft=latest;p.message='已确认：当前内容已保存';} // our own write that was not confirmed
      else if(p.guard!=='conflict'&&p.dismissed!==config.settings_revision){p.guard='conflict';p.message=CONFLICT;}
      renderPanel();
    }
    function closePanel(force=false) {
      if(!panel)return true;
      if((panel.saving||panel.toggling)&&!force){panel.message='正在提交，请等待结果';renderPanel();return false;}
      if(dirty(panel)&&!force){panel.guard='discard';renderPanel();return false;}
      const old=panel;panel=null;queue.release(old.card.panelReadOwner);old.card.configSerial++;old.card.reading=false;old.card.refreshing=null;
      old.node.remove();if(old.trigger?.isConnected)old.trigger.focus({preventScroll:true});return true;
    }
    function changed(){if(panel){if(panel.guard==='conflict')panel.dismissed=panel.latest?.settings_revision;panel.guard=null;panel.message='';panel.errors={};renderPanel();}}
    const fieldError=(p,slot,field)=>{const code=p.errors[slot]?.[field]||C.hints(p.draft)[slot]?.[field];return code?(C.errorText[code]||code):'';};
    function buildPanel(p) {
      const n=p.node;n.className='nq-overlay';
      p.box=el('section','nq-dialog');p.box.setAttribute('role','dialog');p.box.setAttribute('aria-modal','true');p.box.setAttribute('aria-labelledby','nq-dialog-title');
      const header=el('div','nq-dialog-head'),title=el('h2','','网络质量设置');title.id='nq-dialog-title';
      const actions=el('div','nq-dialog-actions');
      p.saveTop=button('保存',()=>savePanel(),'nq-save');p.saveTop.dataset.nqField='save-top';
      p.closeButton=button('关闭',()=>closePanel(),'nq-link');p.closeButton.dataset.nqField='close';
      actions.append(p.saveTop,p.closeButton);header.append(title,actions);
      p.guardDiscard=el('div','nq-status nq-guard');p.guardDiscard.setAttribute('role','alertdialog');
      p.guardDiscard.append(el('p','','放弃修改？'),button('放弃修改',()=>closePanel(true),'nq-link'),button('继续编辑',()=>{p.guard=null;renderPanel();},'nq-link'));
      p.guardDiscard.querySelectorAll('button').forEach((b,i)=>{b.dataset.nqField=i?'guard-keep':'guard-discard';});
      p.guardConflict=el('div','nq-status nq-guard');p.guardConflict.setAttribute('role','alertdialog');
      p.loadLatest=button('载入最新配置',()=>{const latest=p.latest;if(!latest||p.saving||p.toggling)return;p.base=latest.settings_revision;p.baseDraft=C.draftOf(latest);p.draft=C.clone(p.baseDraft);p.guard=null;p.errors={};p.latest=null;p.message='已载入最新配置，本页草稿已放弃';renderPanel();},'nq-link');p.loadLatest.dataset.nqField='guard-load-latest';
      const keepDraft=button('取消',()=>{p.dismissed=p.latest?.settings_revision;p.guard=null;p.message='';renderPanel();},'nq-link');keepDraft.dataset.nqField='guard-conflict-cancel';
      p.guardConflict.append(el('p','',CONFLICT),p.loadLatest,keepDraft);
      p.guardEnable=el('div','nq-status nq-guard');p.guardEnable.setAttribute('role','alertdialog');
      p.guardEnable.append(el('p','','有未保存的修改，先保存吗？'),
        button('保存并开启',()=>savePanel({thenEnable:true}),'nq-link'),
        button('不保存，按上次保存的配置开启',()=>{p.guard=null;setEnabled(true);},'nq-link'),
        button('取消',()=>{p.guard=null;renderPanel();},'nq-link'));
      p.guardEnable.querySelectorAll('button').forEach((b,i)=>{b.dataset.nqField=['guard-save-enable','guard-enable-saved','guard-cancel'][i];});
      const body=el('div','nq-dialog-body');
      // Master switch: takes effect at once and runs the last saved configuration.
      const master=el('label','nq-master');p.enabled=el('input');p.enabled.type='checkbox';p.enabled.dataset.nqField='enabled';
      p.enabled.addEventListener('change',()=>{const on=p.enabled.checked;if(on&&dirty(p)){p.enabled.checked=false;p.guard='enable';renderPanel();}else setEnabled(on);});
      master.append(p.enabled,el('span','','启用检测（立即生效，按最后一次保存的配置执行）'));
      p.masterNote=el('small','nq-muted');
      const freq=el('label','nq-field');p.interval=el('select');p.interval.dataset.nqField='interval';
      for(const ms of C.intervals){const o=el('option','',`${ms/1000} 秒`);o.value=String(ms);p.interval.append(o);}
      p.interval.addEventListener('change',()=>{p.draft.interval_ms=Number(p.interval.value);changed();});
      freq.append(el('span','','检测频率'),p.interval,el('small','nq-muted','30–300 秒，每档 30 秒，3 个卡槽共用'));
      p.frequencyNote=el('small','nq-muted');
      const slow=el('label','nq-field');p.threshold=el('input');p.threshold.dataset.nqField='threshold';p.threshold.inputMode='numeric';p.threshold.maxLength=4;p.threshold.autocomplete='off';
      p.thresholdError=el('small','nq-field-error');p.thresholdError.setAttribute('role','alert');
      p.threshold.addEventListener('input',()=>{p.draft.slow_threshold_ms=p.threshold.value;p.guard=null;p.message='';delete p.errors.threshold;renderPanel();});
      slow.append(el('span','','慢连接门槛（毫秒）'),p.threshold,p.thresholdError,el('small','nq-muted','连接时间超过它算“慢”，50–3000 的整数，3 个卡槽共用，默认 500'));
      body.append(master,p.masterNote,freq,p.frequencyNote,slow,el('p','nq-notice','请仅检测你有权检测的目标'));
      p.slots=new Map();
      for(const [index,slot] of C.slots.entries()) {
        const set=el('fieldset','nq-slot'),legend=el('legend');
        const on=el('input');on.type='checkbox';on.dataset.nqField=`${slot}-enabled`;on.setAttribute('aria-label',`启用卡槽 ${index+1}`);
        on.addEventListener('change',()=>{draftOf(slot).slot_enabled=on.checked;changed();});
        legend.append(on,el('span','',` 卡槽 ${index+1}`));set.append(legend);
        const kinds=el('div','nq-kinds'),radios={};
        for(const [kind,text] of [['builtin','内置'],['custom','自定义']]) {
          const l=el('label','nq-kind'),r=el('input');r.type='radio';r.name=`nq-kind-${slot}`;r.value=kind;r.dataset.nqField=`${slot}-kind-${kind}`;
          r.addEventListener('change',()=>{if(r.checked){const i=p.draft.slots.findIndex(s=>s.slot===slot);p.draft.slots[i]=C.switchKind(p.draft.slots[i],kind);changed();}});
          l.append(r,el('span','',text));kinds.append(l);radios[kind]=r;
        }
        set.append(kinds);
        const region=el('div','nq-field'),regionSelect=el('select'),regionText=el('input');
        regionSelect.dataset.nqField=`${slot}-region-select`;regionText.dataset.nqField=`${slot}-region-text`;regionText.maxLength=64;regionSelect.setAttribute('aria-label','地区');regionText.setAttribute('aria-label','地区（自定义）');
        regionSelect.append(Object.assign(el('option','','请选择地区'),{value:''}));for(const name of C.provinces)regionSelect.append(Object.assign(el('option','',name),{value:name}));
        regionSelect.addEventListener('change',()=>{draftOf(slot).region=regionSelect.value;changed();});
        regionText.addEventListener('input',()=>{draftOf(slot).region=regionText.value;p.guard=null;p.message='';delete p.errors[slot];renderPanel();});
        const regionError=el('small','nq-field-error');regionError.setAttribute('role','alert');
        region.append(el('span','','地区'),regionSelect,regionText,regionError);
        const isp=el('div','nq-field'),ispSelect=el('select'),ispText=el('input');
        ispSelect.dataset.nqField=`${slot}-isp-select`;ispText.dataset.nqField=`${slot}-isp-text`;ispText.maxLength=64;ispSelect.setAttribute('aria-label','ISP');ispText.setAttribute('aria-label','ISP（自定义）');
        ispSelect.append(Object.assign(el('option','','请选择 ISP'),{value:''}));for(const [value,text] of C.isps)ispSelect.append(Object.assign(el('option','',text),{value}));
        ispSelect.addEventListener('change',()=>{draftOf(slot).isp=ispSelect.value;changed();});
        ispText.addEventListener('input',()=>{draftOf(slot).isp=ispText.value;p.guard=null;p.message='';delete p.errors[slot];renderPanel();});
        const ispError=el('small','nq-field-error');ispError.setAttribute('role','alert');
        isp.append(el('span','','ISP'),ispSelect,ispText,ispError);
        set.append(region,isp);
        const host=el('div','nq-field'),hostInput=el('input'),hostError=el('small','nq-field-error');hostInput.dataset.nqField=`${slot}-host`;hostInput.setAttribute('aria-label','目标地址');hostInput.maxLength=253;hostInput.autocomplete='off';hostError.setAttribute('role','alert');
        hostInput.addEventListener('input',()=>{draftOf(slot).host=hostInput.value;p.guard=null;p.message='';delete p.errors[slot];renderPanel();});
        const portInput=el('input'),portError=el('small','nq-field-error');portInput.dataset.nqField=`${slot}-port`;portInput.setAttribute('aria-label','TCP 端口');portInput.inputMode='numeric';portInput.maxLength=5;portError.setAttribute('role','alert');
        portInput.addEventListener('input',()=>{draftOf(slot).port=portInput.value;p.guard=null;p.message='';delete p.errors[slot];renderPanel();});
        host.append(el('span','','目标地址'),hostInput,hostError,el('span','','TCP 端口'),portInput,portError);set.append(host);
        const subs=el('div','nq-subs'),checks=new Map(),subNotes=new Map();
        for(const [key,text] of C.subItems) {
          const l=el('label','nq-sub'),c=el('input');c.type='checkbox';c.dataset.nqField=`${slot}-${key}`;
          c.addEventListener('change',()=>{draftOf(slot)[key]=c.checked;changed();});
          const note=el('small','nq-field-error');subNotes.set(key,note);l.append(c,el('span','',text),note);subs.append(l);checks.set(key,c);
        }
        set.append(subs);body.append(set);
        p.slots.set(slot,{on,radios,regionSelect,regionText,regionError,ispSelect,ispText,ispError,hostInput,hostError,portInput,portError,checks,subNotes});
      }
      body.append(el('small','nq-muted','地区和 ISP 是你的标注，404-probe 不核对。'),el('small','nq-muted','保存只保存配置，不会开启检测。'));
      p.messageNode=el('p','nq-status');p.messageNode.setAttribute('role','status');
      p.saveBottom=button('保存',()=>savePanel(),'nq-save');p.saveBottom.dataset.nqField='save-bottom';
      const foot=el('div','nq-dialog-foot');foot.append(p.messageNode,p.saveBottom);
      p.box.tabIndex=-1;p.box.append(header,p.guardDiscard,p.guardEnable,p.guardConflict,body,foot);n.append(p.box);
      n.addEventListener('pointerdown',event=>{if(event.target===n)closePanel();});
    }
    const draftOf=slot=>panel.draft.slots.find(s=>s.slot===slot);
    function renderPanel() {
      if(!panel)return;const p=panel;
      if(!p.box)buildPanel(p);
      const value=(input,next)=>{if(input.value!==next)input.value=next;};
      p.guardDiscard.hidden=p.guard!=='discard';p.guardEnable.hidden=p.guard!=='enable';p.guardConflict.hidden=p.guard!=='conflict';p.loadLatest.disabled=!p.latest||p.saving||p.toggling;
      const config=p.config||p.card.config;
      p.enabled.checked=!!config?.enabled;p.enabled.disabled=p.toggling||p.saving||p.outdated||!config||!config.supported&&!config.enabled;
      p.masterNote.textContent=!config?'配置尚未读取':!config.supported?C.cardNotice(config)||'Agent 尚未支持':'';
      value(p.interval,String(p.draft.interval_ms));
      if(doc.activeElement!==p.threshold)value(p.threshold,String(p.draft.slow_threshold_ms));
      p.thresholdError.textContent=(code=>code?(C.errorText[code]||code):'')(p.errors.threshold||C.thresholdHint(p.draft));
      p.frequencyNote.textContent=config&&config.effective_interval_ms!==config.interval_ms?`实际间隔 ${config.effective_interval_ms/1000} 秒（Server 容量限制）`:'';
      const hints=C.hints(p.draft);
      // Only the Server's capability answer disables an ICMP box; "unknown" (not negotiated yet, no session) leaves it open.
      const icmpOff=new Set(['ipv4','ipv6'].filter(family=>config?.icmpSupport?.[family]==='unsupported'));
      for(const s of p.draft.slots) {
        const v=p.slots.get(s.slot);v.on.checked=s.slot_enabled;
        v.radios.builtin.checked=s.kind==='builtin';v.radios.custom.checked=s.kind==='custom';
        const builtin=s.kind==='builtin';
        v.regionSelect.hidden=!builtin;v.regionText.hidden=builtin;v.ispSelect.hidden=!builtin;v.ispText.hidden=builtin;
        // An unknown stored value stays selectable so that a saved configuration is never silently changed.
        for(const [select,current] of [[v.regionSelect,s.region],[v.ispSelect,s.isp]]) {
          const known=[...select.children].some(o=>o.value===current);
          select.querySelector('[data-extra]')?.remove();
          if(builtin&&current&&!known){const o=Object.assign(el('option','',current),{value:current});o.dataset.extra='1';select.append(o);}
        }
        value(v.regionSelect,s.region);value(v.ispSelect,s.isp);
        if(doc.activeElement!==v.regionText)value(v.regionText,s.region);if(doc.activeElement!==v.ispText)value(v.ispText,s.isp);
        if(doc.activeElement!==v.hostInput)value(v.hostInput,s.host);if(doc.activeElement!==v.portInput)value(v.portInput,s.port);
        for(const [node,field] of [[v.regionError,'region'],[v.ispError,'isp'],[v.hostError,'host'],[v.portError,'port']])node.textContent=field==='host'||field==='port'||s.host.trim()||p.errors[s.slot]?.[field]?fieldError(p,s.slot,field):'';
        const lockLiteral=C.disabledSubs(s.host);
        for(const [key,,fam,proto] of C.subItems) {
          const box=v.checks.get(key),icmpBlocked=proto==='icmp'&&icmpOff.has(fam);
          // A box can always be unticked; only ticking is refused (other address family, ICMP the host cannot send).
          box.checked=!!s[key];box.disabled=(lockLiteral[key]||icmpBlocked)&&!s[key];
          box.title=lockLiteral[key]?(s[key]?'地址是另一个地址族的字面量，请取消勾选':'地址是另一个地址族的字面量，不能勾选'):icmpBlocked?(s[key]?'当前环境不支持 ICMP，可以取消勾选':'当前环境不支持 ICMP'):'';
          v.subNotes.get(key).textContent=icmpBlocked?'当前环境不支持 ICMP':fieldError(p,s.slot,key);
        }
      }
      const blocked=p.saving||p.toggling||p.outdated||!p.base||!csrf();
      p.messageNode.textContent=p.message||(p.outdated?OUTDATED:dirty(p)?'有未保存的修改':'');
      for(const b of [p.saveTop,p.saveBottom]){b.textContent=p.saving?'保存中…':'保存';b.disabled=blocked;}
    }
    async function submit(p,suffix,payload) {
      const card=p.card;card.configSerial++;card.reading=false;card.refreshing=null;queue.release(card.configOwner);queue.release(card.panelReadOwner);
      const raw=await write(card,suffix,payload);
      return {raw,config:C.adapt(raw)};
    }
    function failed(p,error) {
      if(p.card.dead||panel!==p)return;
      p.saving=false;p.toggling=false;
      if(error.status===410){p.outdated=true;p.message=OUTDATED;}
      else if(error.status===409&&error.code==='revision_conflict'){p.latest=null;p.dismissed=undefined;p.guard='conflict';p.message=CONFLICT;loadConfig(p.card,true);}
      else if(error.status===409&&error.code==='quality_quota')p.message='目标数量达到上限，请减少子项后再试';
      else if(error.status===409)p.message='当前 Agent 不支持自定义目标（需升级或平台不支持）';
      else if(error.status===503&&error.code==='storage_pressure')p.message='Server 存储压力大，暂时不能保存';
      else if(error.status===503)p.message='地址检查繁忙，请稍后重试';
      else if(error.status===400) {
        p.errors={};for(const d of error.details||[]){if(d.slot){(p.errors[d.slot]||(p.errors[d.slot]={}))[d.field]=d.code;}else if(d.field==='slow_threshold_ms')p.errors.threshold=d.code;}
        p.message=Object.keys(p.errors).length?'请检查标出的输入':`保存失败：${C.errorText[error.details?.[0]?.code]||error.message}`;
      }
      else {p.message=`未确认保存成功：${error.message||'网络错误'}`;loadConfig(p.card,true);}
      renderPanel();
    }
    async function savePanel({thenEnable=false}={}) {
      const p=panel;if(!p||p.saving||p.toggling||p.outdated||!p.base||!csrf())return;
      const found=C.hints(p.draft);if(Object.values(found).some(e=>Object.keys(e).length)||C.thresholdHint(p.draft)){p.errors={};p.message='请检查标出的输入';p.guard=null;renderPanel();return;}
      let data;try{data=C.savePayload(p.draft,p.base);if(new TextEncoder().encode(JSON.stringify(data)).length>16384)throw Error('配置超过 16384 字节');}
      catch(error){p.message=error.message;renderPanel();return;}
      p.saving=true;p.guard=null;p.message='';p.errors={};renderPanel();
      try {
        const {raw,config}=await submit(p,'v4/config',data);
        if(p.card.dead||panel!==p)return;
        p.saving=false;p.card.raw=raw;p.config=config;p.base=config.settings_revision;p.baseDraft=C.draftOf(config);p.draft=C.clone(p.baseDraft);
        p.message=config.enabled?'已保存，已按新配置执行':'已保存（未开启检测）';
        bindConfig(p.card,config);renderPanel();
        if(thenEnable)await setEnabled(true);
      }catch(error){failed(p,error);}
    }
    // The master switch is a separate request. Switching on needs the revision the user has seen; switching off never does.
    async function setEnabled(on) {
      const p=panel;if(!p||p.saving||p.toggling||p.outdated||!p.base||!csrf())return;
      let data;try{data=C.enabledPayload(p.base,on);}catch(error){p.message=error.message;renderPanel();return;}
      p.toggling=true;p.message='';renderPanel();
      try {
        const {raw,config}=await submit(p,'v4/enabled',data);
        if(p.card.dead||panel!==p)return;
        p.toggling=false;p.card.raw=raw;
        const before=p.baseDraft,contentChanged=JSON.stringify(C.draftOf(config))!==JSON.stringify(before);
        panelAdopt(p,config);
        if(!contentChanged)p.message=on?'已开启，按最后一次保存的配置执行':'已关闭';
        bindConfig(p.card,config);renderPanel();
      }catch(error){failed(p,error);}
    }
    function destroyPlot(){resize?.disconnect();resize=null;plot?.destroy();plot=null;}
    function closeHistory(){if(!history)return;const old=history;history=null;queue.release(dialogOwner);destroyPlot();if(historyHost){historyContainer()?.replaceChildren();return;}if(dialog.open)dialog.close();doc.body.style.overflow=old.overflow;old.trigger?.isConnected&&old.trigger.focus();root.scrollTo?.(old.scrollX,old.scrollY);}
    function openHistory(card,target,kind,trigger) {
      if(!target.id)return;
      closeHistory();if(historyHost)historyHost.open(card.id,trigger,{target:target.id,family:target.family,kind});
      history={card,target,kind,hours:1,generation:0,data:null,error:'',loading:false,trigger,overflow:doc.body.style.overflow,scrollX:root.scrollX||0,scrollY:root.scrollY||0};
      if(!historyHost)doc.body.style.overflow='hidden';renderHistory();if(!historyHost)dialog.showModal();requestHistory();
    }
    function changeHistory(key,value){if(!history||history[key]===value)return;history[key]=value;history.generation++;history.data=null;history.loading=false;history.error='';queue.release(dialogOwner);destroyPlot();renderHistory();requestHistory();}
    async function requestHistory(){
      const h=history;if(!h||h.loading||!historyActive(h)||closed)return;h.loading=true;const generation=h.generation;
      try {
        const value=await read(C.historyKey(h.card.id,h.target.id,h.target.family,h.hours),dialogOwner,`${path(h.card.id)}v3/history?target_id=${encodeURIComponent(h.target.id)}&view=${h.hours===1?'1hour':'1day'}`);
        if(history!==h||h.generation!==generation||!historyActive(h)||value.target?.id!==h.target.id||value.target?.family!==h.target.family)return;
        const group=value.groups?.find(g=>g.semantics==='staged_probe_v2'&&g.metric===(h.target.protocol==='tcp'?'pure_connect':'echo_rtt'));
        h.data={...value,window:group?.window||{},blocks:(group?.points||[]).map(p=>({...p,gap:p.execution_coverage==='known_gap'}))};h.error='';renderHistory();
      }catch(error){if(history===h&&h.generation===generation&&error.name!=='AbortError'){h.error='读取失败；保留上次结果，不代表当前状态';renderHistory();}}
      finally{if(history===h&&h.generation===generation)h.loading=false;}
    }
    // One axis, one number of decimals (1.36 ... 1.52 ms must not read 1.5 / 1.52), and a width taken from the labels themselves.
    const AXIS_FONT='12px system-ui,sans-serif';let measure;
    const decimals=n=>{const t=String(+n.toFixed(6));return t.includes('.')?t.split('.')[1].length:0;};
    function axisLabels(ticks,kind){
      if(kind==='ratio')return ticks.map(v=>`${v}%`);
      const d=Math.min(4,Math.max(0,...ticks.map(decimals)));
      return ticks.map(v=>`${v.toFixed(d)} ms`);
    }
    function axisWidth(values){
      measure=measure||(doc.createElement('canvas').getContext?.('2d')||null);
      if(!measure||!values)return 64;
      measure.font=AXIS_FONT;
      return Math.ceil(Math.max(0,...values.filter(Boolean).map(t=>measure.measureText(t).width)))+18; // tick mark and gap
    }
    // Rebuilding the history empties its container for a moment; when the page that scrolls it is then shorter than its own
    // viewport the browser drops the scroll position to the top, every 30 s. Every scrolled ancestor gets its position back.
    function renderHistory(){
      const kept=[];
      for(let n=history?historyContainer():null;n&&n.nodeType===1;n=n.parentNode)if(typeof n.scrollTop==='number'&&n.scrollTop>0)kept.push([n,n.scrollTop]);
      drawHistory();
      const restore=()=>{for(const [n,top] of kept)if(n.scrollTop!==top)n.scrollTop=top;};
      restore();
      // The chart's legend settles a frame later and makes the page taller again: put the position back once more then.
      root.requestAnimationFrame?.(restore);
    }
    function drawHistory(){
      if(!history||!historyActive(history))return;const dialog=historyContainer();if(!dialog)return;
      const h=history,focused=dialog.contains(doc.activeElement)?doc.activeElement?.dataset?.nqField:null;destroyPlot();
      const header=el('div','nq-history-head');const title=el('div');const heading=el('h2','',historyHost?'网络质量':'设备详情');heading.id='nq-history-title';
      title.append(heading,el('p','nq-machine',h.card.agent.name||h.card.agent.state?.hostname||'未命名设备'),el('strong','',`网络质量 · ${C.slotLabel(h.card.config,h.target.slot)} · ${h.target.protocol.toUpperCase()} · ${h.target.family==='ipv4'?'V4':'V6'}`));
      const close=button('关闭',historyHost?()=>historyHost.close():closeHistory,'nq-close');close.dataset.nqField='history-close';header.append(title,close);
      const controls=el('div','nq-history-controls');
      for(const [v,text]of [[1,'1 小时'],[24,'24 小时']]){const b=button(text,()=>changeHistory('hours',v));b.dataset.nqField=`hours-${v}`;b.setAttribute('aria-pressed',String(h.hours===v));controls.append(b);}
      for(const [kind,text]of [['latency','延迟'],['ratio',C.ratio({},h.target.protocol).label]]){const b=button(text,()=>changeHistory('kind',kind));b.dataset.nqField=`kind-${kind}`;b.setAttribute('aria-pressed',String(h.kind===kind));controls.append(b);}
      const stats=h.data?.window||{},r=C.ratio(stats,h.target.protocol);
      const summary=el('p','nq-window',`窗口统计：P50 ${C.ms(stats.p50_ms)} · P95 ${C.ms(stats.p95_ms)} · P95−P50 ${C.ms(stats.p95_minus_p50_ms)} · Jitter ${C.ms(stats.jitter_ms)}（${stats.jitter_pairs||0} 对） · ${r.label} ${C.percent(r.value)} (${r.fraction}) · 端点成功 ${stats.probe_successes||0}/${stats.probe_attempts||0} · 样本 ${stats.count||0} · 未执行 ${stats.not_executed||0}`);
      const status=el('p','nq-status',h.error||(!h.data?'正在读取指定目标…':`${C.cardGroup(h.data,h.target)?.stale?'当前过期；历史独立保留 · ':''}${stats.completeness||'no_data'} · ${stats.execution_coverage||'unverified'}（不承诺完整采集）${h.data.migration_pending?' · 正在回填':''} · Server ${C.time(h.data.now)}`));
      const chart=el('div','nq-plot'),tooltip=el('output','nq-chart-detail');tooltip.setAttribute('aria-live','polite');
      dialog.replaceChildren(header,controls,summary,status,el('small','nq-muted','每点为 Server 真实时间桶聚合；1h 为五分钟桶，24h 为一分钟桶；仅 staged_probe_v2，TCP 为纯 Connect。缺测断线，不是原始每30秒点。'),chart,tooltip);
      if(focused)dialog.querySelector(`[data-nq-field="${focused}"]`)?.focus();
      const adapted=C.columns(h.data?.blocks,h.target.protocol,h.kind),data=adapted.data;
      const measured=data[0].filter((_x,i)=>data.slice(1).some(col=>C.finite(col[i]))).length;
      if(measured<2){chart.append(el('p','nq-empty',measured===1?'只有一个真实测量时间桶；不补点绘图':'暂无可绘制测量数据'));return;}
      if(!Plot){chart.append(el('p','nq-empty','本地图表库未加载'));return;}
      const color=root.getComputedStyle?.(dialog)?.color||'#dfd8cc';
      const width=()=>Math.max(240,Math.floor(chart.getBoundingClientRect().width));
      plot=new Plot({width:width(),height:320,legend:{show:true},cursor:{drag:{x:false,y:false}},
        scales:{x:{time:true},y:h.kind==='ratio'?{range:()=>[0,100]}:{auto:true}},
        axes:[{stroke:color,grid:{stroke:'#8883'}},{stroke:color,font:AXIS_FONT,grid:{stroke:'#8883'},values:(_u,ticks)=>axisLabels(ticks,h.kind),size:(_u,values)=>axisWidth(values)}],
        series:[{},...data.slice(1).map((_col,i)=>({label:h.kind==='latency'?(i===0?'P50':'P95'):r.label,stroke:i===0?'#65b9a6':'#dfaa65',spanGaps:false,width:2}))],
        hooks:{setCursor:[u=>{const b=adapted.blocks[u.cursor.idx];tooltip.textContent=b?C.blockText(b,h.target.protocol):'';}]}
      },data,chart);
      if(Resize){resize=new Resize(()=>{if(history===h&&historyActive(h)&&plot)plot.setSize({width:width(),height:320});});resize.observe(chart);}
    }
    function keydown(event){if(event.key==='Escape'&&panel){event.preventDefault();closePanel();}else if(event.key==='Tab'&&panel){
      const nodes=focusables(panel),first=nodes[0],last=nodes.at(-1),active=doc.activeElement;
      if(!nodes.length){event.preventDefault();panel.box.focus();}
      else if(!panel.node.contains(active)){event.preventDefault();(event.shiftKey?last:first).focus();}
      else if(event.shiftKey&&(active===first||active===panel.box)){event.preventDefault();last.focus();}else if(!event.shiftKey&&active===last){event.preventDefault();first.focus();}
    }}
    function tick(){if(doc.hidden||closed)return;fallbackVisibility();queue.pump();for(const card of cards.values())refreshCard(card);requestHistory();}
    function visibility(){if(wasHidden===doc.hidden)return;wasHidden=doc.hidden;if(doc.hidden){for(const card of cards.values()){card.generation++;card.summaryLoading=false;queue.release(card.historyOwner);queue.release(card.configOwner);queue.release(card.panelReadOwner);card.configSerial++;card.reading=false;card.refreshing=null;}if(history){history.generation++;history.loading=false;queue.release(dialogOwner);destroyPlot();}if(timer){clear(timer);timer=null;}}
      else{if(!timer)timer=interval(tick,30000);tick();}}
    const cancelDialog=event=>{event.preventDefault();closeHistory();};
    const dialogClosed=()=>closeHistory();const backdrop=event=>{if(event.target===dialog){const rect=dialog.getBoundingClientRect();if(event.clientX<rect.left||event.clientX>rect.right||event.clientY<rect.top||event.clientY>rect.bottom)closeHistory();}};
    doc.addEventListener('keydown',keydown);doc.addEventListener('visibilitychange',visibility);
    if(!observer){root.addEventListener?.('scroll',fallbackVisibility,{passive:true});root.addEventListener?.('resize',fallbackVisibility,{passive:true});}
    dialog.addEventListener('cancel',cancelDialog);dialog.addEventListener('close',dialogClosed);dialog.addEventListener('click',backdrop);
    if(!doc.hidden)timer=interval(tick,30000);
    return {mount,sync,
      // Face A reads the v5 answer only while it is shown; the shared configuration stays live on either face.
      setFace(id,face){const c=cards.get(id);if(!c||c.face===face)return;c.face=face;c.node.dataset.face=face;
        c.faceA.hidden=face!=='A';c.faceA.inert=face!=='A';c.tip.hidden=true;c.generation++;c.summaryLoading=false;queue.release(c.historyOwner);
        if(face==='A')refreshCard(c);},
      faceNode:id=>cards.get(id)?.faceA,
      familyOf:id=>cards.get(id)?.family||'ipv4',
      setFamily(id,value){const c=cards.get(id);if(c&&['ipv4','ipv6'].includes(value))family(c,value);},
      configOf:id=>cards.get(id)?.config||null,
      openSettings(id,trigger){const c=cards.get(id);if(c)openPanel(c,trigger);},
      historyTargets(id){const c=cards.get(id);return (c?.config?.targets||[]).filter(t=>t.id).map(t=>({...t,label:`${C.slotLabel(c.config,t.slot)} · ${t.protocol.toUpperCase()} · ${t.family==='ipv4'?'V4':'V6'}${t.status==='active'?'':' · 当前未检测'}`}));},
      ensureHistoryTargets(id){const c=cards.get(id);return c&&!c.config?loadConfig(c,true):Promise.resolve();},
      close(){closed=true;if(timer)clear(timer);observer?.disconnect();closePanel(true);closeHistory();for(const c of [...cards.values()])retire(c);queue.release(dialogOwner);if(!coordinator)queue.close();
      doc.removeEventListener('keydown',keydown);doc.removeEventListener('visibilitychange',visibility);
      if(!observer){root.removeEventListener?.('scroll',fallbackVisibility);root.removeEventListener?.('resize',fallbackVisibility);}
      dialog.removeEventListener('cancel',cancelDialog);dialog.removeEventListener('close',dialogClosed);dialog.removeEventListener('click',backdrop);},
      // Production lifecycle hooks exposed for deterministic synthetic verification.
      snapshot:()=>({requests:queue.snapshot(),cards:cards.size,panel:panel?{dirty:dirty(panel),base:panel.base,draft:structuredClone(panel.draft),guard:panel.guard,message:panel.message,outdated:panel.outdated}:null,history:history?{target:history.target.id,family:history.target.family,hours:history.hours,kind:history.kind}:null}),
      releaseHistory:closeHistory,
      openHistoryFor(id,targetId,family,kind,trigger){const card=cards.get(id),target=card?.config?.targets?.find(t=>t.id===targetId&&t.family===family);if(!target||!['latency','ratio'].includes(kind))return false;
        // Asked for what is already open (the page restores its history after every configuration change): nothing to do.
        if(history?.card===card&&history.target.id===target.id&&history.target.family===target.family&&history.kind===kind)return true;
        openHistory(card,target,kind,trigger);return true;},
      setVisible:(id,value)=>{const card=cards.get(id);if(card)setVisible(card,value);},tick};
  }
  if(typeof module==='object'&&module.exports)module.exports={create};else root.NetworkQuality={create};
})(globalThis);
