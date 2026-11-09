/* Synchronous head script: resolve and persist the two-state theme before paint. */
(function(root){
  'use strict';
  const key='404-probe.theme.v1';
  // Missing/legacy preferences take one OS snapshot, then become explicit.
  const resolve=value=>value==='light'||value==='dark'?value:root.matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light';
  let preference;
  try{preference=resolve(root.localStorage.getItem(key));}catch{preference=resolve(null);}
  function apply(){
    const theme=preference;
    root.document.documentElement.dataset.theme=theme;
    const meta=root.document.querySelector('meta[name="color-scheme"]');if(meta)meta.content=theme;
    root.document.documentElement.style.colorScheme=theme;
    root.document.documentElement.style.backgroundColor=theme==='dark'?'#0E1012':'#F7F4EF';
    root.dispatchEvent(new CustomEvent('404-probe:themechange',{detail:{theme,preference}}));
  }
  function persist(){try{root.localStorage.setItem(key,preference);}catch{/* Storage may be disabled. */}}
  function set(value){preference=resolve(value);persist();apply();}
  root.ProbeTheme=Object.freeze({get:()=>preference,set,cycle:()=>set(preference==='dark'?'light':'dark')});
  root.addEventListener('storage',event=>{if(event.key===key||event.key===null)set(event.newValue);});
  persist();apply();
})(window);
