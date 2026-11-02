(function(root){
 'use strict';
 const M=root.DashboardModel;
 function create({document:doc=root.document,onchange=()=>{},onManage=()=>{}}={}){
  const preferences=M.preferences(),p=preferences.value,get=id=>doc.getElementById(id);
  const search=get('dashboard-search');
  const sort=get('dashboard-sort'),chips=get('dashboard-chips'),counts=get('dashboard-count'),quick=get('dashboard-quick'),options={};
  let catalog={groups:[],tags:[]},model=null,signature='',controlsKey='';
  const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
  const button=(text,action,cls='')=>{const b=el('button',cls,text);b.type='button';b.addEventListener('click',action);return b;};
  function change(){preferences.save();onchange();}
  search.value=p.search;sort.value=p.sort;
  search.addEventListener('input',()=>{p.search=search.value;change();});
  sort.addEventListener('change',()=>{p.sort=sort.value;change();});
  const clear=()=>{for(const k of ['groups','tags','countries','statuses','quick'])p[k]=[];p.search='';search.value='';change();};
  get('dashboard-clear').addEventListener('click',clear);
  get('dashboard-all').addEventListener('click',()=>{p.groups=[];change();});
  function toggle(key,id,checked){p[key]=p[key].filter(x=>x!==id);if(checked)p[key].push(id);change();}
  for(const key of ['groups','tags','countries','statuses']){
   const host=get('dashboard-'+key),list=host.querySelector('.dashboard-options');options[key]={host,list,nodes:new Map()};
   if(key==='groups'||key==='tags')list.after(button(key==='groups'?'管理分组…':'管理标签…',e=>onManage(key==='groups'?'groups':'tags',host.querySelector('summary')),'dashboard-manage'));
  }
  const quickButtons={};for(const key of ['offline','network','resources','traffic','renewal']){
   const b=button('',()=>toggle('quick',key,!p.quick.includes(key)),'dashboard-quick-item');b.dataset.quick=key;quickButtons[key]=b;quick.append(b);
  }
  function apply(key){if(!p.quick.includes(key))p.quick.push(key);change();}
  const countryNames=typeof Intl.DisplayNames==='function'?new Intl.DisplayNames(['zh-CN'],{type:'region'}):null;
  const country=code=>code==='unknown'?'未知':(()=>{try{return countryNames?.of(code)||code;}catch{return code;}})();
  function optionRows(key,rows){
   const o=options[key],live=new Set();
   for(const [id,label,count]of rows){live.add(id);let v=o.nodes.get(id);if(!v){const n=el('label','dashboard-option'),input=el('input'),name=el('span'),number=el('small');input.type='checkbox';input.addEventListener('change',()=>toggle(key,id,input.checked));n.append(input,name,number);o.list.append(n);v={n,input,name,number};o.nodes.set(id,v);}v.input.checked=p[key].includes(id);v.name.textContent=label;v.number.textContent=count;}
   for(const [id,v]of o.nodes)if(!live.has(id)){v.n.remove();o.nodes.delete(id);}
   const label={groups:'分组',tags:'标签',countries:'国家/地区',statuses:'状态'}[key];o.host.querySelector('summary').textContent=label+(key==='tags'?' · 匹配任一':'')+(p[key].length?' · '+p[key].length:'');o.host.classList.toggle('selected',!!p[key].length);
  }
  function update(agents,c,server,ready){
   catalog=c;if(ready)preferences.validate(catalog);model=M.select(agents,catalog,p,server);
   const nextControls=JSON.stringify([ready,p,catalog.revision,model.summary.quick,model.summary.health,model.summary.online,model.list.length,[...agents.values()].map(a=>[a.agent_id,a.group_id,a.tag_ids,a.country_code,M.lifecycle(a)])]);
   if(nextControls===controlsKey)return model;controlsKey=nextControls;
   const all=[...agents.values()].filter(a=>!a.revoked),count=(key,id)=>all.filter(a=>key==='tags'?(a.tag_ids||[]).includes(id):key==='groups'?(catalog.groups.some(g=>g.id===a.group_id)?a.group_id:'__ungrouped')===id:key==='countries'?(a.country_code||'unknown')===id:M.lifecycle(a)===id).length;
   optionRows('groups',[...catalog.groups.map(g=>[g.id,g.name,count('groups',g.id)]),['__ungrouped','未分组',count('groups','__ungrouped')]]);
   optionRows('tags',catalog.tags.map(t=>[t.id,t.name,count('tags',t.id)]));
   optionRows('countries',[...new Set(all.map(a=>a.country_code||'unknown'))].sort().map(id=>[id,country(id),count('countries',id)]));
   optionRows('statuses',Object.entries(M.STATUS).map(([id,label])=>[id,label,count('statuses',id)]));
   for(const [key,b]of Object.entries(quickButtons)){b.textContent=M.QUICK[key];const count=el('small','',model.summary.quick[key]);b.append(count);b.disabled=!model.summary.quick[key]&&!p.quick.includes(key);b.setAttribute('aria-pressed',String(p.quick.includes(key)));b.title=key==='traffic'?'用量达到套餐配额的 80%；部分统计可能低于实际用量':'';}
   const selected=[];if(p.search.trim())selected.push(['search','',`搜索：${p.search.trim()}`]);
   for(const key of ['groups','tags','countries','statuses','quick'])for(const id of p[key]){const label=key==='groups'?(id==='__ungrouped'?'未分组':catalog.groups.find(g=>g.id===id)?.name):key==='tags'?catalog.tags.find(t=>t.id===id)?.name:key==='countries'?country(id):key==='statuses'?M.STATUS[id]:M.QUICK[id];selected.push([key,id,label]);}
   const next=JSON.stringify(selected);if(next!==signature){signature=next;chips.replaceChildren(...selected.map(([key,id,label])=>button(label+' ×',()=>{if(key==='search'){p.search='';search.value='';}else p[key]=p[key].filter(x=>x!==id);change();},'dashboard-chip')));}
   get('dashboard-clear').hidden=!selected.length;counts.textContent=`显示 ${model.list.length} / ${all.length} 台`;get('dashboard-all').querySelector('small').textContent=all.length;get('dashboard-all').setAttribute('aria-pressed',String(!p.groups.length));
   return model;
  }
  return {preferences,update,apply,clear,collapse(id){toggle('collapsed',id,!p.collapsed.includes(id));},
   close(){},snapshot:()=>({state:{...p}})};
 }
 root.DashboardView={create};
})(globalThis);
