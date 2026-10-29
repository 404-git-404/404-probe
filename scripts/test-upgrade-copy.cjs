const {test}=require('node:test'),assert=require('node:assert/strict');
const {upgradeReasonDisplay:display}=require('../web/static/agent-state.js');
const codes=['rc_agent_required','updater_unavailable','rc_updater_required','legacy_migration_required','agent_unavailable','version_unrecognized','already_latest','agent_newer_than_server','old_beta_migration_required','rc_server_agent_required','server_build_ineligible'];
const {upgradeButtonHint:hint}=require('../web/static/agent-state.js');
test('troubleshooting links bind formal clean Server versions and fall back for development builds',()=>{
 const {updaterTroubleshootingURL:url}=require('../web/static/agent-state.js');
 for(const [server,tag] of [[{version:'v1.0.10'},'v1.0.10'],[{version:'v1.0.10',dirty:true},'main'],[{version:'dev'},'main'],[{version:'v1.0.10-rc.1'},'main'],[null,'main']])assert.equal(url(server),`https://github.com/404-git-404/404-probe/blob/${tag}/docs/updater-troubleshooting.md`);
});
test('health reasons explain missing, unreachable, replaced and unsupported endpoints with legacy and unknown fallbacks',()=>{
 const {updaterHealthReason:code}=require('../web/static/agent-state.js');
 for(const [health,want] of [[{status:'missing',reason:'socket_missing'},'updater_socket_missing'],[{status:'missing',reason:'runtime_directory_missing'},'updater_directory_missing'],[{status:'unreachable'},'updater_unreachable'],[{status:'identity_mismatch'},'updater_identity_mismatch'],[{status:'unsupported'},'updater_unsupported']]){
  assert.equal(code(health),want);const text=display({reason_code:want});assert.match(text,/升级按钮已停用/);assert.equal(hint({version:'v1.0.9',online:true,upgrade_capable:false,updater_health:health},{version:'v1.0.10',upgrade_eligible:true}),text);
  if(['missing','unreachable'].includes(health.status)){assert.match(text,/先确认没有进行中的升级、恢复或移除/);assert.match(text,/只执行一次 sudo systemctl restart 404-probe-agent-updater/);assert.match(text,/仍不可用就停下/);assert.doesNotMatch(text,/重新安装/);}
 }
 for(const status of ['future','toString','__proto__'])assert.equal(code({status}),'updater_unavailable');
 assert.equal(code(null),'');assert.equal(code({status:'healthy'}),'');
});
test('button hints explain the actual disabled condition and the neutral enabled action',()=>{
 const agent={version:'v1.0.9',online:true,upgrade_capable:true},server={version:'v1.0.10',upgrade_eligible:true};
 assert.equal(hint(agent,server),'可从面板升级到与面板相同的版本');
 for(const [patch,code] of [[{version:'v1.0.10'},'already_latest'],[{version:'v1.0.11'},'agent_newer_than_server'],[{version:'unknown'},'version_unrecognized'],[{online:false},'agent_unavailable'],[{upgrade_capable:false},'updater_unavailable'],[{version:'v1.0.0',upgrade_capable:false},'legacy_migration_required'],[{version:'v1.0.3-beta.1'},'old_beta_migration_required']])assert.equal(hint({...agent,...patch},server),display({reason_code:code}));
 assert.equal(hint(agent,{...server,upgrade_eligible:false}),display({reason_code:'server_build_ineligible'}));
 for(const status of ['requested','claimed','installing','health_check'])assert.equal(hint({...agent,upgrade:{status}},server),'正在升级，请等待完成');
 assert.equal(hint(agent,server,true),'正在升级，请等待完成');
 for(const status of ['succeeded','failed','rolled_back'])assert.equal(hint({...agent,upgrade:{status}},server),'可从面板升级到与面板相同的版本');
});
test('all eleven stable reasons give a next step, retain unknown diagnostics, and avoid implementation jargon',()=>{
 for(const reason_code of codes){const text=display({reason_code,reason:'old diagnostic'});assert.notEqual(text,'old diagnostic');assert.ok(text.length>20);assert.doesNotMatch(text,/RC-aware|bootstrap|handoff|身份配置|Requested/);}
 assert.match(display({reason_code:'updater_unavailable'}),/不用重装.*原配置和历史/);
 assert.match(display({reason_code:'agent_newer_than_server'}),/不能从面板降级/);
 for(const reason_code of ['future_reason','toString','__proto__',''])assert.equal(display({reason_code,reason:'original diagnosis'}),'original diagnosis');
 assert.equal(display({reason:'old server diagnosis'}),'old server diagnosis');assert.equal(display(null),'');
});
