const {test}=require('node:test'),assert=require('node:assert/strict');
const {audit}=require('./lib/v111-click-audit.cjs');
test('two-state theme changes on all 50 real mouse clicks under continuous SSE',async()=>{
 const previous=process.env.V111_CLICK_ONLY;process.env.V111_CLICK_ONLY='theme';
 try{const r=await audit({onResult:r=>console.log('THEME_CLICKS '+JSON.stringify(r))});assert.deepEqual(r.errors,[]);assert.equal(r.results.length,1);assert.equal(r.results[0].hits,50);assert.equal(r.results[0].effects,50);}finally{if(previous===undefined)delete process.env.V111_CLICK_ONLY;else process.env.V111_CLICK_ONLY=previous;}
});
