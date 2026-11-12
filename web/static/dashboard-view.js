(function(root){
 'use strict';
 const M=root.DashboardModel;
 function create({document:doc=root.document,onchange=()=>{},onManage=()=>{},csrf=()=>'',read=url=>root.fetch(url).then(r=>r.json())}={}){
  const preferences=M.preferences(),p=preferences.value,get=id=>doc.getElementById(id);
  const search=get('dashboard-search');
  const sort=get('dashboard-sort'),chips=get('dashboard-chips'),counts=get('dashboard-count'),quick=get('dashboard-quick'),options={};
  const order=M.createOrder();
  const text=(node,value)=>{value=String(value);if(node.textContent!==value)node.textContent=value;};
  sort.replaceChildren(...Object.entries(M.SORTS).map(([key,label])=>{const o=doc.createElement('option');o.value=key;o.textContent='排序：'+label;return o;}));
  const direction=doc.createElement('button');direction.type='button';direction.id='dashboard-direction';sort.after(direction);
  const showDirection=()=>{direction.textContent=p.sortDir==='desc'?'↓':'↑';direction.setAttribute('aria-label',p.sortDir==='desc'?'降序，点击切换升序':'升序，点击切换降序');};
  direction.addEventListener('click',()=>{p.sortDir=p.sortDir==='desc'?'asc':'desc';showDirection();change();});showDirection();
  let catalog={groups:[],tags:[]},model=null,signature='',controlsKey='';
  const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
  const button=(text,action,cls='')=>{const b=el('button',cls,text);b.type='button';b.addEventListener('click',action);return b;};
  function change(){preferences.save();onchange();}
  function sortBy(key){p.sortDir=p.sort===key&&p.sortDir!=='desc'?'desc':'asc';p.sort=key;sort.value=key;showDirection();change();}
  function view(){for(const b of doc.querySelectorAll('[data-view]'))b.setAttribute('aria-pressed',String(b.dataset.view===p.viewMode));}
  for(const b of doc.querySelectorAll('[data-view]'))b.addEventListener('click',()=>{if(p.viewMode!==b.dataset.view&&b.dataset.view==='list')order.reset();p.viewMode=b.dataset.view;view();change();});view();
  search.value=p.search;sort.value=p.sort;
  search.addEventListener('input',()=>{p.search=search.value;change();});
  sort.addEventListener('change',()=>{p.sort=sort.value;change();});
  const clear=()=>{for(const k of ['groups','tags','countries','statuses','quick'])p[k]=[];p.search='';search.value='';change();};
  get('dashboard-clear').addEventListener('click',clear);
  get('dashboard-all').addEventListener('click',()=>{p.groups=[];change();});
  function toggle(key,id,checked){p[key]=p[key].filter(x=>x!==id);if(checked)p[key].push(id);change();}
  for(const key of ['groups','tags','countries','statuses']){
   const host=get('dashboard-'+key),list=host.querySelector('.dashboard-options');options[key]={host,list,nodes:new Map()};
   if(key==='groups'||key==='tags'){const manage=button(key==='groups'?'管理分组…':'管理标签…',e=>onManage(key==='groups'?'groups':'tags',e.currentTarget),'dashboard-manage');if(key==='tags')host.append(manage);else list.after(manage);}
  }
  const tagMore=get('dashboard-tags-more'),tagMenu=tagMore.querySelector('.dashboard-tag-overflow');
  let tagFrame=0;
  function fitTags(){
   tagFrame=0;const list=options.tags.list,nodes=[...options.tags.nodes.values()].map(v=>v.n);if(!nodes.length){tagMore.hidden=true;return;}
   tagMore.hidden=false;for(const n of nodes)if(n.parentNode!==list)list.append(n);
   const gap=parseFloat(root.getComputedStyle(list).gap)||0,widths=nodes.map(n=>n.getBoundingClientRect().width+gap),available=list.clientWidth;
   const all=widths.reduce((a,b)=>a+b,0)-gap;
   if(all<=available+tagMore.getBoundingClientRect().width){tagMore.hidden=true;tagMore.open=false;return;}
   let used=0,extra=0;for(let i=0;i<nodes.length;i++){used+=widths[i];if(used-gap>available){tagMenu.append(nodes[i]);extra++;}}
   tagMore.querySelector('summary').textContent='+'+extra;tagMore.querySelector('summary').setAttribute('aria-label','更多标签，'+extra+' 个');
  }
  function scheduleTags(){if(!tagFrame)tagFrame=root.requestAnimationFrame(fitTags);}
  const tagResize=new root.ResizeObserver(scheduleTags);tagResize.observe(options.tags.list);
  get('dashboard-tags-clear').addEventListener('click',()=>{p.tags=[];change();});
  const quickButtons={},quickCounts={};for(const key of ['offline','disk','network','resources','traffic','renewal']){
   const b=button('',()=>toggle('quick',key,!p.quick.includes(key)),'dashboard-quick-item');b.dataset.quick=key;const label=el('span','',M.QUICK[key]),count=el('small');b.append(label,count);quickButtons[key]=b;quickCounts[key]=count;quick.append(b);
  }
  function apply(key){if(!p.quick.includes(key))p.quick.push(key);change();}
  const panel=get('dashboard-panel'),app=doc.querySelector('.dashboard-app'),mobile=get('panel-mobile-dialog');
  const mobileShell=root.DialogShell.attach(mobile,{decorate:false});
  const narrow=root.matchMedia('(max-width:80rem)'),phone=root.matchMedia('(max-width:44rem)');
  let manualExpanded=false;
  function layout(){
   if(!phone.matches&&mobile.open)mobileShell.close();
   const collapsed=!phone.matches&&(narrow.matches?!manualExpanded:p.panelCollapsed);
   app.classList.toggle('panel-collapsed',collapsed);
   get('panel-collapse').textContent=collapsed?'»':'«';
   get('panel-collapse').setAttribute('aria-label',collapsed?'展开面板':'收起面板');get('panel-collapse').title=collapsed?'展开面板':'收起面板';
  }
  get('panel-collapse').addEventListener('click',()=>{
   if(narrow.matches)manualExpanded=app.classList.contains('panel-collapsed');else{p.panelCollapsed=!app.classList.contains('panel-collapsed');preferences.save();}layout();
  });
  const breakpoint=()=>{manualExpanded=false;layout();onchange();};
  narrow.addEventListener('change',breakpoint);phone.addEventListener('change',breakpoint);
  get('panel-mobile-open').addEventListener('click',e=>{get('panel-mobile-body').append(panel);mobileShell.open(e.currentTarget);get('panel-mobile-close').focus();});
  get('panel-mobile-close').addEventListener('click',mobileShell.close);
  mobile.addEventListener('close',()=>app.prepend(panel));
  const closeOnChoice=e=>{if(!mobile.open)return;const n=e.target.closest('a,button,input');if(n&&!n.closest('#panel-more-toggle,#panel-collapse')&&!n.matches('#panel-mobile-close'))mobileShell.close();};
  panel.addEventListener('click',closeOnChoice);
  get('panel-more-toggle').addEventListener('click',()=>{const open=get('panel-more').classList.toggle('more-open');get('panel-more-toggle').setAttribute('aria-expanded',String(open));});
  for(const [id,key]of [['panel-health','health'],['panel-renewal','renewal']])get(id).addEventListener('click',()=>apply(key));
  layout();
  const settings=el('dialog');settings.id='panel-settings-dialog';
  settings.innerHTML='<div class="shell-content"><label><input id="update-check-enabled" type="checkbox"> 检查新版本（每 12 小时访问一次 GitHub）</label><p>只读取版本号，不上传任何信息；关闭后不再访问。</p><p id="update-check-error" role="alert"></p></div><div class="shell-actions"><button id="update-check-cancel" type="button">取消</button><button id="update-check-save" class="primary-action" type="button">保存</button></div>';
  doc.body.append(settings);const settingsShell=root.DialogShell.attach(settings,{size:'small',title:'设置'});
  let versionData=null,versionReadAt=0,versionBusy=false,settingsBusy=false,closed=false;
  function updateVersion(view){
   versionData=view?.update||{};
   const latest=versionData.latest,valid=typeof latest==='string'&&/^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(latest)&&latest===latest.trim()&&root.AgentState.compareStableVersions(latest,latest)===0;
   const link=get('panel-update');link.hidden=!(versionData.newer===true&&valid);
   link.removeAttribute('href');if(!link.hidden){link.href='https://github.com/404-git-404/404-probe/releases/tag/'+latest;link.querySelector('span').textContent='有新版本 '+latest+' · 说明';link.title='有新版本 '+latest+' · 说明';}
  }
  async function refreshVersion(force=false){
   if(versionBusy||closed||(!force&&Date.now()-versionReadAt<30*60*1000))return;
   versionBusy=true;versionReadAt=Date.now();try{const view=await read('/api/v1/web/version');if(!closed)updateVersion(view);}catch{}finally{versionBusy=false;}
  }
  get('panel-settings').addEventListener('click',async e=>{
   get('update-check-error').textContent='';get('update-check-enabled').checked=versionData?.enabled===true;settingsShell.open(e.currentTarget);
   try{const view=await read('/api/v1/web/version');if(closed)return;updateVersion(view);if(!settingsBusy)get('update-check-enabled').checked=versionData.enabled===true;}catch{get('update-check-error').textContent='读取设置失败，仍可重新保存。';}
  });
  get('update-check-cancel').addEventListener('click',settingsShell.close);
  get('update-check-save').addEventListener('click',async()=>{
   if(settingsBusy)return;settingsBusy=true;get('update-check-save').disabled=true;get('update-check-error').textContent='';
   try{const update=await root.Management.write('/api/v1/web/settings/update-check',{method:'PUT',csrf:csrf(),payload:{enabled:get('update-check-enabled').checked},unauthorized:()=>root.location.assign('/login')});if(!closed){updateVersion({update});settingsShell.close();}}
   catch(error){get('update-check-error').textContent=error.status===403?'保存被拒绝（CSRF 校验失败或页面已过期），请刷新后重试。':'保存失败，请重试。';}
   finally{settingsBusy=false;get('update-check-save').disabled=false;}
  });
  const countryNames=typeof Intl.DisplayNames==='function'?new Intl.DisplayNames(['zh-CN'],{type:'region'}):null;
  const country=code=>code==='unknown'?'未知':(()=>{try{return countryNames?.of(code)||code;}catch{return code;}})();
  function optionRows(key,rows){
   const o=options[key],live=new Set();
   for(const [id,label,count]of rows){live.add(id);let v=o.nodes.get(id);if(!v){const plain=key==='groups'||key==='tags',n=el(plain?'button':'label','dashboard-option'),input=plain?null:el('input'),name=el('span'),number=el('small');if(plain){n.type='button';n.addEventListener('click',()=>toggle(key,id,!p[key].includes(id)));}else{input.type='checkbox';input.addEventListener('change',()=>toggle(key,id,input.checked));n.append(input);}n.append(name,number);o.list.append(n);v={n,input,name,number};o.nodes.set(id,v);}if(v.input)v.input.checked=p[key].includes(id);else v.n.setAttribute('aria-pressed',String(p[key].includes(id)));text(v.name,label);text(v.number,count);}
   for(const [id,v]of o.nodes)if(!live.has(id)){v.n.remove();o.nodes.delete(id);}
   const label={groups:'分组',tags:'标签',countries:'国家/地区',statuses:'状态'}[key];o.host.querySelector('[data-filter-label],summary').textContent=label+(key==='tags'?' · 匹配任一':'')+(p[key].length?' · '+p[key].length:'');o.host.classList.toggle('selected',!!p[key].length);
  }
  function update(agents,c,server,ready){
   catalog=c;if(ready)preferences.validate(catalog);model=M.select(agents,catalog,p,server);order.apply(model,catalog,p);
   const nextControls=JSON.stringify([ready,p,catalog.revision,model.summary.quick,model.summary.health,model.summary.online,model.list.length,[...agents.values()].map(a=>[a.agent_id,a.group_id,a.tag_ids,a.country_code,M.lifecycle(a)])]);
   if(nextControls===controlsKey)return model;controlsKey=nextControls;
   const all=[...agents.values()].filter(a=>!a.revoked),count=(key,id)=>all.filter(a=>key==='tags'?(a.tag_ids||[]).includes(id):key==='groups'?(catalog.groups.some(g=>g.id===a.group_id)?a.group_id:'__ungrouped')===id:key==='countries'?(a.country_code||'unknown')===id:M.lifecycle(a)===id).length;
   optionRows('groups',[...catalog.groups.map(g=>[g.id,g.name,count('groups',g.id)]),['__ungrouped','未分组',count('groups','__ungrouped')]]);
   const tagKey=JSON.stringify(catalog.tags.map(t=>[t.id,t.name]));
   optionRows('tags',catalog.tags.map(t=>[t.id,t.name,count('tags',t.id)]));get('dashboard-tags-clear').hidden=!p.tags.length;if(tagKey!==options.tags.layoutKey){options.tags.layoutKey=tagKey;scheduleTags();}
   optionRows('countries',[...new Set(all.map(a=>a.country_code||'unknown'))].sort().map(id=>[id,country(id),count('countries',id)]));
   optionRows('statuses',Object.entries(M.STATUS).map(([id,label])=>[id,label,count('statuses',id)]));
   for(const [key,b]of Object.entries(quickButtons)){text(quickCounts[key],model.summary.quick[key]);b.classList.toggle('is-zero',!model.summary.quick[key]);b.disabled=!model.summary.quick[key]&&!p.quick.includes(key);b.setAttribute('aria-pressed',String(p.quick.includes(key)));b.title=key==='traffic'?'用量达到套餐配额的 80%；部分统计可能低于实际用量':'';}
   const selected=[];if(p.search.trim())selected.push(['search','',`搜索：${p.search.trim()}`]);
   for(const key of ['groups','tags','countries','statuses','quick'])for(const id of p[key]){const label=key==='groups'?(id==='__ungrouped'?'未分组':catalog.groups.find(g=>g.id===id)?.name):key==='tags'?catalog.tags.find(t=>t.id===id)?.name:key==='countries'?country(id):key==='statuses'?M.STATUS[id]:M.QUICK[id];selected.push([key,id,label]);}
   const next=JSON.stringify(selected);if(next!==signature){signature=next;chips.replaceChildren(...selected.map(([key,id,label])=>button(label+' ×',()=>{if(key==='search'){p.search='';search.value='';}else p[key]=p[key].filter(x=>x!==id);change();},'dashboard-chip')));}
   get('dashboard-clear').hidden=!selected.length;counts.textContent=`显示 ${model.list.length} / ${all.length} 台`;get('dashboard-all').querySelector('small').textContent=all.length;get('dashboard-all').setAttribute('aria-pressed',String(!p.groups.length));
   return model;
  }
  return {preferences,effectiveView:()=>p.viewMode==='list'&&phone.matches?'cards':p.viewMode,update,apply,clear,sortBy,setSlot(slot){p.latencySlot=slot;change();},updateVersion,refreshVersion,collapse(id){toggle('collapsed',id,!p.collapsed.includes(id));},
   close(){closed=true;tagResize.disconnect();if(tagFrame)root.cancelAnimationFrame(tagFrame);settingsShell.close();narrow.removeEventListener('change',breakpoint);phone.removeEventListener('change',breakpoint);panel.removeEventListener('click',closeOnChoice);mobileShell.close();},snapshot:()=>({state:{...p}})};
 }
 root.DashboardView={create};
})(globalThis);
