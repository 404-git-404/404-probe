/* Shared three-state theme button for secondary pages. */
(function(root){
  'use strict';
  const labels={light:'浅色',dark:'深色',system:'跟随系统'},icons={light:'☀',dark:'☾',system:'◐'};
  const buttons=[...root.document.querySelectorAll('[data-theme-toggle]')];
  function update(){const mode=root.ProbeTheme.preference(),label='当前：'+labels[mode];for(const button of buttons){button.textContent=icons[mode];button.title=label;button.setAttribute('aria-label',label);button.dataset.themeMode=mode;}}
  for(const button of buttons)button.addEventListener('click',()=>root.ProbeTheme.cycle());
  root.addEventListener('404-probe:themechange',update);
  update();
})(window);
