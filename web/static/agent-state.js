/* Arrival/request barriers for the actual unversioned Web DTO. No wall-clock ordering. */
(function (root) {
  'use strict';
  // Public formal-release ordering, also used by the pending-upgrade summary.
  // null means incomparable; never label unknown/pre-release builds "latest".
  function compareStableVersions(left, right) {
    const parts = value => {
      if(typeof value!=='string'||!/^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(value))return null;
      const p=value.slice(1).split('.'),max='18446744073709551615';
      return p.some(n=>n.length>max.length||(n.length===max.length&&n>max))?null:p;
    };
    const a=parts(left),b=parts(right);if(!a||!b)return null;
    for(let i=0;i<3;i++){
      if(a[i].length!==b[i].length)return a[i].length<b[i].length?-1:1;
      if(a[i]!==b[i])return a[i]<b[i]?-1:1;
    }
    return 0;
  }
  const upgradeReasons = Object.freeze({
    rc_agent_required: '这台机器的旧版程序不能直接升级到候选测试版，升级按钮已停用。请先在机器上迁移到支持测试版升级的正式版，原有监控仍可使用。',
    updater_unavailable: '这台机器的升级服务暂时不可用，升级按钮已停用。请先确认没有进行中的升级、恢复或移除，再按升级服务排查文档检查；如果这是 Alpine/OpenRC 机器，则不支持从面板升级。不用重装，原配置和历史都会保留。',
    updater_socket_missing: '这台机器的升级服务连接文件丢失，升级按钮已停用。新版升级服务在目录安全时会尝试恢复；若未恢复，先确认没有进行中的升级、恢复或移除，再只执行一次 sudo systemctl restart 404-probe-agent-updater；仍不可用就停下并按升级服务排查文档检查，保留原配置和历史。',
    updater_directory_missing: '这台机器的升级服务运行目录丢失，升级按钮已停用。请先确认没有进行中的升级、恢复或移除，再只执行一次 sudo systemctl restart 404-probe-agent-updater，让系统重建运行目录；仍不可用就停下并按升级服务排查文档检查，保留原配置和历史。',
    updater_unreachable: '这台机器的升级服务无法连接或没有正确应答，升级按钮已停用。先确认没有进行中的升级、恢复或移除，再只执行一次 sudo systemctl restart 404-probe-agent-updater；仍不可用就停下并按升级服务排查文档检查，保留原配置和历史。',
    updater_identity_mismatch: '这台机器的升级服务目录或连接文件被替换，或属主、权限不符合要求，升级按钮已停用。请保留现场并按升级服务排查文档检查；不要删除占位文件、改权限或反复重启，原配置和历史应保留。',
    updater_unsupported: '这台机器的系统（如 Alpine/OpenRC）暂不支持从面板升级，升级按钮已停用，监控仍可使用。请保持现状，等待后续版本；不要为了启用按钮修改服务或重装。',
    rc_updater_required: '这台机器的升级服务还不支持当前候选测试版，升级按钮已停用。请先按对应候选版的说明更新配套升级服务，保留原配置和历史。旧正式版迁移用的 v1.0.1 upgrade-agent 命令不能替代这一步。',
    legacy_migration_required: '这台机器使用较旧的升级方式，暂时不能从面板升级。请先在机器上运行固定 v1.0.1 install.sh 的 upgrade-agent 命令修复升级服务，保留原配置和历史。（命令见 README 的升级说明）',
    agent_unavailable: '这台机器当前离线、已暂停或已撤销，升级按钮已停用。请先恢复连接或监控；已撤销的机器需要重新添加。',
    version_unrecognized: '面板无法识别这台机器当前的版本号，升级按钮已停用。请确认这台机器运行的是正式发布的程序（不用重装，原配置和历史会保留）。',
    already_latest: '这台机器已与面板版本相同，无需升级。以后更新面板后可再检查。',
    agent_newer_than_server: '这台机器的 Agent 比面板新，不能从面板降级；需要降级请在机器上重新安装。重装前请先确认是否需要保留配置和历史。',
    old_beta_migration_required: '这台机器使用较早的测试版，暂时不能从面板升级。请先在机器上运行固定 v1.0.1 install.sh 的 upgrade-agent 命令修复升级服务，保留原配置和历史。（命令见 README 的升级说明）',
    rc_server_agent_required: '面板正在使用候选测试版，这台机器的旧版程序暂时不能从面板升级。请先在机器上更新配套程序，原有监控仍可使用。',
    server_build_ineligible: '当前面板版本不能发起远程升级，升级按钮已停用。请先将面板更新为官方发布的可升级版本。',
  });
  function upgradeReasonDisplay(target) {
    return Object.hasOwn(upgradeReasons,target?.reason_code) ? upgradeReasons[target.reason_code] : target?.reason || '';
  }
  function updaterTroubleshootingURL(server) {
    const tag = compareStableVersions(server?.version, server?.version) === 0 && !server?.dirty ? server.version : 'main';
    return `https://github.com/404-git-404/404-probe/blob/${tag}/docs/updater-troubleshooting.md`;
  }
  function appendUpgradeReason(element, target, server) {
    element.textContent = upgradeReasonDisplay(target);
    if (!element.textContent.includes('升级服务排查文档')) return;
    const [before, ...after] = element.textContent.split('升级服务排查文档');
    const link = document.createElement('a');
    link.textContent = '升级服务排查文档'; link.href = updaterTroubleshootingURL(server);
    link.target = '_blank'; link.rel = 'noopener noreferrer';
    element.replaceChildren(document.createTextNode(before), link, document.createTextNode(after.join('升级服务排查文档')));
  }
  function updaterHealthReason(health) {
    if (!health || health.status === 'healthy') return '';
    if(health.status === 'missing') return health.reason === 'runtime_directory_missing' ? 'updater_directory_missing' : 'updater_socket_missing';
    const codes={unreachable:'updater_unreachable',identity_mismatch:'updater_identity_mismatch',unsupported:'updater_unsupported'};
    return Object.hasOwn(codes,health.status)?codes[health.status]:'updater_unavailable';
  }
  function upgradeButtonHint(agent, server, busy = false) {
    if (busy || (agent.upgrade && !['succeeded', 'failed', 'rolled_back'].includes(agent.upgrade.status))) return '正在升级，请等待完成';
    const order = compareStableVersions(agent.version, server.version);
    let code = order === 0 ? 'already_latest' : order === 1 ? 'agent_newer_than_server'
      : !agent.online || agent.disabled_at || agent.revoked ? 'agent_unavailable'
      : (order === -1 ? updaterHealthReason(agent.updater_health) : '') || (!server.upgrade_eligible ? 'server_build_ineligible'
      : /-beta\./.test(agent.version || '') && !agent.upgrade_v2 ? 'old_beta_migration_required'
      : !agent.upgrade_capable && ['v0.9.3','v1.0.0'].includes(agent.version) ? 'legacy_migration_required'
      : !agent.upgrade_capable ? 'updater_unavailable' : '');
    if (!code && order === null && !agent.upgrade_v2) code = 'version_unrecognized';
    return upgradeReasonDisplay({reason_code: code}) || '可从面板升级到与面板相同的版本';
  }
  const resource = key => ({
    name: 'name', revoked: 'lifecycle', disabled_at: 'lifecycle', created_at: 'lifecycle',
    country_code: 'country', country_source: 'country', country_code_lookup_operation: 'country',
    plan: 'plan', outbounds: 'outbounds', upgrade: 'upgrade', google_status: 'google', public_egress_check: 'egress',
    security: 'security', management: 'management', traffic: 'traffic', removal: 'removal',
  })[key] || 'runtime';
  const fullEvent = value => ['created_at', 'revoked', 'management', 'outbounds', 'google_status', 'security', 'plan']
    .some(key => Object.hasOwn(value, key));
  async function fetchJSON(path, {fetcher, signal, optional = false, timeoutMs = 10000, unauthorized = () => {}}) {
    const controller = new AbortController();
    let rejectInterrupted, timer;
    const interrupted = new Promise((_, reject) => { rejectInterrupted = reject; });
    function interrupt(error) { controller.abort(); rejectInterrupted(error); }
    const cancel = () => interrupt(Object.assign(new Error('Read cancelled'), {name: 'AbortError'}));
    signal?.addEventListener('abort', cancel, {once: true});
    timer = setTimeout(() => interrupt(Object.assign(new Error('Read timed out'), {name: 'TimeoutError'})), timeoutMs);
    if (signal?.aborted) cancel();
    // Race also bounds response.json() and fetchers that ignore AbortSignal.
    const work = (async () => {
      if (controller.signal.aborted) throw Object.assign(new Error('Read cancelled'), {name: 'AbortError'});
      const response = await fetcher(path, {cache: 'no-store', signal: controller.signal});
      if (controller.signal.aborted) throw Object.assign(new Error('Read cancelled'), {name: 'AbortError'});
      if (response.status === 401) { unauthorized(); throw new Error('authentication required'); }
      if (optional && response.status === 204) return null;
      if (!response.ok) throw Object.assign(new Error(response.statusText), {status: response.status});
      return response.json();
    })();
    try { return await Promise.race([work, interrupted]); }
    finally { clearTimeout(timer); signal?.removeEventListener('abort', cancel); }
  }
  function create() {
    const agents = new Map(), entries = new Map(), revokedAgentIDs = new Set();
    let clock = 0, generation = 0, cycle = null;
    function entry(id) {
      if (!entries.has(id)) entries.set(id, {revision: ++clock, lifetime: 0, versions: new Map(), reads: new Map(), pending: new Map(), blocked: false, deleting: false});
      return entries.get(id);
    }
    const version = (e, group) => e.versions.get(group) || 0;
    function changed(e, groups) {
      for (const group of groups) e.versions.set(group, version(e, group) + 1);
      e.revision = ++clock;
    }
    function merge(id, patch, allowed) {
      const e = entry(id), current = agents.get(id) || {agent_id: id}, next = {...current}, groups = new Set();
      for (const [key, value] of Object.entries(patch)) {
        if (key === 'agent_id' || key === 'detail_loaded') continue;
        const group = resource(key);
        if (allowed(group)) {
          next[key] = value; groups.add(group);
        }
      }
      if (!groups.size) return false;
      if (patch.detail_loaded) next.detail_loaded = true;
      agents.set(id, next); changed(e, groups); return true;
    }
    function beginRefresh() {
      cycle?.controller.abort();
      const controller = new AbortController();
      cycle = {generation: ++generation, controller, start: clock,
        before: new Map([...entries].map(([id, e]) => [id, new Map(e.versions)]))};
      return cycle;
    }
    const validCycle = value => value === cycle && !value.controller.signal.aborted;
    function endRefresh(value) { if (value === cycle) cycle = null; }
    function read(id, context = null, groups = null) {
      const e = entry(id), token = {id, entry: e, lifetime: e.lifetime, start: e.revision, context,
        versions: new Map(e.versions), reads: new Map(), groups};
      // Per-resource latest invocation; an upgrade read cannot invalidate detail fields.
      const all = groups || ['runtime','name','lifecycle','country','plan','outbounds','google','egress','security','management','traffic','removal'];
      for (const group of all) {
        const next = (e.reads.get(group) || 0) + 1;
        e.reads.set(group, next); token.reads.set(group, next);
      }
      return token;
    }
    function validRead(token) {
      return entries.get(token.id) === token.entry && token.lifetime === token.entry.lifetime
        && !token.entry.blocked && (!token.context || validCycle(token.context));
    }
    function commitRead(token, patch) {
      if (!validRead(token) || !patch || (patch.agent_id && patch.agent_id !== token.id)) return false;
      if (patch.revoked) { missing(token); return false; }
      return merge(token.id, patch, group => token.reads.has(group)
        && token.entry.reads.get(group) === token.reads.get(group)
        && version(token.entry, group) === (token.versions.get(group) || 0)
        && !token.entry.pending.has(group));
    }
    function missing(token) {
      if (!validRead(token) || token.entry.revision !== token.start || token.entry.pending.size) return false;
      // A newer request can already be in flight without having changed revision.
      if ([...token.reads].some(([group, read]) => token.entry.reads.get(group) !== read)) return false;
      agents.delete(token.id); entries.delete(token.id); revokedAgentIDs.delete(token.id); return true;
    }
    function commitDetail(token, detail) {
      if (!detail) return false;
      // webAgentDetailView omitempty means absence is authoritative removal,
      // unlike a partial event, list summary, upgrade or mutation patch.
      return commitRead(token, {country_code: '', country_source: '', country_code_lookup_operation: null,
        removal: null, traffic: null, plan: null, ...detail});
    }
    function commitList(value, records) {
      if (!validCycle(value)) return false;
      const present = new Set();
      for (const record of records) {
        const id = record.agent_id; present.add(id);
        const e = entry(id);
        if (e.blocked || record.revoked) continue;
        const before = value.before.get(id) || new Map();
        merge(id, record, group => version(e, group) === (before.get(group) || 0) && !e.pending.has(group));
      }
      for (const [id, e] of entries) {
        if (!present.has(id) && e.revision <= value.start && !e.pending.size && !e.deleting) {
          agents.delete(id); entries.delete(id); revokedAgentIDs.delete(id);
        }
      }
      return true;
    }
    function event(update) {
      if (revokedAgentIDs.has(update.agent_id)) return false;
      // Full management snapshots have no server revision. Re-read rather than trust
      // potentially queued old fields. This also reflects other clients' writes.
      if (fullEvent(update) || !agents.has(update.agent_id)) return false;
      return merge(update.agent_id, update, group => group === 'runtime' && !entry(update.agent_id).pending.has(group));
    }
    function beginMutation(id, groups) {
      const e = entry(id);
      if (!agents.has(id) || e.blocked || groups.some(group => e.pending.has(group))) return null;
      const token = {id, entry: e, lifetime: e.lifetime, groups};
      changed(e, groups);
      for (const group of groups) e.pending.set(group, token);
      return token;
    }
    function finishMutation(token, patch = {}) {
      if (!token || entries.get(token.id) !== token.entry || token.entry.lifetime !== token.lifetime) return false;
      if (!token.groups.every(group => token.entry.pending.get(group) === token)) return false;
      for (const group of token.groups) token.entry.pending.delete(group);
      changed(token.entry, token.groups);
      if (token.entry.blocked || !agents.has(token.id)) return false;
      return Object.keys(patch).length ? merge(token.id, patch, group => token.groups.includes(group)) : true;
    }
    function beginDelete(id) {
      const e = entry(id);
      if (!agents.has(id) || e.blocked) return null;
      e.blocked = true; e.deleting = true; e.lifetime++; changed(e, ['lifecycle']); revokedAgentIDs.add(id);
      return {id, entry: e, lifetime: e.lifetime};
    }
    function finishDelete(token, success) {
      if (!token || entries.get(token.id) !== token.entry || token.lifetime !== token.entry.lifetime) return false;
      token.entry.deleting = false;
      token.entry.lifetime++; token.entry.pending.clear(); changed(token.entry, ['lifecycle']);
      if (success) agents.delete(token.id);
      else { token.entry.blocked = false; revokedAgentIDs.delete(token.id); }
      // Successful barriers retire on a subsequent authoritative absence list.
      // Old request tokens retain the old entry/lifetime and cannot resurrect it;
      // unknown SSE identities must pass a fresh active detail read.
      return true;
    }
    return {agents, revokedAgentIDs, beginRefresh, validCycle, endRefresh, read, commitRead, commitDetail, missing,
      commitList, event, fullEvent, beginMutation, finishMutation, beginDelete, finishDelete,
      busy: (id, group) => entries.get(id)?.pending.has(group) || false,
      pending: id => Boolean(entries.get(id)?.pending.size),
      blocked: id => entries.get(id)?.blocked || false, refreshing: () => cycle !== null};
  }
  function reconcile(store, read, changed, failed = () => {}) {
    const jobs = new Map();
    let active = 0, closed = false;
    function pump() {
      if (closed) return;
      for (const [id, job] of jobs) {
        if (active >= 8) break;
        if (job.running || !job.queued || store.blocked(id) || store.pending(id)) continue;
        job.running = true; job.queued = false; active++;
        const dirty = job.dirty, controller = new AbortController();
        job.controller = controller;
        Promise.resolve().then(() => read(id, controller.signal)).then(result => {
          if (closed || jobs.get(id) !== job || controller.signal.aborted) return;
          if (result) changed();
          job.queued = job.dirty !== dirty;
          job.failed = false;
        }).catch(error => {
          if (closed || jobs.get(id) !== job || controller.signal.aborted || error.name === 'AbortError') return;
          job.failed = true; job.queued = false; failed(error);
          // No immediate retry loop; a later full notification or periodic poll retries.
        }).finally(() => {
          active--; job.running = false;
          if (jobs.get(id) === job && !job.queued && !job.failed && !store.pending(id)) jobs.delete(id);
          pump();
        });
      }
    }
    function notify(id) {
      if (closed || store.blocked(id)) return;
      const job = jobs.get(id) || {dirty: 0, queued: false, running: false};
      job.dirty++; job.queued = true; jobs.set(id, job); pump();
    }
    function cancel(id) { const job = jobs.get(id); jobs.delete(id); job?.controller?.abort(); }
    return {notify, cancel, retry() {
      for (const job of jobs.values()) if (job.failed) job.queued = true;
      pump();
    }, close() { closed = true; for (const id of jobs.keys()) cancel(id); },
    size: () => jobs.size};
  }
  const api = {create, resource, fullEvent, reconcile, fetchJSON, compareStableVersions, upgradeReasonDisplay, upgradeButtonHint, updaterHealthReason, updaterTroubleshootingURL, appendUpgradeReason};
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.AgentState = api;
})(globalThis);
