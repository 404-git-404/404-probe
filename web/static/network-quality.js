/* Narrow card/config/history UI; samples remain Server-owned, never persisted by this page. */
(function(root) {
  'use strict';
  const C=root.NetworkQualityCore||(typeof require==='function'?require('./network-quality-core.js'):null);
  const OUTDATED='页面已过期，请刷新',CONFLICT='配置已被其他页面修改',CONFIG_REFRESH_MS=30000;
  function create({document:doc=root.document,fetcher=(...args)=>root.fetch(...args),csrf=()=>'',unauthorized=()=>root.location.assign('/login'),
    state=root.AgentState,management=root.Management,Plot=root.uPlot,Observer=root.IntersectionObserver,Resize=root.ResizeObserver,
    interval=root.setInterval,clear=root.clearInterval,now=()=>Date.now(),coordinator=null,historyHost=null,onConfig=null}={}) {
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
      if(!value&&changed){card.generation++;queue.release(card.historyOwner);card.loading.clear();queue.release(card.configOwner);
        if(panel?.card!==card&&!historyHost?.active(card.id)){card.configSerial++;card.reading=false;}}
      else if(value&&changed)refreshCard(card);
    }
    const observer=Observer?new Observer(entries=>{for(const e of entries){const c=cards.get(e.target.dataset.qualityAgent);if(c)setVisible(c,e.isIntersecting);}},{rootMargin:'0px'}):null;
    function effective(card) {return card.visible&&!doc.hidden&&!card.agent.revoked;}
    // What decides whether a measurement still belongs to the same series: the measurement revision and the
    // targets with their state. Labels, kind, interval text and the settings revision are not part of it.
    const measurement=config=>JSON.stringify([config.revision,config.targets.map(t=>[t.id,t.slot,t.family,t.protocol,t.slot_revision,t.status,t.lane_status,t.condition])]);
    function bindConfig(card,config) {
      const sameSeries=card.config&&measurement(card.config)===measurement(config);
      card.config=config;card.error='';
      if(sameSeries) {
        // Only labels, kind or settings changed: curves and the twelve cells stay, nothing is read again.
        renderCard(card);
        if(history?.card===card)renderHistory();
        onConfig?.(card.id,config);
        return;
      }
      const keep=new Set(config.targets.filter(t=>card.previous?.some(p=>p.id===t.id&&p.family===t.family&&p.protocol===t.protocol&&p.slot===t.slot&&p.slot_revision===t.slot_revision&&p.status===t.status)).map(t=>t.id));
      card.data=new Map([...card.data].filter(([id])=>keep.has(id)));
      card.generation++;card.loading.clear();queue.release(card.historyOwner);
      if(history?.card===card)closeHistory();
      card.previous=config.targets;
      renderCard(card);refreshCard(card);
      onConfig?.(card.id,config);
    }
    const readError=error=>error?.status===410?OUTDATED:error?.message?.startsWith('v4 配置契约')?'配置读取失败：响应不符合契约，保留上次可信状态':'配置读取失败，保留上次可信状态';
    // The v4 state (negotiation, abnormal targets, capacity) changes on the Server without any action of this page, so a visible
    // card reads it again every 30 seconds, the same pace as its bucket reads; failures back off to at most four minutes.
    const configDue=card=>!card.reading&&!(panel?.card===card&&(panel.saving||panel.toggling))&&now()-(card.configAt||0)>=CONFIG_REFRESH_MS*2**Math.min(card.configFails||0,3);
    async function loadConfig(card,force=false,refresh=false) {
      if(closed||card.dead||(!force&&!refresh&&card.config))return;
      // The periodic refresh has its own job and the card's own owner: it dies with the card's visibility, whatever a dialog or an
      // earlier read of the same card still holds. Reads the user asked for keep the dialog's owner.
      const periodic=refresh&&!force,key=JSON.stringify([periodic?'config-refresh':'config',card.id]),owner=force?card.panelReadOwner:periodic?card.configOwner:panel?.card===card?card.panelReadOwner:card.configOwner;
      const gate=periodic?()=>effective(card)&&(!!observer||geometryVisible(card)):null;
      if(card.reading){if(force)read(key,owner,path(card.id)+'v4/config').catch(()=>{});return;}
      if(gate&&!gate())return;
      card.reading=true;const serial=++card.configSerial;
      try {
        const raw=await read(key,owner,path(card.id)+'v4/config',gate),value=C.adapt(raw);
        if(closed||card.dead||serial!==card.configSerial)return;
        if(JSON.stringify(card.raw)!==JSON.stringify(raw)){card.raw=raw;bindConfig(card,value);}else card.error='';
        card.configAt=now();card.configFails=0;
        if(panel?.card===card)panelAdopt(panel,value);
      }catch(error){if(!card.dead&&serial===card.configSerial&&error.name!=='AbortError'){card.configAt=now();card.configFails=(card.configFails||0)+1;card.error=readError(error);if(panel?.card===card){panel.message=card.error;panel.outdated=error.status===410;renderPanel();}renderCard(card);}}
      finally{if(serial===card.configSerial)card.reading=false;}
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
      if(configDue(card))loadConfig(card,false,true);
      if(card.graph)return; // Shared configuration remains live while bucket reads sleep.
      const generation=card.generation;
      // Only a lane that is measuring has a series worth reading; a switched-off or unset one shows its reason.
      for(const target of targets(card).filter(t=>t.id&&t.status==='active')) {
        if(card.loading.has(target.id))continue;
        const cached=card.data.get(target.id);if(cached&&now()-cached.updated<30000)continue;
        const key=JSON.stringify(['card-history',card.id,target.id,card.family]);card.loading.add(target.id);
        read(key,card.historyOwner,`${path(card.id)}v3/card-history?target_id=${encodeURIComponent(target.id)}`).then(value=>{
          if(card.dead||generation!==card.generation||!effective(card)||value.target?.id!==target.id||value.target?.family!==card.family||value.target?.protocol!==target.protocol||value.target?.slot!==target.slot)return;
          card.data.set(target.id,{value,updated:now(),error:''});renderCard(card);
        }).catch(error=>{
          if(!card.dead&&generation===card.generation&&error.name!=='AbortError'){
            const old=card.data.get(target.id);card.data.set(target.id,{...old,updated:now(),error:error.status===410?OUTDATED:'历史读取失败，显示上次结果（非当前）'});renderCard(card);
          }
        }).finally(()=>{if(generation===card.generation)card.loading.delete(target.id);});
      }
    }
    function family(card,value) {
      if(value===card.family)return;
      try{root.localStorage?.setItem('404-probe:nq-family:'+card.id,value);}catch{}
      card.family=value;card.generation++;card.data.clear();card.loading.clear();queue.release(card.historyOwner);
      if(history?.card===card)closeHistory();renderCard(card);refreshCard(card);
    }
    function updateStrip(row,blocks,target,kind) {
      const incoming=(blocks||[]).slice(-12);
      row.setAttribute('aria-label',`${kind==='latency'?'TCP Connect P95':'ICMP 真实丢包'}，最近12个五分钟桶，最后一桶进行中`);
      if(!incoming.length){if(!row.querySelector('.nq-unknown-slot'))row.replaceChildren(el('small','nq-muted nq-unknown-slot','未知'));return;}
      row.querySelector('.nq-muted')?.remove();
      for(let i=0;i<incoming.length;i++) {
        const block=incoming[i],display=C.cardBlock(block,target);
        let wrap=row.children[i];
        if(!wrap){
          wrap=el('span','nq-block-wrap');const square=button('',()=>{square.focus();note.textContent=square.title;}),note=el('span','nq-bucket-note');
          square.addEventListener('focus',()=>{note.textContent=square.title;});square.addEventListener('blur',()=>{note.textContent='';});
          square.addEventListener('pointerenter',()=>{note.textContent=square.title;});square.addEventListener('pointerleave',()=>{if(doc.activeElement!==square)note.textContent='';});
          wrap.append(square,note);row.append(wrap);
        }
        const square=wrap.firstElementChild,text=display.text;square.className=`nq-block nq-${display.tone}`;square.textContent=display.mark;square.dataset.bucketStart=String(block.start);
        if(square.title!==text){square.title=text;square.setAttribute('aria-label',text);if(doc.activeElement===square)wrap.lastElementChild.textContent=text;}
      }
      while(row.children.length>incoming.length)row.lastElementChild.remove();
    }
    function renderCard(card) {
      if(!card.head) {
        const head=el('div','nq-head'),title=el('button','nq-title dv-switch');title.type='button';title.dataset.dvToggle='C';title.append(el('strong','','网络质量'),el('span','nq-subtitle','TCP 延迟 | ICMP 丢包'));head.append(title);
        const settings=button('设置',event=>openPanel(card,event.currentTarget),'nq-link');settings.dataset.focusKey='nq-settings';head.append(settings);
        const tabs=el('div','nq-tabs');for(const [v,text]of [['ipv4','IPv4'],['ipv6','IPv6']]){const b=button(text,()=>family(card,v));b.dataset.focusKey=`nq-${v}`;tabs.append(b);}head.append(tabs);
        card.head=head;card.reason=el('p','nq-status');card.reason.id='nq-status-'+card.id;card.title=title;
        title.tabIndex=0;title.dataset.focusKey='nq-status';title.setAttribute('aria-describedby',card.reason.id);
        card.content=el('div','nq-rows');card.rows=new Map();
        card.node.append(head,card.reason,card.content);
      }
      for(const b of card.head.querySelectorAll('.nq-tabs button'))b.setAttribute('aria-pressed',String(b.dataset.focusKey===`nq-${card.family}`));
      const live=new Set();let position=card.content.firstElementChild;
      const selected=targets(card);
      for(const key of C.slots) {
        const tcp=selected.find(t=>t.slot===key&&t.protocol==='tcp'),icmp=selected.find(t=>t.slot===key&&t.protocol==='icmp');
        live.add(key);let view=card.rows.get(key);
        if(!view){
          view={node:el('section','nq-row'),label:el('div','nq-target'),latencyStrip:el('div','nq-blocks nq-tcp-blocks'),ratioStrip:el('div','nq-blocks nq-icmp-blocks'),note:el('small','nq-muted'),divider:el('span','nq-divider')};
          view.divider.setAttribute('aria-hidden','true');
          view.latency=el('span','nq-metric nq-tcp-value');
          view.ratio=el('span','nq-metric nq-icmp-value');
          view.latency.dataset.focusKey=`nq-${key}-latency`;view.ratio.dataset.focusKey=`nq-${key}-ratio`;
          view.node.append(view.label,view.latency,view.latencyStrip,view.divider,view.ratioStrip,view.ratio,view.note);card.rows.set(key,view);
        }
        view.tcp=tcp;view.icmp=icmp;
        if(view.node!==position)card.content.insertBefore(view.node,position);position=view.node.nextElementSibling;
        const text=(node,value)=>{if(node.textContent!==value)node.textContent=value;};
        const rowLabel=C.slotLabel(card.config,key);
        text(view.label,rowLabel);view.label.title=`${rowLabel} · TCP/ICMP 各自 target`;
        for(const [target,metric,strip,kind] of [[tcp,view.latency,view.latencyStrip,'latency'],[icmp,view.ratio,view.ratioStrip,'ratio']]) {
          const cached=card.data.get(target.id),h=cached?.value,old=!!cached?.error||card.agent.online===false||!!card.agent.state?.stale;
          const notice=card.config?C.laneNotice(card.config,key,card.family,target.protocol):{key:'waiting',text:'等待配置',detail:'配置尚未读取'};
          // A lane that is not measuring says why; an abnormal target says so and keeps its history.
          const display=notice&&!notice.measured?notice:notice?{...notice}:C.cardCurrent(h,target,{old,revision:card.config?.revision,elapsed:cached?now()-cached.updated:0});
          text(metric,display.text);metric.title=display.detail;metric.setAttribute('aria-label',`${rowLabel} ${target.protocol.toUpperCase()} ${display.text}；${display.detail}`);metric.disabled=!target.id||(!!notice&&!notice.measured);
          metric.dataset.laneState=notice?notice.key:'measuring';
          if(history?.card===card&&history.target.id===target.id)history.trigger=metric;
          // Freshness is a current-probe property, never a historical palette mask.
          updateStrip(strip,notice&&!notice.measured?[]:C.cardPoints(h,target),target,kind);
        }
        text(view.note,'最近1小时：12×5分钟；TCP历史为Connect P95，右侧为最新完整三Echo丢包。');
      }
      for(const [key,view]of card.rows)if(!live.has(key)){view.node.remove();card.rows.delete(key);}
      let reason=card.error||C.cardNotice(card.config);
      if(card.agent.revoked||card.agent.disabled_at)reason='设备已撤销/暂停，当前数据不可用';
      card.reason.hidden=!reason;if(card.reason.textContent!==reason)card.reason.textContent=reason;
      card.title.querySelector('.nq-subtitle').textContent='TCP 延迟 | ICMP 丢包';
      const settings=card.head.querySelector('.nq-link');settings.title='网络质量设置：目标地址、检测频率、总开关';
      card.title.title=`网络质量 (${card.graph?'TCP/ICMP RTT 历史':'TCP 延迟/ICMP 丢包'})；切换本模块视图${reason?' · '+reason:''}`;
      card.title.setAttribute('aria-label',card.title.title);
      card.title.dataset.hasStatus=String(!!reason);
    }
    function mount(agent) {
      let card=cards.get(agent.agent_id);
      if(!card){
        const node=el('section','network-quality');node.dataset.qualityAgent=agent.agent_id;
        let saved='ipv4';try{if(root.localStorage?.getItem('404-probe:nq-family:'+agent.agent_id)==='ipv6')saved='ipv6';}catch{}
        card={id:agent.agent_id,agent,node,family:saved,config:null,raw:null,previous:[],graph:false,generation:0,configSerial:0,visible:false,
          data:new Map(),loading:new Set(),historyOwner:{},configOwner:{},panelReadOwner:{},writeOwner:{},reading:false,dead:false,error:''};
        cards.set(card.id,card);renderCard(card);observer?.observe(node);
      }
      card.agent=agent;
      if(agent.revoked){queue.release(card.historyOwner);card.generation++;card.loading.clear();}
      // Metadata/resource refresh never starts a quality config read or resets a draft.
      if(history?.card===card){const machine=historyContainer()?.querySelector('.nq-machine');if(machine)machine.textContent=agent.name||agent.state?.hostname||'未命名设备';}
      renderCard(card);
      return card.node;
    }
    function retire(card) {
      card.dead=true;card.generation++;card.configSerial++;observer?.unobserve(card.node);
      queue.release(card.historyOwner);queue.release(card.configOwner);queue.release(card.panelReadOwner);queue.release(card.writeOwner);card.data.clear();
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
      panel={card,trigger,config,draft:config?C.draftOf(config):{interval_ms:30000,slots:C.slots.map(slot=>C.slotDraft({slot,kind:'builtin',region:'',isp:slot,host:'',port:0,slot_enabled:false,ipv4_tcp:false,ipv4_icmp:false,ipv6_tcp:false,ipv6_icmp:false}))},
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
      const old=panel;panel=null;queue.release(old.card.panelReadOwner);old.card.configSerial++;old.card.reading=false;
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
      body.append(master,p.masterNote,freq,p.frequencyNote,el('p','nq-notice','请仅检测你有权检测的目标'));
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
      const card=p.card;card.configSerial++;card.reading=false;queue.release(card.configOwner);queue.release(card.panelReadOwner);
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
        p.errors={};for(const d of error.details||[]){if(d.slot){(p.errors[d.slot]||(p.errors[d.slot]={}))[d.field]=d.code;}}
        p.message=Object.keys(p.errors).length?'请检查标出的输入':`保存失败：${C.errorText[error.details?.[0]?.code]||error.message}`;
      }
      else {p.message=`未确认保存成功：${error.message||'网络错误'}`;loadConfig(p.card,true);}
      renderPanel();
    }
    async function savePanel({thenEnable=false}={}) {
      const p=panel;if(!p||p.saving||p.toggling||p.outdated||!p.base||!csrf())return;
      const found=C.hints(p.draft);if(Object.values(found).some(e=>Object.keys(e).length)){p.errors={};p.message='请检查标出的输入';p.guard=null;renderPanel();return;}
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
        const value=await read(C.historyKey(h.card.id,h.target.id,h.target.family,h.hours),dialogOwner,h.hours===1?`${path(h.card.id)}v3/card-history?target_id=${encodeURIComponent(h.target.id)}`:`${path(h.card.id)}v3/history?target_id=${encodeURIComponent(h.target.id)}&view=1day`);
        if(history!==h||h.generation!==generation||!historyActive(h)||value.target?.id!==h.target.id||value.target?.family!==h.target.family)return;
        const group=value.groups?.find(g=>g.semantics==='staged_probe_v2'&&g.metric===(h.target.protocol==='tcp'?'pure_connect':'echo_rtt'));
        h.data={...value,window:group?.window||{},blocks:(group?.points||[]).map(p=>({...p,gap:p.execution_coverage==='known_gap'}))};h.error='';renderHistory();
      }catch(error){if(history===h&&h.generation===generation&&error.name!=='AbortError'){h.error='读取失败；保留上次结果，不代表当前状态';renderHistory();}}
      finally{if(history===h&&h.generation===generation)h.loading=false;}
    }
    function renderHistory(){
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
        axes:[{stroke:color,grid:{stroke:'#8883'}},{stroke:color,grid:{stroke:'#8883'},values:(_u,ticks)=>ticks.map(v=>h.kind==='ratio'?`${v}%`:`${v} ms`)}],
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
    function visibility(){if(wasHidden===doc.hidden)return;wasHidden=doc.hidden;if(doc.hidden){for(const card of cards.values()){card.generation++;card.loading.clear();queue.release(card.historyOwner);queue.release(card.configOwner);queue.release(card.panelReadOwner);card.configSerial++;card.reading=false;}if(history){history.generation++;history.loading=false;queue.release(dialogOwner);destroyPlot();}if(timer){clear(timer);timer=null;}}
      else{if(!timer)timer=interval(tick,30000);tick();}}
    const cancelDialog=event=>{event.preventDefault();closeHistory();};
    const dialogClosed=()=>closeHistory();const backdrop=event=>{if(event.target===dialog){const rect=dialog.getBoundingClientRect();if(event.clientX<rect.left||event.clientX>rect.right||event.clientY<rect.top||event.clientY>rect.bottom)closeHistory();}};
    doc.addEventListener('keydown',keydown);doc.addEventListener('visibilitychange',visibility);
    if(!observer){root.addEventListener?.('scroll',fallbackVisibility,{passive:true});root.addEventListener?.('resize',fallbackVisibility,{passive:true});}
    dialog.addEventListener('cancel',cancelDialog);dialog.addEventListener('close',dialogClosed);dialog.addEventListener('click',backdrop);
    if(!doc.hidden)timer=interval(tick,30000);
    return {mount,sync,
      setCardView(id,graph){const c=cards.get(id);if(!c)return;c.graph=graph;c.node.dataset.view=graph?'graph':'detail';c.content.hidden=graph;c.content.inert=graph;c.head.querySelector('.nq-tabs').hidden=graph;c.head.querySelector('.nq-tabs').inert=graph;c.generation++;c.loading.clear();queue.release(c.historyOwner);renderCard(c);refreshCard(c);},
      historyTargets(id){const c=cards.get(id);return (c?.config?.targets||[]).filter(t=>t.id).map(t=>({...t,label:`${C.slotLabel(c.config,t.slot)} · ${t.protocol.toUpperCase()} · ${t.family==='ipv4'?'V4':'V6'}${t.status==='active'?'':' · 当前未检测'}`}));},
      ensureHistoryTargets(id){const c=cards.get(id);return c&&!c.config?loadConfig(c,true):Promise.resolve();},
      close(){closed=true;if(timer)clear(timer);observer?.disconnect();closePanel(true);closeHistory();for(const c of [...cards.values()])retire(c);queue.release(dialogOwner);if(!coordinator)queue.close();
      doc.removeEventListener('keydown',keydown);doc.removeEventListener('visibilitychange',visibility);
      if(!observer){root.removeEventListener?.('scroll',fallbackVisibility);root.removeEventListener?.('resize',fallbackVisibility);}
      dialog.removeEventListener('cancel',cancelDialog);dialog.removeEventListener('close',dialogClosed);dialog.removeEventListener('click',backdrop);},
      // Production lifecycle hooks exposed for deterministic synthetic verification.
      snapshot:()=>({requests:queue.snapshot(),cards:cards.size,panel:panel?{dirty:dirty(panel),base:panel.base,draft:structuredClone(panel.draft),guard:panel.guard,message:panel.message,outdated:panel.outdated}:null,history:history?{target:history.target.id,family:history.target.family,hours:history.hours,kind:history.kind}:null}),
      releaseHistory:closeHistory,
      openHistoryFor(id,targetId,family,kind,trigger){const card=cards.get(id),target=card?.config?.targets?.find(t=>t.id===targetId&&t.family===family);if(!target||!['latency','ratio'].includes(kind))return false;openHistory(card,target,kind,trigger);return true;},
      setVisible:(id,value)=>{const card=cards.get(id);if(card)setVisible(card,value);},tick};
  }
  if(typeof module==='object'&&module.exports)module.exports={create};else root.NetworkQuality={create};
})(globalThis);
