/* Narrow card/config/history UI; samples remain Server-owned, never persisted by this page. */
(function(root) {
  'use strict';
  const C=root.NetworkQualityCore||(typeof require==='function'?require('./network-quality-core.js'):null);
  function create({document:doc=root.document,fetcher=(...args)=>root.fetch(...args),csrf=()=>'',unauthorized=()=>root.location.assign('/login'),
    state=root.AgentState,management=root.Management,Plot=root.uPlot,Observer=root.IntersectionObserver,Resize=root.ResizeObserver,
    interval=root.setInterval,clear=root.clearInterval,now=()=>Date.now(),coordinator=null,historyHost=null,onConfig=null}={}) {
    const cards=new Map(),queue=coordinator||C.coordinator({now}),catalogOwner={},dialogOwner={};
    let catalog=null,catalogPending=null,closed=false,panel=null,history=null,plot=null,resize=null,timer=null,wasHidden=doc.hidden;
    const dialog=doc.querySelector('#network-quality-history');
    const historyContainer=()=>historyHost?historyHost.container():dialog;
    const historyActive=h=>!doc.hidden&&(!historyHost||historyHost.active(h.card.id));
    const el=(tag,className,text)=>{const n=doc.createElement(tag);if(className)n.className=className;if(text!=null)n.textContent=text;return n;};
    const button=(text,action,className='')=>{const b=el('button',className,text);b.type='button';b.addEventListener('click',action);return b;};
    const field=(label,input)=>{const wrap=el('label','nq-field');input.setAttribute('aria-label',label);wrap.append(el('span','',label),input);return wrap;};
    const select=(values,value,change)=>{const s=el('select');for(const [v,t]of values){const o=el('option','',t);o.value=v;s.append(o);}s.value=value;s.addEventListener('change',()=>change(s.value));return s;};
    const read=(key,owner,path)=>queue.request(key,owner,signal=>C.transportLease(tracked=>state.fetchJSON(path,{signal,fetcher:tracked,unauthorized,timeoutMs:10000}),fetcher));
    const path=id=>`/api/v1/web/agents/${encodeURIComponent(id)}/network-quality/`;
    function loadCatalog() {
      if(catalog)return Promise.resolve(catalog);
      if(catalogPending)return catalogPending;
      catalogPending=read('catalog',catalogOwner,'/api/v1/web/network-quality/catalog').then(value=>{catalog=value.regions||[];return catalog;}).finally(()=>{catalogPending=null;});
      return catalogPending;
    }
    const label=t=>`${catalog?.find(r=>r.id===t?.region)?.label||'未选择'}${C.names[t?.slot]||''}`;
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
    function bindConfig(card,config) {
      const keep=new Set((config.targets||[]).filter(t=>card.config?.targets?.some(p=>p.id===t.id&&p.family===t.family&&p.protocol===t.protocol&&p.slot===t.slot&&p.slot_revision===t.slot_revision)).map(t=>t.id));
      card.data=new Map([...card.data].filter(([id])=>keep.has(id)));
      card.config=config;card.error='';card.generation++;card.loading.clear();queue.release(card.historyOwner);
      if(history?.card===card)closeHistory();
      renderCard(card);refreshCard(card);
      onConfig?.(card.id,config);
    }
    async function loadConfig(card,force=false) {
      if(closed||card.dead||(!force&&card.config))return;
      const key=JSON.stringify(['config',card.id]),owner=force||panel?.card===card?card.panelReadOwner:card.configOwner;
      if(card.reading){if(force)read(key,owner,path(card.id)+'v3/card-config').catch(()=>{});return;}
      card.reading=true;const serial=++card.configSerial;
      try {
        const value=await read(key,owner,path(card.id)+'v3/card-config');
        if(closed||card.dead||serial!==card.configSerial)return;
        const different=JSON.stringify(card.config)!==JSON.stringify(value);
        if(different)bindConfig(card,value);else card.error='';
        if(panel?.card===card){
          if(!panel.dirty){panel.draft=C.laneDraft(value);panel.base=value.revision;}
          else if(panel.base!==value.revision)panel.conflict=true;
          panel.recovered=true;renderPanel();
        }
      }catch(error){if(!card.dead&&serial===card.configSerial&&error.name!=='AbortError'){card.error='配置读取失败，保留上次可信状态';if(panel?.card===card){panel.message=card.error;renderPanel();}renderCard(card);}}
      finally{if(serial===card.configSerial)card.reading=false;}
    }
    function targets(card) {
      return C.slots.flatMap(slot=>['tcp','icmp'].map(protocol=>{
        const all=card.config?.targets||[],actual=all.find(t=>t.slot===slot&&t.family===card.family&&t.protocol===protocol&&t.region===card.config.region_id);
        if(actual)return actual;
        return {slot,family:card.family,protocol,status:'unavailable',region:card.config?.region_id};
      }));
    }
    async function refreshCard(card) {
      if(!effective(card)||closed||card.dead)return;
      loadCatalog().then(()=>{if(!card.dead){renderCard(card);if(panel?.card===card)renderPanel();}}).catch(()=>{if(!card.dead){card.error='地区目录读取失败';renderCard(card);}});
      if(!card.config){loadConfig(card);return;}
      if(card.graph)return; // Shared configuration remains live while bucket reads sleep.
      const generation=card.generation;
      for(const target of targets(card)) {
        if(!target.id||card.loading.has(target.id))continue;
        const cached=card.data.get(target.id);if(cached&&now()-cached.updated<30000)continue;
        const key=JSON.stringify(['card-history',card.id,target.id,card.family]);card.loading.add(target.id);
        read(key,card.historyOwner,`${path(card.id)}v3/card-history?target_id=${encodeURIComponent(target.id)}`).then(value=>{
          if(card.dead||generation!==card.generation||!effective(card)||value.target?.id!==target.id||value.target?.family!==card.family||value.target?.protocol!==target.protocol||value.target?.slot!==target.slot)return;
          card.data.set(target.id,{value,updated:now(),error:''});renderCard(card);
        }).catch(error=>{
          if(!card.dead&&generation===card.generation&&error.name!=='AbortError'){
            const old=card.data.get(target.id);card.data.set(target.id,{...old,updated:now(),error:'历史读取失败，显示上次结果（非当前）'});renderCard(card);
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
        const region=button('地区选择',event=>openPanel(card,event.currentTarget),'nq-link');region.dataset.focusKey='nq-region';head.append(region);
        const tabs=el('div','nq-tabs');for(const [v,text]of [['ipv4','IPv4'],['ipv6','IPv6']]){const b=button(text,()=>family(card,v));b.dataset.focusKey=`nq-${v}`;tabs.append(b);}head.append(tabs);
        card.head=head;card.reason=el('p','nq-status');card.reason.id='nq-status-'+card.id;card.title=title;
        title.tabIndex=0;title.dataset.focusKey='nq-status';title.setAttribute('aria-describedby',card.reason.id);
        card.content=el('div','nq-rows');card.rows=new Map();
        card.node.append(head,card.reason,card.content);
      }
      for(const b of card.head.querySelectorAll('.nq-tabs button'))b.setAttribute('aria-pressed',String(b.dataset.focusKey===`nq-${card.family}`));
      const disabled=card.agent.revoked||card.agent.disabled_at||!card.config?.supported||!card.config?.enabled||(card.family==='ipv6'&&!card.config?.ipv6);
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
        text(view.label,C.names[key]);view.label.title=`${label(tcp)} · TCP/ICMP 各自 target`;
        for(const [target,metric,strip,kind] of [[tcp,view.latency,view.latencyStrip,'latency'],[icmp,view.ratio,view.ratioStrip,'ratio']]) {
          const cached=card.data.get(target.id),h=cached?.value,old=disabled||!!cached?.error||card.agent.online===false||!!card.agent.state?.stale;
          const notice=C.configNotice(card.config),display=notice?.key==='region'?notice:C.cardCurrent(h,target,{old,revision:card.config?.revision,elapsed:cached?now()-cached.updated:0});
          text(metric,display.text);metric.title=display.detail;metric.setAttribute('aria-label',`${C.names[key]} ${target.protocol.toUpperCase()} ${display.text}；${display.detail}`);metric.disabled=!target.id;
          if(history?.card===card&&history.target.id===target.id)history.trigger=metric;
          // Freshness is a current-probe property, never a historical palette mask.
          updateStrip(strip,C.cardPoints(h,target),target,kind);
        }
        text(view.note,'最近1小时：12×5分钟；TCP历史为Connect P95，右侧为最新完整三Echo丢包。');
      }
      for(const [key,view]of card.rows)if(!live.has(key)){view.node.remove();card.rows.delete(key);}
      let reason=card.error||(!card.config?'配置尚未读取':C.configNotice(card.config)?.key==='region'?'未选择地区':!card.config.supported?'Agent 尚未支持网络质量':!card.config.enabled?'检测已暂停':card.family==='ipv6'&&!card.config.ipv6?'V6 检测未启用':'');
      if(card.agent.revoked||card.agent.disabled_at)reason='设备已撤销/暂停，当前数据不可用';
      card.reason.hidden=!reason;if(card.reason.textContent!==reason)card.reason.textContent=reason;
      card.title.querySelector('.nq-subtitle').textContent='TCP 延迟 | ICMP 丢包';
      const region=card.head.querySelector('.nq-link');region.textContent=catalog?.find(r=>r.id===card.config?.region_id)?.label||'选择地区';region.title=`三网共用地区；${card.config?.region_id||'未选择'} · 最近1小时12×5分钟`;
      card.title.title=`网络质量 (${card.graph?'TCP/ICMP RTT 历史':'TCP 延迟/ICMP 丢包'})；切换本模块视图${reason?' · '+reason:''}`;
      card.title.setAttribute('aria-label',card.title.title);
      card.title.dataset.hasStatus=String(!!reason);
    }
    function mount(agent) {
      let card=cards.get(agent.agent_id);
      if(!card){
        const node=el('section','network-quality');node.dataset.qualityAgent=agent.agent_id;
        let saved='ipv4';try{if(root.localStorage?.getItem('404-probe:nq-family:'+agent.agent_id)==='ipv6')saved='ipv6';}catch{}
        card={id:agent.agent_id,agent,node,family:saved,config:null,graph:false,generation:0,configSerial:0,visible:false,
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

    function openPanel(card,trigger) {
      if(panel&&!closePanel(false))return;
      panel={card,trigger,draft:C.laneDraft(card.config||{}),base:card.config?.revision,dirty:false,mode:'all',search:'',
        saving:false,conflict:false,uncertain:false,recovered:false,message:'',node:el('section','nq-popover')};
      panel.node.setAttribute('role','dialog');panel.node.setAttribute('aria-label','网络质量地区选择');
      doc.body.append(panel.node);renderPanel();panel.node.querySelector('button')?.focus({preventScroll:true});
      loadCatalog().then(()=>{if(panel?.card===card)renderPanel();}).catch(()=>{if(panel?.card===card){panel.message='地区目录读取失败，可重试；草稿保留';renderPanel();}});
      loadConfig(card,true);
    }
    function closePanel(force=false) {
      if(!panel)return true;
      if(panel.saving&&!force){panel.message='正在保存，请等待结果';renderPanel();return false;}
      if(panel.dirty&&!force){panel.guard=true;renderPanel();return false;}
      const old=panel;panel=null;queue.release(old.card.panelReadOwner);old.card.configSerial++;old.card.reading=false;
      old.node.remove();if(old.trigger?.isConnected)old.trigger.focus({preventScroll:true});return true;
    }
    function changedPanel(){if(panel){panel.dirty=true;panel.guard=false;panel.message='有未保存更改';}}
    function availability(choice) {
      const region=catalog?.find(r=>r.id===choice.region);
      return !choice.region?'未选择地区':!region?'目录待加载/地区未知':
        `${region.available?.[choice.slot]?.ipv4?'V4 有目录端点':'V4 暂无节点'}${panel.draft.ipv6?' · '+(region.available?.[choice.slot]?.ipv6?'V6 有目录端点':'V6 暂无节点'):''}（目录不是连通性证明）`;
    }
    function syncRegion(p) {
      const next=p.region.value;
      if(next===p.draft.region)return;
      p.draft.region=next;for(const c of p.draft.choices)c.region=next;changedPanel();
    }
    function renderPanel() {
      if(!panel)return;const p=panel,n=p.node;
      // Keep the form, native selects and submit button connected while reads settle.
      // Callbacks resolve the current draft: a clean config read may replace it.
      if(!p.form){
        p.header=el('div','nq-head');p.header.append(el('strong','','地区选择'),button('关闭',()=>closePanel(),'nq-link'));
        p.guardNode=el('div','nq-status');p.guardNode.append(el('p','','更改尚未保存，要放弃吗？'),button('继续编辑',()=>{p.guard=false;renderPanel();}),button('放弃更改',()=>closePanel(true)));
        p.form=el('form','nq-config');p.form.addEventListener('submit',savePanel);
        const form=p.form,controls=el('div','nq-form-row');p.flags=new Map();
        for(const [key,text]of [['enabled','启用检测'],['ipv6','V6 检测']]){
          const input=el('input');input.type='checkbox';input.dataset.nqField=key;
          input.addEventListener('change',()=>{p.draft[key]=input.checked;changedPanel();renderPanel();});p.flags.set(key,input);controls.append(field(text,input));
        }
        form.append(controls,el('small','nq-muted','固定 30 秒 ±20% · 超时 5 秒；首次打开不自动启用、不自动保存。'));
        p.region=el('select');p.region.dataset.nqField='all-region';
        for(const event of ['input','change'])p.region.addEventListener(event,()=>{syncRegion(p);renderPanel();});
        p.region.addEventListener('blur',()=>renderPanel());
        form.append(field('三网共用地区',p.region));
        p.searchInput=el('input');p.searchInput.type='search';p.searchInput.placeholder='搜索省/市';p.searchInput.dataset.nqField='search';
        p.searchInput.addEventListener('input',()=>{p.search=p.searchInput.value;renderPanel();});form.append(field('本地地区搜索',p.searchInput));
        form.append(el('small','nq-muted','每条地址族/协议按已保存开关独立启用。'));
        p.lanes=new Map();
        for(const lane of p.draft.choices){
          const key=`${lane.slot}-${lane.family}-${lane.protocol}`,choice=()=>p.draft.choices.find(c=>`${c.slot}-${c.family}-${c.protocol}`===key);
          const group=el('fieldset','nq-choice');group.append(el('legend','',`${C.names[lane.slot]} ${lane.family==='ipv4'?'IPv4':'IPv6'} ${lane.protocol.toUpperCase()}`));
          const enabled=el('input');enabled.type='checkbox';enabled.dataset.nqField=key+'-enabled';
          enabled.addEventListener('change',()=>{choice().enabled=enabled.checked;changedPanel();});group.append(field('启用此通道',enabled));
          const note=el('small','nq-muted');group.append(note);form.append(group);
          p.lanes.set(key,{enabled,note});
        }
        p.recovery=el('div');p.recoveryStatus=el('p','nq-status');p.reload=button('重新读取可信配置',()=>{p.recovered=false;loadConfig(p.card,true);});
        p.rebase=button('采用最新版本，保留草稿',()=>{p.base=p.card.config.revision;p.conflict=false;p.uncertain=false;p.message='已采用最新版本，请核对后明确保存';renderPanel();});
        p.recovery.append(p.recoveryStatus,p.reload,p.rebase);form.append(p.recovery);
        p.messageNode=el('p','nq-status');p.save=el('button','nq-save');p.save.type='submit';form.append(p.messageNode,p.save);
        n.append(p.header,p.guardNode,form);
      }
      const value=(input,next)=>{if(doc.activeElement!==input&&input.value!==next)input.value=next;};
      p.guardNode.hidden=!p.guard;
      for(const [key,input]of p.flags)input.checked=p.draft[key];
      const filtered=(catalog||[]).filter(r=>!p.search||`${r.label} ${r.province}`.includes(p.search));
      const options=[['','未选择地区'],...filtered.map(r=>[r.id,r.label])];
      if(p.draft.region&&!filtered.some(r=>r.id===p.draft.region)){const current=catalog?.find(r=>r.id===p.draft.region);options.push([p.draft.region,current?.label||'已保存地区（目录未知）']);}
      const optionsKey=JSON.stringify(options);
      // Do not alter a native select's option list while its user interaction is open.
      if(p.optionsKey!==optionsKey&&doc.activeElement!==p.region){
        p.region.replaceChildren(...options.map(([v,text])=>{const o=el('option','',text);o.value=v;return o;}));p.optionsKey=optionsKey;
      }
      value(p.region,p.draft.region);value(p.searchInput,p.search);
      for(const c of p.draft.choices){
        const v=p.lanes.get(`${c.slot}-${c.family}-${c.protocol}`);v.enabled.checked=c.enabled;
        v.note.textContent=availability({...c,region:p.draft.region});
      }
      p.recovery.hidden=!(p.conflict||p.uncertain);p.rebase.hidden=!(p.recovered&&p.card.config);
      p.recoveryStatus.textContent=p.conflict?'配置版本已变化；草稿保留，未自动覆盖或重试保存。':'保存结果不确定；草稿和最后可信配置保留，请先重新读取。';
      p.messageNode.textContent=p.message||(!p.card.config?.supported?'Agent 尚未支持；不自动启用':'只检测所选三个槽位，不创建 Probe Jobs。');
      p.save.textContent=p.saving?'保存中…':'保存';p.save.disabled=p.saving||p.conflict||p.uncertain||!p.base||!p.card.config?.supported||!csrf();
      const rect=p.trigger.getBoundingClientRect(),width=Math.min(420,(root.innerWidth||1024)-24);
      n.style.width=`${width}px`;n.style.left=`${Math.max(12,Math.min(rect.left,(root.innerWidth||1024)-width-12))}px`;
      const top=Math.max(12,Math.min(rect.bottom+8,(root.innerHeight||768)-Math.min(560,(root.innerHeight||768)-24)-12));
      n.style.top=`${top}px`;n.style.maxHeight=`${Math.max(120,(root.innerHeight||768)-top-12)}px`;
    }
    async function savePanel(event) {
      event.preventDefault();const p=panel;if(!p||p.saving||p.conflict||p.uncertain||!p.base||!p.card.config?.supported||!csrf())return;
      let data;try {syncRegion(p);data=C.lanePayload(p.draft,p.base);if(new TextEncoder().encode(JSON.stringify(data)).length>8192)throw Error('配置超过8192字节');}
      catch(error){p.message=error.message;renderPanel();return;}
      p.saving=true;p.message='';const card=p.card;card.configSerial++;card.reading=false;queue.release(card.configOwner);queue.release(card.panelReadOwner);renderPanel();
      try {
        const config=await queue.request(JSON.stringify(['write',card.id]),card.writeOwner,signal=>C.transportLease(tracked=>management.write(path(card.id)+'v2/config',{method:'PUT',payload:data,csrf:csrf(),signal,fetcher:tracked,unauthorized}),fetcher));
        if(card.dead||panel!==p)return;
        p.saving=false;p.dirty=false;bindConfig(card,{...config,card_lanes:data.lanes});closePanel(true);
      }catch(error){
        if(card.dead||panel!==p)return;
        p.saving=false;p.message=error.status===409?'保存冲突：草稿未丢失':`未确认保存成功：${error.message||'网络错误'}`;
        if(error.status===409){p.conflict=true;p.recovered=false;loadConfig(card,true);}
        else{p.uncertain=true;p.recovered=false;}
        renderPanel();
      }finally{queue.release(card.writeOwner);}
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
      title.append(heading,el('p','nq-machine',h.card.agent.name||h.card.agent.state?.hostname||'未命名设备'),el('strong','',`网络质量 · ${label(h.target)} · ${h.target.protocol.toUpperCase()} · ${h.target.family==='ipv4'?'V4':'V6'}`));
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
    function outside(event){if(panel&&!panel.node.contains(event.target)&&event.target!==panel.trigger)closePanel();}
    function keydown(event){if(event.key==='Escape'&&panel){event.preventDefault();closePanel();}else if(event.key==='Tab'&&panel){
      const nodes=[...panel.node.querySelectorAll('button,input,select')].filter(n=>!n.disabled&&!n.hidden&&!n.parentNode?.hidden),first=nodes[0],last=nodes.at(-1);
      if(event.shiftKey&&doc.activeElement===first){event.preventDefault();last?.focus();}else if(!event.shiftKey&&doc.activeElement===last){event.preventDefault();first?.focus();}
    }}
    function tick(){if(doc.hidden||closed)return;fallbackVisibility();queue.pump();for(const card of cards.values())refreshCard(card);requestHistory();}
    function visibility(){if(wasHidden===doc.hidden)return;wasHidden=doc.hidden;if(doc.hidden){for(const card of cards.values()){card.generation++;card.loading.clear();queue.release(card.historyOwner);queue.release(card.configOwner);queue.release(card.panelReadOwner);card.configSerial++;card.reading=false;}if(history){history.generation++;history.loading=false;queue.release(dialogOwner);destroyPlot();}if(timer){clear(timer);timer=null;}}
      else{if(!timer)timer=interval(tick,30000);tick();}}
    const cancelDialog=event=>{event.preventDefault();closeHistory();};
    const dialogClosed=()=>closeHistory();const backdrop=event=>{if(event.target===dialog){const rect=dialog.getBoundingClientRect();if(event.clientX<rect.left||event.clientX>rect.right||event.clientY<rect.top||event.clientY>rect.bottom)closeHistory();}};
    doc.addEventListener('pointerdown',outside);doc.addEventListener('keydown',keydown);doc.addEventListener('visibilitychange',visibility);
    if(!observer){root.addEventListener?.('scroll',fallbackVisibility,{passive:true});root.addEventListener?.('resize',fallbackVisibility,{passive:true});}
    dialog.addEventListener('cancel',cancelDialog);dialog.addEventListener('close',dialogClosed);dialog.addEventListener('click',backdrop);
    if(!doc.hidden)timer=interval(tick,30000);
    return {mount,sync,
      setCardView(id,graph){const c=cards.get(id);if(!c)return;c.graph=graph;c.node.dataset.view=graph?'graph':'detail';c.content.hidden=graph;c.content.inert=graph;c.head.querySelector('.nq-tabs').hidden=graph;c.head.querySelector('.nq-tabs').inert=graph;c.generation++;c.loading.clear();queue.release(c.historyOwner);renderCard(c);refreshCard(c);},
      historyTargets(id){return (cards.get(id)?.config?.targets||[]).filter(t=>t.id&&['active','paused','not_scheduled_legacy','unsupported'].includes(t.status)).map(t=>({...t,label:`${C.names[t.slot]} · ${t.protocol.toUpperCase()} · ${t.family==='ipv4'?'V4':'V6'} · ${label(t)}${t.status==='active'?'':t.status==='paused'?' · 暂停':' · 当前不支持探测'}`}));},
      ensureHistoryTargets(id){const c=cards.get(id);return c&&!c.config?loadConfig(c,true):Promise.resolve();},
      close(){closed=true;if(timer)clear(timer);observer?.disconnect();closePanel(true);closeHistory();for(const c of [...cards.values()])retire(c);queue.release(catalogOwner);queue.release(dialogOwner);if(!coordinator)queue.close();
      doc.removeEventListener('pointerdown',outside);doc.removeEventListener('keydown',keydown);doc.removeEventListener('visibilitychange',visibility);
      if(!observer){root.removeEventListener?.('scroll',fallbackVisibility);root.removeEventListener?.('resize',fallbackVisibility);}
      dialog.removeEventListener('cancel',cancelDialog);dialog.removeEventListener('close',dialogClosed);dialog.removeEventListener('click',backdrop);},
      // Production lifecycle hooks exposed for deterministic synthetic verification.
      snapshot:()=>({requests:queue.snapshot(),cards:cards.size,panel:panel?{dirty:panel.dirty,base:panel.base,draft:structuredClone(panel.draft),conflict:panel.conflict,uncertain:panel.uncertain}:null,history:history?{target:history.target.id,family:history.target.family,hours:history.hours,kind:history.kind}:null}),
      releaseHistory:closeHistory,
      openHistoryFor(id,targetId,family,kind,trigger){const card=cards.get(id),target=card?.config?.targets?.find(t=>t.id===targetId&&t.family===family);if(!target||!['latency','ratio'].includes(kind))return false;openHistory(card,target,kind,trigger);return true;},
      setVisible:(id,value)=>{const card=cards.get(id);if(card)setVisible(card,value);},tick};
  }
  if(typeof module==='object'&&module.exports)module.exports={create};else root.NetworkQuality={create};
})(globalThis);
