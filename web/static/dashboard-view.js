(function(root){
 'use strict';
 const M=root.DashboardModel;
 function create({document:doc=root.document,onchange=()=>{},onManage=()=>{}}={}){
  const preferences=M.preferences(),p=preferences.value,get=id=>doc.getElementById(id);
  const panel=get('dashboard-filters'),home=get('dashboard-filter-home'),dialog=get('dashboard-filter-dialog');
  const shell=root.DialogShell.attach(dialog,{title:'筛选设备'});
  dialog.addEventListener('close',()=>home.append(panel));
  const search=get('dashboard-search'),searchHome=get('dashboard-search-slot'),stickyHome=get('dashboard-sticky-search-slot'),sticky=get('dashboard-sticky');
  const sort=get('dashboard-sort'),chips=get('dashboard-chips'),counts=get('dashboard-count'),quick=get('dashboard-quick'),options={};
  let catalog={groups:[],tags:[]},model=null,signature='',stuck=false,controlsKey='';
  const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
  const button=(text,action,cls='')=>{const b=el('button',cls,text);b.type='button';b.addEventListener('click',action);return b;};
  function change(){preferences.save();onchange();}
  search.value=p.search;sort.value=p.sort;
  search.addEventListener('input',()=>{p.search=search.value;change();});
  sort.addEventListener('change',()=>{p.sort=sort.value;change();});
  const clear=()=>{for(const k of ['groups','tags','countries','statuses','quick'])p[k]=[];p.search='';search.value='';change();};
  get('dashboard-clear').addEventListener('click',clear);
  function toggle(key,id,checked){p[key]=p[key].filter(x=>x!==id);if(checked)p[key].push(id);change();}
  for(const key of ['groups','tags','countries','statuses']){
   const host=get('dashboard-'+key),list=host.querySelector('.dashboard-options');options[key]={host,list,nodes:new Map()};
   host.addEventListener('toggle',()=>{if(host.open)for(const other of Object.values(options))if(other.host!==host)other.host.open=false;});
   if(key==='groups'||key==='tags')list.after(button(key==='groups'?'管理分组…':'管理标签…',e=>onManage(key==='groups'?'groups':'tags',host.querySelector('summary')),'dashboard-manage'));
  }
  const closeDropdowns=()=>{let trigger=null;for(const {host}of Object.values(options))if(host.open){host.open=false;trigger=host.querySelector('summary');}trigger?.focus({preventScroll:true});};
  const outside=e=>{if(!Object.values(options).some(({host})=>host.contains(e.target)))closeDropdowns();};
  const escape=e=>{if(e.key==='Escape'&&Object.values(options).some(({host})=>host.open)){e.preventDefault();e.stopPropagation();closeDropdowns();}};
  doc.addEventListener('click',outside);doc.addEventListener('keydown',escape,true);
  const quickButtons={};for(const key of ['offline','network','resources','traffic','renewal']){
   const b=button('',()=>toggle('quick',key,!p.quick.includes(key)),'dashboard-quick-item');b.dataset.quick=key;quickButtons[key]=b;quick.append(b);
  }
  function apply(key){if(!p.quick.includes(key))p.quick.push(key);p.expanded=true;change();}
  function showFilters(trigger){get('dashboard-filter-body').append(panel);shell.open(trigger);}
  get('dashboard-filter-close').addEventListener('click',shell.close);
  get('dashboard-sticky-filter').addEventListener('click',e=>showFilters(e.currentTarget));
  get('dashboard-expand').addEventListener('click',()=>{p.expanded=true;change();search.focus({preventScroll:true});});
  get('dashboard-top').addEventListener('click',()=>root.scrollTo({top:0,behavior:'smooth'}));
  for(const id of ['dashboard-sticky-health','dashboard-sticky-renewal'])get(id).addEventListener('click',()=>apply(id.endsWith('health')?'health':'renewal'));
  function stick(value){
   if(stuck===value)return;stuck=value;sticky.hidden=!value;
   const focused=doc.activeElement===search,start=search.selectionStart,end=search.selectionEnd,target=value?stickyHome:searchHome;
   if(target.moveBefore)target.moveBefore(search,null);else{target.append(search);if(focused){search.focus({preventScroll:true});search.setSelectionRange(start,end);}}
  }
  const observer=root.IntersectionObserver?new root.IntersectionObserver(entries=>stick(!entries[0].isIntersecting&&entries[0].boundingClientRect.top<0)):null;
  observer?.observe(get('dashboard-anchor'));
  // A jump can move a below-viewport anchor above the viewport without ever
  // intersecting it, leaving IntersectionObserver's boolean unchanged.
  let scrollFrame=0;
  const onScroll=()=>{if(!scrollFrame)scrollFrame=root.requestAnimationFrame(()=>{scrollFrame=0;stick(get('dashboard-anchor').getBoundingClientRect().bottom<=0);});};
  root.addEventListener('scroll',onScroll,{passive:true});
  const countryNames=typeof Intl.DisplayNames==='function'?new Intl.DisplayNames(['zh-CN'],{type:'region'}):null;
  const country=code=>code==='unknown'?'未知':(()=>{try{return countryNames?.of(code)||code;}catch{return code;}})();
  function optionRows(key,rows){
   const o=options[key],live=new Set();
   for(const [id,label,count]of rows){live.add(id);let v=o.nodes.get(id);if(!v){const n=el('label','dashboard-option'),input=el('input'),name=el('span'),number=el('small');input.type='checkbox';input.addEventListener('change',()=>toggle(key,id,input.checked));n.append(input,name,number);o.list.append(n);v={n,input,name,number};o.nodes.set(id,v);}v.input.checked=p[key].includes(id);v.name.textContent=label;v.number.textContent=count;}
   for(const [id,v]of o.nodes)if(!live.has(id)){v.n.remove();o.nodes.delete(id);}
   const label={groups:'分组',tags:'标签',countries:'国家/地区',statuses:'状态'}[key];o.host.querySelector('summary').textContent=label+(p[key].length?' · '+p[key].length:'')+' ▾';o.host.classList.toggle('selected',!!p[key].length);
  }
  function update(agents,c,server,ready){
   catalog=c;if(ready)preferences.validate(catalog);model=M.select(agents,catalog,p,server);
   const nextControls=JSON.stringify([ready,p,catalog.revision,model.summary.quick,model.summary.online,model.list.length,[...agents.values()].map(a=>[a.agent_id,a.group_id,a.tag_ids,a.country_code,M.lifecycle(a)])]);
   if(nextControls===controlsKey)return model;controlsKey=nextControls;
   home.hidden=model.simple;get('dashboard-expand').hidden=!model.simple;
   const all=[...agents.values()].filter(a=>!a.revoked),count=(key,id)=>all.filter(a=>key==='tags'?(a.tag_ids||[]).includes(id):key==='groups'?(catalog.groups.some(g=>g.id===a.group_id)?a.group_id:'__ungrouped')===id:key==='countries'?(a.country_code||'unknown')===id:M.lifecycle(a)===id).length;
   optionRows('groups',[...catalog.groups.map(g=>[g.id,g.name,count('groups',g.id)]),['__ungrouped','未分组',count('groups','__ungrouped')]]);
   optionRows('tags',catalog.tags.map(t=>[t.id,t.name,count('tags',t.id)]));
   optionRows('countries',[...new Set(all.map(a=>a.country_code||'unknown'))].sort().map(id=>[id,country(id),count('countries',id)]));
   optionRows('statuses',Object.entries(M.STATUS).map(([id,label])=>[id,label,count('statuses',id)]));
   for(const [key,b]of Object.entries(quickButtons)){b.textContent=M.QUICK[key]+' '+model.summary.quick[key];b.disabled=!model.summary.quick[key]&&!p.quick.includes(key);b.setAttribute('aria-pressed',String(p.quick.includes(key)));b.title=key==='traffic'?'用量达到套餐配额的 80%；部分统计可能低于实际用量':'';}
   const selected=[];if(p.search.trim())selected.push(['search','',`搜索：${p.search.trim()}`]);
   for(const key of ['groups','tags','countries','statuses','quick'])for(const id of p[key]){const label=key==='groups'?(id==='__ungrouped'?'未分组':catalog.groups.find(g=>g.id===id)?.name):key==='tags'?catalog.tags.find(t=>t.id===id)?.name:key==='countries'?country(id):key==='statuses'?M.STATUS[id]:M.QUICK[id];selected.push([key,id,label]);}
   const next=JSON.stringify(selected);if(next!==signature){signature=next;chips.replaceChildren(...selected.map(([key,id,label])=>button(label+' ×',()=>{if(key==='search'){p.search='';search.value='';}else p[key]=p[key].filter(x=>x!==id);change();},'dashboard-chip')));}
   get('dashboard-clear').hidden=!selected.length;counts.textContent=`显示 ${model.list.length} / ${all.length} 台`;get('dashboard-sticky-count').textContent=counts.textContent;
   get('dashboard-sticky-filter').textContent='筛选'+(selected.length?' · '+selected.length:'')+' ▾';
   get('dashboard-sticky-online').textContent=`${model.summary.online} / ${all.length} 在线`;
   get('dashboard-sticky-health').textContent=model.summary.health+' 台需要关注';get('dashboard-sticky-renewal').textContent=model.summary.renewal+' 台 7 天内续费';
   return model;
  }
  return {preferences,update,apply,clear,collapse(id){toggle('collapsed',id,!p.collapsed.includes(id));},
   close(){observer?.disconnect();doc.removeEventListener('click',outside);doc.removeEventListener('keydown',escape,true);root.removeEventListener('scroll',onScroll);if(scrollFrame)root.cancelAnimationFrame(scrollFrame);shell.close();},snapshot:()=>({state:{...p},stuck,simple:model?.simple})};
 }
 root.DashboardView={create};
})(globalThis);
