const container = document.querySelector('#agents');
const empty = document.querySelector('#empty');
const stream = document.querySelector('#stream');
const addAgentButton = document.querySelector('#add-agent');
const addAgentDialog = document.querySelector('#add-agent-dialog');
const addAgentCreate = document.querySelector('#add-agent-create');
const addAgentResult = document.querySelector('#add-agent-result');
const addAgentForm = document.querySelector('#add-agent-form');
const addAgentName = document.querySelector('#add-agent-name');
const addAgentSubmit = document.querySelector('#add-agent-submit');
const addAgentError = document.querySelector('#add-agent-error');
const createdInstallCommand = document.querySelector('#created-install-command');
const copyInstallCommand = document.querySelector('#copy-install-command');
const createdEnrollment = document.querySelector('#created-enrollment');
const removeAgentDialog = document.querySelector('#remove-agent-dialog');
const removeAgentForm = document.querySelector('#remove-agent-form');
const removeAgentName = document.querySelector('#remove-agent-name');
const removeAgentInput = document.querySelector('#remove-agent-input');
const removeAgentPrompt = document.querySelector('#remove-agent-prompt');
const removeAgentConfirm = document.querySelector('#remove-agent-confirm');
const removeAgentError = document.querySelector('#remove-agent-error');
const serverVersion = document.querySelector('#server-version');
const upgradeAgentDialog = document.querySelector('#upgrade-agent-dialog');
const upgradeAgentForm = document.querySelector('#upgrade-agent-form');
const upgradeCurrentVersion = document.querySelector('#upgrade-current-version');
const upgradeTargetVersion = document.querySelector('#upgrade-target-version');
const upgradeAgentConfirm = document.querySelector('#upgrade-agent-confirm');
const upgradeAgentError = document.querySelector('#upgrade-agent-error');
const upgradeReleaseSelect = document.querySelector('#upgrade-release');
const upgradeReleaseReason = document.querySelector('#upgrade-release-reason');
const upgradeBetaLabel = document.querySelector('#upgrade-beta-label');
const upgradeBetaConfirm = document.querySelector('#upgrade-beta-confirm');
let upgradeReleaseTargets = [];
let upgradeCatalogController;
let upgradeCatalogGeneration = 0;
const planDialog = document.querySelector('#plan-dialog');
const planForm = document.querySelector('#plan-form');
const planAgentName = document.querySelector('#plan-agent-name');
const planTrafficEnabled = document.querySelector('#plan-traffic-enabled');
const planTrafficFields = document.querySelector('#plan-traffic-fields');
const planError = document.querySelector('#plan-error');
const addShell = DialogShell.attach(addAgentDialog,{title:'添加 Agent',subtitle:'把一台新机器接入面板'});
const upgradeShell = DialogShell.attach(upgradeAgentDialog,{size:'small',title:'升级 Agent'});
const removeShell = DialogShell.attach(removeAgentDialog,{size:'small',title:'撤销 Agent'});
const agentState = AgentState.create();
const monitorCoordinator = NetworkQualityCore.coordinator();
const networkQuality = NetworkQuality.create({csrf: () => mutationCSRFToken, coordinator: monitorCoordinator,
  onConfig: (id,config) => {latencyCharts.bindConfig(id,config);},
  onFamily: id => latencyCharts.familyChanged(id)});
const latencyCharts = LatencyMini.create({coordinator: monitorCoordinator,
  family: id => networkQuality.familyOf(id), setFamily: (id,value) => networkQuality.setFamily(id,value), openSettings: (id,trigger) => networkQuality.openSettings(id,trigger)});
const trafficCharts = TrafficChart.create({coordinator: monitorCoordinator});
const cardFaces = Card.createFaceStore();
const cardObservations = CardMetrics.createObserver();
const agents = agentState.agents;
let overviewReady = false;
let organizationDialogs=null,expenseDialog=null;
const dashboardUI=DashboardView.create({onchange:()=>render(),onManage:(kind,trigger)=>organizationDialogs?.openManage(kind,trigger)});
const overview = Overview.create({preferences:dashboardUI.preferences,onfilter:key=>dashboardUI.apply(key),onexpense:trigger=>expenseDialog?.open(trigger)});
const deviceDetails = DeviceDetails.create({agents,coordinator:monitorCoordinator,
  identity: agent => countryMark(agent?.country_code,agent?.country_source),
  management: agent => detailsFor(agent.agent_id)?.management,
  overview: agent => detailsFor(agent.agent_id)?.overview,
  upgradeTargets: id => loadDetailUpgradeTargets(id),
  // Revealed IP addresses never outlive the dialog or a switch to another Agent.
  conceal: () => { for (const view of detailViews.values()) view.egressPanel?.conceal(); },
  resourceFacts: agent => resourceFactsFor(agent).filter(row=>row[0]==='DISK'),
  hydrate: id => hydrateActiveDetail(id)});
const revokedAgentIDs = agentState.revokedAgentIDs;
const selectorController = Selector.create({
  state: agentState, fetcher: (...args) => fetch(...args), csrf: () => mutationCSRFToken,
  unauthorized: () => location.assign('/login'), onchange: agentID => selectorView.refresh(agentID),
});
const selectorView = Selector.view({
  controller: selectorController, state: agentState, csrf: () => mutationCSRFToken,
  onopen: agentID => { planRenewal.close(); reconciliation.notify(agentID); selectorController.recover(agentID, readJSON); },
});
const editingAgentNames = new Set();
const agentNameDrafts = new Map();
const agentNameEditGeneration = new Map();
const agentNameErrors = new Map();
const nameWriteControllers = new Map();
const countryWriteControllers = new Map();
const trafficResetRequests = new Map();
const trafficResetControllers = new Map();
const trafficResetFeedback = new Map();
const uncertainCountryRequests = new Map();
let planCountryPicker;
const planWriteControllers = new Map();
const agentActionErrors = new Map();
let planEditGeneration = 0;
let mutationCSRFToken = '';
let pendingRemoveAgentID = '';
let pendingUpgradeAgentID = '';
let pendingPlanAgentID = '';
let planQuotaSession;
let serverBuild = {version: 'unknown', upgrade_eligible: false};

const bytes = (value, rate = false, binary = false) => {
  let number = Number(value || 0);
  const units = binary ? ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'] : ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const base = binary ? 1024 : 1000;
  let index = 0;
  while (number >= base && index < units.length - 1) {
    number /= base;
    index++;
  }
  return `${number.toFixed(index ? 1 : 0)} ${units[index]}${rate ? '/s' : ''}`;
};

const duration = value => {
  const seconds = Number(value || 0);
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor(seconds % 86400 / 3600);
  const minutes = Math.floor(seconds % 3600 / 60);
  return days ? `${days}天 ${hours}小时` : `${hours}小时 ${minutes}分`;
};

const seen = value => value ? new Date(value).toLocaleString() : '从未上报';
const pct = value => `${Number(value || 0).toFixed(1)}%`;
const upgradeLabel = operation => {
  if (!operation) return '';
  const labels = {requested: '已提交升级请求', claimed: '机器已接到升级请求', downloading: '正在下载新版本', verifying: '正在校验下载文件', staging: '正在准备安装文件', installing: '正在安装新版本', restarting: '正在重启程序', health_check: '正在确认新版本运行正常', succeeded: `已升级到 ${operation.target_version}`, failed: '升级失败，请查看详情后重试', rolled_back: '升级失败，已恢复原来的版本'};
  return labels[operation.status] || operation.status;
};
const upgradeHint = agent => AgentState.upgradeButtonHint(agent, serverBuild, agentState.busy(agent.agent_id, 'upgrade'));

async function readJSON(path, signal) {
  return AgentState.fetchJSON(path, {fetcher: fetch, signal, unauthorized: () => location.assign('/login')});
}

async function readOptionalJSON(path, signal) {
  return AgentState.fetchJSON(path, {fetcher: fetch, signal, optional: true, unauthorized: () => location.assign('/login')});
}

async function loadMutationSession() {
  try {
    const session = await readJSON('/api/v1/web/session');
    mutationCSRFToken = session.csrf_token || '';
    serverBuild = await readJSON('/api/v1/web/version');
    serverVersion.textContent = `Server ${serverBuild.version || 'unknown'}`;
    addAgentButton.disabled = !mutationCSRFToken;
    render();
  } catch (error) {
    mutationCSRFToken = '';
    addAgentButton.disabled = true;
  }
}

function refreshUpgradeSelection() {
  const target = upgradeReleaseTargets[Number(upgradeReleaseSelect.value)];
  upgradeTargetVersion.textContent = target?.version || '—';
  AgentState.appendUpgradeReason(upgradeReleaseReason, target, serverBuild);
  upgradeBetaLabel.hidden = target?.channel !== 'beta';
  upgradeAgentConfirm.disabled = !target?.supported || (target.channel === 'beta' && !upgradeBetaConfirm.checked);
}
upgradeReleaseSelect.addEventListener('change', () => { upgradeBetaConfirm.checked = false; refreshUpgradeSelection(); });
upgradeBetaConfirm.addEventListener('change', refreshUpgradeSelection);

async function openUpgradeAgentDialog(agent) {
  upgradeShell.heading('升级 Agent',agent.name);
  upgradeCatalogController?.abort();
  const generation = ++upgradeCatalogGeneration;
  const controller = new AbortController();
  upgradeCatalogController = controller;
  pendingUpgradeAgentID = agent.agent_id;
  upgradeCurrentVersion.textContent = agent.version || 'unknown';
  upgradeTargetVersion.textContent = '—';
  upgradeReleaseSelect.replaceChildren();
  upgradeReleaseTargets = [];
  upgradeReleaseReason.textContent = '正在读取升级状态…';
  upgradeReleaseSelect.disabled = true;
  upgradeBetaConfirm.checked = false;
  upgradeBetaLabel.hidden = true;
  upgradeAgentError.classList.add('hidden');
  upgradeAgentConfirm.disabled = true;
  upgradeAgentDialog.showModal();
  try {
    const catalog = await readJSON(`/api/v1/web/agents/${encodeURIComponent(agent.agent_id)}/upgrade-targets`, controller.signal);
    if (generation !== upgradeCatalogGeneration || pendingUpgradeAgentID !== agent.agent_id) return;
    upgradeReleaseTargets = Array.isArray(catalog.targets) ? catalog.targets : [];
    for (const [index, target] of upgradeReleaseTargets.entries()) {
      const option = document.createElement('option');
      option.value = String(index);
      option.textContent = `升级到 ${target.version}（与 Server 相同）`;
      upgradeReleaseSelect.append(option);
    }
    upgradeReleaseSelect.value = upgradeReleaseTargets.length ? '0' : '';
    const soleStable=upgradeReleaseTargets.length===1&&upgradeReleaseTargets[0].channel==='stable';
    upgradeReleaseSelect.hidden=soleStable;
    document.querySelector('#upgrade-release-label').hidden=soleStable;
    upgradeReleaseSelect.disabled=upgradeReleaseTargets.length<2;
    refreshUpgradeSelection();
    if (!upgradeReleaseTargets.length) {
      const option = document.createElement('option');
      option.textContent = AgentState.upgradeReasonDisplay({reason_code:catalog.message_code,reason:catalog.message}) || AgentState.upgradeReasonDisplay({reason_code:'server_build_ineligible'});
      upgradeReleaseSelect.append(option);
      upgradeReleaseReason.textContent = option.textContent;
    }
  } catch (error) {
    if (generation === upgradeCatalogGeneration && !controller.signal.aborted) upgradeReleaseReason.textContent = '升级状态读取失败，请重新打开重试';
  }
}

document.querySelector('#upgrade-agent-cancel').addEventListener('click', () => upgradeAgentDialog.close());
upgradeAgentDialog.addEventListener('close', () => { if (upgradeAgentDialog.open) return; pendingUpgradeAgentID = ''; ++upgradeCatalogGeneration; upgradeCatalogController?.abort(); });
upgradeAgentForm.addEventListener('submit', async event => {
  event.preventDefault();
  const agentID = pendingUpgradeAgentID;
  const generation = upgradeCatalogGeneration;
  const target = upgradeReleaseTargets[Number(upgradeReleaseSelect.value)];
  if (!mutationCSRFToken || !agentID || !target?.supported || (target.channel === 'beta' && !upgradeBetaConfirm.checked)) return;
  const body = target.channel === 'stable' && target.version === serverBuild.version ? {} : {channel: target.channel, target_version: target.version};
  if (target.channel === 'beta') body.confirm_beta = true;
  const mutation = agentState.beginMutation(agentID, ['upgrade']);
  if (!mutation) return;
  upgradeAgentConfirm.disabled = true;
  upgradeAgentError.classList.add('hidden');
  try {
    const response = await fetch(`/api/v1/web/agents/${encodeURIComponent(agentID)}/upgrade`, {
      method: 'POST', cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken}, body: JSON.stringify(body),
    });
    if (response.status === 401) { location.assign('/login'); return; }
    if (!response.ok) { const detail = await response.json().catch(() => ({})); throw new Error(detail.error?.message || detail.message || detail.error?.code || response.statusText); }
    const operation = await response.json();
    if (!finishResource(mutation, {upgrade: operation})) return;
    if (pendingUpgradeAgentID === agentID && generation === upgradeCatalogGeneration) upgradeAgentDialog.close();
    render();
  } catch (error) {
    if (pendingUpgradeAgentID === agentID && generation === upgradeCatalogGeneration) {
      upgradeAgentError.textContent = error.message || '升级请求失败，请重试。';
      upgradeAgentError.classList.remove('hidden');
      refreshUpgradeSelection();
    }
  } finally {
    finishResource(mutation);
  }
});

