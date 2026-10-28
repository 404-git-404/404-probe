const {test}=require('node:test'),assert=require('node:assert/strict');
const {compareStableVersions:compare}=require('../web/static/agent-state.js');
test('formal versions compare decimal components across 9/10 and uint64 boundaries',()=>{
 for(const [a,b] of [['v1.0.9','v1.0.10'],['v1.9.9','v1.10.0'],['v9.9.9','v10.0.0'],['v1.0.9007199254740992','v1.0.9007199254740993']]){
  assert.equal(compare(a,b),-1);assert.equal(compare(b,a),1);assert.equal(compare(b,b),0);
 }
 for(const value of ['dev','unknown','v1.0.10-dirty','v1.0.10-rc.1','v1.0.10-beta.1','v1.0.010','v01.0.10','v1.0.18446744073709551616','1.0.10',null,10]){
  assert.equal(compare(value,'v1.0.10'),null);assert.equal(compare('v1.0.10',value),null);
 }
});
