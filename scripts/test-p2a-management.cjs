const test = require('node:test'), assert = require('node:assert/strict');
global.AgentState = require('../web/static/agent-state.js');
const Management = require('../web/static/management.js');
test('P2a production ISO catalog exactly matches protocol and code fallback retains missing current', () => {
  const source = require('node:fs').readFileSync(require('node:path').join(__dirname,'../internal/protocol/country.go'),'utf8');
  const codes = source.match(/isoAlpha2CountryCodes = " ([A-Z ]+) "/)[1].split(' ');
  assert.deepEqual(Management.countries({}).map(item=>item.code),codes);
  assert.equal(Management.matches('no match','ZZ',[])[0].code,'ZZ');
  assert.equal(Management.countries({})[0].zh,'AD');
});
test('P2a production write bounds hung success/error bodies and aborts without retry',async()=>{
  for(const ok of [true,false]) {
    let calls=0,signal;
    await assert.rejects(Management.write('/local',{payload:{},timeoutMs:15,fetcher:async(_,options)=>{
      calls++;signal=options.signal;return {ok,status:ok?200:500,json:()=>new Promise(()=>{})};
    }}),{name:'TimeoutError'});
    assert.equal(calls,1);assert.equal(signal.aborted,true);
  }
  const controller=new AbortController();controller.abort();let calls=0;
  await assert.rejects(Management.write('/local',{payload:{},signal:controller.signal,fetcher:()=>{calls++;}}),{name:'AbortError'});
  assert.equal(calls,0);
});

test('R09 country-only full plan payload preserves every hidden configuration and excludes operation calibration',()=>{
 const M=require('../web/static/management.js'),plan={traffic_mode:'rx',cycle_kind:'days',cycle_count:45,cycle_anchor:'2026-09-01',timezone:'Europe/Berlin',quota_value:'1.234',quota_unit:'TiB',purchase_price_period:'once',renewal_price_period:'once',currency:'USD',usage_bytes:123,calibration_value:'10',country_code_override:'US'};
 const p=M.planPayload(plan,{country_code_override:'JP'});for(const key of ['traffic_mode','cycle_kind','cycle_count','cycle_anchor','timezone','quota_value','quota_unit','purchase_price_period','renewal_price_period','currency'])assert.equal(p[key],plan[key]);assert.equal(p.country_code_override,'JP');assert.equal(p.calibration_value,undefined);assert.equal(p.usage_bytes,undefined);
});