// One-click install: the dialog first asks how to install; the command it shows is never kept after closing.
const enrollmentDialog = AgentEnrollment.createController({
  els: {
    choice: document.querySelector('#add-agent-choice'), manual: addAgentCreate, manualResult: addAgentResult,
    oneclick: document.querySelector('#oneclick-pane'), oneclickResult: document.querySelector('#oneclick-result'),
    choiceName: document.querySelector('#add-agent-choice-name'), next: document.querySelector('#add-agent-next'),
    chooseOneClick: document.querySelector('#add-agent-choose-oneclick'), chooseManual: document.querySelector('#add-agent-choose-manual'),
    reason: document.querySelector('#add-agent-oneclick-reason'), form: document.querySelector('#oneclick-form'),
    name: document.querySelector('#oneclick-name'), error: document.querySelector('#oneclick-error'), submit: document.querySelector('#oneclick-submit'),
    oneclickBack: document.querySelector('#oneclick-back'), command: document.querySelector('#oneclick-command'),
    copy: document.querySelector('#oneclick-copy'), countdown: document.querySelector('#oneclick-countdown'),
    status: document.querySelector('#oneclick-status'), revoke: document.querySelector('#oneclick-revoke'),
  },
  csrf: () => mutationCSRFToken,
  available: () => Boolean(serverBuild.one_click_install),
  unavailableReason: () => '此 Server 是开发构建或无法校验的构建，没有对应的正式 release，无法使用一键安装；请使用手动安装。',
  unauthorized: () => location.assign('/login'),
});

let manualEnrollmentGeneration=0;
function clearEnrollmentDialog() {
  manualEnrollmentGeneration++;
  createdInstallCommand.textContent = '';
	copyInstallCommand.disabled = true;
  createdEnrollment.textContent = '';
  addAgentForm.reset();
  addAgentError.classList.add('hidden');
  addAgentSubmit.disabled = false;
  enrollmentDialog.reset();
}

addAgentButton.addEventListener('click', () => {
  clearEnrollmentDialog();
  addAgentDialog.showModal();
  document.querySelector('#add-agent-choice-name').focus();
});

document.querySelector('#add-agent-choice-cancel').addEventListener('click', () => addAgentDialog.close());
document.querySelector('#oneclick-done').addEventListener('click', () => addAgentDialog.close());
document.querySelector('#add-agent-next').addEventListener('click', () => { if(document.querySelector('#add-agent-choice').classList.contains('hidden')) { addAgentName.value=document.querySelector('#add-agent-choice-name').value; (addAgentCreate.classList.contains('hidden')?document.querySelector('#oneclick-submit'):addAgentSubmit).focus(); } });

document.querySelector('#add-agent-cancel').addEventListener('click', () => addAgentDialog.close());
document.querySelector('#add-agent-done').addEventListener('click', () => addAgentDialog.close());
addAgentDialog.addEventListener('close', clearEnrollmentDialog);

addAgentForm.addEventListener('submit', async event => {
  event.preventDefault();
  if (!mutationCSRFToken || addAgentSubmit.disabled) return;
  const generation=manualEnrollmentGeneration;
  addAgentSubmit.disabled = true;
  addAgentError.classList.add('hidden');
  try {
    const response = await fetch('/api/v1/web/agents', {
      method: 'POST',
      cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken},
      body: JSON.stringify({name: addAgentName.value}),
    });
    if (response.status === 401) {
      location.assign('/login');
      return;
    }
    if (!response.ok) throw new Error(response.statusText);
    const enrollment = await response.json();
    if(generation!==manualEnrollmentGeneration||!addAgentDialog.open)return;
    createdInstallCommand.textContent = enrollment.install_available ? enrollment.install_command : (enrollment.install_message || '当前 Server 构建无法生成安全安装命令');
	copyInstallCommand.disabled = !enrollment.install_available;
    createdEnrollment.textContent = enrollment.enrollment_value;
    addAgentCreate.classList.add('hidden');
    addAgentResult.classList.remove('hidden');
    await refresh();
  } catch (error) {
    if(generation!==manualEnrollmentGeneration)return;
    addAgentError.classList.remove('hidden');
    addAgentSubmit.disabled = false;
  }
});

function clearRemoveAgentDialog() {
  pendingRemoveAgentID = '';
  removeAgentName.textContent = '';
  removeAgentError.classList.add('hidden');
  removeAgentInput.value='';removeAgentInput.disabled=false;removeAgentConfirm.disabled = true;
}

function openRemoveAgentDialog(agent) {
  removeShell.heading('撤销 Agent',agent.name);
  clearRemoveAgentDialog();
  pendingRemoveAgentID = agent.agent_id;
  removeAgentName.textContent = agent.name || '未命名 Agent';removeAgentPrompt.textContent='输入设备名 '+removeAgentName.textContent+' 以确认';
  removeAgentDialog.showModal();
}

removeAgentInput.addEventListener('input',()=>{removeAgentConfirm.disabled=removeAgentInput.disabled||!Management.matchesDeviceName(removeAgentInput.value,removeAgentName.textContent);});
document.querySelector('#remove-agent-cancel').addEventListener('click', () => removeAgentDialog.close());
removeAgentDialog.addEventListener('close', clearRemoveAgentDialog);

removeAgentForm.addEventListener('submit', async event => {
  event.preventDefault();
  const agentID = pendingRemoveAgentID;
  if (!mutationCSRFToken || !agentID || removeAgentInput.disabled || !Management.matchesDeviceName(removeAgentInput.value,removeAgentName.textContent)) return;
  const barrier = agentState.beginDelete(agentID);
  if (!barrier) return;
  reconciliation.cancel(agentID);
  nameWriteControllers.get(agentID)?.abort();
  countryWriteControllers.get(agentID)?.abort();
  trafficResetControllers.get(agentID)?.abort();
  planWriteControllers.get(agentID)?.abort();
  selectorController.cancel(agentID);
  pendingTelemetry.delete(agentID);
  let removed = false;
  removeAgentConfirm.disabled = true;removeAgentInput.disabled=true;
  removeAgentError.classList.add('hidden');
  try {
    const response = await fetch(`/api/v1/web/agents/${encodeURIComponent(agentID)}/revoke`, {
      method: 'POST',
      cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken},
      body: '{}',
    });
    if (response.status === 401) {
      location.assign('/login');
      return;
    }
    if (!response.ok) throw new Error(response.statusText);
    const revoked = await response.json();
    if (!revoked.agent || !revoked.agent.revoked) throw new Error('Agent was not revoked');
    removed = true;
    agentState.finishDelete(barrier, true);
    editingAgentNames.delete(agentID); agentNameDrafts.delete(agentID);
    agentNameErrors.delete(agentID); agentNameEditGeneration.delete(agentID);
    agentActionErrors.delete(`${agentID}/google`); agentActionErrors.delete(`${agentID}/country`);
    if (pendingRemoveAgentID === agentID) removeAgentDialog.close();
    render();
    await refresh();
  } catch (error) {
    if(pendingRemoveAgentID===agentID){removeAgentError.classList.remove('hidden');removeAgentInput.disabled=false;removeAgentConfirm.disabled=!Management.matchesDeviceName(removeAgentInput.value,removeAgentName.textContent);}
  } finally {
    if (!removed) {
      agentState.finishDelete(barrier, false);
      reconciliation.notify(agentID);
      render();
    }
  }
});

async function setAgentDisabled(agent, disabled, button) {
  if (!mutationCSRFToken || agent.revoked) return;
  if(disabled && !await DialogShell.confirm({title:'暂停监控',device:agent.name,message:'暂停后不再接收数据，可以随时恢复。',confirmText:'确认暂停',danger:true,trigger:button}))return;
  const mutation = agentState.beginMutation(agent.agent_id, ['lifecycle']);
  if (!mutation) return;
  button.disabled = true;
  const action = disabled ? 'disable' : 'enable';
  try {
    const response = await fetch(`/api/v1/web/agents/${encodeURIComponent(agent.agent_id)}/${action}`, {
      method: 'POST',
      cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken},
      body: '{}',
    });
    if (response.status === 401) {
      location.assign('/login');
      return;
    }
    if (!response.ok) throw new Error(response.statusText);
    const changed = await response.json();
    if (!changed.agent || changed.agent.revoked || Boolean(changed.agent.disabled_at) !== disabled) {
      throw new Error('Agent state did not change');
    }
    if (!finishResource(mutation, changed.agent)) return;
    render();
    await refresh();
  } catch (error) {
    button.disabled = false;
  } finally {
    finishResource(mutation);
  }
}

let activeYouTubeTooltip = null;
function positionYouTubeTooltip() {
 const anchor=activeYouTubeTooltip;
 if(!anchor?.isConnected){activeYouTubeTooltip=null;return;}
 const tip=anchor.nextElementSibling;
 if(!tip||!(document.activeElement===anchor||anchor.matches(':hover')||tip.matches(':hover')))return;
 const margin=8,width=Math.min(300,document.documentElement.clientWidth-margin*2);
 tip.style.width=`${width}px`;tip.style.maxHeight=`${innerHeight-margin*2}px`;tip.style.overflowY='auto';
 const rect=anchor.getBoundingClientRect(),height=tip.getBoundingClientRect().height;
 const below=rect.bottom,above=rect.top-height;
 const top=below+height<=innerHeight-margin?below:above>=margin?above:Math.max(margin,Math.min(below,innerHeight-height-margin));
 tip.style.left=`${Math.max(margin,Math.min(rect.left,document.documentElement.clientWidth-width-margin))}px`;
 tip.style.top=`${top}px`;
}
function openYouTubeTooltip(anchor){activeYouTubeTooltip=anchor;positionYouTubeTooltip();}
addEventListener('resize',positionYouTubeTooltip,{passive:true});
addEventListener('scroll',positionYouTubeTooltip,{capture:true,passive:true});

function googleCheckItems(agent) {
 const value=YouTubeStatus.view(agent);
 return [['YouTube',value.label,value.tone,value.premium]];
}

function googleStatusSummary(agent) {
  return googleCheckItems(agent).map(([name, value]) => `${name} ${value}`).join(' · ');
}

function googleStatusState(google) {
  if (!google?.supported) return '当前 Agent 不支持检测';
  if (google.pending) return '检测中';
  if (!google.result) return '尚未检测';
  if (google.stale) return '旧结果 · 需重新检测';
  return '';
}

function googleStatusPanel(agent) {
  const panel = document.createElement('section');
  panel.className = 'google-checks';
  panel.setAttribute('aria-label', 'YouTube 检测结果');
  for (const [label, value, tone, premium] of googleCheckItems(agent)) {
    const item = document.createElement('div');
    item.className = 'google-check';
    const name = document.createElement('span');
    name.className = 'youtube-icon'; name.textContent = '▶'; name.setAttribute('aria-hidden','true');
    const result = document.createElement('strong');
    result.className = `check-result ${tone}`;
    result.textContent = value;
    if (premium) result.dataset.premium = premium;
    const brand = document.createElement('span');brand.className='youtube-name';brand.textContent='YouTube';
    const detail=YouTubeStatus.view(agent).description;
    result.tabIndex=0;result.setAttribute('aria-label',detail);
    result.addEventListener('mouseenter',()=>openYouTubeTooltip(result));result.addEventListener('focus',()=>openYouTubeTooltip(result));result.addEventListener('click',()=>{result.focus();openYouTubeTooltip(result);});
    const tip=document.createElement('span');tip.className='youtube-status-tip';tip.setAttribute('role','tooltip');tip.textContent=detail;
    tip.id=`youtube-observation-${agent.agent_id||'preview'}`;result.setAttribute('aria-describedby',tip.id);
    item.append(name, brand, result,tip);
    panel.append(item);
  }
  return panel;
}

async function rerunGoogleStatus(agent, button, feedback) {
  if (!mutationCSRFToken) return;
  const mutation = agentState.beginMutation(agent.agent_id, ['google']);
  if (!mutation) return;
  agentActionErrors.delete(`${agent.agent_id}/google`);
  button.disabled = true;
  button.textContent = '检测中…';
  feedback.textContent = '';
  try {
    const response = await fetch(`/api/v1/web/agents/${encodeURIComponent(agent.agent_id)}/google-status`, {
      method: 'POST', cache: 'no-store',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': mutationCSRFToken}, body: '{}',
    });
    if (response.status === 401) { location.assign('/login'); return; }
    if (!response.ok) {
      let message = '检测请求失败';
      try { message = (await response.json()).error?.message || message; } catch (error) { /* use safe fallback */ }
      throw Object.assign(new Error(message),{status:response.status});
    }
    finishResource(mutation, {google_status: {...agents.get(agent.agent_id)?.google_status, pending: true}});
    render();
  } catch (error) {
    button.textContent = '重新检测';
    button.disabled = false;
    const uncertain=!error.status||error.status>=500;
    button.disabled=uncertain;
    feedback.textContent = uncertain?'提交结果未确认，正在核实；不会自动重发':error.message||'检测请求失败';
    agentActionErrors.set(`${agent.agent_id}/google`, feedback.textContent);
  } finally {
    finishResource(mutation);
  }
}

