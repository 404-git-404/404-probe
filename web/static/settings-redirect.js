(() => {
  const section = location.hash.slice(1);
  const target = ['update', 'password', 'regions', 'about'].includes(section) ? section : 'update';
  location.replace('/' + location.search + '#' + target);
})();
