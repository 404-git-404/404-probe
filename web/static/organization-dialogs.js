(function(root){
 'use strict';
 const N=root.OrganizationName;
 function create({api,read,agents,state,refresh,document:doc=root.document}){
  const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
  const button=(text,fn,cls='')=>{const n=el('button',cls,text);n.type='button';n.addEventListener('click',fn);return n;};
  function dialog(id,title,subtitle){const node=el('dialog','dashboard-dialog');node.id=id;doc.body.append(node);const shell=root.DialogShell.attach(node,{title,subtitle}),body=el('div','shell-content'),error=el('p','dashboard-dialog-error');error.setAttribute('role','alert');error.hidden=true;body.append(error);node.append(body);return {node,shell,body,error};}
  const assignment=dialog('organization-dialog','分组和标签',''),manage=dialog('organization-manage-dialog','管理分组和标签','每项修改立即保存，并同步到所有设备');
  let current=null,assignmentSerial=0,manageSerial=0,assignmentBusy=false,manageBusy=false,catalog=null,manageCatalog=null,tab='groups',editing=null,deleting=null;
  function showError(d,text){d.error.textContent=text;d.error.hidden=!text;}
  const groupRow=el('div','organization-group-row'),group=el('select'),newGroupButton=button('新建分组…',()=>{newGroup.hidden=!newGroup.hidden;if(!newGroup.hidden){group.value='';newGroup.focus();}}),newGroup=el('input');group.id='organization-group';newGroup.id='organization-new-group';newGroup.placeholder='新分组名称（最多 40 个字）';newGroup.hidden=true;
  const groupLabel=el('label','','分组（每台只属于一个）');groupLabel.htmlFor=group.id;groupRow.append(group,newGroupButton);
  const tagLabel=el('label','','标签（可以有多个）');tagLabel.htmlFor='organization-tag-input';const tagBox=el('div','organization-tag-box'),tagInput=el('input'),selected=el('div','organization-selected'),existing=el('div','organization-existing');tagInput.id='organization-tag-input';tagInput.placeholder='输入标签，回车添加';tagBox.append(selected,tagInput);
  const note=el('p','dashboard-dialog-note','标签最多 20 个字，每台最多 20 个；改名或删除标签请在“管理标签”统一处理。'),automatic=el('p','dashboard-dialog-info','国家/地区和在线状态会自动显示在首页筛选里，不用设置。');
  assignment.body.append(groupLabel,groupRow,newGroup,el('p','dashboard-dialog-note','不选分组就放在“未分组”里。'),tagLabel,tagBox,existing,note,automatic);
  const assignmentActions=el('div','shell-actions'),save=button('保存',saveAssignment,'primary-action');assignmentActions.append(button('取消',assignment.shell.close),save);assignment.node.append(assignmentActions);
  let chosen=[];
  function tagChips(){selected.replaceChildren(...chosen.map(t=>button(t.name+' ×',()=>{chosen=chosen.filter(x=>x!==t);tagChips();},'dashboard-chip')));existing.replaceChildren(el('span','dashboard-dialog-note','已有标签：'),...(catalog?.tags||[]).filter(t=>!chosen.some(x=>x.id===t.id)).map(t=>button('+ '+t.name,()=>addTag(t.name),'organization-existing-tag')));}
  function addTag(raw){const name=N.normalize(raw);if(!N.valid(raw,20)){showError(assignment,'标签名称需为 1–20 个字，不能含不可见控制字符。');return false;}const key=N.fold(name);if(chosen.some(t=>N.fold(t.name)===key)){tagInput.value='';return true;}if(chosen.length>=20){showError(assignment,'每台设备最多添加 20 个标签。');return false;}const found=catalog.tags.find(t=>N.fold(t.name)===key);chosen.push(found?{id:found.id,name:found.name}:{name});tagInput.value='';showError(assignment,'');tagChips();return true;}
  tagInput.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();if(tagInput.value.trim())addTag(tagInput.value);}});
  async function loadAssignment(serial=assignmentSerial,message=''){
   showError(assignment,'正在读取…');
   try{let c,value;for(let i=0;i<3;i++){[c,value]=await Promise.all([api.catalog(),read('/api/v1/web/agents/'+current+'/organization')]);if(c.revision===value.revision)break;if(i===2)throw Error('资料正在变化，请关闭后重新打开。');}
    if(serial!==assignmentSerial||!assignment.node.open)return;catalog=c;group.replaceChildren(new Option('未分组',''),...c.groups.map(g=>new Option(g.name,g.id)));group.value=value.item.group_id||'';newGroup.hidden=true;newGroup.value='';tagInput.value='';chosen=value.item.tag_ids.map(id=>c.tags.find(t=>t.id===id)).filter(Boolean).map(t=>({id:t.id,name:t.name}));tagChips();showError(assignment,message);
   }catch{if(serial===assignmentSerial)showError(assignment,message||'归属资料读取失败，请关闭后重试。');}
  }
  async function openAssignment(id,trigger){if(!agents.has(id))return;current=id;const serial=++assignmentSerial;assignment.shell.heading('分组和标签',agents.get(id).name);assignment.shell.open(trigger);await loadAssignment(serial);if(serial===assignmentSerial&&assignment.node.open)group.focus({preventScroll:true});}
  async function saveAssignment(){
   if(assignmentBusy||!catalog||!current)return;if(tagInput.value.trim()&&!addTag(tagInput.value))return;
   const creating=!newGroup.hidden;if(creating&&(!N.valid(newGroup.value,40)||N.normalize(newGroup.value)==='未分组')){showError(assignment,'分组名称需为 1–40 个字，不能使用“未分组”或不可见控制字符。');return;}
   assignmentBusy=true;save.disabled=true;const serial=assignmentSerial,mutation=state.beginMutation(current,['organization']);
   try{const body={expected_revision:catalog.revision,group_id:creating?null:group.value||null,tag_ids:chosen.filter(t=>t.id).map(t=>t.id),new_tags:chosen.filter(t=>!t.id).map(t=>t.name)};if(creating)body.new_group=N.normalize(newGroup.value);await api.request('PUT','agents/'+current+'/organization',body);state.finishMutation(mutation);await refresh();if(serial===assignmentSerial)assignment.shell.close();}
   catch(error){state.finishMutation(mutation);if(serial===assignmentSerial){if(error.status===409){await refresh();await loadAssignment(serial,error.message);}else showError(assignment,error.message);}}
   finally{state.finishMutation(mutation);assignmentBusy=false;save.disabled=false;}
  }
  assignment.node.addEventListener('close',()=>{assignmentSerial++;catalog=null;});
  // Management changes retain the revision loaded into this dialog, independent
  // of dashboard/SSE updates. Conflicts always reload, never replay a write.
  const tabs=el('div','organization-tabs'),tabsByKey={};tabs.setAttribute('role','tablist');
  for(const key of ['groups','tags']){const b=button('',()=>{tab=key;editing=deleting=null;renderManage();});b.setAttribute('role','tab');b.addEventListener('keydown',e=>{if(e.key==='ArrowLeft'||e.key==='ArrowRight'){e.preventDefault();tab=key==='groups'?'tags':'groups';editing=deleting=null;renderManage();tabsByKey[tab].focus();}});tabsByKey[key]=b;tabs.append(b);}
  const createRow=el('div','organization-create-row'),createInput=el('input'),createButton=button('',()=>mutateCreate(),'organization-create-button'),confirm=el('div','organization-delete-confirm'),rows=el('div','organization-manage-rows'),manageNote=el('p','dashboard-dialog-note');createInput.id='organization-create-name';createInput.setAttribute('aria-label','新建名称');createRow.append(createInput,createButton);manage.body.append(tabs,createRow,confirm,rows,manageNote);const manageActions=el('div','shell-actions');manageActions.append(button('关闭',manage.shell.close));manage.node.append(manageActions);
  createInput.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();mutateCreate();}});
  const count=(kind,id)=>(manageCatalog.items||[]).filter(a=>kind==='groups'?a.group_id===id:(a.tag_ids||[]).includes(id)).length;
  function renderManage(){if(!manageCatalog)return;for(const b of manage.node.querySelectorAll('button'))b.disabled=false;const isGroups=tab==='groups',items=manageCatalog[tab];
   for(const key of ['groups','tags']){tabsByKey[key].textContent=(key==='groups'?'分组':'标签')+' · '+manageCatalog[key].length;tabsByKey[key].setAttribute('aria-selected',String(tab===key));tabsByKey[key].tabIndex=tab===key?0:-1;}
   createInput.placeholder=(isGroups?'新分组':'新标签')+'名称（最多 '+(isGroups?40:20)+' 个字）';createButton.textContent=isGroups?'新建分组':'新建标签';
   confirm.hidden=!deleting;confirm.replaceChildren();if(deleting){const label=isGroups?'分组':'标签',n=count(tab,deleting.id),text=isGroups?`要删除分组“${deleting.name}”吗？${n} 台设备会回到“未分组”，标签保留。`:`要删除标签“${deleting.name}”吗？它会从 ${n} 台设备上移除。`;confirm.append(el('p','',text),button('取消',()=>{deleting=null;renderManage();}),button('删除',()=>mutate('DELETE',tab+'/'+deleting.id,null),'danger-action'));}
   rows.replaceChildren(...items.map((item,index)=>{const row=el('div','organization-manage-row');row.dataset.organizationId=item.id;
    if(isGroups){const up=button('↑',()=>reorder(index,-1)),down=button('↓',()=>reorder(index,1));up.setAttribute('aria-label','上移 '+item.name);down.setAttribute('aria-label','下移 '+item.name);up.disabled=index===0;down.disabled=index===items.length-1;row.append(up,down);}
    if(editing===item.id){const input=el('input');input.value=item.name;input.setAttribute('aria-label','修改名称');input.dataset.rename=item.id;const apply=()=>{if(!validName(input.value,isGroups?40:20))return;mutate('PATCH',tab+'/'+item.id,{name:N.normalize(input.value)});};input.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();apply();}if(e.key==='Escape'){e.preventDefault();e.stopPropagation();editing=null;renderManage();}});row.append(input,button('保存',apply,'primary-action'),button('取消',()=>{editing=null;renderManage();}));}
    else{row.append(el('strong','organization-item-name',item.name),el('small','',count(tab,item.id)+' 台'),button('改名',()=>{editing=item.id;deleting=null;renderManage();rows.querySelector('[data-rename]')?.focus();}),button('删除',()=>{deleting=item;editing=null;renderManage();},'organization-delete'));}
    return row;
   }));
   manageNote.textContent=isGroups?'删除分组后，设备回到“未分组”，设备本身和标签不受影响。“未分组”是系统保留的，不在这里出现。用 ↑↓ 调整首页顺序。':'改名会同时更新所有设备上的这个标签。删除会把它从所有设备上移除，设备本身不受影响。';
   for(const b of manage.node.querySelectorAll('button'))if(manageBusy)b.disabled=true;
  }
  function validName(raw,limit){if(!N.valid(raw,limit)||(tab==='groups'&&N.normalize(raw)==='未分组')){showError(manage,`名称需为 1–${limit} 个字，不能含不可见控制字符；“未分组”不能作为分组名称。`);return false;}return true;}
  async function loadManage(serial=manageSerial,message=''){try{const value=await api.catalog();if(serial!==manageSerial||!manage.node.open)return;manageCatalog=value;editing=deleting=null;renderManage();showError(manage,message);}catch{if(serial===manageSerial)showError(manage,'分组和标签读取失败，请关闭后重试。');}}
  async function openManage(key,trigger){tab=key==='tags'?'tags':'groups';createInput.value='';manageCatalog=null;const serial=++manageSerial;showError(manage,'正在读取…');manage.shell.open(trigger);await loadManage(serial);if(serial===manageSerial&&manage.node.open)createInput.focus({preventScroll:true});}
  async function mutate(method,path,body){if(manageBusy||!manageCatalog)return;manageBusy=true;const serial=manageSerial,mutations=[...agents.keys()].map(id=>state.beginMutation(id,['organization']));renderManage();
   const finish=()=>mutations.forEach(token=>state.finishMutation(token));
   try{await api.request(method,path,method==='DELETE'?null:{...body,expected_revision:manageCatalog.revision},manageCatalog.revision);finish();await refresh();if(serial===manageSerial){createInput.value='';await loadManage(serial);}}
   catch(error){finish();if(serial===manageSerial){if(error.status===409){await refresh();await loadManage(serial,error.message);}else showError(manage,error.message);}}
   finally{finish();manageBusy=false;if(serial===manageSerial)renderManage();}
  }
  function mutateCreate(){if(!validName(createInput.value,tab==='groups'?40:20))return;mutate('POST',tab,{name:N.normalize(createInput.value)});}
  function reorder(index,delta){const ids=manageCatalog.groups.map(g=>g.id);[ids[index],ids[index+delta]]=[ids[index+delta],ids[index]];mutate('PUT','groups/order',{ids});}
  manage.node.addEventListener('close',()=>{manageSerial++;manageCatalog=null;});
  return {openAssignment,openManage,close(){assignment.shell.close();manage.shell.close();}};
 }
 root.OrganizationDialogs={create};
})(globalThis);
