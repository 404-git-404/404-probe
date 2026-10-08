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
