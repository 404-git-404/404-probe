(() => {
  'use strict';

  const charset = 'qpzry9x8gf2tvdw0s3jn54khce6mua7l';
  const generators = [0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3];

  // age's X25519 keys use Bech32 with the original checksum constant.
  function encode(hrp, bytes) {
    const words = [];
    let buffer = 0, bits = 0;
    for (const byte of bytes) {
      buffer = (buffer << 8) | byte;
      bits += 8;
      while (bits >= 5) {
        bits -= 5;
        words.push((buffer >>> bits) & 31);
      }
    }
    if (bits) words.push((buffer << (5 - bits)) & 31);
    const chars = Array.from(hrp, ch => ch.charCodeAt(0));
    let check = 1;
    for (const word of [...chars.map(c => c >>> 5), 0, ...chars.map(c => c & 31), ...words, 0, 0, 0, 0, 0, 0]) {
      const top = check >>> 25;
      check = ((check & 0x1ffffff) << 5) ^ word;
      for (let i = 0; i < generators.length; i++) {
        if ((top >>> i) & 1) check ^= generators[i];
      }
    }
    check ^= 1;
    for (let i = 5; i >= 0; i--) words.push((check >>> (i * 5)) & 31);
    return `${hrp}1${words.map(word => charset[word]).join('')}`;
  }

  async function generateAgeKey() {
    const pair = await crypto.subtle.generateKey({name: 'X25519'}, true, ['deriveBits']);
    const jwk = await crypto.subtle.exportKey('jwk', pair.privateKey);
    const bytes = value => Uint8Array.from(atob(value.replace(/-/g, '+').replace(/_/g, '/')), ch => ch.charCodeAt(0));
    return {recipient: encode('age', bytes(jwk.x)), identity: encode('age-secret-key-', bytes(jwk.d)).toUpperCase()};
  }

  function attach({request, session, page, backupAvailable}) {
    const path = '/api/v1/web/settings/backup';
    const get = id => document.getElementById(id);
    const field = key => get(`backup-${key}`);
    const fields = field('fields'), form = field('form'), enabled = field('enabled');
    const formatTime = new Intl.DateTimeFormat('zh-CN', {dateStyle: 'medium', timeStyle: 'short'});
    const formatSize = new Intl.NumberFormat('zh-CN', {maximumFractionDigits: 1});
    let state, draftKey, available = backupAvailable(), busy = false, dirty = false, reading = false;
    let operation = 0, formRevision = 0;
    const localTime = at => at ? formatTime.format(new Date(at)) : '—';
    const sizeText = size => size >= 1048576 ? `${formatSize.format(size / 1048576)} MiB`
      : size >= 1024 ? `${formatSize.format(size / 1024)} KiB` : `${size} B`;

    function result(message = '', error = false) {
      field('result').textContent = message;
      field('result').classList.toggle('settings-error', error);
    }
    function clearKey() {
      draftKey = undefined;
      field('private').textContent = '';
      field('key-saved').checked = false;
      field('key').hidden = true;
    }
    function clearCredentials() {
      field('access').value = '';
      field('secret').value = '';
    }
    function controls() {
      fields.disabled = !state || busy || state.running;
      field('save').disabled = !dirty || Boolean(draftKey && !field('key-saved').checked);
      field('run').disabled = !state?.enabled || Boolean(state?.running) || busy || dirty;
      field('run').textContent = state?.running ? '备份中…' : '立即备份';
      field('rekey').hidden = !state?.recipient_saved || Boolean(draftKey);
    }
    function render(view) {
      const finished = state?.running && !view.running;
      state = view;
      get('panel-backup-failure').hidden = !view.alert;
      if (page() !== 'backup') return;
      if (!dirty && !busy) {
        formRevision = view.revision;
        enabled.checked = view.enabled;
        field('endpoint').value = view.endpoint || '';
        field('bucket').value = view.bucket || '';
        field('prefix').value = view.prefix || '';
        field('keep').value = view.keep;
        field('time').value = view.daily_at;
      }
      field('credential-note').hidden = !view.credentials_saved;
      field('key-note').hidden = !view.recipient_saved;
      field('success').textContent = view.last_success ? `${localTime(view.last_success)} · ${sizeText(view.last_size)}` : '—';
      field('last-failure').textContent = view.last_failure ? `${localTime(view.last_failure)} · ${view.reason}` : '—';
      field('last-failure').classList.toggle('settings-error', Boolean(view.last_failure));
      field('next').textContent = view.next ? `${localTime(view.next)}（浏览器本地时间）` : '—';
      field('count').textContent = view.object_count ?? '—';
      field('overdue').hidden = !view.overdue;
      if (finished) result();
      controls();
    }
    async function refresh() {
      if (reading || busy) return;
      reading = true;
      const expected = operation;
      try {
        const view = await request(path);
        if (expected === operation) render(view);
      } catch {
        if (!state && page() === 'backup') result('读取备份设置失败。', true);
      } finally {
        reading = false;
      }
    }
    function destination() {
      return {endpoint: field('endpoint').value.trim(), bucket: field('bucket').value, prefix: field('prefix').value,
        access_key_id: field('access').value, secret_access_key: field('secret').value};
    }
    async function mutate(action, method, data, message) {
      if (busy) return;
      busy = true;
      const expected = ++operation;
      controls();
      result();
      try {
        await session();
        const view = await request(path + action, method, data, 15000);
        if (method === 'PUT' && expected === operation) {
          dirty = false;
          clearKey();
          clearCredentials();
        }
        busy = false;
        if (method === 'PUT' || action === '/run') render(view);
        if (expected === operation) result(message);
      } catch (error) {
        if (expected === operation) result(error.details?.message || '操作失败，请刷新后重试。', true);
      } finally {
        busy = false;
        controls();
      }
    }
    async function generate() {
      if (busy || draftKey) return;
      busy = true;
      const expected = ++operation;
      controls();
      try {
        const key = await generateAgeKey();
        if (expected !== operation) return;
        draftKey = key;
        dirty = true;
        field('private').textContent = key.identity;
        field('key').hidden = false;
        field('download').disabled = false;
      } catch {
        if (expected !== operation) return;
        if (!state?.recipient_saved) enabled.checked = false;
        result('浏览器无法生成 age 钥匙。请使用支持 Web Crypto X25519 的浏览器。', true);
      } finally {
        busy = false;
        controls();
      }
    }

    form.addEventListener('input', () => { dirty = true; controls(); });
    enabled.addEventListener('change', () => {
      if (enabled.checked && !state?.recipient_saved) void generate();
    });
    field('rekey').addEventListener('click', generate);
    field('cancel-key').addEventListener('click', () => {
      clearKey();
      if (!state.recipient_saved) enabled.checked = false;
      controls();
    });
    field('download').addEventListener('click', () => {
      if (!draftKey) return;
      const url = URL.createObjectURL(new Blob([`${draftKey.identity}\n`], {type: 'text/plain'}));
      const link = document.createElement('a');
      link.href = url;
      link.download = '404-probe-backup.agekey';
      link.click();
      field('download').disabled = true;
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    });
    form.addEventListener('submit', event => {
      event.preventDefault();
      if (!state || field('save').disabled) return;
      void mutate('', 'PUT', {...destination(), enabled: enabled.checked, keep: field('keep').valueAsNumber,
        daily_at: field('time').value, revision: formRevision, recipient: draftKey?.recipient || '', key_saved: field('key-saved').checked}, '已保存。');
    });
    field('test').addEventListener('click', () => void mutate('/test', 'POST', destination(), '连接成功，测试对象已删除。'));
    field('run').addEventListener('click', () => void mutate('/run', 'POST', {}, '已开始备份。'));
    document.addEventListener('dashboard:page', event => {
      if (event.detail === 'backup') {
        if (state) render(state);
        void refresh();
      } else {
        operation++;
        dirty = false;
        clearKey();
        clearCredentials();
        result();
      }
    });
    document.addEventListener('dashboard:backup-available', () => {
      if (!available) {
        available = true;
        void refresh();
      }
    });
    setInterval(() => { if (!document.hidden && (available || page() === 'backup')) void refresh(); }, 5000);
    window.addEventListener('pagehide', () => { operation++; clearKey(); clearCredentials(); });
    if (available || page() === 'backup') void refresh();
  }

  globalThis.ProbeBackupSettings = {attach};
})();
