/* Synchronous head script: resolve the saved preference before styles load. */
(function(root){
  'use strict';
  const key='404-probe.theme.v1',modes=['light','dark','system'];
  const media=root.matchMedia('(prefers-color-scheme: dark)');
  const valid=value=>modes.includes(value)?value:'system';
  let preference='system';
  try{preference=valid(root.localStorage.getItem(key));}catch{/* Storage may be disabled. */}
  function apply(){
    const theme=preference==='system'?(media.matches?'dark':'light'):preference;
    root.document.documentElement.dataset.theme=theme;
    const meta=root.document.querySelector('meta[name="color-scheme"]');if(meta)meta.content=theme;
    root.document.documentElement.style.colorScheme=theme;
    root.dispatchEvent(new CustomEvent('404-probe:themechange',{detail:{theme,preference}}));
  }
  function set(value){
    preference=valid(value);
    try{root.localStorage.setItem(key,preference);}catch{/* Keep the in-memory choice. */}
    apply();
  }
  root.ProbeTheme=Object.freeze({get:()=>preference,set,cycle:()=>set(modes[(modes.indexOf(preference)+1)%modes.length])});
  media.addEventListener('change',()=>{if(preference==='system')apply();});
  root.addEventListener('storage',event=>{if(event.key===key||event.key===null){preference=valid(event.newValue);apply();}});
  apply();
})(window);
