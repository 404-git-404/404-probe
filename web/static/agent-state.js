/* Arrival/request barriers for the actual unversioned Web DTO. No wall-clock ordering. */
(function (root) {
  'use strict';
  const resource = key => ({
    name: 'name', revoked: 'lifecycle', disabled_at: 'lifecycle', created_at: 'lifecycle',
    country_code: 'country', country_source: 'country', country_code_lookup_operation: 'country',
    plan: 'plan', outbounds: 'outbounds', upgrade: 'upgrade', google_status: 'google',
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
        if (allowed(group)) { next[key] = value; groups.add(group); }
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
      const all = groups || ['runtime','name','lifecycle','country','plan','outbounds','google','security','management','traffic','removal'];
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
  const api = {create, resource, fullEvent, reconcile, fetchJSON};
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.AgentState = api;
})(globalThis);
