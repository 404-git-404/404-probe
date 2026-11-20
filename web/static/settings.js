(() => {
  'use strict';
  const get = id => document.getElementById(id);
  const settingsPath = '/api/v1/web/settings/';
  const genericSave = '保存失败，请重试。';
  const forbiddenSave = '保存被拒绝（CSRF 校验失败或页面已过期），请刷新后重试。';
  let csrf = '';
  let locked = false;

  async function request(path, method = 'GET', data) {
    const options = {method, cache: 'no-store', signal: AbortSignal.timeout(10000)};
    if (data !== undefined) {
      options.headers = {'Content-Type': 'application/json', 'X-CSRF-Token': csrf};
      options.body = JSON.stringify(data);
    }
    const response = await fetch(path, options);
    if (response.status === 204) return null;
    const result = await response.json().catch(() => ({}));
    if (!response.ok) {
      if (response.status === 401 && (result.code || result.error?.code) !== 'locked_out' && !locked) location.assign('/login');
      throw Object.assign(new Error('request failed'), {status: response.status, details: result.error || result});
    }
    return result;
  }

  async function readSession() {
    const view = await request('/api/v1/web/session');
    csrf = view.csrf_token || '';
  }

  const check = get('update-check-enabled');
  const updateSaved = get('update-saved');
  const updateError = get('update-check-error');
  function showUpdate(state = {}) {
    check.checked = state.enabled === true;
    const time = Number.isFinite(state.checked_at) && state.checked_at > 0 ? new Date(state.checked_at) : null;
    if (!time) {
      get('update-result').textContent = ' 尚未检查。';
      return;
    }
    const when = new Intl.DateTimeFormat('zh-CN', {month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit'}).format(time);
    const result = state.newer ? `发现新版本 ${state.latest}` : '已是最新版本';
    get('update-result').textContent = ` 上次检查：${when}，${result}。`;
  }

  check.addEventListener('change', async () => {
    check.disabled = true;
    updateSaved.hidden = true;
    updateError.textContent = '';
    const desired = check.checked;
    try {
      showUpdate(await request(settingsPath + 'update-check', 'PUT', {enabled: desired}));
      updateSaved.hidden = false;
    } catch (error) {
      check.checked = !desired;
      updateError.textContent = error.status === 403 ? forbiddenSave : genericSave;
    } finally {
      check.disabled = !csrf || locked;
    }
  });

  function showAbout(view) {
    get('ab-version').textContent = view.version || '—';
    get('ab-schema').textContent = view.schema_version ?? '—';
    get('ab-commit').textContent = view.commit ? view.commit.slice(0, 12) : '—';
    get('ab-commit').title = view.commit || '';
    if (/^v\d+\.\d+\.\d+(?:-(?:beta|rc)\.\d+)?$/.test(view.version)) {
      const link = get('ab-notes');
      link.href = `https://github.com/404-git-404/404-probe/releases/tag/${view.version}`;
      link.textContent = `RELEASE_NOTES_${view.version}.md ↗`;
      link.hidden = false;
      get('ab-notes-missing').hidden = true;
    }
  }

  const passwordForm = get('password-form');
  const passwordButton = get('pw-save');
  const passwordBanner = get('pw-banner');
  const passwordFields = ['current', 'new', 'confirm'].map(name => get('pw-' + name));
  const showPassword = get('pw-show');
  showPassword.addEventListener('change', () => {
    for (const field of passwordFields) field.type = showPassword.checked ? 'text' : 'password';
  });

  function passwordMessage(field, message, invalid = true) {
    const input = get('pw-' + field);
    const hint = get(input.id + '-msg');
    hint.textContent = message;
    hint.classList.toggle('settings-error', invalid);
    if (invalid) input.setAttribute('aria-invalid', 'true');
    else input.removeAttribute('aria-invalid');
  }

  function passwordStatus(message, good = false) {
    passwordBanner.textContent = message;
    passwordBanner.hidden = !message;
    passwordBanner.classList.toggle('settings-good', good);
    passwordBanner.classList.toggle('settings-error', !good);
  }

  function showPasswordError(error) {
    const details = error.details || {};
    switch (details.code) {
      case 'wrong_current': passwordMessage('current', `当前密码不对。再错 ${details.remaining} 次会退出登录。`); break;
      case 'mismatch': passwordMessage('confirm', '两次输入的新密码不一样'); break;
      case 'too_short': passwordMessage('new', `太短了：至少 12 个字符，现在 ${details.characters} 个`); break;
      case 'too_long': passwordMessage('new', '新密码不能超过 1024 字节'); break;
      case 'same_as_current': passwordMessage('new', '新密码不能和当前密码一样'); break;
      case 'invalid_chars': passwordMessage('new', '密码必须是有效文本，不能包含控制字符'); break;
      case 'locked_out':
        locked = true;
        csrf = '';
        passwordForm.reset();
        for (const field of passwordFields) field.type = 'password';
        check.disabled = true;
        updateRegions();
        passwordStatus('当前密码连续输错 5 次，已退出登录。3 秒后转到登录页…');
        setTimeout(() => location.assign('/login'), 3000);
        break;
      case 'busy': passwordStatus('服务器正忙，请稍后重试。'); break;
      default: passwordStatus(error.status === 403 ? forbiddenSave : genericSave);
    }
  }

  passwordForm.addEventListener('submit', async event => {
    event.preventDefault();
    if (!csrf || passwordButton.disabled) return;
    passwordButton.disabled = true;
    passwordStatus('');
    passwordMessage('current', '', false);
    passwordMessage('new', '至少 12 个字符', false);
    passwordMessage('confirm', '', false);
    const [current_password, new_password, confirm_password] = passwordFields.map(field => field.value);
    try {
      await request(settingsPath + 'password', 'POST', {current_password, new_password, confirm_password});
      passwordForm.reset();
      for (const field of passwordFields) field.type = 'password';
      csrf = '';
      locked = true;
      check.disabled = true;
      updateRegions();
      passwordStatus('密码已修改。所有浏览器都已退出，3 秒后跳到登录页。', true);
      setTimeout(() => location.assign('/login'), 3000);
    } catch (error) {
      showPasswordError(error);
    } finally {
      passwordButton.disabled = !csrf || locked;
    }
  });

  const countries = Management.countries();
  const countryNames = new Map(countries.map(({code, zh}) => [code, zh]));
  const search = get('rg-search');
  const chips = get('rg-chips');
  const list = get('rg-list');
  const regionStatus = get('rg-status');
  const restore = get('rg-reset');
  const undo = get('rg-undo');
  const save = get('rg-save');
  let savedRegions = null;
  let selected = new Set();
  let defaults = [];
  let saving = false;

  function updateRegions() {
    const dirty = !!savedRegions && (selected.size !== savedRegions.size || [...selected].some(code => !savedRegions.has(code)));
    const disabled = locked || saving || !savedRegions || !csrf;
    get('rg-count').textContent = savedRegions ? selected.size : '—';
    get('rg-dirty').hidden = !dirty;
    search.disabled = restore.disabled = disabled;
    undo.disabled = save.disabled = disabled || !dirty;
    for (const checkbox of list.querySelectorAll('input')) {
      checkbox.checked = selected.has(checkbox.value);
      checkbox.disabled = disabled;
    }
    chips.replaceChildren(...[...selected].sort().map(code => {
      const chip = document.createElement('button');
      chip.type = 'button';
      chip.textContent = `${countryNames.get(code) || code} ${code} ×`;
      chip.setAttribute('aria-label', `移除 ${countryNames.get(code) || code} ${code}`);
      chip.disabled = disabled;
      chip.addEventListener('click', () => { selected.delete(code); regionStatus.textContent = ''; updateRegions(); });
      return chip;
    }));
  }

  function showCountryList() {
    list.replaceChildren(...Management.matches(search.value, '', countries).map(({code, zh}) => {
      const row = document.createElement('label');
      row.className = 'settings-check';
      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox';
      checkbox.value = code;
      checkbox.addEventListener('change', () => {
        if (checkbox.checked) selected.add(code);
        else selected.delete(code);
        regionStatus.textContent = '';
        updateRegions();
      });
      row.append(checkbox, `${zh} ${code}`);
      return row;
    }));
    list.scrollTop = 0;
    updateRegions();
  }

  search.addEventListener('input', showCountryList);
  restore.addEventListener('click', () => { selected = new Set(defaults); regionStatus.textContent = ''; updateRegions(); });
  undo.addEventListener('click', () => { selected = new Set(savedRegions); regionStatus.textContent = ''; updateRegions(); });
  save.addEventListener('click', async () => {
    saving = true;
    regionStatus.textContent = '';
    updateRegions();
    try {
      const view = await request(settingsPath + 'youtube-ad-free-regions', 'PUT', {regions: [...selected]});
      savedRegions = new Set(view.regions);
      selected = new Set(savedRegions);
      regionStatus.textContent = '✓ 已保存';
    } catch (error) {
      regionStatus.textContent = error.status === 403 ? forbiddenSave : genericSave;
    } finally {
      saving = false;
      updateRegions();
    }
  });

  const links = [...document.querySelectorAll('.settings-nav a')];
  const sections = links.map(link => get(link.hash.slice(1)));
  function highlightSection() {
    let index = 0;
    for (let i = 0; i < sections.length; i++) {
      if (sections[i].getBoundingClientRect().top <= 60) index = i;
    }
    if (scrollY > 0 && scrollY + innerHeight >= document.documentElement.scrollHeight - 2) index = sections.length - 1;
    links.forEach((link, i) => {
      if (i === index) link.setAttribute('aria-current', 'location');
      else link.removeAttribute('aria-current');
    });
  }
  addEventListener('scroll', highlightSection, {passive: true});
  addEventListener('resize', highlightSection);
  for (const link of links) link.addEventListener('click', () => requestAnimationFrame(highlightSection));

  async function initialize() {
    const sessionReady = readSession().then(() => { passwordButton.disabled = !csrf; updateRegions(); }, () => {
      passwordStatus('读取会话失败，请刷新后重试。');
    });
    const versionReady = request('/api/v1/web/version').then(view => { showUpdate(view.update); showAbout(view); }, () => {
      updateError.textContent = '读取设置失败，仍可重新保存。';
    });
    request(settingsPath + 'youtube-ad-free-regions').then(view => {
      defaults = view.defaults;
      savedRegions = new Set(view.regions);
      selected = new Set(savedRegions);
      showCountryList();
    }, () => { regionStatus.textContent = '读取地区失败，请刷新后重试。'; });
    await Promise.all([sessionReady, versionReady]);
    check.disabled = !csrf;
    highlightSection();
  }

  initialize();
})();
