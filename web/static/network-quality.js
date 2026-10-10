/* Narrow card/config/history UI; samples remain Server-owned, never persisted by this page. */
(function(root) {
  'use strict';
  const C=root.NetworkQualityCore||(typeof require==='function'?require('./network-quality-core.js'):null);
  function create({document:doc=root.document,fetcher=(...args)=>root.fetch(...args),csrf=()=>'',unauthorized=()=>root.location.assign('/login'),
    state=root.AgentState,management=root.Management,Plot=root.uPlot,Observer=root.IntersectionObserver,Resize=root.ResizeObserver,
    interval=root.setInterval,clear=root.clearInterval,now=()=>Date.now(),coordinator=null,historyHost=null}={}) {
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
    const label=t=>`${t?.source==='manual'?'自定义':catalog?.find(r=>r.id===t?.region)?.label||'未选择'}${C.names[t?.slot]||''}`;
    const geometryVisible=card=>{const r=card.node.getBoundingClientRect();return card.node.isConnected&&r.bottom>0&&r.top<(root.innerHeight||0)&&r.right>0&&r.left<(root.innerWidth||0);};
    function fallbackVisibility(){if(!observer&&!doc.hidden)for(const card of cards.values())setVisible(card,geometryVisible(card));}
    function setVisible(card,value) {
      const changed=card.visible!==value;
      card.visible=value;
      if(!value&&changed){card.generation++;queue.release(card.historyOwner);card.loading.clear();queue.release(card.configOwner);
        if(panel?.card!==card){card.configSerial++;card.reading=false;}}
      else if(value&&changed)refreshCard(card);
    }
    const observer=Observer?new Observer(entries=>{for(const e of entries){const c=cards.get(e.target.dataset.qualityAgent);if(c)setVisible(c,e.isIntersecting);}},{rootMargin:'0px'}):null;
    function effective(card) {return card.visible&&!doc.hidden&&!card.agent.revoked&&!card.agent.disabled_at;}
    function bindConfig(card,config) {
      card.config=config;card.error='';card.generation++;card.data.clear();card.loading.clear();queue.release(card.historyOwner);
      if(history?.card===card)closeHistory();
      renderCard(card);refreshCard(card);
    }
    async function loadConfig(card,force=false) {
      if(closed||card.dead||(!force&&card.config))return;
      const key=JSON.stringify(['config',card.id]),owner=force||panel?.card===card?card.panelReadOwner:card.configOwner;
      if(card.reading){if(force)read(key,owner,path(card.id)+'config').catch(()=>{});return;}
      card.reading=true;const serial=++card.configSerial;
      try {
        const value=await read(key,owner,path(card.id)+'config');
        if(closed||card.dead||serial!==card.configSerial)return;
        const different=JSON.stringify(card.config)!==JSON.stringify(value);
        if(different)bindConfig(card,value);else card.error='';
        if(panel?.card===card){
          if(!panel.dirty){panel.draft=C.draft(value);panel.base=value.revision;}
          else if(panel.base!==value.revision)panel.conflict=true;
          panel.recovered=true;renderPanel();
        }
      }catch(error){if(!card.dead&&serial===card.configSerial&&error.name!=='AbortError'){card.error='配置读取失败，保留上次可信状态';if(panel?.card===card){panel.message=card.error;renderPanel();}renderCard(card);}}
      finally{if(serial===card.configSerial)card.reading=false;}
    }
    function targets(card) {
      return C.slots.map(slot=>{
        const all=card.config?.targets||[],actual=all.find(t=>t.slot===slot&&t.family===card.family);
        if(actual)return actual;
        // Only copy saved choice metadata; never borrow another family's ID/data.
        const fact=all.find(t=>t.slot===slot)||{};
        return {slot,family:card.family,status:'unavailable',source:fact.source,region:fact.region,protocol:fact.protocol};
      });
    }
    async function refreshCard(card) {
      if(!effective(card)||closed||card.dead)return;
      loadCatalog().then(()=>{if(!card.dead){renderCard(card);if(panel?.card===card)renderPanel();}}).catch(()=>{if(!card.dead){card.error='地区目录读取失败';renderCard(card);}});
      if(!card.config){loadConfig(card);return;}
      if(!card.config.supported||!card.config.enabled||(card.family==='ipv6'&&!card.config.ipv6))return;
      const generation=card.generation;
      for(const target of targets(card)) {
        if(!target.id||target.status!=='active'||card.loading.has(target.id))continue;
        const key=C.historyKey(card.id,target.id,card.family,1);card.loading.add(target.id);
        read(key,card.historyOwner,`${path(card.id)}history?target_id=${encodeURIComponent(target.id)}&hours=1`).then(value=>{
          if(card.dead||generation!==card.generation||!effective(card)||value.target?.id!==target.id||value.target?.family!==card.family||value.target?.protocol!==target.protocol||value.target?.slot!==target.slot)return;
          card.data.set(target.id,{value,updated:now(),error:''});renderCard(card);
        }).catch(error=>{
          if(!card.dead&&generation===card.generation&&error.name!=='AbortError'){
            const old=card.data.get(target.id);card.data.set(target.id,{...old,error:'历史读取失败，显示上次结果（非当前）'});renderCard(card);
          }
        }).finally(()=>{if(generation===card.generation)card.loading.delete(target.id);});
      }
    }
    function family(card,value) {
      if(value===card.family)return;
      card.family=value;card.generation++;card.data.clear();card.loading.clear();queue.release(card.historyOwner);
      if(history?.card===card)closeHistory();renderCard(card);refreshCard(card);
    }
    function updateStrip(row,blocks,target,kind,disabled) {
      const incoming=(blocks||[]).slice(-10);
      row.setAttribute('aria-label',`${kind==='latency'?'TCP 延迟':'ICMP 丢包'}，最近${incoming.length}个真实一分钟桶`);
      if(!incoming.length){if(!row.querySelector('.nq-unknown-slot'))row.replaceChildren(el('small','nq-muted nq-unknown-slot','未知'));return;}
      row.querySelector('.nq-muted')?.remove();
      for(let i=0;i<incoming.length;i++) {
        const block=incoming[i],value=kind==='latency'?block.p50_ms:C.ratio(block,target.protocol).value;
        const neutral=disabled||block.gap||!C.finite(value);
        const failed=!disabled&&!block.gap&&target.protocol==='tcp'&&block.attempts>0&&block.failures>0&&!block.successes;
        const tone=failed?'failure':neutral?'neutral':kind==='latency'?(value<100?'low':value<250?'mid':'high'):(value===0?'low':'loss');
        let wrap=row.children[i];
        if(!wrap){
          wrap=el('span','nq-block-wrap');const square=button('',()=>{square.focus();note.textContent=square.title;}),note=el('span','nq-bucket-note');
          square.addEventListener('focus',()=>{note.textContent=square.title;});square.addEventListener('blur',()=>{note.textContent='';});
          square.addEventListener('pointerenter',()=>{note.textContent=square.title;});square.addEventListener('pointerleave',()=>{if(doc.activeElement!==square)note.textContent='';});
          wrap.append(square,note);row.append(wrap);
        }
        const square=wrap.firstElementChild,text=C.blockText(block,target.protocol);square.className=`nq-block nq-${tone}`;square.textContent=failed?'×':neutral?'·':kind==='ratio'&&value>0?'!':'';
        if(square.title!==text){square.title=text;square.setAttribute('aria-label',text);if(doc.activeElement===square)wrap.lastElementChild.textContent=text;}
      }
      while(row.children.length>incoming.length)row.lastElementChild.remove();
    }
    function renderCard(card) {
      if(!card.head) {
        const head=el('div','nq-head'),title=el('div','nq-title');title.append(el('strong','','网络质量'),el('span','nq-subtitle','(TCP 延迟/ICMP 丢包)'));head.append(title);
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
      for(const target of targets(card)) {
        const key=target.slot;live.add(key);let view=card.rows.get(key);
        if(!view){
          view={target,node:el('section','nq-row'),label:el('div','nq-target'),metric:el('div','nq-metrics'),latencyStrip:el('div','nq-blocks'),ratioStrip:el('div','nq-blocks'),note:el('small','nq-muted')};
          view.latency=button('',event=>openHistory(card,view.target,'latency',event.currentTarget),'nq-metric');
          view.ratio=button('',event=>openHistory(card,view.target,'ratio',event.currentTarget),'nq-metric');
          view.latency.dataset.focusKey=`nq-${key}-latency`;view.ratio.dataset.focusKey=`nq-${key}-ratio`;
          view.node.append(view.label,view.latency,view.latencyStrip,view.ratio,view.ratioStrip,view.note);card.rows.set(key,view);
        }
        view.target=target;
        if(view.node!==position)card.content.insertBefore(view.node,position);position=view.node.nextElementSibling;
        const cached=card.data.get(target.id),stored=cached?.value;
        const expired=stored&&C.finite(stored.latest_age_ms)&&stored.latest_age_ms+Math.max(0,now()-(cached.updated||now()))>36000;
        const h=expired?{...stored,stale:true}:stored,shownTarget=disabled&&target.status==='active'?{...target,status:'paused'}:target;
        const old=!!h?.stale||!!cached?.error||card.agent.online===false||!!card.agent.state?.stale;
        const display=C.current(old&&h?{...h,stale:true}:h,shownTarget),r=C.ratio(h?.recent_5min,target.protocol);
        const text=(node,value)=>{if(node.textContent!==value)node.textContent=value;};
        const tcp=target.protocol==='tcp',icmp=target.protocol==='icmp';
        text(view.label,C.names[target.slot]);view.label.title=label(target);
        text(view.latency,tcp?display.text:'未知');view.latency.title=tcp?display.detail:'没有对应 TCP 目标/数据，不借用 ICMP 延迟';
        text(view.ratio,icmp?`${disabled||old||!C.finite(r.value)?'未知':C.percent(r.value)}${old?' 旧':''}`:'未知');
        view.ratio.title=icmp?`${r.label} ${r.fraction}；${old?'旧数据，非当前结果；':''}${h?.recent_5min?.not_executed||0} 未执行。小样本不构成可靠性承诺。`:'没有对应 ICMP 目标/真实丢包数据，不采用 TCP 连接失败率';
        view.latency.disabled=!target.id||!tcp;view.ratio.disabled=!target.id||!icmp;
        if(history?.card===card&&history.target.id===target.id)history.trigger=history.kind==='latency'?view.latency:view.ratio;
        updateStrip(view.latencyStrip,tcp?h?.recent_blocks:[],target,'latency',disabled||old);updateStrip(view.ratioStrip,icmp?h?.recent_blocks:[],target,'ratio',disabled||old);
        text(view.note,cached?.error||`${display.neutral?display.detail+' · ':''}${cached?.updated?'更新 '+C.time(cached.updated):'等待质量数据'}`);
      }
      for(const [key,view]of card.rows)if(!live.has(key)){view.node.remove();card.rows.delete(key);}
      let reason=card.error||(!card.config?'配置尚未读取':!card.config.supported?'Agent 尚未支持网络质量':!card.config.enabled?'检测已暂停':card.family==='ipv6'&&!card.config.ipv6?'V6 检测未启用':'');
      if(card.agent.revoked||card.agent.disabled_at)reason='设备已撤销/暂停，当前数据不可用';
      card.reason.hidden=!reason;if(card.reason.textContent!==reason)card.reason.textContent=reason;
      card.title.querySelector('.nq-subtitle').textContent=reason||'(TCP 延迟/ICMP 丢包)';
      card.title.title=`网络质量 (TCP 延迟/ICMP 丢包)${reason?' · '+reason:''}`;
      card.title.setAttribute('aria-label',card.title.title);
      card.title.dataset.hasStatus=String(!!reason);
    }
    function mount(agent) {
      let card=cards.get(agent.agent_id);
      if(!card){
        const node=el('section','network-quality');node.dataset.qualityAgent=agent.agent_id;
        card={id:agent.agent_id,agent,node,family:'ipv4',config:null,generation:0,configSerial:0,visible:false,
          data:new Map(),loading:new Set(),historyOwner:{},configOwner:{},panelReadOwner:{},writeOwner:{},reading:false,dead:false,error:''};
        cards.set(card.id,card);renderCard(card);observer?.observe(node);
      }
      card.agent=agent;
      if(agent.revoked||agent.disabled_at){queue.release(card.historyOwner);card.generation++;card.loading.clear();}
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
      panel={card,trigger,draft:C.draft(card.config||{}),base:card.config?.revision,dirty:false,mode:'all',search:'',
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
      if(choice.source==='manual')return '手填地址仅保存输入，不推断地址族或 ICMP 可达性';
      const region=catalog?.find(r=>r.id===choice.region);
      return !choice.region?'未选择地区':!region?'目录待加载/地区未知':
        `${region.available?.[choice.slot]?.ipv4?'V4 有目录端点':'V4 暂无节点'}${panel.draft.ipv6?' · '+(region.available?.[choice.slot]?.ipv6?'V6 有目录端点':'V6 暂无节点'):''}（目录不是连通性证明）`;
    }
    function regionSelect(value,slot) {
      const filtered=(catalog||[]).filter(r=>!panel.search||`${r.label} ${r.province}`.includes(panel.search));
      const options=[['','未选择地区'],...filtered.map(r=>[r.id,r.label])];
      if(value&&!filtered.some(r=>r.id===value)){const current=catalog?.find(r=>r.id===value);options.push([value,current?.label||'已保存地区（目录未知）']);}
      const input=select(options,value,next=>{panel.draft=C.chooseRegion(panel.draft,slot,next);changedPanel();renderPanel();});input.dataset.nqField=`${slot}-region`;return input;
    }
    function renderPanel() {
      if(!panel)return;const p=panel,n=p.node;
      // Input drafts live in p, independent of card remounts and background reads.
      const focus=doc.activeElement?.dataset?.nqField,selection=doc.activeElement?.selectionStart,scrollTop=n.scrollTop;
      // Keep dismissal controls connected across asynchronous config reads. A response
      // between pointerdown/up must not remove the user's click target.
      if(!p.header){
        p.header=el('div','nq-head');p.header.append(el('strong','','地区选择'),button('关闭',()=>closePanel(),'nq-link'));
        p.guardNode=el('div','nq-status');p.guardNode.append(el('p','','更改尚未保存，要放弃吗？'),button('继续编辑',()=>{p.guard=false;renderPanel();}),button('放弃更改',()=>closePanel(true)));
        n.append(p.header,p.guardNode);
      }
      p.guardNode.hidden=!p.guard;
      const form=el('form','nq-config');form.addEventListener('submit',savePanel);
      const controls=el('div','nq-form-row');
      for(const [key,text]of [['enabled','启用检测'],['ipv6','V6 检测']]){const input=el('input');input.type='checkbox';input.checked=p.draft[key];input.addEventListener('change',()=>{p.draft[key]=input.checked;changedPanel();renderPanel();});controls.append(field(text,input));}
      form.append(controls,el('small','nq-muted','固定 30 秒 ±20% · 超时 5 秒；首次打开不自动启用、不自动保存。'));
      const mode=select([['all','统一地区快捷选择'],['separate','分别选择三网']],p.mode,value=>{p.mode=value;renderPanel();});form.append(field('选择方式',mode));
      mode.dataset.nqField='mode';
      const search=el('input');search.type='search';search.value=p.search;search.placeholder='搜索全部省/市地区';search.dataset.nqField='search';
      search.addEventListener('input',()=>{p.search=search.value;renderPanel();});form.append(field('本地地区搜索',search));
      if(p.mode==='all') {
        const same=p.draft.choices.every(c=>c.source==='catalog'&&c.region===p.draft.choices[0].region);
        form.append(field('选择地区（明确选择才改变三网）',regionSelect(same?p.draft.choices[0].region:'','all')));
        if(!same)form.append(el('small','nq-muted','当前为独立选择；切换此模式未改变任何草稿。'));
      }
      for(const choice of p.draft.choices) {
        const group=el('fieldset','nq-choice');group.append(el('legend','',C.names[choice.slot]));
        const source=select([['catalog','地区目录'],['manual','手填 Host/Port']],choice.source,value=>{
          choice.source=value;choice.region='';choice.host='';choice.port=value==='manual'&&choice.protocol==='tcp'?443:0;changedPanel();renderPanel();
        });source.dataset.nqField=`${choice.slot}-source`;group.append(field('目标来源',source));
        if(choice.source==='catalog'){
          if(p.mode==='separate')group.append(field('地区',regionSelect(choice.region,choice.slot)));
          else group.append(el('small','nq-muted',catalog?.find(r=>r.id===choice.region)?.label||'未选择地区'));
        }else{
          const host=el('input');host.value=choice.host;host.placeholder='主机名或 IP（不含端口）';host.dataset.nqField=choice.slot+'-host';
          host.addEventListener('input',()=>{choice.host=host.value;changedPanel();});group.append(field('Host',host));
          if(choice.protocol==='tcp'){const port=el('input');port.type='number';port.min='1';port.max='65535';port.value=String(choice.port);port.dataset.nqField=choice.slot+'-port';
            port.addEventListener('input',()=>{choice.port=Number(port.value);changedPanel();});group.append(field('TCP Port',port));}
        }
        const protocol=select([['tcp','TCP 连接'],['icmp','ICMP 单次回显']],choice.protocol,value=>{
          choice.protocol=value;if(value==='icmp')choice.port=0;else if(choice.source==='manual'&&!choice.port)choice.port=443;changedPanel();renderPanel();
        });protocol.dataset.nqField=`${choice.slot}-protocol`;group.append(field('协议',protocol),el('small','nq-muted',availability(choice)));form.append(group);
      }
      if(p.conflict||p.uncertain){
        form.append(el('p','nq-status',p.conflict?'配置版本已变化；草稿保留，未自动覆盖或重试保存。':'保存结果不确定；草稿和最后可信配置保留，请先重新读取。'));
        form.append(button('重新读取可信配置',()=>{p.recovered=false;loadConfig(p.card,true);}));
        if(p.recovered&&p.card.config)form.append(button('采用最新版本，保留草稿',()=>{p.base=p.card.config.revision;p.conflict=false;p.uncertain=false;p.message='已采用最新版本，请核对后明确保存';renderPanel();}));
      }
      const save=el('button','nq-save',p.saving?'保存中…':'保存');save.type='submit';save.disabled=p.saving||p.conflict||p.uncertain||!p.base||!p.card.config?.supported||!csrf();
      form.append(el('p','nq-status',p.message||(!p.card.config?.supported?'Agent 尚未支持；不自动启用':'只检测所选三个槽位，不创建 Probe Jobs。')),save);
      p.form?.remove();n.append(form);p.form=form;n.scrollTop=scrollTop;
      const rect=p.trigger.getBoundingClientRect(),width=Math.min(420,(root.innerWidth||1024)-24);
      n.style.width=`${width}px`;n.style.left=`${Math.max(12,Math.min(rect.left,(root.innerWidth||1024)-width-12))}px`;
      const top=Math.max(12,Math.min(rect.bottom+8,(root.innerHeight||768)-Math.min(560,(root.innerHeight||768)-24)-12));
      n.style.top=`${top}px`;n.style.maxHeight=`${Math.max(120,(root.innerHeight||768)-top-12)}px`;
      if(focus){const input=n.querySelector(`[data-nq-field="${focus}"]`);input?.focus({preventScroll:true});if(typeof selection==='number'&&input?.type!=='number')input?.setSelectionRange?.(selection,selection);}
    }
    async function savePanel(event) {
      event.preventDefault();const p=panel;if(!p||p.saving||p.conflict||p.uncertain||!p.base||!p.card.config?.supported||!csrf())return;
      let data;try {data=C.payload(p.draft,p.base);if(new TextEncoder().encode(JSON.stringify(data)).length>4096)throw Error('配置超过4096字节');}
      catch(error){p.message=error.message;renderPanel();return;}
      p.saving=true;p.message='';const card=p.card;card.configSerial++;card.reading=false;queue.release(card.configOwner);queue.release(card.panelReadOwner);renderPanel();
      try {
        const config=await queue.request(JSON.stringify(['write',card.id]),card.writeOwner,signal=>C.transportLease(tracked=>management.write(path(card.id)+'config',{method:'PUT',payload:data,csrf:csrf(),signal,fetcher:tracked,unauthorized}),fetcher));
        if(card.dead||panel!==p)return;
        p.saving=false;p.dirty=false;bindConfig(card,config);closePanel(true);
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
        const value=await read(C.historyKey(h.card.id,h.target.id,h.target.family,h.hours),dialogOwner,`${path(h.card.id)}history?target_id=${encodeURIComponent(h.target.id)}&hours=${h.hours}`);
        if(history!==h||h.generation!==generation||!historyActive(h)||value.target?.id!==h.target.id||value.target?.family!==h.target.family)return;
        h.data=value;h.error='';renderHistory();
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
      const summary=el('p','nq-window',`窗口统计：P50 ${C.ms(stats.p50_ms)} · P95 ${C.ms(stats.p95_ms)} · 延迟波动 ${C.ms(stats.delay_variation_ms)} · ${r.label} ${C.percent(r.value)} (${r.fraction}) · 样本 ${stats.count||0} · 未执行 ${stats.not_executed||0}`);
      const status=el('p','nq-status',h.error||(!h.data?'正在读取指定目标…':`${h.data.partial?'部分保留 · ':''}${h.data.stale?'旧数据 · ':''}${h.data.gap_reason||'无已知gap'} · 完整性 ${h.data.completeness}（不承诺完整采集） · Server ${C.time(h.data.now)}`));
      const chart=el('div','nq-plot'),tooltip=el('output','nq-chart-detail');tooltip.setAttribute('aria-live','polite');
      dialog.replaceChildren(header,controls,summary,status,el('small','nq-muted','每点为 Server 真实时间桶聚合；1h 为一分钟桶，24h 为五分钟桶。缺测断线，不是原始每30秒点。'),chart,tooltip);
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
      const nodes=[...panel.node.querySelectorAll('button,input,select')].filter(n=>!n.disabled),first=nodes[0],last=nodes.at(-1);
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
    return {mount,sync,close(){closed=true;if(timer)clear(timer);observer?.disconnect();closePanel(true);closeHistory();for(const c of [...cards.values()])retire(c);queue.release(catalogOwner);queue.release(dialogOwner);if(!coordinator)queue.close();
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