for (const button of document.querySelectorAll('[data-copy-target]')) {
  button.addEventListener('click', async () => {
    const target = document.querySelector(`#${button.dataset.copyTarget}`);
    if (!target || !target.textContent) return;
    const label = button.textContent;
    try {
      await navigator.clipboard.writeText(target.textContent);
      button.textContent = '已复制';
      setTimeout(() => { button.textContent = label; }, 1200);
    } catch (error) {
      button.textContent = '复制失败';
      setTimeout(() => { button.textContent = label; }, 1200);
    }
  });
}

const planFields = {
  traffic_mode: '#plan-traffic-mode', quota_value: '#plan-quota-value', quota_unit: '#plan-quota-unit',
  cycle_kind: '#plan-cycle-kind', cycle_count: '#plan-cycle-count', cycle_anchor: '#plan-cycle-anchor', timezone: '#plan-timezone',
  calibration_value: '#plan-calibration-value', calibration_unit: '#plan-calibration-unit',
  bandwidth_value: '#plan-bandwidth-value', bandwidth_unit: '#plan-bandwidth-unit', currency: '#plan-currency',
  purchase_price: '#plan-purchase-price', purchase_price_period: '#plan-purchase-period',
  renewal_price: '#plan-renewal-price', renewal_price_period: '#plan-renewal-period',
  purchase_date: '#plan-purchase-date', renewal_date: '#plan-renewal-date', expiry_date: '#plan-expiry-date',
  country_code_override: '#plan-country-code',
};
const planFieldValues=()=>Object.fromEntries(Object.entries(planFields).map(([key,selector])=>[key,key==='currency'&&document.querySelector(selector).value==='__custom__'?document.querySelector('#plan-currency-custom').value:document.querySelector(selector).value]));
const readPlanDraft=()=>({limited:document.querySelector('#quota-limit').value,traffic_enabled:planTrafficEnabled.checked,...planFieldValues()});
const planEditor = PlanEditor.create({agents,read:readPlanDraft,
  onclose: () => { pendingPlanAgentID = ''; planEditGeneration++; }, onclear: () => clearPlan(),
});
const planRenewal = PlanRenewal.create({agents,
  canWrite: id => Boolean(mutationCSRFToken && agents.has(id) && !agents.get(id).revoked && !agentState.blocked(id) && !agentState.busy(id,'plan')),
  csrf: () => mutationCSRFToken, begin: id => agentState.beginMutation(id,['plan']),
  finish: (token,patch) => finishPlanResource(token,patch),
  changed: () => { if(!suspended)render(); },
  opening: () => { selectorView.hide(); },
  edit: (id,trigger) => { const agent=agents.get(id);if(agent){openPlanDialog(agent,trigger);document.querySelector('#plan-renewal-date').focus();} },
});
function openPlanRenewal(agent,trigger=null) {
  if(!agents.has(agent.agent_id)||agent.revoked||agentState.blocked(agent.agent_id))return;
  planEditor.handoff(()=>{if(agents.has(agent.agent_id)&&!agentState.blocked(agent.agent_id))planRenewal.open(agent.agent_id,trigger);});
}

function togglePlanTrafficFields() {
  planTrafficFields.classList.toggle('hidden', !planTrafficEnabled.checked);
}

