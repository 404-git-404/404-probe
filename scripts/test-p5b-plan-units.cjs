'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),U=require('../web/static/plan-units.js');
test('1 GiB and 1 TiB display exact decimal GB/TB',()=>{
 assert.deepEqual(U.display('1','GiB'),{value:'1.073741824',unit:'GB'});assert.deepEqual(U.display('1','TiB'),{value:'1.099511627776',unit:'TB'});
});
test('fractional-byte legacy values use backend half-up integer bytes before decimal display',()=>{
 assert.deepEqual(U.display('0.001','GiB'),{value:'0.001073742',unit:'GB'});assert.deepEqual(U.display('0.001','TiB'),{value:'0.001099511628',unit:'TB'});
 assert.deepEqual(U.display('1.001','GiB'),{value:'1.074815566',unit:'GB'});
});
test('legacy quota above JS safe integer bytes converts without a Number round trip',()=>{
 assert.deepEqual(U.display('8388608.001','GiB'),{value:'9007199.255814734',unit:'GB'});
 assert.equal(U.open({quota_value:'8388608.001',quota_unit:'GiB'}).wire.value,'8388608.001');
});
test('unchanged displayed legacy pair preserves original wire even with >3 displayed fraction digits',()=>{
 for(const [value,unit] of [['1','GiB'],['0.001','TiB'],['8388608.001','GiB']]){const s=U.open({quota_value:value,quota_unit:unit});const p=U.payload(s,{quota_value:s.shown.value,quota_unit:s.shown.unit,currency:'EUR',calibration_value:''},true);assert.equal(p.quota_value,value);assert.equal(p.quota_unit,unit);assert.equal(p.currency,'EUR');}
});
test('explicit new decimal quota/calibration validate existing precision and never inherit binary units',()=>{
 const s=U.open({quota_value:'1',quota_unit:'GiB'});assert.deepEqual(U.payload(s,{quota_value:'1.074',quota_unit:'GB',calibration_value:'0',calibration_unit:'GB'},true),{quota_value:'1.074',quota_unit:'GB',calibration_value:'0',calibration_unit:'GB'});
 assert.throws(()=>U.payload(s,{quota_value:'1.073741825',quota_unit:'GB'},true),/最多 3 位/);assert.throws(()=>U.payload(s,{quota_value:'2',quota_unit:'GB',calibration_value:'1',calibration_unit:'GiB'},true),/用量校准/);
});
test('quantity validation matches nonnegative, twelve integer digits, positive quota and MaxInt64 boundaries',()=>{
 for(const value of ['1e3','-1','01','1.0001','1000000000000'])assert.match(U.validate(value,'GB'),/最多/);
 assert.match(U.validate('0','GB'),/大于 0/);assert.equal(U.validate('0','GB',true),'');assert.equal(U.validate('9223372.036','TB'),'');assert.match(U.validate('9223372.037','TB'),/支持范围/);assert.equal(U.validate('0.001','GB'),'');
});
test('disabled traffic bypasses quantity validation for original app clearing; empty quota stays empty',()=>{
 const s=U.open({quota_value:'1',quota_unit:'GiB'}),p={quota_value:'nonsense',quota_unit:'GB',calibration_value:'nonsense'};assert.deepEqual(U.payload(s,p,false),p);assert.deepEqual(U.display('',''),{value:'',unit:'GB'});assert.deepEqual(U.payload(s,{quota_value:'',quota_unit:'GB'},true),{quota_value:'',quota_unit:'GB'});
});
