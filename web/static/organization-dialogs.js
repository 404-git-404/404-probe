(function(root){
 'use strict';
 const N=root.OrganizationName;
 const TAG_COLORS=[['blue','蓝'],['sky','天蓝'],['cyan','青'],['teal','青绿'],['indigo','靛'],['purple','紫'],['pink','粉'],['gray','灰']];
 const TAG_COLOR_LABELS=Object.fromEntries(TAG_COLORS);
 const validColor=c=>(typeof c==='string'&&Object.hasOwn(TAG_COLOR_LABELS,c))?c:'';
 function create({api,read,agents,state,refresh,document:doc=root.document}){
  const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
  const button=(text,fn,cls='')=>{const n=el('button',cls,text);n.type='button';n.addEventListener('click',fn);return n;};
  function dialog(id,title,subtitle){const node=el('dialog','dashboard-dialog');node.id=id;doc.body.append(node);const shell=root.DialogShell.attach(node,{title,subtitle}),body=el('div','shell-content'),error=el('p','dashboard-dialog-error');error.setAttribute('role','alert');error.hidden=true;body.append(error);node.append(body);return {node,shell,body,error};}
  const assignment=dialog('organization-dialog','分组和标签',''),manage=dialog('organization-manage-dialog','管理分组和标签','每项修改立即保存，并同步到所有设备');
  let current=null,assignmentSerial=0,manageSerial=0,assignmentBusy=false,manageBusy=false,catalog=null,manageCatalog=null,tab='groups',editing=null,deleting=null,newTagColor=null,openColorKey=null;
  const colorErrors=new Map(),pendingColors=new Map();
  function showError(d,text){d.error.textContent=text;d.error.hidden=!text;}
  const groupRow=el('div','organization-group-row'),group=el('select'),newGroupButton=button('新建分组…',()=>{newGroup.hidden=!newGroup.hidden;if(!newGroup.hidden){group.value='';newGroup.focus();}}),newGroup=el('input');group.id='organization-group';newGroup.id='organization-new-group';newGroup.placeholder='新分组名称（最多 40 个字）';newGroup.hidden=true;
  const groupLabel=el('label','','分组（每台只属于一个）');groupLabel.htmlFor=group.id;groupRow.append(group,newGroupButton);
  const tagLabel=el('label','','标签（可以有多个）');tagLabel.htmlFor='organization-tag-input';const tagBox=el('div','organization-tag-box'),tagInput=el('input'),selected=el('div','organization-selected'),existing=el('div','organization-existing');tagInput.id='organization-tag-input';tagInput.placeholder='输入标签，回车添加';tagBox.append(selected,tagInput);
  const note=el('p','dashboard-dialog-note','标签最多 20 个字，每台最多 20 个；改名或删除标签请在“管理标签”统一处理。'),automatic=el('p','dashboard-dialog-info','国家/地区和在线状态会自动显示在首页筛选里，不用设置。');
  assignment.body.append(groupLabel,groupRow,newGroup,el('p','dashboard-dialog-note','不选分组就放在“未分组”里。'),tagLabel,tagBox,existing,note,automatic);
  const assignmentActions=el('div','shell-actions'),save=button('保存',saveAssignment,'primary-action');assignmentActions.append(button('取消',assignment.shell.close),save);assignment.node.append(assignmentActions);
  let chosen=[];
  assignment.shell.setGuard({busy:()=>assignmentBusy,read:()=>[group.value,newGroup.hidden?'':newGroup.value,tagInput.value,chosen.map(t=>t.id||t.name).sort()]});
  function tagChips(){
   selected.replaceChildren(...chosen.map(t=>{const b=button(t.name+' ×',()=>{chosen=chosen.filter(x=>x!==t);tagChips();},'dashboard-chip');const c=validColor(t.color);if(c)b.dataset.tagColor=c;return b;}));
   existing.replaceChildren(el('span','dashboard-dialog-note','已有标签：'),...(catalog?.tags||[]).filter(t=>!chosen.some(x=>x.id===t.id)).map(t=>{const b=button('+ '+t.name,()=>addTag(t.name),'organization-existing-tag');const c=validColor(t.color);if(c)b.dataset.tagColor=c;return b;}));
  }
  function addTag(raw){const name=N.normalize(raw);if(!N.valid(raw,20)){showError(assignment,'标签名称需为 1–20 个字，不能含不可见控制字符。');return false;}const key=N.fold(name);if(chosen.some(t=>N.fold(t.name)===key)){tagInput.value='';return true;}if(chosen.length>=20){showError(assignment,'每台设备最多添加 20 个标签。');return false;}const found=catalog.tags.find(t=>N.fold(t.name)===key);chosen.push(found?{id:found.id,name:found.name,color:found.color||''}:{name});tagInput.value='';showError(assignment,'');tagChips();return true;}
  tagInput.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();if(tagInput.value.trim())addTag(tagInput.value);}});
  async function loadAssignment(serial=assignmentSerial,message=''){
   showError(assignment,'正在读取…');
   try{let c,value;for(let i=0;i<3;i++){[c,value]=await Promise.all([api.catalog(),read('/api/v1/web/agents/'+current+'/organization')]);if(c.revision===value.revision)break;if(i===2)throw Error('资料正在变化，请关闭后重新打开。');}
    if(serial!==assignmentSerial||!assignment.node.open)return;catalog=c;group.replaceChildren(new Option('未分组',''),...c.groups.map(g=>new Option(g.name,g.id)));group.value=value.item.group_id||'';newGroup.hidden=true;newGroup.value='';tagInput.value='';chosen=value.item.tag_ids.map(id=>c.tags.find(t=>t.id===id)).filter(Boolean).map(t=>({id:t.id,name:t.name,color:t.color||''}));tagChips();assignment.shell.markSaved();showError(assignment,message);
   }catch{if(serial===assignmentSerial)showError(assignment,message||'归属资料读取失败，请关闭后重试。');}
  }
  async function openAssignment(id,trigger){if(!agents.has(id))return;current=id;const serial=++assignmentSerial;assignment.shell.heading('分组和标签',agents.get(id).name);assignment.shell.open(trigger);await loadAssignment(serial);if(serial===assignmentSerial&&assignment.node.open)group.focus({preventScroll:true});}
  async function saveAssignment(){
   if(assignmentBusy||!catalog||!current)return;if(tagInput.value.trim()&&!addTag(tagInput.value))return;
   const creating=!newGroup.hidden;if(creating&&(!N.valid(newGroup.value,40)||N.normalize(newGroup.value)==='未分组')){showError(assignment,'分组名称需为 1–40 个字，不能使用“未分组”或不可见控制字符。');return;}
   assignmentBusy=true;save.disabled=true;const serial=assignmentSerial,mutation=state.beginMutation(current,['organization']);
   try{const body={expected_revision:catalog.revision,group_id:creating?null:group.value||null,tag_ids:chosen.filter(t=>t.id).map(t=>t.id),new_tags:chosen.filter(t=>!t.id).map(t=>t.name)};if(creating)body.new_group=N.normalize(newGroup.value);await api.request('PUT','agents/'+current+'/organization',body);state.finishMutation(mutation);await refresh(current);if(serial===assignmentSerial)assignment.shell.close();}
   catch(error){state.finishMutation(mutation);if(serial===assignmentSerial){if(error.status===409){await refresh();await loadAssignment(serial,error.message);}else showError(assignment,error.message);}}
   finally{state.finishMutation(mutation);assignmentBusy=false;save.disabled=false;}
  }
  assignment.node.addEventListener('close',()=>{if(assignment.node.open)return;assignmentSerial++;catalog=null;});
  // Management changes retain the revision loaded into this dialog, independent
  // of dashboard/SSE updates. Conflicts always reload, never replay a write.
  const tabs=el('div','organization-tabs'),tabsByKey={};tabs.setAttribute('role','tablist');
  for(const key of ['groups','tags']){const b=button('',()=>{tab=key;editing=deleting=openColorKey=null;renderManage();});b.setAttribute('role','tab');b.addEventListener('keydown',e=>{if(e.key==='ArrowLeft'||e.key==='ArrowRight'){e.preventDefault();tab=key==='groups'?'tags':'groups';editing=deleting=openColorKey=null;renderManage();tabsByKey[tab].focus();}});tabsByKey[key]=b;tabs.append(b);}
  const createRow=el('div','organization-create-row'),newColorSlot=el('div','tag-color-picker'),createInput=el('input'),createButton=button('',()=>mutateCreate(),'organization-create-button'),confirm=el('div','organization-delete-confirm'),rows=el('div','organization-manage-rows'),manageNote=el('p','dashboard-dialog-note');createInput.id='organization-create-name';createInput.setAttribute('aria-label','新建名称');createRow.append(newColorSlot,createInput,createButton);manage.body.append(tabs,createRow,confirm,rows,manageNote);const manageActions=el('div','shell-actions');manageActions.append(button('关闭',manage.shell.close));manage.node.append(manageActions);
  manage.shell.setGuard({busy:()=>manageBusy,dirty:()=>!!createInput.value||[...rows.querySelectorAll('input')].some(n=>n.value!==(manageCatalog?.[tab]||[]).find(v=>v.id===n.dataset.rename)?.name)});
  createInput.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();mutateCreate();}});
  manage.node.addEventListener('pointerdown',e=>{if(openColorKey&&!e.target.closest('.tag-color-picker')){openColorKey=null;renderManage();}});
  manage.node.addEventListener('keydown',e=>{if(openColorKey&&e.key==='Escape'){e.preventDefault();e.stopPropagation();const key=openColorKey;openColorKey=null;renderManage();manage.node.querySelector(`.tag-color-trigger[data-color-key="${key}"]`)?.focus({preventScroll:true});}},{capture:true});
  const count=(kind,id)=>(manageCatalog.items||[]).filter(a=>kind==='groups'?a.group_id===id:(a.tag_ids||[]).includes(id)).length;
  function buildColorPicker({key,currentColor,ariaSubject,onPick,host=null}){
   const wrap=host||el('div','tag-color-picker');
   wrap.replaceChildren();
   const c=validColor(currentColor);
   const trigger=el('button','tag-color-trigger');
   trigger.type='button';
   trigger.dataset.colorKey=key;
   if(c){
    trigger.dataset.tagColor=c;
    trigger.title='颜色：'+TAG_COLOR_LABELS[c];
    trigger.setAttribute('aria-label',`${ariaSubject}（当前：${TAG_COLOR_LABELS[c]}）`);
   }else{
    trigger.removeAttribute('data-tag-color');
    trigger.title='颜色：自动（点击选择）';
    trigger.setAttribute('aria-label',`${ariaSubject}（默认自动）`);
   }
   trigger.setAttribute('aria-haspopup','true');
   trigger.setAttribute('aria-expanded',String(openColorKey===key));
   trigger.addEventListener('click',e=>{
    e.stopPropagation();
    openColorKey=openColorKey===key?null:key;
    renderManage();
   });
   wrap.append(trigger);
   if(openColorKey===key){
    const palette=el('div','tag-color-palette');
    palette.setAttribute('role','group');
    palette.setAttribute('aria-label','选择标签颜色');
    for(const [colorKey,label] of TAG_COLORS){
     const sw=el('button','tag-color-swatch');
     sw.type='button';
     sw.dataset.tagColor=colorKey;
     sw.dataset.color=colorKey;
     sw.title=label;
     sw.setAttribute('aria-label',label);
     sw.setAttribute('aria-pressed',String(c===colorKey));
     sw.addEventListener('click',e=>{e.stopPropagation();onPick(colorKey);});
     palette.append(sw);
    }
    wrap.append(palette);
   }
   return wrap;
  }
  function renderManage(){if(!manageCatalog)return;for(const b of manage.node.querySelectorAll('button'))b.disabled=false;const isGroups=tab==='groups',items=manageCatalog[tab];
   for(const key of ['groups','tags']){tabsByKey[key].textContent=(key==='groups'?'分组':'标签')+' · '+manageCatalog[key].length;tabsByKey[key].setAttribute('aria-selected',String(tab===key));tabsByKey[key].tabIndex=tab===key?0:-1;}
   createInput.placeholder=(isGroups?'新分组':'新标签')+'名称（最多 '+(isGroups?40:20)+' 个字）';createButton.textContent=isGroups?'新建分组':'新建标签';
   newColorSlot.hidden=isGroups;
   if(!isGroups){
    buildColorPicker({
     key:'__new__',
     currentColor:newTagColor,
     ariaSubject:'选择新标签颜色',
     host:newColorSlot,
     onPick:colorKey=>{newTagColor=newTagColor===colorKey?null:colorKey;openColorKey=null;renderManage();},
    });
   }else newColorSlot.replaceChildren();
   confirm.hidden=!deleting;confirm.replaceChildren();if(deleting){const label=isGroups?'分组':'标签',n=count(tab,deleting.id),text=isGroups?`要删除分组“${deleting.name}”吗？${n} 台设备会回到“未分组”，标签保留。`:`要删除标签“${deleting.name}”吗？它会从 ${n} 台设备上移除。`;confirm.append(el('p','',text),button('取消',()=>{deleting=null;renderManage();}),button('删除',()=>mutate('DELETE',tab+'/'+deleting.id,null),'danger-action'));}
   rows.replaceChildren(...items.map((item,index)=>{const row=el('div','organization-manage-row');row.dataset.organizationId=item.id;
    if(isGroups){const up=button('↑',()=>reorder(index,-1)),down=button('↓',()=>reorder(index,1));up.setAttribute('aria-label','上移 '+item.name);down.setAttribute('aria-label','下移 '+item.name);up.disabled=index===0;down.disabled=index===items.length-1;row.append(up,down);}
    else{
     const displayColor=pendingColors.has(item.id)?pendingColors.get(item.id):(validColor(item.color)||'gray');
     row.append(buildColorPicker({
      key:item.id,
      currentColor:displayColor,
      ariaSubject:'修改 '+item.name+' 的颜色',
      onPick:colorKey=>mutateColor(item,colorKey),
     }));
    }
    if(editing===item.id){const input=el('input');input.value=item.name;input.setAttribute('aria-label','修改名称');input.dataset.rename=item.id;const apply=()=>{if(!validName(input.value,isGroups?40:20))return;mutate('PATCH',tab+'/'+item.id,{name:N.normalize(input.value)});};input.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();apply();}if(e.key==='Escape'){e.preventDefault();e.stopPropagation();editing=null;renderManage();}});row.append(input,button('保存',apply,'primary-action'),button('取消',()=>{editing=null;renderManage();}));}
    else{row.append(el('strong','organization-item-name',item.name),el('small','',count(tab,item.id)+' 台'),button('改名',()=>{editing=item.id;deleting=openColorKey=null;renderManage();rows.querySelector('[data-rename]')?.focus();}),button('删除',()=>{deleting=item;editing=openColorKey=null;renderManage();},'organization-delete'));}
    if(!isGroups&&colorErrors.has(item.id)){const err=el('p','tag-color-error',colorErrors.get(item.id));err.setAttribute('role','alert');row.append(err);}
    return row;
   }));
   manageNote.textContent=isGroups?'删除分组后，设备回到“未分组”，设备本身和标签不受影响。“未分组”是系统保留的，不在这里出现。用 ↑↓ 调整首页顺序。':'点标签前的色块可更换颜色。改名会同时更新所有设备上的这个标签。删除会把它从所有设备上移除，设备本身不受影响。';
   for(const b of manage.node.querySelectorAll('button'))if(manageBusy)b.disabled=true;
  }
  function validName(raw,limit){if(!N.valid(raw,limit)||(tab==='groups'&&N.normalize(raw)==='未分组')){showError(manage,`名称需为 1–${limit} 个字，不能含不可见控制字符；“未分组”不能作为分组名称。`);return false;}return true;}
  async function loadManage(serial=manageSerial,message=''){try{const value=await api.catalog();if(serial!==manageSerial||!manage.node.open)return;manageCatalog=value;editing=deleting=openColorKey=null;pendingColors.clear();colorErrors.clear();renderManage();manage.shell.markSaved();showError(manage,message);}catch{if(serial===manageSerial)showError(manage,'分组和标签读取失败，请关闭后重试。');}}
  async function openManage(key,trigger){tab=key==='tags'?'tags':'groups';createInput.value='';newTagColor=null;openColorKey=null;pendingColors.clear();colorErrors.clear();manageCatalog=null;const serial=++manageSerial;showError(manage,'正在读取…');manage.shell.open(trigger);await loadManage(serial);if(serial===manageSerial&&manage.node.open)createInput.focus({preventScroll:true});}
  async function mutate(method,path,body){if(manageBusy||!manageCatalog)return;manageBusy=true;const serial=manageSerial,mutations=[...agents.keys()].map(id=>state.beginMutation(id,['organization']));renderManage();
   const finish=()=>mutations.forEach(token=>state.finishMutation(token));
   try{await api.request(method,path,method==='DELETE'?null:{...body,expected_revision:manageCatalog.revision},manageCatalog.revision);finish();await refresh();if(serial===manageSerial){createInput.value='';newTagColor=null;await loadManage(serial);}}
   catch(error){finish();if(serial===manageSerial){if(error.status===409){await refresh();await loadManage(serial,error.message);}else showError(manage,error.message);}}
   finally{finish();manageBusy=false;if(serial===manageSerial)renderManage();}
  }
  async function mutateColor(item,nextColor){
   openColorKey=null;
   colorErrors.delete(item.id);
   if(nextColor===item.color){renderManage();return;}
   if(manageBusy||!manageCatalog)return;
   manageBusy=true;
   pendingColors.set(item.id,nextColor);
   const serial=manageSerial,mutations=[...agents.keys()].map(id=>state.beginMutation(id,['organization']));
   renderManage();
   const finish=()=>mutations.forEach(token=>state.finishMutation(token));
   try{
    await api.request('PATCH','tags/'+item.id,{color:nextColor,expected_revision:manageCatalog.revision},manageCatalog.revision);
    pendingColors.delete(item.id);
    finish();
    await refresh();
    if(serial===manageSerial)await loadManage(serial);
   }catch(error){
    pendingColors.delete(item.id);
    finish();
    if(serial===manageSerial){
     if(error.status===409){await refresh();await loadManage(serial,error.message);}
     else colorErrors.set(item.id,error.message||'颜色保存失败，请重试。');
    }
   }finally{
    finish();
    manageBusy=false;
    if(serial===manageSerial)renderManage();
   }
  }
  function mutateCreate(){if(!validName(createInput.value,tab==='groups'?40:20))return;const body={name:N.normalize(createInput.value)};if(tab==='tags'&&newTagColor)body.color=newTagColor;mutate('POST',tab,body);}
  function reorder(index,delta){const ids=manageCatalog.groups.map(g=>g.id);[ids[index],ids[index+delta]]=[ids[index+delta],ids[index]];mutate('PUT','groups/order',{ids});}
  manage.node.addEventListener('close',()=>{if(manage.node.open)return;manageSerial++;manageCatalog=null;newTagColor=null;openColorKey=null;pendingColors.clear();colorErrors.clear();});
  return {openAssignment,openManage,close(){assignment.shell.close();manage.shell.close();}};
 }
 root.OrganizationDialogs={create,TAG_COLORS,TAG_COLOR_LABELS};
})(globalThis);
