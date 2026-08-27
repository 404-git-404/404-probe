const logout = document.querySelector('#logout');

if (logout) {
  let csrfToken = '';
  fetch('/api/v1/web/session', {cache: 'no-store'})
    .then(response => {
      if (response.status === 401) {
        location.assign('/login');
        throw new Error('authentication required');
      }
      if (!response.ok) throw new Error(response.statusText);
      return response.json();
    })
    .then(session => {
      csrfToken = session.csrf_token;
      logout.disabled = false;
    })
    .catch(() => {
      logout.disabled = true;
    });

  logout.addEventListener('click', async () => {
    if (!csrfToken) return;
    logout.disabled = true;
    try {
      const body = new URLSearchParams({csrf_token: csrfToken});
      const response = await fetch('/logout', {method: 'POST', body, redirect: 'follow'});
      if (!response.ok) throw new Error(response.statusText);
      location.assign('/login');
    } catch (error) {
      logout.disabled = false;
    }
  });
}
