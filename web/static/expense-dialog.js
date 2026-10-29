(function(root){
 'use strict';
 const exclusion={once:'一次性买断',missing_price:'未设置价格',invalid_price:'价格无法识别',missing_currency:'未设置币种',invalid_currency:'币种无法识别',missing_period:'未设置付款周期',invalid_period:'付款周期无法识别',missing_rate:'缺汇率'};
 const number=value=>{const [integer,fraction]=String(value??'—').split('.');return integer.replace(/\B(?=(\d{3})+(?!\d))/g,',')+(fraction!==undefined?'.'+fraction:'');};
 const decimal=value=>{const m=/^([0-9]+)(?:\.([0-9]+))?$/.exec(String(value??''));if(!m)return '—';const f=(m[2]||'').padEnd(3,'0'),c=BigInt(m[1])*100n+BigInt(f.slice(0,2))+(f[2]>='5'?1n:0n);return (c/100n)+'.'+String(c%100n).padStart(2,'0');};
 const symbol={USD:'$',CNY:'¥',EUR:'€',HKD:'HK$',JPY:'¥',GBP:'£'};
 function create({api,visibleIDs,refresh,document:doc=root.document}){
  const el=(tag,cls,text)=>{const n=doc.createElement(tag);if(cls)n.className=cls;if(text!=null)n.textContent=text;return n;};
  const button=(text,fn,cls='')=>{const n=el('button',cls,text);n.type='button';n.addEventListener('click',fn);return n;};
  const dialog=el('dialog','dashboard-dialog expense-dialog');dialog.id='expense-dialog';doc.body.append(dialog);
  const shell=root.DialogShell.attach(dialog,{title:'支出估算',subtitle:'按套餐价格折算成 USD，只是估算'}),body=el('div','shell-content'),error=el('p','dashboard-dialog-error');error.setAttribute('role','alert');error.hidden=true;
  const scopes=el('div','expense-scopes'),all=button('全部设备',()=>setScope('all')),filtered=button('当前筛选',()=>setScope('filtered')),included=el('span','dashboard-dialog-note');all.dataset.expenseScope='all';filtered.dataset.expenseScope='filtered';scopes.append(all,filtered,included);
  const totals=el('div','expense-totals'),monthly=el('strong'),yearly=el('strong');monthly.id='expense-monthly';yearly.id='expense-yearly';for(const [label,value]of [['每月',monthly],['每年',yearly]]){const tile=el('section');tile.append(el('small','',label),value);totals.append(tile);}
  const table=el('table','expense-table'),deviceDetails=el('details','expense-devices'),deviceSummary=el('summary','','按设备查看 ▸'),deviceRows=el('div'),excluded=el('p','dashboard-dialog-info');deviceDetails.append(deviceSummary,deviceRows);excluded.id='expense-excluded';
  const rates=el('section','expense-rates'),ratesTitle=el('h3','','汇率（手动填写，1 USD = ）'),rateRows=el('div','expense-rate-grid'),addRow=el('div','expense-add-currency'),currency=el('input'),add=button('添加币种',addCurrency),rateNote=el('p','dashboard-dialog-note','只用于估算，不联网获取。缺汇率的币种不计入合计；美元固定为 1。');currency.id='expense-new-currency';currency.placeholder='例如 EUR';currency.setAttribute('aria-label','新增币种');currency.maxLength=3;addRow.append(currency,add);rates.append(ratesTitle,rateRows,addRow,rateNote);
  body.append(error,scopes,totals,table,el('p','dashboard-dialog-note','月付、季付、半年付、年付统一折成每月。'),deviceDetails,excluded,rates);dialog.append(body);const actions=el('div','shell-actions'),save=button('保存汇率',saveRates,'primary-action');actions.append(button('关闭',shell.close),save);dialog.append(actions);
  const updatedFormatter=new Intl.DateTimeFormat('zh-CN',{month:'2-digit',day:'2-digit'});
  let scope='all',catalog=null,estimate=null,serial=0,estimateSerial=0,busy=false;const inputs=new Map();
  function showError(text){error.textContent=text;error.hidden=!text;}
  function setScope(value){scope=value;loadEstimate();}
  async function loadEstimate(retry=true){const generation=++estimateSerial,owner=serial;all.setAttribute('aria-pressed',String(scope==='all'));filtered.setAttribute('aria-pressed',String(scope==='filtered'));monthly.textContent=yearly.textContent='正在读取…';
   try{const request=scope==='all'?{scope:'all'}:{scope:'filtered',agent_ids:visibleIDs()};const data=await api.request('POST','expenses/estimate',request);if(owner!==serial||generation!==estimateSerial||!dialog.open)return;estimate=data;renderEstimate();}
   catch(e){if(owner!==serial||generation!==estimateSerial)return;if(e.status===404&&retry){await refresh();return loadEstimate(false);}monthly.textContent=yearly.textContent='—';showError('支出资料读取失败，请关闭后重试。');}
  }
  function renderEstimate(){
   if(!estimate)return;included.textContent=`计入 ${estimate.included_count} / ${estimate.scope_count} 台`;monthly.textContent='$'+number(estimate.monthly_usd);yearly.textContent='$'+number(estimate.yearly_usd);
   const head=el('thead'),tr=el('tr');for(const text of ['币种','台数','原币 / 月','≈ USD / 月'])tr.append(el('th','',text));head.append(tr);const tbody=el('tbody');
   for(const c of estimate.currencies||[]){const row=el('tr');row.append(el('th','',c.currency),el('td','',c.device_count),el('td','',(symbol[c.currency]||c.currency+' ')+number(decimal(c.monthly_original))),el('td','',c.rate_missing?'缺汇率':'$'+number(decimal(c.monthly_usd))));tbody.append(row);}table.replaceChildren(head,tbody);
   deviceRows.replaceChildren(...(estimate.devices||[]).map(d=>{const row=el('div','expense-device-row');row.append(el('span','',d.name),el('span','',d.exclusion?exclusion[d.exclusion]||'未计入':d.monthly_usd==null?'缺汇率':'$'+number(decimal(d.monthly_usd))+' / 月'));if(d.estimated_from_purchase)row.append(el('small','dashboard-dialog-note','按首次价格估算'));return row;}));
   const notes=[`一次性买断 ${estimate.once_count||0} 台`,`未设置价格 ${estimate.missing_price_count||0} 台${estimate.missing_price_devices?.length?'（'+estimate.missing_price_devices.map(d=>d.name).join('、')+'）':''}`];
   if(estimate.missing_rates?.length)notes.push('缺汇率：'+estimate.missing_rates.map(c=>c.currency+' '+c.device_count+' 台').join('、'));
   const other=(estimate.excluded||[]).filter(d=>!['once','missing_price','missing_rate'].includes(d.reason||d.exclusion));if(other.length)notes.push(other.map(d=>(d.name?d.name+'：':'')+(exclusion[d.reason||d.exclusion]||'套餐资料不完整')).join('；'));
   excluded.textContent='未计入：'+notes.join(' · ');
  }
  function addRate(code,value='',saved=false){if(code==='USD'||inputs.has(code))return;const label=el('label','expense-rate'),text=el('span','',code),input=el('input'),remove=button('删除',()=>removeRate(code));input.type='text';input.inputMode='decimal';input.value=value;input.id='expense-rate-'+code;input.setAttribute('aria-label','1 USD 兑换 '+code);label.append(text,input,remove);rateRows.append(label);inputs.set(code,{input,label,saved});}
  function renderRates(){const updated=Math.max(0,...catalog.exchange_rates.map(r=>r.updated_at||0));rateNote.textContent='只用于估算，不联网获取。'+(updated?'上次修改 '+updatedFormatter.format(new Date(updated))+'。':'')+'缺汇率的币种不计入合计；美元固定为 1。';inputs.clear();rateRows.replaceChildren();const codes=new Set();for(const row of catalog.items||[])if(/^[A-Z]{3}$/.test(row.plan?.currency||''))codes.add(row.plan.currency);for(const rate of catalog.exchange_rates||[])codes.add(rate.currency);for(const code of [...codes].sort()){const saved=catalog.exchange_rates.find(r=>r.currency===code);addRate(code,saved?.units_per_usd||'',!!saved);}}
  function addCurrency(){const code=currency.value.trim().toUpperCase();if(!/^[A-Z]{3}$/.test(code)||code==='USD'){showError('请输入美元以外的三位币种代码，例如 EUR。');return;}addRate(code);currency.value='';inputs.get(code)?.input.focus();showError('');}
  currency.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();addCurrency();}});
  async function load(message=''){const owner=serial;showError('正在读取…');try{const data=await api.catalog();if(owner!==serial||!dialog.open)return;catalog=data;renderRates();await loadEstimate();if(owner===serial)showError(message);}catch{if(owner===serial)showError(message||'汇率资料读取失败，请关闭后重试。');}}
  async function open(trigger){scope='all';estimate=null;catalog=null;serial++;deviceDetails.open=false;monthly.textContent=yearly.textContent='正在读取…';shell.open(trigger);await load();}
  function setBusy(value){busy=value;save.disabled=value;for(const {input,label}of inputs.values()){input.disabled=value;label.querySelector('button').disabled=value;}add.disabled=value;currency.disabled=value;}
  async function saveRates(){if(busy||!catalog)return;const values=[];for(const [code,v]of inputs){const value=v.input.value.trim();if(!value&&!v.saved)continue;if(!/^(?:[1-9]\d{0,11}|0)(?:\.\d{1,12})?$/.test(value)||!/[1-9]/.test(value)){showError('汇率须大于 0，整数和小数各最多 12 位；请用普通小数填写。');v.input.focus();return;}values.push({currency:code,units_per_usd:value});}if(!values.length){showError('请先填写至少一种汇率。');return;}const owner=serial;setBusy(true);try{await api.request('PUT','exchange-rates',{expected_revision:catalog.revision,rates:values});await refresh();if(owner===serial)await load('汇率已保存。');}catch(e){if(owner===serial){if(e.status===409)await load(e.message);else showError(e.message);}}finally{setBusy(false);}}
  async function removeRate(code){if(busy||!catalog)return;const row=inputs.get(code);if(!row.saved){row.label.remove();inputs.delete(code);return;}const owner=serial;setBusy(true);try{await api.request('DELETE','exchange-rates/'+code,null,catalog.revision);await refresh();if(owner===serial)await load('汇率已删除；该币种现在不计入合计。');}catch(e){if(owner===serial){if(e.status===409)await load(e.message);else showError(e.message);}}finally{setBusy(false);}}
  dialog.addEventListener('close',()=>{serial++;estimateSerial++;catalog=null;});
  return {open,close:shell.close};
 }
 root.ExpenseDialog={create};
})(globalThis);
