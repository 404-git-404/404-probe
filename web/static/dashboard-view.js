(function(root){
  'use strict';
  const M = root.DashboardModel;
  const settings = ['update', 'password', 'regions', 'about'];

  function create({document:doc=root.document,onchange=()=>{},onManage=()=>{},read=url=>root.fetch(url).then(r=>r.json())}={}){
    const preferences = M.preferences(), p = preferences.value, get = id => doc.getElementById(id);
    const search = get('dashboard-search'), sort = get('dashboard-sort'), direction = get('dashboard-direction');
    const count = get('dashboard-count'), allDevices = get('dashboard-all'), allGroups = get('dashboard-all-groups');
    const tagAll = get('dashboard-tags-clear'), tagMore = get('dashboard-tags-more');
    const tagMenu = tagMore.querySelector('.dashboard-tag-overflow');
    const panel = get('dashboard-panel'), app = doc.querySelector('.dashboard-app'), mobile = get('panel-mobile-dialog');
    const mobileOpen = get('panel-mobile-open'), collapseButton = get('panel-collapse');
    const phone = root.matchMedia('(max-width:44rem)'), narrow = root.matchMedia('(max-width:80rem)');
    const order = M.createOrder();
    const text = (node,value) => { if(node.textContent!==String(value)) node.textContent = value; };
    const pressed = (node,value) => node.setAttribute('aria-pressed',String(value));
    const element = (tag,className) => {
      const node = doc.createElement(tag);
      if(className) node.className = className;
      return node;
    };
    const drawer = key => get('dashboard-'+key);
    const options = Object.fromEntries(['groups','countries','statuses','tags'].map(key=>[
      key,{list:drawer(key).querySelector('.dashboard-options'),nodes:new Map()}
    ]));
    let page = 'devices', controlsKey = '', tagLayoutKey = '', tagFrame = 0, lastSummary;
    let touchStart = null, manualExpanded = false;
    const mobileShell = root.DialogShell.attach(mobile,{decorate:false,guard:false,onclose:()=>app.prepend(panel)});

    function paintPage(){
      const devices = page==='devices';
      get('dashboard-toolbar').hidden = !devices;
      get('network-placeholder').hidden = page!=='network';
      get('dashboard-settings-content').hidden = !settings.includes(page);
      pressed(get('dashboard-network'),page==='network');
      for(const item of doc.querySelectorAll('[data-settings]')){
        const active = item.dataset.settings===page;
        get(item.dataset.settings).hidden = !active;
        if(active) item.setAttribute('aria-current','page');
        else item.removeAttribute('aria-current');
      }
      if(settings.includes(page)){
        drawer('settings').open = true;
        doc.dispatchEvent(new CustomEvent('dashboard:settings',{detail:page}));
      }
      if(devices) scheduleTags();
    }

    function navigate(next,{history=true,closeMobile=false}={}){
      if(closeMobile) mobileShell.close();
      if(next===page) return;
      page = next;
      if(history) root.history.pushState(null,'',next==='devices'?root.location.pathname+root.location.search:'#'+next);
      paintPage();
      root.scrollTo(0,0);
    }

    function fromHash(){
      const hash = root.location.hash.slice(1);
      navigate(settings.includes(hash)||hash==='network'?hash:'devices',{history:false});
      onchange();
    }
    root.addEventListener('popstate',fromHash);
    root.addEventListener('hashchange',fromHash);

    function change(){ preferences.save(); onchange(); }
    function showDirection(){
      const descending = p.sortDir==='desc';
      text(direction,descending?'↓':'↑');
      direction.setAttribute('aria-label',descending?'降序，点击切换升序':'升序，点击切换降序');
    }
    for(const [key,label] of Object.entries(M.SORTS)){
      if(key==='name') continue;
      const option = element('option');
      option.value = key;
      option.textContent = '排序：'+label;
      sort.append(option);
    }
    direction.addEventListener('click',()=>{p.sortDir=p.sortDir==='desc'?'asc':'desc';showDirection();change();});
    showDirection();
    search.value = p.search;
    sort.value = p.sort;
    function sortBy(key){
      p.sortDir = p.sort===key&&p.sortDir!=='desc'?'desc':'asc';
      p.sort = sort.value = key;
      showDirection();
      change();
    }
    sort.addEventListener('change',()=>{p.sort=sort.value;change();});
    search.addEventListener('input',()=>{p.search=search.value;navigate('devices');change();});
    const views = [...doc.querySelectorAll('[data-view]')];
    function paintView(){for(const button of views) pressed(button,button.dataset.view===p.viewMode);}
    for(const button of views) button.addEventListener('click',()=>{
      if(p.viewMode!==button.dataset.view&&button.dataset.view==='list') order.reset();
      p.viewMode=button.dataset.view;
      paintView();
      change();
    });
    paintView();

    function clear(){
      for(const key of ['groups','countries','statuses','youtubeRegions','quick']) p[key]=[];
      p.sort = sort.value = 'name';
      p.sortDir = 'asc';
      order.reset();
      showDirection();
      navigate('devices',{closeMobile:true});
      change();
    }
    allDevices.addEventListener('click',clear);
    function selectGroup(id){
      p.groups=p.groups.includes(id)?[]:[id];
      navigate('devices');
      change();
    }
    allGroups.addEventListener('click',()=>selectGroup(M.ALL_GROUPS));
    function toggle(key,id,checked){
      p[key]=p[key].filter(item=>item!==id);
      if(checked) p[key].push(id);
      navigate('devices');
      change();
    }
    tagAll.addEventListener('click',()=>{p.tags=[];change();});
    const youtube = get('dashboard-youtube-free');
    youtube.addEventListener('change',()=>toggle('youtubeRegions',M.AD_FREE,youtube.checked));
    function apply(key){
      if(!lastSummary?.[key]) return;
      p.quick=p.quick.includes(key)?[]:[key];
      navigate('devices');
      change();
    }

    for(const key of ['devices','groups','countries','statuses','settings']){
      const host = drawer(key);
      host.open = !p.panelSectionsClosed.includes(key);
      if(key==='devices'||key==='settings'){
        host.querySelector('summary').addEventListener('click',event=>{
          if(key==='devices') { navigate('devices'); onchange(); }
          if(!app.classList.contains('panel-collapsed')) return;
          event.preventDefault();
          setCollapsed(false);
          host.open=true;
        });
      }
      host.addEventListener('toggle',()=>{
        p.panelSectionsClosed = p.panelSectionsClosed.filter(item=>item!==key);
        if(!host.open) p.panelSectionsClosed.push(key);
        preferences.save();
      });
    }

    get('dashboard-network').addEventListener('click',()=>{navigate('network',{closeMobile:true});onchange();});
    for(const button of doc.querySelectorAll('[data-settings]')) button.addEventListener('click',()=>{
      navigate(button.dataset.settings,{closeMobile:true});onchange();
    });
    const themeButtons = [...doc.querySelectorAll('[data-panel-theme]')];
    function paintTheme(){for(const button of themeButtons) pressed(button,button.dataset.panelTheme===root.ProbeTheme.get());}
    for(const button of themeButtons) button.addEventListener('click',()=>root.ProbeTheme.set(button.dataset.panelTheme));
    root.addEventListener('404-probe:themechange',paintTheme);
    paintTheme();

    function layout(){
      if(!phone.matches&&mobile.open) mobileShell.close();
      const collapsed = !phone.matches&&(narrow.matches?!manualExpanded:p.panelCollapsed);
      app.classList.toggle('panel-collapsed',collapsed);
      text(collapseButton,collapsed?'»':'«');
      collapseButton.title=collapsed?'展开面板':'收起面板';
      collapseButton.setAttribute('aria-label',collapseButton.title);
    }
    function setCollapsed(value){
      if(narrow.matches) manualExpanded=!value;
      else {p.panelCollapsed=value;preferences.save();}
      layout();
    }
    collapseButton.addEventListener('click',()=>setCollapsed(!app.classList.contains('panel-collapsed')));
    function breakpoint(){manualExpanded=false;layout();onchange();}
    phone.addEventListener('change',breakpoint);
    narrow.addEventListener('change',breakpoint);
    function openMobile(focusSearch=false){
      if(!mobile.open){get('panel-mobile-body').append(panel);mobileShell.open(mobileOpen);}
      if(focusSearch) search.focus();
    }
    mobileOpen.addEventListener('click',()=>openMobile());
    function focusSearch(){
      if(phone.matches) openMobile(true);
      else {if(app.classList.contains('panel-collapsed')) setCollapsed(false);search.focus();}
    }
    get('panel-search-open').addEventListener('click',focusSearch);
    doc.addEventListener('keydown',event=>{
      if(event.ctrlKey&&event.key.toLowerCase()==='k'){event.preventDefault();focusSearch();}
    });
    mobile.addEventListener('touchstart',event=>{
      touchStart=event.touches.length===1?[event.touches[0].clientX,event.touches[0].clientY]:null;
    },{passive:true});
    mobile.addEventListener('touchcancel',()=>{touchStart=null;},{passive:true});
    mobile.addEventListener('touchend',event=>{
      const touch=event.changedTouches[0];
      if(touchStart&&touchStart[0]-touch.clientX>55&&Math.abs(touch.clientY-touchStart[1])<45) mobileShell.close();
      touchStart=null;
    },{passive:true});
    get('manage-groups').addEventListener('click',event=>{
      const trigger=mobile.open?mobileOpen:event.currentTarget;
      mobileShell.close();
      onManage('groups',trigger);
    });
    get('manage-tags').addEventListener('click',event=>onManage('tags',event.currentTarget));
    get('overview-expense').addEventListener('click',()=>mobileShell.close(),{capture:true});
    layout();

    function fitTags(){
      tagFrame = 0;
      const {list,nodes}=options.tags;
      if(!list.getClientRects().length) return;
      const buttons=[...nodes.values()].map(entry=>entry.node);
      for(const button of buttons) if(button.parentNode!==list) list.append(button);
      tagMore.hidden=true;
      const gap=parseFloat(root.getComputedStyle(list).gap)||0;
      const widths=buttons.map(button=>button.getBoundingClientRect().width+gap);
      if(widths.reduce((sum,width)=>sum+width,0)-gap<=list.clientWidth){tagMore.open=false;return;}
      tagMore.hidden=false;
      const available=list.clientWidth;
      let used=0, extra=0;
      for(let i=0;i<buttons.length;i++){
        used+=widths[i];
        if(used-gap>available){tagMenu.append(buttons[i]);extra++;}
      }
      const summary=tagMore.querySelector('summary');
      text(summary,'+'+extra);
      summary.setAttribute('aria-label','更多标签，'+extra+' 个');
    }
    function scheduleTags(){if(!tagFrame) tagFrame=root.requestAnimationFrame(fitTags);}
    // Observe the row rather than the tag list, whose width changes while fitting.
    const tagResize = new root.ResizeObserver(scheduleTags);
    tagResize.observe(get('dashboard-toolbar'));
    tagResize.observe(count);
    const initialPage=root.location.hash.slice(1);
    if(settings.includes(initialPage)||initialPage==='network') page=initialPage;
    paintPage();

    const countryNames = typeof Intl.DisplayNames==='function'?new Intl.DisplayNames(['zh-CN'],{type:'region'}):null;
    function country(code){
      if(code==='unknown') return '未知';
      try{return countryNames?.of(code)||code;}catch{return code;}
    }
    function optionRows(key,rows){
      const {list,nodes}=options[key], live=new Set(), checkbox=key==='countries'||key==='statuses';
      let next=list.firstElementChild;
      for(const [id,label,total] of rows){
        live.add(id);
        let entry=nodes.get(id);
        if(!entry){
          const node=element(checkbox?'label':'button','dashboard-option'+(key==='tags'?'':' panel-item'));
          const input=checkbox?element('input'):null, name=element('span'), number=element('small');
          if(input){
            input.type='checkbox';
            input.addEventListener('change',()=>toggle(key,id,input.checked));
            node.append(input);
          }else{
            node.type='button';
            node.addEventListener('click',()=>key==='groups'?selectGroup(id):toggle(key,id,!p[key].includes(id)));
          }
          node.append(name,number);
          entry={node,input,name,number};
          nodes.set(id,entry);
        }
        if(entry.input) entry.input.checked=p[key].includes(id);
        else pressed(entry.node,p[key].includes(id));
        text(entry.name,label);
        text(entry.number,total);
        if(key!=='tags'||!entry.node.parentNode){
          if(entry.node!==next) list.insertBefore(entry.node,next);
          next=entry.node.nextElementSibling;
        }
      }
      for(const [id,entry] of nodes) if(!live.has(id)){entry.node.remove();nodes.delete(id);}
    }

    function update(agents,catalog,server,ready){
      if(ready) preferences.validate(catalog);
      const model=M.select(agents,catalog,p,server);
      lastSummary=model.summary;
      order.apply(model,catalog,p);
      if(ready&&p.quick.length&&!lastSummary[p.quick[0]]){
        p.quick=[];
        preferences.save();
        return update(agents,catalog,server,ready);
      }
      const nextControls=JSON.stringify([ready,p,catalog.revision,model.list.length,model.youtubeFreeCount,
        [...agents.values()].map(a=>[a.agent_id,a.revoked,a.group_id,a.tag_ids,a.country_code,M.lifecycle(a)])]);
      if(nextControls===controlsKey) return model;
      controlsKey=nextControls;
      const totals={groups:new Map(),tags:new Map(),countries:new Map(),statuses:new Map()};
      const groupNames=new Map(catalog.groups.map(group=>[group.id,group.name]));
      groupNames.set(M.UNGROUPED,'未分组');
      const bump=(key,id)=>totals[key].set(id,(totals[key].get(id)||0)+1);
      for(const a of agents.values()){
        if(a.revoked) continue;
        bump('groups',groupNames.has(a.group_id)?a.group_id:M.UNGROUPED);
        for(const tag of a.tag_ids||[]) bump('tags',tag);
        bump('countries',a.country_code||'unknown');
        bump('statuses',M.lifecycle(a));
      }
      const groupRows=[...groupNames].map(([id,name])=>[id,name,totals.groups.get(id)||0]);
      const tagRows=catalog.tags.map(tag=>[tag.id,tag.name,totals.tags.get(tag.id)||0]);
      optionRows('groups',groupRows);
      optionRows('tags',tagRows);
      optionRows('countries',[...totals.countries].sort(([a],[b])=>a.localeCompare(b)).map(([id,n])=>[id,country(id),n]));
      optionRows('statuses',Object.entries(M.STATUS).map(([id,label])=>[id,label,totals.statuses.get(id)||0]));
      const nextTags=JSON.stringify(tagRows);
      if(nextTags!==tagLayoutKey){tagLayoutKey=nextTags;scheduleTags();}
      pressed(tagAll,!p.tags.length);
      const total=lastSummary.registered;
      text(count,model.list.length+' / '+total+' 台');
      text(allDevices.querySelector('small'),total);
      text(allGroups.querySelector('small'),total);
      pressed(allDevices,!['groups','countries','statuses','youtubeRegions','quick'].some(key=>p[key].length));
      pressed(allGroups,p.groups.includes(M.ALL_GROUPS));
      youtube.checked=p.youtubeRegions.includes(M.AD_FREE);
      text(get('dashboard-youtube-count'),model.youtubeFreeCount);
      const summaries={
        groups:p.groups.map(id=>id===M.ALL_GROUPS?'全部分组':groupNames.get(id)).filter(Boolean),
        countries:[...p.countries.map(country),...(youtube.checked?['YouTube 无广告地区']:[])],
        statuses:p.statuses.map(id=>M.STATUS[id]).filter(Boolean)
      };
      for(const [key,labels] of Object.entries(summaries)){
        const summary=drawer(key).querySelector('.panel-summary');
        text(summary,labels.join('、'));
        summary.title=summary.textContent;
      }
      return model;
    }

    let versionReadAt=0,versionBusy=false,closed=false;
    function updateVersion(view){
      const versionData=view?.update||{}, latest=versionData.latest;
      const valid=typeof latest==='string'&&root.AgentState.compareStableVersions(latest,latest)===0;
      const link=get('panel-update');
      link.hidden=!(versionData.newer===true&&valid);
      if(link.hidden) link.removeAttribute('href');
      else {
        link.href='https://github.com/404-git-404/404-probe/releases/tag/'+latest;
        text(link.querySelector('span'),'有新版本 '+latest+' · 说明');
      }
    }
    async function refreshVersion(force=false){
      if(versionBusy||closed||(!force&&Date.now()-versionReadAt<30*60*1000)) return;
      versionBusy=true;
      versionReadAt=Date.now();
      try{const view=await read('/api/v1/web/version');if(!closed) updateVersion(view);}catch{}finally{versionBusy=false;}
    }
    return {
      preferences, page:()=>page, effectiveView:()=>p.viewMode==='list'&&phone.matches?'cards':p.viewMode,
      update,apply,clear,sortBy,updateVersion,refreshVersion,
      setSlot(slot){p.latencySlot=slot;change();},
      collapse(id){p.collapsed=p.collapsed.includes(id)?p.collapsed.filter(value=>value!==id):[...p.collapsed,id];change();},
      close(){
        closed=true;
        tagResize.disconnect();
        if(tagFrame) root.cancelAnimationFrame(tagFrame);
        phone.removeEventListener('change',breakpoint);
        narrow.removeEventListener('change',breakpoint);
        mobileShell.close();
      },
      snapshot:()=>({state:{...p}})
    };
  }
  root.DashboardView={create};
})(globalThis);
