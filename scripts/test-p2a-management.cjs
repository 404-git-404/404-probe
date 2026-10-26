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


test('revoke device name confirmation preserves case and interior whitespace, permits paste and trim',()=>{
 assert.equal(require('../web/static/management.js').matchesDeviceName(' DediRock-LAX-1 ','DediRock-LAX-1'),true);
 for(const value of ['', 'dedirock-lax-1','DediRock-LAX-2'])assert.equal(require('../web/static/management.js').matchesDeviceName(value,'DediRock-LAX-1'),false);
 assert.equal(require('../web/static/management.js').matchesDeviceName('A B','A  B'),false);assert.equal(require('../web/static/management.js').matchesDeviceName('   ',' '),false);
});

test('split plan presentations preserve hidden fields, clear scope, unlimited and exact binary quota',()=>{
 const M=require('../web/static/management.js'),U=require('../web/static/plan-units.js');
 const old={traffic_mode:'tx',quota_value:'1.123',quota_unit:'TiB',cycle_kind:'days',cycle_count:37,cycle_anchor:'2026-01-31',timezone:'Pacific/Kiritimati',purchase_price:'19.95',purchase_price_period:'once',renewal_price:'2.9',renewal_price_period:'once',country_code_override:'JP'};
 const price=M.planSectionPayload(old,'plan',{...old,renewal_price:'6.85'});for(const key of ['traffic_mode','quota_value','quota_unit','cycle_kind','cycle_count','cycle_anchor','country_code_override','purchase_price_period'])assert.equal(price[key],old[key],key);
 const session=U.open(old),fields={...old,quota_value:session.shown.value,quota_unit:session.shown.unit};const quota=M.planSectionPayload(old,'quota',fields,{enabled:true,limited:true,quotaSession:session});for(const key of ['quota_value','quota_unit','purchase_price','purchase_price_period','renewal_price_period','timezone','country_code_override'])assert.equal(quota[key],old[key],key);
 const unlimited=M.planSectionPayload(old,'quota',fields,{enabled:true,limited:false,quotaSession:session});assert.equal(unlimited.quota_value,'');assert.equal(unlimited.quota_unit,'');assert.equal(unlimited.traffic_mode,'tx');assert.equal(unlimited.cycle_count,37);
 const cleared=M.planSectionPayload(old,'clear');for(const key of ['quota_value','quota_unit','cycle_kind','cycle_count','traffic_mode','timezone','country_code_override'])assert.equal(cleared[key],old[key],key);assert.equal(cleared.purchase_price,'');assert.equal(cleared.renewal_price,'');assert.ok(!('calibration_value' in cleared));
 const off=M.planSectionPayload(old,'quota',fields,{enabled:false});assert.equal(off.traffic_mode,'');assert.equal(off.cycle_count,0);assert.equal(off.purchase_price,'19.95');
});

test('short text dates keep calendar validation before a PUT',()=>{
 const M=require('../web/static/management.js');for(const value of ['2026-02-30','2026-13-01','0000-01-01','2026-2-01'])assert.throws(()=>M.planSectionPayload({},'plan',{renewal_date:value}),/有效日期/);
 assert.equal(M.planSectionPayload({},'plan',{renewal_date:'2024-02-29'}).renewal_date,'2024-02-29');
});
