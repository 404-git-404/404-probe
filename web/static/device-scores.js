(function(root){
  'use strict';
  const gradeNames = new Set(['SSS','SS','S','A','B','C','D']);
  const partNames = {
    network:'网络', uptime:'在线率', resources:'资源余量',
    availability:'可用性', latency:'延迟', stability:'稳定性', evening:'晚高峰', alerts:'告警',
  };
  const reasons = {
    paused:'监控已暂停', no_history:'暂无足够历史数据',
    no_card_lines:'没有启用并在卡片显示的线路', network_coverage:'近 7 天没有达到覆盖要求的线路',
    line_coverage:'有数据的时长不到 1 小时', no_probe_results:'没有可计分的探测结果',
    no_presence:'暂无在线或离线记录', no_resources:'暂无资源使用记录',
    once:'一次性付费', fewer_than_two:'可比较的计费设备少于 2 台', missing_rate:'没有设置该币种的汇率',
    missing_price:'未设置价格', invalid_price:'价格无效', missing_currency:'未设置币种', invalid_currency:'币种无效',
    missing_period:'未设置付费周期', invalid_period:'付费周期无效',
  };
  const paused = {overall:{reason:'paused'},network:{reason:'paused'}};
  const refreshInterval = 10 * 60 * 1000;
  let items = {}, computedAt = 0, nextRead = 0, request;
  const attached = new WeakMap();
  let active, popup;
  let scrollIdle;
  const overflowObserver = root.IntersectionObserver && new root.IntersectionObserver(entries=>{
    for(const entry of entries){
      if(entry.target.dataset.scoreLarge==='true') entry.target.dataset.scoreOverflow=entry.isIntersecting;
    }
  },{rootMargin:'140px 40px'});

  const shown = score => typeof score?.score === 'number' && gradeNames.has(score.grade);
  const isHighValue = model => model?.high_value === true && shown(model?.overall);
  function valueRibbonTip(model){
    if(model?.value_rank > 0 && model?.value_count > 0){
      return '高性价比 · 第 ' + model.value_rank + ' / ' + model.value_count + ' 名';
    }
    return '高性价比';
  }
  const format = value => new Intl.NumberFormat('zh-CN',{maximumFractionDigits:1}).format(Math.floor(value*10+1e-8)/10);
  const reason = code => reasons[code] || '数据不足';
  const element = (doc, tag, className, text) => {
    const node = doc.createElement(tag);
    if(className) node.className = className;
    if(text != null) node.textContent = text;
    return node;
  };

  function art(doc, grade){
    const node = element(doc,'span','score-art');
    node.dataset.tier = grade || 'none';
    node.setAttribute('aria-hidden','true');
    if(!grade){
      node.textContent = '—';
      return node;
    }
    const ns = 'http://www.w3.org/2000/svg';
    const svg = doc.createElementNS(ns,'svg');
    svg.setAttribute('viewBox','0 0 32 32');
    const use = doc.createElementNS(ns,'use');
    use.setAttribute('href','/score-stamps.svg#grade-'+grade);
    svg.append(use);
    node.append(svg);
    if(grade === 'SSS'){
      for(let i=0;i<6;i++){
        const star = element(doc,'i','score-star');
        star.style.setProperty('--star',i);
        node.append(star);
      }
    }
    return node;
  }

  function breakdown(doc, model, type, layout){
    const network = type === 'network';
    const score = model?.[type];
    const box = element(doc,layout==='detail'?'section':'div','score-breakdown');
    box.dataset.layout = layout;
    const figure = art(doc, shown(score) ? score.grade : '');
    const content = element(doc,'div','score-breakdown-body');
    const header = element(doc,layout==='detail'?'h3':'div','score-breakdown-heading');
    const heading = (network?'网络评分':'综合评分')+(shown(score)?' '+score.grade:'');
    header.append(element(doc,'strong','',heading),
      element(doc,'span','score-total',shown(score)?format(score.score)+' / 100':'数据不足'));
    content.append(header);
    if(!shown(score)){
      content.append(element(doc,'div','score-note',reason(score?.reason)));
    }
    for(const part of score?.parts || []){
      if(!shown(score) && part.reason===score.reason) continue;
      const row = element(doc,'div','score-part');
      row.append(element(doc,'span','score-part-label',partNames[part.key]));
      if(typeof part.score === 'number'){
        const meter = element(doc,'span','score-meter');
        meter.setAttribute('aria-hidden','true');
        const bar = element(doc,'i');
        bar.style.width = Math.max(0, Math.min(100,part.score))+'%';
        meter.append(bar);
        row.append(meter, element(doc,'span','score-part-value',format(part.score)));
      }else{
        row.append(element(doc,'span','score-part-skip','未计入 · '+reason(part.reason)));
      }
      content.append(row);
      if(part.key==='network' && shown(model?.network)){
        const sub = element(doc,'div','score-subparts');
        for(const item of model.network.parts || []){
          sub.append(element(doc,'span','',partNames[item.key]+' '+format(item.score)));
        }
        content.append(sub);
      }
      if(part.key==='uptime' && model.online_percent!=null){
        content.append(element(doc,'div','score-note','近 7 天在线率 '+format(model.online_percent)+'%'));
      }
    }
    if(!network && score?.parts?.some(part => part.score==null) && shown(score)){
      content.append(element(doc,'div','score-note','已按原比例放大计入项的权重。'));
    }
    if(!network){
      const text = model?.value_rank>0
        ? '性价比 第 '+model.value_rank+' / '+model.value_count+' 名'
        : '性价比 不参与排名（'+reason(model?.value_reason || score?.reason)+'）';
      content.append(element(doc,'div','score-note',text));
    }
    if(layout==='tip' && !network){
      content.append(element(doc,'div','score-tip-action','点击查看详情'));
    }
    box.append(figure,content);
    return box;
  }

  function hide(){
    active?.removeAttribute('aria-describedby');
    active = null;
    if(popup) popup.hidden = true;
  }

  function show(node){
    if(!node.isConnected) return;
    hide();
    const doc = node.ownerDocument;
    const current = attached.get(node);
    if(!current) return;
    if(!popup || popup.ownerDocument!==doc){
      popup = element(doc,'div','score-popup');
      popup.id = 'device-score-tooltip';
      popup.setAttribute('role','tooltip');
      doc.body.append(popup);
    }
    popup.replaceChildren(breakdown(doc,current.model,current.type,'tip'));
    popup.hidden = false;
    const anchor = node.getBoundingClientRect();
    const {width,height} = popup.getBoundingClientRect();
    const left = Math.max(8,Math.min(anchor.right-width,root.innerWidth-width-8));
    const top = anchor.bottom+height+8<=root.innerHeight ? anchor.bottom+6 : Math.max(8,anchor.top-height-6);
    popup.style.left = left+'px';
    popup.style.top = top+'px';
    active = node;
    node.setAttribute('aria-describedby',popup.id);
  }

  function attachTip(node){
    node.addEventListener('pointerenter',event=>{
      if(event.pointerType!=='touch' && root.matchMedia('(hover:hover)').matches) show(node);
    });
    node.addEventListener('pointerleave',()=>{if(active===node && !node.matches(':focus-visible')) hide();});
    node.addEventListener('focus',()=>{if(node.matches(':focus-visible')) show(node);});
    node.addEventListener('blur',()=>{if(active===node) hide();});
    node.addEventListener('keydown',event=>{
      if(event.key==='Escape' && active===node){
        hide();
        event.stopPropagation();
      }
    });
    node.addEventListener('click',hide);
  }

  function stamp(doc, size, open){
    const slot = element(doc,'span','score-slot score-slot-'+size);
    const button = element(doc,'button','score-stamp score-stamp-'+size);
    button.type = 'button';
    button.setAttribute('aria-haspopup','dialog');
    attachTip(button);
    button.addEventListener('click',()=>open(button));
    slot.append(button);
    return {slot,button,update(model){
      if(attached.get(button)?.model===model && button.firstChild) return;
      attached.set(button,{model,type:'overall'});
      const score = model?.overall;
      const tier = shown(score)?score.grade:'';
      if(button.dataset.tier !== (tier||'none')){
        button.dataset.tier = tier || 'none';
        button.replaceChildren(art(doc,tier));
      }
      button.setAttribute('aria-label','综合评分 '+(tier?tier+' '+format(score.score)+' 分':'数据不足')+'，点击查看详情');
      if(active===button) show(button);
    }};
  }

  function networkBadge(node, model){
    if(attached.get(node)?.model===model && node.firstChild) return;
    if(!attached.has(node)) attachTip(node);
    attached.set(node,{model,type:'network'});
    const score = model?.network;
    node.textContent = shown(score)?score.grade+' · '+format(score.score)+' 分':'数据不足';
    node.setAttribute('aria-label','网络评分 '+node.textContent);
    if(active===node) show(node);
  }

  function forAgent(agent){
    if(agent?.disabled_at || agent?.status==='paused') return paused;
    return items[agent?.agent_id];
  }

  function valueRibbon(doc, size){
    const ns = 'http://www.w3.org/2000/svg';
    const node = element(doc, 'span', 'value-ribbon value-ribbon-' + size);
    node.hidden = true;
    node.setAttribute('role', 'img');
    const svg = doc.createElementNS(ns, 'svg');
    svg.setAttribute('aria-hidden', 'true');
    const use = doc.createElementNS(ns, 'use');
    if(size === 'card'){
      svg.setAttribute('class', 'value-ribbon-bg');
      svg.setAttribute('viewBox', '0 0 68 18');
      use.setAttribute('href', '/score-stamps.svg#value-ribbon-card');
      svg.append(use);
      node.append(svg, element(doc, 'span', 'value-ribbon-text', '高性价比'));
    }else{
      svg.setAttribute('viewBox', '0 0 16 16');
      use.setAttribute('href', '/score-stamps.svg#value-ribbon-icon');
      svg.append(use);
      node.append(svg);
    }
    return {node, update(model){
      const show = isHighValue(model);
      node.hidden = !show;
      if(show){
        const tip = valueRibbonTip(model);
        node.title = tip;
        node.setAttribute('aria-label', tip);
      }else{
        node.removeAttribute('title');
        node.removeAttribute('aria-label');
      }
    }};
  }

  function setOverflow(node, model){
    const large = ['S','SS','SSS'].includes(model?.overall?.grade) || isHighValue(model);
    if(node.dataset.scoreLarge===String(large)) return;
    node.dataset.scoreLarge = large;
    if(large && overflowObserver){
      overflowObserver.observe(node);
    }else{
      overflowObserver?.unobserve(node);
      node.dataset.scoreOverflow = large;
    }
  }

  async function refresh(read, changed){
    if(request || performance.now()<nextRead) return;
    request = new AbortController();
    nextRead = performance.now()+30000;
    try{
      const snapshot = await read('/api/v1/web/scores',request.signal);
      if(!snapshot.computed_at) return;
      const untilRefresh = snapshot.computed_at+refreshInterval-(snapshot.server_now||Date.now());
      nextRead = performance.now()+Math.max(30000,Math.min(refreshInterval,untilRefresh+1000));
      if(snapshot.computed_at!==computedAt){
        items = snapshot.items || {};
        computedAt = snapshot.computed_at;
        changed();
      }
    }catch{
      // Retry after the same delay if the panel is still open.
    }finally{
      request = null;
    }
  }

  root.addEventListener?.('scroll',()=>{
    hide();
    const page=root.document.documentElement;
    if(!page.classList.contains('score-scrolling')) page.classList.add('score-scrolling');
    clearTimeout(scrollIdle);
    scrollIdle=setTimeout(()=>page.classList.remove('score-scrolling'),120);
  },{capture:true,passive:true});
  root.addEventListener?.('resize',hide);
  root.addEventListener?.('pagehide',()=>{request?.abort();hide();});
  root.DeviceScores = {stamp,valueRibbon,isHighValue,valueRibbonTip,networkBadge,forAgent,refresh,setOverflow,unobserve:node=>overflowObserver?.unobserve(node),version:()=>computedAt,
    details:(doc,model)=>breakdown(doc,model,'overall','detail')};
})(globalThis);
