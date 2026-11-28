/* Synchronous head script: resolve and persist the three-state theme before paint. */
(function(root){
  'use strict';
  const key='404-probe.theme.v1';
  const media=root.matchMedia('(prefers-color-scheme: dark)');
  const order=['light','dark','system'];
  const normalize=value=>value==='light'||value==='dark'||value==='system'?value:'system';
  const resolve=value=>value==='light'||value==='dark'?value:(media.matches?'dark':'light');
  let preference;
  try{preference=normalize(root.localStorage.getItem(key));}catch{preference='system';}
  function apply(){
    const theme=resolve(preference);
    root.document.documentElement.dataset.theme=theme;
    const meta=root.document.querySelector('meta[name="color-scheme"]');if(meta)meta.content=theme;
    root.document.documentElement.style.colorScheme=theme;
    root.dispatchEvent(new CustomEvent('404-probe:themechange',{detail:{theme,preference}}));
  }
  function persist(){try{root.localStorage.setItem(key,preference);}catch{/* Storage may be disabled. */}}
  function set(value){preference=normalize(value);persist();apply();}
  function cycle(){set(order[(order.indexOf(preference)+1)%order.length]);}
  media.addEventListener('change',()=>{if(preference==='system')apply();});
  root.ProbeTheme=Object.freeze({get:()=>resolve(preference),preference:()=>preference,set,cycle});
  root.addEventListener('storage',event=>{if(event.key===key||event.key===null){preference=normalize(event.newValue);apply();}});
  apply();
})(window);
