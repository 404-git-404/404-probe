
(function(root) {
  'use strict';

  const day = 86400000;
  const setup = '404-probe-install setup-root-collector';
  const statusText = {
    ok: '正常', disabled: '未开启', unsupported: '不支持',
    not_installed: '未安装', not_running: '未运行', error: '采集出错'
  };
  const virtNames = {none: '物理机', kvm: 'KVM', qemu: 'QEMU', lxc: 'LXC', 'lxc-libvirt': 'LXC'};
  const virtualization = value => virtNames[value] || value || '未知';

  function element(doc, tag, classes, text) {
    const node = doc.createElement(tag);
    if (classes) node.className = classes;
    if (text != null) node.textContent = text;
    return node;
  }

  function pill(doc, text, tone) {
    const node = element(doc, 'span', 'system-pill', text);
    if (tone) node.dataset.tone = tone;
    return node;
  }

  function badges(doc, agent) {
    return root.DashboardModel.systemBadges(agent).map(([text, tone]) => pill(doc, text, tone));
  }

  function systemPane(doc, agent, now) {
    const frame = doc.createDocumentFragment();
    const checks = agent?.online ? agent.system_checks : null;
    const unsupported = checks && !checks.supported;
    const pending = checks ? '' : '暂无系统检查数据';
    const supportNote = unsupported ? statusText.unsupported : pending;
    const header = element(doc, 'div', 'system-summary');
    const grid = element(doc, 'div', 'details-grid system-grid');
    const note = (parent, text) => parent.append(element(doc, 'p', 'details-note', text));
    const code = text => element(doc, 'code', '', text);
    const section = (name, wide, badge) => {
      const box = element(doc, 'section', 'details-box' + (wide ? ' system-wide' : ''));
      const heading = element(doc, 'div', 'details-box-head');
      heading.append(element(doc, 'h2', '', name));
      if (badge) heading.append(badge);
      box.append(heading);
      grid.append(box);
      return box;
    };
    const fact = (box, label, value, tone) => {
      const row = element(doc, 'div', 'details-fact');
      row.append(element(doc, 'span', 'details-label', label), tone ? pill(doc, value, tone) : element(doc, 'span', '', value));
      box.append(row);
    };
    const reason = status => statusText[status] || '未知';
    const certs = checks?.certificates || [];
    const expired = certs.filter(c => c.expires_at <= now).length;
    const expiring = certs.filter(c => c.expires_at > now && c.expires_at - now <= 7 * day).length;
    const stopped = (checks?.services || []).filter(s => s.state !== 'active').length;
    const failed = (checks?.failed_units || []).length;
    const timeWarning = !!agent?.system_attention?.time;
    const restarts = checks?.service_restarts || [];
    const serviceDanger = stopped > 0 || failed > 0 || restarts.some(s => s.count >= 3);
    const warnings = Number(expiring > 0) + Number(timeWarning) + Number(restarts.length > 0 && !serviceDanger);
    const dangers = Number(expired > 0) + Number(serviceDanger);
    if (!supportNote) {
      if (warnings) header.append(pill(doc, warnings + ' 项提醒', 'warning'));
      if (dangers) header.append(pill(doc, dangers + ' 项异常', 'danger'));
      if (!warnings && !dangers) header.append(pill(doc, '无告警', 'ok'));
      const age = checks.root_collected_at ? ' · ' + root.Card.agoText(checks.root_collected_at) : '';
      const rootStatus = element(doc, 'span', 'details-note system-root-status', 'root 采集：');
      rootStatus.append(element(doc, 'strong', '', (checks.root_status === 'ok' ? '已开启' : reason(checks.root_status)) + age));
      header.append(rootStatus);
    }
    frame.append(header, grid);

    const certBadge = expired ? pill(doc, expired + ' 张已过期', 'danger') :
      expiring ? pill(doc, expiring + ' 张 7 天内到期', 'warning') : null;
    const certificateBox = section('证书', true, certBadge);
    if (supportNote) note(certificateBox, supportNote);
    else if (certs.length) {
      const table = element(doc, 'table', 'system-certificates');
      const head = element(doc, 'tr');
      for (const label of ['域名', '到期', '剩余', '文件']) {
        const th = element(doc, 'th', '', label);
        th.scope = 'col';
        head.append(th);
      }
      const thead = element(doc, 'thead');
      thead.append(head);
      const body = element(doc, 'tbody');
      for (const cert of certs) {
        const row = element(doc, 'tr');
        const delta = cert.expires_at - now;
        const left = delta <= 0 ? '已过期' : Math.ceil(delta / day) + ' 天';
        const tone = delta <= 0 ? 'danger' : delta <= 7 * day ? 'warning' : 'ok';
        const remains = element(doc, 'td');
        remains.append(pill(doc, left, tone));
        const file = element(doc, 'td');
        file.append(code(cert.path));
        row.append(element(doc, 'td', '', cert.domains), element(doc, 'td', '', new Date(cert.expires_at).toISOString().slice(0, 10)), remains, file);
        body.append(row);
      }
      table.append(thead, body);
      const scroll = element(doc, 'div', 'system-table-scroll');
      scroll.append(table);
      certificateBox.append(scroll);
    } else note(certificateBox, '未发现域名证书');
    if (!supportNote) {
      note(certificateBox, '自动扫描 /etc、/usr/local/etc 和“管理”里添加的目录。7 天内到期为黄，已过期为红。');
      if (checks.limited) note(certificateBox, '已达到扫描上限，显示当前取得的结果。');
      if (checks.unreadable_count) {
        const notice = element(doc, 'p', 'system-access-note', '有 ' + checks.unreadable_count + ' 个目录没有权限读取');
        if (checks.unreadable?.length) notice.append('（', code(checks.unreadable.join('、')), '）');
        notice.append('。');
        if (checks.root_status === 'disabled') notice.append('以 root 运行一次 ', code(setup), ' 可补全。');
        certificateBox.append(notice);
      }
    }

    const serviceBadge = stopped ? pill(doc, stopped + ' 个未运行', 'danger') :
      failed ? pill(doc, failed + ' 个失败单元', 'danger') :
      restarts.length ? pill(doc, serviceDanger ? '反复重启' : '偶发重启', serviceDanger ? 'danger' : 'warning') : null;
    const services = section('服务', false, serviceBadge);
    if (supportNote) note(services, supportNote);
    else {
      for (const service of checks.services || []) {
        fact(services, service.name, service.state === 'active' ? '运行中' : '未运行', service.state === 'active' ? 'ok' : 'danger');
      }
      if (checks.service_status !== 'ok') note(services, reason(checks.service_status));
      else if (!checks.services?.length) note(services, '未设置关注的服务');
      if (checks.service_status === 'ok' || failed) fact(services, '失败的单元', (checks.failed_units || []).join('、') || '无', failed ? 'danger' : null);
      for (const service of restarts) {
        const recurring = service.count >= 3;
        fact(services, service.unit, (recurring ? '反复重启' : '偶发重启') + ' · 最近 10 分钟重启 ' + service.count + ' 次',
          recurring ? 'danger' : 'warning');
      }
      note(services, '关注的服务在“管理”里设置。');
    }

    const clock = section('时间同步', false, timeWarning ? pill(doc, '提醒', 'warning') :
      checks?.ntp_status === 'ok' ? pill(doc, '正常', 'ok') : null);
    if (supportNote) note(clock, supportNote);
    else {
      fact(clock, 'NTP', checks.ntp_status !== 'ok' ? reason(checks.ntp_status) : checks.ntp_synchronized ? '已同步' : '未同步');
      fact(clock, '与 Server 相差', (Math.abs(checks.time_offset_ms) / 1000).toFixed(1) + ' 秒');
      note(clock, '未同步，或与 Server 相差超过 5 秒为黄。');
    }

    const packages = section('系统更新', false, pill(doc, '仅显示'));
    if (supportNote) note(packages, supportNote);
    else {
      const p = checks.packages;
      fact(packages, '待更新软件包', p?.status === 'ok' ? p.upgradable + ' 个（其中安全更新 ' + p.security + ' 个）' : reason(p?.status));
      const stable = agent.debian_stable_major, major = checks.debian_major;
      if (major) fact(packages, '新的大版本', stable ? (stable > major ? 'Debian ' + stable + ' 可用（当前 Debian ' + major + '）' : '无（当前 Debian ' + major + '）') : '版本信息尚未取得');
    }

    const reboot = section('需要重启', false, pill(doc, '仅显示'));
    if (supportNote) note(reboot, supportNote);
    else {
      fact(reboot, '状态', checks.reboot_status === 'ok' ? checks.reboot_required ? '需要重启' : '无需重启' : reason(checks.reboot_status));
      if (checks.reboot_status === 'ok' && checks.reboot_required && checks.reboot_packages?.length) {
        fact(reboot, '原因', checks.reboot_packages.join('、') + (checks.reboot_packages_truncated ? ' 等' : ''));
      }
      const uptime = agent.state?.uptime;
      if (Number.isFinite(uptime)) fact(reboot, '已运行', Math.floor(uptime / 86400) + ' 天 ' + Math.floor(uptime % 86400 / 3600) + ' 时');
    }

    const machine = section('机器', true);
    fact(machine, '虚拟化', supportNote || virtualization(checks.virtualization));
    return frame;
  }

  function fail2banPane(doc, agent) {
    const section = element(doc, 'section', 'details-box system-fail2ban');
    const result = agent?.online ? agent.system_checks?.fail2ban : null;
    section.append(element(doc, 'h2', '', 'fail2ban 封锁 IP'));
    const text = result?.status === 'ok' ? (result.jail_count || 0) + ' 个 jail' : statusText[result?.status] || '暂无采集数据';
    const status = element(doc, 'p', 'details-note', text);
    if (result?.status === 'disabled') status.append('。以 root 运行 ', element(doc, 'code', '', setup), ' 开启。');
    section.append(status);
    if (result?.status === 'ok') for (const jail of result.jails || []) {
      const details = element(doc, 'details', 'system-jail');
      const shown = jail.ips?.length || 0;
      details.append(element(doc, 'summary', '', jail.name + ' · 封锁 ' + jail.total + ' 个' + (shown < jail.total ? ' · 显示 ' + shown + ' 个' : '')));
      if (shown) details.append(element(doc, 'p', 'system-jail-ips', jail.ips.join(' · ')));
      section.append(details);
    }
    return section;
  }

  function settingsEditor(doc, save, canEdit) {
    const node = element(doc, 'div', 'details-grid system-settings');
    const editors = [];
    let agent, saving = false;

    function editor(field, title, description, defaults, placeholder, label) {
      const section = element(doc, 'section', 'details-box');
      const heading = element(doc, 'div', 'details-box-head');
      heading.append(element(doc, 'h2', '', title));
      const content = element(doc, 'div', 'system-settings-content');
      const chips = element(doc, 'div', 'system-chips');
      const form = element(doc, 'form', 'system-add');
      const input = element(doc, 'input');
      input.placeholder = placeholder;
      input.setAttribute('aria-label', label);
      input.maxLength = field === 'watched_services' ? 120 : 384;
      const add = element(doc, 'button', '', '添加');
      add.type = 'submit';
      const error = element(doc, 'p', 'details-note system-settings-error');
      error.setAttribute('role', 'status');
      const value = () => agent?.system_check_settings?.[field] || [];
      async function change(list, clear) {
        if (saving || !canEdit(agent)) return;
        saving = true;
        error.textContent = '';
        update(agent);
        try {
          const message = await save(agent, {...agent.system_check_settings, [field]: list});
          error.textContent = message || '';
          if (!message && clear) input.value = '';
        } finally {
          saving = false;
          update(agent);
        }
      }
      form.addEventListener('submit', event => {
        event.preventDefault();
        const text = input.value.trim();
        if (text) void change([...value(), text], true);
      });
      form.append(input, add);
      content.append(element(doc, 'p', 'details-note', description), chips, form, error);
      section.append(heading, content);
      node.append(section);
      let signature;
      return enabled => {
        const items = value();
        const next = JSON.stringify(items);
        if (next !== signature) {
          chips.replaceChildren();
          for (const value of defaults) chips.append(element(doc, 'code', 'system-chip', value));
          for (const item of items) {
            const chip = element(doc, 'span', 'system-chip', item);
            const remove = element(doc, 'button', '', '×');
            remove.type = 'button';
            remove.setAttribute('aria-label', '移除 ' + item);
            remove.addEventListener('click', () => void change(value().filter(entry => entry !== item), false));
            chip.append(remove);
            chips.append(chip);
          }
          signature = next;
        }
        input.disabled = add.disabled = !enabled;
        for (const button of chips.querySelectorAll('button')) button.disabled = !enabled;
      };
    }
    editors.push(editor('certificate_directories', '系统检查 · 证书目录', '默认扫描以下目录（含 5 层子目录）。证书放在别处时，可以添加绝对路径。', ['/etc', '/usr/local/etc'], '/root/certs', '添加证书目录'));
    editors.push(editor('watched_services', '系统检查 · 关注的服务', '这些服务未运行时标红，计入“需要关注”。', [], '服务名，例如 caddy', '添加关注的服务'));
    function update(latest) {
      agent = latest;
      const enabled = !!agent?.system_check_settings && !saving && canEdit(agent);
      for (const refresh of editors) refresh(enabled);
    }
    return {node, update};
  }

  root.SystemChecks = {badges, systemPane, fail2banPane, settingsEditor, virtualization};
})(globalThis);
