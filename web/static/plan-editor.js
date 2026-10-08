/* Native plan dialog presentation only. No request, retry or payload translation. */
(function(root){
 'use strict';
 function position(rect,height,viewport){
  const margin=16,w=Math.min(Math.max(rect?.width||560,360),620,viewport.width-2*margin),h=Math.min(height,viewport.height-2*margin);
  if(viewport.width<=620)return{left:0,top:0,width:viewport.width};
  const x=rect?rect.left:margin,y=rect?(rect.top+h<=viewport.height-margin?rect.top:rect.bottom-h):margin;
  return{left:Math.max(margin,Math.min(x,viewport.width-w-margin)),top:Math.max(margin,Math.min(y,viewport.height-h-margin)),width:w};
 }
 function create({document:doc=root.document,window:win=root,agents,read,onclose=()=>{},onclear=()=>{}}){
  const get=id=>doc.querySelector('#'+id),dialog=get('plan-dialog'),save=get('plan-save'),clear=get('plan-clear');
  const confirmation=get('plan-confirmation'),note=get('plan-confirm-note'),resume=get('plan-continue'),discard=get('plan-discard');
  let id=null,origin=null,baseline='',generation=0,busy=false,action=null,scroll=null,dead=false;
  const dirty=()=>JSON.stringify(read())!==baseline;
  const buttons=()=>{save.disabled=clear.disabled=busy;};
  function hide(){confirmation.hidden=true;action=null;}
  function place(){if(!dialog.open)return;const card=doc.querySelector(`.agent-card[data-agent-id="${id}"]`),rect=card?.getBoundingClientRect();
   const p=position(rect,dialog.getBoundingClientRect().height,{width:win.innerWidth||1366,height:win.innerHeight||900});
   dialog.style.left=p.left+'px';dialog.style.top=p.top+'px';dialog.style.width=p.width+'px';
  }
  function ended(){if(id===null)return;const last=id,trigger=origin;id=null;origin=null;generation++;busy=false;hide();buttons();onclose();
   if(agents.has(last)){const n=trigger?.isConnected?trigger:doc.querySelector(`.agent-card[data-agent-id="${last}"] [data-focus-key="plan"]`);n?.focus({preventScroll:true});}
   if(scroll)win.scrollTo?.(...scroll);scroll=null;
  }
  function close(){if(dialog.open)dialog.close();ended();}
  function ask(kind,next=null){action={kind,next,focus:doc.activeElement};confirmation.hidden=false;
   note.textContent=kind==='clear'?'清除这台设备的套餐与计费周期资料？永久流量累计不会删除。':busy?'提交可能仍在服务器执行。关闭不代表未写入；再次打开后核对实际资料。':'有未保存的修改。放弃后不会自动保存。';
   discard.textContent=kind==='clear'?'确认清除套餐资料':'放弃修改';resume.focus({preventScroll:true});
  }
  function requestClose(){if(!dialog.open)return;if(dirty()||busy)ask('close');else close();}
  function open(nextId,trigger,initialize){if(dead||!agents.has(nextId))return;
   if(dialog.open){if(dirty()||busy){ask('switch',()=>open(nextId,trigger,initialize));return;}close();}
   id=nextId;origin=trigger;generation++;busy=false;scroll=[win.scrollX||0,win.scrollY||0];initialize();baseline=JSON.stringify(read());hide();buttons();dialog.showModal();place();get('plan-cancel').focus({preventScroll:true});
  }
  resume.addEventListener('click',()=>{const focus=action?.focus;hide();focus?.focus({preventScroll:true});});
  discard.addEventListener('click',()=>{const decision=action;if(!decision)return;hide();if(decision.kind==='clear'){if(!busy)return onclear();}else{close();decision.next?.();}});
  get('plan-cancel').addEventListener('click',requestClose);get('plan-dismiss').addEventListener('click',requestClose);
  clear.addEventListener('click',()=>{if(!busy&&dialog.open)ask('clear');});
  const cancel=e=>{e.preventDefault();if(action){const focus=action.focus;hide();focus?.focus({preventScroll:true});}else requestClose();};
  dialog.addEventListener('cancel',cancel);dialog.addEventListener('close',ended);win.addEventListener('resize',place);
  return{open,requestClose,close,dirty,place,
   handoff(next){if(dead)return false;if(dialog.open&&(dirty()||busy)){const owner=generation,old=id;ask('switch',()=>{if(!dead&&generation===owner+1&&agents.has(old))next();});return false;}close();if(!dead)next();return true;},
   canSubmit:()=>!dead&&dialog.open&&!busy&&agents.has(id),
   begin(){if(!this.canSubmit())return null;busy=true;hide();buttons();return generation;},
   finish(token){if(token===generation){busy=false;buttons();}},
   sync(){if(id!==null&&!agents.has(id))close();},
   shutdown(){dead=true;close();win.removeEventListener?.('resize',place);dialog.removeEventListener('cancel',cancel);dialog.removeEventListener('close',ended);},
   snapshot:()=>({id,open:dialog.open,busy,generation,dirty:dialog.open&&dirty(),confirmation:action?.kind||null})};
 }
 const api={create,position};if(typeof module==='object'&&module.exports)module.exports=api;else root.PlanEditor=api;
})(typeof globalThis!=='undefined'?globalThis:this);
