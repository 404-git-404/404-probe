const {test}=require('node:test'),assert=require('node:assert/strict');
const C=require('../web/static/network-quality-core.js'),L=require('../web/static/latency-mini.js');
const lane=(state,failed=0,judged=100)=>({tcp:{status:'active',blocks:[{state:'red',judged,failed},{state,judged:0,failed:0}]},right:{kind:'icmp_loss',percent:100}});
test('R33 A uses only TCP judged blocks, never the ICMP fallback, and latest cell controls tone',()=>{
 assert.equal(C.tcpRightView(lane('green')).text,'失败 0%');
 assert.equal(C.tcpRightView(lane('green',3)).tone,'muted');
 assert.equal(C.tcpRightView(lane('yellow',3)).tone,'warn');
 assert.equal(C.tcpRightView(lane('red',100)).text,'失败 100%');
 assert.equal(C.tcpRightView(lane('gray',1,3)).text,'失败 33.3%');
 assert.equal(C.tcpRightView(lane('red',1,2)).text,'—');
 assert.equal(C.tcpRightView({tcp:{status:'inactive'},right:{kind:'icmp_loss',percent:100}}).text,'—');
});
test('R33 slider descriptions distinguish a full day and a cross-midnight partial range',()=>{
 const end=new Date(2026,9,25,9,8).getTime();
 assert.equal(L.rangeText(end-86400000,end),'近 24 小时');
 const partial=L.rangeText(end-12*3600000,end);
 assert.match(partial,/24/);assert.match(partial,/25/);
 assert.notEqual(partial,'21:08 – 09:08');
 assert.equal(L.rangeText(end-3600000,end),'08:08 – 09:08');
});
