const {test}=require('node:test'),assert=require('node:assert/strict');
const {upgradeReasonDisplay:display}=require('../web/static/agent-state.js');
const codes=['rc_agent_required','updater_unavailable','rc_updater_required','legacy_migration_required','agent_unavailable','version_unrecognized','already_latest','agent_newer_than_server','old_beta_migration_required','rc_server_agent_required','server_build_ineligible'];
const {upgradeButtonHint:hint}=require('../web/static/agent-state.js');
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