let planDialogMode='plan',quotaOffConfirmed=true;
function openPlanDialog(agent, trigger = null, mode='plan') {
  if(planRenewal.unconfirmed(agent.agent_id))return;
  planRenewal.close();
  planEditor.open(agent.agent_id,trigger,()=>{
  planDialogMode=mode;planDialog.dataset.mode=mode;document.querySelector('#plan-title').textContent=mode==='quota'?'流量配额':'套餐与续费';document.querySelector('#plan-information').hidden=mode==='quota';document.querySelector('#quota-information').hidden=mode!=='quota';document.querySelector('#plan-clear').hidden=mode==='quota';document.querySelector('#quota-decimal-note').hidden=mode!=='quota';quotaOffConfirmed=true;document.querySelector('#quota-off-confirm').hidden=true;
  planEditGeneration++;
  pendingPlanAgentID = agent.agent_id;
  planForm.reset();
  const plan = agent.plan || {};
  planQuotaSession = PlanUnits.open(plan);
  planAgentName.textContent = agent.name || agent.state?.hostname || '未命名 Agent';planAgentName.title=planAgentName.textContent;
  planTrafficEnabled.checked = Boolean(plan.traffic_mode);
  for (const [key, selector] of Object.entries(planFields)) {
    const input = document.querySelector(selector);
    const fallback = key === 'cycle_count' ? '1' : key === 'timezone' ? (Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC') : '';
    if(input.tagName==='SELECT'){
      for(const old of Array.from(input.options))if(old.dataset.planStored==='true')old.remove();
      if(plan[key]&&!Array.from(input.options).some(o=>o.value===String(plan[key]))){const o=document.createElement('option');o.value=String(plan[key]);o.textContent=o.value+(key.endsWith('_price_period')?'（未知周期，保留原值）':'');o.dataset.planStored='true';input.append(o);}
    }
    input.value = key.startsWith('calibration_') ? (key === 'calibration_unit' ? 'GB' : '') : (plan[key] ?? fallback);
  }
  document.querySelector('#plan-currency-custom').hidden=true;document.querySelector('#plan-currency-custom').required=false;
  if (!plan.traffic_mode) document.querySelector('#plan-traffic-mode').value = 'sum';
  if (!plan.quota_unit) document.querySelector('#plan-quota-unit').value = 'TB';
  if (plan.quota_value) {
    document.querySelector('#plan-quota-value').value = planQuotaSession.shown.value;
    document.querySelector('#plan-quota-unit').value = planQuotaSession.shown.unit;
  }
  const quotaNote = document.querySelector('#plan-quota-note');
  quotaNote.title = planQuotaSession.legacy ? '旧额度已精确换算为十进制。未改额度保存时保留原字节定义；修改后的额度最多 3 位小数。' : '流量单位采用十进制：1 TB = 1000 GB；额度与校准最多 3 位小数。';
  quotaNote.textContent='选“不限流量”时照样统计每期用量，卡片上不显示进度条和百分比。';
  if (!plan.cycle_kind) document.querySelector('#plan-cycle-kind').value = 'monthly';if(!plan.cycle_anchor)document.querySelector('#plan-cycle-anchor').value=new Intl.DateTimeFormat('en-CA').format(new Date());document.querySelector('#quota-cycle-day').value=Number(document.querySelector('#plan-cycle-anchor').value.slice(-2))||1;document.querySelector('#quota-limit').value=plan.traffic_mode&&!plan.quota_value?'unlimited':'limited';document.querySelector('#quota-timezone').textContent='时区：'+document.querySelector('#plan-timezone').value;const cycleMore=document.querySelector('.quota-cycle-more');cycleMore.open=plan.cycle_kind&&plan.cycle_kind!=='monthly'||Number(plan.cycle_count||1)!==1||!plan.timezone;document.querySelector('#quota-cycle-options').setAttribute('aria-expanded',String(cycleMore.open));const days=Card.cycleDays(plan,Date.now());document.querySelector('#quota-cycle-summary').textContent=plan.cycle_start&&plan.cycle_end?'当前周期 '+shortPlanDate(plan.cycle_start)+' – '+shortPlanDate(plan.cycle_end)+(days?'，已过 '+Math.max(0,days.day-1)+' 天':''):'';syncQuotaPresentation();
  planError.classList.add('hidden');
  planError.textContent = '';
  togglePlanTrafficFields();
  });
}

const shortPlanDate=v=>{const d=new Date(v);return String(d.getMonth()+1).padStart(2,'0')+'-'+String(d.getDate()).padStart(2,'0');};
function syncQuotaPresentation(){const limit=document.querySelector('#quota-limit').value;document.querySelector('#quota-amount').hidden=limit==='unlimited';for(const group of document.querySelectorAll('[data-quota-segment]')){const value=group.dataset.quotaSegment==='limit'?limit:document.querySelector('#plan-traffic-mode').value;for(const b of group.querySelectorAll('button'))b.setAttribute('aria-pressed',String(b.dataset.value===value));}const monthly=document.querySelector('#plan-cycle-kind').value==='monthly';document.querySelector('#quota-cycle-day').hidden=document.querySelector('#quota-cycle-day-label').hidden=!monthly;}
for(const group of document.querySelectorAll('[data-quota-segment]'))for(const b of group.querySelectorAll('button'))b.addEventListener('click',()=>{document.querySelector(group.dataset.quotaSegment==='limit'?'#quota-limit':'#plan-traffic-mode').value=b.dataset.value;planEditGeneration++;syncQuotaPresentation();});
planTrafficEnabled.addEventListener('change',()=>{if(!planTrafficEnabled.checked&&agents.get(pendingPlanAgentID)?.plan?.traffic_mode){quotaOffConfirmed=false;document.querySelector('#quota-off-confirm').hidden=false;}else{quotaOffConfirmed=true;document.querySelector('#quota-off-confirm').hidden=true;togglePlanTrafficFields();}});
document.querySelector('#quota-keep-on').addEventListener('click',()=>{planTrafficEnabled.checked=true;quotaOffConfirmed=true;document.querySelector('#quota-off-confirm').hidden=true;togglePlanTrafficFields();});
document.querySelector('#quota-confirm-off').addEventListener('click',()=>{quotaOffConfirmed=true;document.querySelector('#quota-off-confirm').hidden=true;togglePlanTrafficFields();});
document.querySelector('#quota-cycle-day').addEventListener('input',e=>{const input=document.querySelector('#plan-cycle-anchor');input.value=input.value.slice(0,8)+String(e.target.value).padStart(2,'0');});
document.querySelector('#plan-cycle-kind').addEventListener('change',()=>{syncQuotaPresentation();if(document.querySelector('#plan-cycle-kind').value!=='monthly'){document.querySelector('.quota-cycle-more').open=true;document.querySelector('#quota-cycle-options').setAttribute('aria-expanded','true');}});
document.querySelector('#quota-cycle-options').addEventListener('click',()=>{const more=document.querySelector('.quota-cycle-more');more.open=!more.open;document.querySelector('#quota-cycle-options').setAttribute('aria-expanded',String(more.open));if(more.open)document.querySelector('#plan-cycle-count').focus();});
document.querySelector('#plan-currency').addEventListener('change',()=>{const input=document.querySelector('#plan-currency-custom'),custom=document.querySelector('#plan-currency').value==='__custom__';input.hidden=!custom;input.required=custom;if(custom)input.focus();});
planForm.addEventListener('input', () => { planEditGeneration++; });
planForm.addEventListener('change', () => { planEditGeneration++; });

async function savePlanPayload(agentID, payload, signal) {
  return Management.write(`/api/v1/web/agents/${encodeURIComponent(agentID)}/plan`, {
    method: 'PUT', csrf: mutationCSRFToken, payload, signal, unauthorized: () => location.assign('/login'),
  });
}

planForm.addEventListener('submit', async event => {
  event.preventDefault();
  if (!pendingPlanAgentID || !mutationCSRFToken || !planEditor.canSubmit() || planRenewal.unconfirmed(pendingPlanAgentID)) return;
  if(planDialogMode==='quota'&&!planTrafficEnabled.checked&&!quotaOffConfirmed)return;
  let payload;try {payload=Management.planSectionPayload(agents.get(pendingPlanAgentID)?.plan||{},planDialogMode,planFieldValues(),{enabled:planTrafficEnabled.checked,limited:document.querySelector('#quota-limit').value==='limited',quotaSession:planQuotaSession});}
  catch (error) { planError.textContent = error.message; planError.classList.remove('hidden'); return; }
  const agentID=pendingPlanAgentID,submittedDraft=JSON.stringify(readPlanDraft());
  const mutation = agentState.beginMutation(agentID, ['plan', 'country']);
  if (!mutation) return;
  const editorToken = planEditor.begin();
  const controller = new AbortController(); planWriteControllers.set(agentID, controller);
  planError.classList.add('hidden');
  try {
    const plan = await savePlanPayload(agentID, payload, controller.signal);
    planRenewal.invalidate(agentID);
    if (!finishPlanResource(mutation, {plan})) return;
    if (pendingPlanAgentID === agentID && planEditor.snapshot().generation===editorToken&&JSON.stringify(readPlanDraft())===submittedDraft) planDialog.close();
    if(!suspended)render();
  } catch (error) {
    if (!controller.signal.aborted && !agentState.blocked(agentID) && pendingPlanAgentID === agentID && planEditor.snapshot().generation===editorToken&&JSON.stringify(readPlanDraft())===submittedDraft) {
      planError.textContent = error.message || '套餐资料保存失败';
      planError.classList.remove('hidden');
    }
  } finally {
    if (planWriteControllers.get(agentID) === controller) planWriteControllers.delete(agentID);
    finishPlanResource(mutation);
    planEditor.finish(editorToken);
  }
});

async function clearPlan() {
  if (!pendingPlanAgentID || !mutationCSRFToken || !planEditor.canSubmit() || planRenewal.unconfirmed(pendingPlanAgentID)) return;
  const agentID=pendingPlanAgentID,submittedDraft=JSON.stringify(readPlanDraft());
  const mutation = agentState.beginMutation(agentID, ['plan', 'country']);
  if (!mutation) return;
  const editorToken = planEditor.begin();
  const controller = new AbortController(); planWriteControllers.set(agentID, controller);
  try {
    const plan=await savePlanPayload(agentID, Management.planSectionPayload(agents.get(agentID)?.plan||{},'clear'), controller.signal);
    planRenewal.invalidate(agentID);
    if (!finishPlanResource(mutation, {plan})) return;
    if (pendingPlanAgentID === agentID && planEditor.snapshot().generation===editorToken&&JSON.stringify(readPlanDraft())===submittedDraft) planDialog.close();
    if(!suspended)render();
  } catch (error) {
    if (!controller.signal.aborted && !agentState.blocked(agentID) && pendingPlanAgentID === agentID && planEditor.snapshot().generation===editorToken&&JSON.stringify(readPlanDraft())===submittedDraft) {
      planError.textContent = error.message || '套餐资料清除失败';
      planError.classList.remove('hidden');
    }
  } finally {
    if (planWriteControllers.get(agentID) === controller) planWriteControllers.delete(agentID);
    finishPlanResource(mutation);
    planEditor.finish(editorToken);
  }
}

function finishPlanResource(token,patch={}) {
  return suspended ? agentState.finishMutation(token,patch) : finishResource(token,patch);
}

function metric(label, value) {
  const box = document.createElement('div');
  box.className = 'metric';
  const name = document.createElement('label');
  name.textContent = label;
  const strong = document.createElement('strong');
  setReadableText(strong, value, `${label}: ${value}`);
  box.append(name, strong);
  return box;
}

function progressBar(percent, tone = '') {
  const track = document.createElement('div');
  track.className = `progress-track ${tone}`.trim();
  track.setAttribute('role', 'progressbar');
  const bounded = Math.max(0, Math.min(100, Number(percent || 0)));
  track.setAttribute('aria-valuenow', bounded.toFixed(1));
  track.setAttribute('aria-valuemin', '0');
  track.setAttribute('aria-valuemax', '100');
  const fill = document.createElement('span');
  fill.style.width = `${bounded}%`;
  track.append(fill);
  return track;
}

function resourceRow(label, value, detail, percent, tone = '') {
  const row = document.createElement('div');
  row.className = `resource-row ${label === 'CPU' ? 'cpu-resource' : ''} ${label === '内存' ? 'memory-resource' : ''}`.trim();
  const head = document.createElement('div');
  const name = document.createElement('span');
  name.textContent = label;
  const strong = document.createElement('strong');
  strong.textContent = value;
  head.append(name, strong);
  row.append(head);
  if (percent != null) row.append(progressBar(percent, tone));
  if (detail) {
    const small = document.createElement('small');
    small.textContent = detail;
    row.append(small);
  }
  return row;
}

function utilizationTone(percent, warning = 70, critical = 90) {
  if (percent == null || !Number.isFinite(Number(percent))) return 'unknown';
  return Number(percent) >= critical ? 'critical' : Number(percent) >= warning ? 'warning' : 'healthy';
}

function stealTone(percent) {
  return utilizationTone(percent, 2, 10);
}

function diskBusyTone(percent) {
  return utilizationTone(percent, 60, 85);
}

function metricTile(kind, icon, label, value, detail, percent, tone, extra) {
  const tile = document.createElement('section');
  tile.className = `metric-tile metric-${kind} tone-${tone || 'unknown'}`;
  const header = document.createElement('div');
  header.className = 'metric-tile-head';
  const title = document.createElement('span');
  title.className = 'metric-tile-label';
  title.innerHTML = `<i aria-hidden="true">${icon}</i>${label}`;
  const strong = document.createElement('strong');
  strong.textContent = value;
  header.append(title, strong);
  const note = document.createElement('small');
  note.textContent = detail;
  note.title = detail;
  tile.append(header, note, progressBar(percent, tone || 'unknown'));
  if (extra) tile.append(extra);
  return tile;
}

function cpuStealRow(state, stale) {
  const value = stale ? null : state.cpu_steal_percent;
  const row = document.createElement('div');
  row.className = `steal-row tone-${value == null ? 'unknown' : stealTone(value)}`;
  const label = document.createElement('span');
  label.textContent = 'CPU steal · 等待宿主机 CPU';
  label.title = '虚拟机等待宿主机 CPU 的时间占比';
  const amount = document.createElement('strong');
  amount.textContent = value == null ? '—' : pct(value);
  row.append(label, amount, progressBar(value, value == null ? 'unknown' : stealTone(value)));
  return row;
}

function pricePeriod(value) {
  return ({once: '一次性', monthly: '/ 月', quarterly: '/ 季', semiannual: '/ 半年', yearly: '/ 年'})[value] || '';
}

function calendarDaysRemaining(date, timezone = 'UTC') {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date || '')) return '';
  const [year, month, day] = date.split('-').map(Number);
  let todayParts;
  try {
    const parts = new Intl.DateTimeFormat('en-CA', {timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit'}).formatToParts(new Date());
    const values = Object.fromEntries(parts.map(part => [part.type, part.value]));
    todayParts = [Number(values.year), Number(values.month), Number(values.day)];
  } catch (error) {
    const today = new Date();
    todayParts = [today.getUTCFullYear(), today.getUTCMonth() + 1, today.getUTCDate()];
  }
  return Math.ceil((Date.UTC(year, month - 1, day) - Date.UTC(todayParts[0], todayParts[1] - 1, todayParts[2])) / 86400000);
}

function calendarCountdown(date, timezone = 'UTC') {
  const remaining = calendarDaysRemaining(date, timezone);
  if (remaining === '') return '';
  if (remaining < 0) return `已过期 ${Math.abs(remaining)} 天`;
  if (remaining === 0) return '今天';
  if (remaining < 60) return `${remaining} 天`;
  const months = Math.floor(remaining / 30);
  return months < 24 ? `约 ${months} 个月` : `约 ${(remaining / 365).toFixed(1)} 年`;
}

function dateInTimezone(timestamp, timezone = 'UTC') {
  try {
    return new Date(Number(timestamp)).toLocaleDateString(undefined, {timeZone: timezone});
  } catch (error) {
    return new Date(Number(timestamp)).toLocaleDateString(undefined, {timeZone: 'UTC'});
  }
}

function planFact(label, value, detail = '') {
  const item = document.createElement('div');
  const name = document.createElement('span');
  name.textContent = label;
  const strong = document.createElement('strong');
  strong.textContent = value;
  item.append(name, strong);
  if (detail) {
    const small = document.createElement('small');
    small.textContent = detail;
    item.append(small);
  }
  return item;
}

function compactPrice(plan) {
  return CardMetrics.price(plan);
}

function planDeadline(plan) {
  if (!plan) return {label: '未设续费日', tone: ''};
  const date = plan.renewal_date || plan.expiry_date;
  if (!date) return {label: '未设续费日', tone: ''};
  const days = calendarDaysRemaining(date, plan.timezone || 'UTC');
  const isRenewal = Boolean(plan.renewal_date);
  let label;
  if (days < 0) label = isRenewal ? `续费日已过 ${Math.abs(days)} 天` : `已到期 ${Math.abs(days)} 天`;
  else if (days === 0) label = isRenewal ? '续费日已到' : '今天到期';
  else label = isRenewal ? `${days} 天后续费` : `${days} 天后到期`;
  return {
    label,
    tone: days <= 0 ? 'danger' : days <= 7 ? 'warn' : '',
  };
}

function detailFact(label, value) {
  const item = document.createElement('div');
  const name = document.createElement('span');
  name.textContent = label;
  const content = document.createElement('strong');
  content.textContent = value || '—';
  item.append(name, content);
  return item;
}

function setReadableText(element, value, accessibleValue = value) {
  setCardText(element, value);
  element.title = value;
  element.setAttribute('aria-label', accessibleValue);
}

function countryMark(code, source) {
  const mark = document.createElement('span');
  mark.className = 'country-mark';
  const normalized = /^[A-Z]{2}$/.test(code || '') ? code : '';
  if (normalized) {
    mark.dataset.countryCode = normalized;
    const sourceLabel = source === 'manual' ? '手动覆盖' : source === 'lookup' ? 'Agent 主动重取' : 'Agent 自动识别';
    mark.title = `${normalized} · ${sourceLabel}`;
    mark.setAttribute('aria-label', `出站国家或地区 ${normalized}，${sourceLabel}`);
    const flag = document.createElement('img');
    flag.className = 'country-flag';
    flag.src = `/vendor/flag-icons/4x3/${normalized.toLowerCase()}.svg`;
    flag.alt = '';
    flag.setAttribute('aria-hidden', 'true');
    flag.addEventListener('error', () => {
      mark.replaceChildren('🌐');
      mark.removeAttribute('data-country-code');
      mark.title = '出站国家/地区未知';
      mark.setAttribute('aria-label', '出站国家或地区未知');
    }, {once: true});
    mark.append(flag);
  } else {
    mark.textContent = '🌐';
    mark.title = '出站国家/地区未知';
    mark.setAttribute('aria-label', '出站国家或地区未知');
  }
  return mark;
}

async function verifyCountryRequest(agent, button, feedback) {
  const agentID = agent.agent_id;
  if (countryWriteControllers.has(agentID) || agentState.blocked(agentID)) return;
  const controller = new AbortController(); countryWriteControllers.set(agentID, controller);
  button.disabled = true; feedback.textContent = '正在核实服务器上的单次识别记录…';
  let offerNew = false;
  try {
    const committed = await readAgentDetail(agentID, controller.signal);
    if (!committed || controller.signal.aborted || agentState.blocked(agentID)) return;
    agent = agents.get(agentID);
    const operation = agent?.country_code_lookup_operation;
    if (operation && operation.operation_id !== uncertainCountryRequests.get(agentID)) {
      uncertainCountryRequests.delete(agentID); agentActionErrors.delete(`${agentID}/country`);
    } else {
      offerNew = true;
      agentActionErrors.set(`${agentID}/country`, '服务器尚未确认前请求；无法断言失败。可再次核实，或明确确认发起新的单次识别。');
    }
  } catch (error) {
    if (!controller.signal.aborted && !agentState.blocked(agentID)) agentActionErrors.set(`${agentID}/country`, '核实失败，前请求仍不确定；不会自动重新提交。');
  } finally {
    if (countryWriteControllers.get(agentID) === controller) countryWriteControllers.delete(agentID);
    render();
  }
  if (offerNew && !controller.signal.aborted) await refreshCountryCodeLookup(agent, button, feedback, true);
}

async function refreshCountryCodeLookup(agent, button, feedback, newAfterVerification = false) {
  agent = agents.get(agent.agent_id);
  if (!agent || !mutationCSRFToken || !agent.management?.country_code_lookup || !agent.online
    || agent.disabled_at || agent.revoked || (uncertainCountryRequests.has(agent.agent_id) && !newAfterVerification)
    || ['requested', 'delivered'].includes(agent.country_code_lookup_operation?.status)) return;
  if (!await DialogShell.confirm({title:'重新识别出口地区',device:agent.name,trigger:button,message:newAfterVerification
    ? '前请求结果仍不确定，服务器未见新的操作记录；是否明确发起一个新的单次识别？这不是原请求重试，前请求可能仍执行；服务器会拒绝已有待执行操作。取消可继续核实。'
    : 'Agent 将向 ipwho.is 查询国家/地区，只回传两字母代码；查询失败时保留当前值。是否继续？',confirmText:'重新识别'})) return;
  const mutation = agentState.beginMutation(agent.agent_id, ['country']);
  if (!mutation) return;
  const controller = new AbortController(); countryWriteControllers.set(agent.agent_id, controller);
  agentActionErrors.delete(`${agent.agent_id}/country`);
  button.disabled = true;
  button.textContent = '已触发…';
  feedback.textContent = '';
  try {
    const operation = await Management.write(`/api/v1/web/agents/${encodeURIComponent(agent.agent_id)}/country-code/refresh`, {
      csrf: mutationCSRFToken, payload: {}, signal: controller.signal, unauthorized: () => location.assign('/login'),
    });
    if (!finishResource(mutation, {country_code_lookup_operation: operation})) return;
    uncertainCountryRequests.delete(agent.agent_id);
    render();
  } catch (error) {
    if (controller.signal.aborted || agentState.blocked(agent.agent_id) || !agents.has(agent.agent_id)) return;
    if (!error.status || error.status >= 500) uncertainCountryRequests.set(agent.agent_id, agent.country_code_lookup_operation?.operation_id || '');
    button.textContent = '重新识别一次';
    button.disabled = false;
    feedback.textContent = uncertainCountryRequests.has(agent.agent_id)
      ? '提交结果不确定；不会自动重试。请刷新状态核实，当前国家/地区保留。'
      : error.message || '国家/地区识别请求失败';
    agentActionErrors.set(`${agent.agent_id}/country`, error.message || '国家/地区识别请求失败');
  } finally {
    if (countryWriteControllers.get(agent.agent_id) === controller) countryWriteControllers.delete(agent.agent_id);
    finishResource(mutation);
    reconciliation.notify(agent.agent_id);
    render();
  }
}

async function readMutationError(response) {
  try {
    const body = await response.json();
    return body.error?.message || body.message || response.statusText;
  } catch (error) {
    return response.statusText;
  }
}

async function saveAgentName(agent, input, saveButton) {
  const name = input.value.trim();
  if (!Management.validName(input.value)) {
    input.setCustomValidity('名称不能为空，最多 100 UTF-8 字节，且不能包含换行或 NUL');
    input.reportValidity();
    return;
  }
  input.setCustomValidity('');
  const mutation = agentState.beginMutation(agent.agent_id, ['name']);
  if (!mutation) return;
  const draft = input.value, editGeneration = agentNameEditGeneration.get(agent.agent_id);
  const controller = new AbortController();
  nameWriteControllers.set(agent.agent_id, controller);
  saveButton.disabled = true;
  try {
    const updated = await Management.write(`/api/v1/web/agents/${encodeURIComponent(agent.agent_id)}/name`, {
      method: 'PUT', csrf: mutationCSRFToken, payload: {name}, signal: controller.signal,
      unauthorized: () => location.assign('/login'),
    });
    if (!finishResource(mutation, {name: updated.name})) return;
    if (agentNameDrafts.get(agent.agent_id) === draft && agentNameEditGeneration.get(agent.agent_id) === editGeneration) {
      editingAgentNames.delete(agent.agent_id);
      agentNameDrafts.delete(agent.agent_id);
      agentNameErrors.delete(agent.agent_id);
    }
    render();
  } catch (error) {
    if (controller.signal.aborted || agentState.blocked(agent.agent_id) || !agents.has(agent.agent_id)
      || !editingAgentNames.has(agent.agent_id) || agentNameEditGeneration.get(agent.agent_id) !== editGeneration
      || agentNameDrafts.get(agent.agent_id) !== draft) return;
    agentNameErrors.set(agent.agent_id, error.message || '名称保存失败');
    input.setCustomValidity(error.message || '名称保存失败');
    input.reportValidity();
    saveButton.disabled = false;
  } finally {
    if (nameWriteControllers.get(agent.agent_id) === controller) nameWriteControllers.delete(agent.agent_id);
    finishResource(mutation);
    if (!controller.signal.aborted && agents.has(agent.agent_id) && !agentState.blocked(agent.agent_id)) render();
    if (!editingAgentNames.has(agent.agent_id) && agentNameEditGeneration.get(agent.agent_id)===editGeneration && document.activeElement===document.body)
      container.querySelector(`[data-agent-id="${agent.agent_id}"] .agent-name-edit`)?.focus({preventScroll:true});
  }
}

function agentNameControl(agent, state) {
  if (!editingAgentNames.has(agent.agent_id)) {
    const group = document.createElement('span'); group.className = 'agent-name-display';
    const heading = document.createElement('h2');
    setReadableText(heading, agent.name || state.hostname || '未命名 VPS'); heading.tabIndex=0;
    const edit = document.createElement('button');
    edit.type = 'button'; edit.className = 'agent-name-edit'; edit.textContent = '✎'; edit.title = '编辑名称'; edit.setAttribute('aria-label', '编辑 Agent 名称');
    edit.dataset.focusKey='name-edit';
    edit.disabled = !mutationCSRFToken || agent.revoked || agentState.blocked(agent.agent_id);
    edit.addEventListener('click', () => {
      editingAgentNames.add(agent.agent_id); agentNameDrafts.set(agent.agent_id, agents.get(agent.agent_id)?.name || '');
      agentNameEditGeneration.set(agent.agent_id, (agentNameEditGeneration.get(agent.agent_id) || 0) + 1);
      agentNameErrors.delete(agent.agent_id); render();
      container.querySelector(`[data-agent-id="${agent.agent_id}"] [data-focus-key="name-input"]`)?.focus({preventScroll: true});
    });
    const fullName=document.createElement('span');fullName.className='name-tooltip';fullName.textContent=heading.textContent;fullName.setAttribute('aria-hidden','true');
    group.append(heading, edit, fullName);
    return group;
  }
  const group = document.createElement('span'); group.className = 'agent-name-editor';
  const input = document.createElement('input');
  input.value = agentNameDrafts.get(agent.agent_id) ?? agent.name ?? '';
  input.maxLength = 100; input.setAttribute('aria-label', 'Agent 名称'); input.dataset.focusKey = 'name-input';
  input.setCustomValidity(agentNameErrors.get(agent.agent_id) || '');
  const save = document.createElement('button'); save.type = 'button'; save.textContent = '保存';
  save.disabled = agentState.busy(agent.agent_id, 'name') || agentState.blocked(agent.agent_id);
  const cancel = document.createElement('button'); cancel.type = 'button'; cancel.textContent = '取消';
  input.addEventListener('input', () => { agentNameDrafts.set(agent.agent_id, input.value); agentNameErrors.delete(agent.agent_id); });
  save.addEventListener('click', () => saveAgentName(agent, input, save));
  cancel.addEventListener('click', () => {
    agentNameEditGeneration.set(agent.agent_id, (agentNameEditGeneration.get(agent.agent_id) || 0) + 1);
    nameWriteControllers.get(agent.agent_id)?.abort();
    editingAgentNames.delete(agent.agent_id); agentNameDrafts.delete(agent.agent_id); agentNameErrors.delete(agent.agent_id); render();
    container.querySelector(`[data-agent-id="${agent.agent_id}"] .agent-name-edit`)?.focus({preventScroll:true});
  });
  input.addEventListener('keydown', event => {
    if (event.key === 'Enter') { event.preventDefault(); save.click(); }
    if (event.key === 'Escape') { event.preventDefault(); cancel.click(); }
  });
  group.append(input, save, cancel);
  return group;
}

function countryCodeLookupControls(agent) {
  const controls = document.createElement('div');
  controls.className = 'card-detail-actions country-lookup-controls';
  const button = document.createElement('button');
  button.type = 'button';
  const status = document.createElement('span');
  status.setAttribute('role', 'status');
  status.setAttribute('aria-live', 'polite');
  function update(next) {
    agent = next;
  let statusText = '';
  const operation = agent.country_code_lookup_operation;
  if (operation && uncertainCountryRequests.has(agent.agent_id)
    && operation.operation_id !== uncertainCountryRequests.get(agent.agent_id)) {
    uncertainCountryRequests.delete(agent.agent_id); agentActionErrors.delete(`${agent.agent_id}/country`);
  }
  const capable = Boolean(agent.management?.country_code_lookup);
  const pending = operation?.status === 'requested' || operation?.status === 'delivered';
  setCardText(button, !capable ? '升级 Agent 后可重新识别' : pending ? '识别中…' : uncertainCountryRequests.has(agent.agent_id) ? '核实前次识别请求' : '重新识别');
  button.disabled = !mutationCSRFToken || !capable || !agent.online || Boolean(agent.disabled_at) || agent.revoked || pending || countryWriteControllers.has(agent.agent_id) || agentState.busy(agent.agent_id, 'country') || agentState.blocked(agent.agent_id);
  button.title = !capable ? '当前 Agent 版本不支持主动重取，需要升级' : !agent.online ? 'Agent 离线时不能触发' : pending ? '本次请求只会触发一次，等待结果或状态超时' : '手动触发一次 HTTPS 国家/地区识别';
  if (!capable) statusText = '需要升级 Agent 后才能主动重新识别。';
  else if (agent.revoked || agentState.blocked(agent.agent_id)) statusText = '设备已撤销或正在删除，不能触发识别。';
  else if (agent.disabled_at) statusText = '设备已暂停，不能触发识别。';
  else if (!agent.online) statusText = '设备离线，不能触发识别。';
  else if (operation?.status === 'requested') statusText = '已排队，等待 Agent 领取。';
  else if (operation?.status === 'delivered') statusText = 'Agent 已领取，等待一次性查询结果。';
  else if (operation?.status === 'unknown') statusText = '上次结果待核实；当前显示值未变。重新触发会创建新的单次请求。';
  else if (operation?.status === 'failed') statusText = '上次识别失败，已保留此前国家/地区。';
  else if (operation?.status === 'succeeded') statusText = `上次识别成功${operation.result_code ? `：${operation.result_code}` : ''}。`;
  else statusText = '不会自动查询；只有手动触发时才发起一次识别。';
  if (agent.plan?.country_code_override) statusText += ' 当前启用了手动覆盖，识别结果不会改变当前显示。';
  if (agentActionErrors.has(`${agent.agent_id}/country`)) statusText = agentActionErrors.get(`${agent.agent_id}/country`);
  if (uncertainCountryRequests.has(agent.agent_id)) statusText += ' 提交结果不确定，不会自动重试；请刷新状态核实，当前值保留。';
    setCardText(status, statusText);status.title=statusText;controls.title=statusText;button.title=statusText;status.dataset.attention=String(operation?.status==='failed'||agentActionErrors.has(`${agent.agent_id}/country`));
  }
  update(agent);
  button.addEventListener('click', () => uncertainCountryRequests.has(agent.agent_id)
    ? verifyCountryRequest(agent, button, status) : refreshCountryCodeLookup(agent, button, status));
  controls.append(button, status);
  controls.update = update;
  return controls;
}

const agentCards = new Map();

function updateDisplay(current, next) {
  if (current.nodeType !== next.nodeType || current.nodeName !== next.nodeName) {
    current.replaceWith(next); return next;
  }
  if (current.nodeType === Node.TEXT_NODE) {
    if (current.data !== next.data) current.data = next.data;
    return current;
  }
  for (const attribute of [...current.attributes]) {
    if (!next.hasAttribute(attribute.name)) current.removeAttribute(attribute.name);
  }
  for (const attribute of next.attributes) {
    if (current.getAttribute(attribute.name) !== attribute.value) current.setAttribute(attribute.name, attribute.value);
  }
  const children = [...current.childNodes], incoming = [...next.childNodes];
  for (let i = 0; i < incoming.length; i++) {
    if (children[i]) updateDisplay(children[i], incoming[i]);
    else current.append(incoming[i]);
  }
  for (let i = incoming.length; i < children.length; i++) children[i].remove();
  return current;
}

function setCardText(node, value) {
  if (node.textContent !== value) node.textContent = value;
}

const renderedCardKeys=new Map();
const dormantCards=new Map();
function cardRenderKey(a){return JSON.stringify([a.name,a.online,a.revoked,a.disabled_at,a.country_code,a.country_source,a.state,a.plan,a.traffic,a.google_status,a.outbounds,a.upgrade,a.updater_health,a.masked_egress,a.tag_ids,(a.tag_ids||[]).map(id=>dashboardData.catalog().tags.find(t=>t.id===id)?.name),mutationCSRFToken,serverBuild.version,serverBuild.upgrade_eligible]);}
let renderFrame=0,lastLayout='';
let renderAll=false;
const renderDirty=new Set();
const groupViews=new Map();
function render(ids){if(!ids)renderAll=true;else for(const id of ids)renderDirty.add(id);if(renderFrame)return;renderFrame=requestAnimationFrame(()=>{renderFrame=0;const dirty=renderAll?null:new Set(renderDirty);renderAll=false;renderDirty.clear();renderDashboard(dirty);});}
function renderDashboard(dirty=null) {
  const deadline=performance.now()+8,deferred=new Set();let updated=0;
  const catalog=dashboardData.catalog(),model=dashboardUI.update(agents,catalog,serverBuild,overviewReady),prefs=dashboardUI.preferences.value;
  document.body.classList.toggle('dashboard-simple',model.simple);
  overview.update(agents,overviewReady,{summary:model.summary,catalog,server:serverBuild,simple:model.simple});
  const allIds=[...agents.keys()],mounted=new Set();
  for(const group of model.sections)if(!prefs.collapsed.includes(group.id))for(const a of group.items)mounted.add(a.agent_id);
  for(const [id,view]of agentCards)if(!mounted.has(id)){view.card.remove();agentCards.delete(id);dormantCards.set(id,view);}
  for(const id of dormantCards.keys())if(!agents.has(id)){dormantCards.delete(id);renderedCardKeys.delete(id);}
  networkQuality.sync(allIds,mounted,agents);latencyCharts.sync(allIds);trafficCharts.sync(allIds);
  for(const id of allIds)if(!mounted.has(id)){latencyCharts.setVisible(id,false);trafficCharts.setVisible(id,false);}
  cardObservations.sync(allIds);selectorView.sync(allIds);cardFaces.sync(allIds);
  for(const [id,view]of detailViews)if(!agents.has(id)){view.management.remove();view.overview.remove();detailViews.delete(id);}
  selectorView.refreshActive();
  empty.classList.toggle('hidden',model.list.length>0);empty.textContent=agents.size?'没有符合筛选的设备':'尚无活动 Agent';
  const liveGroups=new Set(model.sections.map(g=>g.id)),layout=JSON.stringify([prefs.sort,model.simple,model.sections.map(g=>[g.id,prefs.collapsed.includes(g.id),g.items.map(a=>a.agent_id)])]);
  for(const [id,view]of groupViews)if(!liveGroups.has(id)){view.node.remove();groupViews.delete(id);}
  let groupPosition=container.firstElementChild;
  for(const group of model.sections){
    let gv=groupViews.get(group.id);
    if(!gv){const node=document.createElement('section'),header=document.createElement('button'),body=document.createElement('div');node.className='dashboard-group';header.type='button';header.className='dashboard-group-header';header.addEventListener('click',()=>dashboardUI.collapse(group.id));body.className='dashboard-card-grid';node.append(header,body);gv={node,header,body};groupViews.set(group.id,gv);}
    const collapsed=prefs.collapsed.includes(group.id),counts={online:0,offline:0,paused:0,unknown:0};for(const a of group.items)counts[DashboardModel.lifecycle(a)]++;
    const status=counts.online===group.items.length?'全部在线':Object.entries(counts).filter(([,n])=>n).map(([key,n])=>DashboardModel.STATUS[key]+' '+n).join(' · ');
    const title=`${collapsed?'▸':'▾'}  ${group.name} · ${group.items.length} 台`,headerKey=title+status;if(gv.header.dataset.key!==headerKey){gv.header.dataset.key=headerKey;const name=document.createElement('span'),note=document.createElement('small');name.textContent=title;note.textContent=status;note.className=counts.online===group.items.length?'all-online':'';gv.header.replaceChildren(name,note);}gv.header.setAttribute('aria-expanded',String(!collapsed));gv.header.hidden=prefs.sort!=='group'||model.simple;gv.body.hidden=collapsed;
    if(layout!==lastLayout){if(gv.node!==groupPosition){if(gv.node.parentNode===container&&container.moveBefore)container.moveBefore(gv.node,groupPosition);else container.insertBefore(gv.node,groupPosition);}groupPosition=gv.node.nextElementSibling;}
    if(collapsed)continue;
    let position=gv.body.firstElementChild;
    for(const agent of group.items){let view=agentCards.get(agent.agent_id);const remount=!view;
      if(!view&&dormantCards.has(agent.agent_id)){view=dormantCards.get(agent.agent_id);dormantCards.delete(agent.agent_id);agentCards.set(agent.agent_id,view);}
      if(remount||dirty===null||dirty.has(agent.agent_id)){
       if(updated&&performance.now()>=deadline){deferred.add(agent.agent_id);if(!view)continue;}
       else{const key=cardRenderKey(agent);updated++;
       if(!view){view=createAgentCard(agent);agentCards.set(agent.agent_id,view);}else if(renderedCardKeys.get(agent.agent_id)!==key)view.update(agent);
       renderedCardKeys.set(agent.agent_id,key);
       }
      }
      if(layout!==lastLayout&&view.card!==position){if(view.card.parentNode===gv.body&&gv.body.moveBefore)gv.body.moveBefore(view.card,position);else gv.body.insertBefore(view.card,position);}position=view.card.nextElementSibling;
    }
  }
  if(!deferred.size)lastLayout=layout;else render(deferred);
  const open=deviceDetails.snapshot();if(open.open&&agents.has(open.id)){detailViews.get(open.id)?.update(agents.get(open.id));deviceDetails.update(agents.get(open.id));}
  deviceDetails.sync();planEditor.sync();planRenewal.sync();
  for(const id of new Set([...trafficResetRequests.keys(),...trafficResetFeedback.keys(),...trafficResetControllers.keys()]))if(!agents.has(id)){trafficResetControllers.get(id)?.abort();trafficResetControllers.delete(id);trafficResetRequests.delete(id);trafficResetFeedback.delete(id);}
}

function cardLifecycle(agent) {return agent.revoked?'revoked':agent.disabled_at?'paused':agent.online===true?'online':agent.online===false&&(agent.state||agent.last_seen)?'offline':'unknown';}
function cardUptimeLabel(agent) {return cardLifecycle(agent)==='offline'?'上次运行':'运行时长';}
function cardRuntimeText(agent, oldMetrics) {
  if (!agent.state) return '— · 等待首次上报';
  const label = cardLifecycle(agent)==='offline'?'上次运行':'运行';
  return `${label} ${duration(agent.state.uptime)}${oldMetrics?` · 旧数据 · 最后上报 ${seen(agent.last_seen)}`:''}`;
}

// The card shows the number of days (or hours) only; the full text, with the old-data note, is the tooltip.
function cardRuntimeShort(agent, oldMetrics) {
  if (!agent.state) return '等待首次上报';
  const seconds = Number(agent.state.uptime || 0), days = Math.floor(seconds / 86400), hours = Math.floor(seconds % 86400 / 3600);
  const label = cardLifecycle(agent) === 'offline' ? '上次运行' : '运行';
  return `${label} ${days ? days + ' 天' : hours + ' 小时'}${oldMetrics ? ' · 旧' : ''}`;
}

function resourceFactsFor(agent) {
  const s=agent?.state||{},old=!agent?.online||agent?.disabled_at||s.stale,prefix=old?'旧数据 · ':'';
  const observed=agent?cardObservations.observe(agent):{cpu:'unknown',io:'unknown',stale:true},stale=old||observed.stale;
  const notes=[CardMetrics.annotation('cpu',s.cpu_percent,observed.cpu,stale),CardMetrics.annotation('memory',s.ram_percent,CardMetrics.tone(s.ram_percent),stale),CardMetrics.annotation('disk',s.disk_percent,CardMetrics.tone(s.disk_percent),stale,observed.io)];
  const suffix=i=>[notes[i].capacity,notes[i].io].filter(Boolean).map(text=>' · '+text).join('');
  return [['CPU',`${prefix}${CardMetrics.pct(s.cpu_percent)} · ${s.cpu_cores||'未知'} 核 · steal ${CardMetrics.pct(s.cpu_steal_percent)}${suffix(0)}`],
    ['RAM',`${prefix}${CardMetrics.size(s.ram_used,false,true)} / ${CardMetrics.size(s.ram_total,false,true)} · Swap ${CardMetrics.size(s.swap_used,false,true)} / ${CardMetrics.size(s.swap_total,false,true)}${suffix(1)}`],
    ['DISK',`${prefix}${CardMetrics.size(s.disk_used,false,true)} / ${CardMetrics.size(s.disk_total,false,true)} · 最忙 I/O ${CardMetrics.pct(s.disk_busy_percent)} · 读 ${CardMetrics.size(s.disk_read_rate,true,true)} · 写 ${CardMetrics.size(s.disk_write_rate,true,true)}${suffix(2)}`]];
}

// Only address families that are currently usable are rendered; hidden ones leave no focusable element behind.
function createPublicEgress(agentID) {
  const egress=document.createElement('div');
  egress.className='public-egress';
  egress.setAttribute('aria-label','Agent 公网出站');
  const items=['ipv4','ipv6'].map((family,index)=>{
    const item=document.createElement('button');item.type='button';item.className='public-egress-item';item.dataset.family=family;
    item.textContent=index?'V6':'V4';
    const tip=document.createElement('div');tip.className='public-egress-tip';tip.id=`egress-${agentID}-${family}`;tip.setAttribute('role','tooltip');
    item.setAttribute('aria-describedby',tip.id);
    item.addEventListener('click',event=>event.stopPropagation());
    item.addEventListener('keydown',event=>{event.stopPropagation();if(event.key==='Escape'){item.blur();item.classList.add('tip-dismissed');}});
    item.addEventListener('blur',()=>item.classList.remove('tip-dismissed'));
    item.addEventListener('mouseleave',()=>item.classList.remove('tip-dismissed'));
    item.addEventListener('focus',()=>item.classList.remove('tip-dismissed'));
    item.addEventListener('mouseenter',()=>item.classList.remove('tip-dismissed'));
    return {family,item,tip};
  });
  let shownKey=null;
  function update(agent) {
    const p=agent.state?.public_egress;
    const lifecycle=Boolean(!agent.online||agent.disabled_at||agent.revoked||agent.state?.stale);
    const shown=[],families=[];
    for(const {family,item,tip} of items) {
      const raw=p?.[family],masked=agent.masked_egress?.[family],observation=raw?.checked_at?raw:masked,status=observation?.status||'unknown';
      const stale=Boolean(lifecycle||observation?.stale||!observation?.checked_at||!observation?.valid_until||Date.now()>=observation.valid_until);
      const available=status==='available'&&typeof observation?.ip==='string'&&observation.ip.length>0;
      if(!available||stale)continue;
      const at=new Date(observation.checked_at).toLocaleString();
      const mask=observation===masked?masked.ip:PublicEgressCheck.maskIP(observation.ip);
      const history=observation.last_success,historyIP=history&&(observation===masked?history.ip:PublicEgressCheck.maskIP(history.ip));
      const full=`${family==='ipv4'?'IPv4':'IPv6'} · 可用 ${mask} · ${at}${observation.reason==='verified'?' · 已验证':''}${history?' · 最后成功 '+historyIP+'（'+new Date(history.checked_at).toLocaleString()+'）':''}；Agent 实际网络路径的出站观测`;
      setCardText(tip,full);item.setAttribute('aria-label',full);
      item.dataset.status=status;item.dataset.stale='false';
      shown.push(item,tip);families.push(family);
    }
    const key=families.join();
    if(key!==shownKey){shownKey=key;egress.replaceChildren(...shown);}
  }
  return {element:egress,update};
}

function planActionBlocked(agent) {
  return !mutationCSRFToken || agent.revoked || agentState.busy(agent.agent_id, 'plan') || agentState.blocked(agent.agent_id) || planRenewal.unconfirmed(agent.agent_id);
}

const detailUpgradeTargets=new Map(),detailUpgradeReads=new Map();
async function loadDetailUpgradeTargets(id){
 if(detailUpgradeReads.has(id))return detailUpgradeReads.get(id);
 const task=(async()=>{try{const catalog=await readJSON(`/api/v1/web/agents/${encodeURIComponent(id)}/upgrade-targets`);if(agents.has(id)){detailUpgradeTargets.set(id,catalog.targets?.find(t=>t.supported)||catalog.targets?.[0]||null);render();}}catch{detailUpgradeTargets.set(id,null);}finally{detailUpgradeReads.delete(id);}})();detailUpgradeReads.set(id,task);return task;
}
async function saveDetailCountry(agent,code){
 const id=agent.agent_id;if(planActionBlocked(agent)||planDialog.open||planRenewal.unconfirmed(id))return;
 const mutation=agentState.beginMutation(id,['plan','country']);if(!mutation)return;
 try{const plan=await savePlanPayload(id,Management.planPayload(agents.get(id)?.plan,{country_code_override:code}));planRenewal.invalidate(id);finishPlanResource(mutation,{plan});}
 catch(error){agentActionErrors.set(`${id}/country`,error.message||'地区保存失败');}
 finally{finishPlanResource(mutation);reconciliation.notify(id);render();}
}

function createAgentCard(initialAgent) {
    let agent = initialAgent;
    const state = agent.state || {};
    const hasState = Boolean(agent.state);
    const metricsStale = Boolean(!agent.online || agent.disabled_at || state.stale);
    const observed = cardObservations.observe(agent);
    const oldMetrics = metricsStale || observed.stale;
    const lifecycleLabels = {online:'在线',offline:'离线',paused:'暂停',revoked:'已撤销',unknown:'状态未知'};
    const card = document.createElement('article');
    card.className = 'card agent-card c7';
    card.dataset.agentId = agent.agent_id;
    const head = document.createElement('div');
    head.className = 'card-head c7-head';
    const identity = document.createElement('div');
    identity.className = 'c7-id';
    const sub = document.createElement('div');
    sub.className = 'name c7-sub';
    const status = document.createElement('span');
    const stateName = cardLifecycle(agent);
    card.setAttribute('aria-label', `Agent ${agent.name || '未命名'}，${lifecycleLabels[stateName]}`);
    status.className = `status ${stateName}`;
    status.dataset.agentState = stateName;
    status.textContent = lifecycleLabels[stateName];
    // Online shows nothing and offline is the dimmed card; the other states are named in front of the system line.
    status.hidden = stateName === 'online' || stateName === 'offline';
    const system = CardMetrics.os(state.os);
    const logo = document.createElement('img');
    logo.src = `/vendor/card-icons/${system.icon}.svg`;
    logo.alt = '';
    logo.setAttribute('aria-hidden', 'true');
    const systemText = document.createElement('span');
    systemText.className = 'c7-system';
    setReadableText(systemText, system.text);
    const publicEgress = createPublicEgress(agent.agent_id);
    const egress = publicEgress.element, updateEgress = publicEgress.update;
    updateEgress(agent);
    const runtime = document.createElement('small');
    runtime.className = 'card-runtime c7-runtime';
    setReadableText(runtime, cardRuntimeShort(agent, oldMetrics), cardRuntimeText(agent, oldMetrics)); runtime.title = cardRuntimeText(agent, oldMetrics);
    sub.append(status, logo, systemText, egress, runtime);
    let nameControl = agentNameControl(agent, state);
    const mark = countryMark(agent.country_code, agent.country_source);
    identity.append(nameControl, sub);
    head.append(mark, identity);
    card.classList.add(`state-${stateName}`);
    card.append(head);
    const tags=document.createElement('div');tags.className='dashboard-card-tags';
    const updateTags=()=>{const catalog=dashboardData.catalog(),key=(agent.tag_ids||[]).map(id=>{const t=catalog.tags.find(t=>t.id===id);return t?[id,t.name]:null;}).filter(Boolean);const signature=JSON.stringify(key);if(tags.dataset.signature===signature)return;tags.dataset.signature=signature;tags.replaceChildren(...key.map(([,name])=>{const n=document.createElement('span');n.textContent=name;n.title=name;return n;}));tags.hidden=!key.length;};
    updateTags();card.append(tags);

    // ---- machine ----
    const machine = Card.machineFaces(document);
    machine.update(Card.machineView(agent, observed, oldMetrics));
    const machineModule = Card.createModule({document, key: 'machine', label: '机器', host: document.createElement('section'), a: machine.a, b: machine.b});

    // ---- traffic ----
    const trafficNode = trafficCharts.mount(agent);
    const quotaDisplay = plan => { try { return plan?.quota_value ? PlanUnits.display(plan.quota_value, plan.quota_unit) : null; } catch (_) { return null; } };
    const trafficFaces = Card.trafficFaces(document, {chart: trafficNode, onEditPlan: trigger => openPlanDialog(agent, trigger)});
    const updateTraffic = () => {
      const old = Boolean(!agent.online || agent.disabled_at || agent.state?.stale || cardObservations.observe(agent).stale);
      trafficFaces.update(Card.trafficView(agent, {old, quotaDisplay: quotaDisplay(agent.plan)}), Card.trafficDetail(agent));
      trafficFaces.emptyButton.disabled = planActionBlocked(agent);
    };
    const trafficModule = Card.createModule({document, key: 'traffic', label: '流量', host: document.createElement('section'), a: trafficFaces.a, b: trafficFaces.b,
      onFace: face => trafficCharts.setActive(agent.agent_id, face === 'B')});

    // ---- network ----
    const qualityNode = networkQuality.mount(agent);
    const latencyNode = latencyCharts.mount(agent);
    const networkModule = Card.createModule({document, key: 'network', label: '网络', host: qualityNode, a: networkQuality.faceNode(agent.agent_id), b: latencyNode,
      onFace: face => { networkQuality.setFace(agent.agent_id, face); latencyCharts.setActive(agent.agent_id, face === 'B'); }});
    updateTraffic();
    card.append(machineModule.node, trafficModule.node, qualityNode);

    // ---- which face each module shows; kept in this browser ----
    const modules = [machineModule, trafficModule, networkModule];
    // History/quality plots have their own data/config timers. Rebuilding their
    // SVG paths for a CPU report or a summary calibration is unnecessary.
    const chartKey=a=>JSON.stringify([a.name,a.online,a.revoked,a.disabled_at,a.removal,a.state?.hostname]);
    let renderedCharts=chartKey(agent);
    const remember = () => cardFaces.write(agent.agent_id, modules.map(m => m.face).join(''));
    const flip = document.createElement('button');
    flip.type = 'button'; flip.className = 'c7-flip dv-all'; flip.dataset.focusKey = 'dv-all';
    flip.innerHTML = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 8h14l-4-4"></path><path d="M20 16H6l4 4"></path></svg>';
    const describeFlip = () => {
      const faces = modules.map(m => m.face), allA = !faces.includes('B'), allB = !faces.includes('A');
      flip.title = allB ? '切换这张卡的三个模块为简要' : '切换这张卡的三个模块为详细';
      flip.setAttribute('aria-label', flip.title);
      flip.dataset.preset = allA ? 'A' : allB ? 'B' : 'mixed';
    };
    const initialFaces = cardFaces.read(agent.agent_id);
    modules.forEach((module, index) => module.set(initialFaces[index]));
    describeFlip();
    modules.forEach(module => Card.attachFlip(module.node, {toggle: () => { module.toggle(); remember(); describeFlip(); }}));
    flip.addEventListener('click', () => { const next = Card.flipAll(modules.map(m => m.face)); modules.forEach((module, index) => module.set(next[index])); remember(); describeFlip(); });
    head.append(flip);

    // ---- footer: YouTube and the exit choice, then the money ----
    const googleTools = document.createElement('div');
    googleTools.className = 'google-selector-tools c7-foot1';
    googleTools.append(googleStatusPanel(agent));
    let selectorTrigger = selectorView.trigger(agent);
    if (selectorTrigger) googleTools.append(selectorTrigger);
    card.append(googleTools);
    selectorView.refresh(agent.agent_id);

    const detailToggle = document.createElement('button');
    detailToggle.className = 'detail-toggle score-detail c7-detail';
    detailToggle.type = 'button';
    detailToggle.textContent = '详情';
    detailToggle.dataset.focusKey = 'manage';
    detailToggle.setAttribute('aria-haspopup', 'dialog');
    detailToggle.title = '设备详情';
    detailToggle.addEventListener('click', () => {planRenewal.close();deviceDetails.open(agent.agent_id,'overview',detailToggle);});
    head.append(detailToggle);

    const deadline = planDeadline(agent.plan);
    const editPlan = document.createElement('button');
    editPlan.className = 'edit-plan c7-money';
    editPlan.type = 'button';
    editPlan.dataset.focusKey = 'plan';
    const price = document.createElement('span');
    price.className = 'footer-price c7-money-main';
    price.textContent = compactPrice(agent.plan);
    const editHint = document.createElement('span');
    editHint.className = 'c7-money-side';
    editHint.textContent = agent.plan ? '编辑套餐 ✎' : '添加套餐 ✎';
    editPlan.title = agent.plan ? '编辑套餐' : '添加套餐';
    editPlan.append(price, editHint);
    editPlan.disabled = planActionBlocked(agent);
    editPlan.addEventListener('click', () => openPlanDialog(agent,editPlan));
    const renewPlan = document.createElement('button');
    renewPlan.type='button';renewPlan.className=`renew-plan c7-money ${deadline.tone}`;renewPlan.dataset.focusKey='renewal';
    renewPlan.setAttribute('aria-haspopup','dialog');
    const due = document.createElement('span');
    due.className = 'c7-money-main';
    due.textContent = deadline.label;
    const renewHint = document.createElement('span');
    renewHint.className = 'c7-money-side';
    renewHint.textContent = '标记已续费';
    renewPlan.append(due, renewHint);
    renewPlan.disabled=!mutationCSRFToken||agent.revoked||agentState.busy(agent.agent_id,'plan')||agentState.blocked(agent.agent_id);
    renewPlan.addEventListener('click',()=>openPlanRenewal(agent,renewPlan));
    renewPlan.title='记录本次已续费，需确认；此按钮不是已确认状态';renewPlan.setAttribute('aria-label','记录已续费');
    const actions = document.createElement('div');
    actions.className = 'card-actions c7-foot2';
    actions.append(editPlan, renewPlan);
    card.append(actions);
    const offline = document.createElement('div');
    offline.className = 'c7-offline';
    const offlinePill = document.createElement('span'); offlinePill.className = 'c7-offline-pill'; offlinePill.textContent = '离线';
    const offlineNote = document.createElement('span'); offlineNote.className = 'c7-offline-note';
    offline.append(offlinePill, offlineNote);
    const showOffline = () => { offline.hidden = cardLifecycle(agent) !== 'offline'; setCardText(offlineNote, '最后在线 ' + Card.agoText(agent.last_seen)); };
    showOffline();
    card.append(offline);

    return {card, modules, setFaces(value){const faces=Card.parseFaces(value);modules.forEach((module,index)=>module.set(faces[index]));remember();describeFlip();}, update(next) {
      agent = next;
      updateTags();
      const state = agent.state || {}, hasState = Boolean(agent.state);
      const metricsStale = Boolean(!agent.online || agent.disabled_at || state.stale);
      const observed = cardObservations.observe(agent), oldMetrics = metricsStale || observed.stale;
      const stateName = cardLifecycle(agent);
      card.className = `card agent-card c7 state-${stateName}`;
      card.setAttribute('aria-label', `Agent ${agent.name || '未命名'}，${lifecycleLabels[stateName]}`);
      status.className = `status ${stateName}`; status.dataset.agentState = stateName;
      setCardText(status, lifecycleLabels[stateName]);
      status.hidden = stateName === 'online' || stateName === 'offline';
      const system = CardMetrics.os(state.os);
      if (logo.getAttribute('src') !== `/vendor/card-icons/${system.icon}.svg`) logo.src = `/vendor/card-icons/${system.icon}.svg`;
      setReadableText(systemText, system.text);
      updateEgress(agent);
      updateDisplay(mark, countryMark(agent.country_code, agent.country_source));
      const editing = editingAgentNames.has(agent.agent_id);
      if (editing !== nameControl.classList.contains('agent-name-editor')) {
        const nextName = agentNameControl(agent, state); nameControl.replaceWith(nextName); nameControl = nextName;
      }
      if (editing) {
        const input = nameControl.querySelector('input');
        input.setCustomValidity(agentNameErrors.get(agent.agent_id) || '');
        nameControl.querySelector('button').disabled = agentState.busy(agent.agent_id, 'name') || agentState.blocked(agent.agent_id);
      } else {
        setReadableText(nameControl.querySelector('h2'), agent.name || state.hostname || '未命名 VPS');
        setCardText(nameControl.querySelector('.name-tooltip'), agent.name || state.hostname || '未命名 VPS');
        nameControl.querySelector('button').disabled = !mutationCSRFToken || agent.revoked || agentState.blocked(agent.agent_id);
      }
      setReadableText(runtime, cardRuntimeShort(agent, oldMetrics), cardRuntimeText(agent, oldMetrics)); runtime.title = cardRuntimeText(agent, oldMetrics);
      machine.update(Card.machineView(agent, observed, oldMetrics));
      updateTraffic();
      const charts=chartKey(agent);if(charts!==renderedCharts){renderedCharts=charts;trafficCharts.mount(agent);latencyCharts.mount(agent);networkQuality.mount(agent);}
      updateDisplay(googleTools.querySelector('.google-checks'), googleStatusPanel(agent));
      positionYouTubeTooltip();
      const nextTrigger = selectorView.trigger(agent);
      if (nextTrigger !== selectorTrigger) { selectorTrigger?.remove(); if (nextTrigger) googleTools.append(nextTrigger); selectorTrigger = nextTrigger; }
      selectorView.refresh(agent.agent_id);
      const deadline = planDeadline(agent.plan); renewPlan.className = `renew-plan c7-money ${deadline.tone}`; setCardText(due, deadline.label);
      setCardText(price, compactPrice(agent.plan));
      setCardText(editHint, agent.plan ? '编辑套餐 ✎' : '添加套餐 ✎'); editPlan.title = agent.plan ? '编辑套餐' : '添加套餐';
      editPlan.disabled = planActionBlocked(agent);
      renewPlan.disabled=!mutationCSRFToken||agent.revoked||agentState.busy(agent.agent_id,'plan')||agentState.blocked(agent.agent_id);
      showOffline();
    }};
}

const detailViews=new Map();
function detailsFor(id){
 if(!agents.has(id))return null;
 if(!detailViews.has(id))detailViews.set(id,createAgentDetails(agents.get(id)));
 const view=detailViews.get(id);view.update(agents.get(id));return view;
}
function createAgentDetails(initial){
 let agent=initial;
    const remove = document.createElement('button');
    remove.className = 'remove-agent';
    remove.dataset.agentId = agent.agent_id;
    remove.type = 'button';
    remove.textContent = '撤销…';
    remove.dataset.focusKey = 'remove';
    remove.disabled = !mutationCSRFToken || agentState.blocked(agent.agent_id);
    remove.addEventListener('click', () => openRemoveAgentDialog(agent));
    const stateAction = document.createElement('button');
    stateAction.className = 'agent-state-action';
    stateAction.dataset.agentId = agent.agent_id;
    stateAction.dataset.agentAction = agent.disabled_at ? 'enable' : 'disable';
    stateAction.type = 'button';
    stateAction.textContent = agent.disabled_at ? '恢复监控' : '暂停…';
    stateAction.dataset.focusKey = 'state';
    stateAction.disabled = !mutationCSRFToken || agent.revoked || agentState.busy(agent.agent_id, 'lifecycle') || agentState.blocked(agent.agent_id);
    stateAction.addEventListener('click', () => setAgentDisabled(agent, !agent.disabled_at, stateAction));
    const upgrade = document.createElement('button');
    upgrade.className = 'upgrade-agent';
    upgrade.type = 'button';
    upgrade.textContent = '升级…';
    upgrade.dataset.focusKey = 'upgrade';
    const newer = AgentState.compareStableVersions(agent.version, serverBuild.version) === -1;
    upgrade.disabled = !mutationCSRFToken || !agent.online || Boolean(agent.disabled_at) || !agent.upgrade_capable || !serverBuild.upgrade_eligible || (!newer && !agent.upgrade_v2) || Boolean(agent.upgrade && !['succeeded', 'failed', 'rolled_back'].includes(agent.upgrade.status)) || agentState.busy(agent.agent_id, 'upgrade') || agentState.blocked(agent.agent_id);
    upgrade.title = upgradeHint(agent);
    upgrade.addEventListener('click', () => openUpgradeAgentDialog(agent));
    const detailPanel = document.createElement('section');
    detailPanel.className = 'card-detail-panel';
    detailPanel.hidden = true;
    const historyLink = document.createElement('a');
    historyLink.href = `/history.html?id=${encodeURIComponent(agent.agent_id)}`;
    historyLink.textContent = '旧版历史页面（兼容入口）';
    historyLink.dataset.focusKey = 'history';
    const recheck = document.createElement('button');
    recheck.type = 'button';
    recheck.textContent = agent.google_status?.pending ? '检测中…' : '重新检测';
    recheck.dataset.focusKey = 'recheck';
    recheck.disabled = !mutationCSRFToken || !agent.online || Boolean(agent.disabled_at) || agent.revoked || !agent.google_status?.supported || agent.google_status?.pending || agentState.busy(agent.agent_id, 'google') || agentState.blocked(agent.agent_id);
    const checkFeedback = document.createElement('span');
    checkFeedback.setAttribute('aria-live', 'polite');
    checkFeedback.textContent = agentActionErrors.get(`${agent.agent_id}/google`) || '';
    recheck.addEventListener('click', () => rerunGoogleStatus(agent, recheck, checkFeedback));
    const management = document.createElement('div');
    management.className = 'agent-actions';
    if (!agent.revoked) management.append(upgrade, stateAction);
    management.append(remove);
    const resetTraffic = document.createElement('button');
    resetTraffic.type = 'button'; resetTraffic.dataset.focusKey = 'traffic-reset';
    resetTraffic.textContent = trafficResetRequests.has(agent.agent_id) ? '核实前次请求' : '重新开始…';
    resetTraffic.disabled = !canResetTraffic(agent);
    resetTraffic.addEventListener('click', () => resetAccumulatedTraffic(agent.agent_id));
    const resetFeedback = document.createElement('span');resetFeedback.setAttribute('aria-live','polite');
    resetFeedback.textContent = trafficResetFeedback.get(agent.agent_id) || '';
    const countryControls = countryCodeLookupControls(agent);
    // Public IP detection lives in the same panel as the other device management actions.
    const egressPanel = PublicEgressCheck.createPanel({
      agentId: agent.agent_id, csrf: () => mutationCSRFToken, idPrefix: 'detail-egress',
      begin: () => agentState.beginMutation(agent.agent_id, ['egress']),
      finish: (token, patch) => finishResource(token, patch),
    });
    egressPanel.update(agent);


    const splitEgress=egressPanel.split();
    const summary=DetailSummary.create({actions:{upgrade,pause:stateAction,remove,reset:resetTraffic,resetFeedback,google:recheck,googleFeedback:checkFeedback,history:historyLink},egress:splitEgress,country:countryControls,
      edit:(a,n,mode)=>openPlanDialog(a,n,mode),renew:(a,n)=>openPlanRenewal(a,n),saveCountry:saveDetailCountry,canWrite:a=>!planActionBlocked(a),organization:(a,n)=>organizationDialogs.openAssignment(a.agent_id,n),organizationText:a=>{const c=dashboardData.catalog();return [c.groups.find(g=>g.id===a.group_id)?.name||'未分组',...(a.tag_ids||[]).map(id=>c.tags.find(t=>t.id===id)?.name).filter(Boolean)].join(' · ');}});
    detailPanel.replaceChildren(summary.management);summary.update(agent,detailUpgradeTargets.get(agent.agent_id),serverBuild);

 return {management:detailPanel,overview:summary.overview,egressPanel,update(next){
  agent=next;egressPanel.update(agent);
      const newer = AgentState.compareStableVersions(agent.version, serverBuild.version) === -1;
      remove.disabled = !mutationCSRFToken || agentState.blocked(agent.agent_id);
      stateAction.dataset.agentAction = agent.disabled_at ? 'enable' : 'disable';
      setCardText(stateAction, agent.disabled_at ? '恢复监控' : '暂停…');
      stateAction.disabled = !mutationCSRFToken || agent.revoked || agentState.busy(agent.agent_id, 'lifecycle') || agentState.blocked(agent.agent_id);
      upgrade.disabled = !mutationCSRFToken || !agent.online || Boolean(agent.disabled_at) || !agent.upgrade_capable || !serverBuild.upgrade_eligible || (!newer && !agent.upgrade_v2) || Boolean(agent.upgrade && !['succeeded', 'failed', 'rolled_back'].includes(agent.upgrade.status)) || agentState.busy(agent.agent_id, 'upgrade') || agentState.blocked(agent.agent_id);
      upgrade.title = upgradeHint(agent);
      setCardText(recheck, agent.google_status?.pending ? '检测中…' : '重新检测');
      recheck.disabled = !mutationCSRFToken || !agent.online || Boolean(agent.disabled_at) || agent.revoked || !agent.google_status?.supported || agent.google_status?.pending || agentState.busy(agent.agent_id, 'google') || agentState.blocked(agent.agent_id);
      setCardText(checkFeedback, agentActionErrors.get(`${agent.agent_id}/google`) || '');
      setCardText(resetTraffic, trafficResetRequests.has(agent.agent_id) ? '核实前次请求' : '重新开始…');
      resetTraffic.disabled = !canResetTraffic(agent);
      setCardText(resetFeedback, trafficResetFeedback.get(agent.agent_id) || '');

      countryControls.update(agent);summary.update(agent,detailUpgradeTargets.get(agent.agent_id),serverBuild);

 }};
}

function canResetTraffic(agent) {
  return Boolean(mutationCSRFToken && agent?.online && agent.state && !agent.state.stale && agent.traffic
    && !agent.disabled_at && !agent.revoked && !agentState.busy(agent.agent_id,'traffic') && !agentState.blocked(agent.agent_id)
    && !trafficResetControllers.has(agent.agent_id));
}
async function resetAccumulatedTraffic(id) {
  const agent=agents.get(id);if(!canResetTraffic(agent))return;
  const oldRequest=trafficResetRequests.get(id);
  if(!await DialogShell.confirm({title:'重新开始设备累计',device:agent.name,messageTitle:oldRequest?'原请求编号：'+oldRequest:'',message:oldRequest ? '核实上次的重新开始是否已生效，不会创建新的重置。继续？' : '只重新开始页面累计入站/出站统计；历史与套餐周期用量不会改变。继续？',confirmText:'确认重新开始',danger:true}))return;
  const mutation=agentState.beginMutation(id,['traffic']);if(!mutation)return;
  const verifying=Boolean(oldRequest),operationID=oldRequest||crypto.randomUUID().replaceAll('-','');trafficResetRequests.set(id,operationID);
  const controller=new AbortController();trafficResetControllers.set(id,controller);trafficResetFeedback.set(id,'正在记录新的累计起点…');render();
  try {
    const traffic=await Management.write(`/api/v1/web/agents/${encodeURIComponent(id)}/traffic/reset`,{
      csrf:mutationCSRFToken,payload:{request_id:operationID},signal:controller.signal,unauthorized:()=>location.assign('/login')});
    if(!finishResource(mutation,verifying?{}:{traffic}))return;
    trafficResetRequests.delete(id);trafficResetFeedback.set(id,verifying?'已确认原请求执行；正在核对当前累计，不使用旧回执覆盖新样本或后续起点。':'累计统计已从当前有效样本重新开始；套餐周期用量未改变。');
  } catch(error) {
    if(controller.signal.aborted||agentState.blocked(id)||!agents.has(id))return;
    const uncertain=!error.status||error.status>=500;if(!uncertain)trafficResetRequests.delete(id);
    trafficResetFeedback.set(id,uncertain?'结果未确认，保留原累计；可核实上次的重新开始是否已生效。':error.message||'无法重新开始累计统计');
  } finally {
    if(trafficResetControllers.get(id)===controller)trafficResetControllers.delete(id);
    agentState.finishMutation(mutation);reconciliation.notify(id);if(!suspended)render();
  }
}

function finishResource(token, patch = {}) {
  const committed = agentState.finishMutation(token, patch);
  if (committed) { reconciliation.notify(token.id); render(); }
  return committed;
}

async function hydrateDetails(records, context) {
  const pending = records;
  let next = 0;
  const worker = async () => {
    while (next < pending.length) {
      const record = pending[next++];
      const results = await Promise.allSettled([readAgentDetail(record.agent_id, context?.controller.signal, context),
        readAgentUpgrade(record.agent_id, context)]);
      if (results.some(result => result.status === 'rejected') && (!context || agentState.validCycle(context))) {
        stream.textContent = '部分资料读取失败'; stream.classList.remove('live');
      }
    }
  };
  const workers = Array.from({length: Math.min(8, pending.length)}, worker);
  await Promise.all(workers);
}

async function readAgentDetail(id, signal, context = null) {
  const ticket = agentState.read(id, context);
  const eventAtStart = pendingTelemetry.get(id);
  try {
    const detail = await readJSON(`/api/v1/web/agents/${encodeURIComponent(id)}`, signal);
    if (signal?.aborted) return false;
    const committed = agentState.commitDetail(ticket, {...detail, detail_loaded: true});
    if (committed) {
      const pending = pendingTelemetry.get(id);
      if (pending) {
        pendingTelemetry.delete(id);
        if (pending !== eventAtStart) agentState.event(pending);
      }
      render(new Set([id]));
    }
    return committed;
  } catch (error) {
    if (signal?.aborted || error.name === 'AbortError') return false;
    if (error.status === 404) {
      if (agentState.missing(ticket)) { pendingTelemetry.delete(id); reconciliation.cancel(id); }
      return false;
    }
    throw error;
  }
}

async function readAgentUpgrade(id, context, signal) {
  const ticket = agentState.read(id, context, ['upgrade']);
  try {
    const upgrade = await readOptionalJSON(`/api/v1/web/agents/${encodeURIComponent(id)}/upgrade`, context?.controller.signal||signal);
    if (agentState.commitRead(ticket, {upgrade})) render();
  } catch (error) {
    if (!context?.controller.signal.aborted && error.name !== 'AbortError' && error.status !== 404) throw error;
  }
}

const pendingTelemetry = new Map();
const detailJobs=new Map();
function hydrateActiveDetail(id){
 if(!agents.has(id)||detailJobs.has(id)||suspended)return detailJobs.get(id)?.task;
 const controller=new AbortController();
 const task=Promise.allSettled([readAgentDetail(id,controller.signal),readAgentUpgrade(id,null,controller.signal)]).finally(()=>detailJobs.delete(id));
 detailJobs.set(id,{controller,task});return task;
}
const dashboardData=DashboardData.create({state:agentState,read:readJSON,onchange:ids=>{overviewReady=true;render(ids);},onsuccess:()=>{if(events.readyState===EventSource.OPEN){stream.textContent='实时连接';stream.classList.add('live');}},onerror:()=>{stream.textContent='部分资料读取失败';stream.classList.remove('live');}});
const reconciliation={
 notify(id){dashboardData.notify(id);if(deviceDetails.snapshot().open&&deviceDetails.snapshot().id===id)hydrateActiveDetail(id);},
 retry(){dashboardData.retry();},cancel(id){detailJobs.get(id)?.controller.abort();detailJobs.delete(id);},
 close(){for(const id of detailJobs.keys())this.cancel(id);dashboardData.close();}
};
function refresh(){return dashboardData.refresh();}
const organizationAPI=OrganizationAPI.create({csrf:()=>mutationCSRFToken,read:readJSON});
organizationDialogs=OrganizationDialogs.create({api:organizationAPI,read:readJSON,agents,state:agentState,refresh:()=>dashboardData.reload()});
expenseDialog=ExpenseDialog.create({api:organizationAPI,visibleIDs:()=>DashboardModel.select(agents,dashboardData.catalog(),dashboardUI.preferences.value,serverBuild).list.map(a=>a.agent_id),refresh:()=>dashboardData.reload()});


loadMutationSession();
refresh();
let suspended = false;
setInterval(() => { if (suspended||document.hidden) return; reconciliation.retry(); if (!agentState.refreshing()) refresh(); }, 15000);
document.addEventListener('visibilitychange',()=>{if(!suspended&&!document.hidden)dashboardData.reload();});
addEventListener('pagehide', () => { suspended = true; planRenewal.shutdown(); planEditor.shutdown(); deviceDetails.shutdown(); organizationDialogs.close(); expenseDialog.close(); dashboardUI.close(); networkQuality.close(); latencyCharts.close(); trafficCharts.close(); monitorCoordinator.close(); for (const controller of [...planWriteControllers.values(), ...nameWriteControllers.values(), ...countryWriteControllers.values(), ...trafficResetControllers.values()]) controller.abort(); trafficResetControllers.clear();trafficResetRequests.clear();trafficResetFeedback.clear(); selectorController.closeAll(); events.close(); reconciliation.close(); pendingTelemetry.clear(); agentState.beginRefresh().controller.abort(); });
// A bfcache restore must not retain closed reconciliation or a dead SSE stream.
addEventListener('pageshow', event => { if (event.persisted) location.reload(); });

const events = new EventSource('/api/v1/web/events');
let streamOpened=false;
events.onopen = () => {
  if (suspended) return;
  stream.textContent = '实时连接';
  stream.classList.add('live');
  if(streamOpened||!agentState.refreshing())refresh();streamOpened=true;
};
events.onerror = () => {
  stream.textContent = '正在重连';
  stream.classList.remove('live');
};
events.addEventListener('agent', event => {
 let update;try{update=JSON.parse(event.data);}catch{return;}
 const id=update.agent_id;if (revokedAgentIDs.has(update.agent_id)) return;
 const previous=agents.get(id),telemetry=!!previous&&!agentState.fullEvent(update)&&Object.keys(update).every(key=>['agent_id','state','last_seen','summary_version'].includes(key)||JSON.stringify(update[key])===JSON.stringify(previous[key]));
 if(agentState.fullEvent(update))agentState.invalidate(id,[...new Set(Object.keys(update).map(AgentState.resource))]);
 else if(agentState.event(update))render(new Set([id]));
 if(!telemetry)dashboardData.notify(id);
 else dashboardData.telemetry(id);
 if(deviceDetails.snapshot().open&&deviceDetails.snapshot().id===id&&agentState.fullEvent(update))hydrateActiveDetail(id);
});
events.addEventListener('dashboard',event=>{try{const value=JSON.parse(event.data);dashboardData.metadata(value);if(deviceDetails.snapshot().open&&deviceDetails.snapshot().id===value.agent_id&&!value.revoked)hydrateActiveDetail(value.agent_id);}catch{}});
events.addEventListener('organization',event=>{try{const value=JSON.parse(event.data);dashboardData.organization(value);}catch{}});
