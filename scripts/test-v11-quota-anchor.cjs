const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs');
const Management=require('../web/static/management.js'),{cases,bounds}=require('./lib/v11-quota-cases.cjs');
test('monthly reset day retains the nearest real date, including short months and leap/year boundaries',()=>{
 for(const [today,day,want] of [['2026-03-15',31,'2026-01-31'],['2026-03-15',30,'2026-01-30'],['2026-03-15',29,'2026-01-29'],['2024-03-15',29,'2024-02-29'],['2026-05-01',31,'2026-03-31'],['2026-12-01',31,'2026-10-31'],['2026-01-01',31,'2025-12-31'],['2026-11-02',30,'2026-10-30']])assert.equal(Management.monthlyQuotaAnchor(day,Date.parse(today+'T12:00:00Z'),'UTC'),want);
 for(const row of cases()){
  const today=Management.planDate(Date.parse(row.now),row.timezone),anchor=Date.parse(row.anchor+'T12:00:00Z'),end=Date.parse(today+'T12:00:00Z');
  assert.equal(Number(row.anchor.slice(-2)),row.day);assert(anchor<=end);
  for(let t=anchor+86400000;t<=end;t+=86400000)assert.notEqual(new Date(t).getUTCDate(),row.day,'a more recent valid anchor exists: '+JSON.stringify(row));
 }
});
test('actual frontend anchors produce 13 active Server cycles without reset-day drift',()=>{
 const input=cases(),rows=bounds(input);assert.equal(rows.length,118);
 for(const [i,row] of rows.entries()){assert.equal(row.anchor,input[i].anchor);assert.equal(row.starts.length,13);assert.equal(row.start,row.starts[0]);}
 if(process.env.V11_QUOTA_EVIDENCE)fs.writeFileSync(process.env.V11_QUOTA_EVIDENCE,JSON.stringify(rows,null,2)+'\n');
});
test('plan-local today crosses both directions and invalid timezone returns no replacement date',()=>{
 const now=Date.parse('2026-03-15T00:30:00Z');
 assert.equal(Management.planDate(now,'America/Los_Angeles'),'2026-03-14');assert.equal(Management.planDate(now,'Asia/Shanghai'),'2026-03-15');
 assert.equal(Management.planDate(Date.parse('2026-03-15T20:30:00Z'),'Asia/Shanghai'),'2026-03-16');
 assert.equal(Management.planDate(now,'Not/AZone'),'');assert.equal(Management.monthlyQuotaAnchor(31,now,'Not/AZone'),'');
});
