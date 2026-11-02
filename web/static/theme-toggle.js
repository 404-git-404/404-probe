/* Standalone temporary header control; move this button into the panel later. */
(function(root){
  'use strict';
  const labels={light:'浅色',dark:'深色',system:'跟随系统'};
  const buttons=[...root.document.querySelectorAll('[data-theme-toggle]')];
  function update(){const label='主题：'+labels[root.ProbeTheme.get()];for(const button of buttons){button.title=label;button.setAttribute('aria-label',label);button.dataset.themeMode=root.ProbeTheme.get();}}
  for(const button of buttons)button.addEventListener('click',()=>root.ProbeTheme.cycle());
  root.addEventListener('404-probe:themechange',update);
  update();
})(window);
