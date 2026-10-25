// The card no longer judges anything (v1.0.7, R25): it checks the shape of the Server's v5/card answer and words it.
const {test}=require('node:test'),assert=require('node:assert/strict');
const C=require('../web/static/network-quality-core.js');
const block=(i,state='green',extra={})=>({start:1800000000000+i*180000,state,judged:6,slow:0,failed:0,unjudged:0,in_progress:false,partial:false,...extra});
const lane=(slot,extra={})=>({slot,tcp:{status:'active',condition:'',blocks:Array.from({length:20},(_,i)=>block(i)),red:{active:false}},icmp:{status:'active',condition:''},right:{kind:'icmp_loss',percent:3.3,numerator:12,denominator:360,samples:'ok'},...extra});
const answer=(over={})=>({now:1800000300000,eval_end:1800000240000,family:'ipv4',settings_revision:'7',config_revision:'12',threshold_ms:500,interval_ms:30000,block_ms:180000,window_start:1800000000000,lanes:C.slots.map(s=>lane(s)),alert_active:false,...over});

test('v5/card: a well-formed answer is adopted, a damaged one is refused',()=>{
 const card=C.normalizeCard(answer(),'ipv4');
 assert.equal(card.lanes.length,3);assert.equal(card.lanes[0].tcp.blocks.length,20);assert.equal(card.threshold_ms,500);
 assert.throws(()=>C.normalizeCard(answer(),'ipv6'),/地址族/);
 assert.throws(()=>C.normalizeCard(answer({lanes:[lane('telecom')]}),'ipv4'),/卡槽/);
 assert.throws(()=>C.normalizeCard(answer({lanes:C.slots.map(s=>lane(s,{tcp:{status:'active',blocks:[block(0)]}}))}),'ipv4'),/方块数量/);
 assert.throws(()=>C.normalizeCard(answer({lanes:C.slots.map(s=>lane(s,{tcp:{status:'active',blocks:Array.from({length:20},(_,i)=>block(i,'purple'))}}))}),'ipv4'),/方块/);
 assert.throws(()=>C.normalizeCard(answer({alert_active:'yes'}),'ipv4'));
 assert.throws(()=>C.normalizeCard(null,'ipv4'));
 // a lane that does not run carries no blocks
 const off=C.normalizeCard(answer({lanes:C.slots.map(s=>lane(s,{tcp:{status:'disabled',condition:''}}))}),'ipv4');
 assert.equal(off.lanes[0].tcp.blocks,null);
});

test('block text restates the Server numbers and nothing else',()=>{
 const red=C.blockView(block(2,'red',{judged:6,slow:3,failed:2,max_ms:1840,in_progress:true,partial:true}),{label:'上海电信',threshold:500});
 assert.equal(red.state,'red');
 assert.match(red.text,/上海电信 · .*持续（告警）/);assert.match(red.text,/慢 3 · 失败 2 · 最高约 1840 ms/);assert.match(red.text,/门槛|超过 500 ms/);
 assert.match(red.text,/数据仍在到达/);assert.match(red.text,/配置变更或缺测/);assert.doesNotMatch(red.text,/B 面曲线同一时间有对应标记|已持续/);
 assert.doesNotMatch(C.blockView(block(0)).text,/B 面|慢 =/);
 const gray=C.blockView(block(0,'gray',{judged:0}),{intervalMS:300000});
 assert.match(gray.text,/没有可用样本/);assert.match(gray.text,/检测间隔较长，部分方块没有样本/);
 assert.match(C.blockView(block(0,'gray',{judged:1})).text,/样本不足/);
});

test('the number at the right: ICMP loss, or TCP failure; TCP is never called loss',()=>{
 assert.deepEqual(C.rightView({kind:'icmp_loss',percent:0,numerator:0,denominator:360,samples:'ok'}),{text:'丢包 0%',tone:'muted',title:'近 1 小时 360 个 Echo，丢 0 个'});
 assert.equal(C.rightView({kind:'icmp_loss',percent:3.33,numerator:12,denominator:360,samples:'ok'}).text,'丢包 3.3%');
 assert.equal(C.rightView({kind:'icmp_loss',percent:3.33,numerator:12,denominator:360,samples:'ok'}).tone,'warn');
 assert.equal(C.rightView({kind:'tcp_failure',percent:12,numerator:15,denominator:120,samples:'ok'}).text,'失败 12%'.replace('12%','12.0%'));
 assert.equal(C.rightView({kind:'tcp_failure',percent:12,numerator:15,denominator:120,samples:'ok'}).tone,'crit');
 const few=C.rightView({kind:'tcp_failure',percent:0,numerator:0,denominator:8,samples:'few'});
 assert.match(few.text,/·少$/);assert.match(few.title,/样本较少/);
 for(const right of [{kind:'none',percent:null,numerator:0,denominator:0,samples:'none'},{kind:'icmp_loss',percent:null,numerator:0,denominator:0,samples:'none'}])assert.equal(C.rightView(right).text,'—');
});

test('what stands in place of the blocks',()=>{
 const l=(tcp,icmp={status:'disabled'})=>({tcp,icmp,right:{kind:'none'}});
 assert.equal(C.laneView(l({status:'active',blocks:[block(0)],red:true})).kind,'blocks');
 assert.equal(C.laneView(l({status:'active',blocks:[block(0)],dns_failing:true})).text,'解析失败');
 assert.match(C.laneView(l({status:'inactive'},{status:'active'})).text,/^未测 TCP/);
 assert.equal(C.laneView(l({status:'inactive'},{status:'disabled'})).text,'未启用');
 assert.equal(C.laneView(l({status:'disabled'})).text,'已关闭');
 assert.equal(C.laneView(l({status:'unset'})).text,'未配置目标');
 assert.equal(C.laneView(l({status:'no_ipv6'})).text,'本机无 IPv6');
 assert.equal(C.laneView(l({status:'target_abnormal',condition:'not_found'})).text,'目标异常');
 assert.equal(C.laneView(l({status:'device_inactive'})).text,'设备已撤销/暂停');
});

test('card label: region + carrier for a built-in slot, what the user typed for a custom one',()=>{
 const config={slots:[{slot:'telecom',kind:'builtin',region:'上海',isp:'telecom'},{slot:'unicom',kind:'custom',region:'Tokyo',isp:'NTT'},{slot:'mobile',kind:'builtin',region:'',isp:''}]};
 assert.equal(C.cardLabel(config,'telecom'),'上海电信');assert.equal(C.cardLabel(config,'unicom'),'Tokyo NTT');assert.equal(C.cardLabel(config,'mobile'),'卡槽 3（未标注）');
});

test('the slow threshold is a whole number of milliseconds from 50 to 3000',()=>{
 for(const [v,bad] of [['500',false],['50',false],['3000',false],['49',true],['3001',true],['12.5',true],['',true],['x',true]])assert.equal(C.thresholdHint({slow_threshold_ms:v})==='invalid_threshold',bad,v);
});

test('v3 detail ratios use exact Connect denominator; probe attempts remain separate',()=>{
 const r=C.ratio({connect_attempts:4,connect_successes:3,probe_attempts:6,probe_successes:3},'tcp');assert.equal(r.value,25);assert.equal(r.fraction,'1 / 4');
 assert.equal(C.ratio({connect_attempts:0,connect_successes:0},'tcp').value,null);
});
