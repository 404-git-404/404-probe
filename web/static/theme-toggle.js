/* Shared two-state theme button for the panel and secondary pages. */
(function(root){
  'use strict';
  const labels={light:'浅色',dark:'深色'},icons={light:'☀',dark:'☾'};
  const buttons=[...root.document.querySelectorAll('[data-theme-toggle]')];
  function update(){const mode=root.ProbeTheme.get(),label='当前：'+labels[mode];for(const button of buttons){button.textContent=icons[mode];button.title=label;button.setAttribute('aria-label',label);button.dataset.themeMode=root.ProbeTheme.get();}}
  for(const button of buttons)button.addEventListener('click',()=>root.ProbeTheme.cycle());
  root.addEventListener('404-probe:themechange',update);
  update();
})(window);
