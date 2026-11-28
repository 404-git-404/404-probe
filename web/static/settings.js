(() => {
  'use strict';
  const get = id => document.getElementById(id);
  const settingsPath = '/api/v1/web/settings/';
  const genericSave = '保存失败，请重试。';
  const forbiddenSave = '保存被拒绝（CSRF 校验失败或页面已过期），请刷新后重试。';
  let csrf = '';
  let locked = false;

  async function request(path, method = 'GET', data, timeout = 10000) {
    const options = {method, cache: 'no-store', signal: AbortSignal.timeout(timeout)};
    const headers = {};
    if (method !== 'GET' && method !== 'HEAD') {
      headers['X-CSRF-Token'] = csrf;
    }
    if (data !== undefined) {
      headers['Content-Type'] = 'application/json';
      options.body = typeof data === 'string' ? data : JSON.stringify(data);
    }
    if (Object.keys(headers).length) options.headers = headers;
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
      dashboardUI.refreshVersion(true);
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
  function updatePasswordPreviews() {
    for (const field of passwordFields) {
      const preview = get(field.id + '-preview');
      const value = showPassword.checked ? field.value : '';
      preview.textContent = value;
      preview.hidden = !value;
    }
  }
  showPassword.addEventListener('change', updatePasswordPreviews);
  for (const field of passwordFields) {
    field.addEventListener('input', updatePasswordPreviews);
    field.addEventListener('change', updatePasswordPreviews);
  }

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
        updatePasswordPreviews();
        check.disabled = true;
        updateRegions();
        updateSkinControls();
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
      updatePasswordPreviews();
      csrf = '';
      locked = true;
      check.disabled = true;
      updateRegions();
      updateSkinControls();
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
    chips.replaceChildren(...countries.filter(({code}) => selected.has(code)).map(({code}) => {
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
      void dashboardData.reload();
    } catch (error) {
      regionStatus.textContent = error.status === 403 ? forbiddenSave : genericSave;
    } finally {
      saving = false;
      updateRegions();
    }
  });

  const appearanceModeButtons = [...document.querySelectorAll('[data-appearance-mode]')];
  const skinImportButton = get('skin-import');
  const skinFileInput = get('skin-file-input');
  const skinStatus = get('skin-status');
  const skinErrors = get('skin-errors');
  const skinList = get('skin-list');
  const skinsPath = '/api/v1/web/skins';
  const skinModeLabels = {light: '浅色', dark: '深色', file: '文件'};
  const skinReasonLabels = {
    invalid_value: '颜色值不合法',
    too_long: '内容过长',
    invalid_format: '格式标识必须是 404-probe-skin',
    invalid_version: '版本必须是 1',
    invalid_name: '名称需为 1–40 个字符',
    invalid_author: '作者不能超过 40 个字符',
    invalid_description: '说明不能超过 200 个字符',
    empty_variables: '至少需要 1 个有效颜色变量',
    invalid_json: '不是有效的 JSON 文件',
    no_valid_variables: '至少需要 1 个有效颜色变量',
    invalid_variables: '颜色变量必须是对象',
    invalid_size: '皮肤文件不能超过 64 KB',
  };
  const skinLengthLabels = {
    name: '名称需为 1–40 个字符',
    author: '作者不能超过 40 个字符',
    description: '说明不能超过 200 个字符',
  };
  let skinState = {active_id: 'default', skins: []};
  let skinBusy = false;
  let skinLoadSerial = 0;
  let lastSkinVersion = 0;

  function paintAppearanceMode() {
    if (!window.ProbeTheme) return;
    const pref = typeof window.ProbeTheme.preference === 'function' ? window.ProbeTheme.preference() : window.ProbeTheme.get();
    for (const button of appearanceModeButtons) {
      button.setAttribute('aria-pressed', String(button.dataset.appearanceMode === pref));
    }
  }
  for (const button of appearanceModeButtons) {
    button.addEventListener('click', () => {
      window.ProbeTheme?.set(button.dataset.appearanceMode);
    });
  }
  window.addEventListener('404-probe:themechange', paintAppearanceMode);
  paintAppearanceMode();

  function setSkinMessage(message, good = false) {
    skinStatus.textContent = message;
    skinStatus.hidden = !message;
    skinStatus.classList.toggle('settings-good', good);
    skinStatus.classList.toggle('settings-error', !good && Boolean(message));
  }

  function clearSkinErrors() {
    skinErrors.replaceChildren();
    skinErrors.hidden = true;
  }

  function showSkinError(heading, items = [], extraCount = 0) {
    skinErrors.replaceChildren();
    const p = document.createElement('p');
    p.textContent = heading;
    skinErrors.append(p);
    if (items.length) {
      const ul = document.createElement('ul');
      for (const text of items) {
        const li = document.createElement('li');
        li.textContent = text;
        ul.append(li);
      }
      skinErrors.append(ul);
    }
    if (extraCount > 0) {
      const note = document.createElement('p');
      note.className = 'settings-note';
      note.textContent = `另有 ${extraCount} 条错误未显示。`;
      skinErrors.append(note);
    }
    skinErrors.hidden = false;
  }

  function formatSkinDetail(item) {
    const mode = skinModeLabels[item?.mode] || item?.mode || '文件';
    const variable = item?.variable ? ' ' + item.variable : '';
    const reason = (item?.reason === 'invalid_length' && skinLengthLabels[item?.variable])
      || skinReasonLabels[item?.reason] || item?.reason || '格式无效';
    return `${mode}${variable}：${reason}`;
  }

  function reloadSkinStylesheet(skinId) {
    return new Promise(resolve => {
      const version = Math.max(Date.now(), lastSkinVersion + 1);
      lastSkinVersion = version;
      const nextHref = '/skin.css?v=' + version;
      let link = [...document.querySelectorAll('link[rel="stylesheet"]')].find(node => {
        const raw = node.getAttribute('href') || '';
        return raw === '/skin.css' || raw.startsWith('/skin.css?');
      });
      if (!link) {
        link = document.createElement('link');
        link.rel = 'stylesheet';
        const styleLink = [...document.querySelectorAll('link[rel="stylesheet"]')].find(node => {
          const raw = node.getAttribute('href') || '';
          return raw === '/style.css' || raw.endsWith('/style.css');
        });
        if (styleLink) styleLink.after(link);
        else document.head.append(link);
      }
      let finished = false;
      const done = () => {
        if (finished) return;
        finished = true;
        clearTimeout(timer);
        window.dispatchEvent(new CustomEvent('404-probe:skinchange', {detail: {id: skinId, href: nextHref}}));
        resolve();
      };
      const timer = setTimeout(done, 5000);
      link.addEventListener('load', done, {once: true});
      link.addEventListener('error', done, {once: true});
      link.setAttribute('href', nextHref);
    });
  }

  function updateSkinControls() {
    const disabled = locked || skinBusy || !csrf;
    if (skinImportButton) skinImportButton.disabled = disabled;
    if (!skinList) return;
    for (const card of skinList.querySelectorAll('.skin-card')) {
      const active = card.dataset.active === 'true';
      const useBtn = card.querySelector('[data-skin-action="activate"]');
      const delBtn = card.querySelector('[data-skin-action="delete"]');
      if (useBtn) useBtn.disabled = disabled || active;
      if (delBtn) delBtn.disabled = disabled;
    }
  }

  function renderSkins() {
    if (!skinList) return;
    const skins = Array.isArray(skinState.skins) ? skinState.skins : [];
    const activeId = skinState.active_id || 'default';
    if (!skins.length) {
      const empty = document.createElement('p');
      empty.className = 'settings-note';
      empty.textContent = '暂无可用皮肤。';
      skinList.replaceChildren(empty);
      updateSkinControls();
      return;
    }
    const cards = skins.map(skin => {
      const isBuiltin = skin.builtin === true || skin.id === 'default';
      const isActive = skin.id === activeId;
      const card = document.createElement('article');
      card.className = 'skin-card';
      card.dataset.skinId = skin.id;
      card.dataset.active = String(isActive);
      card.dataset.builtin = String(isBuiltin);

      const meta = document.createElement('div');
      meta.className = 'skin-meta';

      const head = document.createElement('div');
      head.className = 'skin-head';
      const nameEl = document.createElement('strong');
      nameEl.className = 'skin-name';
      nameEl.textContent = skin.name || skin.id;
      head.append(nameEl);
      if (isActive) {
        const badge = document.createElement('span');
        badge.className = 'skin-active-badge';
        badge.textContent = '当前';
        head.append(badge);
      }
      if (skin.author) {
        const author = document.createElement('span');
        author.className = 'skin-author';
        author.textContent = skin.author;
        head.append(author);
      }
      meta.append(head);

      if (skin.description) {
        const desc = document.createElement('p');
        desc.className = 'skin-desc';
        desc.textContent = skin.description;
        meta.append(desc);
      }

      const swatches = document.createElement('div');
      swatches.className = 'skin-swatches';
      for (const [modeKey, modeLabel] of [['light', '浅色'], ['dark', '深色']]) {
        const colors = Array.isArray(skin.swatches?.[modeKey]) ? skin.swatches[modeKey] : [];
        if (!colors.length) continue;
        const row = document.createElement('div');
        row.className = 'skin-swatch-row';
        row.dataset.mode = modeKey;
        const label = document.createElement('span');
        label.className = 'skin-swatch-label';
        label.textContent = modeLabel;
        row.append(label);
        for (const color of colors) {
          const chip = document.createElement('span');
          chip.className = 'skin-swatch';
          chip.style.backgroundColor = color;
          chip.title = color;
          chip.setAttribute('aria-hidden', 'true');
          row.append(chip);
        }
        swatches.append(row);
      }
      if (swatches.childElementCount) meta.append(swatches);

      const actions = document.createElement('div');
      actions.className = 'skin-actions';

      const useBtn = document.createElement('button');
      useBtn.type = 'button';
      useBtn.dataset.skinAction = 'activate';
      useBtn.textContent = '使用';
      useBtn.addEventListener('click', () => activateSkin(skin));
      actions.append(useBtn);

      if (!isBuiltin) {
        const downloadLink = document.createElement('a');
        downloadLink.className = 'settings-link-btn';
        downloadLink.dataset.skinAction = 'download';
        downloadLink.href = `${skinsPath}/${encodeURIComponent(skin.id)}/download`;
        downloadLink.download = '';
        downloadLink.textContent = '下载';
        actions.append(downloadLink);

        const delBtn = document.createElement('button');
        delBtn.type = 'button';
        delBtn.className = 'skin-delete-btn';
        delBtn.dataset.skinAction = 'delete';
        delBtn.textContent = '删除';
        delBtn.addEventListener('click', () => deleteSkin(skin, delBtn));
        actions.append(delBtn);
      }

      card.append(meta, actions);
      return card;
    });
    skinList.replaceChildren(...cards);
    updateSkinControls();
  }

  async function loadSkins() {
    if (!skinList) return;
    const serial = ++skinLoadSerial;
    try {
      const view = await request(skinsPath);
      if (serial !== skinLoadSerial) return;
      skinState = {
        active_id: view?.active_id || 'default',
        skins: Array.isArray(view?.skins) ? view.skins : [],
      };
      renderSkins();
    } catch {
      if (serial !== skinLoadSerial) return;
      setSkinMessage('读取皮肤列表失败，请稍后重试。');
    }
  }

  async function activateSkin(skin) {
    if (skinBusy || !csrf || locked) return;
    skinBusy = true;
    clearSkinErrors();
    setSkinMessage('');
    updateSkinControls();
    try {
      await request(skinsPath + '/active', 'PUT', {id: skin.id});
      await reloadSkinStylesheet(skin.id);
      await loadSkins();
      setSkinMessage(`已使用皮肤「${skin.name}」`, true);
    } catch (error) {
      setSkinMessage(error.status === 403 ? forbiddenSave : '切换皮肤失败，请重试。');
    } finally {
      skinBusy = false;
      updateSkinControls();
    }
  }

  async function deleteSkin(skin, trigger) {
    if (skinBusy || !csrf || locked) return;
    const confirmed = await DialogShell.confirm({
      title: '删除皮肤',
      message: `删除皮肤「${skin.name}」？正在使用时会回到默认。`,
      confirmText: '删除',
      danger: true,
      trigger,
    });
    if (!confirmed) return;
    const wasActive = skin.id === skinState.active_id;
    skinBusy = true;
    clearSkinErrors();
    setSkinMessage('');
    updateSkinControls();
    try {
      await request(`${skinsPath}/${encodeURIComponent(skin.id)}`, 'DELETE');
      if (wasActive) await reloadSkinStylesheet('default');
      await loadSkins();
      setSkinMessage(`已删除皮肤「${skin.name}」`, true);
    } catch (error) {
      setSkinMessage(error.status === 403 ? forbiddenSave : '删除皮肤失败，请重试。');
    } finally {
      skinBusy = false;
      updateSkinControls();
    }
  }

  skinImportButton?.addEventListener('click', () => {
    if (skinBusy || !csrf || locked) return;
    skinFileInput.value = '';
    skinFileInput.click();
  });

  skinFileInput?.addEventListener('change', async () => {
    const file = skinFileInput.files?.[0];
    skinFileInput.value = '';
    if (!file || skinBusy || !csrf || locked) return;
    clearSkinErrors();
    setSkinMessage('');
    if (file.size > 65536) {
      showSkinError('导入失败：皮肤文件不能超过 64 KB。');
      return;
    }
    skinBusy = true;
    updateSkinControls();
    try {
      const raw = await file.text();
      const result = await request(skinsPath, 'POST', raw);
      const name = result?.skin?.name || '未命名皮肤';
      const ignored = Array.isArray(result?.ignored) ? result.ignored : [];
      await loadSkins();
      const msg = ignored.length
        ? `已导入「${name}」，忽略了 ${ignored.length} 个不认识的变量`
        : `已导入「${name}」`;
      setSkinMessage(msg, true);
      skinStatus.title = ignored.length ? `已忽略：${ignored.join('、')}` : '';
    } catch (error) {
      const details = error.details || {};
      if (error.status === 409 || details.code === 'skin_limit') {
        showSkinError('导入失败：最多只能保存 20 个皮肤，请先删除不用的皮肤。');
      } else if (Array.isArray(details.details) && details.details.length) {
        const shown = details.details.slice(0, 10).map(formatSkinDetail);
        const extra = Math.max(0, details.details.length - 10);
        showSkinError('导入失败，发现不合法的皮肤配置：', shown, extra);
      } else if (error.status === 403) {
        showSkinError(forbiddenSave);
      } else {
        showSkinError(details.message || '导入失败，请检查皮肤 JSON 文件格式。');
      }
    } finally {
      skinBusy = false;
      updateSkinControls();
    }
  });

  let sessionReady, versionReady, regionsReady;
  function initialize(section) {
    if (!sessionReady) sessionReady = readSession().then(() => {
      passwordButton.disabled = !csrf;
      updateRegions();
      updateSkinControls();
      if (versionReady) check.disabled = !csrf;
    }, () => passwordStatus('读取会话失败，请刷新后重试。'));

    if ((section === 'update' || section === 'about') && !versionReady) {
      versionReady = request('/api/v1/web/version').then(view => {
        showUpdate(view.update);
        showAbout(view);
      }, () => { updateError.textContent = '读取设置失败，仍可重新保存。'; });
      void Promise.all([sessionReady, versionReady]).then(() => { check.disabled = !csrf; });
    }
    if (section === 'regions' && !regionsReady) {
      regionsReady = request(settingsPath + 'youtube-ad-free-regions').then(view => {
        defaults = view.defaults;
        savedRegions = new Set(view.regions);
        selected = new Set(savedRegions);
        showCountryList();
      }, () => { regionStatus.textContent = '读取地区失败，请刷新后重试。'; });
    }
    if (section === 'appearance') {
      paintAppearanceMode();
      void loadSkins();
    }
  }

  document.addEventListener('dashboard:settings', event => initialize(event.detail));
  ProbeBackupSettings.attach({request, session: () => sessionReady || readSession(), page: dashboardUI.page, backupAvailable: dashboardUI.backupAvailable});
  const section = dashboardUI.page();
  if (section !== 'devices' && section !== 'network') initialize(section);
})();
