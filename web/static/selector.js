/* Shared Selector interaction/operation state. HTTP remains the existing Web API. */
(function (root) {
  'use strict';
  const errors = {
    selector_not_found: '本地 Selector 已不存在，请刷新后重试',
    choice_not_found: '本地选项已不存在，请刷新后重试',
    clash_api_unavailable: 'Agent 无法连接本地 Clash API',
    clash_api_not_detected: '未检测到 sing-box Clash API',
    clash_api_auth_required: '检测到 Clash API 鉴权；请移除 secret，404-probe 不读取密钥',
    switch_failed: 'Clash API 拒绝了切换',
    switch_verification_failed: '切换后的回读结果与目标不一致',
    selector_switch_pending: '该 Selector 已有等待或执行中的切换',
    agent_disabled: 'Agent 已暂停，无法执行切换',
    agent_revoked: 'Agent 已撤销，无法执行切换',
    agent_offline: 'Agent 离线，无法执行切换',
    outbounds_not_configured: 'Agent 未配置出站发现',
    outbounds_unavailable: 'Clash API 当前不可用',
    selector_choice_not_allowed: '目标已不在最新快照中，请刷新后重试',
  };
  function reason(agent, csrf) {
    const out = agent?.outbounds;
    if (!agent || agent.revoked) return 'Agent 已撤销或移除';
    if (agent.disabled_at) return 'Agent 已暂停';
    if (!agent.online) return 'Agent 离线';
    if (!out?.configured) return '未配置出站发现';
    if (out.status === 'auth_required') return errors.clash_api_auth_required;
    if (out.status === 'not_detected') return errors.clash_api_not_detected;
    if (!out.available) return 'Clash API 当前不可用';
    if (out.stale) return '状态已过期，等待最新快照';
    if (!csrf) return '等待安全会话';
    return '';
  }
  function create({state, fetcher, csrf, onchange = () => {}, unauthorized = () => {}, budgetMs = 30000, pollMs = 250}) {
    const entities = new Map();
    let disposed = false;
    const model = id => {
      if (!entities.has(id)) entities.set(id, {open: false, intent: 0, expanded: '', groups: new Map(), active: null, inspection: null, recoveryIncomplete: false});
      return entities.get(id);
    };
    const group = (id, name) => {
      const m = model(id);
      if (!m.groups.has(name)) m.groups.set(name, {draft: null, generation: 0, operationVersion: 0, feedback: null, recovered: null, remotePending: false});
      return m.groups.get(name);
    };
    const changed = id => { if (!disposed) onchange(id); };
    function open(id) { const m = model(id); m.open = true; m.intent++; changed(id); }
    function close(id) { const m = model(id); m.open = false; m.intent++; m.inspection?.controller.abort(); changed(id); }
    function expand(id, name) {
      const m = model(id); m.expanded = m.expanded === name ? '' : name; m.intent++; changed(id);
    }
    function draft(id, name, value) {
      const g = group(id, name); g.draft = value; g.generation++; changed(id);
    }
    function sync(id) {
      const m = entities.get(id), agent = state.agents.get(id);
      if (!m) return;
      if (!agent || state.blocked(id)) { cancel(id); return; }
      for (const [name, g] of m.groups) {
        const selector = agent.outbounds?.selectors?.find(item => item.name === name);
        if (g.draft !== null && !selector?.choices?.includes(g.draft)) {
          // Preserve the invalid draft so the user sees what disappeared. Do not
          // silently replace it with another allowed target or submit it.
          g.feedback = {message: '先前选择已不在最新选项中，请重新选择', error: true};
        }
      }
    }
    function cancel(id) {
      const m = entities.get(id);
      if (!m) return;
      m.open = false; m.intent++; m.inspection?.controller.abort(); m.active?.controller.abort();
    }
    function closeAll() { disposed = true; for (const id of entities.keys()) cancel(id); }
    async function recover(id, readJSON, timeoutMs = 10000) {
      const m = model(id);
      if (!m.open || disposed || !state.agents.has(id) || state.blocked(id)) return;
      m.inspection?.controller.abort();
      const controller = new AbortController(), ticket = state.read(id, null, []);
      const inspection = {controller}; m.inspection = inspection;
      const before = new Map((state.agents.get(id).outbounds?.selectors || []).map(s => {
        const g = group(id, s.name); return [s.name, [g.generation, g.operationVersion]];
      }));
      const valid = () => !disposed && m.open && m.inspection === inspection && !controller.signal.aborted
        && state.agents.has(id) && !state.blocked(id) && ticket.entry.lifetime === ticket.lifetime
        && state.read(id, null, []).entry === ticket.entry;
      const timer = setTimeout(() => controller.abort(), timeoutMs);
      let rejectAbort;
      const aborted = new Promise((_, reject) => { rejectAbort = reject; });
      const cancelRead = () => rejectAbort(Object.assign(new Error('pending read cancelled'), {name: 'AbortError'}));
      controller.signal.addEventListener('abort', cancelRead, {once: true});
      const read = path => Promise.race([readJSON(path, controller.signal), aborted]);
      changed(id);
      try {
        const pages = await Promise.all(['queued', 'leased'].map(status => read(
          `/api/v1/web/jobs?agent_id=${encodeURIComponent(id)}&probe_type=singbox_selector_switch&status=${status}&limit=10`)));
        if (!valid()) return;
        const candidates = pages.flatMap(page => (page.items || []).slice(0, 10)).filter(item => ['queued', 'running'].includes(item.operation_status));
        const found = new Map(); let cursor = 0;
        // At most two detail reads in flight and twenty candidates total. The
        // two filtered first pages cannot be hidden by newer completed jobs.
        async function worker() {
          while (cursor < candidates.length && valid()) {
            const summary = candidates[cursor++];
            const job = await read(`/api/v1/web/jobs/${encodeURIComponent(summary.job_id)}`);
            if (['queued', 'running'].includes(job.operation_status) && job.config?.selector && !found.has(job.config.selector)) found.set(job.config.selector, job);
          }
        }
        await Promise.all([worker(), worker()]);
        if (!valid()) return;
        m.recoveryIncomplete = pages.some(page => Boolean(page.next_cursor) || page.items?.length > 10);
        m.recoveryMessage = m.recoveryIncomplete ? '待执行状态未完全核实，请在 Probe Jobs 查看；重新打开可核对' : '';
        for (const [name, generations] of before) {
          const g = group(id, name);
          if (g.operationVersion !== generations[1] || m.active) continue;
          const job = found.get(name);
          g.remotePending = Boolean(job);
          // A changed draft keeps its local feedback. The independent remote
          // pending guard still prevents a duplicate write of that new draft.
          if (g.generation !== generations[0]) continue;
          g.recovered = job ? {pending: true, error: false, jobID: job.job_id,
            message: `${job.operation_status === 'queued' ? '等待' : '正在'}切换至 ${job.config.choice || ''} · Probe Jobs 可查看`} : null;
        }
      } catch (error) {
        if (m.inspection === inspection && m.open && !disposed && state.agents.has(id) && !state.blocked(id)) {
          m.recoveryIncomplete = true; m.recoveryMessage = '待执行状态尚未核实，请重新打开核对或在 Probe Jobs 查看';
        }
      } finally {
        clearTimeout(timer); controller.signal.removeEventListener('abort', cancelRead);
        controller.abort();
        if (m.inspection === inspection) m.inspection = null;
        changed(id);
      }
    }
    async function submit(id, name, choice) {
      const m = model(id), g = group(id, name), agent = state.agents.get(id);
      const selector = agent?.outbounds?.selectors?.find(item => item.name === name);
      if (disposed || m.active || m.inspection || m.recoveryIncomplete || g.remotePending || reason(agent, csrf()) || state.blocked(id)
        || !selector?.choices?.includes(choice) || choice === selector.current) return false;
      const mutation = state.beginMutation(id, ['outbounds']);
      if (!mutation) return false;
      g.operationVersion++;
      const controller = new AbortController(), deadline = Date.now() + budgetMs;
      const operation = {controller, requestID: crypto.randomUUID().replaceAll('-', ''), jobID: '', intent: m.intent, generation: g.generation};
      m.active = operation;
      g.feedback = {message: `正在切换至 ${choice}`, pending: true, error: false};
      changed(id);
      let rejectAbort;
      const abortPromise = new Promise((_, reject) => { rejectAbort = reject; });
      const abort = () => rejectAbort(Object.assign(new Error('本地等待已取消'), {name: 'AbortError'}));
      controller.signal.addEventListener('abort', abort, {once: true});
      const timer = setTimeout(() => {
        rejectAbort(Object.assign(new Error('仍在执行／等待核实，请在 Probe Jobs 查看结果'), {name: 'TimeoutError'}));
        controller.abort();
      }, budgetMs);
      const bounded = promise => Promise.race([promise, abortPromise]);
      async function request(path, options = {}) {
        if (controller.signal.aborted) throw Object.assign(new Error('本地等待已取消'), {name: 'AbortError'});
        const response = await bounded(fetcher(path, {cache: 'no-store', ...options, signal: controller.signal}));
        if (response.status === 401) { unauthorized(); throw new Error('authentication required'); }
        const body = await bounded(response.json());
        if (!response.ok) throw Object.assign(new Error(errors[body.error?.code] || body.error?.message || response.statusText), {definitive: options.method === 'POST'});
        return body;
      }
      const wait = ms => bounded(new Promise(resolve => {
        const t = setTimeout(done, Math.min(ms, Math.max(0, deadline - Date.now())));
        function done() { clearTimeout(t); controller.signal.removeEventListener('abort', done); resolve(); }
        controller.signal.addEventListener('abort', done, {once: true});
      }));
      const base = `/api/v1/web/agents/${encodeURIComponent(id)}`;
      try {
        // Exactly one POST. An uncertain response retains request identity; never
        // automatically repeat a mutation or cancel the accepted remote task.
        const accepted = await request(`${base}/outbounds/switch`, {
          method: 'POST', headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrf()},
          body: JSON.stringify({request_id: operation.requestID, selector: name, choice}),
        });
        operation.jobID = accepted.job_id;
        if (!operation.jobID) throw new Error('未取得任务编号，等待核实');
        let job;
        do {
          job = await request(`/api/v1/web/jobs/${encodeURIComponent(operation.jobID)}`);
          if (['success', 'failed', 'expired'].includes(job.operation_status)) break;
          await wait(pollMs);
        } while (!controller.signal.aborted);
        if (job.operation_status === 'expired') throw Object.assign(new Error('切换任务已过期（可能因暂停或超时）'), {definitive: true});
        if (job.operation_status !== 'success' || !job.result?.success) {
          throw Object.assign(new Error(errors[job.result?.error_category] || job.result?.error_message || '切换失败'), {definitive: true});
        }
        const target = job.result.measurement?.selector_switch?.current || choice;
        let latest, reflected = false;
        for (let attempt = 0; attempt < 5; attempt++) {
          latest = await request(base);
          reflected = latest.outbounds?.selectors?.find(item => item.name === name)?.current === target;
          if (reflected) break;
          if (attempt < 4) await wait(300);
        }
        if (!state.finishMutation(mutation, {outbounds: latest.outbounds})) return false;
        if (g.generation === operation.generation) {
          g.feedback = {message: reflected ? `已切换到 ${target}` : `任务已完成，快照尚未确认 ${target}`, error: false, uncertain: !reflected};
          if (reflected) {
            g.draft = null;
            if (m.intent === operation.intent && m.expanded === name) m.expanded = '';
          }
        }
        return reflected;
      } catch (error) {
        if (g.generation === operation.generation && !disposed && state.agents.has(id) && !state.blocked(id)) {
          const uncertain = !error.definitive && error.name !== 'AbortError';
          g.feedback = {message: uncertain ? `${error.message}（请求 ${operation.requestID}）` : error.message,
            error: !uncertain && error.name !== 'AbortError', uncertain, requestID: operation.requestID, jobID: operation.jobID};
        }
        return false;
      } finally {
        clearTimeout(timer); controller.signal.removeEventListener('abort', abort);
        state.finishMutation(mutation);
        g.operationVersion++;
        if (m.active === operation) m.active = null;
        changed(id);
      }
    }
    return {model, group, open, close, expand, draft, sync, cancel, closeAll, submit, recover};
  }
  // The floating surface lives outside card re-renders. Native auto popover
  // supplies top-layer/light dismissal; fixed fallback keeps the same intent.
  function view({controller, state, csrf, onopen = () => {}}) {
    const triggers = new Map();
    let panel = null, activeID = '', native = false;
    const el = (tag, text, className = '') => {
      const node = document.createElement(tag); node.textContent = text; node.className = className; return node;
    };
    function position() {
      const trigger = triggers.get(activeID);
      if (!panel || !trigger?.isConnected) return;
      const rect = trigger.getBoundingClientRect(), width = Math.min(400, innerWidth - 24);
      panel.style.width = `${width}px`;
      panel.style.left = `${Math.max(12, Math.min(rect.left, innerWidth - width - 12))}px`;
      const below = innerHeight - rect.bottom - 20, above = rect.top - 20;
      const useBelow = below >= Math.min(280, above);
      panel.style.maxHeight = `${Math.max(80, useBelow ? below : above)}px`;
      panel.style.top = useBelow ? `${rect.bottom + 8}px` : 'auto';
      panel.style.bottom = useBelow ? 'auto' : `${innerHeight - rect.top + 8}px`;
    }
    function hide() {
      if (!panel || !activeID) return;
      const id = activeID;
      activeID = '';
      if (native) { if (panel.matches(':popover-open')) panel.hidePopover(); }
      else panel.hidden = true;
      controller.close(id);
      triggers.get(id)?.setAttribute('aria-expanded', 'false');
      triggers.get(id)?.focus({preventScroll: true});
    }
    function ensure() {
      if (panel) return;
      panel = el('section', '', 'selector-popover'); panel.id = 'selector-popover';
      panel.setAttribute('aria-label', '出站选择');
      native = typeof panel.showPopover === 'function';
      if (native) {
        panel.setAttribute('popover', 'auto');
        panel.addEventListener('toggle', event => { if (event.newState === 'closed' && activeID && !panel.matches(':popover-open')) hide(); });
      } else panel.hidden = true;
      document.body.append(panel);
      addEventListener('keydown', event => { if (activeID && event.key === 'Escape') { event.preventDefault(); hide(); } });
      addEventListener('pointerdown', event => {
        if (!native && activeID && !panel.contains(event.target) && !triggers.get(activeID)?.contains(event.target)) hide();
      });
      addEventListener('resize', position); addEventListener('scroll', position, true);
    }
    function refresh(id) {
      controller.sync(id);
      if (!panel || activeID !== id) return;
      const m = controller.model(id), agent = state.agents.get(id);
      if (!m.open || !agent || state.blocked(id)) { hide(); return; }
      const focus = document.activeElement;
      const focusedName = focus?.dataset?.selectorName, focusedKind = focus?.dataset?.selectorKind;
      panel.replaceChildren();
      const heading = el('div', '', 'selector-popover-heading');
      const closeButton = el('button', '关闭'); closeButton.type = 'button'; closeButton.addEventListener('click', hide);
      closeButton.dataset.selectorKind = 'close';
      heading.append(el('strong', '出站选择'), closeButton); panel.append(heading);
      const out = agent.outbounds || {}, blocked = reason(agent, csrf());
      panel.append(el('p', `${blocked || '已连接'} · ${out.order_source === 'config' ? '按配置顺序' : '名称回退顺序'}`, 'selector-popover-status'));
      if (m.inspection || m.recoveryMessage) panel.append(el('p', m.inspection ? '正在核对待执行切换…' : m.recoveryMessage, 'selector-popover-status'));
      const jobsLink = el('a', '完整操作记录见 Probe Jobs'); jobsLink.href = '/jobs.html'; jobsLink.dataset.selectorKind = 'jobs'; panel.append(jobsLink);
      if (out.updated_at) panel.append(el('small', `最后快照 ${new Date(out.updated_at).toLocaleString()}`));
      for (const selector of out.selectors || []) {
        const g = controller.group(id, selector.name), expanded = m.expanded === selector.name;
        const section = el('div', '', 'selector-popover-group');
        const summary = el('button', '', 'selector-popover-summary'); summary.type = 'button';
        summary.dataset.selectorName = selector.name; summary.dataset.selectorKind = 'summary';
        summary.setAttribute('aria-expanded', String(expanded));
        summary.append(el('strong', selector.name), el('span', `${selector.current || '—'} ${expanded ? '▴' : '▾'}`));
        summary.addEventListener('click', () => controller.expand(id, selector.name)); section.append(summary);
        if (expanded) {
          const controls = el('div', '', 'selector-popover-controls'), select = el('select', '');
          select.dataset.selectorName = selector.name; select.dataset.selectorKind = 'select';
          select.setAttribute('aria-label', `${selector.name} 目标出站`);
          const selected = g.draft ?? selector.current;
          if (g.draft !== null && !selector.choices?.includes(g.draft)) {
            const invalid = el('option', `${g.draft}（已移除）`); invalid.value = g.draft; invalid.disabled = true; select.append(invalid);
          }
          for (const choice of selector.choices || []) {
            const option = el('option', choice === selector.current ? `${choice}（当前）` : choice); option.value = choice; select.append(option);
          }
          select.value = selected; select.disabled = Boolean(blocked || m.active);
          select.addEventListener('change', () => controller.draft(id, selector.name, select.value));
          const button = el('button', m.active ? '处理中…' : '切换'); button.type = 'button';
          button.dataset.selectorName = selector.name; button.dataset.selectorKind = 'submit';
          button.disabled = Boolean(blocked || m.active || m.inspection || m.recoveryIncomplete || g.remotePending || selected === selector.current || !selector.choices?.includes(selected));
          button.addEventListener('click', () => controller.submit(id, selector.name, select.value));
          controls.append(select, button); section.append(controls);
        }
        const status = g.recovered || g.feedback;
        if (status) {
          const feedback = el('p', status.message, status.error ? 'selector-popover-error' : 'selector-popover-feedback');
          feedback.setAttribute('aria-live', 'polite'); section.append(feedback);
        }
        panel.append(section);
      }
      if (!out.selectors?.length) panel.append(el('p', '未发现 Selector'));
      position();
      if (focusedKind === 'close') closeButton.focus({preventScroll: true});
      else if (focusedKind === 'jobs') jobsLink.focus({preventScroll: true});
      else if (focusedName) {
        let restored = false, summary;
        for (const node of panel.querySelectorAll('[data-selector-kind]')) {
          if (node.dataset.selectorName === focusedName && node.dataset.selectorKind === 'summary') summary = node;
          if (node.dataset.selectorName === focusedName && node.dataset.selectorKind === focusedKind && !node.disabled) { node.focus({preventScroll: true}); restored = true; break; }
        }
        if (!restored) (summary || closeButton).focus({preventScroll: true});
      }
    }
    function trigger(agent) {
      if (!agent.outbounds?.configured) return null;
      const id = agent.agent_id, button = el('button', '出站选择 ▾', 'selector-trigger'); button.type = 'button';
      button.dataset.focusKey = 'selector'; button.dataset.agentId = id;
      button.setAttribute('aria-controls', 'selector-popover'); button.setAttribute('aria-expanded', String(controller.model(id).open));
      triggers.set(id, button);
      button.addEventListener('click', () => {
        ensure();
        if (activeID === id) { hide(); return; }
        if (activeID) hide();
        activeID = id; controller.open(id); refresh(id);
        if (native) panel.showPopover(); else panel.hidden = false;
        position(); panel.querySelector('button')?.focus({preventScroll: true}); onopen(id);
      });
      return button;
    }
    return {trigger, refresh, hide, refreshActive: () => { if (activeID) refresh(activeID); }};
  }
  const api = {create, view, reason, errors};
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.Selector = api;
})(typeof globalThis === 'object' ? globalThis : this);
