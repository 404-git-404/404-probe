const path=require('node:path'),cp=require('node:child_process');
const Management=require('../../web/static/management.js');
function cases(){
 const dates=['2026-01-01','2026-01-15','2026-01-31','2026-03-01','2026-03-15','2026-03-31','2026-02-01','2026-02-15','2026-02-28','2024-02-01','2024-02-15','2024-02-29','2026-05-01','2026-05-15','2026-05-31','2026-12-01','2026-12-15','2026-12-31','2026-11-02'];
 const rows=dates.flatMap(date=>[1,15,28,29,30,31].map(day=>({day,now:date+'T12:00:00Z',timezone:'UTC'})));
 for(const timezone of ['America/Los_Angeles','Asia/Shanghai'])for(const now of ['2026-03-15T00:30:00Z','2026-03-15T20:30:00Z'])rows.push({day:15,now,timezone});
 return rows.map(s=>({...s,anchor:Management.monthlyQuotaAnchor(s.day,Date.parse(s.now),s.timezone)}));
}
function bounds(rows){return JSON.parse(cp.execFileSync(process.env.V11_QUOTA_GO||'go',['run','./scripts/fixtures/quota-cycle-oracle'],{cwd:path.resolve(__dirname,'../..'),input:JSON.stringify(rows),encoding:'utf8',windowsHide:true,env:{...process.env,GOPROXY:'off'}}));}
module.exports={cases,bounds};
