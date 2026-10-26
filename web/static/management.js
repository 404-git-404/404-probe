/* P2a field writes: reuse P1's single fetch + body deadline. */
(function (root) {
  'use strict';
  function validName(value) {
    const name = value.trim();
    const bytes = new TextEncoder().encode(name).length;
    return Boolean(name) && bytes <= 100 && !/[\r\n\0]/.test(name);
  }
  function write(path, {method = 'POST', payload, csrf, signal, fetcher = root.fetch,
    timeoutMs = 10000, unauthorized = () => {}}) {
    return root.AgentState.fetchJSON(path, {signal, timeoutMs, unauthorized,
      fetcher: async (url, options) => {
        const response = await fetcher(url, {...options, method,
          headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrf},
          body: JSON.stringify(payload)});
        if (response.status === 401) return response;
        if (!response.ok) {
          let body;
          try { body = await response.json(); } catch (_) { /* status is authoritative */ }
          throw Object.assign(new Error(body?.error?.message || response.statusText || '请求失败'),
            {status: response.status});
        }
        return response.status === 204 ? {ok: true, status: 200, json: async () => null} : response;
      }});
  }
  // Same ISO subset as internal/protocol/country.go; no pseudo-region flags.
  const codes = 'AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW'.split(' ');
  function countries(intl = root.Intl) {
    let zh, en;
    try { zh = new intl.DisplayNames(['zh'], {type: 'region'}); en = new intl.DisplayNames(['en'], {type: 'region'}); } catch (_) { /* codes remain usable */ }
    return codes.map(code => ({code, zh: zh?.of(code) || code, en: en?.of(code) || code}));
  }
  function matches(query, current = '', catalog = countries()) {
    const needle = query.trim().toLocaleLowerCase();
    const result = catalog.filter(item => [item.code, item.zh, item.en].some(value => value.toLocaleLowerCase().includes(needle)));
    if (/^[A-Z]{2}$/.test(current) && !result.some(item => item.code === current)) {
      result.unshift(catalog.find(item => item.code === current) || {code: current, zh: current, en: current});
    }
    return result;
  }
  function countryPicker({search, select, clear, preview, status, onchange}) {
    const catalog = countries();
    let selected = '';
    function render() {
      select.replaceChildren();
      const automatic = root.document.createElement('option');
      automatic.value = ''; automatic.textContent = '使用 Agent 识别值'; select.append(automatic);
      const found = matches(search.value, selected, catalog);
      for (const item of found) {
        const option = root.document.createElement('option'); option.value = item.code;
        option.textContent = `${item.zh} · ${item.en} (${item.code})`; select.append(option);
      }
      select.value = selected;
      preview.replaceChildren();
      if (selected) {
        const flag = root.document.createElement('img'); flag.src = `/vendor/flag-icons/4x3/${selected.toLowerCase()}.svg`;
        flag.alt = selected; flag.width = 24; flag.height = 18;
        flag.addEventListener('error', () => { flag.hidden = true; }); preview.append(flag);
      }
      const needle = search.value.trim().toLocaleLowerCase();
      const count = catalog.filter(item => [item.code, item.zh, item.en].some(value => value.toLocaleLowerCase().includes(needle))).length;
      status.textContent = `${count ? `${count} 个匹配地区` : '无匹配地区；当前选择仍保留'}。${selected ? `手动覆盖 ${selected}` : '使用 Agent 识别值'}；保存套餐后生效。`;
    }
    search.addEventListener('input', render);
    select.addEventListener('change', () => { selected = select.value; render(); onchange(); });
    clear.addEventListener('click', () => { selected = ''; search.value = ''; render(); onchange(); });
    return {set(code) { selected = /^[A-Z]{2}$/.test(code || '') ? code : ''; search.value = ''; render(); }};
  }
  function planPayload(plan={},patch={}) {
    const keys=['traffic_mode','quota_value','quota_unit','cycle_kind','cycle_count','cycle_anchor','timezone','bandwidth_value','bandwidth_unit','currency','purchase_price','purchase_price_period','renewal_price','renewal_price_period','purchase_date','renewal_date','expiry_date','country_code_override'];
    return {...Object.fromEntries(keys.map(k=>[k,plan[k]??(k==='cycle_count'?0:'')])),...patch};
  }
  const api = {validName, write, countries, matches, countryPicker, planPayload};
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.Management = api;
})(globalThis);

