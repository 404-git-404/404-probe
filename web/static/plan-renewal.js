/* Date-only renewal UI. Server owns calendar math, revisions and receipts. */
(function(root){
 'use strict';
 const hex=v=>typeof v==='string'&&/^[a-f0-9]{32}$/.test(v);
 const date=v=>typeof v==='string'&&/^\d{4}-\d{2}-\d{2}$/.test(v);
 const fields=['renewal_date','expiry_date'];
 const labels={renewal_date:'下次续费',expiry_date:'到期日期'};
 const reasons={plan_missing:'尚未设置套餐。',date_missing:'未设置选中的日期。',period_missing:'未设置价格续费周期。',one_time:'一次性套餐没有月付或年付续费周期。'};
 const configFields=['traffic_mode','quota_value','quota_unit','cycle_kind','cycle_count','cycle_anchor','timezone','bandwidth_value','bandwidth_unit','currency','purchase_price','purchase_price_period','renewal_price','renewal_price_period','purchase_date','renewal_date','expiry_date','country_code_override'];
 const facts=p=>p?JSON.stringify(configFields.map(k=>p[k]??'')):'null';
 function requestID(crypto=root.crypto){
  if(!crypto?.getRandomValues)throw Error('安全随机源不可用，未提交。');
  return Array.from(crypto.getRandomValues(new Uint8Array(16)),v=>v.toString(16).padStart(2,'0')).join('');
 }
 function validPreview(p){
  if(!p||typeof p.eligible!=='boolean')throw Error('预览响应无法验证。');
  if(!p.eligible){if(!Object.hasOwn(reasons,p.reason)||p.edit_plan!==true)throw Error('预览响应无法验证。');return p;}
  if(!fields.includes(p.field)||!date(p.from_date)||!date(p.to_date)||!hex(p.expected_revision)||!['monthly','yearly'].includes(p.period)||typeof p.timezone!=='string'||!p.timezone||!['purchase_date','current_date'].includes(p.anchor_source)||typeof p.new_overdue!=='boolean'||typeof p.due_today!=='boolean'||p.new_overdue&&p.due_today||p.unchanged_field!==fields.find(f=>f!==p.field)||p.unchanged_date!=null&&p.unchanged_date!==''&&!date(p.unchanged_date))throw Error('预览响应无法验证。');
  return p;
 }
 function validResult(r,id,kind,q){
  const op=r?.operation;
  if(!r||typeof r.replayed!=='boolean'||!hex(r.current_revision)||!op||op.request_id!==q.request_id||op.kind!==kind||!fields.includes(op.field)||!date(op.from_date)||!date(op.to_date)||!Number.isSafeInteger(op.applied_at)||!r.undo||typeof r.undo.available!=='boolean'||r.undo.available&&!hex(r.undo.operation_id)||!Object.hasOwn(r,'current_plan')||r.current_plan!==null&&(typeof r.current_plan!=='object'||Array.isArray(r.current_plan)||r.current_plan.agent_id!==id))throw Error('操作响应无法验证；结果未确认。');
  if(kind==='apply'&&(op.field!==q.field||op.from_date!==q.from_date||op.to_date!==q.to_date||!Number.isSafeInteger(op.undo_until)||op.undo_until<=op.applied_at))throw Error('操作响应无法验证；结果未确认。');
  if(!r.replayed&&(!r.current_plan||r.current_plan[op.field]!==op.to_date))throw Error('操作响应无法验证；结果未确认。');
  return r;
 }
 function create({document:doc=root.document,window:win=root,agents,canWrite,csrf,begin,finish,edit,opening=()=>{},changed=()=>{},write=(...args)=>root.Management.write(...args),now=()=>Date.now(),crypto=root.crypto,position=root.PlanEditor.position}={}){
  const get=id=>doc.querySelector('#renewal-'+id),dialog=get('dialog'),field=get('field'),confirm=get('confirm');
  const records=new Map();let id=null,origin=null,scroll=null,generation=0,preview=null,previewFacts='',previewController=null,loading=false,undoAsk=false,dead=false;
  const record=key=>{if(!records.has(key))records.set(key,{unknown:null,latest:null,revision:'',facts:'',error:'',message:'',owner:null,controller:null,serverUndo:null});return records.get(key);};
  const live=(key,serial)=>!dead&&id===key&&generation===serial&&dialog.open&&agents.has(key);
  function undoAvailable(r){return !!(r.latest&&r.serverUndo?.available&&r.serverUndo.operation_id===r.latest.request_id&&r.revision&&facts(agents.get(id)?.plan)===r.facts&&now()>=r.latest.applied_at&&now()<r.latest.undo_until);}
  function place(){if(!dialog.open)return;const card=doc.querySelector(`.agent-card[data-agent-id="${id}"]`),p=position(card?.getBoundingClientRect(),dialog.getBoundingClientRect().height,{width:win.innerWidth||1366,height:win.innerHeight||900});Object.assign(dialog.style,{left:p.left+'px',top:p.top+'px',width:p.width+'px'});}
  function draw(){if(!dialog.open||!id||dead)return;const r=record(id),busy=!!r.owner;
   get('name').textContent=agents.get(id)?.name||agents.get(id)?.state?.hostname||'未命名设备';
   get('error').textContent=r.error;get('error').hidden=!r.error;
   get('result').textContent=r.message;get('result').hidden=!r.message;
   get('recovery').hidden=!r.unknown;get('request-id').textContent=r.unknown?.dto.request_id||'';
   get('verify').disabled=busy||!canWrite(id);get('copy').disabled=!r.unknown;
   get('undo').hidden=!r.latest;get('undo').disabled=busy||loading||!canWrite(id)||!!r.unknown||!undoAvailable(r);
   get('edit').hidden=!!preview?.eligible||undoAsk;get('edit').disabled=busy||!!r.unknown||!canWrite(id);
   get('refresh').disabled=busy||loading||!!r.unknown||!canWrite(id);
   field.disabled=busy||loading||!!r.unknown||undoAsk;
   get('loading').textContent=loading?'正在读取服务端预览…':!preview&&!r.unknown&&!undoAsk?'请重新预览后确认。':'';
   get('dates').hidden=undoAsk?!r.latest:!preview?.eligible;
   if(undoAsk&&r.latest){const other=fields.find(f=>f!==r.latest.field);field.value=r.latest.field;get('from').textContent=r.latest.to_date;get('to').textContent=r.latest.from_date;
    get('detail').textContent=`撤销本次登记 · ${labels[r.latest.field]}恢复原日期 · 时区 ${agents.get(id)?.plan?.timezone||'UTC'}。${labels[other]} ${agents.get(id)?.plan?.[other]||'未设置'} 保持不变。`;
    get('warning').textContent='';get('anchor').textContent='';
   }else if(preview?.eligible){field.value=preview.field;get('from').textContent=preview.from_date;get('to').textContent=preview.to_date;
    get('detail').textContent=`${labels[preview.field]} · ${preview.period==='monthly'?'每月顺延一期':'每年顺延一期'} · 时区 ${preview.timezone}。${labels[preview.unchanged_field]} ${preview.unchanged_date||'未设置'} 保持不变。`;
    get('warning').textContent=preview.new_overdue?'新日期仍已过期；确认只从原日期顺延一期，不自动补齐。':preview.due_today?'新日期今天到期；仍从原日期顺延一期。':'';
    get('anchor').textContent=preview.anchor_source==='current_date'?'以原当前日期为锚点；没有可兼容的购买日，不猜测未知历史月末。':'沿原购买日锚定，月末或闰日按服务端夹取。';
   }else{get('detail').textContent=preview?(reasons[preview.reason]+' 请编辑套餐后再登记。'):'';get('warning').textContent='';get('anchor').textContent='';}
   confirm.textContent=undoAsk?'确认撤销此次登记':'确认登记已续费';
   confirm.disabled=busy||loading||!!r.unknown||!canWrite(id)||(undoAsk?!undoAvailable(r):!preview?.eligible);
   get('undo-note').hidden=!undoAsk;place();
  }
  function end(){if(id===null)return;const old=id,trigger=origin;generation++;previewController?.abort();previewController=null;records.get(old)?.controller?.abort();id=null;origin=null;preview=null;loading=false;undoAsk=false;
   if(agents.has(old)){const target=trigger?.isConnected?trigger:doc.querySelector(`.agent-card[data-agent-id="${old}"] [data-focus-key="renewal"]`);target?.focus({preventScroll:true});}
   if(scroll)win.scrollTo?.(...scroll);scroll=null;if(!dead)changed();
  }
  function close(){if(dialog.open)dialog.close();end();}
  async function load(selected=''){if(!id||dead)return;const key=id,r=record(key);if(r.unknown||r.owner||!canWrite(key))return;
   previewController?.abort();const controller=new AbortController();previewController=controller;const serial=++generation;loading=true;preview=null;undoAsk=false;draw();
   try{const value=validPreview(await write(`/api/v1/web/agents/${encodeURIComponent(key)}/plan/renewal/preview`,{csrf:csrf(),payload:selected?{field:selected}:{},signal:controller.signal,unauthorized:()=>root.location.assign('/login')}));
    if(!live(key,serial))return;
    preview=value;previewFacts=facts(agents.get(key)?.plan);
    if(r.latest&&value.expected_revision!==r.revision){r.latest=null;r.serverUndo=null;r.message='套餐已被其他编辑改变，原登记不再可撤销。';}
   }catch(error){if(live(key,serial)&&error.name!=='AbortError')r.error=error.message||'预览失败，请显式重新预览。';}
   finally{if(live(key,serial)){loading=false;previewController=null;draw();}}
  }
  function open(key,trigger=null){if(dead||!agents.has(key))return;if(dialog.open)close();opening(key);id=key;origin=trigger;scroll=[win.scrollX||0,win.scrollY||0];generation++;preview=null;undoAsk=false;loading=false;dialog.showModal();draw();get('cancel').focus({preventScroll:true});if(!record(key).unknown)load();}
  async function submit(kind,dto){const key=id;if(!key||dead||!canWrite(key))return;const r=record(key);if(r.owner)return;
   const mutation=begin(key);if(!mutation)return;
   const owner={},controller=new AbortController(),serial=generation;r.owner=owner;r.controller=controller;
   const pending=r.unknown||Object.freeze({kind,dto:Object.freeze({...dto})});r.unknown=pending;r.error='结果未确认：请保留原请求 ID；关闭不代表服务器未写入。';draw();changed();
   let completed=false;
   try{const result=validResult(await write(`/api/v1/web/agents/${encodeURIComponent(key)}/plan/renewal/${pending.kind}`,{csrf:csrf(),payload:pending.dto,signal:controller.signal,unauthorized:()=>root.location.assign('/login')}),key,pending.kind,pending.dto);
    if(dead||records.get(key)!==r||!agents.has(key))return;
    r.facts=facts(result.current_plan);r.revision=result.current_revision;r.serverUndo=result.undo;
    if(pending.kind==='apply'&&result.undo.available&&result.undo.operation_id===result.operation.request_id)r.latest={...result.operation};
    else if(pending.kind==='undo'||result.undo.operation_id!==r.latest?.request_id)r.latest=null;
    completed=finish(mutation,{plan:result.current_plan});
    if(!completed)return;
    r.unknown=null;r.error='';r.message=`${pending.kind==='undo'?'撤销已确认':'登记已确认'}：${labels[result.operation.field]} ${result.operation.from_date} → ${result.operation.to_date}。当前套餐以服务器返回资料为准。`;
    if(live(key,serial)){preview=null;undoAsk=false;}
   }catch(error){if(dead||records.get(key)!==r||!agents.has(key))return;
    const certain=[400,403,404,409,413,415,429].includes(error.status);
    r.error=(error.message||'请求失败')+(certain?(error.status===409?' 请重新预览并明确确认。':error.status===429?' 请稍后再试，不自动重试。':''):' 结果未确认；仅可用原请求 ID 和同一确认内容核实。');
    if(certain){r.unknown=null;if([403,404,409].includes(error.status)){r.latest=null;r.serverUndo=null;}if(live(key,serial)){preview=null;undoAsk=false;}}
   }finally{
    if(!completed)finish(mutation);
    if(r.owner===owner){r.owner=null;r.controller=null;}
    if(!dead&&records.get(key)===r){changed();if(live(key,serial))draw();}
   }
  }
  function confirmAction(){if(!id||confirm.disabled)return;const r=record(id);let dto,kind;
   try{if(undoAsk){if(!undoAvailable(r)){r.message='撤销已失效或超时，请核对当前套餐。';draw();return;}kind='undo';dto={request_id:requestID(crypto),operation_id:r.latest.request_id,expected_revision:r.revision};}
    else{if(!preview?.eligible)return;kind='apply';dto={request_id:requestID(crypto),expected_revision:preview.expected_revision,field:preview.field,from_date:preview.from_date,to_date:preview.to_date,new_overdue:preview.new_overdue,due_today:preview.due_today};}
   }catch(error){r.error=error.message;draw();return;}
   return submit(kind,dto);
  }
  field.addEventListener('change',()=>load(field.value));confirm.addEventListener('click',confirmAction);
  get('refresh').addEventListener('click',()=>load(field.value));
  // Undo is the inverse immutable receipt, not another forward calendar preview.
  // A same-value external PUT is fenced by the server's undo revision CAS.
  get('undo').addEventListener('click',()=>{const r=id&&record(id);if(r&&undoAvailable(r)&&!r.unknown&&!r.owner&&canWrite(id)){previewController?.abort();previewController=null;generation++;loading=false;preview=null;undoAsk=true;draw();}else if(r){r.message='撤销窗口已结束或资格已改变；以服务器最终裁定为准。';draw();}});
  get('verify').addEventListener('click',()=>{const p=id&&record(id).unknown;if(p)return submit(p.kind,p.dto);});
  get('copy').addEventListener('click',async()=>{const key=id,serial=generation,p=key&&record(key).unknown;if(!p)return;try{await win.navigator.clipboard.writeText(p.dto.request_id);}catch(_){if(live(key,serial)){record(key).error='复制失败，请手动复制下方请求 ID。';draw();}}});
  get('edit').addEventListener('click',()=>{const key=id,trigger=origin;if(!key||record(key).unknown||record(key).owner)return;close();edit(key,trigger);});
  for(const name of ['cancel','dismiss'])get(name).addEventListener('click',close);
  // Native close events are queued: an earlier close must not end a reopened modal.
  dialog.addEventListener('cancel',e=>{e.preventDefault();close();});dialog.addEventListener('close',()=>{if(!dialog.open)end();});win.addEventListener('resize',place);
  return {open,close,load,confirm:confirmAction,verify(){const p=id&&record(id).unknown;return p?submit(p.kind,p.dto):undefined;},unconfirmed:key=>!!records.get(key)?.unknown,
   invalidate(key){const r=records.get(key);if(r){r.latest=null;r.serverUndo=null;r.revision='';r.message='套餐已编辑，原登记不再可撤销。';}if(id===key){preview=null;undoAsk=false;draw();}},
   sync(){for(const key of records.keys())if(!agents.has(key)){records.get(key).controller?.abort();records.delete(key);if(id===key)close();}
    if(id&&agents.has(id)){const r=record(id);if(r.latest&&facts(agents.get(id)?.plan)!==r.facts&&!r.owner)this.invalidate(id);if(preview&&previewFacts!==facts(agents.get(id)?.plan)&&!r.owner){preview=null;undoAsk=false;r.message='套餐资料已改变，请重新预览。';}if(agents.get(id)?.revoked){preview=null;r.latest=null;r.serverUndo=null;}draw();}},
   shutdown(){dead=true;close();for(const r of records.values())r.controller?.abort();records.clear();win.removeEventListener?.('resize',place);},
   snapshot:()=>({id,open:dialog.open,generation,loading,undoAsk,preview:preview?{...preview}:null,records:Array.from(records,([key,r])=>({id:key,unknown:r.unknown?{kind:r.unknown.kind,dto:{...r.unknown.dto}}:null,latest:r.latest?{...r.latest}:null,error:r.error,message:r.message,busy:!!r.owner}))})};
 }
 const api={create,requestID,validPreview,validResult,facts};if(typeof module==='object'&&module.exports)module.exports=api;else root.PlanRenewal=api;
})(globalThis);
