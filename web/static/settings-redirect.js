(() => {
  const section = location.hash.slice(1);
  const target = ['update', 'password', 'regions', 'backup', 'appearance', 'about'].includes(section) ? section : 'update';
  location.replace('/' + location.search + '#' + target);
})();
