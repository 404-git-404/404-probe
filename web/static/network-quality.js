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
      const lines=text.split('\n');
      card.tip.replaceChildren(el('strong','',lines.shift()),el('span','',lines.length?'\n'+lines.join('\n'):''));card.tip.hidden=false;
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
      const tip=el('div','nq7-tip');tip.hidden=true;tip.setAttribute('role','tooltip');
      content.addEventListener('pointerover',event=>{const cell=event.target?.closest?.('.nq7-b[data-i]');if(cell)showTip(card,cell);});
      content.addEventListener('pointerleave',()=>{tip.hidden=true;});
      faceA.append(head,content,tip);
      Object.assign(card,{faceA,head,sub,tabs,content,tip});
      card.node.append(faceA);
    }
    function buildRow(card,key) {
      const node=el('div','nq7-row'),label=el('span','nq7-label'),mid=el('span','nq7-mid'),blocks=el('span','nq7-blocks'),text=el('span','nq7-text'),right=el('span','nq7-right');
      node.dataset.slot=key;
      const cells=Array.from({length:20},(_,i)=>{const c=el('span','nq7-b');c.dataset.i=String(i);blocks.append(c);return c;});
      mid.append(blocks,text);node.append(label,mid,right);
      const view={node,label,blocks,text,right,cells,tcp:null,icmp:null};
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
        const right=C.tcpRightView(lane);
        const set=(node,value)=>{if(node.textContent!==value)node.textContent=value;};
        set(view.label,label);view.label.title=label;
        view.blocks.hidden=shown.kind!=='blocks';view.text.hidden=shown.kind==='blocks';
        view.node.dataset.lane=shown.kind==='blocks'?(shown.red?'red':'blocks'):shown.key;
        if(shown.kind==='blocks') {
          const counts={green:0,yellow:0,red:0,gray:0};
          shown.blocks.forEach((block,i)=>{
            const cell=view.cells[i],cls=`nq7-b nq7-b-${block.state}${block.in_progress?' is-live':''}`;
            if(cell.className!==cls)cell.className=cls;
            cell.tipText=C.blockView(block,{label,threshold,intervalMS:interval,blockMS:summary.block_ms}).text;cell.title=cell.tipText;counts[block.state]++;
          });
          view.text.title='';
          view.node.setAttribute('aria-label',`${label}，TCP 最近 1 小时：正常 ${counts.green} 格，偶发慢或失败 ${counts.yellow} 格，持续告警 ${counts.red} 格，无数据 ${counts.gray} 格；${right.text}`);
        } else {
          set(view.text,shown.text);view.text.title=shown.title;
          view.node.setAttribute('aria-label',`${label}，${shown.text}；${right.text}`);
        }
        set(view.right,right.text);view.right.title=right.title;view.right.dataset.tone=right.tone;

        view.node.dataset.stale=String(!!card.summaryError&&!!summary);
      }
      for(const [key,view]of card.rows)if(!live.has(key)){view.node.remove();card.rows.delete(key);}
      // One line for the whole card: a failed read says so (the old blocks stay, marked as not current).
      let reason=card.summaryError||card.error||C.cardNotice(card.config);
      if(device)reason=card.agent.removal?'设备正在移除，当前数据不可用':'设备已撤销/暂停，当前数据不可用';
      card.head.title=card.sub.title+' · 正常 / 偶发慢或失败 / 持续告警 / 无数据或样本不足'+(reason?' · '+reason:'');
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
    const dirty=p=>C.isDirty(p.draft,p.baseDraft)||p.draftEnabled!==p.baseEnabled;
    const ispName=value=>C.isps.find(([key])=>key===value)?.[1]||value;
    const focusables=p=>[...p.node.querySelectorAll('button,input,select,summary')].filter(n=>!n.disabled&&!n.hidden&&(!n.closest||!n.closest('[hidden]'))&&!n.parentNode?.hidden);
    function replaceDraft(p,draft,enabled){p.draft=C.clone(draft);p.baseDraft=C.clone(draft);p.baseEnabled=p.draftEnabled=enabled;p.labels=new Map(draft.slots.map(slot=>[slot.slot,{...slot}]));p.present=new Set(draft.slots.filter(slot=>slot.host||slot.slot_enabled).map(slot=>slot.slot));p.newSlots=new Set();}
    function openPanel(card,trigger) {
      if(panel&&!closePanel(false))return;
      const config=card.config,draft=config?C.draftOf(config):{interval_ms:30000,slow_threshold_ms:String(C.THRESHOLD_DEFAULT),slots:C.slots.map(slot=>C.slotDraft({slot,kind:'builtin',region:'',isp:slot,host:'',port:0,slot_enabled:false,ipv4_tcp:false,ipv4_icmp:false,ipv6_tcp:false,ipv6_icmp:false}))};
      const p=panel={card,trigger,config,base:config?.settings_revision,loaded:!!config,saving:false,toggling:false,guard:null,message:'',outdated:false,errors:{},node:el('dialog','nq-overlay')};replaceDraft(p,draft,!!config?.enabled);
      p.node.id='network-quality-settings';p.node.dataset.nqPanel='open';p.node.setAttribute('aria-labelledby','nq-dialog-title');doc.body.append(p.node);renderPanel();
      p.shell=root.DialogShell?.attach(p.node,{decorate:false,requestClose:()=>closePanel()});p.overflow=doc.body.style.overflow;
      if(p.shell)p.shell.open(trigger);else{doc.body.style.overflow='hidden';p.node.showModal();p.node.addEventListener('cancel',e=>{e.preventDefault();closePanel();});}
      const usable=focusables(p);(usable.includes(p.enabled)?p.enabled:usable[0]||p.box).focus({preventScroll:true});loadConfig(card,true);
    }
    function panelAdopt(p,config) {
      if(panel!==p)return;const latest=C.draftOf(config),first=!p.loaded;p.loaded=true;p.config=config;p.latest=config;
      const sameBase=C.sameContent(latest,p.baseDraft)&&config.enabled===p.baseEnabled,sameDraft=C.sameContent(latest,p.draft)&&config.enabled===p.draftEnabled;
      if(first||!dirty(p)||sameDraft){const changed=!first&&!sameBase;p.base=config.settings_revision;replaceDraft(p,latest,config.enabled);if(changed)p.message=sameDraft?'已确认：当前设置已保存':'设置已改变，已载入最新内容';}
      else if(sameBase)p.base=config.settings_revision;
      else if(p.guard!=='conflict'&&p.dismissed!==config.settings_revision){p.guard='conflict';p.message=CONFLICT;}
      if(p.partial){p.message=p.partial+'设备当前'+(config.enabled?'已启用':'未启用')+'检测；请核对后再保存。';p.partial='';}
      renderPanel();
    }
    function closePanel(force=false) {
      if(!panel)return true;
      if((panel.saving||panel.toggling)&&!force){panel.message='正在提交，请等待结果';renderPanel();return false;}
      if(dirty(panel)&&!force){panel.guard='discard';renderPanel();return false;}
      const old=panel;panel=null;old.copyDialog?.close();queue.release(old.copyOwner);queue.release(old.card.panelReadOwner);old.card.configSerial++;old.card.reading=false;old.card.refreshing=null;
      if(old.shell)old.shell.close();else{old.node.close();doc.body.style.overflow=old.overflow;}old.node.remove();if(old.trigger?.isConnected)old.trigger.focus({preventScroll:true});return true;
    }
    function changed(){if(panel){if(panel.guard==='conflict')panel.dismissed=panel.latest?.settings_revision;panel.guard=null;panel.message='';panel.errors={};renderPanel();}}
    const fieldError=(p,slot,field)=>{const code=p.errors[slot]?.[field]||C.hints(p.draft)[slot]?.[field];return code?(C.errorText[code]||'请检查此项'):'';};
    const draftOf=slot=>panel.draft.slots.find(s=>s.slot===slot);
    function editLabels(p,slot){const original=p.labels.get(slot),v=p.slots.get(slot),row=draftOf(slot),region=v.regionText.value,isp=v.ispText.value;
      if(original?.kind==='builtin'&&region===original.region&&isp===ispName(original.isp)){row.kind='builtin';row.region=original.region;row.isp=original.isp;}
      else{row.kind='custom';row.region=region;row.isp=isp;}changed();
    }
    function addSlot(p){const slot=C.slots.find(key=>!p.present.has(key));if(!slot)return;const row=draftOf(slot);Object.assign(row,{kind:'custom',region:'',isp:'',host:'',port:'443',slot_enabled:true,ipv4_tcp:true,ipv4_icmp:true,ipv6_tcp:true,ipv6_icmp:true});p.present.add(slot);p.newSlots.add(slot);p.labels.set(slot,{...row});changed();p.slots.get(slot).regionText.focus({preventScroll:true});}
    function removeSlot(p,slot){Object.assign(draftOf(slot),{kind:'builtin',region:'',isp:slot,host:'',port:'',slot_enabled:false,ipv4_tcp:false,ipv4_icmp:false,ipv6_tcp:false,ipv6_icmp:false});p.present.delete(slot);p.newSlots.delete(slot);changed();}
    function openCopy(p){
      if(p.copyDialog?.open||p.saving||p.toggling)return;const d=el('dialog','dialog-shell dialog-small'),body=el('div','shell-content'),choose=el('select'),status=el('p','nq-muted'),actions=el('div','shell-actions');d.setAttribute('aria-label','从其他设备复制');choose.setAttribute('aria-label','来源设备');
      for(const card of cards.values())if(card.id!==p.card.id&&!card.agent.revoked){const option=el('option','',card.agent.name||card.agent.state?.hostname||'未命名设备');option.value=card.id;choose.append(option);}
      const cancel=button('取消',()=>d.close()),apply=button('复制到当前草稿',async()=>{const id=choose.value;if(!id||!cards.has(id))return;apply.disabled=true;status.textContent='正在读取来源设备…';
        try{const raw=await read(JSON.stringify(['copy-config',id]),p.copyOwner,path(id)+'v4/config'),source=C.adapt(raw);if(panel!==p||!d.open||p.copyDialog!==d)return;if(!cards.has(id)||raw.agent_id!==id)throw Error('来源设备已不可用');
          const copied=C.draftOf(source);p.draft={interval_ms:copied.interval_ms,slow_threshold_ms:copied.slow_threshold_ms,slots:copied.slots.map((row,i)=>({...row,slot:C.slots[i]}))};p.labels=new Map(p.draft.slots.map(row=>[row.slot,{...row}]));p.present=new Set(p.draft.slots.filter(row=>row.host||row.slot_enabled).map(row=>row.slot));p.newSlots=new Set();p.guard=null;p.errors={};p.message='已复制到当前草稿，保存后生效';d.close();renderPanel();
        }catch(error){if(panel===p&&d.open)status.textContent=error.message||'无法读取来源设备';}finally{if(d.open)apply.disabled=!choose.value;}
      },'primary-action');apply.disabled=!choose.value;choose.addEventListener('change',()=>{apply.disabled=!choose.value;});status.textContent=choose.children.length?'只复制间隔、慢阈值和线路内容，会替换当前的线路设置；保存后才生效。':'没有其他可复制的设备。';body.append(choose,status);actions.append(cancel,apply);d.append(body,actions);doc.body.append(d);p.copyDialog=d;p.copyOwner={};
      const shell=root.DialogShell?.attach(d,{title:'从其他设备复制',size:'small',subtitle:p.card.agent.name||''});d.addEventListener('close',()=>{queue.release(p.copyOwner);if(p.copyDialog===d)p.copyDialog=null;d.remove();});if(shell)shell.open(p.copyButton);else d.showModal();choose.focus({preventScroll:true});
    }
    function buildPanel(p) {
      const n=p.node;p.box=el('section','nq-dialog');const header=el('div','nq-dialog-head shell-head'),titleGroup=el('div'),title=el('h2','','网络质量设置'),subtitle=el('p','nq-device',p.card.agent.name||p.card.agent.state?.hostname||'未命名设备');title.id='nq-dialog-title';subtitle.title=subtitle.textContent;titleGroup.append(title,subtitle);
      p.closeButton=button('×',()=>closePanel(),'nq-link');p.closeButton.setAttribute('aria-label','关闭');p.closeButton.dataset.nqField='close';header.append(titleGroup,p.closeButton);
      p.guardDiscard=el('div','nq-status nq-guard');p.guardDiscard.setAttribute('role','alertdialog');p.guardDiscard.append(el('p','','放弃修改？'),button('放弃修改',()=>closePanel(true),'nq-link'),button('继续编辑',()=>{p.guard=null;renderPanel();},'nq-link'));p.guardDiscard.querySelectorAll('button').forEach((b,i)=>{b.dataset.nqField=i?'guard-keep':'guard-discard';});
      p.guardConflict=el('div','nq-status nq-guard');p.guardConflict.setAttribute('role','alertdialog');p.loadLatest=button('载入最新配置',()=>{const latest=p.latest;if(!latest||p.saving||p.toggling)return;p.base=latest.settings_revision;replaceDraft(p,C.draftOf(latest),latest.enabled);p.guard=null;p.errors={};p.message='已载入最新配置，本页草稿已放弃';renderPanel();},'nq-link');p.loadLatest.dataset.nqField='guard-load-latest';const keepDraft=button('继续编辑',()=>{p.dismissed=p.latest?.settings_revision;p.guard=null;p.message='';renderPanel();},'nq-link');keepDraft.dataset.nqField='guard-conflict-cancel';p.guardConflict.append(el('p','',CONFLICT),p.loadLatest,keepDraft);
      p.guardEnable=el('div','nq-status nq-guard');p.guardEnable.setAttribute('role','alertdialog');const enable=button('保存并开启',()=>savePanel({confirmedEnable:true}),'nq-link'),enableCancel=button('取消',()=>{p.guard=null;renderPanel();},'nq-link');enable.dataset.nqField='guard-save-enable';enableCancel.dataset.nqField='guard-cancel';p.guardEnable.append(el('p','','开启后会开始检测这些目标。请仅检测你有权检测的目标。'),enable,enableCancel);
      const body=el('div','nq-dialog-body'),settings=el('div','nq-settings-line'),master=el('label','nq-master');p.enabled=el('input');p.enabled.type='checkbox';p.enabled.dataset.nqField='enabled';p.enabled.addEventListener('change',()=>{p.draftEnabled=p.enabled.checked;changed();});master.append(p.enabled,el('strong','','启用检测'));settings.append(master,el('span','','每隔'));
      p.interval=el('select');p.interval.setAttribute('aria-label','检测间隔');p.interval.dataset.nqField='interval';for(const ms of C.intervals){const option=el('option','',ms/1000+' 秒');option.value=String(ms);p.interval.append(option);}p.interval.addEventListener('change',()=>{p.draft.interval_ms=Number(p.interval.value);changed();});settings.append(p.interval,el('span','','测一次'),el('span','','超过'));
      p.threshold=el('input');p.threshold.setAttribute('aria-label','慢阈值');p.threshold.dataset.nqField='threshold';p.threshold.inputMode='numeric';p.threshold.maxLength=4;p.threshold.addEventListener('input',()=>{p.draft.slow_threshold_ms=p.threshold.value;delete p.errors.threshold;p.guard=null;p.message='';renderPanel();});settings.append(p.threshold,el('span','','ms 算慢'));p.thresholdError=el('small','nq-field-error');p.masterNote=el('small','nq-muted');p.frequencyNote=el('small','nq-muted');body.append(settings,p.masterNote,p.frequencyNote,p.thresholdError);
      p.capabilityNote=el('p','nq-capability-note');body.append(p.capabilityNote);
      const columns=el('div','nq-columns');for(const label of ['地区','运营商','目标地址','TCP 端口','测什么（默认全测）',''])columns.append(el('span','',label));body.append(columns);p.slots=new Map();
      const suggestions=el('datalist');suggestions.id='nq-isp-hints';for(const label of ['电信','联通','移动']){const option=el('option');option.value=label;suggestions.append(option);}body.append(suggestions);
      for(const slot of C.slots){const set=el('fieldset','nq-slot');set.dataset.nqSlot=slot;const regionText=el('input'),ispText=el('input'),hostInput=el('input'),portInput=el('input'),errors=el('small','nq-field-error'),checks=new Map(),subNotes=new Map();errors.setAttribute('role','alert');
        for(const [input,key,label,max]of [[regionText,'region-text','地区',64],[ispText,'isp-text','运营商',64],[hostInput,'host','目标地址',253],[portInput,'port','TCP 端口',5]]){input.dataset.nqField=slot+'-'+key;input.setAttribute('aria-label',label);input.maxLength=max;input.autocomplete='off';}ispText.setAttribute('list','nq-isp-hints');portInput.inputMode='numeric';regionText.addEventListener('input',()=>editLabels(p,slot));ispText.addEventListener('input',()=>editLabels(p,slot));
        hostInput.addEventListener('input',()=>{const row=draftOf(slot);row.host=hostInput.value;if(p.newSlots.has(slot)){const family=C.literalFamily(row.host);for(const [key,,fam]of C.subItems)row[key]=!family||family===fam;}p.guard=null;p.message='';delete p.errors[slot];renderPanel();});portInput.addEventListener('input',()=>{draftOf(slot).port=portInput.value;p.guard=null;p.message='';delete p.errors[slot];renderPanel();});
        const subs=el('div','nq-subs');for(const [key,,family,protocol]of C.subItems){const label=el('label','nq-sub'),input=el('input');input.type='checkbox';input.dataset.nqField=slot+'-'+key;input.setAttribute('aria-label',(family==='ipv4'?'v4 ':'v6 ')+protocol.toUpperCase());input.addEventListener('change',()=>{draftOf(slot)[key]=input.checked;p.newSlots.delete(slot);changed();});label.append(input,el('span','',(family==='ipv4'?'v4 ':'v6 ')+protocol.toUpperCase()));subs.append(label);checks.set(key,input);subNotes.set(key,label);}
        const remove=button('×',()=>removeSlot(p,slot),'nq-remove');remove.setAttribute('aria-label','删除线路 '+(C.slots.indexOf(slot)+1));remove.dataset.nqField=slot+'-remove';const resume=button('启用这条线路',()=>{draftOf(slot).slot_enabled=true;changed();},'nq-link');resume.dataset.nqField=slot+'-resume';set.append(regionText,ispText,hostInput,portInput,subs,remove,errors,resume);body.append(set);p.slots.set(slot,{set,regionText,ispText,hostInput,portInput,checks,subNotes,errors,resume});}
      p.add=button('+ 添加线路',()=>addSlot(p),'nq-add');p.add.dataset.nqField='add';body.append(p.add,el('p','nq-footnote','亮着的就会测，点一下关掉。这台机器没有 IPv6 时自动跳过 v6；容器里测不了 ICMP 时会自动变灰。'),el('p','nq-footnote','只检测你有权检测的目标。地区和运营商是你自己的标注，404-probe 不核对。'));
      p.messageNode=el('p','nq-status');p.messageNode.setAttribute('role','status');p.saveBottom=button('保存',()=>savePanel(),'nq-save');p.saveBottom.dataset.nqField='save-bottom';p.copyButton=button('从其他设备复制…',()=>openCopy(p),'nq-link');p.copyButton.dataset.nqField='copy';const cancel=button('取消',()=>closePanel(),'nq-link');cancel.dataset.nqField='cancel';const foot=el('div','nq-dialog-foot');foot.append(p.copyButton,cancel,p.saveBottom);p.box.tabIndex=-1;p.box.append(header,p.guardDiscard,p.guardEnable,p.guardConflict,body,p.messageNode,foot);n.append(p.box);
      let pressed=false;const outside=e=>{if(e.target!==n)return false;const r=n.getBoundingClientRect();return e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom;};n.addEventListener('pointerdown',e=>{pressed=e.button===0&&outside(e);});n.addEventListener('pointercancel',()=>{pressed=false;});n.addEventListener('click',e=>{const dismiss=pressed&&outside(e);pressed=false;if(dismiss)closePanel();});
    }
    function renderPanel(){if(!panel)return;const p=panel;if(!p.box)buildPanel(p);const config=p.config||p.card.config,locked=p.saving||p.toggling,value=(node,next)=>{if(doc.activeElement!==node&&node.value!==next)node.value=next;};
      p.guardDiscard.hidden=p.guard!=='discard';p.guardEnable.hidden=p.guard!=='enable';p.guardConflict.hidden=p.guard!=='conflict';p.loadLatest.disabled=!p.latest||locked;p.enabled.checked=p.draftEnabled;p.enabled.disabled=locked||p.outdated||!config||!config.supported&&!config.enabled;p.interval.disabled=p.threshold.disabled=locked;
      p.masterNote.textContent=!config?'正在读取设置…':!config.supported?C.cardNotice(config)||'Agent 尚未支持':'';p.masterNote.hidden=!p.masterNote.textContent;value(p.interval,String(p.draft.interval_ms));value(p.threshold,String(p.draft.slow_threshold_ms));p.thresholdError.textContent=(C.errorText[p.errors.threshold||C.thresholdHint(p.draft)]||'');p.thresholdError.hidden=!p.thresholdError.textContent;p.frequencyNote.textContent=config&&config.effective_interval_ms!==config.interval_ms?'实际每隔 '+config.effective_interval_ms/1000+' 秒检测一次（当前负载限制）':'';p.frequencyNote.hidden=!p.frequencyNote.textContent;
      const noV6=config&&[...config.lanes.values()].some(l=>l.status==='no_ipv6'),icmpOff=new Set(['ipv4','ipv6'].filter(f=>config?.icmpSupport?.[f]==='unsupported'));
      p.capabilityNote.textContent=[noV6?'这台机器没有 IPv6，v6 会自动跳过':'',icmpOff.size?'这台机器不支持 '+[...icmpOff].map(f=>(f==='ipv4'?'v4':'v6')+' ICMP').join(' / ')+'，相关开关已置灰，已勾选的仍可取消':''].filter(Boolean).join('；');p.capabilityNote.hidden=!p.capabilityNote.textContent;
      for(const row of p.draft.slots){const v=p.slots.get(row.slot);v.set.hidden=!p.present.has(row.slot);v.set.disabled=locked;value(v.regionText,row.region);value(v.ispText,row.kind==='builtin'?ispName(row.isp):row.isp);v.regionText.title=v.regionText.value;v.ispText.title=v.ispText.value;value(v.hostInput,row.host);value(v.portInput,row.port);const hints=['region','isp','host','port'].map(field=>fieldError(p,row.slot,field));v.errors.textContent=[...new Set(hints.filter(Boolean))].join(' · ');v.errors.hidden=!v.errors.textContent;v.resume.hidden=row.slot_enabled||!row.host;
        const literal=C.disabledSubs(row.host);for(const [key,,family,protocol]of C.subItems){const input=v.checks.get(key),unsupported=protocol==='icmp'&&icmpOff.has(family);input.checked=!!row[key];input.disabled=locked||(literal[key]||unsupported)&&!row[key];input.title=literal[key]?'地址属于另一个地址族，请取消不匹配的勾选':unsupported?'当前环境不支持 ICMP；已选项仍可取消':'';v.subNotes.get(key).dataset.unavailable=String(unsupported);}
      }
      const remaining=C.slots.length-p.present.size;p.add.textContent='+ 添加线路（还可加 '+remaining+' 条）';p.add.disabled=!remaining||locked;p.add.hidden=!remaining;p.copyButton.disabled=locked;p.messageNode.textContent=p.message||(p.outdated?OUTDATED:dirty(p)?'有未保存的修改':'');p.messageNode.hidden=!p.messageNode.textContent;p.saveBottom.textContent=p.saving?'保存中…':'保存';p.saveBottom.disabled=locked||p.outdated||!p.base||!csrf();
    }
    async function submit(p,suffix,payload){const card=p.card;card.configSerial++;card.reading=false;card.refreshing=null;queue.release(card.configOwner);queue.release(card.panelReadOwner);const raw=await write(card,suffix,payload);return {raw,config:C.adapt(raw)};}
    function failed(p,error){if(p.card.dead||panel!==p)return;p.saving=false;p.toggling=false;
      if(error.status===410){p.outdated=true;p.message=OUTDATED;}
      else if(error.status===409&&error.code==='revision_conflict'){p.latest=null;p.dismissed=undefined;p.guard='conflict';p.message=CONFLICT;loadConfig(p.card,true);}
      else if(error.status===409&&error.code==='quality_quota')p.message='目标数量达到上限，请减少检测项目后再试';
      else if(error.status===409)p.message='当前 Agent 不支持自定义目标，请升级后重试';
      else if(error.status===503&&error.code==='storage_pressure')p.message='Server 存储压力大，暂时不能保存';
      else if(error.status===503)p.message='地址检查繁忙，请稍后重试';
      else if(error.status===400){p.errors={};for(const d of error.details||[]){if(d.slot)(p.errors[d.slot]||(p.errors[d.slot]={}))[d.field]=d.code;else if(d.field==='slow_threshold_ms')p.errors.threshold=d.code;}p.message=Object.keys(p.errors).length?'请检查标出的输入':'保存失败：'+(C.errorText[error.details?.[0]?.code]||error.message);}
      else{p.message='保存结果未确认，请核对实际状态';loadConfig(p.card,true);}renderPanel();
    }
    async function savePanel({confirmedEnable=false}={}){const p=panel;if(!p||p.saving||p.toggling||p.outdated||!p.base||!csrf())return;const found=C.hints(p.draft);if(Object.values(found).some(error=>Object.keys(error).length)||C.thresholdHint(p.draft)){p.errors={};p.message='请检查标出的输入';p.guard=null;renderPanel();return;}
      if(p.draftEnabled&&!p.baseEnabled&&!confirmedEnable){p.guard='enable';renderPanel();return;}
      let data;try{data=C.savePayload(p.draft,p.base);if(new TextEncoder().encode(JSON.stringify(data)).length>16384)throw Error('配置内容过长');}catch(error){p.message=error.message;renderPanel();return;}
      const wanted=p.draftEnabled;p.saving=true;p.guard=null;p.message='';p.errors={};let configurationSaved=false;renderPanel();
      try{let {raw,config}=await submit(p,'v4/config',data);if(p.card.dead||panel!==p)return;configurationSaved=true;p.card.raw=raw;p.config=config;p.base=config.settings_revision;p.baseDraft=C.draftOf(config);p.baseEnabled=config.enabled;p.labels=new Map(p.baseDraft.slots.map(row=>[row.slot,{...row}]));bindConfig(p.card,config);
        if(config.enabled!==wanted){const enabledResult=await submit(p,'v4/enabled',C.enabledPayload(config.settings_revision,wanted));if(p.card.dead||panel!==p)return;raw=enabledResult.raw;config=enabledResult.config;}
        p.saving=false;p.card.raw=raw;p.config=config;p.base=config.settings_revision;replaceDraft(p,C.draftOf(config),config.enabled);p.message=config.enabled?'已保存，已按新配置执行':'已保存（未开启检测）';bindConfig(p.card,config);renderPanel();
      }catch(error){if(configurationSaved&&panel===p){p.saving=false;p.partial='线路配置已保存；启用状态未确认。';p.message=p.partial+'正在读取实际状态。';if(error.status===409&&error.code==='revision_conflict')p.guard='conflict';renderPanel();loadConfig(p.card,true);}else failed(p,error);}
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
    function keydown(event){if(event.defaultPrevented||panel?.copyDialog?.open)return;if(event.key==='Escape'&&panel){event.preventDefault();closePanel();}else if(event.key==='Tab'&&panel){
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
      snapshot:()=>({requests:queue.snapshot(),cards:cards.size,panel:panel?{dirty:dirty(panel),base:panel.base,draft:structuredClone(panel.draft),enabled:panel.draftEnabled,guard:panel.guard,message:panel.message,outdated:panel.outdated}:null,history:history?{target:history.target.id,family:history.target.family,hours:history.hours,kind:history.kind}:null}),
      releaseHistory:closeHistory,
      openHistoryFor(id,targetId,family,kind,trigger){const card=cards.get(id),target=card?.config?.targets?.find(t=>t.id===targetId&&t.family===family);if(!target||!['latency','ratio'].includes(kind))return false;
        // Asked for what is already open (the page restores its history after every configuration change): nothing to do.
        if(history?.card===card&&history.target.id===target.id&&history.target.family===target.family&&history.kind===kind)return true;
        openHistory(card,target,kind,trigger);return true;},
      setVisible:(id,value)=>{const card=cards.get(id);if(card)setVisible(card,value);},tick};
  }
  if(typeof module==='object'&&module.exports)module.exports={create};else root.NetworkQuality={create};
})(globalThis);
