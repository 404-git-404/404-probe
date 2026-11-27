/* Single-line unified status row (tags on left, fault badges on right) with +N overflow popovers. */
(function(root) {
  'use strict';
  const M = root.DashboardModel || (typeof require === 'function' ? require('./dashboard-model.js') : null);
  const observers = new WeakMap();
  const callbacks = new WeakMap();
  let activePopover = null;

  function ensureObserver(doc) {
    const win = doc.defaultView || root;
    if (!win.ResizeObserver) return null;
    let observer = observers.get(doc);
    if (!observer) {
      observer = new win.ResizeObserver(entries => {
        for (const entry of entries) {
          callbacks.get(entry.target)?.();
        }
      });
      observers.set(doc, observer);
    }
    return observer;
  }

  function el(doc, tag, className, text) {
    const node = doc.createElement(tag);
    if (className) node.className = className;
    if (text != null) node.textContent = text;
    return node;
  }

  function items(agent, {catalog, server, warnings, now} = {}) {
    const tags = [];
    if (agent?.server_local) {
      tags.push({kind: 'server', label: 'Server', tone: 'server'});
    }
    const catalogTags = catalog?.tags || [];
    for (const id of agent?.tag_ids || []) {
      const tag = catalogTags.find(t => t.id === id);
      if (tag) tags.push({kind: 'tag', id: tag.id, label: tag.name});
    }
    const faults = [];
    if (M) {
      for (const [label, tone] of M.systemBadges(agent, false)) {
        faults.push({kind: 'system', label, tone});
      }
      const networkWarn = warnings ? !!warnings.network : !!M.flags(agent, server, now).network;
      if (networkWarn) {
        faults.push({kind: 'network', label: '网络异常', tone: 'danger'});
      }
    }
    return {tags, faults};
  }

  function renderTagNode(doc, item) {
    const cls = item.kind === 'server' ? 'status-item status-tag system-pill' : 'status-item status-tag';
    const node = el(doc, 'span', cls, item.label);
    if (item.tone) node.dataset.tone = item.tone;
    node.title = item.label;
    return node;
  }

  function renderFaultNode(doc, item, variant, forPopover = false) {
    const extra = !forPopover && item.kind === 'network'
      ? (variant === 'list' ? ' banner-network-warning' : ' dashboard-network-warning')
      : '';
    const text = '⚠ ' + item.label;
    const node = el(doc, 'span', 'status-item status-fault system-pill' + extra, text);
    node.dataset.tone = item.tone || 'warning';
    if (item.kind === 'network') node.dataset.fault = 'network';
    node.title = text;
    return node;
  }

  function closePopover(restoreFocus = false) {
    if (!activePopover) return;
    const {popover, button, controller} = activePopover;
    activePopover = null;
    controller.abort();
    button.setAttribute('aria-expanded', 'false');
    popover.remove();
    if (restoreFocus && button.isConnected && !button.hidden) {
      button.focus({preventScroll: true});
    }
  }

  function openPopover(doc, button, side, list, variant) {
    if (activePopover && activePopover.button === button) {
      closePopover(false);
      return;
    }
    closePopover(false);
    const win = doc.defaultView || root;
    const popover = el(doc, 'div', 'status-popover');
    popover.setAttribute('role', 'dialog');
    popover.setAttribute('aria-label', side === 'tags' ? '全部标签' : '全部故障徽章');
    popover.dataset.side = side;
    popover.dataset.variant = variant;
    const heading = el(doc, 'div', 'status-popover-title', side === 'tags' ? '标签' : '故障状态');
    const body = el(doc, 'div', 'status-popover-items');
    for (const item of list) {
      body.append(side === 'tags' ? renderTagNode(doc, item) : renderFaultNode(doc, item, variant, true));
    }
    popover.append(heading, body);
    doc.body.append(popover);

    const anchor = button.getBoundingClientRect();
    const box = popover.getBoundingClientRect();
    const margin = 8, gap = 6;
    const vw = doc.documentElement.clientWidth || win.innerWidth;
    const vh = doc.documentElement.clientHeight || win.innerHeight;
    const preferredLeft = side === 'faults' ? anchor.right - box.width : anchor.left;
    const left = Math.max(margin, Math.min(preferredLeft, vw - box.width - margin));
    const below = anchor.bottom + gap;
    const above = anchor.top - gap - box.height;
    const top = below + box.height <= vh - margin
      ? below
      : Math.max(margin, above >= margin ? above : vh - box.height - margin);
    popover.style.left = left + 'px';
    popover.style.top = top + 'px';

    button.setAttribute('aria-expanded', 'true');
    const controller = new AbortController();
    const {signal} = controller;
    doc.addEventListener('pointerdown', event => {
      if (popover.contains(event.target) || button.contains(event.target)) return;
      closePopover(false);
    }, {capture: true, signal});
    doc.addEventListener('keydown', event => {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        closePopover(true);
      }
    }, {capture: true, signal});
    win.addEventListener('scroll', event => {
      if (event.target && popover.contains(event.target)) return;
      closePopover(false);
    }, {capture: true, passive: true, signal});
    win.addEventListener('resize', () => closePopover(false), {passive: true, signal});
    activePopover = {popover, button, controller, side};
  }

  function create({document: doc = root.document, variant = 'card'} = {}) {
    const node = el(doc, 'div', 'status-row ' + (variant === 'list' ? 'banner-system-badges' : 'dashboard-card-tags'));
    node.dataset.variant = variant;
    node.dataset.empty = 'true';
    const left = el(doc, 'div', 'status-row-side status-row-tags');
    const right = el(doc, 'div', 'status-row-side status-row-faults');
    const leftMore = el(doc, 'button', 'status-more status-more-tags');
    leftMore.type = 'button';
    leftMore.hidden = true;
    leftMore.setAttribute('aria-haspopup', 'dialog');
    leftMore.setAttribute('aria-expanded', 'false');
    const rightMore = el(doc, 'button', 'status-more status-more-faults');
    rightMore.type = 'button';
    rightMore.hidden = true;
    rightMore.setAttribute('aria-haspopup', 'dialog');
    rightMore.setAttribute('aria-expanded', 'false');
    node.append(left, right);

    let data = {tags: [], faults: []};
    let leftNodes = [], rightNodes = [];
    let signature = '';
    let frame = 0;

    leftMore.addEventListener('click', event => {
      event.stopPropagation();
      openPopover(doc, leftMore, 'tags', data.tags, variant);
    });
    rightMore.addEventListener('click', event => {
      event.stopPropagation();
      openPopover(doc, rightMore, 'faults', data.faults, variant);
    });

    function measureMore(btn, count) {
      btn.textContent = '+' + count;
      btn.hidden = false;
      const w = Math.ceil(btn.getBoundingClientRect().width);
      btn.hidden = true;
      return w;
    }

    function fit() {
      frame = 0;
      const width = Math.floor(node.clientWidth) - 1;
      if (width <= 0) return;
      const hasTags = leftNodes.length > 0;
      const hasFaults = rightNodes.length > 0;
      if (!hasTags && !hasFaults) {
        leftMore.hidden = true;
        rightMore.hidden = true;
        if (activePopover && (activePopover.button === leftMore || activePopover.button === rightMore)) {
          closePopover(false);
        }
        return;
      }

      const itemGap = variant === 'list' ? 3 : 4;
      const sideGap = hasTags && hasFaults ? (variant === 'list' ? 4 : 6) : 0;

      for (const item of leftNodes) { item.hidden = false; item.classList.remove('is-truncated'); }
      for (const item of rightNodes) { item.hidden = false; item.classList.remove('is-truncated'); }
      leftMore.hidden = true;
      rightMore.hidden = true;

      right.hidden = true;
      const lw = leftNodes.map(item => Math.ceil(item.getBoundingClientRect().width));
      const tagMoreWidths = new Map();
      const measureLeftMore = count => {
        if (!tagMoreWidths.has(count)) tagMoreWidths.set(count, measureMore(leftMore, count));
        return tagMoreWidths.get(count);
      };
      right.hidden = false;
      left.hidden = true;
      const rw = rightNodes.map(item => Math.ceil(item.getBoundingClientRect().width));
      const faultMoreWidths = new Map();
      const measureRightMore = count => {
        if (!faultMoreWidths.has(count)) faultMoreWidths.set(count, measureMore(rightMore, count));
        return faultMoreWidths.get(count);
      };
      left.hidden = false;

      // Priority: keep fault badges complete first; collapse tags before faults;
      // if faults alone overflow (or leave no room for the tag +N button), collapse trailing faults too.
      const fitWith = (widths, avail, measureBtn) => {
        const n = widths.length;
        if (!n) return {shown: 0, hidden: 0, used: 0};
        const total = widths.reduce((sum, w) => sum + w, 0) + itemGap * (n - 1);
        if (total <= avail) return {shown: n, hidden: 0, used: total};
        for (let k = n - 1; k >= 0; k--) {
          const hiddenCount = n - k;
          const mw = measureBtn(hiddenCount);
          let used = mw;
          if (k > 0) {
            for (let i = 0; i < k; i++) used += widths[i];
            used += itemGap * k;
          }
          if (used <= avail) return {shown: k, hidden: hiddenCount, used};
        }
        return {shown: 0, hidden: n, used: measureBtn(n)};
      };

      right.hidden = true;
      const minTagMore = hasTags ? measureLeftMore(lw.length) : 0;
      right.hidden = false;
      left.hidden = true;
      let rFit = fitWith(rw, width, measureRightMore);
      let lFit = {shown: 0, hidden: 0, used: 0};
      if (hasTags) {
        let availL = hasFaults ? Math.max(0, width - rFit.used - sideGap) : width;
        if (hasFaults && lw.length > 0 && availL < minTagMore && width > minTagMore + sideGap) {
          rFit = fitWith(rw, Math.max(0, width - minTagMore - sideGap), measureRightMore);
          availL = Math.max(0, width - rFit.used - sideGap);
        }
        left.hidden = false;
        right.hidden = true;
        if (!hasFaults && lw.length === 1 && lw[0] > width) {
          lFit = {shown: 1, hidden: 0, used: width};
          leftNodes[0].classList.add('is-truncated');
        } else {
          lFit = fitWith(lw, availL, measureLeftMore);
        }
        right.hidden = false;
      } else {
        left.hidden = false;
      }

      for (let i = 0; i < leftNodes.length; i++) leftNodes[i].hidden = i >= lFit.shown;
      if (lFit.hidden > 0) {
        leftMore.textContent = '+' + lFit.hidden;
        leftMore.hidden = false;
        leftMore.setAttribute('aria-label', '查看全部 ' + leftNodes.length + ' 个标签');
      } else {
        leftMore.hidden = true;
        if (activePopover?.button === leftMore) closePopover(false);
      }

      for (let i = 0; i < rightNodes.length; i++) rightNodes[i].hidden = i >= rFit.shown;
      if (rFit.hidden > 0) {
        const hiddenFaults = data.faults.slice(rFit.shown);
        rightMore.dataset.tone = hiddenFaults.some(f => f.tone === 'danger') ? 'danger' : 'warning';
        rightMore.textContent = '+' + rFit.hidden;
        rightMore.hidden = false;
        rightMore.setAttribute('aria-label', '查看全部 ' + rightNodes.length + ' 个故障徽章');
      } else {
        rightMore.hidden = true;
        if (activePopover?.button === rightMore) closePopover(false);
      }
    }

    function scheduleFit() {
      const win = doc.defaultView || root;
      if (node.clientWidth > 0) fit();
      if (!frame && win.requestAnimationFrame) {
        frame = win.requestAnimationFrame(fit);
      }
    }

    const observer = ensureObserver(doc);
    if (observer) {
      callbacks.set(node, fit);
      observer.observe(node);
    }

    function update(next) {
      const nextSig = JSON.stringify(next);
      if (nextSig !== signature) {
        signature = nextSig;
        data = {tags: next.tags || [], faults: next.faults || []};
        leftNodes = data.tags.map(item => renderTagNode(doc, item));
        rightNodes = data.faults.map(item => renderFaultNode(doc, item, variant, false));
        left.replaceChildren(...leftNodes, leftMore);
        right.replaceChildren(...rightNodes, rightMore);
        node.dataset.empty = String(!leftNodes.length && !rightNodes.length);
        node.dataset.signature = signature;
        if (activePopover && (activePopover.button === leftMore || activePopover.button === rightMore)) {
          closePopover(false);
        }
      }
      scheduleFit();
    }

    function destroy() {
      const win = doc.defaultView || root;
      if (frame && win.cancelAnimationFrame) win.cancelAnimationFrame(frame);
      frame = 0;
      if (activePopover && (activePopover.button === leftMore || activePopover.button === rightMore)) {
        closePopover(false);
      }
      callbacks.delete(node);
      observers.get(doc)?.unobserve(node);
    }

    return {node, update, fit, destroy};
  }

  const api = {items, create, closePopover};
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.StatusRow = api;
})(globalThis);
