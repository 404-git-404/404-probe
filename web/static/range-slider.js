/* Two-handle range slider over a time axis (milliseconds). No dependencies; reused by the card (24 hours) and,
   later, by the device details page. Markup is built here, colours and sizes come from --rs-* variables. */
(function(root) {
  'use strict';
  const clamp = (value, low, high) => Math.min(high, Math.max(low, value));
  const STATES = new Set(['yellow', 'red']);

  function create({document: doc = root.document, min, max, start, end, minSpan = 600000, step = 60000, follow = false,
    labels = {}, format = value => new Date(value).toLocaleString(), marks = [], onChange = () => {},
    raf = fn => (root.requestAnimationFrame ? root.requestAnimationFrame(fn) : root.setTimeout(fn, 16)),
    cancelRaf = id => (root.cancelAnimationFrame ? root.cancelAnimationFrame(id) : root.clearTimeout(id))} = {}) {
    const el = (tag, className) => { const node = doc.createElement(tag); if (className) node.className = className; return node; };
    const state = {min, max, start, end, follow: !!follow, marks: []};
    const element = el('div', 'rs');
    element.dataset.noFlip = 'true';
    const track = el('div', 'rs-track'), markLayer = el('div', 'rs-marks'), selection = el('div', 'rs-selection');
    const thumbs = {start: el('span', 'rs-thumb rs-thumb-start'), end: el('span', 'rs-thumb rs-thumb-end')};
    for (const [name, thumb] of Object.entries(thumbs)) {
      thumb.setAttribute('role', 'slider');
      thumb.tabIndex = 0;
      thumb.dataset.thumb = name;
      thumb.setAttribute('aria-orientation', 'horizontal');
      thumb.setAttribute('aria-label', name === 'start' ? (labels.start || '开始时间') : (labels.end || '结束时间'));
    }
    track.append(markLayer, selection, thumbs.start, thumbs.end);
    element.append(track);
    // A drag on the slider must never be taken for a click on the module around it.
    for (const type of ['click', 'pointerdown']) element.addEventListener(type, event => event.stopPropagation());

    const span = () => Math.max(1, state.max - state.min);
    const pct = value => ((clamp(value, state.min, state.max) - state.min) / span()) * 100;
    const snapTo = value => Math.round(value / step) * step;
    // Dragging a handle within about six pixels of "now" sticks to it; the keyboard has its own key (End) for that.
    const snapMS = () => Math.max(step, (span() / Math.max(1, track.getBoundingClientRect().width)) * 6);
    const emitState = {pending: null, frame: null, source: 'api'};

    // Keeps minSpan, stays inside the bounds and, when told which handle moved, gives way on the other side.
    function normalise(next, moved) {
      let {start: s, end: e} = next;
      const room = Math.min(minSpan, span());
      if (moved === 'start') { s = clamp(s, state.min, state.max - room); e = Math.max(e, s + room); }
      else if (moved === 'end') { e = clamp(e, state.min + room, state.max); s = Math.min(s, e - room); }
      else { const length = clamp(e - s, room, span()); s = clamp(s, state.min, state.max - length); e = s + length; }
      s = clamp(s, state.min, state.max - room); e = clamp(e, s + room, state.max);
      return {start: s, end: e};
    }
    function paint() {
      const left = pct(state.start), right = pct(state.end);
      selection.style.left = left + '%';
      selection.style.width = Math.max(0, right - left) + '%';
      thumbs.start.style.left = left + '%';
      thumbs.end.style.left = right + '%';
      const room = Math.min(minSpan, span());
      for (const [name, thumb] of Object.entries(thumbs)) {
        const value = name === 'start' ? state.start : state.end;
        thumb.setAttribute('aria-valuemin', String(Math.round(name === 'start' ? state.min : state.start + room)));
        thumb.setAttribute('aria-valuemax', String(Math.round(name === 'start' ? state.end - room : state.max)));
        thumb.setAttribute('aria-valuenow', String(Math.round(value)));
        thumb.setAttribute('aria-valuetext', format(value) + (name === 'end' && state.follow ? '（跟随现在）' : ''));
      }
      element.dataset.follow = String(state.follow);
    }
    function paintMarks() {
      markLayer.replaceChildren();
      for (const mark of state.marks) {
        const from = clamp(mark.start, state.min, state.max), to = clamp(mark.end, state.min, state.max);
        if (to <= from) continue;
        const bar = el('span', 'rs-mark rs-mark-' + mark.state);
        bar.style.left = pct(from) + '%';
        bar.style.width = Math.max(0.4, pct(to) - pct(from)) + '%';
        markLayer.append(bar);
      }
    }
    function flush(committed) {
      if (emitState.frame !== null) { cancelRaf(emitState.frame); emitState.frame = null; }
      const source = emitState.source;
      emitState.pending = null;
      onChange({start: state.start, end: state.end}, {follow: state.follow, source, committed});
    }
    // Dragging is throttled to one notification per frame; the last position of a drag is always delivered.
    function notify(source, committed) {
      emitState.source = source;
      if (committed) { flush(true); return; }
      if (emitState.frame !== null) return;
      emitState.frame = raf(() => { emitState.frame = null; flush(false); });
    }
    function apply(next, moved, source, committed) {
      const range = normalise(next, moved);
      let follow = state.follow;
      if (moved === 'end' || moved === 'move') follow = range.end >= state.max || (source === 'pointer' && state.max - range.end < snapMS());
      else if (moved === 'start') follow = state.follow && range.end >= state.max;
      if (follow && (moved === 'end' || moved === 'move')) { const length = range.end - range.start; range.end = state.max; range.start = Math.max(state.min, state.max - length); }
      const changed = range.start !== state.start || range.end !== state.end || follow !== state.follow;
      state.start = range.start; state.end = range.end; state.follow = follow;
      paint();
      if (changed || committed) notify(source, committed);
    }

    // ---- pointer ----
    let drag = null;
    const msPerPixel = () => span() / Math.max(1, track.getBoundingClientRect().width);
    function down(kind, event) {
      if (event.button !== undefined && event.button !== 0) return;
      drag = {kind, id: event.pointerId, x: event.clientX, start: state.start, end: state.end};
      try { (kind === 'move' ? selection : thumbs[kind]).setPointerCapture?.(event.pointerId); } catch (_) { /* a synthetic pointer */ }
      event.preventDefault?.();
      if (kind !== 'move') thumbs[kind].focus?.({preventScroll: true});
    }
    function move(event) {
      if (!drag || drag.id !== event.pointerId) return;
      const delta = (event.clientX - drag.x) * msPerPixel();
      if (drag.kind === 'start') apply({start: snapTo(drag.start + delta), end: state.end}, 'start', 'pointer', false);
      else if (drag.kind === 'end') apply({start: state.start, end: snapTo(drag.end + delta)}, 'end', 'pointer', false);
      else { const length = drag.end - drag.start, nextStart = snapTo(drag.start + delta); apply({start: nextStart, end: nextStart + length}, 'move', 'pointer', false); }
    }
    function up(event) {
      if (!drag || drag.id !== event.pointerId) return;
      drag = null;
      notify('pointer', true);
    }
    thumbs.start.addEventListener('pointerdown', event => down('start', event));
    thumbs.end.addEventListener('pointerdown', event => down('end', event));
    selection.addEventListener('pointerdown', event => down('move', event));
    for (const node of [thumbs.start, thumbs.end, selection]) {
      node.addEventListener('pointermove', move);
      node.addEventListener('pointerup', up);
      node.addEventListener('pointercancel', up);
    }
    // A press on the empty track moves the nearer handle there.
    track.addEventListener('pointerdown', event => {
      if (event.target !== track && event.target !== markLayer) return;
      const rect = track.getBoundingClientRect(), at = snapTo(state.min + clamp((event.clientX - rect.left) / Math.max(1, rect.width), 0, 1) * span());
      const nearer = Math.abs(at - state.start) <= Math.abs(at - state.end) ? 'start' : 'end';
      apply(nearer === 'start' ? {start: at, end: state.end} : {start: state.start, end: at}, nearer, 'pointer', true);
    });

    // ---- keyboard ----
    function key(name, event) {
      const room = Math.min(minSpan, span()), amount = (event.shiftKey ? 10 : 1) * step;
      let next = null;
      const value = name === 'start' ? state.start : state.end;
      if (event.key === 'ArrowLeft' || event.key === 'ArrowDown') next = value - amount;
      else if (event.key === 'ArrowRight' || event.key === 'ArrowUp') next = value + amount;
      else if (event.key === 'Home') next = name === 'start' ? state.min : state.start + room;
      else if (event.key === 'End') next = name === 'start' ? state.end - room : state.max;
      if (next === null) return;
      event.preventDefault();
      apply(name === 'start' ? {start: next, end: state.end} : {start: state.start, end: next}, name, 'keyboard', true);
    }
    thumbs.start.addEventListener('keydown', event => key('start', event));
    thumbs.end.addEventListener('keydown', event => key('end', event));

    const range = normalise({start: state.start, end: state.end}, null);
    state.start = range.start; state.end = range.end;
    state.marks = (marks || []).filter(mark => STATES.has(mark.state));
    paint(); paintMarks();
    return {
      element,
      // Time moves on (max grows) or the range of the data changes. Following keeps the end on max.
      setBounds(nextMin, nextMax) {
        const length = state.end - state.start, wasFollowing = state.follow;
        state.min = nextMin; state.max = Math.max(nextMax, nextMin);
        const target = wasFollowing ? {start: state.max - length, end: state.max} : {start: state.start, end: state.end};
        const next = normalise(target, null);
        state.start = next.start; state.end = next.end;
        if (wasFollowing) state.end = state.max;
        paint(); paintMarks();
      },
      setRange(nextStart, nextEnd, options = {}) {
        const next = normalise({start: nextStart, end: nextEnd}, null);
        state.start = next.start; state.end = next.end;
        state.follow = options.follow !== undefined ? !!options.follow : state.end >= state.max;
        if (state.follow) state.end = state.max;
        paint();
      },
      setMarks(next) { state.marks = (next || []).filter(mark => STATES.has(mark.state)); paintMarks(); },
      getRange: () => ({start: state.start, end: state.end, follow: state.follow, min: state.min, max: state.max}),
      destroy() {
        if (emitState.frame !== null) cancelRaf(emitState.frame);
        emitState.frame = null; drag = null; element.remove();
      },
    };
  }

  const api = {create};
  if (typeof module === 'object' && module.exports) module.exports = api; else root.RangeSlider = api;
})(globalThis);