/* Presentation and native-dialog lifecycle only; callers own every mutation. */
(function(root){
 'use strict';
 const stack=[];let saved=null;
 const visible=n=>!!(n?.isConnected&&!n.disabled&&n.getClientRects?.().length&&!n.closest?.('[hidden],.hidden'));
 function attach(dialog,{size='medium',title='',subtitle='',trigger=null,onclose=()=>{},decorate=true}={}){
  if(dialog._shell)return dialog._shell;
  const doc=dialog.ownerDocument||root.document;
  if(decorate)dialog.classList.add('dialog-shell','dialog-'+size);
  let origin=null,opened=false,pressed=false;
  function end(){if(!opened||dialog.open)return;opened=false;const i=stack.indexOf(dialog);if(i>=0)stack.splice(i,1);
   if(!stack.length&&saved){doc.body.style.overflow=saved.overflow;root.scrollTo?.(saved.x,saved.y);saved=null;}
   onclose();
   const parent=stack.at(-1),fallback=parent?.querySelector('button:not(:disabled),input:not(:disabled),select:not(:disabled)')||doc.querySelector('.agent-card .detail-toggle,#add-agent');
   (visible(origin)?origin:visible(fallback)?fallback:null)?.focus({preventScroll:true});
  }
  const nativeOpen=dialog.showModal.bind(dialog),nativeClose=dialog.close.bind(dialog);
  function open(from=trigger||doc.activeElement){if(dialog.open)return;origin=from||doc.activeElement;opened=true;
   if(!stack.length)saved={overflow:doc.body.style.overflow,x:root.scrollX||0,y:root.scrollY||0};
   stack.push(dialog);doc.body.style.overflow='hidden';nativeOpen();
  }
  function close(){if(dialog.open)nativeClose();end();}
  const outside=e=>{const r=dialog.getBoundingClientRect();return e.target===dialog&&(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom);};
  dialog.addEventListener('pointerdown',e=>{pressed=e.button===0&&e.isPrimary!==false&&outside(e);});
  dialog.addEventListener('pointercancel',()=>{pressed=false;});
  dialog.addEventListener('click',e=>{const dismiss=pressed&&outside(e);pressed=false;if(dismiss)close();});
  dialog.addEventListener('keydown',e=>{if(e.key!=='Tab')return;const nodes=[...dialog.querySelectorAll('button,input,select,textarea,a[href],[tabindex]')].filter(n=>visible(n)&&n.tabIndex!==-1),first=nodes[0],last=nodes.at(-1);if(!first){e.preventDefault();dialog.focus();}else if(e.shiftKey&&doc.activeElement===first){e.preventDefault();last.focus();}else if(!e.shiftKey&&doc.activeElement===last){e.preventDefault();first.focus();}});
  dialog.addEventListener('cancel',e=>{e.preventDefault();close();});dialog.addEventListener('close',end);
  // Existing controllers may continue using showModal/close; no request logic moves here.
  dialog.showModal=()=>open();dialog.close=close;
  function heading(text=title,device=subtitle){let head=dialog.querySelector(':scope > .shell-head');if(!head){head=doc.createElement('div');head.className='shell-head';const group=doc.createElement('div'),h=doc.createElement('h2'),p=doc.createElement('p'),x=doc.createElement('button');h.id=dialog.id+'-shell-title';dialog.setAttribute('aria-labelledby',h.id);group.append(h,p);x.type='button';x.textContent='×';x.setAttribute('aria-label','关闭');x.addEventListener('click',close);head.append(group,x);dialog.prepend(head);}head.querySelector('h2').textContent=text;head.querySelector('p').textContent=device;head.querySelector('p').title=device;}
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
 const api={attach,confirm};if(typeof module==='object'&&module.exports)module.exports.DialogShell=api;else root.DialogShell=api;
})(globalThis);
