/* Presentation and native-dialog lifecycle only; callers own every mutation. */
(function(root){
 'use strict';
 const stack=[];let saved=null;
 const visible=n=>!!(n?.isConnected&&!n.disabled&&n.getClientRects?.().length&&!n.closest?.('[hidden],.hidden'));
 function attach(dialog,{size='medium',title='',subtitle='',trigger=null,onclose=()=>{}}={}){
  if(dialog._shell)return dialog._shell;
  const doc=dialog.ownerDocument||root.document;
  dialog.classList.add('dialog-shell','dialog-'+size);
  let origin=null,opened=false,pressed=false;
  function end(){if(!opened||dialog.open)return;opened=false;const i=stack.indexOf(dialog);if(i>=0)stack.splice(i,1);
   if(!stack.length&&saved){doc.body.style.overflow=saved.overflow;root.scrollTo?.(saved.x,saved.y);saved=null;}
   onclose();
   const parent=stack.at(-1),fallback=parent?.querySelector('button:not(:disabled),input:not(:disabled),select:not(:disabled)')||doc.querySelector('.agent-card .detail-toggle,#add-agent');
   (visible(origin)?origin:visible(fallback)?fallback:null)?.focus({preventScroll:true});
  }
  const nativeOpen=dialog.showModal.bind(dialog),nativeClose=dialog.close.bind(dialog);
  function open(from=trigger||doc.activeElement){if(dialog.open)return;origin=from;opened=true;
   if(!stack.length)saved={overflow:doc.body.style.overflow,x:root.scrollX||0,y:root.scrollY||0};
   stack.push(dialog);doc.body.style.overflow='hidden';nativeOpen();
  }
  function close(){if(dialog.open)nativeClose();end();}
  const outside=e=>{const r=dialog.getBoundingClientRect();return e.target===dialog&&(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom);};
  dialog.addEventListener('pointerdown',e=>{pressed=e.button===0&&e.isPrimary!==false&&outside(e);});
  dialog.addEventListener('pointercancel',()=>{pressed=false;});
  dialog.addEventListener('click',e=>{const dismiss=pressed&&outside(e);pressed=false;if(dismiss)close();});
  dialog.addEventListener('cancel',e=>{e.preventDefault();close();});dialog.addEventListener('close',end);
  // Existing controllers may continue using showModal/close; no request logic moves here.
  dialog.showModal=()=>open();dialog.close=close;
  function heading(text=title,device=subtitle){let head=dialog.querySelector(':scope > .shell-head');if(!head){head=doc.createElement('div');head.className='shell-head';const group=doc.createElement('div'),h=doc.createElement('h2'),p=doc.createElement('p'),x=doc.createElement('button');h.id=dialog.id+'-shell-title';dialog.setAttribute('aria-labelledby',h.id);group.append(h,p);x.type='button';x.textContent='×';x.setAttribute('aria-label','关闭');x.addEventListener('click',close);head.append(group,x);dialog.prepend(head);}head.querySelector('h2').textContent=text;head.querySelector('p').textContent=device;}
  if(title)heading();
  return dialog._shell={open,close,heading,setOrigin:n=>{origin=n;}};
 }
 function confirm({title,device='',message,confirmText='确认',danger=false,trigger=null}={}){
  const doc=root.document,d=doc.createElement('dialog');d.id='action-confirm';doc.body.append(d);
  const shell=attach(d,{size:'small',title,subtitle:device});
  const body=doc.createElement('div');body.className='shell-content';const p=doc.createElement('p');p.textContent=message;body.append(p);
  const actions=doc.createElement('div');actions.className='shell-actions';const cancel=doc.createElement('button'),yes=doc.createElement('button');cancel.type=yes.type='button';cancel.textContent='取消';yes.textContent=confirmText;yes.className=danger?'danger-action':'primary-action';actions.append(cancel,yes);d.append(body,actions);
  return new Promise(resolve=>{let accepted=false;yes.addEventListener('click',()=>{accepted=true;shell.close();});cancel.addEventListener('click',shell.close);d.addEventListener('close',()=>{resolve(accepted);d.remove();},{once:true});shell.open(trigger);cancel.focus({preventScroll:true});});
 }
 const api={attach,confirm};if(typeof module==='object'&&module.exports)module.exports=api;else root.DialogShell=api;
})(globalThis);
