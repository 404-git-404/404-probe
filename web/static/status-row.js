/* Single-line unified status row (colored tags on left, interactive fault badges on right) with hover reasons and popovers. */
(function(root) {
  'use strict';
  const M = root.DashboardModel || (typeof require === 'function' ? require('./dashboard-model.js') : null);
  const VALID_TAG_COLORS = new Set(['blue', 'sky', 'cyan', 'teal', 'indigo', 'purple', 'pink', 'gray']);
  const KNOWN_KINDS = new Set(['certificate', 'service', 'service_restart', 'time', 'dns']);
  const NETWORK_DESC = '这台机器有线路在持续丢包或延迟超过门槛';
  const IGNORE_FOOTER_NOTE = '忽略后这一项不再提醒，可在 详情 → 系统 里取消。';
  const observers = new WeakMap();
  const callbacks = new WeakMap();
  const copyTimers = new WeakMap();
  let activePopover = null;
  let activeTooltip = null;
  let rowSeq = 0;

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

  function formatHoverReason(list, fallback) {
    if (!Array.isArray(list) || !list.length) return fallback || '';
    const first = list[0]?.detail || fallback || '';
    return list.length > 1 ? `${first} 等 ${list.length} 项` : first;
  }

  function shellArg(value) {
    const s = String(value ?? '');
    if (s.length > 0 && /^[A-Za-z0-9_@%+=:,./-]+$/.test(s)) return s;
    return "'" + s.replaceAll("'", "'\\''") + "'";
  }

  function remediationFor(kind, itemValue) {
    const v = shellArg(itemValue);
    switch (kind) {
      case 'service':
        return {
          steps: [
            {label: '查看原因：', commands: [`systemctl status ${v} --no-pager`, `journalctl -u ${v} -n 50 --no-pager`]},
            {label: '修好后清除失败状态：', commands: [`systemctl reset-failed ${v}`]},
            {label: '不需要这个服务：', commands: [`systemctl disable --now ${v}`]},
          ],
          note: '',
        };
      case 'service_restart':
        return {
          steps: [
            {label: '查看最近日志：', commands: [`journalctl -u ${v} -n 100 --no-pager`]},
            {label: '查看状态：', commands: [`systemctl status ${v} --no-pager`]},
          ],
          note: '',
        };
      case 'certificate':
        return {
          steps: [
            {label: '查看到期时间：', commands: [`openssl x509 -noout -enddate -in ${v}`]},
          ],
          note: '续期并替换这个文件；不再使用就删除它',
        };
      case 'time':
        return {
          steps: [
            {label: '查看状态：', commands: ['timedatectl status']},
            {label: '重启时间同步：', commands: ['systemctl restart systemd-timesyncd']},
            {label: '用 chrony 的机器：', commands: ['systemctl restart chrony']},
          ],
          note: '',
        };
      case 'dns':
        return {
          steps: [
            {label: '直接测试：', commands: [`dig @${v} cloudflare.com +short`]},
            {label: '查看谁在监听 53 端口：', commands: ["ss -lunpt 'sport = :53'"]},
          ],
          note: '重启该 DNS 服务（如 smartdns）后再观察',
        };
      default:
        return null;
    }
  }

  function items(agent, {catalog, server, warnings, now, score} = {}) {
    const scoreModel = score !== undefined ? score : root.DeviceScores?.forAgent?.(agent);
    const highValue = root.DeviceScores?.isHighValue
      ? root.DeviceScores.isHighValue(scoreModel)
      : Boolean(scoreModel?.high_value === true && typeof scoreModel?.overall?.score === 'number');
    let medal = null;
    if (highValue) {
      const tip = root.DeviceScores?.valueMedalTip
        ? root.DeviceScores.valueMedalTip(scoreModel)
        : (scoreModel.value_rank > 0 && scoreModel.value_count > 0
          ? '高性价比 · 第 ' + scoreModel.value_rank + ' / ' + scoreModel.value_count + ' 名'
          : '高性价比');
      medal = {title: tip};
    }
    const tags = [];
    if (agent?.server_local) {
      tags.push({kind: 'server', label: 'Server', tone: 'server'});
    }
    const catalogTags = catalog?.tags || [];
    for (const id of agent?.tag_ids || []) {
      const tag = catalogTags.find(t => t.id === id);
      if (tag) {
        const color = typeof tag.color === 'string' && VALID_TAG_COLORS.has(tag.color) ? tag.color : '';
        tags.push({kind: 'tag', id: tag.id, label: tag.name, color});
      }
    }
    const faults = [];
    const agentId = agent?.agent_id || '';
    const agentName = agent?.name || agent?.state?.hostname || agentId || '未命名设备';
    if (agent?.online && !agent?.disabled_at) {
      const attention = agent.system_attention || {};
      const byKind = new Map();
      for (const entry of attention.items || []) {
        if (!entry?.kind) continue;
        if (!byKind.has(entry.kind)) byKind.set(entry.kind, []);
        byKind.get(entry.kind).push({
          kind: entry.kind,
          item: entry.item,
          level: entry.level || 'warning',
          detail: entry.detail || '',
        });
      }
      const maxLevel = k => (byKind.get(k) || []).some(i => i.level === 'danger') ? 'danger' : byKind.has(k) ? 'warning' : '';
      const pushSystem = (attentionKind, label, tone) => {
        const list = byKind.get(attentionKind) || [];
        faults.push({
          kind: 'system',
          attentionKind,
          label,
          tone,
          agentId,
          agentName,
          items: list,
          reason: formatHoverReason(list, '⚠ ' + label),
        });
      };
      const certLevel = attention.certificate || maxLevel('certificate');
      if (certLevel === 'danger') pushSystem('certificate', '证书已过期', 'danger');
      else if (certLevel === 'warning') pushSystem('certificate', '证书将到期', 'warning');
      if (attention.service || byKind.has('service')) pushSystem('service', '服务异常', 'danger');
      const restartLevel = attention.service_restart || maxLevel('service_restart');
      if (restartLevel === 'danger') pushSystem('service_restart', '服务反复重启', 'danger');
      else if (restartLevel === 'warning') pushSystem('service_restart', '服务偶发重启', 'warning');
      if (attention.time || byKind.has('time')) pushSystem('time', '时间未同步', 'warning');
      if (attention.dns || byKind.has('dns')) pushSystem('dns', 'DNS 异常', 'danger');
      for (const [unknownKind] of byKind) {
        if (KNOWN_KINDS.has(unknownKind)) continue;
        pushSystem(unknownKind, unknownKind + ' 异常', maxLevel(unknownKind) || 'warning');
      }
    }
    if (M) {
      const networkWarn = warnings ? !!warnings.network : !!M.flags(agent, server, now).network;
      if (networkWarn) {
        faults.push({
          kind: 'network',
          attentionKind: 'network',
          label: '网络异常',
          tone: 'danger',
          agentId,
          agentName,
          items: [],
          reason: NETWORK_DESC,
        });
      }
    }
    return {medal, tags, faults};
  }

  function hideTooltip() {
    if (!activeTooltip) return;
    activeTooltip.node.remove();
    activeTooltip = null;
  }

  function showTooltip(doc, target, text) {
    if (!text) return;
    if (activePopover && activePopover.button === target) return;
    hideTooltip();
    const win = doc.defaultView || root;
    const tip = el(doc, 'div', 'status-fault-tip', text);
    tip.setAttribute('role', 'tooltip');
    doc.body.append(tip);
    const anchor = target.getBoundingClientRect();
    const box = tip.getBoundingClientRect();
    const margin = 8, gap = 6;
    const vw = doc.documentElement.clientWidth || win.innerWidth;
    const vh = doc.documentElement.clientHeight || win.innerHeight;
    const left = Math.max(margin, Math.min(anchor.left + (anchor.width - box.width) / 2, vw - box.width - margin));
    const above = anchor.top - gap - box.height;
    const below = anchor.bottom + gap;
    const top = above >= margin ? above : Math.min(below, vh - box.height - margin);
    tip.style.left = left + 'px';
    tip.style.top = Math.max(margin, top) + 'px';
    activeTooltip = {node: tip, target};
  }

  function positionPopover(doc, popover, anchorRect, alignRight) {
    const win = doc.defaultView || root;
    const box = popover.getBoundingClientRect();
    const margin = 8, gap = 6;
    const vw = doc.documentElement.clientWidth || win.innerWidth;
    const vh = doc.documentElement.clientHeight || win.innerHeight;
    const preferredLeft = alignRight ? anchorRect.right - box.width : anchorRect.left;
    const left = Math.max(margin, Math.min(preferredLeft, vw - box.width - margin));
    const below = anchorRect.bottom + gap;
    const above = anchorRect.top - gap - box.height;
    const top = below + box.height <= vh - margin
      ? below
      : Math.max(margin, above >= margin ? above : vh - box.height - margin);
    popover.style.left = left + 'px';
    popover.style.top = top + 'px';
  }

  function closePopover(restoreFocus = false) {
    hideTooltip();
    if (!activePopover) return;
    const {popover, button, controller} = activePopover;
    activePopover = null;
    controller.abort();
    if (button) button.setAttribute('aria-expanded', 'false');
    popover.remove();
    if (restoreFocus && button && button.isConnected && !button.hidden) {
      button.focus({preventScroll: true});
    }
  }

  function bindPopoverDismiss(doc, popover) {
    const win = doc.defaultView || root;
    const controller = new AbortController();
    const {signal} = controller;
    doc.addEventListener('pointerdown', event => {
      const btn = activePopover?.button;
      if (popover.contains(event.target) || (btn && btn.contains(event.target))) return;
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
    return controller;
  }

  function copyWithFeedback(doc, button, text) {
    const win = doc.defaultView || root;
    const prev = copyTimers.get(button);
    const baseLabel = prev?.label || button.textContent || '复制';
    if (prev) win.clearTimeout(prev.timer);
    const finish = ok => {
      button.textContent = ok ? '已复制' : '复制失败';
      const timer = win.setTimeout(() => {
        button.textContent = baseLabel;
        copyTimers.delete(button);
      }, 1500);
      copyTimers.set(button, {label: baseLabel, timer});
    };
    const clipboard = win.navigator?.clipboard;
    if (!clipboard?.writeText) {
      finish(false);
      return;
    }
    Promise.resolve(clipboard.writeText(text)).then(() => finish(true), () => finish(false));
  }

  function renderTagNode(doc, item) {
    const cls = item.kind === 'server' ? 'status-item status-tag system-pill' : 'status-item status-tag';
    const node = el(doc, 'span', cls, item.label);
    if (item.tone) node.dataset.tone = item.tone;
    if (item.kind === 'tag' && item.color) node.dataset.tagColor = item.color;
    node.title = item.label;
    return node;
  }

  function openFaultPopover(doc, button, fault, variant, actions, rowId, fallbackAnchorRect = null) {
    hideTooltip();
    if (
      activePopover &&
      activePopover.type === 'fault' &&
      activePopover.rowId === rowId &&
      activePopover.attentionKind === fault.attentionKind
    ) {
      closePopover(false);
      return;
    }
    const anchorRect = button && button.isConnected ? button.getBoundingClientRect() : fallbackAnchorRect;
    closePopover(false);
    const popover = el(doc, 'div', 'status-popover status-fault-popover');
    popover.setAttribute('role', 'dialog');
    popover.setAttribute('aria-label', `${fault.label} · ${fault.agentName}`);
    popover.dataset.side = 'fault-detail';
    popover.dataset.faultKind = fault.attentionKind || fault.kind;
    popover.dataset.variant = variant;

    const head = el(doc, 'div', 'status-fault-popover-head');
    const titleEl = el(doc, 'span', 'status-fault-popover-title', fault.label);
    const deviceEl = el(doc, 'span', 'status-fault-popover-device', fault.agentName);
    head.append(titleEl, deviceEl);
    popover.append(head);

    if (fault.kind === 'network') {
      const body = el(doc, 'div', 'fault-network-body');
      body.append(el(doc, 'p', 'fault-network-desc', NETWORK_DESC));
      const jump = el(doc, 'button', 'fault-network-action', '查看网络状态');
      jump.type = 'button';
      jump.addEventListener('click', event => {
        event.stopPropagation();
        closePopover(false);
        if (actions?.onNavigateNetwork) {
          actions.onNavigateNetwork(fault.agentId);
        } else {
          const win = doc.defaultView || root;
          win.location.hash = '#network/' + encodeURIComponent(fault.agentId);
        }
      });
      body.append(jump);
      popover.append(body);
    } else {
      const listEl = el(doc, 'div', 'fault-popover-list');
      const entries = fault.items || [];
      if (!entries.length) {
        listEl.append(el(doc, 'p', 'fault-remediation-note', '暂无详细告警项目。'));
      }
      for (const entry of entries) {
        const card = el(doc, 'div', 'fault-popover-item');
        card.dataset.kind = entry.kind;
        card.dataset.item = entry.item;

        const rowHead = el(doc, 'div', 'fault-item-head');
        const detail = el(doc, 'div', 'fault-item-detail', entry.detail || entry.item);
        const ignoreBtn = el(doc, 'button', 'fault-ignore-btn', '忽略');
        ignoreBtn.type = 'button';
        ignoreBtn.setAttribute('aria-label', `忽略 ${entry.item}`);
        rowHead.append(detail, ignoreBtn);

        const errorEl = el(doc, 'p', 'fault-item-error', '');
        errorEl.setAttribute('role', 'alert');

        ignoreBtn.addEventListener('click', async event => {
          event.stopPropagation();
          if (ignoreBtn.disabled) return;
          ignoreBtn.disabled = true;
          errorEl.textContent = '';
          try {
            if (!actions?.onIgnore) throw new Error('暂不支持忽略操作');
            await actions.onIgnore(fault.agentId, entry.kind, entry.item);
            card.remove();
            if (popover.isConnected && listEl.children.length === 0) {
              closePopover(false);
            }
          } catch (err) {
            ignoreBtn.disabled = false;
            errorEl.textContent = err?.status === 400
              ? '忽略失败，请检查忽略条数或项目格式'
              : '忽略失败，请重试';
          }
        });

        card.append(rowHead, errorEl);

        const guide = remediationFor(entry.kind, entry.item);
        if (guide) {
          const rem = el(doc, 'div', 'fault-item-remediation');
          rem.append(el(doc, 'div', 'fault-remediation-heading', '怎么处理'));
          for (const step of guide.steps) {
            const stepBox = el(doc, 'div', 'fault-remediation-step');
            stepBox.append(el(doc, 'span', 'fault-remediation-label', step.label));
            for (const cmd of step.commands) {
              const cmdRow = el(doc, 'div', 'fault-cmd-row');
              const code = el(doc, 'code', '', cmd);
              const copyBtn = el(doc, 'button', 'fault-cmd-copy', '复制');
              copyBtn.type = 'button';
              copyBtn.setAttribute('aria-label', '复制命令 ' + cmd);
              copyBtn.addEventListener('click', e => {
                e.stopPropagation();
                copyWithFeedback(doc, copyBtn, cmd);
              });
              cmdRow.append(code, copyBtn);
              stepBox.append(cmdRow);
            }
            rem.append(stepBox);
          }
          if (guide.note) {
            rem.append(el(doc, 'p', 'fault-remediation-note', guide.note));
          }
          card.append(rem);
        }

        listEl.append(card);
      }
      popover.append(listEl);
      popover.append(el(doc, 'p', 'fault-popover-footer', IGNORE_FOOTER_NOTE));
    }

    doc.body.append(popover);
    if (anchorRect) positionPopover(doc, popover, anchorRect, true);

    const anchorButton = button && button.isConnected ? button : null;
    if (anchorButton) anchorButton.setAttribute('aria-expanded', 'true');
    const controller = bindPopoverDismiss(doc, popover, anchorButton);
    activePopover = {
      type: 'fault',
      popover,
      button: anchorButton,
      controller,
      side: 'fault-detail',
      rowId,
      attentionKind: fault.attentionKind || fault.kind,
      titleEl,
    };
  }

  function renderFaultNode(doc, item, variant, {forPopover = false, actions = null, rowId = 0, fallbackAnchor = null} = {}) {
    const extra = !forPopover && item.kind === 'network'
      ? (variant === 'list' ? ' banner-network-warning' : ' dashboard-network-warning')
      : '';
    const text = '⚠ ' + item.label;
    const node = el(doc, 'button', 'status-item status-fault system-pill' + extra, text);
    node.type = 'button';
    node.dataset.tone = item.tone || 'warning';
    node.dataset.faultKind = item.attentionKind || item.kind;
    if (item.kind === 'network') node.dataset.fault = 'network';
    if (item.reason) node.dataset.reason = item.reason;
    node.setAttribute('aria-haspopup', 'dialog');
    node.setAttribute('aria-expanded', 'false');
    node.setAttribute('aria-label', item.reason ? `${text}：${item.reason}` : text);
    node.addEventListener('pointerenter', () => showTooltip(doc, node, item.reason));
    node.addEventListener('pointerleave', () => {
      if (activeTooltip?.target === node) hideTooltip();
    });
    node.addEventListener('focus', () => showTooltip(doc, node, item.reason));
    node.addEventListener('blur', () => {
      if (activeTooltip?.target === node) hideTooltip();
    });
    node.addEventListener('click', event => {
      event.stopPropagation();
      const rect = node.getBoundingClientRect();
      const targetBtn = forPopover ? fallbackAnchor : node;
      openFaultPopover(doc, targetBtn, item, variant, actions, rowId, rect);
    });
    return node;
  }

  function openOverflowPopover(doc, button, side, list, variant, actions, rowId) {
    hideTooltip();
    if (activePopover && activePopover.type === 'overflow' && activePopover.button === button) {
      closePopover(false);
      return;
    }
    closePopover(false);
    const popover = el(doc, 'div', 'status-popover');
    popover.setAttribute('role', 'dialog');
    popover.setAttribute('aria-label', side === 'tags' ? '全部标签' : '全部故障徽章');
    popover.dataset.side = side;
    popover.dataset.variant = variant;
    const heading = el(doc, 'div', 'status-popover-title', side === 'tags' ? '标签' : '故障状态');
    const body = el(doc, 'div', 'status-popover-items');
    for (const item of list) {
      body.append(
        side === 'tags'
          ? renderTagNode(doc, item)
          : renderFaultNode(doc, item, variant, {forPopover: true, actions, rowId, fallbackAnchor: button})
      );
    }
    popover.append(heading, body);
    doc.body.append(popover);
    positionPopover(doc, popover, button.getBoundingClientRect(), side === 'faults');

    button.setAttribute('aria-expanded', 'true');
    const controller = bindPopoverDismiss(doc, popover, button);
    activePopover = {type: 'overflow', popover, button, controller, side, rowId};
  }

  function createMedalNode(doc) {
    const ns = 'http://www.w3.org/2000/svg';
    const node = el(doc, 'span', 'value-medal');
    node.hidden = true;
    node.setAttribute('role', 'img');
    const svg = doc.createElementNS(ns, 'svg');
    svg.setAttribute('viewBox', '0 0 16 16');
    svg.setAttribute('width', '14');
    svg.setAttribute('height', '14');
    svg.setAttribute('aria-hidden', 'true');
    const leftRibbon = doc.createElementNS(ns, 'path');
    leftRibbon.setAttribute('class', 'value-medal-ribbon');
    leftRibbon.setAttribute('d', 'M5.2 9.2L3.4 15.2l2.5-1.1 1.5 1.4.8-5.1Z');
    const rightRibbon = doc.createElementNS(ns, 'path');
    rightRibbon.setAttribute('class', 'value-medal-ribbon');
    rightRibbon.setAttribute('d', 'M10.8 9.2l1.8 6-2.5-1.1-1.5 1.4-.8-5.1Z');
    const disc = doc.createElementNS(ns, 'circle');
    disc.setAttribute('class', 'value-medal-disc');
    disc.setAttribute('cx', '8');
    disc.setAttribute('cy', '6');
    disc.setAttribute('r', '4.6');
    const ring = doc.createElementNS(ns, 'circle');
    ring.setAttribute('class', 'value-medal-ring');
    ring.setAttribute('cx', '8');
    ring.setAttribute('cy', '6');
    ring.setAttribute('r', '3.1');
    const star = doc.createElementNS(ns, 'path');
    star.setAttribute('class', 'value-medal-star');
    star.setAttribute('d', 'M8 3.7l.7 1.4 1.5.2-1.1 1.1.3 1.5L8 7.2l-1.4.7.3-1.5-1.1-1.1 1.5-.2Z');
    svg.append(leftRibbon, rightRibbon, disc, ring, star);
    node.append(svg);
    return node;
  }

  function create({document: doc = root.document, variant = 'card', onIgnore = null, onNavigateNetwork = null} = {}) {
    const rowId = ++rowSeq;
    const actions = {onIgnore, onNavigateNetwork};
    const node = el(doc, 'div', 'status-row ' + (variant === 'list' ? 'banner-system-badges' : 'dashboard-card-tags'));
    node.dataset.variant = variant;
    node.dataset.empty = 'true';
    const left = el(doc, 'div', 'status-row-side status-row-tags');
    const right = el(doc, 'div', 'status-row-side status-row-faults');
    const medalNode = createMedalNode(doc);
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
    left.append(medalNode, leftMore);
    right.append(rightMore);
    node.append(left, right);

    let data = {medal: null, tags: [], faults: []};
    let leftNodes = [], rightNodes = [];
    let signature = '';
    let frame = 0;
    let fittedWidth = -1;
    let fittedSignature = null;

    leftMore.addEventListener('click', event => {
      event.stopPropagation();
      openOverflowPopover(doc, leftMore, 'tags', data.tags, variant, actions, rowId);
    });
    rightMore.addEventListener('click', event => {
      event.stopPropagation();
      openOverflowPopover(doc, rightMore, 'faults', data.faults, variant, actions, rowId);
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
      if (width === fittedWidth && signature === fittedSignature) return;
      fittedWidth = width;
      fittedSignature = signature;

      const hasMedal = !medalNode.hidden;
      const hasTags = leftNodes.length > 0;
      const hasFaults = rightNodes.length > 0;
      if (!hasTags && !hasFaults) {
        leftMore.hidden = true;
        rightMore.hidden = true;
        if (activePopover && activePopover.rowId === rowId) {
          closePopover(false);
        }
        return;
      }

      const itemGap = variant === 'list' ? 3 : 4;
      const hasLeft = hasMedal || hasTags;
      const sideGap = hasLeft && hasFaults ? (variant === 'list' ? 4 : 6) : 0;

      for (const item of leftNodes) { item.hidden = false; item.classList.remove('is-truncated'); }
      for (const item of rightNodes) { item.hidden = false; item.classList.remove('is-truncated'); }
      leftMore.hidden = true;
      rightMore.hidden = true;

      right.hidden = true;
      const medalW = hasMedal ? Math.ceil(medalNode.getBoundingClientRect().width) : 0;
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
      const maxRight = hasMedal ? Math.max(0, width - medalW - sideGap) : width;
      let rFit = fitWith(rw, maxRight, measureRightMore);
      let lFit = {shown: 0, hidden: 0, used: 0};
      if (hasTags) {
        const medalPrefix = hasMedal ? medalW + itemGap : 0;
        let availL = (hasFaults ? Math.max(0, width - rFit.used - sideGap) : width) - medalPrefix;
        if (hasFaults && lw.length > 0 && availL < minTagMore && width > medalPrefix + minTagMore + sideGap) {
          rFit = fitWith(rw, Math.max(0, width - medalPrefix - minTagMore - sideGap), measureRightMore);
          availL = Math.max(0, width - rFit.used - sideGap - medalPrefix);
        }
        availL = Math.max(0, availL);
        left.hidden = false;
        right.hidden = true;
        if (!hasFaults && lw.length === 1 && lw[0] > availL) {
          lFit = {shown: 1, hidden: 0, used: availL};
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
        if (activePopover?.type === 'overflow' && activePopover.button === leftMore) closePopover(false);
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
        if (activePopover?.type === 'overflow' && activePopover.button === rightMore) closePopover(false);
      }
    }

    function scheduleFit() {
      const win = doc.defaultView || root;
      const width = Math.floor(node.clientWidth) - 1;
      if (width > 0) {
        if (width === fittedWidth && signature === fittedSignature) return;
        fit();
      }
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
        fittedWidth = -1;
        fittedSignature = null;
        data = {medal: next.medal || null, tags: next.tags || [], faults: next.faults || []};
        if (data.medal) {
          const tip = data.medal.title || '高性价比';
          medalNode.hidden = false;
          medalNode.title = tip;
          medalNode.setAttribute('aria-label', tip);
        } else {
          medalNode.hidden = true;
          medalNode.removeAttribute('title');
          medalNode.removeAttribute('aria-label');
        }
        leftNodes = data.tags.map(item => renderTagNode(doc, item));
        rightNodes = data.faults.map(item => renderFaultNode(doc, item, variant, {forPopover: false, actions, rowId}));
        left.replaceChildren(medalNode, ...leftNodes, leftMore);
        right.replaceChildren(...rightNodes, rightMore);
        node.dataset.empty = String(!data.medal && !leftNodes.length && !rightNodes.length);
        node.dataset.signature = signature;
        if (activeTooltip && !activeTooltip.target.isConnected) hideTooltip();
        if (activePopover && activePopover.rowId === rowId) {
          if (activePopover.type === 'overflow') {
            closePopover(false);
          } else if (activePopover.type === 'fault') {
            const matchIdx = data.faults.findIndex(f => (f.attentionKind || f.kind) === activePopover.attentionKind);
            if (matchIdx === -1) {
              closePopover(false);
            } else {
              const matchedFault = data.faults[matchIdx];
              if (activePopover.titleEl) activePopover.titleEl.textContent = matchedFault.label;
              const nextBtn = rightNodes[matchIdx];
              if (nextBtn) {
                activePopover.button = nextBtn;
                nextBtn.setAttribute('aria-expanded', 'true');
              }
            }
          }
        }
      }
      scheduleFit();
    }

    function destroy() {
      const win = doc.defaultView || root;
      if (frame && win.cancelAnimationFrame) win.cancelAnimationFrame(frame);
      frame = 0;
      if (activePopover && activePopover.rowId === rowId) {
        closePopover(false);
      }
      if (activeTooltip && node.contains(activeTooltip.target)) hideTooltip();
      callbacks.delete(node);
      observers.get(doc)?.unobserve(node);
    }

    return {node, update, fit, destroy};
  }

  const api = {items, create, closePopover, hideTooltip, formatHoverReason, remediationFor, shellArg};
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.StatusRow = api;
})(globalThis);
