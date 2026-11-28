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
        updateTargetControls();
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
      updateTargetControls();
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

  // ---- Network target library ----
  const ntPath = '/api/v1/web/network-targets';
  const NT_LIMIT = 64;
  const ntCount = get('nt-count');
  const ntCreate = get('nt-create');
  const ntStatus = get('nt-status');
  const ntNotice = get('nt-notice');
  const ntEmpty = get('nt-empty');
  const ntTable = get('nt-table');
  const ntTbody = get('nt-tbody');
  const ntSkipReasons = {
    full: '已满 8 个',
    already: '已使用',
    not_found: '设备不存在',
    revoked: '已撤销',
    removing: '正在删除',
    unsupported: '不支持网络质量',
  };
  let ntTargets = [];
  let ntLoaded = false;
  let ntBusy = false;
  let ntLoadSerial = 0;

  const ntErrorLabel = code => window.NetworkQualityCore?.errorText?.[code] || '请检查此项';
  function ntAgentName(id, fallback = '') {
    if (fallback) return fallback;
    const item = typeof agents !== 'undefined' ? agents.get(id) : null;
    return item?.name || item?.state?.hostname || id || '未知设备';
  }

  function clearNtFeedback() {
    if (ntStatus) {
      ntStatus.textContent = '';
      ntStatus.hidden = true;
      ntStatus.classList.remove('settings-good', 'settings-error');
    }
    if (ntNotice) {
      ntNotice.replaceChildren();
      ntNotice.hidden = true;
      ntNotice.removeAttribute('data-tone');
    }
  }

  function setNtStatus(message, good = false) {
    if (!ntStatus) return;
    ntStatus.textContent = message;
    ntStatus.hidden = !message;
    ntStatus.classList.toggle('settings-good', good);
    ntStatus.classList.toggle('settings-error', !good && Boolean(message));
  }

  function setNtNotice(paragraphs = [], items = [], tone = 'good') {
    if (!ntNotice) return;
    ntNotice.replaceChildren();
    for (const line of paragraphs) {
      if (!line) continue;
      const p = document.createElement('p');
      p.textContent = line;
      ntNotice.append(p);
    }
    if (items.length) {
      const ul = document.createElement('ul');
      for (const text of items) {
        const li = document.createElement('li');
        li.textContent = text;
        ul.append(li);
      }
      ntNotice.append(ul);
    }
    ntNotice.dataset.tone = tone;
    ntNotice.hidden = ntNotice.childElementCount === 0;
  }

  function updateTargetControls() {
    const full = ntTargets.length >= NT_LIMIT;
    const disabled = locked || ntBusy || !csrf;
    if (ntCount) ntCount.textContent = `${ntTargets.length} / ${NT_LIMIT}`;
    if (ntCreate) {
      ntCreate.disabled = disabled || full;
      ntCreate.title = full ? `最多只能保存 ${NT_LIMIT} 个目标` : '';
    }
    if (ntTbody) {
      for (const btn of ntTbody.querySelectorAll('button[data-nt-action]')) {
        btn.disabled = disabled;
      }
    }
  }

  function renderTargets() {
    if (!ntTbody || !ntTable || !ntEmpty) return;
    ntEmpty.hidden = !ntLoaded || ntTargets.length > 0;
    ntTable.hidden = ntTargets.length === 0;
    const rows = ntTargets.map(target => {
      const tr = document.createElement('tr');
      tr.dataset.targetId = target.id;
      const usedBy = Array.isArray(target.used_by) ? target.used_by : [];

      const tdRegion = document.createElement('td');
      tdRegion.textContent = target.region || '';

      const tdIsp = document.createElement('td');
      tdIsp.textContent = target.isp || '';

      const tdHost = document.createElement('td');
      const hostCode = document.createElement('code');
      hostCode.className = 'targets-host';
      hostCode.textContent = target.host || '';
      tdHost.append(hostCode);

      const tdPort = document.createElement('td');
      tdPort.className = 'targets-port';
      tdPort.textContent = String(target.port ?? '');

      const tdUsed = document.createElement('td');
      const usedSpan = document.createElement('span');
      if (usedBy.length > 0) {
        usedSpan.className = 'targets-used';
        usedSpan.textContent = `${usedBy.length} 台`;
        usedSpan.title = usedBy.map(a => ntAgentName(a.agent_id, a.name)).join('、');
      } else {
        usedSpan.className = 'targets-unused';
        usedSpan.textContent = '未使用';
      }
      tdUsed.append(usedSpan);

      const tdActions = document.createElement('td');
      tdActions.className = 'targets-row-actions';
      const editBtn = document.createElement('button');
      editBtn.type = 'button';
      editBtn.className = 'targets-action-btn';
      editBtn.dataset.ntAction = 'edit';
      editBtn.textContent = '编辑';
      editBtn.addEventListener('click', () => openTargetEditor(target, editBtn));

      const applyBtn = document.createElement('button');
      applyBtn.type = 'button';
      applyBtn.className = 'targets-action-btn';
      applyBtn.dataset.ntAction = 'apply';
      applyBtn.textContent = '应用到设备…';
      applyBtn.addEventListener('click', () => openApplyDialog(target, applyBtn));

      const delBtn = document.createElement('button');
      delBtn.type = 'button';
      delBtn.className = 'targets-action-btn targets-action-danger';
      delBtn.dataset.ntAction = 'delete';
      delBtn.textContent = '删除';
      delBtn.addEventListener('click', () => deleteTarget(target, delBtn));

      tdActions.append(editBtn, applyBtn, delBtn);
      tr.append(tdRegion, tdIsp, tdHost, tdPort, tdUsed, tdActions);
      return tr;
    });
    ntTbody.replaceChildren(...rows);
    updateTargetControls();
  }

  async function loadTargets() {
    if (!ntTbody) return [];
    const serial = ++ntLoadSerial;
    try {
      const view = await request(ntPath);
      if (serial !== ntLoadSerial) return ntTargets;
      ntTargets = Array.isArray(view?.targets) ? view.targets : [];
      ntLoaded = true;
      renderTargets();
      return ntTargets;
    } catch {
      if (serial !== ntLoadSerial) return ntTargets;
      setNtStatus('读取网络目标失败，请稍后重试。');
      return ntTargets;
    }
  }

  function openTargetEditor(target = null, trigger = null) {
    if (locked || !csrf) return;
    document.getElementById('nt-edit-dialog')?.remove();
    let current = target ? {...target, used_by: Array.isArray(target.used_by) ? [...target.used_by] : []} : null;
    const isEdit = Boolean(current);
    let submitting = false;

    const dialog = document.createElement('dialog');
    dialog.id = 'nt-edit-dialog';
    dialog.className = 'dashboard-dialog nt-dialog';
    document.body.append(dialog);

    const form = document.createElement('form');
    form.autocomplete = 'off';
    const content = document.createElement('div');
    content.className = 'shell-content';
    const errorBanner = document.createElement('p');
    errorBanner.className = 'dashboard-dialog-error';
    errorBanner.setAttribute('role', 'alert');
    errorBanner.hidden = true;

    const grid = document.createElement('div');
    grid.className = 'nt-form-grid';
    const inputs = {};
    const errors = {};
    const fieldSpecs = [
      ['region', '地区', '例如 华东', 32, current?.region || ''],
      ['isp', '运营商', '电信 / 联通 / 移动', 32, current?.isp || ''],
      ['host', '地址', '域名或 IP', 253, current?.host || ''],
      ['port', '端口', '443', 5, current ? String(current.port ?? '') : '443'],
    ];
    for (const [key, labelText, placeholder, maxLength, initial] of fieldSpecs) {
      const wrap = document.createElement('div');
      wrap.className = 'nt-field';
      const label = document.createElement('label');
      label.htmlFor = 'nt-field-' + key;
      label.textContent = labelText;
      const input = document.createElement('input');
      input.id = 'nt-field-' + key;
      input.name = key;
      input.type = 'text';
      input.maxLength = maxLength;
      input.autocomplete = 'off';
      input.placeholder = placeholder;
      input.value = initial;
      if (key === 'isp') input.setAttribute('list', 'nt-isp-hints');
      if (key === 'port') input.inputMode = 'numeric';
      const err = document.createElement('small');
      err.className = 'nt-field-error';
      err.dataset.ntError = key;
      err.setAttribute('role', 'alert');
      input.addEventListener('input', () => {
        err.textContent = '';
        input.removeAttribute('aria-invalid');
        errorBanner.textContent = '';
        errorBanner.hidden = true;
      });
      inputs[key] = input;
      errors[key] = err;
      wrap.append(label, input, err);
      grid.append(wrap);
    }
    content.append(errorBanner, grid);

    const impact = document.createElement('p');
    impact.className = 'nt-edit-impact';
    function paintImpact() {
      const n = current?.used_by?.length || 0;
      impact.textContent = n > 0 ? `将同时修改 ${n} 台设备` : '';
      impact.hidden = !isEdit || n === 0;
    }
    paintImpact();

    const actions = document.createElement('div');
    actions.className = 'shell-actions';
    const cancelBtn = document.createElement('button');
    cancelBtn.type = 'button';
    cancelBtn.textContent = '取消';
    const saveBtn = document.createElement('button');
    saveBtn.type = 'submit';
    saveBtn.className = 'primary-action';
    saveBtn.textContent = '保存';
    actions.append(cancelBtn, saveBtn);

    form.append(content, impact, actions);
    dialog.append(form);

    const shell = DialogShell.attach(dialog, {
      size: 'small',
      title: isEdit ? '编辑目标' : '新建目标',
      busy: () => submitting,
      read: () => [inputs.region.value, inputs.isp.value, inputs.host.value, inputs.port.value],
      onclose: () => dialog.remove(),
    });
    cancelBtn.addEventListener('click', () => shell.requestClose());

    function clearFieldErrors() {
      errorBanner.textContent = '';
      errorBanner.hidden = true;
      for (const key of Object.keys(errors)) {
        errors[key].textContent = '';
        inputs[key].removeAttribute('aria-invalid');
      }
    }

    function showDialogError(msg) {
      errorBanner.textContent = msg;
      errorBanner.hidden = !msg;
    }

    form.addEventListener('submit', async event => {
      event.preventDefault();
      if (submitting || locked || !csrf) return;
      clearFieldErrors();
      const portText = inputs.port.value.trim();
      const payload = {
        region: inputs.region.value,
        isp: inputs.isp.value,
        host: inputs.host.value.trim(),
        port: /^[0-9]+$/.test(portText) ? Number(portText) : 0,
      };
      submitting = true;
      saveBtn.disabled = true;
      cancelBtn.disabled = true;
      try {
        let result;
        if (isEdit) {
          result = await request(`${ntPath}/${encodeURIComponent(current.id)}`, 'PUT', {
            expected_revision: current.revision,
            ...payload,
          });
        } else {
          result = await request(ntPath, 'POST', payload);
        }
        submitting = false;
        shell.markSaved();
        shell.close();
        clearNtFeedback();
        await loadTargets();
        window.dispatchEvent(new CustomEvent('404-probe:network-targets-changed'));
        const saved = result?.target || payload;
        const label = `${saved.region} ${saved.isp}`.trim();
        const familyAdjusted = Array.isArray(result?.family_adjusted) ? result.family_adjusted : [];
        const skipped = Array.isArray(result?.skipped) ? result.skipped : [];
        if (familyAdjusted.length || skipped.length) {
          const lines = [isEdit ? `已保存目标「${label}」` : `已新建目标「${label}」`];
          if (familyAdjusted.length) {
            lines.push(`以下设备已自动关闭另一地址族的检测：${familyAdjusted.map(a => ntAgentName(a.agent_id, a.name)).join('、')}`);
          }
          if (skipped.length) {
            const skippedNames = skipped.map(item => typeof item === 'string'
              ? ntAgentName(item)
              : ntAgentName(item?.agent_id, item?.name)).join('、');
            lines.push(`以下设备未修改（已撤销 / 正在删除 / 不支持）：${skippedNames}`);
          }
          setNtNotice(lines, [], 'warn');
        } else {
          const updatedCount = Number(result?.updated_agents || 0);
          setNtStatus(
            isEdit
              ? (updatedCount > 0 ? `已保存目标「${label}」，已同步修改 ${updatedCount} 台设备` : `已保存目标「${label}」`)
              : `已新建目标「${label}」`,
            true
          );
        }
      } catch (error) {
        submitting = false;
        const details = error.details || {};
        if (error.status === 409 && details.code === 'duplicate_target') {
          showDialogError('已有相同的目标');
        } else if (error.status === 409 && details.code === 'target_limit') {
          showDialogError(`目标库已满（最多 ${NT_LIMIT} 个）`);
        } else if (error.status === 409 && details.code === 'revision_conflict') {
          showDialogError('目标已被别处修改，已刷新，请重试');
          const refreshed = await loadTargets();
          if (isEdit && current) {
            const latest = refreshed.find(t => t.id === current.id);
            if (latest) {
              current = {...latest, used_by: Array.isArray(latest.used_by) ? [...latest.used_by] : []};
              paintImpact();
            } else {
              showDialogError('目标已被别处删除，已刷新列表');
              saveBtn.disabled = true;
              cancelBtn.disabled = false;
              return;
            }
          }
        } else if (error.status === 400 && Array.isArray(details.details) && details.details.length) {
          let unmatched = [];
          for (const item of details.details) {
            const text = ntErrorLabel(item?.code);
            if (item?.field && errors[item.field]) {
              errors[item.field].textContent = text;
              inputs[item.field].setAttribute('aria-invalid', 'true');
            } else {
              unmatched.push(text);
            }
          }
          if (unmatched.length) showDialogError([...new Set(unmatched)].join('；'));
        } else if (error.status === 503) {
          showDialogError('地址检查繁忙，请稍后重试');
        } else if (error.status === 403) {
          showDialogError(forbiddenSave);
        } else {
          showDialogError(genericSave);
        }
      } finally {
        submitting = false;
        if (dialog.isConnected) {
          saveBtn.disabled = locked || !csrf;
          cancelBtn.disabled = false;
        }
      }
    });

    shell.open(trigger);
    inputs.region.focus({preventScroll: true});
  }

  async function deleteTarget(target, trigger) {
    if (ntBusy || locked || !csrf) return;
    const usedBy = Array.isArray(target.used_by) ? target.used_by : [];
    const label = `${target.region} ${target.isp}`.trim();
    const message = usedBy.length > 0
      ? `删除目标「${label}」？\n将从以下 ${usedBy.length} 台设备移除：${usedBy.map(a => ntAgentName(a.agent_id, a.name)).join('、')}`
      : `删除目标「${label}」？`;
    const confirmed = await DialogShell.confirm({
      title: '删除目标',
      message,
      confirmText: '删除',
      danger: true,
      trigger,
    });
    if (!confirmed) return;
    ntBusy = true;
    clearNtFeedback();
    updateTargetControls();
    try {
      const result = await request(`${ntPath}/${encodeURIComponent(target.id)}`, 'DELETE', {
        expected_revision: target.revision,
      });
      await loadTargets();
      window.dispatchEvent(new CustomEvent('404-probe:network-targets-changed'));
      const removedFrom = Number(result?.removed_from || 0);
      setNtStatus(
        removedFrom > 0 ? `已删除目标「${label}」（已从 ${removedFrom} 台设备移除）` : `已删除目标「${label}」`,
        true
      );
    } catch (error) {
      const details = error.details || {};
      if (error.status === 409 && details.code === 'revision_conflict') {
        await loadTargets();
        setNtNotice(['目标已被别处修改，已刷新，请重试'], [], 'error');
      } else if (error.status === 404) {
        await loadTargets();
        setNtStatus('该目标已不存在，已刷新列表。');
      } else {
        setNtStatus(error.status === 403 ? forbiddenSave : '删除目标失败，请重试。');
      }
    } finally {
      ntBusy = false;
      updateTargetControls();
    }
  }

  function openApplyDialog(target, trigger) {
    if (ntBusy || locked || !csrf) return;
    document.getElementById('nt-apply-dialog')?.remove();
    let submitting = false;
    const usedIds = new Set((target.used_by || []).map(a => a.agent_id));
    const deviceList = (typeof agents !== 'undefined' ? [...agents.values()] : [])
      .sort((a, b) => (a.name || a.state?.hostname || a.agent_id).localeCompare(b.name || b.state?.hostname || b.agent_id, 'zh-CN'));

    const dialog = document.createElement('dialog');
    dialog.id = 'nt-apply-dialog';
    dialog.className = 'dashboard-dialog nt-dialog';
    document.body.append(dialog);

    const content = document.createElement('div');
    content.className = 'shell-content';
    const errorBanner = document.createElement('p');
    errorBanner.className = 'dashboard-dialog-error';
    errorBanner.setAttribute('role', 'alert');
    errorBanner.hidden = true;
    content.append(errorBanner);

    const listEl = document.createElement('div');
    listEl.className = 'nt-apply-list';
    const selectableBoxes = [];

    if (!deviceList.length) {
      const emptyNote = document.createElement('p');
      emptyNote.className = 'dashboard-dialog-note';
      emptyNote.textContent = '暂无可应用的设备。';
      content.append(emptyNote);
    } else {
      for (const agent of deviceList) {
        const isUsed = usedIds.has(agent.agent_id);
        const row = document.createElement('label');
        row.className = 'nt-apply-row';
        row.dataset.agentId = agent.agent_id;
        row.dataset.used = String(isUsed);
        const box = document.createElement('input');
        box.type = 'checkbox';
        box.value = agent.agent_id;
        box.checked = isUsed;
        box.disabled = isUsed;
        if (!isUsed) {
          box.addEventListener('change', syncApplyButton);
          selectableBoxes.push(box);
        }
        row.append(box);
        if (typeof countryMark === 'function') {
          row.append(countryMark(agent.country_code, agent.country_source, agent.country_lookup_enabled));
        }
        const nameSpan = document.createElement('span');
        nameSpan.className = 'nt-apply-name';
        nameSpan.textContent = agent.name || agent.state?.hostname || agent.agent_id;
        nameSpan.title = nameSpan.textContent;
        row.append(nameSpan);
        if (isUsed) {
          const badge = document.createElement('span');
          badge.className = 'nt-apply-badge';
          badge.textContent = '已使用';
          row.append(badge);
        }
        listEl.append(row);
      }
      content.append(listEl);
    }

    const actions = document.createElement('div');
    actions.className = 'shell-actions';
    const cancelBtn = document.createElement('button');
    cancelBtn.type = 'button';
    cancelBtn.textContent = '取消';
    const applyBtn = document.createElement('button');
    applyBtn.type = 'button';
    applyBtn.className = 'primary-action';
    applyBtn.textContent = '添加到所选设备';
    applyBtn.disabled = true;
    actions.append(cancelBtn, applyBtn);
    dialog.append(content, actions);

    const shell = DialogShell.attach(dialog, {
      size: 'small',
      title: '应用到设备',
      subtitle: `${target.region} ${target.isp} · ${target.host}:${target.port}`,
      busy: () => submitting,
      guard: false,
      onclose: () => dialog.remove(),
    });
    cancelBtn.addEventListener('click', () => shell.close());

    function syncApplyButton() {
      applyBtn.disabled = submitting || locked || !csrf || !selectableBoxes.some(box => box.checked);
    }

    applyBtn.addEventListener('click', async () => {
      const selectedIds = selectableBoxes.filter(box => box.checked).map(box => box.value);
      if (!selectedIds.length || submitting || locked || !csrf) return;
      submitting = true;
      errorBanner.hidden = true;
      errorBanner.textContent = '';
      syncApplyButton();
      cancelBtn.disabled = true;
      try {
        const result = await request(`${ntPath}/${encodeURIComponent(target.id)}/apply`, 'POST', {agent_ids: selectedIds});
        submitting = false;
        shell.close();
        clearNtFeedback();
        await loadTargets();
        window.dispatchEvent(new CustomEvent('404-probe:network-targets-changed'));
        const added = Array.isArray(result?.added) ? result.added : [];
        const skipped = Array.isArray(result?.skipped) ? result.skipped : [];
        const summary = `已添加到 ${added.length} 台`;
        if (skipped.length) {
          const skipLines = skipped.map(item => `${ntAgentName(item?.agent_id, item?.name)}：${ntSkipReasons[item?.reason] || item?.reason || '未添加'}`);
          setNtNotice([summary], skipLines, added.length > 0 ? 'warn' : 'error');
        } else {
          setNtNotice([summary], [], 'good');
        }
      } catch (error) {
        submitting = false;
        errorBanner.textContent = error.status === 404
          ? '该目标已不存在，请关闭后刷新。'
          : error.status === 403 ? forbiddenSave : '应用失败，请重试。';
        errorBanner.hidden = false;
        cancelBtn.disabled = false;
        syncApplyButton();
      }
    });

    shell.open(trigger);
    (selectableBoxes[0] || cancelBtn).focus({preventScroll: true});
  }

  ntCreate?.addEventListener('click', () => openTargetEditor(null, ntCreate));

  document.addEventListener('404-probe:edit-network-target', async event => {
    const targetId = event.detail?.id;
    if (!targetId) return;
    clearNtFeedback();
    if (!sessionReady) initialize('targets');
    try { await sessionReady; } catch { return; }
    const list = await loadTargets();
    const found = list.find(t => t.id === targetId);
    if (found) {
      openTargetEditor(found);
    } else {
      setNtStatus('目标库中找不到该目标，可能已被删除。');
    }
  });

  window.addEventListener('404-probe:network-targets-changed', () => {
    if (dashboardUI.page() === 'targets') void loadTargets();
  });

  let sessionReady, versionReady, regionsReady;
  function initialize(section) {
    if (!sessionReady) sessionReady = readSession().then(() => {
      passwordButton.disabled = !csrf;
      updateRegions();
      updateSkinControls();
      updateTargetControls();
      if (versionReady) check.disabled = !csrf;
    }, () => passwordStatus('读取会话失败，请刷新后重试。'));

    if ((section === 'update' || section === 'about') && !versionReady) {
      versionReady = request('/api/v1/web/version').then(view => {
        showUpdate(view.update);
        showAbout(view);
      }, () => { updateError.textContent = '读取设置失败，仍可重新保存。'; });
      void Promise.all([sessionReady, versionReady]).then(() => { check.disabled = !csrf; });
    }
    if (section === 'targets') {
      void loadTargets();
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
