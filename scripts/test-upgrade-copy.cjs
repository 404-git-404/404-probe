const {test}=require('node:test'),assert=require('node:assert/strict');
const {upgradeReasonDisplay:display}=require('../web/static/agent-state.js');
const codes=['rc_agent_required','updater_unavailable','rc_updater_required','legacy_migration_required','agent_unavailable','target_not_newer','already_latest','agent_newer_than_server','old_beta_migration_required','rc_server_agent_required','server_build_ineligible'];
test('all eleven stable reasons give a next step, retain unknown diagnostics, and avoid implementation jargon',()=>{
 for(const reason_code of codes){const text=display({reason_code,reason:'old diagnostic'});assert.notEqual(text,'old diagnostic');assert.ok(text.length>20);assert.doesNotMatch(text,/RC-aware|bootstrap|handoff|身份配置|Requested/);}
 assert.match(display({reason_code:'updater_unavailable'}),/不用重装.*原配置和历史/);
 assert.match(display({reason_code:'agent_newer_than_server'}),/不能从面板降级/);
 for(const reason_code of ['future_reason','toString','__proto__',''])assert.equal(display({reason_code,reason:'original diagnosis'}),'original diagnosis');
 assert.equal(display({reason:'old server diagnosis'}),'old server diagnosis');assert.equal(display(null),'');
});
